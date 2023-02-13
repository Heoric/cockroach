// Copyright 2018 The Cockroach Authors.
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
	"context"
	"strconv"

	"github.com/cockroachdb/cockroach/pkg/sql/catalog/typedesc"
	"github.com/cockroachdb/cockroach/pkg/sql/delegate"
	"github.com/cockroachdb/cockroach/pkg/sql/opt"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/cat"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/norm"
	"github.com/cockroachdb/cockroach/pkg/sql/opt/optgen/exprgen"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgcode"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
	"github.com/cockroachdb/cockroach/pkg/sql/privilege"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/errorutil"
	"github.com/cockroachdb/cockroach/pkg/util/errorutil/unimplemented"
	"github.com/cockroachdb/errors"
	"github.com/lib/pq/oid"
)

// Builder holds the context needed for building a memo structure from a SQL
// statement. Builder.Build() is the top-level function to perform this build
// process. As part of the build process, it performs name resolution and
// type checking on the expressions within Builder.stmt.
// Builder 保存从 SQL 语句构建备忘录结构所需的上下文。
// Builder.Build() 是执行此构建过程的顶级函数。
// 作为构建过程的一部分，它对 Builder.stmt 中的表达式执行名称解析和类型检查。
//
// The memo structure is the primary data structure used for query optimization,
// so building the memo is the first step required to optimize a query. The memo
// is maintained inside Builder.factory, which exposes methods to construct
// expression groups inside the memo. Once the expression tree has been built,
// the builder calls SetRoot on the memo to indicate the root memo group, as
// well as the set of physical properties (e.g., row and column ordering) that
// at least one expression in the root group must satisfy.
// 备忘录结构是用于查询优化的主要数据结构，因此构建备忘录是优化查询所需的第一步。
// 备忘录在 Builder.factory 中维护，它公开了在备忘录中构造表达式组的方法。
// 构建表达式树后，构建器会在备忘录上调用 SetRoot 以指示根备忘录组，
// 以及根组中至少一个表达式必须满足的一组物理属性（例如，行和列排序）。
//
// A memo is essentially a compact representation of a forest of logically-
// equivalent query trees. Each tree is either a logical or a physical plan
// for executing the SQL query. After the build process is complete, the memo
// forest will contain exactly one tree: the logical query plan corresponding
// to the AST of the original SQL statement with some number of "normalization"
// transformations applied. Normalization transformations include heuristics
// such as predicate push-down that should always be applied. They do not
// include "exploration" transformations whose benefit must be evaluated with
// the optimizer's cost model (e.g., join reordering).
// 备忘录本质上是逻辑上等效的查询树森林的紧凑表示。 每棵树都是执行 SQL 查询的逻辑或物理计划。
// 构建过程完成后，备忘录森林将只包含一棵树：
// 与应用了一定数量的“规范化”转换的原始 SQL 语句的 AST 对应的逻辑查询计划。
// 规范化转换包括应始终应用的启发式方法，例如谓词下推。
// 它们不包括必须使用优化器的成本模型（例如，连接重新排序）评估其收益的“探索”转换。
//
// See factory.go and memo.go inside the opt/xform package for more details
// about the memo structure.
type Builder struct {

	// -- Control knobs -- 控制旋钮
	//
	// These fields can be set before calling Build to control various aspects of
	// the building process.
	// 这些字段可以在调用 Build 之前设置，以控制构建过程的各个方面。

	// KeepPlaceholders is a control knob: if set, optbuilder will never replace
	// a placeholder operator with its assigned value, even when it is available.
	// This is used when re-preparing invalidated queries.
	// KeepPlaceholders 是一个控制旋钮：如果设置，optbuilder
	// 将永远不会用其分配的值替换占位符运算符，即使它可用。 这在重新准备无效查询时使用。
	KeepPlaceholders bool

	// -- Results --
	//
	// These fields are set during the building process and can be used after
	// Build is called.
	// 这些字段是在构建过程中设置的，可以在调用 Build 后使用。

	// HadPlaceholders is set to true if we replaced any placeholders with their
	// values.
	// 如果我们用它们的值替换任何占位符，则 HadPlaceholders 设置为 true。
	HadPlaceholders bool

	// DisableMemoReuse is set to true if we encountered a statement that is not
	// safe to cache the memo for. This is the case for various DDL and SHOW
	// statements.
	// 如果我们遇到无法安全缓存备忘录的语句，则 DisableMemoReuse 设置为 true。
	// 各种 DDL 和 SHOW 语句就是这种情况。
	DisableMemoReuse bool

	factory *norm.Factory
	stmt    tree.Statement

	ctx        context.Context
	semaCtx    *tree.SemaContext
	evalCtx    *tree.EvalContext
	catalog    cat.Catalog
	scopeAlloc []scope

	// ctes stores CTEs which may need to be built at the top-level.
	ctes cteSources

	// cteRefMap stores information about CTE-to-CTE references.
	//
	// For each WithID, the map stores a list of CTEs that refer to that WithID.
	// Together, they form a directed acyclic graph.
	cteRefMap map[opt.WithID]cteSources

	// If set, the planner will skip checking for the SELECT privilege when
	// resolving data sources (tables, views, etc). This is used when compiling
	// views and the view SELECT privilege has already been checked. This should
	// be used with care.
	// 如果设置，规划器将在解析数据源（表、视图等）时跳过对 SELECT 权限的检查。
	// 这在编译视图时使用，并且已经检查了视图 SELECT 权限。 这应该小心使用。
	skipSelectPrivilegeChecks bool

	// views contains a cache of views that have already been parsed, in case they
	// are referenced multiple times in the same query.
	// views 包含已解析视图的缓存，以防它们在同一查询中被多次引用。
	views map[cat.View]*tree.Select

	// subquery contains a pointer to the subquery which is currently being built
	// (if any).
	subquery *subquery

	// If set, we are processing a view definition; in this case, catalog caches
	// are disabled and certain statements (like mutations) are disallowed.
	// 如果设置，我们正在处理视图定义； 在这种情况下，目录缓存被禁用并且某些语句（如突变）被禁止。
	insideViewDef bool

	// If set, we are collecting view dependencies in viewDeps. This can only
	// happen inside view definitions.
	// 如果设置，我们将在 viewDeps 中收集视图依赖项。 这只能发生在视图定义内部。
	//
	// When a view depends on another view, we only want to track the dependency
	// on the inner view itself, and not the transitive dependencies (so
	// trackViewDeps would be false inside that inner view).
	// 当一个视图依赖于另一个视图时，我们只想跟踪对内部视图本身的依赖，
	// 而不是传递依赖（因此在该内部视图中 trackViewDeps 将为 false）。
	trackViewDeps bool
	viewDeps      opt.ViewDeps
	viewTypeDeps  opt.ViewTypeDeps

	// If set, the data source names in the AST are rewritten to the fully
	// qualified version (after resolution). Used to construct the strings for
	// CREATE VIEW and CREATE TABLE AS queries.
	// 如果设置，AST 中的数据源名称将被重写为完全限定版本（解析后）。
	// 用于为 CREATE VIEW 和 CREATE TABLE AS 查询构造字符串。
	// TODO(radu): modifying the AST in-place is hacky; we will need to switch to
	// using AST annotations.
	qualifyDataSourceNamesInAST bool

	// isCorrelated is set to true if we already reported to telemetry that the
	// query contains a correlated subquery.
	// isCorrelated 如果我们已经向遥测报告查询包含相关子查询，则设置为 true。
	isCorrelated bool

	// areAllTableMutationsSimpleInserts maps from each table mutated by the
	// statement to true if all mutations of that table are simple inserts
	// (without ON CONFLICT) or false otherwise. All mutated tables will have an
	// entry in the map.
	// areAllTableMutationsSimpleInserts 从每个由语句突变的表映射到 true，
	// 如果该表的所有突变都是简单插入（没有 ON CONFLICT），否则为 false。
	// 所有变异的表都会在映射中有一个条目。
	areAllTableMutationsSimpleInserts map[cat.StableID]bool
}

