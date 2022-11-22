// Copyright 2019 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package optbuilder

import (
	"github.com/cockroachdb/cockroach/pkg/sql/opt"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/memo"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
)

func (b *Builder) buildHierarchical(hierarchical *tree.Hierarchical, inScope *scope) {
	if hierarchical == nil {
		return
	}

	condition := b.resolveAndBuildScalar(
		hierarchical.ConnectBy.Condition,
		types.Bool,
		exprKindHierachical,
		tree.RejectGenerators,
		inScope,
	)

	var startwith opt.ScalarExpr

	if hierarchical.ConnectBy.StartWith != nil {
		startwith = b.resolveAndBuildScalar(
			hierarchical.StartWith.Condition,
			types.Bool,
			exprKindHierachical,
			tree.RejectGenerators,
			inScope,
		)
	}

	if hierarchical.StartWith != nil {
		startwith = b.resolveAndBuildScalar(
			hierarchical.StartWith.Condition,
			types.Bool,
			exprKindHierachical,
			tree.RejectGenerators,
			inScope,
		)
	}

	inScope.expr = b.factory.ConstructHierarchical(inScope.expr,
		memo.FiltersExpr{b.factory.ConstructFiltersItem(condition)},
		memo.FiltersExpr{b.factory.ConstructFiltersItem(startwith)})

	// inScope.addColumn()

}
