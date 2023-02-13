// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package sql

import (
	"context"
	"strings"

	"github.com/cockroachdb/cockroach/pkg/settings"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/colinfo"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/cockroach/pkg/sql/opt"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/exec"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/exec/execbuilder"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/exec/explain"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/indexrec"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/memo"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/optbuilder"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/xform"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgcode"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
	"github.com/cockroachdb/cockroach/pkg/sql/physicalplan"
	"github.com/cockroachdb/cockroach/pkg/sql/querycache"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sessiondatapb"
	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/redact"
)

var queryCacheEnabled = settings.RegisterBoolSetting(
	settings.TenantWritable,
	"sql.query_cache.enabled", "enable the query cache", true,
)

// prepareUsingOptimizer builds a memo for a prepared statement and populates
// the following stmt.Prepared fields:
// prepareUsingOptimizer 为 prepared 语句构建 memo 并填充以下 stmt.Prepared 字段：
//  - Columns
//  - Types
//  - AnonymizedStr
//  - Memo (for reuse during exec, if appropriate).
func (p *planner) prepareUsingOptimizer(ctx context.Context) (planFlags, error) {
	stmt := &p.stmt

	opc := &p.optPlanningCtx
	opc.reset()

	switch t := stmt.AST.(type) {
	case *tree.AlterIndex, *tree.AlterTable, *tree.AlterSequence,
		*tree.Analyze,
		*tree.BeginTransaction,
		*tree.CommentOnColumn, *tree.CommentOnConstraint, *tree.CommentOnDatabase, *tree.CommentOnIndex, *tree.CommentOnTable, *tree.CommentOnSchema,
		*tree.CommitTransaction,
		*tree.CopyFrom, *tree.CreateDatabase, *tree.CreateIndex, *tree.CreateView,
		*tree.CreateSequence,
		*tree.CreateStats,
		*tree.Deallocate, *tree.Discard, *tree.DropDatabase, *tree.DropIndex,
		*tree.DropTable, *tree.DropView, *tree.DropSequence, *tree.DropType,
		*tree.Grant, *tree.GrantRole,
		*tree.Prepare,
		*tree.ReleaseSavepoint, *tree.RenameColumn, *tree.RenameDatabase,
		*tree.RenameIndex, *tree.RenameTable, *tree.Revoke, *tree.RevokeRole,
		*tree.RollbackToSavepoint, *tree.RollbackTransaction,
		*tree.Savepoint, *tree.SetTransaction, *tree.SetTracing, *tree.SetSessionAuthorizationDefault,
		*tree.SetSessionCharacteristics:
		// These statements do not have result columns and do not support placeholders
		// so there is no need to do anything during prepare.
		// 这些语句没有结果列，也不支持占位符，所以在准备期间不需要做任何事情。
		//
		// Some of these statements (like BeginTransaction) aren't supported by the
		// optbuilder so they would error out. Others (like CreateIndex) have planning
		// code that can introduce unnecessary txn retries (because of looking up
		// descriptors and such).
		// 其中一些语句（如 BeginTransaction）不受 optbuilder 支持，因此它们会出错。
		// 其他人（如 CreateIndex）的计划代码可能会引入不必要的 txn 重试（因为查找描述符等）。
		return opc.flags, nil

	case *tree.Execute:
		// This statement is going to execute a prepared statement. To prepare it,
		// we need to set the expected output columns to the output columns of the
		// prepared statement that the user is trying to execute.
		// 该语句将执行准备好的语句。 为了准备它，我们需要将预期的输出列设置为用户尝试执行的准备好的语句的输出列。
		name := string(t.Name)
		prepared, ok := p.preparedStatements.Get(name)
		if !ok {
			// We're trying to prepare an EXECUTE of a statement that doesn't exist.
			// Let's just give up at this point.
			// Postgres doesn't fail here, instead it produces an EXECUTE that returns
			// no columns. This seems like dubious behavior at best.
			// 我们正在尝试准备一个不存在的语句的 EXECUTE。 让我们在这一点上放弃吧。
			// Postgres 不会在这里失败，而是生成一个不返回任何列的 EXECUTE。 这充其量似乎是可疑的行为。
			return opc.flags, pgerror.Newf(pgcode.UndefinedPreparedStatement,
				"no such prepared statement %s", name)
		}
		stmt.Prepared.Columns = prepared.Columns
		return opc.flags, nil

	case *tree.ExplainAnalyze:
		// This statement returns result columns but does not support placeholders,
		// and we don't want to do anything during prepare.
		// 此语句返回结果列但不支持占位符，我们不想在准备期间做任何事情。
		if len(p.semaCtx.Placeholders.Types) != 0 {
			return 0, errors.Errorf("%s does not support placeholders", stmt.AST.StatementTag())
		}
		stmt.Prepared.Columns = colinfo.ExplainPlanColumns
		return opc.flags, nil
	case *tree.DeclareCursor:
		// Build memo for the purposes of typing placeholders.
		// 为输入占位符构建备忘录。
		// TODO(jordan): converting DeclareCursor to not be an opaque statement
		// would be a better way to accomplish this goal. See CREATE TABLE for an
		// example.
		// 将是实现此目标的更好方法。 有关示例，请参见创建表。
		f := opc.optimizer.Factory()
		bld := optbuilder.New(ctx, &p.semaCtx, p.EvalContext(), &opc.catalog, f, t.Select)
		if err := bld.Build(); err != nil {
			return opc.flags, err
		}
	}

	if opc.useCache {
		cachedData, ok := p.execCfg.QueryCache.Find(&p.queryCacheSession, stmt.SQL)
		if ok && cachedData.PrepareMetadata != nil {
			pm := cachedData.PrepareMetadata
			// Check that the type hints match (the type hints affect type checking).
			if !pm.TypeHints.Identical(p.semaCtx.Placeholders.TypeHints) {
				opc.log(ctx, "query cache hit but type hints don't match")
			} else {
				isStale, err := cachedData.Memo.IsStale(ctx, p.EvalContext(), &opc.catalog)
				if err != nil {
					return 0, err
				}
				if !isStale {
					opc.log(ctx, "query cache hit (prepare)")
					opc.flags.Set(planFlagOptCacheHit)
					stmt.Prepared.StatementNoConstants = pm.StatementNoConstants
					stmt.Prepared.Columns = pm.Columns
					stmt.Prepared.Types = pm.Types
					stmt.Prepared.Memo = cachedData.Memo
					return opc.flags, nil
				}
				opc.log(ctx, "query cache hit but memo is stale (prepare)")
			}
		} else if ok {
			opc.log(ctx, "query cache hit but there is no prepare metadata")
		} else {
			opc.log(ctx, "query cache miss")
		}
		opc.flags.Set(planFlagOptCacheMiss)
	}

	memo, err := opc.buildReusableMemo(ctx)
	if err != nil {
		return 0, err
	}

	md := memo.Metadata()
	physical := memo.RootProps()
	resultCols := make(colinfo.ResultColumns, len(physical.Presentation))
	for i, col := range physical.Presentation {
		colMeta := md.ColumnMeta(col.ID)
		resultCols[i].Name = col.Alias
		resultCols[i].Typ = colMeta.Type
		if err := checkResultType(resultCols[i].Typ); err != nil {
			return 0, err
		}
		// If the column came from a table, set up the relevant metadata.
		if colMeta.Table != opt.TableID(0) {
			// Get the cat.Table that this column references.
			tab := md.Table(colMeta.Table)
			resultCols[i].TableID = descpb.ID(tab.ID())
			// Convert the metadata opt.ColumnID to its ordinal position in the table.
			colOrdinal := colMeta.Table.ColumnOrdinal(col.ID)
			// Use that ordinal position to retrieve the column's stable ID.
			var column catalog.Column
			if catTable, ok := tab.(optCatalogTableInterface); ok {
				column = catTable.getCol(colOrdinal)
			}
			if column != nil {
				resultCols[i].PGAttributeNum = column.GetPGAttributeNum()
			} else {
				resultCols[i].PGAttributeNum = uint32(tab.Column(colOrdinal).ColID())
			}
		}
	}

	// Verify that all placeholder types have been set.
	if err := p.semaCtx.Placeholders.Types.AssertAllSet(); err != nil {
		return 0, err
	}

	stmt.Prepared.Columns = resultCols
	stmt.Prepared.Types = p.semaCtx.Placeholders.Types
	if opc.allowMemoReuse {
		stmt.Prepared.Memo = memo
		if opc.useCache {
			// execPrepare sets the PrepareMetadata.InferredTypes field after this
			// point. However, once the PrepareMetadata goes into the cache, it
			// can't be modified without causing race conditions. So make a copy of
			// it now.
			// TODO(radu): Determine if the extra object allocation is really
			// necessary.
			pm := stmt.Prepared.PrepareMetadata
			cachedData := querycache.CachedData{
				SQL:             stmt.SQL,
				Memo:            memo,
				PrepareMetadata: &pm,
			}
			p.execCfg.QueryCache.Add(&p.queryCacheSession, &cachedData)
		}
	}
	return opc.flags, nil
}

