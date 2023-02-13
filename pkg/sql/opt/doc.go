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
Package opt contains the Cockroach SQL optimizer. The optimizer transforms the
AST of a SQL query into a physical query plan for execution. Naive execution of
a SQL query can be prohibitively expensive, because SQL specifies the desired
results and not how to achieve them. A given SQL query can have thousands of
equivalent query plans with vastly different execution times. The Cockroach
optimizer is cost-based, meaning that it enumerates some or all of these
alternate plans and chooses the one with the lowest estimated cost.

包 opt 包含 Cockroach SQL 优化器。 优化器将 SQL 查询的 AST 转换为物理查询计划以供执行。
SQL 查询的幼稚执行可能会非常昂贵，因为 SQL 指定了所需的结果，而不是如何实现它们。
一个给定的 SQL 查询可以有数千个执行时间大不相同的等效查询计划。
Cockroach 优化器是基于成本的，这意味着它会枚举部分或全部这些替代计划，并选择估计成本最低的一个。

Overview

SQL query planning is often described in terms of 8 modules:

1. Properties

2. Stats

3. Cost Model

4. Memo

5. Transforms

6. Prep

7. Rewrite

8. Search

Note Prep, Rewrite and Search could be considered phases, though this document
will refer to all 8 uniformly as modules. Memo is a technique for compactly
representing the forest of trees generated during Search. Stats, Properties,
Cost Model and Transformations are modules that power the Prep, Rewrite and
Search phases.

注意 Prep、Rewrite 和 Search 可以被视为阶段，尽管本文档将所有 8 个统一称为模块。
memo 是一种紧凑地表示 Search 期间生成的树木森林的技术。
Stats、Properties、Cost Model 和 Transforms 是为 Prep、Rewrite 和 Search 阶段提供动力的模块。

                       SQL query text
                             |
                       +-----v-----+  - parse SQL text according to grammar
                       |   Parse   |  - report syntax errors
                       +-----+-----+    根据语法解析 SQL 文本。报告语法错误
                             |
                           (ast)
                             |
                       +-----v-----+  - fold constants, check types, resolve
                       |  Analyze  |    names
                       +-----+-----+  - annotate tree with semantic info
                             |        - report semantic errors
                           (ast+)
         +-------+           |
         | Stats +----->-----v-----+  - normalize tree with cost-agnostic
         +-------+     |   Prep    |    transforms (placeholders present)
                    +-->-----+-----+  - compute initial properties
                    |        |        - retrieve and attach stats
                    |     (expr)      - done once per PREPARE
                    |        |
    +------------+  |  +-----v-----+  - capture placeholder values / timestamps
    | Transforms |--+-->  Rewrite  |  - normalize tree with cost-agnostic
    +------------+  |  +-----+-----+    transforms (placeholders not present)
                    |        |        - done once per EXECUTE
                    |     (expr)
                    |        |
                    +-->-----v-----+  - generate equivalent expression trees
    +------------+     |  Search   |  - find lowest cost physical plan
    | Cost Model +----->-----+-----+  - includes DistSQL physical planning
    +------------+           |
                      (physical plan)
                             |
                       +-----v-----+
                       | Execution |
                       +-----------+

The opt-related packages implement portions of these modules, while other parts
are implemented elsewhere. For example, other sql packages are used to perform
name resolution and type checking which are part of the Analyze phase.
-- opt 相关的包实现了这些模块的一部分，而其他部分则在其他地方实现。
-- 例如，其他 sql 包用于执行名称解析和类型检查，这是分析阶段的一部分。

Parse

The parse phase is not discussed in this document. It transforms the SQL query
text into an abstract syntax tree (AST).
parse 将sql 文本转换为抽象语法树 AST

Analyze

The analyze phase ensures that the AST obeys all SQL semantic rules, and
annotates the AST with information that will be used by later phases. In
addition, some simple transforms are applied to the AST in order to simplify
handling in later phases. Semantic rules are many and varied; this document
describes a few major categories of semantic checks and rewrites.
分析阶段确保 AST 遵守所有 SQL 语义规则，并使用稍后阶段将使用的信息对 AST 进行注释。
此外，为了简化后续阶段的处理，对 AST 应用了一些简单的转换。
语义规则多种多样； 本文档描述了几个主要类别的语义检查和重写。

"Name resolution" binds table, column, and other references. Each name must be
matched to the appropriate schema object, and an error reported if no matching
object can be found. Name binding can result in AST annotations that make it
easy for other components to find the target object, or rewrites that replace
unbound name nodes with new nodes that are easier to handle (e.g. IndexedVar).
“名称解析”绑定表、列和其他引用。 每个名称都必须与相应的模式对象匹配，如果找不到匹配的对象，则会报告错误。
名称绑定可以导致 AST 注释，使其他组件更容易找到目标对象，
或者重写以更易于处理的新节点（例如 IndexedVar）替换未绑定的名称节点。

