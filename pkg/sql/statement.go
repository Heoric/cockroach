// Copyright 2017 The Cockroach Authors.
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
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/colinfo"
	"github.com/cockroachdb/cockroach/pkg/sql/parser"
)

// Statement contains a statement with optional expected result columns and metadata.
type Statement struct {
	parser.Statement

	StmtNoConstants string
	StmtSummary     string
	QueryID         ClusterWideID

	ExpectedTypes colinfo.ResultColumns

	// Prepared is non-nil during the PREPARE phase, as well as during EXECUTE of
	// a previously prepared statement. The Prepared statement can be modified
	// during either phase; the PREPARE phase sets its initial state, and the
	// EXECUTE phase can re-prepare it. This happens when the original plan has
	// been invalidated by schema changes, session data changes, permission
	// changes, or other changes to the context in which the original plan was
	// prepared.
	// Prepared 在 PREPARE 阶段以及在先前准备好的语句的 EXECUTE 期间是非零的。
	// Prepared 语句可以在任一阶段进行修改； PREPARE 阶段设置其初始状态，EXECUTE 阶段可以重新准备它。
	// 当原始计划因模式更改、会话数据更改、权限更改或准备原始计划的上下文的其他更改而失效时，就会发生这种情况。
	//
	// Given that the PreparedStatement can be modified during planning, it is
	// not safe for use on multiple threads.
	// 由于PreparedStatement在规划时可以修改，所以在多线程上使用是不安全的。
	Prepared *PreparedStatement
}

func makeStatement(parserStmt parser.Statement, queryID ClusterWideID) Statement {
	return Statement{
		Statement:       parserStmt,
		StmtNoConstants: formatStatementHideConstants(parserStmt.AST),
		StmtSummary:     formatStatementSummary(parserStmt.AST),
		QueryID:         queryID,
	}
}

func makeStatementFromPrepared(prepared *PreparedStatement, queryID ClusterWideID) Statement {
	return Statement{
		Statement:       prepared.Statement,
		Prepared:        prepared,
		ExpectedTypes:   prepared.Columns,
		StmtNoConstants: prepared.StatementNoConstants,
		StmtSummary:     prepared.StatementSummary,
		QueryID:         queryID,
	}
}

func (s Statement) String() string {
	// We have the original SQL, but we still use String() because it obfuscates
	// passwords.
	return s.AST.String()
}