// makeOptimizerPlan generates a plan using the cost-based optimizer.
// On success, it populates p.curPlan.
// makeOptimizerPlan 使用基于成本的优化器生成计划。 成功时，它会填充 p.curPlan。
func (p *planner) makeOptimizerPlan(ctx context.Context) error {
	p.curPlan.init(&p.stmt, &p.instrumentation)

	opc := &p.optPlanningCtx
	opc.reset()

	execMemo, err := opc.buildExecMemo(ctx)
	if err != nil {
		return err
	}

	// Build the plan tree.
	if mode := p.SessionData().ExperimentalDistSQLPlanningMode; mode != sessiondatapb.ExperimentalDistSQLPlanningOff {
		planningMode := distSQLDefaultPlanning
		// If this transaction has modified or created any types, it is not safe to
		// distribute due to limitations around leasing descriptors modified in the
		// current transaction.
		// 如果此事务已修改或创建任何类型，则由于当前事务中修改的租赁描述符的限制，分发是不安全的。
		if p.Descriptors().HasUncommittedTypes() {
			planningMode = distSQLLocalOnlyPlanning
		}
		err := opc.runExecBuilder(
			&p.curPlan,
			&p.stmt,
			newDistSQLSpecExecFactory(p, planningMode),
			execMemo,
			p.EvalContext(),
			p.autoCommit,
		)
		if err != nil {
			if mode == sessiondatapb.ExperimentalDistSQLPlanningAlways &&
				!strings.Contains(p.stmt.AST.StatementTag(), "SET") {
				// We do not fallback to the old path because experimental
				// planning is set to 'always' and we don't have a SET
				// statement, so we return an error. SET statements are
				// exceptions because we want to be able to execute them
				// regardless of whether they are supported by the new factory.
				// 我们不回退到旧路径，因为实验计划设置为“始终”并且我们没有 SET 语句，所以我们返回错误。
				// SET 语句是例外，因为无论新工厂是否支持它们，我们都希望能够执行它们。
				// TODO(yuzefovich): update this once SET statements are
				// supported (see #47473).
				return err
			}
			// We will fallback to the old path.
		} else {
			// TODO(yuzefovich): think through whether subqueries or
			// postqueries can be distributed. If that's the case, we might
			// need to also look at the plan distribution of those.
			// 考虑是否可以分发子查询或后查询。 如果是这样的话，我们可能还需要查看它们的计划分布。
			m := p.curPlan.main
			isPartiallyDistributed := m.physPlan.Distribution == physicalplan.PartiallyDistributedPlan
			if isPartiallyDistributed && p.SessionData().PartiallyDistributedPlansDisabled {
				// The planning has succeeded, but we've created a partially
				// distributed plan yet the session variable prohibits such
				// plan distribution - we need to replan with a new factory
				// that forces local planning.
				// 计划已经成功，但是我们已经创建了一个部分分布式的计划，
				// 但是会话变量禁止这样的计划分配——我们需要用一个强制本地计划的新工厂重新计划。
				// TODO(yuzefovich): remove this logic when deleting old
				// execFactory.
				err = opc.runExecBuilder(
					&p.curPlan,
					&p.stmt,
					newDistSQLSpecExecFactory(p, distSQLLocalOnlyPlanning),
					execMemo,
					p.EvalContext(),
					p.autoCommit,
				)
			}
			if err == nil {
				return nil
			}
		}
		// TODO(yuzefovich): make the logging conditional on the verbosity
		// level once new DistSQL planning is no longer experimental.
		log.Infof(
			ctx, "distSQLSpecExecFactory failed planning with %v, falling back to the old path", err,
		)
	}
	// If we got here, we did not create a plan above.
	// 如果我们到了这里，我们没有在上面创建计划。
	return opc.runExecBuilder(
		&p.curPlan,
		&p.stmt,
		newExecFactory(p),
		execMemo,
		p.EvalContext(),
		p.autoCommit,
	)
}

