// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package pgwire

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/cockroach/pkg/col/coldata"
	"github.com/cockroachdb/cockroach/pkg/server/telemetry"
	"github.com/cockroachdb/cockroach/pkg/settings"
	"github.com/cockroachdb/cockroach/pkg/sql"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/colinfo"
	"github.com/cockroachdb/cockroach/pkg/sql/parser"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgcode"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgnotice"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgwirebase"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgwirecancel"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sessiondatapb"
	"github.com/cockroachdb/cockroach/pkg/sql/sqltelemetry"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/envutil"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/log/eventpb"
	"github.com/cockroachdb/cockroach/pkg/util/mon"
	"github.com/cockroachdb/cockroach/pkg/util/netutil"
	"github.com/cockroachdb/cockroach/pkg/util/ring"
	"github.com/cockroachdb/cockroach/pkg/util/timeutil"
	"github.com/cockroachdb/cockroach/pkg/util/tracing"
	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/logtags"
	"github.com/cockroachdb/redact"
	"github.com/lib/pq/oid"
	"go.opentelemetry.io/otel/attribute"
)

// conn implements a pgwire network connection (version 3 of the protocol,
// implemented by Postgres v7.4 and later). conn.serve() reads protocol
// messages, transforms them into commands that it pushes onto a StmtBuf (where
// they'll be picked up and executed by the connExecutor).
// The connExecutor produces results for the commands, which are delivered to
// the client through the sql.ClientComm interface, implemented by this conn
// (code is in command_result.go).
// conn 实现 pgwire 网络连接（协议的版本 3，由 Postgres v7.4 及更高版本实现）。
// conn.serve() 读取协议消息，将它们转换成命令，
// 然后推送到 StmtBuf（它们将被 connExecutor 拾取并执行）。
// connExecutor 为命令生成结果，这些结果通过 sql.ClientComm 接口传递给客户端，
// 由这个 conn 实现（代码在 command_result.go 中）。
type conn struct {
	conn net.Conn

	sessionArgs sql.SessionArgs
	metrics     *ServerMetrics

	// startTime is the time when the connection attempt was first received
	// by the server.
	// startTime 是服务器首次接收到连接尝试的时间。
	startTime time.Time

	// rd is a buffered reader consuming conn. All reads from conn go through
	// this.
	// rd 是一个使用 conn 的缓冲读取器。 所有来自 conn 的读取都经过这个。
	rd bufio.Reader

	// parser is used to avoid allocating a parser each time.
	// parser 用于避免每次都分配一个解析器。
	parser parser.Parser

	// stmtBuf is populated with commands queued for execution by this conn.
	// stmtBuf 填充了排队等待此 conn 执行的命令。
	stmtBuf sql.StmtBuf

	// res is used to avoid allocations in the conn's ClientComm implementation.
	// res 用于避免在 conn 的 ClientComm 实现中进行分配。
	res commandResult

	// err is an error, accessed atomically. It represents any error encountered
	// while accessing the underlying network connection. This can read via
	// GetErr() by anybody. If it is found to be != nil, the conn is no longer to
	// be used.
	// err 是一个错误，以原子方式访问。 它表示访问底层网络连接时遇到的任何错误。
	// 任何人都可以通过 GetErr() 读取它。 如果发现是!= nil，则不再使用该conn。
	err atomic.Value

	// writerState groups together all aspects of the write-side state of the
	// connection.
	// writerState 将连接的写入端状态的所有方面组合在一起。
	writerState struct {
		fi flushInfo
		// buf contains command results (rows, etc.) until they're flushed to the
		// network connection.
		// buf 包含命令结果（行等），直到它们被刷新到网络连接。
		buf    bytes.Buffer
		tagBuf [64]byte
	}

	readBuf    pgwirebase.ReadBuffer
	msgBuilder writeBuffer

	// vecsScratch is a scratch space used by bufferBatch.
	// vecsScratch 是 bufferBatch 使用的暂存空间。
	vecsScratch coldata.TypedVecs

	sv *settings.Values

	// alwaysLogAuthActivity is used force-enables logging of authn events.
	// alwaysLogAuthActivity 用于强制启用 authn 事件的日志记录。
	alwaysLogAuthActivity bool

	// afterReadMsgTestingKnob is called after reading every message.
	// afterReadMsgTestingKnob 在读取每条消息后被调用。
	afterReadMsgTestingKnob func(context.Context) error
}

// serveConn creates a conn that will serve the netConn. It returns once the
// network connection is closed.
// serveConn 创建一个将为 netConn 服务的连接。 一旦网络连接关闭，它就会返回。
//
// Internally, a connExecutor will be created to execute commands. Commands read
// from the network are buffered in a stmtBuf which is consumed by the
// connExecutor. The connExecutor produces results which are buffered and
// sometimes synchronously flushed to the network.
// 在内部，将创建一个 connExecutor 来执行命令。
// 从网络读取的命令缓冲在 stmtBuf 中，由 connExecutor 使用。
// connExecutor 产生的结果被缓冲，有时会同步刷新到网络。
//
// The reader goroutine (this one) outlives the connExecutor's goroutine (the
// "processor goroutine").
// 读取器 goroutine（这个）比 connExecutor 的 goroutine（“处理器 goroutine”）活得更久。
// However, they can both signal each other to stop. Here's how the different
// cases work:
// 但是，它们都可以互相发出停止信号。 以下是不同案例的工作方式：
// 1) The reader receives a ClientMsgTerminate protocol packet: the reader
// closes the stmtBuf and also cancels the command processing context. These
// actions will prompt the command processor to finish.
// 阅读器收到ClientMsgTerminate协议包：阅读器关闭stmtBuf，同时取消命令处理上下文。
// 这些操作将提示命令处理器完成。
// 2) The reader gets a read error from the network connection: like above, the
// reader closes the command processor.
// 阅读器从网络连接中收到读取错误：如上，阅读器关闭命令处理器。
// 3) The reader's context is canceled (happens when the server is draining but
// the connection was busy and hasn't quit yet): the reader notices the canceled
// context and, like above, closes the processor.
// 读取器的上下文被取消（发生在服务器耗尽但连接繁忙且尚未退出时）：
// 读取器注意到已取消的上下文，并像上面一样关闭处理器。
// 4) The processor encounters an error. This error can come from various fatal
// conditions encountered internally by the processor, or from a network
// communication error encountered while flushing results to the network.
// The processor will cancel the reader's context and terminate.
// Note that query processing errors are different; they don't cause the
// termination of the connection.
// 处理器遇到错误。 此错误可能来自处理器内部遇到的各种致命情况，
// 或者来自将结果刷新到网络时遇到的网络通信错误。
// 处理器将取消读取器的上下文并终止。 请注意，查询处理错误是不同的； 它们不会导致连接终止。
//
// Draining notes:
//
// The reader notices that the server is draining by polling the IsDraining
// closure passed to serveImpl. At that point, the reader delegates the
// responsibility of closing the connection to the statement processor: it will
// push a DrainRequest to the stmtBuf which signals the processor to quit ASAP.
// The processor will quit immediately upon seeing that command if it's not
// currently in a transaction. If it is in a transaction, it will wait until the
// first time a Sync command is processed outside of a transaction - the logic
// being that we want to stop when we're both outside transactions and outside
// batches.
// 读者通过轮询传递给 serveImpl 的 IsDraining 闭包注意到服务器正在耗尽。
// 那时，读者将关闭连接的责任委托给语句处理器：
// 它会将 DrainRequest 推送到 stmtBuf，这会通知处理器尽快退出。
// 如果当前不在事务中，处理器将在看到该命令后立即退出。
// 如果它在一个事务中，它将等到第一次在事务外处理 Sync 命令
// - 逻辑是我们希望在我们既在事务外又在批处理外时停止。
func (s *Server) serveConn(
	ctx context.Context,
	netConn net.Conn,
	sArgs sql.SessionArgs,
	reserved mon.BoundAccount,
	connStart time.Time,
	authOpt authOptions,
) {
	if log.V(2) {
		log.Infof(ctx, "new connection with options: %+v", sArgs)
	}

	c := newConn(netConn, sArgs, &s.metrics, connStart, &s.execCfg.Settings.SV)
	c.alwaysLogAuthActivity = alwaysLogAuthActivity || atomic.LoadInt32(&s.testingAuthLogEnabled) > 0
	if s.execCfg.PGWireTestingKnobs != nil {
		c.afterReadMsgTestingKnob = s.execCfg.PGWireTestingKnobs.AfterReadMsgTestingKnob
	}

	// Do the reading of commands from the network.
	c.serveImpl(ctx, s.IsDraining, s.SQLServer, reserved, authOpt)
}

// alwaysLogAuthActivity makes it possible to unconditionally enable
// authentication logging when cluster settings do not work reliably,
// e.g. in multi-tenant setups in v20.2. This override mechanism
// can be removed after all of CC is moved to use v21.1 or a version
// which supports cluster settings.
// alwaysLogAuthActivity 可以在集群设置无法可靠运行时无条件启用身份验证日志记录，
// 例如 在 v20.2 的多租户设置中。 在所有 CC 移动到使用 v21.1 或支持集群设置的版本后，
// 可以删除此覆盖机制。
var alwaysLogAuthActivity = envutil.EnvOrDefaultBool("COCKROACH_ALWAYS_LOG_AUTHN_EVENTS", false)

