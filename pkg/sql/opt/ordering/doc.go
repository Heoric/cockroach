// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

/*
Package ordering contains operator-specific logic related to orderings - whether
ops can provide Required orderings, what orderings do they need to require from
their children, etc.
包排序包含与排序相关的特定于运营商的逻辑——运营商是否可以提供必需的排序，他们需要从他们的孩子那里获得什么排序，等等。

The package provides generic APIs that can be called on any RelExpr, as well as
operator-specific APIs in some cases.
该包提供可在任何 RelExpr 上调用的通用 API，以及在某些情况下特定于运算符的 API。

Required orderings

A Required ordering is part of the physical properties with respect to which an
expression was optimized. It effectively describes a set of orderings, any of
which are acceptable. See OrderingChoice for more information on how this set is
represented.
必需的排序是表达式优化所依据的物理属性的一部分。 它有效地描述了一组顺序，其中任何一个都是可接受的。
有关此集合如何表示的更多信息，请参阅 OrderingChoice。

An operator can provide a Required ordering if it can guarantee its results
respect at least one ordering in the OrderingChoice set, perhaps by requiring
specific orderings of its inputs and/or configuring its execution in a specific
way. This package implements the logic that decides whether each operator can
provide a Required ordering, as well as what Required orderings on its input(s)
are necessary.
如果运算符可以保证其结果至少遵守 OrderingChoice 集中的一个顺序，则可以提供 Required 顺序，
可能是通过要求其输入的特定顺序和/或以特定方式配置其执行。
该包实现了决定每个运算符是否可以提供 Required 排序的逻辑，以及其输入的哪些 Required 排序是必需的。

Provided orderings

In a single-node serial execution model, the Required ordering would be
sufficient to configure execution. But in a distributed setting, even if an
operator logically has a natural ordering, when multiple instances of that
operator are running on multiple nodes we must do some extra work (row
comparisons) to maintain their natural orderings when their results merge into a
single node. We must know exactly what order must be maintained on the streams
(i.e. along which columns we should perform the comparisons).
在单节点串行执行模型中，Required 顺序足以配置执行。
但是在分布式设置中，即使运算符在逻辑上具有自然顺序，当该运算符的多个实例在多个节点上运行时，
我们必须做一些额外的工作（行比较）以在它们的结果合并到单个节点时保持它们的自然顺序 .
我们必须确切地知道必须在流上维护什么顺序（即我们应该沿着哪些列执行比较）。

Consider a Scan operator that is scanning an index on a,b. In query:
考虑一个正在扫描 a,b 上的索引的 Scan 运算符。 在查询中：
  SELECT a, b FROM abc ORDER BY a, b
the Scan has Required ordering "+a,+b". Now consider another case where (as part
of some more complicated query) we have the same Scan operator but with Required
ordering "+b opt(a)"¹, which means that any of "+b", "+b,±a", "±a,+b" are
acceptable. Execution would still need to be configured with "+a,+b" because
that is the ordering for the rows that are produced², but this information is
not available from the Required ordering "+b opt(a)".
扫描需要排序“+a，+b”。 现在考虑另一种情况，其中（作为一些更复杂的查询的一部分）我们有相同的 Scan 运算符，
但具有 Required ordering "+b opt(a)"¹，这意味着任何 "+b"、"+b,±a ", "±a,+b" 是可以接受的。
执行仍需要使用“+a,+b”进行配置，因为这是生成的行的排序²，但此信息无法从所需的排序“+b opt(a)”获得。

¹This could for example happen under a Select with filter "a=1".
²For example, imagine that node A produces rows (1,4), (2,1) and node B produces
rows (1,2), (2,3). If these results need to be merged on a single node and we
configure execution to "maintain" an ordering of +b, it will cause an incorrect
ordering or a runtime error.
¹例如，这可能发生在带有过滤器“a=1”的选择下。
²例如，假设节点 A 生成行 (1,4)、(2,1)，节点 B 生成行 (1,2)、(2,3)。
如果这些结果需要在单个节点上合并，并且我们将执行配置为“保持”+b 的排序，则会导致排序不正确或运行时错误。

To address this issue, this package implements logic to calculate Provided
orderings for each expression in the lowest-cost tree. Provided orderings are
calculated bottom-up, in conjunction with the Required ordering at the level of
each operator.
为了解决这个问题，这个包实现了计算最低成本树中每个表达式的 Provided 顺序的逻辑。
提供的排序是自下而上计算的，并结合每个运算符级别的必需排序。

The Provided ordering is a specific opt.Ordering which describes the ordering
produced by the operator, and which intersects the Required OrderingChoice (when
the operator's FDs are taken into account). A best-effort attempt is made to
keep the Provided ordering as simple as possible, to minimize the comparisons
that are necessary to maintain it.
Provided 排序是一个特定的 opt.Ordering，它描述了运算符生成的排序，
并且与 Required OrderingChoice 相交（当考虑到运算符的 FD 时）。
尽最大努力使提供的顺序尽可能简单，以尽量减少维护它所需的比较。
*/
package ordering