"Constant folding" rewrites expressions that have constant inputs. For example,
1+1 would be folded to 2. Cockroach's typing rules assume that constants have
been folded, as there are some expressions that would otherwise produce a
semantic error if they are not first folded.
“常量折叠”重写具有常量输入的表达式。 例如，1+1 将被折叠成 2。
Cockroach 的 类型 规则假定常量已经被折叠，因为有些表达式如果不首先折叠会产生语义错误。

"Type inference" automatically determines the return data type of various SQL
expressions, based on the types of inputs, as well as the context in which the
expression is used. The AST is annotated with the resolved types for later use.
“类型推断”根据输入的类型以及使用表达式的上下文自动确定各种 SQL 表达式的返回数据类型。
AST 使用解析的类型进行注释以供以后使用。

"Type checking" ensures that all inputs to SQL expressions and statements have
legal static types. For example, the CONCAT function only accepts zero or more
arguments that are statically typed as strings. Violation of the typing rules
produces a semantic error.
“类型检查”确保 SQL 表达式和语句的所有输入都具有合法的静态类型。
例如，CONCAT 函数只接受零个或多个静态类型为字符串的参数。 违反打字规则会产生语义错误。


Properties

Properties are meta-information that are computed (and sometimes stored) for
each node in an expression. Properties power transformations and optimization.
属性是为表达式中的每个节点计算（有时存储）的元信息。 属性 驱动 转换和优化。

"Logical properties" describe the structure and content of data returned by an
expression, such as whether relational output columns can contain nulls, or the
data type of a scalar expression. Two expressions which are logically
equivalent according to the rules of the relational algebra will return the
same set of rows and columns, and will have the same set of logical properties.
However, the order of the rows, naming of the columns, and other presentational
aspects of the result are not governed by the logical properties.
“逻辑属性”描述了表达式返回的数据的结构和内容，例如关系输出列是否可以包含空值，或者标量表达式的数据类型。
根据关系代数规则逻辑等价的两个表达式将返回相同的行和列集，并且具有相同的逻辑属性集。
但是，行的顺序、列的命名以及结果的其他表示方面不受逻辑属性的支配。

"Physical properties" are interesting characteristics of an expression that
impact its layout, presentation, or location, but not its logical content.
Examples include row order, column naming, and data distribution (physical
location of data ranges). Physical properties exist outside of the relational
algebra, and arise from both the SQL query itself (e.g. the non-relational
ORDER BY operator) and by the selection of specific implementations during
optimization (e.g. a merge join requires the inputs to be sorted in a
particular order).
“物理属性”是表达式的有趣特征，会影响其布局、表示或位置，但不会影响其逻辑内容。
示例包括行顺序、列命名和数据分布（数据范围的物理位置）。
物理属性存在于关系代数之外，并且来自 SQL 查询本身（例如，非关系 ORDER BY 运算符）
和优化期间特定实现的选择（a merge join requires the inputs to be sorted in a
particular order）。

Properties can be "required" or "derived". A required property is one specified
by the SQL query text. For example, a DISTINCT clause is a required property on
the set of columns of the corresponding projection -- that the tuple of columns
forms a key (unique values) in the results. A derived property is one derived
by the optimizer for an expression based on the properties of the child
expressions. For example:
属性可以是“必需的”或“派生的”。 必需属性是由 SQL 查询文本指定的属性。
例如，DISTINCT 子句是相应投影的列集上的必需属性——列的元组在结果中形成一个键（唯一值）。
派生属性是优化器根据子表达式的属性为表达式派生的属性。 例如：

  SELECT k+1 FROM kv

Once the ordering of "k" is known from kv's descriptor, the same ordering
property can be derived for k+1. During optimization, for each expression with
required properties, the optimizer will look at child expressions to check
whether their actual properties (which can be derived) match the requirement.
If they don't, the optimizer must introduce an "enforcer" operator in the plan
that provides the required property. For example, an ORDER BY clause creates a
required ordering property that can cause the optimizer to add a Sort operator
as an enforcer of that property.
一旦从 kv 的描述符中知道了“k”的排序，就可以为 k+1 导出相同的排序属性。
在优化过程中，对于每个具有所需属性的表达式，优化器将查看子表达式以检查其实际属性（可以派生）是否符合要求。
如果他们不这样做，优化器必须在提供所需属性的计划中引入 “enforcer” 运算符。
例如，一个 ORDER BY 子句创建一个必需的排序属性，它可以导致优化器添加一个排序运算符作为该属性的强制执行器。

Stats