type optPlanningCtx struct {
	p *planner

	// catalog is initialized once, and reset for each query. This allows the
	// catalog objects to be reused across queries in the same session.
	// 目录初始化一次，并为每个查询重置。 这允许在同一会话中跨查询重用目录对象。
	catalog optCatalog

	// -- Fields below are reinitialized for each query ---
	// 下面的字段为每个查询重新初始化

	optimizer xform.Optimizer

	// When set, we are allowed to reuse a memo, or store a memo for later reuse.
	// 设置后，我们可以重复使用备忘录，或存储备忘录供以后重复使用。
	allowMemoReuse bool

	// When set, we consult and update the query cache. Never set if
	// allowMemoReuse is false.
	// 设置后，我们查询并更新查询缓存。 如果 allowMemoReuse 为 false，则从不设置。
	useCache bool

	flags planFlags
}

// init performs one-time initialization of the planning context; reset() must
// also be called before each use.
// init 执行规划上下文的一次性初始化； reset() 也必须在每次使用前调用。
func (opc *optPlanningCtx) init(p *planner) {
	opc.p = p
	opc.catalog.init(p)
}

// reset initializes the planning context for the statement in the planner.
// reset 为计划器中的语句初始化计划上下文。
func (opc *optPlanningCtx) reset() {
	p := opc.p
	opc.catalog.reset()
	opc.optimizer.Init(p.EvalContext(), &opc.catalog)
	opc.flags = 0

	// We only allow memo caching for SELECT/INSERT/UPDATE/DELETE. We could
	// support it for all statements in principle, but it would increase the
	// surface of potential issues (conditions we need to detect to invalidate a
	// cached memo).
	// 我们只允许 SELECT/INSERT/UPDATE/DELETE 的备忘录缓存。
	// 我们原则上可以支持所有语句，但它会增加潜在问题的表面（我们需要检测的条件以使缓存的备忘录无效）。
	switch p.stmt.AST.(type) {
	case *tree.ParenSelect, *tree.Select, *tree.SelectClause, *tree.UnionClause, *tree.ValuesClause,
		*tree.Insert, *tree.Update, *tree.Delete, *tree.CannedOptPlan:
		// If the current transaction has uncommitted DDL statements, we cannot rely
		// on descriptor versions for detecting a "stale" memo. This is because
		// descriptor versions are bumped at most once per transaction, even if there
		// are multiple DDL operations; and transactions can be aborted leading to
		// potential reuse of versions. To avoid these issues, we prevent saving a
		// memo (for prepare) or reusing a saved memo (for execute).
		// 如果当前事务有未提交的 DDL 语句，我们不能依赖描述符版本来检测“陈旧”的备忘录。
		// 这是因为即使有多个 DDL 操作，每个事务的描述符版本最多也会发生一次变化；
		// 并且事务可以被中止，从而导致版本的潜在重用。
		// 为避免这些问题，我们防止保存备忘录（用于准备）或重复使用已保存的备忘录（用于执行）。
		opc.allowMemoReuse = !p.Descriptors().HasUncommittedTables()
		opc.useCache = opc.allowMemoReuse && queryCacheEnabled.Get(&p.execCfg.Settings.SV)

		if _, isCanned := p.stmt.AST.(*tree.CannedOptPlan); isCanned {
			// It's unsafe to use the cache, since PREPARE AS OPT PLAN doesn't track
			// dependencies and check permissions.
			// 使用缓存是不安全的，因为 PREPARE AS OPT PLAN 不会跟踪依赖关系和检查权限。
			opc.useCache = false
		}

	default:
		opc.allowMemoReuse = false
		opc.useCache = false
	}
}