func newConn(
	netConn net.Conn,
	sArgs sql.SessionArgs,
	metrics *ServerMetrics,
	connStart time.Time,
	sv *settings.Values,
) *conn {
	c := &conn{
		conn:        netConn,
		sessionArgs: sArgs,
		metrics:     metrics,
		startTime:   connStart,
		rd:          *bufio.NewReader(netConn),
		sv:          sv,
		readBuf:     pgwirebase.MakeReadBuffer(pgwirebase.ReadBufferOptionWithClusterSettings(sv)),
	}
	c.stmtBuf.Init()
	c.res.released = true
	c.writerState.fi.buf = &c.writerState.buf
	c.writerState.fi.lastFlushed = -1
	c.msgBuilder.init(metrics.BytesOutCount)

	return c
}

func (c *conn) setErr(err error) {
	c.err.Store(err)
}

func (c *conn) GetErr() error {
	err := c.err.Load()
	if err != nil {
		return err.(error)
	}
	return nil
}

func (c *conn) sendError(ctx context.Context, execCfg *sql.ExecutorConfig, err error) error {
	// We could, but do not, report server-side network errors while
	// trying to send the client error. This is because clients that
	// receive error payload are highly correlated with clients
	// disconnecting abruptly.
	// 我们可以，但不能，在尝试发送客户端错误时报告服务器端网络错误。
	// 这是因为接收到错误负载的客户端与突然断开连接的客户端高度相关。
	_ /* err */ = writeErr(ctx, &execCfg.Settings.SV, err, &c.msgBuilder, c.conn)
	return err
}

func (c *conn) checkMaxConnections(ctx context.Context, sqlServer *sql.Server) error {
	if c.sessionArgs.IsSuperuser {
		// This user is a super user and is therefore not affected by connection limits.
		sqlServer.IncrementConnectionCount()
		return nil
	}

	maxNumConnectionsValue := maxNumConnections.Get(&sqlServer.GetExecutorConfig().Settings.SV)
	if maxNumConnectionsValue < 0 {
		// Unlimited connections are allowed.
		sqlServer.IncrementConnectionCount()
		return nil
	}
	if !sqlServer.IncrementConnectionCountIfLessThan(maxNumConnectionsValue) {
		return c.sendError(ctx, sqlServer.GetExecutorConfig(), errors.WithHintf(
			pgerror.New(pgcode.TooManyConnections, "sorry, too many clients already"),
			"the maximum number of allowed connections is %d and can be modified using the %s config key",
			maxNumConnectionsValue,
			maxNumConnections.Key(),
		))
	}
	return nil
}

func (c *conn) authLogEnabled() bool {
	return c.alwaysLogAuthActivity || logSessionAuth.Get(c.sv)
}

// maxRepeatedErrorCount is the number of times an error can be received
// while reading from the network connection before the server decides to give
// up and abort the connection.
// maxRepeatedErrorCount 是在服务器决定放弃并中止连接之前从网络连接读取时可以接收到错误的次数。
const maxRepeatedErrorCount = 1 << 15

