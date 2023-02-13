// Copyright 2019 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package growstack

// Grow grows the goroutine stack by 16 KB. Goroutine stacks currently start
// at 2 KB in size. The code paths through the storage package often need a
// stack that is 32 KB in size. The stack growth is mildly expensive making it
// useful to trick the runtime into growing the stack early. Since goroutine
// stacks grow in multiples of 2 and start at 2 KB in size, by placing a 16 KB
// object on the stack early in the lifetime of a goroutine we force the
// runtime to use a 32 KB stack for the goroutine.
// Grow 将 goroutine 堆栈增加 16 KB。 Goroutine 堆栈目前的起始大小为 2 KB。
// 通过存储包的代码路径通常需要 32 KB 大小的堆栈。
// 堆栈增长的代价是适度的，这使得欺骗运行时提前增长堆栈很有用。
// 由于 goroutine 堆栈以 2 的倍数增长并且大小从 2 KB 开始，
// 通过在 goroutine 的生命周期早期将 16 KB 的对象放在堆栈上，
// 我们强制运行时为 goroutine 使用 32 KB 的堆栈。
func Grow()