func (opc *optPlanningCtx) log(ctx context.Context, msg string) {
	if log.VDepth(1, 1) {
		log.InfofDepth(ctx, 1, "%s: %s", redact.Safe(msg), opc.p.stmt)
	} else {
		log.Event(ctx, msg)
	}
}

// buildReusableMemo builds the statement into a memo that can be stored for
// prepared statements and can later be used as a starting point for
// optimization. The returned memo is fully detached from the planner and can be
// used with reuseMemo independently and concurrently by multiple threads.
//
// buildReusableMemo 将语句构建成备忘录，可以为准备好的语句存储，以后可以用作优化的起点。
// 返回的 memo 与 planner 完全分离，可以独立使用 reuseMemo 并由多个线程并发使用。
func (opc *optPlanningCtx) buildReusableMemo(ctx context.Context) (_ *memo.Memo, _ error) {
	p := opc.p

	_, isCanned := opc.p.stmt.AST.(*tree.CannedOptPlan)
	if isCanned {
		if !p.EvalContext().SessionData().AllowPrepareAsOptPlan {
			return nil, pgerror.New(pgcode.InsufficientPrivilege,
				"PREPARE AS OPT PLAN is a testing facility that should not be used directly",
			)
		}

		if !p.SessionData().User().IsRootUser() {
			return nil, pgerror.New(pgcode.InsufficientPrivilege,
				"PREPARE AS OPT PLAN may only be used by root",
			)
		}
	}

	if p.SessionData().SaveTablesPrefix != "" && !p.SessionData().User().IsRootUser() {
		return nil, pgerror.New(pgcode.InsufficientPrivilege,
			"sub-expression tables creation may only be used by root",
		)
	}

	// Build the Memo (optbuild) and apply normalization rules to it. If the
	// query contains placeholders, values are not assigned during this phase,
	// as that only happens during the EXECUTE phase. If the query does not
	// contain placeholders, then also apply exploration rules to the Memo so
	// that there's even less to do during the EXECUTE phase.
	//
	// 构建备忘录 (optbuild) 并对其应用规范化规则。
	// 如果查询包含占位符，则在此阶段不会分配值，因为这只会在 EXECUTE 阶段发生。
	// 如果查询不包含占位符，那么还可以将探索规则应用于备忘录，这样在执行阶段就可以做更少的事情。
	f := opc.optimizer.Factory()
	bld := optbuilder.New(ctx, &p.semaCtx, p.EvalContext(), &opc.catalog, f, opc.p.stmt.AST)
	bld.KeepPlaceholders = true
	if err := bld.Build(); err != nil {
		return nil, err
	}

	if bld.DisableMemoReuse {
		// The builder encountered a statement that prevents safe reuse of the memo.
		// 构建器遇到阻止安全重用备忘录的语句。
		opc.allowMemoReuse = false
		opc.useCache = false
	}

	if isCanned {
		if f.Memo().HasPlaceholders() {
			// We don't support placeholders inside the canned plan. The main reason
			// is that they would be invisible to the parser (which reports the number
			// of placeholders, used to initialize the relevant structures).
			// 我们不支持固定计划中的占位符。
			// 主要原因是它们对解析器不可见（解析器报告占位符的数量，用于初始化相关结构）。
			return nil, pgerror.Newf(pgcode.Syntax,
				"placeholders are not supported with PREPARE AS OPT PLAN")
		}
		// With a canned plan, we don't want to optimize the memo.
		// 有了固定计划，我们不想优化备忘录。
		return opc.optimizer.DetachMemo(), nil
	}

	if f.Memo().HasPlaceholders() {
		// Try the placeholder fast path.
		_, ok, err := opc.optimizer.TryPlaceholderFastPath()
		if err != nil {
			return nil, err
		}
		if ok {
			opc.log(ctx, "placeholder fast path")
		}
	} else {
		// If the memo doesn't have placeholders and did not encounter any stable
		// operators that can be constant folded, then fully optimize it now - it
		// can be reused without further changes to build the execution tree.
		// 如果 memo 没有占位符并且没有遇到任何可以常量折叠的稳定运算符，
		// 那么现在就对其进行全面优化——它可以被重用而无需进一步更改来构建执行树。
		if !f.FoldingControl().PreventedStableFold() {
			opc.log(ctx, "optimizing (no placeholders)")
			if _, err := opc.optimizer.Optimize(); err != nil {
				return nil, err
			}
		}
	}

	// Detach the prepared memo from the factory and transfer its ownership
	// to the prepared statement. DetachMemo will re-initialize the optimizer
	// to an empty memo.
	// 从工厂中分离准备好的备忘录，并将其所有权转移到准备好的语句中。
	// DetachMemo 会将优化器重新初始化为空备忘录。
	return opc.optimizer.DetachMemo(), nil
}