Table statistics power both the cost model and the search of alternate query
plans. A simple example of where statistics guide the search of alternate query
plans is in join ordering:
表统计信息支持 成本模型 和 搜索替代查询计划。
一个简单的示例说明统计信息指导替代查询计划的搜索是在连接排序中：

	SELECT * FROM a JOIN b

In the absence of other opportunities, this might be implemented as a hash
join. With a hash join, we want to load the smaller set of rows (either from a
or b) into the hash table and then query that table while looping through the
larger set of rows. How do we know whether a or b is larger? We keep statistics
about the cardinality of a and b, i.e. the (approximate) number of different
values.
在没有其他机会的情况下，这可以作为 hash join 来实现。
使用哈希联接，我们希望将较小的行集（来自 a 或 b）加载到哈希表中，然后在遍历较大的行集时查询该表。
我们如何知道 a 或 b 是否更大？ 我们保留关于 a 和 b 的基数的统计信息，即不同值的（近似）数量。


Simple table cardinality is sufficient for the above query but fails in other
queries. Consider:
简单的表基数对于上述查询就足够了，但在其他查询中失败。 考虑：

	SELECT * FROM a JOIN b ON a.x = b.x WHERE a.y > 10

Table statistics might indicate that a contains 10x more data than b, but the
predicate a.y > 10 is filtering a chunk of the table. What we care about is
whether the result of the scan *after* filtering returns more rows than the
scan of b. This can be accomplished by making a determination of the
selectivity of the predicate a.y > 10 (the % of rows it will filter) and then
multiplying that selectivity by the cardinality of a. The common technique for
estimating selectivity is to collect a histogram on a.y.
表统计信息可能表明 a 包含的数据比 b 多 10 倍，但谓词 a.y > 10 正在过滤表的一个块。
我们关心的是scan *after*过滤的结果是否比b的scan返回的行多。
这可以通过确定谓词 a.y > 10 的选择性（它将过滤的行的百分比）然后将该选择性乘以 a 的基数来实现。
估计选择性的常用技术是在 a.y 上收集直方图。

The collection of table statistics occurs prior to receiving the query. As
such, the statistics are necessarily out of date and may be inaccurate. The
system may bound the inaccuracy by recomputing the stats based on how fast a
table is being modified. Or the system may notice when stat estimations are
inaccurate during query execution.
表统计信息的收集发生在接收查询之前。 因此，统计数据必然过时并且可能不准确。
系统可以通过基于修改表的速度重新计算统计数据来限制不准确性。 或者系统可能会在查询执行期间注意到统计估计不准确。

Cost Model

The cost model takes an expression as input and computes an estimated "cost"
to execute that expression. The unit of "cost" can be arbitrary, though it is
desirable if it has some real world meaning such as expected execution time.
What is required is for the costs of different query plans to be comparable.
The optimizer seeks to find the shortest expected execution time for a query
and uses cost as a proxy for execution time.
成本模型将表达式作为输入并计算估计的“成本”以执行该表达式。
“成本”的单位可以是任意的，但如果它具有一些现实世界的意义，例如预期的执行时间，则它是可取的。
需要的是不同查询计划的成本具有可比性。 优化器寻求为查询找到最短的预期执行时间，并使用成本作为执行时间的代理。


Cost is roughly calculated by estimating how much time each node in the
expression tree will use to process all results and modeling how data flows
through the expression tree. Table statistics are used to power cardinality
estimates of base relations which in term power cardinality estimates of
intermediate relations. This is accomplished by propagating histograms of
column values from base relations up through intermediate nodes (e.g. combining
histograms from the two join inputs into a single histogram). Operator-specific
computations model the network, disk and CPU costs. The cost model should
include data layout and the specific operating environment. For example,
network RTT in one cluster might be vastly different than another.
通过估计表达式树中的每个节点将使用多少时间来处理所有结果并对数据如何流经表达式树进行建模来粗略计算成本。
表统计数据用于对基本关系的基数估计进行功率基数估计，这在术语中是对中间关系的功率基数估计。
这是通过将列值的直方图从基本关系向上传播到中间节点来实现的（例如，将来自两个连接输入的直方图组合成一个直方图）。
特定于运营商的计算对网络、磁盘和 CPU 成本进行建模。 成本模型应包括数据布局和具体的操作环境。
例如，一个集群中的网络 RTT 可能与另一个集群大不相同。

