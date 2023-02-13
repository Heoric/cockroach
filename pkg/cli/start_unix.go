// Copyright 2017 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

//go:build !windows
// +build !windows

package cli

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/cockroachdb/cockroach/pkg/util/log"
	"github.com/cockroachdb/cockroach/pkg/util/sdnotify"
	"github.com/cockroachdb/cockroach/pkg/util/sysutil"
	"golang.org/x/sys/unix"
)

// drainSignals are the signals that will cause the server to drain and exit.
// drainSignals 是将导致服务器耗尽并退出的信号。
//
// If two drain signals are seen, the second drain signal will be reraised
// without a signal handler. The default action of any signal listed here thus
// must terminate the process.
// 如果看到两个漏极信号，第二个漏极信号将在没有信号处理程序的情况下重新发出。
// 因此，此处列出的任何信号的默认操作都必须终止该过程。
var drainSignals = []os.Signal{unix.SIGINT, unix.SIGTERM}

// termSignal is the signal that causes an idempotent graceful
// shutdown (i.e. second occurrence does not incur hard shutdown).
// termSignal 是导致幂等正常关闭的信号（即第二次出现不会导致硬关闭）。
var termSignal os.Signal = unix.SIGTERM

// quitSignal is the signal to recognize to dump Go stacks.
// quitSignal 是识别转储 Go 堆栈的信号。
var quitSignal os.Signal = unix.SIGQUIT

// debugSignal is the signal to open a pprof debugging server.
// debugSignal 是打开 pprof 调试服务器的信号。
var debugSignal os.Signal = unix.SIGUSR2

func handleSignalDuringShutdown(sig os.Signal) {
	// On Unix, a signal that was not handled gracefully by the application
	// should be reraised so it is visible in the exit code.
	// 在 Unix 上，应用程序未妥善处理的信号应重新引发，以便在退出代码中可见。

	// Reset signal to its original disposition.
	// 将信号重置为其原始配置。
	signal.Reset(sig)

	// Reraise the signal. os.Signal is always sysutil.Signal.
	// 重新发出信号。 os.Signal 始终是 sysutil.Signal。
	if err := unix.Kill(unix.Getpid(), sig.(sysutil.Signal)); err != nil {
		// Sending a valid signal to ourselves should never fail.
		// 向我们自己发送有效信号永远不会失败。
		//
		// Unfortunately it appears (#34354) that some users
		// run CockroachDB in containers that only support
		// a subset of all syscalls. If this ever happens, we
		// still need to quit immediately.
		// 不幸的是 (#34354) 一些用户在仅支持所有系统调用的子集的容器中运行 CockroachDB。
		// 如果发生这种情况，我们仍然需要立即退出。
		log.Fatalf(context.Background(), "unable to forward signal %v: %v", sig, err)
	}

	// Block while we wait for the signal to be delivered.
	// 在我们等待信号传递时阻塞。
	select {}
}

const backgroundFlagDefined = true

func maybeRerunBackground() (bool, error) {
	if startBackground {
		args := make([]string, 0, len(os.Args))
		foundBackground := false
		for _, arg := range os.Args {
			if arg == "--background" || strings.HasPrefix(arg, "--background=") {
				foundBackground = true
				continue
			}
			args = append(args, arg)
		}
		if !foundBackground {
			args = append(args, "--background=false")
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = stderr

		// Notify to ourselves that we're restarting.
		_ = os.Setenv(backgroundEnvVar, "1")

		return true, sdnotify.Exec(cmd)
	}
	return false, nil
}

func disableOtherPermissionBits() {
	mask := unix.Umask(0000)
	mask |= 00007
	_ = unix.Umask(mask)
}