// reuseMemo returns an optimized memo using a cached memo as a starting point.
//
// The cached memo is not modified; it is safe to call reuseMemo on the same
// cachedMemo from multiple threads concurrently.
//
// The returned memo is only safe to use in one thread, during execution of the
// current statement.
func (opc *optPlanningCtx) reuseMemo(cachedMemo *memo.Memo) (*memo.Memo, error) {
	if cachedMemo.IsOptimized() {
		// The query could have been already fully optimized if there were no
		// placeholders or the placeholder fast path succeeded (see
		// buildReusableMemo).
		return cachedMemo, nil
	}
	f := opc.optimizer.Factory()
	// Finish optimization by assigning any remaining placeholders and
	// applying exploration rules. Reinitialize the optimizer and construct a
	// new memo that is copied from the prepared memo, but with placeholders
	// assigned. Stable operators can be constant-folded at this time.
	// 通过分配任何剩余的占位符并应用探索规则来完成优化。
	// 重新初始化优化器并构建一个从准备好的备忘录中复制的新备忘录，但分配了占位符。
	// 稳定算子此时可以常量折叠。
	f.FoldingControl().AllowStableFolds()
	if err := f.AssignPlaceholders(cachedMemo); err != nil {
		return nil, err
	}
	if _, err := opc.optimizer.Optimize(); err != nil {
		return nil, err
	}
	return f.Memo(), nil
}