// serveImpl continuously reads from the network connection and pushes execution
// instructions into a sql.StmtBuf, from where they'll be processed by a command
// "processor" goroutine (a connExecutor).
// serveImpl 不断地从网络连接中读取并将执行指令推送到 sql.StmtBuf 中，
// 它们将由命令“处理器”goroutine（一个 connExecutor）从那里进行处理。
// The method returns when the pgwire termination message is received, when
// network communication fails, when the server is draining or when ctx is
// canceled (which also happens when draining (but not from the get-go), and
// when the processor encounters a fatal error).
// 该方法在收到 pgwire 终止消息时返回，当网络通信失败时，
// 当服务器正在耗尽或 ctx 被取消时（在耗尽时也会发生（但不是从一开始就发生），
// 以及当处理器遇到 致命错误）。
//
// serveImpl always closes the network connection before returning.
// serveImpl 总是在返回之前关闭网络连接。
//
// sqlServer is used to create the command processor. As a special facility for
// tests, sqlServer can be nil, in which case the command processor and the
// write-side of the connection will not be created.
// sqlServer 用于创建命令处理器。 作为测试的特殊工具，sqlServer 可以为 nil，
// 在这种情况下将不会创建命令处理器和连接的写入端。
func (c *conn) serveImpl(
	ctx context.Context,
	draining func() bool,
	sqlServer *sql.Server,
	reserved mon.BoundAccount,
	authOpt authOptions,
) {
	defer func() { _ = c.conn.Close() }()

	if c.sessionArgs.User.IsRootUser() || c.sessionArgs.User.IsNodeUser() {
		ctx = logtags.AddTag(ctx, "user", redact.Safe(c.sessionArgs.User))
	} else {
		ctx = logtags.AddTag(ctx, "user", c.sessionArgs.User)
	}
	tracing.SpanFromContext(ctx).SetTag("user", attribute.StringValue(c.sessionArgs.User.Normalized()))

	inTestWithoutSQL := sqlServer == nil
	if !inTestWithoutSQL {
		sessionStart := timeutil.Now()
		defer func() {
			if c.authLogEnabled() {
				endTime := timeutil.Now()
				ev := &eventpb.ClientSessionEnd{
					CommonEventDetails:      eventpb.CommonEventDetails{Timestamp: endTime.UnixNano()},
					CommonConnectionDetails: authOpt.connDetails,
					Duration:                endTime.Sub(sessionStart).Nanoseconds(),
				}
				log.StructuredEvent(ctx, ev)
			}
		}()
	}

	// NOTE: We're going to write a few messages to the connection in this method,
	// for the handshake. After that, all writes are done async, in the
	// startWriter() goroutine.
	// 注意：我们将在此方法中向连接写入一些消息，用于握手。
	// 之后，所有写入都在 startWriter() goroutine 中异步完成。

	ctx, cancelConn := context.WithCancel(ctx)
	defer cancelConn() // This calms the linter that wants these callbacks to always be called.
	// 这使希望始终调用这些回调的 linter 平静下来。

	var sentDrainSignal bool
	// The net.Conn is switched to a conn that exits if the ctx is canceled.
	// 如果 ctx 被取消，net.Conn 将切换为退出的连接。
	c.conn = NewReadTimeoutConn(c.conn, func() error {
		// If the context was canceled, it's time to stop reading. Either a
		// higher-level server or the command processor have canceled us.
		// 如果上下文被取消，就该停止读取了。 更高级别的服务器或命令处理器已取消我们。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// If the server is draining, we'll let the processor know by pushing a
		// DrainRequest. This will make the processor quit whenever it finds a good
		// time.
		// 如果服务器正在耗尽，我们将通过推送 DrainRequest 通知处理器。
		// 这将使处理器在找到合适的时间时退出。
		if !sentDrainSignal && draining() {
			_ /* err */ = c.stmtBuf.Push(ctx, sql.DrainRequest{})
			sentDrainSignal = true
		}
		return nil
	})
	c.rd = *bufio.NewReader(c.conn)

	// the authPipe below logs authentication messages iff its auth
	// logger is non-nil. We define this here.
	// 下面的 authPipe 记录身份验证消息，前提是它的身份验证记录器是非零的。 我们在这里定义它。
	logAuthn := !inTestWithoutSQL && c.authLogEnabled()

	// We'll build an authPipe to communicate with the authentication process.
	// 我们将构建一个 authPipe 来与身份验证过程进行通信。
	authPipe := newAuthPipe(c, logAuthn, authOpt, c.sessionArgs.User)
	var authenticator authenticatorIO = authPipe

	// procCh is the channel on which we'll receive the termination signal from
	// the command processor.
	// procCh 是我们将从命令处理器接收终止信号的通道。
	var procCh <-chan error

	// We need a value for the unqualified int size here, but it is controlled
	// by a session variable, and this layer doesn't have access to the session
	// data. The callback below is called whenever default_int_size changes.
	// It happens in a different goroutine, so it has to be changed atomically.
	// 我们在这里需要一个非限定 int 大小的值，但它由会话变量控制，并且该层无权访问会话数据。
	// 只要 default_int_size 发生变化，就会调用下面的回调。
	// 它发生在不同的 goroutine 中，因此必须以原子方式更改它。
	var atomicUnqualifiedIntSize = new(int32)
	onDefaultIntSizeChange := func(newSize int32) {
		atomic.StoreInt32(atomicUnqualifiedIntSize, newSize)
	}

	if sqlServer != nil {
		// Spawn the command processing goroutine, which also handles connection
		// authentication). It will notify us when it's done through procCh, and
		// we'll also interact with the authentication process through ac.
		// 产生命令处理 goroutine，它也处理连接认证）。
		// 它会在完成时通过 procCh 通知我们，我们还将通过 ac 与身份验证过程进行交互。
		var ac AuthConn = authPipe
		procCh = c.processCommandsAsync(
			ctx,
			authOpt,
			ac,
			sqlServer,
			reserved,
			cancelConn,
			onDefaultIntSizeChange,
		)
	} else {
		// sqlServer == nil means we are in a local test. In this case
		// we only need the minimum to make pgx happy.
		var err error
		for param, value := range testingStatusReportParams {
			err = c.sendParamStatus(param, value)
			if err != nil {
				break
			}
		}
		if err != nil {
			reserved.Close(ctx)
			return
		}
		var ac AuthConn = authPipe
		// Simulate auth succeeding.
		ac.AuthOK(ctx)
		dummyCh := make(chan error)
		close(dummyCh)
		procCh = dummyCh

		if err := c.sendReadyForQuery(0 /* queryCancelKey */); err != nil {
			reserved.Close(ctx)
			return
		}
	}

	var terminateSeen bool
	var authDone, ignoreUntilSync bool
	var repeatedErrorCount int
	for {
		breakLoop, isSimpleQuery, err := func() (bool, bool, error) {
			typ, n, err := c.readBuf.ReadTypedMsg(&c.rd)
			c.metrics.BytesInCount.Inc(int64(n))
			if err == nil && c.afterReadMsgTestingKnob != nil {
				err = c.afterReadMsgTestingKnob(ctx)
			}
			isSimpleQuery := typ == pgwirebase.ClientMsgSimpleQuery
			if err != nil {
				if pgwirebase.IsMessageTooBigError(err) {
					log.VInfof(ctx, 1, "pgwire: found big error message; attempting to slurp bytes and return error: %s", err)

					// Slurp the remaining bytes.
					slurpN, slurpErr := c.readBuf.SlurpBytes(&c.rd, pgwirebase.GetMessageTooBigSize(err))
					c.metrics.BytesInCount.Inc(int64(slurpN))
					if slurpErr != nil {
						return false, isSimpleQuery, errors.Wrap(slurpErr, "pgwire: error slurping remaining bytes")
					}
				}

				// Write out the error over pgwire.
				if err := c.stmtBuf.Push(ctx, sql.SendError{Err: err}); err != nil {
					return false, isSimpleQuery, errors.New("pgwire: error writing too big error message to the client")
				}

				// If this is a simple query, we have to send the sync message back as
				// well.
				if isSimpleQuery {
					if err := c.stmtBuf.Push(ctx, sql.Sync{}); err != nil {
						return false, isSimpleQuery, errors.New("pgwire: error writing sync to the client whilst message is too big")
					}
				}

				// We need to continue processing here for pgwire clients to be able to
				// successfully read the error message off pgwire.
				//
				// If break here, we terminate the connection. The client will instead see that
				// we terminated the connection prematurely (as opposed to seeing a ClientMsgTerminate
				// packet) and instead return a broken pipe or io.EOF error message.
				return false, isSimpleQuery, errors.Wrap(err, "pgwire: error reading input")
			}
			timeReceived := timeutil.Now()
			log.VEventf(ctx, 2, "pgwire: processing %s", typ)

			if ignoreUntilSync {
				if typ != pgwirebase.ClientMsgSync {
					log.VInfof(ctx, 1, "pgwire: skipping non-sync message after encountering error")
					return false, isSimpleQuery, nil
				}
				ignoreUntilSync = false
			}

			if !authDone {
				if typ == pgwirebase.ClientMsgPassword {
					var pwd []byte
					if pwd, err = c.readBuf.GetBytes(n - 4); err != nil {
						return false, isSimpleQuery, err
					}
					// Pass the data to the authenticator. This hopefully causes it to finish
					// authentication in the background and give us an intSizer when we loop
					// around.
					if err = authenticator.sendPwdData(pwd); err != nil {
						return false, isSimpleQuery, err
					}
					return false, isSimpleQuery, nil
				}
				// Wait for the auth result.
				if err = authenticator.authResult(); err != nil {
					// The error has already been sent to the client.
					return true, isSimpleQuery, nil //nolint:returnerrcheck
				}
				authDone = true
			}

			switch typ {
			case pgwirebase.ClientMsgPassword:
				// This messages are only acceptable during the auth phase, handled above.
				err = pgwirebase.NewProtocolViolationErrorf("unexpected authentication data")
				return true, isSimpleQuery, writeErr(
					ctx, &sqlServer.GetExecutorConfig().Settings.SV, err,
					&c.msgBuilder, &c.writerState.buf)
			case pgwirebase.ClientMsgSimpleQuery:
				if err = c.handleSimpleQuery(
					ctx, &c.readBuf, timeReceived, parser.NakedIntTypeFromDefaultIntSize(atomic.LoadInt32(atomicUnqualifiedIntSize)),
				); err != nil {
					return false, isSimpleQuery, err
				}
				return false, isSimpleQuery, c.stmtBuf.Push(ctx, sql.Sync{})

			case pgwirebase.ClientMsgExecute:
				// To support the 1PC txn fast path, we peek at the next command to
				// see if it is a Sync. This is because in the extended protocol, an
				// implicit transaction cannot commit until the Sync is seen. If there's
				// an error while peeking (for example, there are no bytes in the
				// buffer), the error is ignored since it will be handled on the next
				// loop iteration.
				// 为了支持 1PC txn 快速路径，我们查看下一个命令以查看它是否是 Sync。
				// 这是因为在扩展协议中，在看到 Sync 之前，隐式事务无法提交。
				// 如果在查看时出现错误（例如，缓冲区中没有字节），
				// 该错误将被忽略，因为它将在下一次循环迭代中处理。
				followedBySync := false
				if nextMsgType, err := c.rd.Peek(1); err == nil &&
					pgwirebase.ClientMessageType(nextMsgType[0]) == pgwirebase.ClientMsgSync {
					followedBySync = true
				}
				return false, isSimpleQuery, c.handleExecute(ctx, &c.readBuf, timeReceived, followedBySync)

			case pgwirebase.ClientMsgParse:
				return false, isSimpleQuery, c.handleParse(ctx, &c.readBuf, parser.NakedIntTypeFromDefaultIntSize(atomic.LoadInt32(atomicUnqualifiedIntSize)))

			case pgwirebase.ClientMsgDescribe:
				return false, isSimpleQuery, c.handleDescribe(ctx, &c.readBuf)

			case pgwirebase.ClientMsgBind:
				return false, isSimpleQuery, c.handleBind(ctx, &c.readBuf)

			case pgwirebase.ClientMsgClose:
				return false, isSimpleQuery, c.handleClose(ctx, &c.readBuf)

			case pgwirebase.ClientMsgTerminate:
				terminateSeen = true
				return true, isSimpleQuery, nil

			case pgwirebase.ClientMsgSync:
				// We're starting a batch here. If the client continues using the extended
				// protocol and encounters an error, everything until the next sync
				// message has to be skipped. See:
				// 我们在这里开始一批。 如果客户端继续使用扩展协议并遇到错误，
				// 则必须跳过下一个同步消息之前的所有内容。 看：
				// https://www.postgresql.org/docs/current/10/protocol-flow.html#PROTOCOL-FLOW-EXT-QUERY

				return false, isSimpleQuery, c.stmtBuf.Push(ctx, sql.Sync{})

			case pgwirebase.ClientMsgFlush:
				return false, isSimpleQuery, c.handleFlush(ctx)

			case pgwirebase.ClientMsgCopyData, pgwirebase.ClientMsgCopyDone, pgwirebase.ClientMsgCopyFail:
				// We're supposed to ignore these messages, per the protocol spec. This
				// state will happen when an error occurs on the server-side during a copy
				// operation: the server will send an error and a ready message back to
				// the client, and must then ignore further copy messages. See:
				// 根据协议规范，我们应该忽略这些消息。 当在复制操作期间服务器端发生错误时，
				// 将发生此状态：服务器将向客户端发送错误和就绪消息，然后必须忽略进一步的复制消息。 看：
				// https://github.com/postgres/postgres/blob/6e1dd2773eb60a6ab87b27b8d9391b756e904ac3/src/backend/tcop/postgres.c#L4295
				return false, isSimpleQuery, nil
			default:
				return false, isSimpleQuery, c.stmtBuf.Push(
					ctx,
					sql.SendError{Err: pgwirebase.NewUnrecognizedMsgTypeErr(typ)})
			}
		}()
		if err != nil {
			log.VEventf(ctx, 1, "pgwire: error processing message: %s", err)
			if !isSimpleQuery {
				// In the extended protocol, after seeing an error, we ignore all
				// messages until receiving a sync.
				ignoreUntilSync = true
			}
			repeatedErrorCount++
			// If we can't read data because of any one of the following conditions,
			// then we should break:
			// 1. the connection was closed.
			// 2. the context was canceled (e.g. during authentication).
			// 3. we reached an arbitrary threshold of repeated errors.
			if netutil.IsClosedConnection(err) ||
				errors.Is(err, context.Canceled) ||
				repeatedErrorCount > maxRepeatedErrorCount {
				break
			}
		} else {
			repeatedErrorCount = 0
		}
		if breakLoop {
			break
		}
	}

	// We're done reading data from the client, so make the communication
	// goroutine stop. Depending on what that goroutine is currently doing (or
	// blocked on), we cancel and close all the possible channels to make sure we
	// tickle it in the right way.
	// 我们已经完成从客户端读取数据，所以让通信 goroutine 停止。
	// 根据 goroutine 当前正在做什么（或被阻塞），
	// 我们取消并关闭所有可能的通道以确保我们以正确的方式触发它。

	// Signal command processing to stop. It might be the case that the processor
	// canceled our context and that's how we got here; in that case, this will
	// be a no-op.
	// 信号命令处理停止。 处理器可能取消了我们的上下文，这就是我们到达这里的方式；
	// 在那种情况下，这将是一个空操作。
	c.stmtBuf.Close()
	// Cancel the processor's context.
	cancelConn()
	// In case the authenticator is blocked on waiting for data from the client,
	// tell it that there's no more data coming. This is a no-op if authentication
	// was completed already.
	// 如果身份验证器在等待来自客户端的数据时被阻塞，请告诉它没有更多数据传来。
	// 如果身份验证已经完成，则这是一个空操作。
	authenticator.noMorePwdData()

	// Wait for the processor goroutine to finish, if it hasn't already. We're
	// ignoring the error we get from it, as we have no use for it. It might be a
	// connection error, or a context cancelation error case this goroutine is the
	// one that triggered the execution to stop.
	// 等待处理器 goroutine 完成，如果它还没有完成的话。
	// 我们忽略了从中得到的错误，因为我们对它没有用。
	// 它可能是连接错误，或者是上下文取消错误，这个 goroutine 是触发执行停止的 goroutine。
	<-procCh

	if terminateSeen {
		return
	}
	// If we're draining, let the client know by piling on an AdminShutdownError
	// and flushing the buffer.
	// 如果我们正在耗尽，通过堆积 AdminShutdownError 并刷新缓冲区让客户端知道。
	if draining() {
		// TODO(andrei): I think sending this extra error to the client if we also
		// sent another error for the last query (like a context canceled) is a bad
		// idea; see #22630. I think we should find a way to return the
		// AdminShutdown error as the only result of the query.
		log.Ops.Info(ctx, "closing existing connection while server is draining")
		_ /* err */ = writeErr(ctx, &sqlServer.GetExecutorConfig().Settings.SV,
			newAdminShutdownErr(ErrDrainingExistingConn), &c.msgBuilder, &c.writerState.buf)
		_ /* n */, _ /* err */ = c.writerState.buf.WriteTo(c.conn)
	}
}

