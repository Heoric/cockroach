// Copyright 2022 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package sql

import (
	"context"
	"github.com/cockroachdb/cockroach/pkg/sql/execinfrapb"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
)

type HierarchicalNode struct {
	// The source node.
	initial planNode
	// columns is the set of result columns.

	workingRows rowContainerHelper
	resultsRows rowContainerHelper

	iterator   *rowContainerIterator
	currentRow tree.Datums

	initialDone bool
	done        bool

	connectby tree.TypedExpr
	startwith tree.TypedExpr
	evalCtx   *tree.EvalContext

	typs []*types.T
}

func (n *HierarchicalNode) startExec(params runParams) error {
	n.typs = planTypes(n.initial)
	n.workingRows.Init(n.typs, params.extendedEvalCtx, "Hierarchical" /* opName */)
	n.resultsRows.Init(n.typs, params.extendedEvalCtx, "Hierarchical" /* opName */)
	return nil
}

func (n *HierarchicalNode) Next(params runParams) (bool, error) {

	if !n.initialDone {
		// 收集所有对数据。
		err := n.collectAllRows(params)
		if err != nil {
			return false, err
		}

		// 计算需要对数据。 需要 connect by 和 start with 的信息。
		n.resultsRows = n.workingRows

		n.initialDone = true
	}

	if n.done {
		return false, nil
	}

	if n.workingRows.Len() == 0 {
		// Last iteration returned no rows.
		n.done = true
		return false, nil
	}

	// 定位到要返回对行上。
	var err error
	n.currentRow, err = n.iterator.Next()

	if err != nil {
		return false, err
	}
	if n.currentRow != nil {
		// There are more rows to return from the last iteration.
		return true, nil
	}
	return n.currentRow != nil, nil
}

func (n *HierarchicalNode) Values() tree.Datums {
	return n.currentRow
}

func (n *HierarchicalNode) Close(ctx context.Context) {
	n.initial.Close(ctx)

	n.workingRows.Close(ctx)
	n.resultsRows.Close(ctx)
	if n.iterator != nil {
		n.iterator.Close()
		n.iterator = nil
	}
}

func (n *HierarchicalNode) bfs(params runParams) {
	n.iterator = newRowContainerIterator(params.ctx, n.workingRows, n.typs)

	//n.startwith.Eval()
	//n.startwith.

}

func (n *HierarchicalNode) collectAllRows(params runParams) error {
	for {
		ok, err := n.initial.Next(params)
		if err != nil {
			return err
		}
		if !ok {
			break
		}

		pass, err := execinfrapb.RunFilter(n.startwith, n.evalCtx)
		if err != nil {
			return err
		}

		if pass {
			if err := n.AddRow(params.ctx, n.initial.Values()); err != nil {
				return err
			}
		}

	}
	return nil
}

func (n *HierarchicalNode) AddRow(ctx context.Context, row tree.Datums) error {
	return n.workingRows.AddRow(ctx, row)
}
