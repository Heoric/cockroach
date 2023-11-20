// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package props

import (
	"github.com/cockroachdb/cockroach/pkg/sql/opt"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/constraint"
)

// AvailableRuleProps is a bit set that indicates when lazily-populated Rule
// properties are initialized and ready for use.
//AvailableRuleProps 是一个位集，指示延迟填充的规则属性何时初始化并可供使用。
type AvailableRuleProps int8

const (
	// PruneCols is set when the Relational.Rule.PruneCols field is populated.
	// PruneCols 在填充 Relational.Rule.PruneCols 字段时设置。
	PruneCols AvailableRuleProps = 1 << iota

	// RejectNullCols is set when the Relational.Rule.RejectNullCols field is
	// populated.
	RejectNullCols

	// InterestingOrderings is set when the Relational.Rule.InterestingOrderings
	// field is populated.
	InterestingOrderings

	// HasHoistableSubquery is set when the Scalar.Rule.HasHoistableSubquery
	// is populated.
	HasHoistableSubquery

	// UnfilteredCols is set when the Relational.Rule.UnfilteredCols field is
	// populated.
	UnfilteredCols

	// WithUses is set when the Shared.Rule.WithUses field is populated.
	WithUses
)

// Shared are properties that are shared by both relational and scalar
// expressions.
// Shared 是由关系表达式和标量表达式共享的属性。
type Shared struct {
	// Populated is set to true once the properties have been built for the
	// operator.
	// 一旦为 operator 构建了属性，Populated 就会设置为 true。
	Populated bool

	// HasSubquery is true if the subtree rooted at this node contains a subquery.
	// The subquery can be a Subquery, Exists, Any, or ArrayFlatten expression.
	// Subqueries are the only place where a relational node can be nested within a
	// scalar expression.
	// 如果以该节点为根的子树包含子查询，则 HasSubquery 为真。
	// 子查询可以是 Subquery、Exists、Any 或 ArrayFlatten 表达式。
	// 子查询是关系节点可以嵌套在标量表达式中的唯一位置。
	HasSubquery bool

	// HasCorrelatedSubquery is true if the scalar expression tree contains a
	// subquery having one or more outer columns. The subquery can be a Subquery,
	// Exists, or Any operator. These operators usually need to be hoisted out of
	// scalar expression trees and turned into top-level apply joins. This
	// property makes detection fast and easy so that the hoister doesn't waste
	// time searching subtrees that don't contain subqueries.
	// 如果标量表达式树包含具有一个或多个外部列的子查询，则 HasCorrelatedSubquery 为真。
	// 子查询可以是 Subquery、Exists 或 Any 运算符。
	// 这些运算符通常需要从标量表达式树中提升出来并转换为顶级应用连接。
	// 此属性使检测变得快速和容易，因此提升器不会浪费时间搜索不包含子查询的子树。
	HasCorrelatedSubquery bool

	// VolatilitySet contains the set of volatilities contained in the expression.
	// VolatilitySet 包含表达式中包含的波动率集合。
	VolatilitySet VolatilitySet

	// CanMutate is true if the subtree rooted at this expression contains at
	// least one operator that modifies schema (like CreateTable) or writes or
	// deletes rows (like Insert).
	// 如果以该表达式为根的子树包含至少一个修改模式（如 CreateTable）或写入或删除行（如 Insert）的运算符，
	// 则 CanMutate 为真。
	CanMutate bool

	// HasPlaceholder is true if the subtree rooted at this expression contains
	// at least one Placeholder operator.
	HasPlaceholder bool

	// OuterCols is the set of columns that are referenced by variables within
	// this sub-expression, but are not bound within the scope of the expression.
	// OuterCols 是由该子表达式中的变量引用但未绑定在表达式范围内的一组列。 例如：
	// For example:
	//
	//   SELECT *
	//   FROM a
	//   WHERE EXISTS(SELECT * FROM b WHERE b.x = a.x AND b.y = 5)
	//
	// For the EXISTS expression, a.x is an outer column, meaning that it is
	// defined "outside" the EXISTS expression (hence the name "outer"). The
	// SELECT expression binds the b.x and b.y references, so they are not part
	// of the outer column set. The outer SELECT binds the a.x column, and so
	// its outer column set is empty.
	// 对于 EXISTS 表达式，a.x 是一个外部列，这意味着它是在 EXISTS 表达式“外部”定义的（因此名称为“outer”）。
	// SELECT 表达式绑定 b.x 和 b.y 引用，因此它们不是外部列集的一部分。
	// 外部 SELECT 绑定 a.x 列，因此其外部列集为空。
	//
	// Note that what constitutes an "outer column" is dependent on an
	// expression's location in the query. For example, while the b.x and b.y
	// columns are not outer columns on the EXISTS expression, they *are* outer
	// columns on the inner WHERE condition.
	// 请注意，构成“外列”的内容取决于表达式在查询中的位置。
	// 例如，虽然 b.x 和 b.y 列不是 EXISTS 表达式的外部列，但它们*是*内部 WHERE 条件的外部列。
	OuterCols opt.ColSet

	// Rule props are lazily calculated and typically only apply to a single
	// rule. See the comment above Relational.Rule for more details.
	// 规则属性是延迟计算的，通常仅适用于单个规则。 有关更多详细信息，请参阅上面的 Relational.Rule 评论。
	Rule struct {
		// WithUses tracks information about the WithScans inside the given
		// expression which reference WithIDs outside of that expression.
		// WithUses 跟踪有关给定表达式内的 WithScans 的信息，这些信息引用了该表达式之外的 WithID。
		WithUses WithUsesMap
	}
}