// processCommandsAsync spawns a goroutine that authenticates the connection and
// then processes commands from c.stmtBuf.
// processCommandsAsync 生成一个 goroutine，该 goroutine 对连接进行身份验证，
// 然后处理来自 c.stmtBuf 的命令。
//
// It returns a channel that will be signaled when this goroutine is done.
// Whatever error is returned on that channel has already been written to the
// client connection, if applicable.
// 它返回一个通道，当这个 goroutine 完成时，该通道将发出信号。
// 如果适用，该通道上返回的任何错误都已写入客户端连接。
//
// If authentication fails, this goroutine finishes and, as always, cancelConn
// is called.
// 如果身份验证失败，则此 goroutine 结束，并且一如既往地调用 cancelConn。
//
// Args:
// ac: An interface used by the authentication process to receive password data
//   and to ultimately declare the authentication successful.
// ac: 身份验证过程用来接收密码数据并最终声明身份验证成功的接口。
// reserved: Reserved memory. This method takes ownership.
// 保留：保留内存。 此方法获取所有权。
// cancelConn: A function to be called when this goroutine exits. Its goal is to
//   cancel the connection's context, thus stopping the connection's goroutine.
//   The returned channel is also closed before this goroutine dies, but the
//   connection's goroutine is not expected to be reading from that channel
//   (instead, it's expected to always be monitoring the network connection).
// cancelConn：这个 goroutine 退出时调用的函数。
//   它的目标是取消连接的上下文，从而停止连接的 goroutine。
//   返回的通道也在这个 goroutine 死亡之前关闭，
//   但是连接的 goroutine 不应该从那个通道读取（相反，它应该始终监视网络连接）。
func (c *conn) processCommandsAsync(
	ctx context.Context,
	authOpt authOptions,
	ac AuthConn,
	sqlServer *sql.Server,
	reserved mon.BoundAccount,
	cancelConn func(),
	onDefaultIntSizeChange func(newSize int32),
) <-chan error {
	// reservedOwned is true while we own reserved, false when we pass ownership
	// away.
	// reservedOwned 在我们拥有保留时为真，在我们放弃所有权时为假。
	reservedOwned := true
	retCh := make(chan error, 1)
	go func() {
		var retErr error
		var connHandler sql.ConnectionHandler
		var authOK bool
		var connCloseAuthHandler func()
		defer func() {
			// Release resources, if we still own them.
			if reservedOwned {
				reserved.Close(ctx)
			}
			// Notify the connection's goroutine that we're terminating. The
			// connection might know already, as it might have triggered this
			// goroutine's finish, but it also might be us that we're triggering the
			// connection's death. This context cancelation serves to interrupt a
			// network read on the connection's goroutine.
			cancelConn()

			pgwireKnobs := sqlServer.GetExecutorConfig().PGWireTestingKnobs
			if pgwireKnobs != nil && pgwireKnobs.CatchPanics {
				if r := recover(); r != nil {
					// Catch the panic and return it to the client as an error.
					if err, ok := r.(error); ok {
						// Mask the cause but keep the details.
						retErr = errors.Handled(err)
					} else {
						retErr = errors.Newf("%+v", r)
					}
					retErr = pgerror.WithCandidateCode(retErr, pgcode.CrashShutdown)
					// Add a prefix. This also adds a stack trace.
					retErr = errors.Wrap(retErr, "caught fatal error")
					_ = writeErr(
						ctx, &sqlServer.GetExecutorConfig().Settings.SV, retErr,
						&c.msgBuilder, &c.writerState.buf)
					_ /* n */, _ /* err */ = c.writerState.buf.WriteTo(c.conn)
					c.stmtBuf.Close()
					// Send a ready for query to make sure the client can react.
					// TODO(andrei, jordan): Why are we sending this exactly?
					c.bufferReadyForQuery('I')
				}
			}
			if !authOK {
				ac.AuthFail(retErr)
			}
			if connCloseAuthHandler != nil {
				connCloseAuthHandler()
			}
			// Inform the connection goroutine of success or failure.
			retCh <- retErr
		}()

		// Authenticate the connection.
		// 验证连接。
		if connCloseAuthHandler, retErr = c.handleAuthentication(
			ctx, ac, authOpt, sqlServer.GetExecutorConfig(),
		); retErr != nil {
			// Auth failed or some other error.
			return
		}

		if retErr = c.checkMaxConnections(ctx, sqlServer); retErr != nil {
			return
		}
		defer sqlServer.DecrementConnectionCount()

		if retErr = c.authOKMessage(); retErr != nil {
			return
		}

		// Inform the client of the default session settings.
		// 通知客户端默认会话设置。
		connHandler, retErr = c.sendInitialConnData(ctx, sqlServer, onDefaultIntSizeChange)
		if retErr != nil {
			return
		}
		// Signal the connection was established to the authenticator.
		// 向身份验证器发出连接已建立的信号。
		ac.AuthOK(ctx)
		ac.LogAuthOK(ctx)

		// We count the connection establish latency until we are ready to
		// serve a SQL query. It includes the time it takes to authenticate and
		// send the initial ReadyForQuery message.
		// 我们计算连接建立延迟，直到我们准备好提供 SQL 查询。
		// 它包括验证和发送初始 ReadyForQuery 消息所需的时间。
		duration := timeutil.Since(c.startTime).Nanoseconds()
		c.metrics.ConnLatency.RecordValue(duration)

		// Mark the authentication as succeeded in case a panic
		// is thrown below and we need to report to the client
		// using the defer above.
		// 将身份验证标记为成功，以防下面抛出恐慌，我们需要使用上面的 defer 向客户端报告。
		authOK = true

		// Now actually process commands.
		reservedOwned = false // We're about to pass ownership away.
		// 我们即将放弃所有权。
		retErr = sqlServer.ServeConn(ctx, connHandler, reserved, cancelConn)
	}()
	return retCh
}

func (c *conn) sendParamStatus(param, value string) error {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgParameterStatus)
	c.msgBuilder.writeTerminatedString(param)
	c.msgBuilder.writeTerminatedString(value)
	return c.msgBuilder.finishMsg(c.conn)
}

func (c *conn) bufferParamStatus(param, value string) error {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgParameterStatus)
	c.msgBuilder.writeTerminatedString(param)
	c.msgBuilder.writeTerminatedString(value)
	return c.msgBuilder.finishMsg(&c.writerState.buf)
}

func (c *conn) bufferNotice(ctx context.Context, noticeErr pgnotice.Notice) error {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgNoticeResponse)
	return writeErrFields(ctx, c.sv, noticeErr, &c.msgBuilder, &c.writerState.buf)
}