Because the cost for a query plan is an estimate, there is an associated error.
This error might be implicit in the cost, or could be explicitly tracked. One
advantage to explicitly tracking the expected error is that it can allow
selecting a higher cost but lower expected error plan over a lower cost but
higher expected error plan. Where does the error come from? One source is the
innate inaccuracy of stats: selectivity estimation might be wildly off due to
an outlier value. Another source is the accumulated build up of estimation
errors the higher up in the query tree. Lastly, the cost model is making an
estimation for the execution time of an operation such as a network RTT. This
estimate can also be wildly inaccurate due to bursts of activity.
因为查询计划的成本是估计值，所以存在相关错误。 此错误可能隐含在成本中，或者可以明确跟踪。
显式跟踪预期错误的一个优点是，它可以允许选择成本较高但预期错误较低的计划，而不是成本较低但预期错误较高的计划。
错误来自哪里？ 一个来源是统计数据与生俱来的不准确性：选择性估计可能由于异常值而严重偏离。
另一个来源是查询树中越高的估计误差的累积累积。 最后，成本模型对网络 RTT 等操作的执行时间进行估计。
由于活动的爆发，这一估计也可能非常不准确。

Search finds the lowest cost plan using dynamic programming. That imposes a
restriction on the cost model: it must exhibit optimal substructure. An optimal
solution can be constructed from optimal solutions of its sub-problems.
搜索使用动态规划查找成本最低的计划。 这对成本模型施加了限制：它必须表现出最优的子结构。
可以从其子问题的最优解构造最优解。

Memo

Memo is a data structure for efficiently storing a forest of query plans.
Conceptually, the memo is composed of a numbered set of equivalency classes
called groups where each group contains a set of logically equivalent
expressions. The different expressions in a single group are called memo
expressions (memo-ized expressions). A memo expression has a list of child
groups as its children rather than a list of individual expressions. The
forest is composed of every possible combination of parent expression with
its children, recursively applied.
备忘录是一种用于有效存储查询计划森林的数据结构。
从概念上讲，备忘录由一组编号的等价类组成，称为组，其中每个组包含一组逻辑上等价的表达式。
单个组中的不同表达式称为备忘录表达式（memo-ized expressions）。
备忘录表达式有一个子组列表作为其子项，而不是单个表达式的列表。
森林由父表达式及其子表达式的所有可能组合组成，递归应用。

Memo expressions can be relational (e.g. join) or scalar (e.g. <). Operators
are always both logical (specify results) and physical (specify results and a
particular implementation). This means that even a "raw" unoptimized expression
tree can be executed (naively). Both relational and scalar operators are
uniformly represented as nodes in memo expression trees, which facilitates tree
pattern matching and replacement
备忘录表达式可以是关系的（例如 join）或标量的（例如 <）。
运算符始终是逻辑的（指定结果）和物理的（指定结果和特定实现）。
这意味着即使是“原始”未优化的表达式树也可以（天真地）执行。
关系运算符和标量运算符都统一表示为备忘录表达式树中的节点，这有助于树模式匹配和替换。.

Because memo groups contain logically equivalent expressions, all the memo
expressions in a group share the same logical properties. However, it's
possible for two logically equivalent expressions to be placed in different
memo groups. This occurs because determining logical equivalency of two
relational expressions is too complex to perform 100% correctly. A correctness
failure (i.e. considering two expressions logically equivalent when they are
not) results in invalid transformations and invalid plans. But placing two
logically equivalent expressions in different groups has a much gentler failure
mode: the memo and transformations are less efficient. Expressions within the
memo may have different physical properties. For example, a memo group might
contain both hash join and merge join expressions which produce the same set of
output rows, but produce them in different orders.
因为备忘录组包含逻辑上等价的表达式，所以组中的所有备忘录表达式共享相同的逻辑属性。
但是，可以将两个逻辑上等价的表达式放在不同的备忘录组中。
这是因为确定两个关系表达式的逻辑等价性太复杂而无法 100% 正确执行。
正确性失败（即认为两个表达式在逻辑上不等价时）会导致无效的转换和无效的计划。
但是将两个逻辑上等价的表达式放在不同的组中会有一个更温和的失败模式：备忘录和转换效率较低。
备忘录中的表述可能具有不同的物理特性。
例如，一个备忘录组可能包含哈希连接和合并连接表达式，它们产生相同的一组输出行，但以不同的顺序产生它们。


Expressions are inserted into the memo by the factory, which ensure that
expressions have been fully normalized before insertion (see the Prep section
for more details). A new group is created only when unique normalized
expressions are created by the factory during construction or rewrite of the
tree. Uniqueness is determined by computing the fingerprint for a memo
expression, which is simply the expression operator and its list of child
groups. For example, consider this query:
表达式由工厂插入到备忘录中，这确保表达式在插入之前已经完全规范化（有关更多详细信息，请参阅准备部分）。
仅当工厂在构造或重写树期间创建唯一的规范化表达式时，才会创建新组。
唯一性是通过计算备忘录表达式的指纹来确定的，它只是表达式运算符及其子组列表。 例如，考虑这个查询：

	SELECT * FROM a, b WHERE a.x = b.x

