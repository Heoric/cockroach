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

	"github.com/cockroachdb/cockroach/pkg/sql/opt"
)

// Provided physical properties of an operator. An operator might be able to
// satisfy a required property in multiple ways, and additional information is
// necessary for execution. For example, the required properties may allow
// multiple ordering choices; the provided properties would describe the
// specific ordering that has to be respected during execution.
// 提供 operator 的物理属性。 operator 可能能够以多种方式满足所需的属性，并且执行需要附加信息。
// 例如，required 属性可能允许多种排序选择； provided 属性将描述执行期间必须遵守的特定顺序。
//
// Provided properties are derived bottom-up (on the lowest cost tree).
// Provided 属性是自下而上导出的（在最低成本树上）。
type Provided struct {
	// Ordering is an ordering that needs to be maintained on the rows produced by
	// this operator in order to satisfy its required ordering. This is useful for
	// configuring execution in a distributed setting, where results from multiple
	// nodes may need to be merged. A best-effort attempt is made to have as few
	// columns as possible.
	// 排序是需要在此运算符生成的行上维护的排序，以满足其所需的排序。
	// 这对于在分布式设置中配置执行非常有用，其中可能需要合并来自多个节点的结果。
	// 已尽最大努力尝试使用尽可能少的列。
	//
	// The ordering, in conjunction with the functional dependencies (in the
	// logical properties), must intersect the required ordering.
	// 顺序与功能依赖项（在逻辑属性中）相结合，必须与所需的顺序相交。
	//
	// See the documentation for the opt/ordering package for some examples.
	Ordering opt.Ordering

	// Distribution is a distribution that needs to be maintained on the rows
	// produced by this operator in order to satisfy its required distribution. If
	// there is a required distribution, the provided distribution must match it
	// exactly.
	// Distribution 是需要在此运算符生成的行上维护的分布，以满足其所需的分布。
	// 如果有 required 的分布，则 provided 的分布必须与其完全匹配。
	//
	// The provided distribution is not yet used when building the DistSQL plan,
	// but eventually it should inform the decision about whether to plan
	// processors locally or remotely. Currently, it is used to determine whether
	// a Distribute operator is needed between this operator and its parent, which
	// can affect the cost of a plan.
	// 在构建 DistSQL 计划时尚未使用provided的分布，但最终它应该通知有关是否在本地或远程规划处理器的决定。
	// 目前，它用于确定该运算符与其父级之间是否需要 Distribute 运算符，这可能会影响计划的成本。
	Distribution Distribution
}

// Equals returns true if the two sets of provided properties are identical.
func (p *Provided) Equals(other *Provided) bool {
	return p.Ordering.Equals(other.Ordering) && p.Distribution.Equals(other.Distribution)
}

func (p *Provided) String() string {
	var buf bytes.Buffer

	if len(p.Ordering) > 0 {
		buf.WriteString("[ordering: ")
		p.Ordering.Format(&buf)
		if p.Distribution.Any() {
			buf.WriteByte(']')
		} else {
			buf.WriteString(", ")
		}
	}

	if !p.Distribution.Any() {
		if len(p.Ordering) == 0 {
			buf.WriteByte('[')
		}
		buf.WriteString("distribution: ")
		p.Distribution.format(&buf)
		buf.WriteByte(']')
	}

	return buf.String()
}