// New creates a new Builder structure initialized with the given
// parsed SQL statement.
func New(
	ctx context.Context,
	semaCtx *tree.SemaContext,
	evalCtx *tree.EvalContext,
	catalog cat.Catalog,
	factory *norm.Factory,
	stmt tree.Statement,
) *Builder {
	return &Builder{
		factory: factory,
		stmt:    stmt,
		ctx:     ctx,
		semaCtx: semaCtx,
		evalCtx: evalCtx,
		catalog: catalog,
	}
}

// Build is the top-level function to build the memo structure inside
// Builder.factory from the parsed SQL statement in Builder.stmt. See the
// comment above the Builder type declaration for details.
// Build 是顶级函数，用于从 Builder.stmt 中解析的 SQL 语句在 Builder.factory 内部构建备忘录结构。
// 有关详细信息，请参阅 Builder 类型声明上方的注释。
//
// If any subroutines panic with a non-runtime error as part of the build
// process, the panic is caught here and returned as an error.
// 如果任何子例程在构建过程中因非运行时错误而恐慌，那么恐慌会在此处被捕获并作为错误返回。
func (b *Builder) Build() (err error) {
	defer func() {
		if r := recover(); r != nil {
			// This code allows us to propagate errors without adding lots of checks
			// for `if err != nil` throughout the construction code. This is only
			// possible because the code does not update shared state and does not
			// manipulate locks.
			// 这段代码允许我们传播错误，而无需在整个构造代码中为 `if err != nil` 添加大量检查。
			// 这是唯一可能的，因为代码不更新共享状态并且不操作锁。
			if ok, e := errorutil.ShouldCatch(r); ok {
				err = e
			} else {
				panic(r)
			}
		}
	}()

	// TODO (rohany): We shouldn't be modifying the semaCtx passed to the builder
	//  but we unfortunately rely on mutation to the semaCtx. We modify the input
	//  semaCtx during building of opaque statements, and then expect that those
	//  mutations are visible on the planner's semaCtx.
	// 我们不应该修改传递给构建器的 semaCtx，但不幸的是，我们依赖于对 semaCtx 的突变。
	// 我们在构建不透明语句期间修改输入 semaCtx，然后期望这些突变在规划器的 semaCtx 上可见。

	// Hijack the input TypeResolver in the semaCtx to record all of the user
	// defined types that we resolve while building this query.
	// 劫持 semaCtx 中的输入 TypeResolver 以记录我们在构建此查询时解析的所有用户定义类型。
	existingResolver := b.semaCtx.TypeResolver
	// Ensure that the original TypeResolver is reset after.
	defer func() { b.semaCtx.TypeResolver = existingResolver }()
	typeTracker := &optTrackingTypeResolver{
		res:      b.semaCtx.TypeResolver,
		metadata: b.factory.Metadata(),
	}
	b.semaCtx.TypeResolver = typeTracker

	// Special case for CannedOptPlan.
	if canned, ok := b.stmt.(*tree.CannedOptPlan); ok {
		b.factory.DisableOptimizations()
		_, err := exprgen.Build(b.catalog, b.factory, canned.Plan)
		return err
	}

	// Build the memo, and call SetRoot on the memo to indicate the root group
	// and physical properties.
	outScope := b.buildStmtAtRoot(b.stmt, nil /* desiredTypes */)

	physical := outScope.makePhysicalProps()
	b.factory.Memo().SetRoot(outScope.expr, physical)
	return nil
}