After insertion into the memo, the memo would contain these six groups:

	6: [inner-join [1 2 5]]
	5: [eq [3 4]]
	4: [variable b.x]
	3: [variable a.x]
	2: [scan b]
	1: [scan a]

The fingerprint for the inner-join expression is [inner-join [1 2 5]]. The
memo maintains a map from expression fingerprint to memo group which allows
quick determination of whether the normalized form of an expression already
exists in the memo.
内连接表达式的指纹是 [inner-join [1 2 5]]。
备忘录维护从表达式指纹到备忘录组的映射，这允许快速确定表达式的规范化形式是否已经存在于备忘录中。

The normalizing factory will never add more than one expression to a memo
group. But the explorer (see Search section for more details) does add
denormalized expressions to existing memo groups, since oftentimes one of these
equivalent, but denormalized expressions will have a lower cost than the
initial normalized expression added by the factory. For example, the join
commutativity transformation expands the memo like this:
规范化工厂永远不会向一个备忘录组添加一个以上的表达式。
但是资源管理器（有关更多详细信息，请参阅搜索部分）确实将非规范化表达式添加到现有的备忘录组，
因为通常这些等价的表达式之一，但非规范化表达式将比工厂添加的初始规范化表达式具有更低的成本。
例如，join commutativity transformation 将 memo 扩展如下：

	6: [inner-join [1 2 5]] [inner-join [2 1 5]]
	5: [eq [3 4]]
	4: [variable b.x]
	3: [variable a.x]
	2: [scan b]
	1: [scan a]

Notice that there are now two expressions in memo group 6. The coster (see Cost
Model section for more details) will estimate the execution cost of each
expression, and the optimizer will select the lowest cost alternative.
请注意，备忘录组 6 中现在有两个表达式。成本器（有关详细信息，请参阅成本模型部分）
将估计每个表达式的执行成本，优化器将选择成本最低的替代方案。

Transforms

Transforms convert an input expression tree into zero or more logically
equivalent trees. Transforms consist of two parts: a "match pattern" and a
"replace pattern". Together, the match pattern and replace pattern are called a
"rule". Transform rules are categorized as "normalization" or "exploration"
rules.
转换将输入表达式树转换为零个或多个逻辑等效树。 转换由两部分组成：“匹配模式”和“替换模式”。
匹配模式和替换模式一起称为“规则”。 转换规则被归类为“规范化”或“探索”规则。

If an expression in the tree matches the match pattern, then a new expression
will be constructed according to the replace pattern. Note that "replace" means
the new expression is a logical replacement for the existing expression, not
that the existing expression needs to physically be replaced. Depending on the
context, the existing expression may be discarded, or it may be retained side-
by-side with the new expression in the memo group.
如果树中的表达式与匹配模式匹配，则将根据替换模式构造一个新的表达式。
请注意，“替换”表示新表达式是现有表达式的逻辑替换，而不是现有表达式需要物理替换。
根据上下文，现有表达式可能会被丢弃，或者它可能与备忘录组中的新表达式并排保留。

Normalization rules are cost-agnostic, as they are always considered to be
beneficial. All normalization rules are implemented by the normalizing factory,
which does its best to map all logically equivalent expression trees to a
single canonical form from which searches can branch out. See the Prep section
for more details.
规范化规则与成本无关，因为它们总是被认为是有益的。
所有规范化规则都由规范化工厂实现，它尽最大努力将所有逻辑等效的表达式树映射到一个单一的规范形式，
搜索可以从该规范形式中分支出来。
有关详细信息，请参阅准备部分。

Exploration rules generate equivalent expression trees that must be costed in
order to determine the lowest cost alternative. All exploration rules are
implemented by the explorer, which is optimized to efficiently enumerate all
possible expression tree combinations in the memo in order to look for rule
matches. When it finds a match, the explorer applies the rule and adds an
equivalent expression to the existing memo group. See the Search section for
more details.
探索规则生成等价的表达式树，必须对其进行计算以确定成本最低的替代方案。
所有的探索规则都由 explorer 实现，它经过优化，可以有效地枚举备忘录中所有可能的表达式树组合，以查找规则匹配。
当它找到匹配项时，资源管理器应用规则并将等效表达式添加到现有的备忘录组。 有关详细信息，请参阅搜索部分。

