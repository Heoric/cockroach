// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// Package cat contains interfaces that are used by the query optimizer to avoid
// including specifics of sqlbase structures in the opt code.
// 包 cat 包含查询优化器使用的接口，以避免在 opt 代码中包含 sqlbase 结构的细节。
package cat

import (
	"context"

	"github.com/cockroachdb/cockroach/pkg/security"
	"github.com/cockroachdb/cockroach/pkg/sql/privilege"
	"github.com/cockroachdb/cockroach/pkg/sql/roleoption"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/lib/pq/oid"
)

// StableID permanently and uniquely identifies a catalog object (table, view,
// index, column, etc.) within its scope:
// StableID 在其作用域内永久唯一标识一个目录对象（表、视图、索引、列等）：
//
//   data source StableID: unique within database
//   index StableID: unique within table
//   column StableID: unique within table
//
// If a new catalog object is created, it will always be assigned a new StableID
// that has never, and will never, be reused by a different object in the same
// scope. This uniqueness guarantee is true even if the new object has the same
// name and schema as an old (and possibly dropped) object. The StableID will
// never change as long as the object exists.
// 如果创建了一个新的目录对象，它总是会被分配一个新的 StableID，这个 ID 从来没有，
// 也永远不会被同一范围内的不同对象重用。 即使新对象与旧（并且可能已删除）对象具有相同的名称和模式，
// 这种唯一性保证也是如此。 只要对象存在，StableID 就永远不会改变。
//
// Note that while two instances of the same catalog object will always have the
// same StableID, they can have different schema if the schema has changed over
// time. See the Version type comments for more details.
// 请注意，虽然同一目录对象的两个实例始终具有相同的 StableID，但如果架构随时间发生变化，
// 它们可能具有不同的架构。 有关详细信息，请参阅版本类型注释。
//
// For most sqlbase objects, the StableID is the 32-bit descriptor ID. However,
// this is not always the case. For example, the StableID for virtual tables
// prepends the database ID, since the same descriptor ID is reused across
// databases.
// 对于大多数 sqlbase 对象，StableID 是 32 位描述符 ID。 然而，这并非总是如此。
// 例如，虚拟表的 StableID 在数据库 ID 之前，因为相同的描述符 ID 在数据库中重复使用。
type StableID uint64

// SchemaName is an alias for tree.ObjectNamePrefix, since it consists of the
// catalog + schema name.
// SchemaName 是 tree.ObjectNamePrefix 的别名，因为它由目录 + 架构名称组成。
type SchemaName = tree.ObjectNamePrefix

// Flags allows controlling aspects of some Catalog operations.
// Flags 允许控制某些 Catalog 操作的各个方面。
type Flags struct {
	// AvoidDescriptorCaches avoids using any cached descriptors (for tables,
	// views, schemas, etc). This is useful in cases where we are running a
	// statement like SHOW and we don't want to get table leases or otherwise
	// pollute the caches.
	// AvoidDescriptorCaches 避免使用任何缓存的描述符（用于表、视图、模式等）。
	// 这在我们运行 SHOW 之类的语句并且我们不想获得表租约或以其他方式污染缓存的情况下很有用。
	AvoidDescriptorCaches bool

	// NoTableStats doesn't retrieve table statistics. This should be used in all
	// cases where we don't need them (like SHOW variants), to avoid polluting the
	// stats cache.
	// NoTableStats 不检索表统计信息。 这应该在我们不需要它们的所有情况下使用（比如 SHOW 变体），
	// 以避免污染统计缓存。
	NoTableStats bool
}