func (c *conn) sendInitialConnData(
	ctx context.Context, sqlServer *sql.Server, onDefaultIntSizeChange func(newSize int32),
) (sql.ConnectionHandler, error) {
	connHandler, err := sqlServer.SetupConn(
		ctx,
		c.sessionArgs,
		&c.stmtBuf,
		c,
		c.metrics.SQLMemMetrics,
		onDefaultIntSizeChange,
	)
	if err != nil {
		_ /* err */ = writeErr(
			ctx, &sqlServer.GetExecutorConfig().Settings.SV, err, &c.msgBuilder, c.conn)
		return sql.ConnectionHandler{}, err
	}

	// Send the initial "status parameters" to the client.  This
	// overlaps partially with session variables. The client wants to
	// see the values that result from the combination of server-side
	// defaults with client-provided values.
	// 向客户端发送初始的“状态参数”。 这与会话变量部分重叠。
	// 客户端希望查看由服务器端默认值与客户端提供的值组合产生的值。
	// For details see: https://www.postgresql.org/docs/10/static/libpq-status.html
	for _, param := range statusReportParams {
		param := param
		value := connHandler.GetParamStatus(ctx, param)
		if err := c.sendParamStatus(param, value); err != nil {
			return sql.ConnectionHandler{}, err
		}
	}
	// The two following status parameters have no equivalent session
	// variable.
	if err := c.sendParamStatus("session_authorization", c.sessionArgs.User.Normalized()); err != nil {
		return sql.ConnectionHandler{}, err
	}

	if err := c.sendReadyForQuery(connHandler.GetQueryCancelKey()); err != nil {
		return sql.ConnectionHandler{}, err
	}
	return connHandler, nil
}

// sendReadyForQuery sends the final messages of the connection handshake.
// This includes a BackendKeyData message and a ServerMsgReady
// message indicating that there is no active transaction.
func (c *conn) sendReadyForQuery(queryCancelKey pgwirecancel.BackendKeyData) error {
	// Send our BackendKeyData to the client, so they can cancel the connection.
	c.msgBuilder.initMsg(pgwirebase.ServerMsgBackendKeyData)
	c.msgBuilder.putInt64(int64(queryCancelKey))
	if err := c.msgBuilder.finishMsg(c.conn); err != nil {
		return err
	}

	// An initial ServerMsgReady message is part of the handshake.
	c.msgBuilder.initMsg(pgwirebase.ServerMsgReady)
	c.msgBuilder.writeByte(byte(sql.IdleTxnBlock))
	if err := c.msgBuilder.finishMsg(c.conn); err != nil {
		return err
	}
	return nil
}

// An error is returned iff the statement buffer has been closed. In that case,
// the connection should be considered toast.
// 如果语句缓冲区已关闭，则返回错误。 在这种情况下，连接应该被视为 toast。
func (c *conn) handleSimpleQuery(
	ctx context.Context,
	buf *pgwirebase.ReadBuffer,
	timeReceived time.Time,
	unqualifiedIntSize *types.T,
) error {
	query, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}

	startParse := timeutil.Now()
	stmts, err := c.parser.ParseWithInt(query, unqualifiedIntSize)
	if err != nil {
		log.SqlExec.Errorf(ctx, "failed to parse simple query: %s", query)
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	endParse := timeutil.Now()

	if len(stmts) == 0 {
		return c.stmtBuf.Push(
			ctx, sql.ExecStmt{
				Statement:    parser.Statement{},
				TimeReceived: timeReceived,
				ParseStart:   startParse,
				ParseEnd:     endParse,
			})
	}

	for i := range stmts {
		// The CopyFrom statement is special. We need to detect it so we can hand
		// control of the connection, through the stmtBuf, to a copyMachine, and
		// block this network routine until control is passed back.
		// CopyFrom 语句比较特殊。 我们需要检测它，以便我们可以通过 stmtBuf
		// 将连接控制权交给 copyMachine，并阻止此网络例程，直到控制权传回。
		if cp, ok := stmts[i].AST.(*tree.CopyFrom); ok {
			if len(stmts) != 1 {
				// NOTE(andrei): I don't know if Postgres supports receiving a COPY
				// together with other statements in the "simple" protocol, but I'd
				// rather not worry about it since execution of COPY is special - it
				// takes control over the connection.
				// 我不知道 Postgres 是否支持接收 COPY 以及“简单”协议中的其他语句，
				// 但我不想担心它，因为 COPY 的执行是特殊的——它控制连接。
				return c.stmtBuf.Push(
					ctx,
					sql.SendError{
						Err: pgwirebase.NewProtocolViolationErrorf(
							"COPY together with other statements in a query string is not supported"),
					})
			}
			copyDone := sync.WaitGroup{}
			copyDone.Add(1)
			if err := c.stmtBuf.Push(ctx, sql.CopyIn{Conn: c, Stmt: cp, CopyDone: &copyDone}); err != nil {
				return err
			}
			copyDone.Wait()
			return nil
		}

		if err := c.stmtBuf.Push(
			ctx,
			sql.ExecStmt{
				Statement:    stmts[i],
				TimeReceived: timeReceived,
				ParseStart:   startParse,
				ParseEnd:     endParse,
				LastInBatch:  i == len(stmts)-1,
			}); err != nil {
			return err
		}
	}
	return nil
}