Some examples of transforms:

	Join commutativity
	Swaps the order of the inputs to an inner join.
	join交换性
	将输入的顺序交换为内部联接。
		SELECT * FROM a, b => SELECT * FROM b, a

	Join associativity
	Reorders the children of a parent and child join
	join结合性
	重新排序父母和孩子的孩子 join
		SELECT * FROM (SELECT * FROM a, b), c
		=>
		SELECT * FROM (SELECT * FROM a, c), b

	Predicate pushdown
	Moves predicates below joins
	谓词下推
	将谓词移动到连接下方
		SELECT * FROM a, b USING (x) WHERE a.x < 10
		=>
		SELECT * FROM (SELECT * FROM a WHERE a.x < 10), b USING (x)

	Join elimination
	Removes unnecessary joins based on projected columns and foreign keys.
	join淘汰
	根据投影列和外键删除不必要的连接。
		SELECT a.x FROM a, b USING (x)
		=>
		SELECT a.x FROM a

	Distinct/group-by elimination
	Removes unnecessary distinct/group-by operations based on keys.
	Distinct/group-by消除
	删除基于键的不必要的Distinct/group-by操作。
		SELECT DISTINCT a.x FROM a
		=>
		SELECT a.x FROM a

	Predicate inference
	Adds predicates based on filter conditions.
	谓词推理
	添加基于过滤条件的谓词。
		SELECT * FROM a, b USING (x)
		=>
		SELECT * FROM a, b USING (x) WHERE a.x IS NOT NULL AND b.x IS NOT NULL

	Decorrelation
	Replaces correlated subqueries with semi-join, anti-join and apply ops.
	去相关
	用semi-join, anti-join和应用操作替换相关子查询。

	Scan to index scan
	Transforms scan operator into one or more index scans on covering indexes.
	将扫描运算符转换为覆盖索引上的一个或多个索引扫描。

	Inner join to merge join
	Generates alternate merge-join operator from default inner-join operator.
	从默认的内部联接运算符生成备用合并联接运算符。

Much of the optimizer's rule matching and application code is generated by a
tool called Optgen, short for "optimizer generator". Optgen is a domain-
specific language (DSL) that provides an intuitive syntax for defining
transform rules. Here is an example:
优化器的大部分规则匹配和应用程序代码是由一个名为 Optgen 的工具生成的，它是“优化器生成器”的缩写。
Optgen 是一种领域特定语言 (DSL)，它提供了用于定义转换规则的直观语法。 这是一个例子：

  [NormalizeEq]
  (Eq
    $left:^(Variable)
    $right:(Variable)
  )
  =>
  (Eq $right $left)

The expression above the arrow is the match pattern and the expression below
the arrow is the replace pattern. This example rule will match Eq expressions
which have a left input which is not a Variable operator and a right input
which is a Variable operator. The replace pattern will trigger a replacement
that reverses the two inputs. In addition, custom match and replace functions
can be defined in order to run arbitrary Go code.
箭头上方的表达式是匹配模式，箭头下方的表达式是替换模式。
此示例规则将匹配具有不是变量运算符的左输入和是变量运算符的右输入的 Eq 表达式。
替换模式将触发反转两个输入的替换。 此外，可以定义自定义匹配和替换函数以运行任意 Go 代码。

Prep

Prep (short for "prepare") is the first phase of query optimization, in which
the annotated AST is transformed into a single normalized "expression tree".
The optimizer directly creates the expression tree in the memo data structure
rather than first constructing an intermediate data structure. A forest of
equivalent trees will be generated in later phases, but at the end of the prep
phase, the memo contains just one normalized tree that is logically equivalent
to the SQL query.
Prep（“prepare”的缩写）是查询优化的第一阶段，其中带注释的 AST 被转换为单个规范化的“表达式树”。
优化器直接在 memo 数据结构中创建表达式树，而不是先构建一个中间数据结构。
稍后阶段将生成等效树的森林，但在准备阶段结束时，备忘录仅包含一个在逻辑上等效于 SQL 查询的规范化树。

During the prep phase, placeholder values are not yet known, so normalization
cannot go as far as it can during later phases. However, this also means that
the resulting expression tree can be cached in response to a PREPARE statement,
and then be reused as a starting point each time an EXECUTE statement provides
new placeholder values.
在准备阶段，占位符值尚不知道，因此标准化在后期阶段无法达到其所能达到的程度。
但是，这也意味着生成的表达式树可以被缓存以响应 PREPARE 语句，
然后在每次 EXECUTE 语句提供新的占位符值时将其用作起点。

The memo expression tree is constructed by the normalizing factory, which does
its best to map all logically equivalent expression trees to a single canonical
form from which searches can branch out. The factory has an interface similar
to this:
备忘录表达式树是由规范化工厂构建的，它尽最大努力将所有逻辑等效的表达式树映射到一个单一的规范形式，
搜索可以从该规范形式中分支出来。 工厂有一个类似这样的接口：

	ConstructConst(value PrivateID) GroupID
	ConstructAnd(conditions ListID) GroupID
	ConstructInnerJoin(left GroupID, right GroupID, on GroupID) GroupID

