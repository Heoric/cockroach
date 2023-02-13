// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package tree

import "github.com/cockroachdb/errors"

// Volatility indicates whether the result of a function is dependent *only*
// on the values of its explicit arguments, or can change due to outside factors
// (such as parameter variables or table contents).
// 波动性表示函数的结果是否*仅*取决于其显式参数的值，或者是否会因外部因素（例如参数变量或表内容）而发生变化。
//
// The values are ordered with smaller values being strictly more restrictive
// than larger values.
// 这些值按较小的值排序，比较大的值严格地更具限制性。
//
// NOTE: functions having side-effects, such as setval(),
// must be labeled volatile to ensure they will not get optimized away,
// even if the actual return value is not changeable.
// 注意：具有副作用的函数，例如 setval()，必须标记为 volatile 以确保它们不会被优化掉，
// 即使实际返回值不可更改。
type Volatility int8

const (
	// VolatilityLeakProof means that the operator cannot modify the database, the
	// transaction state, or any other state. It cannot depend on configuration
	// settings and is guaranteed to return the same results given the same
	// arguments in any context. In addition, no information about the arguments
	// is conveyed except via the return value. Any function that might throw an
	// error depending on the values of its arguments is not leak-proof.
	// VolatilityLeakProof 意味着操作者不能修改数据库、交易状态或任何其他状态。
	// 它不能依赖于配置设置，并且保证在任何上下文中给定相同的参数返回相同的结果。
	// 此外，除了通过返回值外，不传递任何关于参数的信息。 任何可能根据其参数值抛出错误的函数都不是防漏的。
	//
	// USE THIS WITH CAUTION! The optimizer might call operators that are leak
	// proof on inputs that they wouldn't normally be called on (e.g. pulling
	// expressions out of a CASE). In the future, they may even run on rows that
	// the user doesn't have permission to access.
	// 小心使用！ 优化器可能会调用在通常不会被调用的输入上防漏的运算符（例如，从 CASE 中提取表达式）。
	// 将来，它们甚至可能在用户无权访问的行上运行。
	//
	// Note: VolatilityLeakProof is strictly stronger than VolatilityImmutable. In
	// principle it could be possible to have leak-proof stable or volatile
	// functions (perhaps now()); but this is not useful in practice as very few
	// operators are marked leak-proof.
	// Examples: integer comparison.
	// 注意：VolatilityLeakProof 严格强于 VolatilityImmutable。
	// 原则上，可以有防泄漏的稳定或易变函数（也许是 now()）；但这在实践中没有用，因为很少有操作员被标记为防漏。
	// 示例：整数比较。
	VolatilityLeakProof Volatility = 1 + iota
	// VolatilityImmutable means that the operator cannot modify the database, the
	// transaction state, or any other state. It cannot depend on configuration
	// settings and is guaranteed to return the same results given the same
	// arguments in any context. ImmutableCopy operators can be constant folded.
	// Examples: log, from_json.
	// VolatilityImmutable 意味着操作者不能修改数据库、事务状态或任何其他状态。
	// 它不能依赖于配置设置，并且保证在任何上下文中给定相同的参数返回相同的结果。
	// ImmutableCopy 运算符可以常量折叠。 示例：日志、from_json。
	VolatilityImmutable
	// VolatilityStable means that the operator cannot modify the database or the
	// transaction state and is guaranteed to return the same results given the
	// same arguments whenever it is evaluated within the same statement. Multiple
	// calls to a stable operator can be optimized to a single call.
	// Examples: current_timestamp, current_date.
	// VolatilityStable 意味着运算符不能修改数据库或事务状态，
	// 并且只要在同一语句中进行评估，就可以保证在给定相同参数的情况下返回相同的结果。
	// 对稳定运营商的多次调用可以优化为一次调用。 示例：current_timestamp、current_date。
	VolatilityStable
	// VolatilityVolatile means that the operator can do anything, including
	// modifying database state.
	// Examples: random, crdb_internal.force_error, nextval.
	// VolatilityVolatile 意味着操作员可以做任何事情，包括修改数据库状态。
	// 示例：random、crdb_internal.force_error、nextval。
	VolatilityVolatile
)

// String returns the byte representation of Volatility as a string.
func (v Volatility) String() string {
	switch v {
	case VolatilityLeakProof:
		return "leak-proof"
	case VolatilityImmutable:
		return "immutable"
	case VolatilityStable:
		return "stable"
	case VolatilityVolatile:
		return "volatile"
	default:
		return "invalid"
	}
}

// TitleString returns the byte representation of Volatility as a title-cased
// string.
func (v Volatility) TitleString() string {
	switch v {
	case VolatilityLeakProof:
		return "Leakproof"
	case VolatilityImmutable:
		return "Immutable"
	case VolatilityStable:
		return "Stable"
	case VolatilityVolatile:
		return "Volatile"
	default:
		return "Invalid"
	}
}

// ToPostgres returns the postgres "provolatile" string ("i" or "s" or "v") and
// the "proleakproof" flag.
func (v Volatility) ToPostgres() (provolatile string, proleakproof bool) {
	switch v {
	case VolatilityLeakProof:
		return "i", true
	case VolatilityImmutable:
		return "i", false
	case VolatilityStable:
		return "s", false
	case VolatilityVolatile:
		return "v", false
	default:
		panic(errors.AssertionFailedf("invalid volatility %s", v))
	}
}

// VolatilityFromPostgres returns a Volatility that matches the postgres
// provolatile/proleakproof settings.
func VolatilityFromPostgres(provolatile string, proleakproof bool) (Volatility, error) {
	switch provolatile {
	case "i":
		if proleakproof {
			return VolatilityLeakProof, nil
		}
		return VolatilityImmutable, nil
	case "s":
		return VolatilityStable, nil
	case "v":
		return VolatilityVolatile, nil
	default:
		return 0, errors.AssertionFailedf("invalid provolatile %s", provolatile)
	}
}