// An error is returned iff the statement buffer has been closed. In that case,
// the connection should be considered toast.
func (c *conn) handleParse(
	ctx context.Context, buf *pgwirebase.ReadBuffer, nakedIntSize *types.T,
) error {
	telemetry.Inc(sqltelemetry.ParseRequestCounter)
	name, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	query, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	// The client may provide type information for (some of) the placeholders.
	numQArgTypes, err := buf.GetUint16()
	if err != nil {
		return err
	}
	inTypeHints := make([]oid.Oid, numQArgTypes)
	for i := range inTypeHints {
		typ, err := buf.GetUint32()
		if err != nil {
			return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
		}
		inTypeHints[i] = oid.Oid(typ)
	}

	startParse := timeutil.Now()
	stmts, err := c.parser.ParseWithInt(query, nakedIntSize)
	if err != nil {
		log.SqlExec.Errorf(ctx, "failed to parse: %s", query)
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	if len(stmts) > 1 {
		err := pgerror.WrongNumberOfPreparedStatements(len(stmts))
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	var stmt parser.Statement
	if len(stmts) == 1 {
		stmt = stmts[0]
	}
	// len(stmts) == 0 results in a nil (empty) statement.

	if len(inTypeHints) > stmt.NumPlaceholders {
		err := pgwirebase.NewProtocolViolationErrorf(
			"received too many type hints: %d vs %d placeholders in query",
			len(inTypeHints), stmt.NumPlaceholders,
		)
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}

	var sqlTypeHints tree.PlaceholderTypes
	if len(inTypeHints) > 0 {
		// Prepare the mapping of SQL placeholder names to types. Pre-populate it with
		// the type hints received from the client, if any.
		sqlTypeHints = make(tree.PlaceholderTypes, stmt.NumPlaceholders)
		for i, t := range inTypeHints {
			if t == 0 {
				continue
			}
			// If the OID is user defined or unknown, then write nil into the type
			// hints and let the consumer of the PrepareStmt resolve the types.
			if t == oid.T_unknown || types.IsOIDUserDefinedType(t) {
				sqlTypeHints[i] = nil
				continue
			}
			v, ok := types.OidToType[t]
			if !ok {
				err := pgwirebase.NewProtocolViolationErrorf("unknown oid type: %v", t)
				return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
			}
			sqlTypeHints[i] = v
		}
	}

	endParse := timeutil.Now()

	if _, ok := stmt.AST.(*tree.CopyFrom); ok {
		// We don't support COPY in extended protocol because it'd be complicated:
		// it wouldn't be the preparing, but the execution that would need to
		// execute the copyMachine.
		// Be aware that the copyMachine assumes it always runs in the simple
		// protocol, so if we ever support this, many parts of the copyMachine
		// would need to be changed.
		// Also note that COPY FROM in extended mode seems to be quite broken in
		// Postgres too:
		// https://www.postgresql.org/message-id/flat/CAMsr%2BYGvp2wRx9pPSxaKFdaObxX8DzWse%2BOkWk2xpXSvT0rq-g%40mail.gmail.com#CAMsr+YGvp2wRx9pPSxaKFdaObxX8DzWse+OkWk2xpXSvT0rq-g@mail.gmail.com
		return c.stmtBuf.Push(ctx, sql.SendError{Err: fmt.Errorf("CopyFrom not supported in extended protocol mode")})
	}

	return c.stmtBuf.Push(
		ctx,
		sql.PrepareStmt{
			Name:         name,
			Statement:    stmt,
			TypeHints:    sqlTypeHints,
			RawTypeHints: inTypeHints,
			ParseStart:   startParse,
			ParseEnd:     endParse,
		})
}

// An error is returned iff the statement buffer has been closed. In that case,
// the connection should be considered toast.
func (c *conn) handleDescribe(ctx context.Context, buf *pgwirebase.ReadBuffer) error {
	telemetry.Inc(sqltelemetry.DescribeRequestCounter)
	typ, err := buf.GetPrepareType()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	name, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	return c.stmtBuf.Push(
		ctx,
		sql.DescribeStmt{
			Name: name,
			Type: typ,
		})
}

// An error is returned iff the statement buffer has been closed. In that case,
// the connection should be considered toast.
func (c *conn) handleClose(ctx context.Context, buf *pgwirebase.ReadBuffer) error {
	telemetry.Inc(sqltelemetry.CloseRequestCounter)
	typ, err := buf.GetPrepareType()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	name, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	return c.stmtBuf.Push(
		ctx,
		sql.DeletePreparedStmt{
			Name: name,
			Type: typ,
		})
}

// If no format codes are provided then all arguments/result-columns use
// the default format, text.
var formatCodesAllText = []pgwirebase.FormatCode{pgwirebase.FormatText}

// handleBind queues instructions for creating a portal from a prepared
// statement.
// An error is returned iff the statement buffer has been closed. In that case,
// the connection should be considered toast.
func (c *conn) handleBind(ctx context.Context, buf *pgwirebase.ReadBuffer) error {
	telemetry.Inc(sqltelemetry.BindRequestCounter)
	portalName, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	statementName, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}

	// From the docs on number of argument format codes to bind:
	// This can be zero to indicate that there are no arguments or that the
	// arguments all use the default format (text); or one, in which case the
	// specified format code is applied to all arguments; or it can equal the
	// actual number of arguments.
	// http://www.postgresql.org/docs/current/static/protocol-message-formats.html
	numQArgFormatCodes, err := buf.GetUint16()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	var qArgFormatCodes []pgwirebase.FormatCode
	switch numQArgFormatCodes {
	case 0:
		// No format codes means all arguments are passed as text.
		qArgFormatCodes = formatCodesAllText
	case 1:
		// `1` means read one code and apply it to every argument.
		ch, err := buf.GetUint16()
		if err != nil {
			return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
		}
		code := pgwirebase.FormatCode(ch)
		if code == pgwirebase.FormatText {
			qArgFormatCodes = formatCodesAllText
		} else {
			qArgFormatCodes = []pgwirebase.FormatCode{code}
		}
	default:
		qArgFormatCodes = make([]pgwirebase.FormatCode, numQArgFormatCodes)
		// Read one format code for each argument and apply it to that argument.
		for i := range qArgFormatCodes {
			ch, err := buf.GetUint16()
			if err != nil {
				return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
			}
			qArgFormatCodes[i] = pgwirebase.FormatCode(ch)
		}
	}

	numValues, err := buf.GetUint16()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	qargs := make([][]byte, numValues)
	for i := 0; i < int(numValues); i++ {
		plen, err := buf.GetUint32()
		if err != nil {
			return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
		}
		if int32(plen) == -1 {
			// The argument is a NULL value.
			qargs[i] = nil
			continue
		}
		b, err := buf.GetBytes(int(plen))
		if err != nil {
			return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
		}
		qargs[i] = b
	}

	// From the docs on number of result-column format codes to bind:
	// This can be zero to indicate that there are no result columns or that
	// the result columns should all use the default format (text); or one, in
	// which case the specified format code is applied to all result columns
	// (if any); or it can equal the actual number of result columns of the
	// query.
	// http://www.postgresql.org/docs/current/static/protocol-message-formats.html
	numColumnFormatCodes, err := buf.GetUint16()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	var columnFormatCodes []pgwirebase.FormatCode
	switch numColumnFormatCodes {
	case 0:
		// All columns will use the text format.
		columnFormatCodes = formatCodesAllText
	case 1:
		// All columns will use the one specified format.
		ch, err := buf.GetUint16()
		if err != nil {
			return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
		}
		code := pgwirebase.FormatCode(ch)
		if code == pgwirebase.FormatText {
			columnFormatCodes = formatCodesAllText
		} else {
			columnFormatCodes = []pgwirebase.FormatCode{code}
		}
	default:
		columnFormatCodes = make([]pgwirebase.FormatCode, numColumnFormatCodes)
		// Read one format code for each column and apply it to that column.
		for i := range columnFormatCodes {
			ch, err := buf.GetUint16()
			if err != nil {
				return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
			}
			columnFormatCodes[i] = pgwirebase.FormatCode(ch)
		}
	}
	return c.stmtBuf.Push(
		ctx,
		sql.BindStmt{
			PreparedStatementName: statementName,
			PortalName:            portalName,
			Args:                  qargs,
			ArgFormatCodes:        qArgFormatCodes,
			OutFormats:            columnFormatCodes,
		})
}

// An error is returned iff the statement buffer has been closed. In that case,
// the connection should be considered toast.
// 如果语句缓冲区已关闭，则返回错误。 在这种情况下，连接应该被视为 toast。
func (c *conn) handleExecute(
	ctx context.Context, buf *pgwirebase.ReadBuffer, timeReceived time.Time, followedBySync bool,
) error {
	telemetry.Inc(sqltelemetry.ExecuteRequestCounter)
	portalName, err := buf.GetString()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	limit, err := buf.GetUint32()
	if err != nil {
		return c.stmtBuf.Push(ctx, sql.SendError{Err: err})
	}
	return c.stmtBuf.Push(ctx, sql.ExecPortal{
		Name:           portalName,
		TimeReceived:   timeReceived,
		Limit:          int(limit),
		FollowedBySync: followedBySync,
	})
}

func (c *conn) handleFlush(ctx context.Context) error {
	telemetry.Inc(sqltelemetry.FlushRequestCounter)
	return c.stmtBuf.Push(ctx, sql.Flush{})
}

// BeginCopyIn is part of the pgwirebase.Conn interface.
func (c *conn) BeginCopyIn(
	ctx context.Context, columns []colinfo.ResultColumn, format pgwirebase.FormatCode,
) error {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgCopyInResponse)
	c.msgBuilder.writeByte(byte(format))
	c.msgBuilder.putInt16(int16(len(columns)))
	for range columns {
		c.msgBuilder.putInt16(int16(format))
	}
	return c.msgBuilder.finishMsg(c.conn)
}

// SendCommandComplete is part of the pgwirebase.Conn interface.
func (c *conn) SendCommandComplete(tag []byte) error {
	c.bufferCommandComplete(tag)
	return nil
}

// Rd is part of the pgwirebase.Conn interface.
func (c *conn) Rd() pgwirebase.BufferedReader {
	return &pgwireReader{conn: c}
}

// flushInfo encapsulates information about what results have been flushed to
// the network.
type flushInfo struct {
	// buf is a reference to writerState.buf.
	buf *bytes.Buffer
	// lastFlushed indicates the highest command for which results have been
	// flushed. The command may have further results in the buffer that haven't
	// been flushed.
	lastFlushed sql.CmdPos
	// cmdStarts maintains the state about where the results for the respective
	// positions begin. We utilize the invariant that positions are
	// monotonically increasing sequences.
	cmdStarts cmdIdxBuffer
}

type cmdIdx struct {
	pos sql.CmdPos
	idx int
}

var cmdIdxPool = sync.Pool{
	New: func() interface{} {
		return &cmdIdx{}
	},
}

func (c *cmdIdx) release() {
	*c = cmdIdx{}
	cmdIdxPool.Put(c)
}

type cmdIdxBuffer struct {
	// We intentionally do not just embed ring.Buffer in order to restrict the
	// methods that can be called on cmdIdxBuffer.
	buffer ring.Buffer
}

func (b *cmdIdxBuffer) empty() bool {
	return b.buffer.Len() == 0
}

func (b *cmdIdxBuffer) addLast(pos sql.CmdPos, idx int) {
	cmdIdx := cmdIdxPool.Get().(*cmdIdx)
	cmdIdx.pos = pos
	cmdIdx.idx = idx
	b.buffer.AddLast(cmdIdx)
}

// removeLast removes the last cmdIdx from the buffer and will panic if the
// buffer is empty.
func (b *cmdIdxBuffer) removeLast() {
	b.getLast().release()
	b.buffer.RemoveLast()
}

// getLast returns the last cmdIdx in the buffer and will panic if the buffer is
// empty.
func (b *cmdIdxBuffer) getLast() *cmdIdx {
	return b.buffer.GetLast().(*cmdIdx)
}

func (b *cmdIdxBuffer) clear() {
	for !b.empty() {
		b.removeLast()
	}
}

// registerCmd updates cmdStarts buffer when the first result for a new command
// is received.
func (fi *flushInfo) registerCmd(pos sql.CmdPos) {
	if !fi.cmdStarts.empty() && fi.cmdStarts.getLast().pos >= pos {
		// Not a new command, nothing to do.
		return
	}
	fi.cmdStarts.addLast(pos, fi.buf.Len())
}