The factory methods construct a memo expression tree bottom-up, with each memo
group becoming an input to operators higher in the tree.
工厂方法自下而上构造一个备忘录表达式树，每个备忘录组成为树中较高层运算符的输入。

As each expression is constructed by the factory, it transitively applies
normalization rules defined for that expression type. This may result in the
construction of a different type of expression than what was requested. If,
after normalization, the expression is already part of the memo, then
construction is a no-op. Otherwise, a new memo group is created, with the
normalized expression as its first and only expression.
由于每个表达式都是由工厂构造的，因此它会传递地应用为该表达式类型定义的规范化规则。
这可能会导致构造与所请求的不同类型的表达式。
如果在规范化之后，表达式已经是备忘录的一部分，那么构造是一个空操作。
否则，将创建一个新的备忘录组，并将规范化表达式作为其第一个也是唯一的表达式。

By applying normalization rules as the expression tree is constructed, the
factory can avoid creating intermediate expressions; often, "replacement" of
an existing expression means it's never created to begin with.
通过在构造表达式树时应用规范化规则，工厂可以避免创建中间表达式；
通常，现有表达式的“替换”意味着它从未被创建。

During Prep, all columns used by the SQL query are given a numeric index that
is unique across the query. Column numbering involves assigning every base
column and non-trivial projection in a query a unique, query-specific index.
Giving each column a unique index allows the expression nodes mentioned above
to track input and output columns, or really any set of columns during Prep and
later phases, using a bitmap (FastIntSet). The bitmap representation allows
fast determination of compatibility between expression nodes and is utilized by
transforms to determine the legality of such operations.
在准备期间，SQL 查询使用的所有列都被赋予一个在查询中唯一的数字索引。
列编号涉及为查询中的每个基本列和重要的投影分配一个唯一的、特定于查询的索引。
为每列提供唯一索引允许上述表达式节点使用位图 (FastIntSet) 跟踪输入和输出列，
或者在准备和后续阶段期间的任何列集。
位图表示允许快速确定表达式节点之间的兼容性，并被转换用来确定此类操作的合法性。

The Prep phase also computes logical properties, such as the input and output
columns of each (sub-)expression, equivalent columns, not-null columns and
functional dependencies. These properties are computed bottom-up as part of
constructing the expression tree.
Prep 阶段还计算逻辑属性，例如每个（子）表达式的输入和输出列、等效列、非空列和函数依赖关系。
这些属性是自下而上计算的，作为构建表达式树的一部分。

Rewrite

Rewrite is the second phase of query optimization. Placeholder values are
available starting at this phase, so new normalization rules will typically
match once constant values are substituted for placeholders. As mentioned in
the previous section, the expression tree produced by the Prep phase can be
cached and serve as the starting point for the Rewrite phase. In addition, the
Rewrite phase takes a set of physical properties that are required from the
result, such as row ordering and column naming.
重写是查询优化的第二阶段。 占位符值在此阶段开始可用，因此一旦将常量值替换为占位符，新的规范化规则通常会匹配。
如上一节所述，Prep 阶段生成的表达式树可以被缓存并作为 Rewrite 阶段的起点。
此外，重写阶段从结果中获取一组所需的物理属性，例如行排序和列命名。

The Rewrite and Search phases have significant overlap. Both phases perform
transformations on the expression tree. However, Search preserves the matched
expression side-by-side with the new expression, while Rewrite simply discards
the matched expression, since the new expression is assumed to always be
better. In addition, the application of exploration rules may trigger
additional normalization rules, which may in turn trigger additional
exploration rules.
重写和搜索阶段有很大的重叠。 两个阶段都在表达式树上执行转换。
但是，Search 将匹配的表达式与新表达式并排保留，而 Rewrite 只是丢弃匹配的表达式，因为假定新表达式总是更好。
此外，探索规则的应用可能会触发额外的规范化规则，这反过来又会触发额外的探索规则。

Together, the Rewrite and Search phases are responsible for finding the
expression that can provide the required set of physical properties at the
lowest possible execution cost. That mandate is recursively applied; in other
words, each subtree is also optimized with respect to a set of physical
properties required by its parent, and the goal is to find the lowest cost
equivalent expression. An example of an "interior" optimization goal is a merge
join that requires its inner child to return its rows in a specific order. The
same group can be (and sometimes is) optimized multiple times, but with
different required properties each time.
重写和搜索阶段一起负责找到能够以尽可能低的执行成本提供所需物理属性集的表达式。
该任务是递归应用的； 换句话说，每个子树也针对其父级所需的一组物理属性进行了优化，
目标是找到成本最低的等价表达式。
“内部”优化目标的一个示例是合并联接，它要求其内部子级以特定顺序返回其行。
同一组可以（有时是）多次优化，但每次都具有不同的所需属性。

Search