// Catalog is an interface to a database catalog, exposing only the information
// needed by the query optimizer.
// Catalog 是数据库目录的接口，仅公开查询优化器所需的信息。
//
// NOTE: Catalog implementations need not be thread-safe. However, the objects
// returned by the Resolve methods (schemas and data sources) *must* be
// immutable after construction, and therefore also thread-safe.
// 注意：目录实现不需要是线程安全的。 但是，Resolve 方法（模式和数据源）
// 返回的对象*必须*在构造后是不可变的，因此也是线程安全的。
type Catalog interface {
	// ResolveSchema locates a schema with the given name and returns it along
	// with the resolved SchemaName (which has all components filled in).
	// If the SchemaName is empty, returns the current database/schema (if one is
	// set; otherwise returns an error).
	// ResolveSchema 找到具有给定名称的模式并将其与解析的 SchemaName（已填充所有组件）一起返回。
	// 如果 SchemaName 为空，则返回当前数据库/模式（如果已设置；否则返回错误）。
	//
	// The resolved SchemaName is the same with the resulting Schema.Name() except
	// that it has the ExplicitCatalog/ExplicitSchema flags set to correspond to
	// the input name. Its use is mainly for cosmetic purposes.
	// 解析的 SchemaName 与生成的 Schema.Name() 相同，除了它具有
	// ExplicitCatalog/ExplicitSchema 标志设置以对应于输入名称。 它的用途主要是用于美容目的。
	//
	// If no such schema exists, then ResolveSchema returns an error.
	// 如果不存在这样的模式，则 ResolveSchema 返回错误。
	//
	// NOTE: The returned schema must be immutable after construction, and so can
	// be safely copied or used across goroutines.
	// 注意：返回的模式在构建后必须是不可变的，因此可以安全地复制或跨 goroutines 使用。
	ResolveSchema(ctx context.Context, flags Flags, name *SchemaName) (Schema, SchemaName, error)

	// ResolveDataSource locates a data source with the given name and returns it
	// along with the resolved DataSourceName.
	// ResolveDataSource 定位具有给定名称的数据源并将其与已解析的 DataSourceName 一起返回。
	//
	// The resolved DataSourceName is the same with the resulting
	// DataSource.Name() except that it has the ExplicitCatalog/ExplicitSchema
	// flags set to correspond to the input name. Its use is mainly for cosmetic
	// purposes. For example: the input name might be "t". The fully qualified
	// DataSource.Name() would be "currentdb.public.t"; the returned
	// DataSourceName would have the same fields but would still be formatted as
	// "t".
	//
	// If no such data source exists, then ResolveDataSource returns an error.
	//
	// NOTE: The returned data source must be immutable after construction, and
	// so can be safely copied or used across goroutines.
	ResolveDataSource(
		ctx context.Context, flags Flags, name *DataSourceName,
	) (DataSource, DataSourceName, error)

	// ResolveDataSourceByID is similar to ResolveDataSource, except that it
	// locates a data source by its StableID. See the comment for StableID for
	// more details.
	//
	// If the table is in the process of being added, returns an
	// "undefined relation" error but also returns isAdding=true.
	//
	// NOTE: The returned data source must be immutable after construction, and
	// so can be safely copied or used across goroutines.
	ResolveDataSourceByID(
		ctx context.Context, flags Flags, id StableID,
	) (_ DataSource, isAdding bool, _ error)

	// ResolveTypeByOID is used to look up a user defined type by ID.
	ResolveTypeByOID(ctx context.Context, oid oid.Oid) (*types.T, error)

	// ResolveType is used to resolve an unresolved object name.
	ResolveType(
		ctx context.Context, name *tree.UnresolvedObjectName,
	) (*types.T, error)

	// CheckPrivilege verifies that the current user has the given privilege on
	// the given catalog object. If not, then CheckPrivilege returns an error.
	CheckPrivilege(ctx context.Context, o Object, priv privilege.Kind) error

	// CheckAnyPrivilege verifies that the current user has any privilege on
	// the given catalog object. If not, then CheckAnyPrivilege returns an error.
	CheckAnyPrivilege(ctx context.Context, o Object) error

	// HasAdminRole checks that the current user has admin privileges. If yes,
	// returns true. Returns an error if query on the `system.users` table failed
	HasAdminRole(ctx context.Context) (bool, error)

	// RequireAdminRole checks that the current user has admin privileges. If not,
	// returns an error.
	RequireAdminRole(ctx context.Context, action string) error

	// HasRoleOption converts the roleoption to its SQL column name and checks if
	// the user belongs to a role where the option has value true. Requires a
	// valid transaction to be open.
	//
	// This check should be done on the version of the privilege that is stored in
	// the role options table. Example: CREATEROLE instead of NOCREATEROLE.
	// NOLOGIN instead of LOGIN.
	HasRoleOption(ctx context.Context, roleOption roleoption.Option) (bool, error)

	// FullyQualifiedName retrieves the fully qualified name of a data source.
	// Note that:
	//  - this call may involve a database operation so it shouldn't be used in
	//    performance sensitive paths;
	//  - the fully qualified name of a data source object can change without the
	//    object itself changing (e.g. when a database is renamed).
	FullyQualifiedName(ctx context.Context, ds DataSource) (DataSourceName, error)

	// RoleExists returns true if the role exists.
	RoleExists(ctx context.Context, role security.SQLUsername) (bool, error)
}
