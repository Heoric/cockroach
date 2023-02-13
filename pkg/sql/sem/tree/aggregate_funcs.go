// Copyright 2017 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package tree

import "context"

// AggregateFunc accumulates the result of a function of a Datum.
type AggregateFunc interface {
	// Add accumulates the passed datums into the AggregateFunc.
	// Most implementations require one and only one firstArg argument.
	// If an aggregate function requires more than one argument,
	// all additional arguments (after firstArg) are passed in as a
	// variadic collection, otherArgs.
	// Add 将传递的数据累积到 AggregateFunc 中。 大多数实现只需要一个 firstArg 参数。
	// 如果聚合函数需要多个参数，则所有附加参数（在 firstArg 之后）都作为可变参数集合 otherArgs 传入。
	// This interface (as opposed to `args ...Datum`) avoids unnecessary
	// allocation of otherArgs in the majority of cases.
	// 此接口（与 `args ...Datum` 相对）在大多数情况下避免了不必要的 otherArgs 分配。
	Add(_ context.Context, firstArg Datum, otherArgs ...Datum) error

	// Result returns the current value of the accumulation. This value
	// will be a deep copy of any AggregateFunc internal state, so that
	// it will not be mutated by additional calls to Add.
	// Result 返回当前的累加值。 此值将是任何 AggregateFunc 内部状态的深层副本，
	// 因此它不会因对 Add 的额外调用而发生变化。
	Result() (Datum, error)

	// Reset resets the aggregate function which allows for reusing the same
	// instance for computation without the need to create a new instance.
	// Any memory is kept, if possible.
	// Reset 重置聚合函数，允许在不需要创建新实例的情况下重复使用相同的实例进行计算。
	// 如果可能的话，所有的记忆都会被保留下来。
	Reset(context.Context)

	// Close closes out the AggregateFunc and allows it to release any memory it
	// requested during aggregation, and must be called upon completion of the
	// aggregation.
	// Close 关闭 AggregateFunc 并允许它释放它在聚合期间请求的任何内存，并且必须在聚合完成时调用。
	Close(context.Context)

	// Size returns the size of the AggregateFunc implementation in bytes. It
	// does *not* account for additional memory used during accumulation.
	// Size 以字节为单位返回 AggregateFunc 实现的大小。 它*不*考虑累积期间使用的额外内存。
	Size() int64
}