Search is the final phase of optimization. Search begins with a single
normalized tree that was created by the earlier phases. For each group, the
"explorer" component generates alternative expressions that are logically
equivalent to the normalized expression, but which may have very different
execution plans. The "coster" component computes the estimated cost for each
alternate expression. The optimizer remembers the "best expression" for each
group, for each set of physical properties required of that group.
搜索是优化的最后阶段。 搜索从早期阶段创建的单个规范化树开始。
对于每个组，“explorer”组件生成在逻辑上等同于规范化表达式的替代表达式，但可能具有非常不同的执行计划。
“coster”组件计算每个替代表达式的估计成本。 优化器会记住每个组的“最佳表达式”，以及该组所需的每组物理属性。

Optimization of a group proceeds in two phases:
组的优化分两个阶段进行：

1. Compute the cost of any previously generated expressions. That set initially
contains only the group's normalized expression, but exploration may yield
additional expressions. Costing a parent expression requires that the children
first be costed, so costing triggers a recursive traversal of the memo groups.
计算任何先前生成的表达式的成本。 该集合最初仅包含该组的规范化表达式，但探索可能会产生其他表达式。
计算父表达式的成本需要首先计算子表达式的成本，因此成本计算会触发备忘录组的递归遍历。

2. Invoke the explorer to generate new equivalent expressions for the group.
Those new expressions are costed once the optimizer loops back to the first
phase.
调用资源管理器为组生成新的等价表达式。 一旦优化器循环回到第一阶段，这些新表达式就会被计算成本。


In order to avoid a combinatorial explosion in the number of expression trees,
the optimizer utilizes the memo structure. Due to the large number of possible
plans for some queries, the optimizer cannot always explore all of them.
Therefore, it proceeds in multiple iterative "passes", until either it hits
some configured time or resource limit, or until an exhaustive search is
complete. As long as the search is allowed to complete, the best plan will be
found, just as in Volcano and Cascades.
为了避免表达式树数量的组合爆炸，优化器利用了备忘录结构。
由于某些查询有大量可能的计划，优化器不能总是探索所有这些计划。
因此，它会进行多次迭代“通过”，直到达到某个配置的时间或资源限制，或者直到彻底搜索完成。
只要允许搜索完成，就会找到最好的计划，就像在火山和瀑布中一样。

The optimizer uses several techniques to maximize the chance that it finds the
best plan early on:
优化器使用多种技术来最大限度地提高早期找到最佳计划的机会：

- As with Cascades, the search is highly directed, interleaving exploration
with costing in order to prune parts of the tree that cannot yield a better
plan. This contrasts with Volcano, which first generates all possible plans in
one global phase (exploration), and then determines the lowest cost plan in
another global phase (costing).
- 与 Cascades 一样，搜索是高度定向的，将探索与成本计算交织在一起，以便修剪无法产生更好计划的树部分。
这与 Volcano 形成对比，后者首先在一个全局阶段（探索）生成所有可能的计划，
然后在另一个全局阶段（成本核算）确定成本最低的计划。

- The optimizer uses a simple hill climbing heuristic to make greedy progress
towards the best plan. During a given pass, the optimizer visits each group and
performs costing and exploration for that group. As long as doing that yields a
lower cost expression for the group, the optimizer will repeat those steps.
This finds a local maxima for each group during the current pass.
- 优化器使用简单的爬山启发式算法来贪婪地朝着最佳计划前进。
在给定的传递期间，优化器访问每个组并为该组执行成本计算和探索。
只要这样做会为组产生较低成本的表达式，优化器就会重复这些步骤。 这会在当前传递期间为每个组找到一个局部最大值。

In order to avoid costing or exploring parts of the search space that cannot
yield a better plan, the optimizer performs aggressive "branch and bound
pruning". Each group expression is optimized with respect to a "budget"
parameter. As soon as this budget is exceeded, optimization of that expression
terminates. It's not uncommon for large sections of the search space to never
be costed or explored due to this pruning. Example:
为了避免花费或探索无法产生更好计划的搜索空间部分，优化器执行激进的“分支和绑定修剪”。
每个组表达式都针对“预算”参数进行了优化。 一旦超出此预算，该表达式的优化就会终止。
由于这种修剪，搜索空间的大部分从未被计算或探索的情况并不少见。 例子：

	innerJoin
		left:  cost = 50
		right: cost = 75
		on:    cost = 25

If the current best expression for the group has a cost of 100, then the
optimizer does not need to cost or explore the "on" child of the join, and
does not need to cost the join itself. This is because the combined cost of
the left and right children already exceeds 100.
如果组的当前最佳表达式的成本为 100，则优化器不需要计算或探索连接的“on”子节点，也不需要计算连接本身的成本。
这是因为左右孩子的总成本已经超过 100。
*/
package opt
