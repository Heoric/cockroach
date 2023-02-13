// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package physical

import (
	"bytes"
	"fmt"
	"math"

	"github.com/cockroachdb/cockroach/pkg/sql/opt"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/props"
)

// Required properties are interesting characteristics of an expression that
// impact its layout, presentation, or location, but not its logical content.
// Examples include row order, column naming, and data distribution (physical
// location of data ranges). Physical properties exist outside of the relational
// algebra, and arise from both the SQL query itself (e.g. the non-relational
// ORDER BY operator) and by the selection of specific implementations during
// optimization (e.g. a merge join requires the inputs to be sorted in a
// particular order).
// 必需属性是表达式的有趣特征，会影响其布局、表示或位置，但不会影响其逻辑内容。
// 示例包括行顺序、列命名和数据分布（数据范围的物理位置）。
// 物理属性存在于关系代数之外，来自 SQL 查询本身（例如非关系 ORDER BY 运算符）
// 和优化期间特定实现的选择（例如合并连接要求输入按特定顺序排序） 命令）。
//
// Required properties are derived top-to-bottom - there is a required physical
// property on the root, and each expression can require physical properties on
// one or more of its operands. When an expression is optimized, it is always
// with respect to a particular set of required physical properties. The goal
// is to find the lowest cost expression that provides those properties while
// still remaining logically equivalent.
// 必需的属性是从上到下派生的——根上有一个必需的物理属性，每个表达式都需要一个或多个操作数的物理属性。
// 当一个表达式被优化时，它总是与一组特定的所需物理属性有关。
// 目标是找到提供这些属性的最低成本表达式，同时仍保持逻辑等效。
type Required struct {
	// Presentation specifies the naming, membership (including duplicates),
	// and order of result columns. If Presentation is not defined, then no
	// particular column presentation is required or provided.
	// Presentation 指定结果列的命名、成员资格（包括重复项）和顺序。
	// 如果未定义 Presentation，则不需要或提供特定的列展示。
	Presentation Presentation

	// Ordering specifies the sort order of result rows. Rows can be sorted by
	// one or more columns, each of which can be sorted in either ascending or
	// descending order. If Ordering is not defined, then no particular ordering
	// is required or provided.
	// Ordering 指定结果行的排序顺序。 行可以按一列或多列排序，每一列都可以按升序或降序排序。
	// 如果未定义排序，则不需要或提供特定的排序。
	Ordering props.OrderingChoice

	// LimitHint specifies a "soft limit" to the number of result rows that may
	// be required of the expression. If requested, an expression will still need
	// to return all result rows, but it can be optimized based on the assumption
	// that only the hinted number of rows will be needed.
	// A LimitHint of 0 indicates "no limit". The LimitHint is an intermediate
	// float64 representation, and can be converted to an integer number of rows
	// using LimitHintInt64.
	// LimitHint 指定表达式可能需要的结果行数的“软限制”。
	// 如果需要，表达式仍需要返回所有结果行，但可以基于仅需要提示的行数的假设对其进行优化。
	// LimitHint 为 0 表示“无限制”。
	// LimitHint 是一种中间 float64 表示形式，可以使用 LimitHintInt64 将其转换为整数行。
	LimitHint float64

	// Distribution specifies the physical distribution of result rows. This is
	// defined as the set of regions that may contain result rows. If
	// Distribution is not defined, then no particular distribution is required.
	// Currently, the only operator in a plan tree that has a required
	// distribution is the root, since data must always be returned to the gateway
	// region.
	// Distribution 指定结果行的物理分布。 这被定义为可能包含结果行的区域集。
	// 如果未定义 Distribution，则不需要特定的分布。
	// 目前，计划树中唯一具有所需分布的运算符是根，因为数据必须始终返回到网关区域。
	Distribution Distribution
}

// MinRequired are the default physical properties that require nothing and
// provide nothing.
var MinRequired = &Required{}

// Defined is true if any physical property is defined. If none is defined, then
// this is an instance of MinRequired.
func (p *Required) Defined() bool {
	return !p.Presentation.Any() || !p.Ordering.Any() || p.LimitHint != 0 || !p.Distribution.Any()
}

// ColSet returns the set of columns used by any of the physical properties.
func (p *Required) ColSet() opt.ColSet {
	colSet := p.Ordering.ColSet()
	for _, col := range p.Presentation {
		colSet.Add(col.ID)
	}
	return colSet
}

func (p *Required) String() string {
	var buf bytes.Buffer
	output := func(name string, fn func(*bytes.Buffer)) {
		if buf.Len() != 0 {
			buf.WriteByte(' ')
		}
		buf.WriteByte('[')
		buf.WriteString(name)
		buf.WriteString(": ")
		fn(&buf)
		buf.WriteByte(']')
	}

	if !p.Presentation.Any() {
		output("presentation", p.Presentation.format)
	}
	if !p.Ordering.Any() {
		output("ordering", p.Ordering.Format)
	}
	if p.LimitHint != 0 {
		output("limit hint", func(buf *bytes.Buffer) { fmt.Fprintf(buf, "%.2f", p.LimitHint) })
	}
	if !p.Distribution.Any() {
		output("distribution", p.Distribution.format)
	}

	// Handle empty properties case.
	if buf.Len() == 0 {
		return "[]"
	}
	return buf.String()
}

// Equals returns true if the two physical properties are identical.
func (p *Required) Equals(rhs *Required) bool {
	return p.Presentation.Equals(rhs.Presentation) && p.Ordering.Equals(&rhs.Ordering) &&
		p.LimitHint == rhs.LimitHint && p.Distribution.Equals(rhs.Distribution)
}

// LimitHintInt64 returns the limit hint converted to an int64.
func (p *Required) LimitHintInt64() int64 {
	h := int64(math.Ceil(p.LimitHint))
	if h < 0 {
		// If we have an overflow, then disable the limit hint.
		h = 0
	}
	return h
}

// Presentation specifies the naming, membership (including duplicates), and
// order of result columns that are required of or provided by an operator.
// While it cannot add unique columns, Presentation can rename, reorder,
// duplicate and discard columns. If Presentation is not defined, then no
// particular column presentation is required or provided. For example:
//   a.y:2 a.x:1 a.y:2 column1:3
// Presentation 指定操作员需要或提供的结果列的命名、成员资格（包括重复项）和顺序。
// 虽然它不能添加唯一列，但 Presentation 可以重命名、重新排序、复制和丢弃列。
// 如果未定义 Presentation，则不需要或提供特定的列展示。 例如：
//   a.y:2 a.x:1 a.y:2 column1:3
type Presentation []opt.AliasedColumn

// Any is true if any column presentation is allowed or can be provided.
func (p Presentation) Any() bool {
	return p == nil
}

// Equals returns true iff this presentation exactly matches the given
// presentation.
func (p Presentation) Equals(rhs Presentation) bool {
	// The 0 column presentation is not the same as the nil presentation.
	if p.Any() != rhs.Any() {
		return false
	}
	if len(p) != len(rhs) {
		return false
	}

	for i := 0; i < len(p); i++ {
		if p[i] != rhs[i] {
			return false
		}
	}
	return true
}

func (p Presentation) String() string {
	var buf bytes.Buffer
	p.format(&buf)
	return buf.String()
}

func (p Presentation) format(buf *bytes.Buffer) {
	for i, col := range p {
		if i > 0 {
			buf.WriteString(",")
		}
		fmt.Fprintf(buf, "%s:%d", col.Alias, col.ID)
	}
}