// WithUsesMap stores information about each WithScan referencing an outside
// WithID, grouped by each WithID.
type WithUsesMap map[opt.WithID]WithUseInfo

// WithUseInfo contains information about the usage of a specific WithID.
type WithUseInfo struct {
	// Count is the number of WithScan operators which reference this WithID.
	Count int

	// UsedCols is the union of columns used by all WithScan operators which
	// reference this WithID.
	UsedCols opt.ColSet
}

// Relational properties describe the content and characteristics of relational
// data returned by all expression variants within a memo group. While each
// expression in the group may return rows or columns in a different order, or
// compute the result using different algorithms, the same set of data is
// returned and can then be  transformed into whatever layout or presentation
// format that is desired, according to the required physical properties.
// 关系属性描述了一个备忘录组中所有表达式变体返回的关系数据的内容和特征。
// 虽然组中的每个表达式可能以不同的顺序返回行或列，或者使用不同的算法计算结果，
// 但返回相同的数据集，然后可以根据需要将其转换为所需的任何布局或表示格式 物理性质。
type Relational struct {
	Shared

	// OutputCols is the set of columns that can be projected by the expression.
	// Ordering, naming, and duplication of columns is not representable by this
	// property; those are physical properties.
	// OutputCols 是可由表达式投影的列集。 此属性无法表示列的排序、命名和重复； 这些是物理特性。
	OutputCols opt.ColSet

	// NotNullCols is the subset of output columns which cannot be NULL. The
	// nullability of columns flows from the inputs and can also be derived from
	// filters that reject nulls.
	// NotNullCols 是不能为 NULL 的输出列的子集。 列的可空性来自输入，也可以来自拒绝空值的过滤器。
	NotNullCols opt.ColSet

	// Cardinality is the number of rows that can be returned from this relational
	// expression. The number of rows will always be between the inclusive Min and
	// Max bounds. If Max=math.MaxUint32, then there is no limit to the number of
	// rows returned by the expression.
	// 基数是可以从此关系表达式返回的行数。 行数将始终介于最小和最大界限之间。
	// 如果 Max=math.MaxUint32，则表达式返回的行数没有限制。
	Cardinality Cardinality

	// FuncDepSet is a set of functional dependencies (FDs) that encode useful
	// relationships between columns in a base or derived relation. Given two sets
	// of columns A and B, a functional dependency A-->B holds if A uniquely
	// determines B. In other words, if two different rows have equal values for
	// columns in A, then those two rows will also have equal values for columns
	// in B. For example:
	// FuncDepSet 是一组函数依赖项 (FD)，它们对基本或派生关系中的列之间的有用关系进行编码。
	// 给定两组列 A 和 B，如果 A 唯一确定 B，则函数依赖性 A-->B 成立。换句话说，
	// 如果两个不同的行 A 中的列具有相同的值，则这两行也将具有相同的值 B 中的列。例如：
	//
	//   a1 a2 b1
	//   --------
	//   1  2  5
	//   1  2  5
	//
	// FDs assist the optimizer in proving useful properties about query results.
	// This information powers many optimizations, including eliminating
	// unnecessary DISTINCT operators, simplifying ORDER BY columns, removing
	// Max1Row operators, and mapping semi-joins to inner-joins.
	// FD 帮助优化器证明有关查询结果的有用属性。 此信息支持许多优化，包括消除不必要的
	// DISTINCT 运算符、简化 ORDER BY 列、删除 Max1Row 运算符以及将半联接映射到内联接。
	//
	// The methods that are most useful for optimizations are:
	//   Key: extract a candidate key for the relation
	//   ColsAreStrictKey: determine if a set of columns uniquely identify rows
	//   ReduceCols: discard redundant columns to create a candidate key
	// 对于优化最有用的方法是：
	// 	Key：提取关系的候选键
	// 	ColsAreStrictKey：判断一组列是否唯一标识行
	//	ReduceCols：丢弃冗余列以创建候选键
	//
	// For more details, see the header comment for FuncDepSet.
	FuncDeps FuncDepSet

	// Stats is the set of statistics that apply to this relational expression.
	// See statistics.go and memo/statistics_builder.go for more details.
	Stats Statistics

	// Rule encapsulates the set of properties that are maintained to assist
	// with specific sets of transformation rules. They are not intended to be
	// general purpose in nature. Typically, they're used by rules which need to
	// decide whether to push operators down into the tree. These properties
	// "bubble up" information about the subtree which can aid in that decision.
	// 规则封装了一组属性，这些属性被维护以协助特定的转换规则集。 它们本质上不是通用的。
	// 通常，它们被需要决定是否将运算符下推到树中的规则使用。
	// 这些属性“冒泡”有关子树的信息，可以帮助做出该决定。
	//
	// Whereas the other logical relational properties are filled in by the memo
	// package upon creation of a new memo group, the rules properties are filled
	// in by one of the transformation packages, since deriving rule properties
	// is so closely tied with maintenance of the rules that depend upon them.
	// For example, the PruneCols set is connected to the PruneCols normalization
	// rules. The decision about which columns to add to PruneCols depends upon
	// what works best for those rules. Neither the rules nor their properties
	// can be considered in isolation, without considering the other.
	// 其他逻辑关系属性在创建新备忘录组时由备忘录包填充，而规则属性由其中一个转换包填充，
	// 因为派生规则属性与维护依赖于的规则密切相关 他们。
	// 例如，PruneCols 集连接到 PruneCols 规范化规则。
	// 关于将哪些列添加到 PruneCols 的决定取决于最适合这些规则的内容。
	// 无论是规则还是它们的属性都不能孤立地考虑，而不考虑另一个。
	Rule struct {
		// Available contains bits that indicate whether lazily-populated Rule
		// properties have been initialized. For example, if the UnfilteredCols
		// bit is set, then the Rule.UnfilteredCols field has been initialized
		// and is ready for use.
		// 可用包含指示延迟填充的规则属性是否已初始化的位。
		// 例如，如果设置了 UnfilteredCols 位，则 Rule.UnfilteredCols 字段已初始化并可以使用。
		Available AvailableRuleProps

		// PruneCols is the subset of output columns that can potentially be
		// eliminated by one of the PruneCols normalization rules. Those rules
		// operate by pushing a Project operator down the tree that discards
		// unused columns. For example:
		// PruneCols 是输出列的子集，可能会被 PruneCols 规范化规则之一消除。
		// 这些规则通过将 Project 运算符向下推到丢弃未使用列的树中来运行。 例如：
		//
		//   SELECT y FROM xyz WHERE x=1 ORDER BY y LIMIT 1
		//
		// The z column is never referenced, either by the filter or by the
		// limit, and would be part of the PruneCols set for the Limit operator.
		// The final Project operator could then push down a pruning Project
		// operator that eliminated the z column from its subtree.
		// z 列从不被过滤器或限制引用，并且将是为限制运算符设置的 PruneCols 的一部分。
		// 然后，最终的 Project 运算符可以下推修剪 Project 运算符，从其子树中删除 z 列。
		//
		// PruneCols is built bottom-up. It typically starts out containing the
		// complete set of output columns in a leaf expression, but quickly
		// empties out at higher levels of the expression tree as the columns
		// are referenced. Drawing from the example above:
		// PruneCols 是自下而上构建的。 它通常一开始在叶表达式中包含完整的输出列集，
		// 但在引用列时在表达式树的更高级别迅速清空。 从上面的例子中绘制：
		//
		//   Limit PruneCols : [z]
		//   Select PruneCols: [y, z]
		//   Scan PruneCols  : [x, y, z]
		//
		// Only a small number of relational operators are capable of pruning
		// columns (e.g. Scan, Project). A pruning Project operator pushed down
		// the tree must journey downwards until it finds a pruning-capable
		// operator. If a column is part of PruneCols, then it is guaranteed that
		// such an operator exists at the end of the journey. Operators that are
		// not capable of filtering columns (like Explain) will not add any of
		// their columns to this set.
		// 只有少数关系运算符能够修剪列（例如 Scan、Project）。
		// 下推树的修剪项目操作员必须向下移动，直到找到具有修剪能力的操作员。
		// 如果列是 PruneCols 的一部分，则保证在旅程结束时存在这样的运算符。
		// 不能过滤列的操作符（如 Explain）不会将它们的任何列添加到此集合中。
		//
		// PruneCols is lazily populated by rules in prune_cols.opt. It is
		// only valid once the Rule.Available.PruneCols bit has been set.
		PruneCols opt.ColSet

		// RejectNullCols is the subset of nullable output columns that can
		// potentially be made not-null by one of the RejectNull normalization
		// rules. Those rules work in concert with the predicate pushdown rules
		// to synthesize a "col IS NOT NULL" filter and push it down the tree.
		// See the header comments for the reject_nulls.opt file for more
		// information and an example.
		// RejectNullCols 是可为空的输出列的子集，可能通过 RejectNull 规范化规则之一使其不为空。
		// 这些规则与谓词下推规则协同工作以合成“col IS NOT NULL”过滤器并将其下推到树中。
		// 有关更多信息和示例，请参阅reject_nulls.opt 文件的标题注释。
		//
		// RejectNullCols is built bottom-up by rulePropsBuilder, and only contains
		// nullable outer join columns that can be simplified. The columns can be
		// propagated up through multiple operators, giving higher levels of the
		// tree a window into the structure of the tree several layers down. In
		// particular, the null rejection rules use this property to determine when
		// it's advantageous to synthesize a new "IS NOT NULL" filter. Without this
		// information, the rules can clutter the tree with extraneous and
		// marginally useful null filters.
		// RejectNullCols 是由 rulePropsBuilder 自下而上构建的，并且只包含可以简化的可为空的外连接列。
		// 这些列可以通过多个运算符向上传播，从而为树的更高级别提供一个窗口，以了解向下几层的树结构。
		// 特别是，null 拒绝规则使用此属性来确定何时合成新的“IS NOT NULL”过滤器是有利的。
		// 如果没有这些信息，这些规则可能会使树变得杂乱无章，并带有无关紧要的空过滤器。
		//
		// RejectNullCols is lazily populated by rules in reject_nulls.opt. It is
		// only valid once the Rule.Available.RejectNullCols bit has been set.
		RejectNullCols opt.ColSet

		// InterestingOrderings is a list of orderings that potentially could be
		// provided by the operator without sorting. Interesting orderings normally
		// come from scans (index orders) and are bubbled up through some operators.
		// InterestingOrderings 是可能由操作员提供而无需排序的排序列表。
		// 有趣的排序通常来自扫描（索引订单）并通过一些操作员冒泡。
		//
		// Note that all prefixes of an interesting order are "interesting"; the
		// list doesn't need to contain orderings that are prefixes of some other
		// ordering in the list.
		// 请注意，有趣顺序的所有前缀都是“有趣的”； 该列表不需要包含作为列表中某些其他排序前缀的排序。
		//
		// InterestingOrderings is lazily populated by interesting_orderings.go.
		// It is only valid once the Rule.Available.InterestingOrderings bit has
		// been set.
		InterestingOrderings OrderingSet

		// UnfilteredCols is the set of all columns for which rows from their base
		// table are guaranteed not to have been filtered. Rows may be duplicated,
		// but no rows can be missing. Even columns which are not output columns are
		// included as long as table rows are guaranteed not filtered. For example,
		// an unconstrained, unlimited Scan operator can add all columns from its
		// table to this property, but a Select operator cannot add any columns, as
		// it may have filtered rows.
		// UnfilteredCols 是保证基表中的行不会被过滤的所有列的集合。 行可以重复，但不能缺少行。
		// 只要保证不过滤表行，即使不是输出列的列也会包括在内。
		// 例如，不受约束的、无限制的 Scan 运算符可以将其表中的所有列添加到此属性，
		// 但 Select 运算符不能添加任何列，因为它可能已过滤了行。
		//
		// UnfilteredCols is lazily populated by GetJoinMultiplicityFromInputs. It
		// is only valid once the Rule.Available.UnfilteredCols bit has been set.
		// UnfilteredCols 由 GetJoinMultiplicityFromInputs 延迟填充。
		// 仅当设置了 Rule.Available.UnfilteredCols 位后，它才有效。
		UnfilteredCols opt.ColSet
	}
}