// buildExecMemo creates a fully optimized memo, possibly reusing a previously
// cached memo as a starting point.
// buildExecMemo 创建一个完全优化的备忘录，可能会重新使用以前缓存的备忘录作为起点。
//
// The returned memo is only safe to use in one thread, during execution of the
// current statement.
// 返回的备忘录只能在当前语句执行期间在一个线程中安全使用。
func (opc *optPlanningCtx) buildExecMemo(ctx context.Context) (_ *memo.Memo, _ error) {
	prepared := opc.p.stmt.Prepared
	p := opc.p
	if opc.allowMemoReuse && prepared != nil && prepared.Memo != nil {
		// We are executing a previously prepared statement and a reusable memo is
		// available.

		// If the prepared memo has been invalidated by schema or other changes,
		// re-prepare it. memo 无效了
		if isStale, err := prepared.Memo.IsStale(ctx, p.EvalContext(), &opc.catalog); err != nil {
			return nil, err
		} else if isStale {
			prepared.Memo, err = opc.buildReusableMemo(ctx)
			opc.log(ctx, "rebuilding cached memo")
			if err != nil {
				return nil, err
			}
		}
		opc.log(ctx, "reusing cached memo")
		memo, err := opc.reuseMemo(prepared.Memo)
		return memo, err
	}

	if opc.useCache {
		// Consult the query cache.
		cachedData, ok := p.execCfg.QueryCache.Find(&p.queryCacheSession, opc.p.stmt.SQL)
		if ok {
			if isStale, err := cachedData.Memo.IsStale(ctx, p.EvalContext(), &opc.catalog); err != nil {
				return nil, err
			} else if isStale {
				cachedData.Memo, err = opc.buildReusableMemo(ctx)
				if err != nil {
					return nil, err
				}
				// Update the plan in the cache. If the cache entry had PrepareMetadata
				// populated, it may no longer be valid.
				cachedData.PrepareMetadata = nil
				p.execCfg.QueryCache.Add(&p.queryCacheSession, &cachedData)
				opc.log(ctx, "query cache hit but needed update")
				opc.flags.Set(planFlagOptCacheMiss)
			} else {
				opc.log(ctx, "query cache hit")
				opc.flags.Set(planFlagOptCacheHit)
			}
			memo, err := opc.reuseMemo(cachedData.Memo)
			return memo, err
		}
		opc.flags.Set(planFlagOptCacheMiss)
		opc.log(ctx, "query cache miss")
	} else {
		opc.log(ctx, "not using query cache")
	}

	// We are executing a statement for which there is no reusable memo
	// available.
	// 我们正在执行一个没有可重用备忘录的语句。
	f := opc.optimizer.Factory()
	f.FoldingControl().AllowStableFolds()
	bld := optbuilder.New(ctx, &p.semaCtx, p.EvalContext(), &opc.catalog, f, opc.p.stmt.AST)
	if err := bld.Build(); err != nil {
		return nil, err
	}

	// For index recommendations, after building we must interrupt the flow to
	// find potential index candidates in the memo.
	// 对于索引推荐，在构建之后我们必须中断流程以在备忘录中找到潜在的索引候选者。
	_, isExplain := opc.p.stmt.AST.(*tree.Explain)
	if isExplain && p.SessionData().IndexRecommendationsEnabled {
		if err := opc.makeQueryIndexRecommendation(); err != nil {
			return nil, err
		}
	}

	if _, isCanned := opc.p.stmt.AST.(*tree.CannedOptPlan); !isCanned {
		if _, err := opc.optimizer.Optimize(); err != nil {
			return nil, err
		}
	}

	// If this statement doesn't have placeholders and we have not constant-folded
	// any VolatilityStable operators, add it to the cache.
	// Note that non-prepared statements from pgwire clients cannot have
	// placeholders.
	if opc.useCache && !bld.HadPlaceholders && !bld.DisableMemoReuse &&
		!f.FoldingControl().PermittedStableFold() {
		memo := opc.optimizer.DetachMemo()
		cachedData := querycache.CachedData{
			SQL:  opc.p.stmt.SQL,
			Memo: memo,
		}
		p.execCfg.QueryCache.Add(&p.queryCacheSession, &cachedData)
		opc.log(ctx, "query cache add")
		return memo, nil
	}

	return f.Memo(), nil
}