// unimplementedWithIssueDetailf formats according to a format
// specifier and returns a Postgres error with the
// pg code FeatureNotSupported.
func unimplementedWithIssueDetailf(issue int, detail, format string, args ...interface{}) error {
	return unimplemented.NewWithIssueDetailf(issue, detail, format, args...)
}

// buildStmtAtRoot builds a statement, beginning a new conceptual query
// "context". This is used at the top-level of every statement, and inside
// EXPLAIN, CREATE VIEW, CREATE TABLE AS.
// buildStmtAtRoot 构建一个语句，开始一个新的概念查询“context”。
// 这用于每个语句的顶层，以及 EXPLAIN、CREATE VIEW、CREATE TABLE AS 内部。
func (b *Builder) buildStmtAtRoot(stmt tree.Statement, desiredTypes []*types.T) (outScope *scope) {
	// A "root" statement cannot refer to anything from an enclosing query, so we
	// always start with an empty scope.
	// “根”语句不能引用封闭查询中的任何内容，因此我们总是从一个空范围开始。
	inScope := b.allocScope()
	inScope.atRoot = true

	// Save any CTEs above the boundary.
	prevCTEs := b.ctes
	b.ctes = nil
	outScope = b.buildStmt(stmt, desiredTypes, inScope)
	// Build With operators for any CTEs hoisted to the top level.
	outScope.expr = b.buildWiths(outScope.expr, b.ctes)
	b.ctes = prevCTEs
	return outScope
}