// Scalar properties are logical properties that are computed for scalar
// expressions that return primitive-valued types. Scalar properties are
// lazily populated on request.
// 标量属性是为返回原始值类型的标量表达式计算的逻辑属性。 标量属性根据请求延迟填充。
type Scalar struct {
	Shared

	// Constraints is the set of constraints deduced from a boolean expression.
	// For the expression to be true, all constraints in the set must be
	// satisfied. The constraints are not guaranteed to be exactly equivalent to
	// the expression, see TightConstraints.
	// 约束是从布尔表达式推导出的一组约束。 要使表达式为真，必须满足集合中的所有约束。
	// 不保证约束与表达式完全等效，请参阅 TightConstraints。
	Constraints *constraint.Set

	// FuncDeps is a set of functional dependencies (FDs) inferred from a
	// boolean expression. This field is only populated for Filters expressions.
	// FuncDeps 是一组从布尔表达式推断的函数依赖关系 (FD)。 此字段仅为过滤器表达式填充。
	//
	//  - Constant column FDs such as ()-->(1,2) from conjuncts such as
	//    x = 5 AND y = 10.
	//  - Equivalent column FDs such as (1)==(2), (2)==(1) from conjuncts such
	//    as x = y.
	//
	// It is useful to calculate FDs on Filters expressions, because it allows
	// additional filters to be inferred for push-down. For example, consider
	// the query:
	// 计算过滤器表达式上的 FD 很有用，因为它允许推断附加过滤器以进行下推。 例如，考虑以下查询：
	//
	//   SELECT * FROM a, b WHERE a.x = b.x AND a.x > 5;
	//
	// By adding the equivalency FD for a.x = b.x, we can infer an additional
	// filter, b.x > 5. This allows us to rewrite the query as:
	// 通过添加 a.x = b.x 的等价 FD，我们可以推断出一个附加过滤器 b.x > 5。这允许我们将查询重写为：
	//
	//   SELECT * FROM (SELECT * FROM a WHERE a.x > 5) AS a,
	//     (SELECT * FROM b WHERE b.x > 5) AS b WHERE a.x = b.x;
	//
	// For more details, see the header comment for FuncDepSet.
	FuncDeps FuncDepSet

	// TightConstraints is true if the expression is exactly equivalent to the
	// constraints. If it is false, the constraints are weaker than the
	// expression.
	// 如果表达式完全等于约束，则 TightConstraints 为 true。 如果为 false，则约束弱于表达式。
	TightConstraints bool

	// Rule encapsulates the set of properties that are maintained to assist
	// with specific sets of transformation rules. See the Relational.Rule
	// comment for more details.
	//Rule封装了一组属性，这些属性被维护以协助特定的转换规则集。 有关更多详细信息，请参阅 Relational.Rule 注释。
	Rule struct {
		// Available contains bits that indicate whether lazily-populated Rule
		// properties have been initialized. For example, if the
		// HasHoistableSubquery bit is set, then the Rule.HasHoistableSubquery
		// field has been initialized and is ready for use.
		//Available 包含指示延迟填充的规则属性是否已初始化的位。
		//例如，如果设置了 HasHoistableSubquery 位，则 Rule.HasHoistableSubquery 字段已初始化并可以使用。
		Available AvailableRuleProps

		// HasHoistableSubquery is true if the scalar expression tree contains a
		// subquery having one or more outer columns, and if the subquery needs
		// to be hoisted up into its parent query as part of query decorrelation.
		// The subquery can be a Subquery, Exists, or Any operator. These operators
		// need to be hoisted out of scalar expression trees and turned into top-
		// level apply joins. This property makes detection fast and easy so that
		// the hoister doesn't waste time searching subtrees that don't contain
		// subqueries.
		// 如果标量表达式树包含具有一个或多个外部列的子查询，
		// 并且如果需要将子查询提升到其父查询中作为查询去相关的一部分，则 HasHoistableSubquery 为真。
		// 子查询可以是 Subquery、Exists 或 Any 运算符。
		// 这些运算符需要从标量表达式树中提升出来并转换为顶级应用连接。
		// 此属性使检测变得快速和容易，因此提升器不会浪费时间搜索不包含子查询的子树。
		//
		// HasHoistableSubquery is lazily populated by rules in decorrelate.opt.
		// It is only valid once the Rule.Available.HasHoistableSubquery bit has
		// been set.
		// HasHoistableSubquery 由 dererelate.opt 中的规则延迟填充。
		// 仅在设置 Rule.Available.HasHoistableSubquery 位后才有效。
		HasHoistableSubquery bool
	}
}

// IsAvailable returns true if the specified rule property has been populated
// on this relational properties instance.
func (r *Relational) IsAvailable(p AvailableRuleProps) bool {
	return (r.Rule.Available & p) != 0
}

// SetAvailable sets the available bits for the given properties, in order to
// mark them as populated on this relational properties instance.
func (r *Relational) SetAvailable(p AvailableRuleProps) {
	r.Rule.Available |= p
}

// IsAvailable returns true if the specified rule property has been populated
// on this scalar properties instance.
func (s *Scalar) IsAvailable(p AvailableRuleProps) bool {
	return (s.Rule.Available & p) != 0
}

// SetAvailable sets the available bits for the given properties, in order to
// mark them as populated on this scalar properties instance.
func (s *Scalar) SetAvailable(p AvailableRuleProps) {
	s.Rule.Available |= p
}