// runExecBuilder execbuilds a plan using the given factory and stores the
// result in planTop. If required, also captures explain data using the explain
// factory.
// runExecBuilder exec 使用给定的工厂构建计划并将结果存储在 planTop 中。
// 如果需要，还可以使用解释工厂捕获解释数据。
func (opc *optPlanningCtx) runExecBuilder(
	planTop *planTop,
	stmt *Statement,
	f exec.Factory,
	mem *memo.Memo,
	evalCtx *tree.EvalContext,
	allowAutoCommit bool,
) error {
	var result *planComponents
	var isDDL bool
	var containsFullTableScan bool
	var containsFullIndexScan bool
	var containsLargeFullTableScan bool
	var containsLargeFullIndexScan bool
	var containsMutation bool
	var gf *explain.PlanGistFactory
	if !opc.p.SessionData().DisablePlanGists {
		gf = explain.NewPlanGistFactory(f)
		f = gf
	}
	if !planTop.instrumentation.ShouldBuildExplainPlan() {
		bld := execbuilder.New(f, &opc.optimizer, mem, &opc.catalog, mem.RootExpr(), evalCtx, allowAutoCommit)
		plan, err := bld.Build()
		if err != nil {
			return err
		}
		result = plan.(*planComponents)
		isDDL = bld.IsDDL
		containsFullTableScan = bld.ContainsFullTableScan
		containsFullIndexScan = bld.ContainsFullIndexScan
		containsLargeFullTableScan = bld.ContainsLargeFullTableScan
		containsLargeFullIndexScan = bld.ContainsLargeFullIndexScan
		containsMutation = bld.ContainsMutation
	} else {
		// Create an explain factory and record the explain.Plan.
		explainFactory := explain.NewFactory(f)
		bld := execbuilder.New(
			explainFactory, &opc.optimizer, mem, &opc.catalog, mem.RootExpr(), evalCtx, allowAutoCommit,
		)
		plan, err := bld.Build()
		if err != nil {
			return err
		}
		explainPlan := plan.(*explain.Plan)
		result = explainPlan.WrappedPlan.(*planComponents)
		isDDL = bld.IsDDL
		containsFullTableScan = bld.ContainsFullTableScan
		containsFullIndexScan = bld.ContainsFullIndexScan
		containsLargeFullTableScan = bld.ContainsLargeFullTableScan
		containsLargeFullIndexScan = bld.ContainsLargeFullIndexScan
		containsMutation = bld.ContainsMutation

		planTop.instrumentation.RecordExplainPlan(explainPlan)
	}
	if gf != nil {
		planTop.instrumentation.planGist = gf.PlanGist()
	}
	planTop.instrumentation.costEstimate = float64(mem.RootExpr().(memo.RelExpr).Cost())

	if stmt.ExpectedTypes != nil {
		cols := result.main.planColumns()
		if !stmt.ExpectedTypes.TypesEqual(cols) {
			return pgerror.New(pgcode.FeatureNotSupported, "cached plan must not change result type")
		}
	}

	planTop.planComponents = *result
	planTop.stmt = stmt
	planTop.flags = opc.flags
	if isDDL {
		planTop.flags.Set(planFlagIsDDL)
	}
	if containsFullTableScan {
		planTop.flags.Set(planFlagContainsFullTableScan)
	}
	if containsFullIndexScan {
		planTop.flags.Set(planFlagContainsFullIndexScan)
	}
	if containsLargeFullTableScan {
		planTop.flags.Set(planFlagContainsLargeFullTableScan)
	}
	if containsLargeFullIndexScan {
		planTop.flags.Set(planFlagContainsLargeFullIndexScan)
	}
	if containsMutation {
		planTop.flags.Set(planFlagContainsMutation)
	}
	if planTop.instrumentation.ShouldSaveMemo() {
		planTop.mem = mem
		planTop.catalog = &opc.catalog
	}
	return nil
}