// buildStmt builds a set of memo groups that represent the given SQL
// statement.
// buildStmt 构建一组表示给定 SQL 语句的备忘录组。
//
// NOTE: The following descriptions of the inScope parameter and outScope
//       return value apply for all buildXXX() functions in this directory.
//       Note that some buildXXX() functions pass outScope as a parameter
//       rather than a return value so its scopeColumns can be built up
//       incrementally across several function calls.
// 注意：以下对 inScope 参数和 outScope 返回值的描述适用于该目录下的所有 buildXXX() 函数。
// 请注意，一些 buildXXX() 函数将 outScope 作为参数而不是返回值传递，
// 因此它的 scopeColumns 可以在多个函数调用中逐步构建。
//
// inScope   This parameter contains the name bindings that are visible for this
//           statement/expression (e.g., passed in from an enclosing statement).
//           此参数包含对该语句/表达式可见的名称绑定（例如，从封闭语句传入）。
//
// outScope  This return value contains the newly bound variables that will be
//           visible to enclosing statements, as well as a pointer to any
//           "parent" scope that is still visible. The top-level memo expression
//           for the built statement/expression is returned in outScope.expr.
// 					此返回值包含对封闭语句可见的新绑定变量，以及指向任何仍然可见的“父”作用域的指针。
// 					构建语句/表达式的顶级备忘录表达式在 outScope.expr 中返回。
func (b *Builder) buildStmt(
	stmt tree.Statement, desiredTypes []*types.T, inScope *scope,
) (outScope *scope) {
	if b.insideViewDef {
		// A blocklist of statements that can't be used from inside a view.
		switch stmt := stmt.(type) {
		case *tree.Delete, *tree.Insert, *tree.Update, *tree.CreateTable, *tree.CreateView,
			*tree.Split, *tree.Unsplit, *tree.Relocate, *tree.RelocateRange,
			*tree.ControlJobs, *tree.ControlSchedules, *tree.CancelQueries, *tree.CancelSessions:
			panic(pgerror.Newf(
				pgcode.Syntax, "%s cannot be used inside a view definition", stmt.StatementTag(),
			))
		}
	}

	switch stmt := stmt.(type) {
	case *tree.Select:
		return b.buildSelect(stmt, noRowLocking, desiredTypes, inScope)

	case *tree.ParenSelect:
		return b.buildSelect(stmt.Select, noRowLocking, desiredTypes, inScope)

	case *tree.Delete:
		return b.processWiths(stmt.With, inScope, func(inScope *scope) *scope {
			return b.buildDelete(stmt, inScope)
		})

	case *tree.Insert:
		return b.processWiths(stmt.With, inScope, func(inScope *scope) *scope {
			return b.buildInsert(stmt, inScope)
		})

	case *tree.Update:
		return b.processWiths(stmt.With, inScope, func(inScope *scope) *scope {
			return b.buildUpdate(stmt, inScope)
		})

	case *tree.CreateTable:
		return b.buildCreateTable(stmt, inScope)

	case *tree.CreateView:
		return b.buildCreateView(stmt, inScope)

	case *tree.Explain:
		return b.buildExplain(stmt, inScope)

	case *tree.ExplainAnalyze:
		// This statement should have been handled by the executor.
		panic(pgerror.Newf(pgcode.Syntax, "EXPLAIN ANALYZE can only be used as a top-level statement"))

	case *tree.ShowTraceForSession:
		return b.buildShowTrace(stmt, inScope)

	case *tree.Split:
		return b.buildAlterTableSplit(stmt, inScope)

	case *tree.Unsplit:
		return b.buildAlterTableUnsplit(stmt, inScope)

	case *tree.Relocate:
		return b.buildAlterTableRelocate(stmt, inScope)

	case *tree.RelocateRange:
		return b.buildAlterRangeRelocate(stmt, inScope)

	case *tree.ControlJobs:
		return b.buildControlJobs(stmt, inScope)

	case *tree.ControlSchedules:
		return b.buildControlSchedules(stmt, inScope)

	case *tree.CancelQueries:
		return b.buildCancelQueries(stmt, inScope)

	case *tree.CancelSessions:
		return b.buildCancelSessions(stmt, inScope)

	case *tree.CreateStats:
		return b.buildCreateStatistics(stmt, inScope)

	case *tree.Analyze:
		// ANALYZE is syntactic sugar for CREATE STATISTICS. We add AS OF SYSTEM
		// TIME '-0.001ms' to trigger use of inconsistent scans. This prevents
		// GC TTL errors during ANALYZE. See the sql.stats.max_timestamp_age
		// setting.
		return b.buildCreateStatistics(&tree.CreateStats{
			Table: stmt.Table,
			Options: tree.CreateStatsOptions{
				AsOf: tree.AsOfClause{
					Expr: tree.NewStrVal("-0.001ms"),
				},
			},
		}, inScope)

	case *tree.Export:
		return b.buildExport(stmt, inScope)

	default:
		// See if this statement can be rewritten to another statement using the
		// delegate functionality.
		newStmt, err := delegate.TryDelegate(b.ctx, b.catalog, b.evalCtx, stmt)
		if err != nil {
			panic(err)
		}
		if newStmt != nil {
			// Many delegate implementations resolve objects. It would be tedious to
			// register all those dependencies with the metadata (for cache
			// invalidation). We don't care about caching plans for these statements.
			b.DisableMemoReuse = true
			return b.buildStmt(newStmt, desiredTypes, inScope)
		}

		// See if we have an opaque handler registered for this statement type.
		if outScope := b.tryBuildOpaque(stmt, inScope); outScope != nil {
			// The opaque handler may resolve objects; we don't care about caching
			// plans for these statements.
			b.DisableMemoReuse = true
			return outScope
		}
		panic(errors.AssertionFailedf("unexpected statement: %T", stmt))
	}
}