func cookTag(
	tagStr string, buf []byte, stmtType tree.StatementReturnType, rowsAffected int,
) []byte {
	if tagStr == "INSERT" {
		// From the postgres docs (49.5. Message Formats):
		// `INSERT oid rows`... oid is the object ID of the inserted row if
		//	rows is 1 and the target table has OIDs; otherwise oid is 0.
		tagStr = "INSERT 0"
	}
	tag := append(buf, tagStr...)

	switch stmtType {
	case tree.RowsAffected:
		tag = append(tag, ' ')
		tag = strconv.AppendInt(tag, int64(rowsAffected), 10)

	case tree.Rows:
		tag = append(tag, ' ')
		tag = strconv.AppendUint(tag, uint64(rowsAffected), 10)

	case tree.Ack, tree.DDL:
		if tagStr == "SELECT" {
			tag = append(tag, ' ')
			tag = strconv.AppendInt(tag, int64(rowsAffected), 10)
		}

	case tree.CopyIn:
		// Nothing to do. The CommandComplete message has been sent elsewhere.
		panic(errors.AssertionFailedf("CopyIn statements should have been handled elsewhere " +
			"and not produce results"))
	default:
		panic(errors.AssertionFailedf("unexpected result type %v", stmtType))
	}

	return tag
}

// bufferRow serializes a row and adds it to the buffer. Depending on the buffer
// size limit, bufferRow may flush the buffered data to the connection.
func (c *conn) bufferRow(ctx context.Context, row tree.Datums, r *commandResult) error {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgDataRow)
	c.msgBuilder.putInt16(int16(len(row)))
	for i, col := range row {
		fmtCode := pgwirebase.FormatText
		if r.formatCodes != nil {
			fmtCode = r.formatCodes[i]
		}
		switch fmtCode {
		case pgwirebase.FormatText:
			c.msgBuilder.writeTextDatum(ctx, col, r.conv, r.location, r.types[i])
		case pgwirebase.FormatBinary:
			c.msgBuilder.writeBinaryDatum(ctx, col, r.location, r.types[i])
		default:
			c.msgBuilder.setError(errors.Errorf("unsupported format code %s", fmtCode))
		}
	}
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
	return c.maybeFlush(r.pos, r.bufferingDisabled)
}

// bufferBatch serializes a batch and adds all the rows from it to the buffer.
// It is a noop for zero-length batch. Depending on the buffer size limit,
// bufferBatch may flush the buffered data to the connection.
func (c *conn) bufferBatch(ctx context.Context, batch coldata.Batch, r *commandResult) error {
	sel := batch.Selection()
	n := batch.Length()
	if n > 0 {
		c.vecsScratch.SetBatch(batch)
		// Make sure that c doesn't hold on to the memory of the batch.
		defer c.vecsScratch.Reset()
		width := int16(len(c.vecsScratch.Vecs))
		for i := 0; i < n; i++ {
			rowIdx := i
			if sel != nil {
				rowIdx = sel[rowIdx]
			}
			c.msgBuilder.initMsg(pgwirebase.ServerMsgDataRow)
			c.msgBuilder.putInt16(width)
			for vecIdx := 0; vecIdx < len(c.vecsScratch.Vecs); vecIdx++ {
				fmtCode := pgwirebase.FormatText
				if r.formatCodes != nil {
					fmtCode = r.formatCodes[vecIdx]
				}
				switch fmtCode {
				case pgwirebase.FormatText:
					c.msgBuilder.writeTextColumnarElement(ctx, &c.vecsScratch, vecIdx, rowIdx, r.conv, r.location)
				case pgwirebase.FormatBinary:
					c.msgBuilder.writeBinaryColumnarElement(ctx, &c.vecsScratch, vecIdx, rowIdx, r.location)
				default:
					c.msgBuilder.setError(errors.Errorf("unsupported format code %s", fmtCode))
				}
			}
			if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
				panic(fmt.Sprintf("unexpected err from buffer: %s", err))
			}
			if err := c.maybeFlush(r.pos, r.bufferingDisabled); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *conn) bufferReadyForQuery(txnStatus byte) {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgReady)
	c.msgBuilder.writeByte(txnStatus)
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferParseComplete() {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgParseComplete)
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferBindComplete() {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgBindComplete)
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferCloseComplete() {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgCloseComplete)
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferCommandComplete(tag []byte) {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgCommandComplete)
	c.msgBuilder.write(tag)
	c.msgBuilder.nullTerminate()
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferPortalSuspended() {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgPortalSuspended)
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferErr(ctx context.Context, err error) {
	if err := writeErr(ctx, c.sv,
		err, &c.msgBuilder, &c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferEmptyQueryResponse() {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgEmptyQuery)
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func writeErr(
	ctx context.Context, sv *settings.Values, err error, msgBuilder *writeBuffer, w io.Writer,
) error {
	// Record telemetry for the error.
	sqltelemetry.RecordError(ctx, err, sv)
	msgBuilder.initMsg(pgwirebase.ServerMsgErrorResponse)
	return writeErrFields(ctx, sv, err, msgBuilder, w)
}

func writeErrFields(
	ctx context.Context, sv *settings.Values, err error, msgBuilder *writeBuffer, w io.Writer,
) error {
	pgErr := pgerror.Flatten(err)

	msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldSeverity)
	msgBuilder.writeTerminatedString(pgErr.Severity)

	msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldSeverityNonLocalized)
	msgBuilder.writeTerminatedString(pgErr.Severity)

	msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldSQLState)
	msgBuilder.writeTerminatedString(pgErr.Code)

	if pgErr.Detail != "" {
		msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldDetail)
		msgBuilder.writeTerminatedString(pgErr.Detail)
	}

	if pgErr.Hint != "" {
		msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldHint)
		msgBuilder.writeTerminatedString(pgErr.Hint)
	}

	if pgErr.ConstraintName != "" {
		msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldConstraintName)
		msgBuilder.writeTerminatedString(pgErr.ConstraintName)
	}

	if pgErr.Source != nil {
		errCtx := pgErr.Source
		if errCtx.File != "" {
			msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldSrcFile)
			msgBuilder.writeTerminatedString(errCtx.File)
		}

		if errCtx.Line > 0 {
			msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldSrcLine)
			msgBuilder.writeTerminatedString(strconv.Itoa(int(errCtx.Line)))
		}

		if errCtx.Function != "" {
			msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldSrcFunction)
			msgBuilder.writeTerminatedString(errCtx.Function)
		}
	}

	msgBuilder.putErrFieldMsg(pgwirebase.ServerErrFieldMsgPrimary)
	msgBuilder.writeTerminatedString(pgErr.Message)

	msgBuilder.nullTerminate()
	return msgBuilder.finishMsg(w)
}

func (c *conn) bufferParamDesc(types []oid.Oid) {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgParameterDescription)
	c.msgBuilder.putInt16(int16(len(types)))
	for _, t := range types {
		c.msgBuilder.putInt32(int32(t))
	}
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

func (c *conn) bufferNoDataMsg() {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgNoData)
	if err := c.msgBuilder.finishMsg(&c.writerState.buf); err != nil {
		panic(errors.NewAssertionErrorWithWrappedErrf(err, "unexpected err from buffer"))
	}
}

// writeRowDescription writes a row description to the given writer.
//
// formatCodes specifies the format for each column. It can be nil, in which
// case all columns will use FormatText.
//
// If an error is returned, it has also been saved on c.err.
func (c *conn) writeRowDescription(
	ctx context.Context,
	columns []colinfo.ResultColumn,
	formatCodes []pgwirebase.FormatCode,
	w io.Writer,
) error {
	c.msgBuilder.initMsg(pgwirebase.ServerMsgRowDescription)
	c.msgBuilder.putInt16(int16(len(columns)))
	for i, column := range columns {
		if log.V(2) {
			log.Infof(ctx, "pgwire: writing column %s of type: %s", column.Name, column.Typ)
		}
		c.msgBuilder.writeTerminatedString(column.Name)
		typ := pgTypeForParserType(column.Typ)
		c.msgBuilder.putInt32(int32(column.TableID))        // Table OID (optional).
		c.msgBuilder.putInt16(int16(column.PGAttributeNum)) // Column attribute ID (optional).
		c.msgBuilder.putInt32(int32(typ.oid))
		c.msgBuilder.putInt16(int16(typ.size))
		c.msgBuilder.putInt32(column.GetTypeModifier()) // Type modifier
		if formatCodes == nil {
			c.msgBuilder.putInt16(int16(pgwirebase.FormatText))
		} else {
			c.msgBuilder.putInt16(int16(formatCodes[i]))
		}
	}
	if err := c.msgBuilder.finishMsg(w); err != nil {
		c.setErr(err)
		return err
	}
	return nil
}

// Flush is part of the ClientComm interface.
//
// In case conn.err is set, this is a no-op - the previous err is returned.
func (c *conn) Flush(pos sql.CmdPos) error {
	// Check that there were no previous network errors. If there were, we'd
	// probably also fail the write below, but this check is here to make
	// absolutely sure that we don't send some results after we previously had
	// failed to send others.
	if err := c.GetErr(); err != nil {
		return err
	}

	c.writerState.fi.lastFlushed = pos
	// Make sure that the entire cmdStarts buffer is drained.
	c.writerState.fi.cmdStarts.clear()

	_ /* n */, err := c.writerState.buf.WriteTo(c.conn)
	if err != nil {
		c.setErr(err)
		return err
	}
	return nil
}