// DecodeGist Avoid an import cycle by keeping the cat out of the tree.
// DecodeGist 通过让猫远离树来避免导入循环。
func (p *planner) DecodeGist(gist string) ([]string, error) {
	return explain.DecodePlanGistToRows(gist, &p.optPlanningCtx.catalog)
}

// makeQueryIndexRecommendation builds a statement and walks through it to find
// potential index candidates. It then optimizes the statement with those
// indexes hypothetically added to the table. An index recommendation for the
// query is outputted based on which hypothetical indexes are helpful in the
// optimal plan.
// makeQueryIndexRecommendation 构建一个语句并遍历它以找到潜在的索引候选者。
// 然后它使用假设添加到表中的那些索引优化语句。 根据哪些假设索引有助于优化计划，输出查询的索引建议。
func (opc *optPlanningCtx) makeQueryIndexRecommendation() error {
	// Save the normalized memo created by the optbuilder.
	// 保存由 optbuilder 创建的标准化备忘录。
	savedMemo := opc.optimizer.DetachMemo()

	// Use the optimizer to fully normalize the memo. We need to do this before
	// finding index candidates because the *memo.SortExpr from the sort enforcer
	// is only added to the memo in this step. The sort expression is required to
	// determine certain index candidates.
	// 使用优化器完全规范化备忘录。 我们需要在查找候选索引之前执行此操作，
	// 因为排序执行器中的 *memo.SortExpr 仅在这一步中添加到备忘录中。
	// 需要排序表达式来确定某些索引候选者。
	f := opc.optimizer.Factory()
	f.FoldingControl().AllowStableFolds()
	f.CopyAndReplace(
		savedMemo.RootExpr().(memo.RelExpr),
		savedMemo.RootProps(),
		f.CopyWithoutAssigningPlaceholders,
	)
	opc.optimizer.NotifyOnMatchedRule(func(ruleName opt.RuleName) bool {
		return ruleName.IsNormalize()
	})
	if _, err := opc.optimizer.Optimize(); err != nil {
		return err
	}

	// Walk through the fully normalized memo to determine index candidates and
	// create hypothetical tables.
	// 遍历完全规范化的备忘录以确定候选索引并创建假设表。
	indexCandidates := indexrec.FindIndexCandidateSet(f.Memo().RootExpr(), f.Metadata())
	optTables, hypTables := indexrec.BuildOptAndHypTableMaps(indexCandidates)

	// Optimize with the saved memo and hypothetical tables. Walk through the
	// optimal plan to determine index recommendations.
	// 使用保存的备忘录和假设表进行优化。 遍历最佳计划以确定索引建议。
	opc.optimizer.Init(f.EvalContext(), &opc.catalog)
	f.CopyAndReplace(
		savedMemo.RootExpr().(memo.RelExpr),
		savedMemo.RootProps(),
		f.CopyWithoutAssigningPlaceholders,
	)
	opc.optimizer.Memo().Metadata().UpdateTableMeta(hypTables)
	if _, err := opc.optimizer.Optimize(); err != nil {
		return err
	}

	indexRecommendations := indexrec.FindIndexRecommendationSet(f.Memo().RootExpr(), f.Metadata())
	opc.p.instrumentation.indexRecommendations = indexRecommendations.Output()

	// Re-initialize the optimizer (which also re-initializes the factory) and
	// update the saved memo's metadata with the original table information.
	// Prepare to re-optimize and create an executable plan.
	// 重新初始化优化器（也重新初始化工厂）并使用原始表信息更新保存的备忘录的元数据。
	// 准备重新优化并创建可执行计划。
	opc.optimizer.Init(f.EvalContext(), &opc.catalog)
	savedMemo.Metadata().UpdateTableMeta(optTables)
	f.CopyAndReplace(
		savedMemo.RootExpr().(memo.RelExpr),
		savedMemo.RootProps(),
		f.CopyWithoutAssigningPlaceholders,
	)

	return nil
}