func (b *Builder) allocScope() *scope {
	if len(b.scopeAlloc) == 0 {
		// scope is relatively large (~250 bytes), so only allocate in small
		// chunks.
		b.scopeAlloc = make([]scope, 4)
	}
	r := &b.scopeAlloc[0]
	b.scopeAlloc = b.scopeAlloc[1:]
	r.builder = b
	return r
}

// trackReferencedColumnForViews is used to add a column to the view's
// dependencies. This should be called whenever a column reference is made in a
// view query.
func (b *Builder) trackReferencedColumnForViews(col *scopeColumn) {
	if b.trackViewDeps {
		for i := range b.viewDeps {
			dep := b.viewDeps[i]
			if ord, ok := dep.ColumnIDToOrd[col.id]; ok {
				dep.ColumnOrdinals.Add(ord)
			}
			b.viewDeps[i] = dep
		}
	}
}

func (b *Builder) maybeTrackRegclassDependenciesForViews(texpr tree.TypedExpr) {
	if b.trackViewDeps {
		if texpr.ResolvedType() == types.RegClass {
			// We do not add a dependency if the RegClass Expr contains variables,
			// we cannot resolve the variables in this context. This matches Postgres
			// behavior.
			if !tree.ContainsVars(texpr) {
				regclass, err := texpr.Eval(b.evalCtx)
				if err != nil {
					panic(err)
				}

				var ds cat.DataSource
				// Regclass can contain an ID or a string.
				// Ex. nextval('s'::regclass) and nextval(59::regclass) are both valid.
				id, err := strconv.Atoi(regclass.String())
				if err == nil {
					ds, _, err = b.catalog.ResolveDataSourceByID(b.ctx, cat.Flags{}, cat.StableID(id))
					if err != nil {
						panic(err)
					}
				} else {
					tn := tree.MakeUnqualifiedTableName(tree.Name(regclass.String()))
					ds, _, _ = b.resolveDataSource(&tn, privilege.SELECT)
				}

				b.viewDeps = append(b.viewDeps, opt.ViewDep{
					DataSource: ds,
				})
			}
		}
	}
}

func (b *Builder) maybeTrackUserDefinedTypeDepsForViews(texpr tree.TypedExpr) {
	if b.trackViewDeps {
		if texpr.ResolvedType().UserDefined() {
			children, err := typedesc.GetTypeDescriptorClosure(texpr.ResolvedType())
			if err != nil {
				panic(err)
			}
			for id := range children {
				b.viewTypeDeps.Add(int(id))
			}
		}
	}
}

// optTrackingTypeResolver is a wrapper around a TypeReferenceResolver that
// remembers all of the resolved types in the provided Metadata.
type optTrackingTypeResolver struct {
	res      tree.TypeReferenceResolver
	metadata *opt.Metadata
}

// ResolveType implements the TypeReferenceResolver interface.
func (o *optTrackingTypeResolver) ResolveType(
	ctx context.Context, name *tree.UnresolvedObjectName,
) (*types.T, error) {
	typ, err := o.res.ResolveType(ctx, name)
	if err != nil {
		return nil, err
	}
	o.metadata.AddUserDefinedType(typ)
	return typ, nil
}

// ResolveTypeByOID implements the tree.TypeResolver interface.
func (o *optTrackingTypeResolver) ResolveTypeByOID(
	ctx context.Context, oid oid.Oid,
) (*types.T, error) {
	typ, err := o.res.ResolveTypeByOID(ctx, oid)
	if err != nil {
		return nil, err
	}
	o.metadata.AddUserDefinedType(typ)
	return typ, nil
}