// maybeFlush flushes the buffer to the network connection if it exceeded
// sessionArgs.ConnResultsBufferSize or if buffering is disabled.
func (c *conn) maybeFlush(pos sql.CmdPos, bufferingDisabled bool) error {
	if !bufferingDisabled && int64(c.writerState.buf.Len()) <= c.sessionArgs.ConnResultsBufferSize {
		return nil
	}
	return c.Flush(pos)
}

// LockCommunication is part of the ClientComm interface.
//
// The current implementation of conn writes results to the network
// synchronously, as they are produced (modulo buffering). Therefore, there's
// nothing to "lock" - communication is naturally blocked as the command
// processor won't write any more results.
func (c *conn) LockCommunication() sql.ClientLock {
	return (*clientConnLock)(&c.writerState.fi)
}

// clientConnLock is the connection's implementation of sql.ClientLock. It lets
// the sql module lock the flushing of results and find out what has already
// been flushed.
type clientConnLock flushInfo

var _ sql.ClientLock = &clientConnLock{}

// Close is part of the sql.ClientLock interface.
func (cl *clientConnLock) Close() {
	// Nothing to do. See LockCommunication note.
}

// ClientPos is part of the sql.ClientLock interface.
func (cl *clientConnLock) ClientPos() sql.CmdPos {
	return cl.lastFlushed
}

// RTrim is part of the sql.ClientLock interface.
func (cl *clientConnLock) RTrim(ctx context.Context, pos sql.CmdPos) {
	if pos <= cl.lastFlushed {
		panic(errors.AssertionFailedf("asked to trim to pos: %d, below the last flush: %d", pos, cl.lastFlushed))
	}
	// If we don't have a start index for pos yet, it must be that no results
	// for it yet have been produced yet.
	truncateIdx := cl.buf.Len()
	// Update cmdStarts buffer: delete commands that were trimmed from the back
	// of the cmdStarts buffer.
	for !cl.cmdStarts.empty() {
		cmdStart := cl.cmdStarts.getLast()
		if cmdStart.pos < pos {
			break
		}
		truncateIdx = cmdStart.idx
		cl.cmdStarts.removeLast()
	}
	cl.buf.Truncate(truncateIdx)
}

// CreateStatementResult is part of the sql.ClientComm interface.
func (c *conn) CreateStatementResult(
	stmt tree.Statement,
	descOpt sql.RowDescOpt,
	pos sql.CmdPos,
	formatCodes []pgwirebase.FormatCode,
	conv sessiondatapb.DataConversionConfig,
	location *time.Location,
	limit int,
	portalName string,
	implicitTxn bool,
) sql.CommandResult {
	return c.newCommandResult(descOpt, pos, stmt, formatCodes, conv, location, limit, portalName, implicitTxn)
}

// CreateSyncResult is part of the sql.ClientComm interface.
func (c *conn) CreateSyncResult(pos sql.CmdPos) sql.SyncResult {
	return c.newMiscResult(pos, readyForQuery)
}

// CreateFlushResult is part of the sql.ClientComm interface.
func (c *conn) CreateFlushResult(pos sql.CmdPos) sql.FlushResult {
	return c.newMiscResult(pos, flush)
}

// CreateDrainResult is part of the sql.ClientComm interface.
func (c *conn) CreateDrainResult(pos sql.CmdPos) sql.DrainResult {
	return c.newMiscResult(pos, noCompletionMsg)
}

// CreateBindResult is part of the sql.ClientComm interface.
func (c *conn) CreateBindResult(pos sql.CmdPos) sql.BindResult {
	return c.newMiscResult(pos, bindComplete)
}

// CreatePrepareResult is part of the sql.ClientComm interface.
func (c *conn) CreatePrepareResult(pos sql.CmdPos) sql.ParseResult {
	return c.newMiscResult(pos, parseComplete)
}

// CreateDescribeResult is part of the sql.ClientComm interface.
func (c *conn) CreateDescribeResult(pos sql.CmdPos) sql.DescribeResult {
	return c.newMiscResult(pos, noCompletionMsg)
}

// CreateEmptyQueryResult is part of the sql.ClientComm interface.
func (c *conn) CreateEmptyQueryResult(pos sql.CmdPos) sql.EmptyQueryResult {
	return c.newMiscResult(pos, emptyQueryResponse)
}

// CreateDeleteResult is part of the sql.ClientComm interface.
func (c *conn) CreateDeleteResult(pos sql.CmdPos) sql.DeleteResult {
	return c.newMiscResult(pos, closeComplete)
}

// CreateErrorResult is part of the sql.ClientComm interface.
func (c *conn) CreateErrorResult(pos sql.CmdPos) sql.ErrorResult {
	res := c.newMiscResult(pos, noCompletionMsg)
	res.errExpected = true
	return res
}

// CreateCopyInResult is part of the sql.ClientComm interface.
func (c *conn) CreateCopyInResult(pos sql.CmdPos) sql.CopyInResult {
	return c.newMiscResult(pos, noCompletionMsg)
}

// pgwireReader is an io.Reader that wraps a conn, maintaining its metrics as
// it is consumed.
type pgwireReader struct {
	conn *conn
}

// pgwireReader implements the pgwirebase.BufferedReader interface.
var _ pgwirebase.BufferedReader = &pgwireReader{}

// Read is part of the pgwirebase.BufferedReader interface.
func (r *pgwireReader) Read(p []byte) (int, error) {
	n, err := r.conn.rd.Read(p)
	r.conn.metrics.BytesInCount.Inc(int64(n))
	return n, err
}

// ReadString is part of the pgwirebase.BufferedReader interface.
func (r *pgwireReader) ReadString(delim byte) (string, error) {
	s, err := r.conn.rd.ReadString(delim)
	r.conn.metrics.BytesInCount.Inc(int64(len(s)))
	return s, err
}

// ReadByte is part of the pgwirebase.BufferedReader interface.
func (r *pgwireReader) ReadByte() (byte, error) {
	b, err := r.conn.rd.ReadByte()
	if err == nil {
		r.conn.metrics.BytesInCount.Inc(1)
	}
	return b, err
}

// statusReportParams is a list of session variables that are also
// reported as server run-time parameters in the pgwire connection
// initialization.
//
// The standard PostgreSQL status vars are listed here:
// https://www.postgresql.org/docs/10/static/libpq-status.html
var statusReportParams = []string{
	"server_version",
	"server_encoding",
	"client_encoding",
	"application_name",
	// Note: session_authorization is handled specially in serveImpl().
	"DateStyle",
	"IntervalStyle",
	"is_superuser",
	"TimeZone",
	"integer_datetimes",
	"standard_conforming_strings",
	"crdb_version", // CockroachDB extension.
}

// testingStatusReportParams is the minimum set of status parameters
// needed to make pgx tests in the local package happy.
var testingStatusReportParams = map[string]string{
	"client_encoding":             "UTF8",
	"standard_conforming_strings": "on",
}

// readTimeoutConn overloads net.Conn.Read by periodically calling
// checkExitConds() and aborting the read if an error is returned.
// readTimeoutConn 通过定期调用 checkExitConds() 并在返回错误时中止读取来重载 net.Conn.Read。
type readTimeoutConn struct {
	net.Conn
	// checkExitConds is called periodically by Read(). If it returns an error,
	// the Read() returns that error. Future calls to Read() are allowed, in which
	// case checkExitConds() will be called again.
	// checkExitConds 由 Read() 定期调用。 如果它返回错误，则 Read() 返回该错误。
	// 允许将来调用 Read()，在这种情况下将再次调用 checkExitConds()。
	checkExitConds func() error
}

// NewReadTimeoutConn wraps the given connection with a readTimeoutConn.
func NewReadTimeoutConn(c net.Conn, checkExitConds func() error) net.Conn {
	return &readTimeoutConn{
		Conn:           c,
		checkExitConds: checkExitConds,
	}
}

func (c *readTimeoutConn) Read(b []byte) (int, error) {
	// readTimeout is the amount of time ReadTimeoutConn should wait on a
	// read before checking for exit conditions. The tradeoff is between the
	// time it takes to react to session context cancellation and the overhead
	// of waking up and checking for exit conditions.
	// readTimeout 是 ReadTimeoutConn 在检查退出条件之前应等待读取的时间量。
	// 折衷是在对会话上下文取消做出反应所需的时间与唤醒和检查退出条件的开销之间。
	const readTimeout = 1 * time.Second

	// Remove the read deadline when returning from this function to avoid
	// unexpected behavior.
	// 从此函数返回时删除读取截止时间以避免意外行为。
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	for {
		if err := c.checkExitConds(); err != nil {
			return 0, err
		}
		if err := c.SetReadDeadline(timeutil.Now().Add(readTimeout)); err != nil {
			return 0, err
		}
		n, err := c.Conn.Read(b)
		if err != nil {
			// Continue if the error is due to timing out.
			if ne := (net.Error)(nil); errors.As(err, &ne) && ne.Timeout() {
				continue
			}
		}
		return n, err
	}
}
