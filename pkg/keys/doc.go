// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// Package keys manages the construction of keys for CockroachDB's key-value
// layer.
//
// The keys package is necessarily tightly 紧紧 coupled 耦合的 to the storage package. In
// theory, it is oblivious to higher levels of the stack. In practice, it
// exposes several functions that blur abstraction boundaries to break
// dependency cycles. For example, EnsureSafeSplitKey knows far too much about
// how to decode SQL keys.
//
// 1. Overview
//
// This is the ten-thousand foot view of the keyspace:
//
//    +------------------+
//    | (empty)          | /Min
//    | \x01...          | /Local            ---------------------+
//    |                  |                                        |
//    | ...              |                                        | local keys
//    |                  |                                        |
//    |                  |                   ---------------------+
//    |                  |                   ---------------------+
//    | \x02...          | /Meta1            ----+                |
//    | \x03...          | /Meta2                |                |
//    | \x04...          | /System               |                |
//    |                  |                       | system keys    |
//    | ...              |                       |                |
//    |                  |                   ----+                |
//    | \x89...          | /Table/1          ----+                |
//    | \x8a...          | /Table/2              |                |
//    |                  |                       | system tenant  |
//    | ...              |                       |                | global keys
//    |                  |                   ----+                |
//    | \xfe\x8a\x89...  | /Tenant/2/Table/1 ----+                |
//    | \xfe\x8a\x8a...  | /Tenant/2/Table/2     |                |
//    |                  |                       | tenant 2       |
//    | ...              |                       |                |
//    |                  |                   ----+                |
//    | \xfe...          | /Tenant/...       ----+                |
//    | \xfe...          |                       |                |
//    |                  |                       | tenant ...     |
//    | ...              |                       |                |
//    |                  |                   ----+                |
//    | \xff\xff         | /Max              ---------------------+
//    +------------------+
//
// When keys are pretty printed, the logical name to the right of the table is
// shown instead of the raw byte sequence.
// 当键打印得很漂亮时，会显示表右侧的逻辑名称，而不是原始字节序列。
//
//
// 1. Key Ranges
//
// The keyspace is divided into contiguous, non-overlapping chunks called
// "ranges." A range is defined by its start and end keys. For example, a range
// might span from [/Table/1, /Table/2), where the lower bound is inclusive and
// the upper bound is exclusive. Any key that begins with /Table/1, like
// /Table/1/SomePrimaryKeyValue..., would belong to this range. Key ranges
// exist over the "resolved" keyspace, refer to the "Key Addressing" section
// below for more details.
// keyspace 被划分为连续的、不重叠的块，称为“ranges”。 range 由其开始键和结束键定义。
// 例如，范围可能从 [/Table/1, /Table/2) 开始，其中包含下限，不包含上限。
// 任何以 /Table/1 开头的键（例如 /Table/1/SomePrimaryKeyValue...）都属于此范围。
// 键范围存在于“已解析”键空间上，有关更多详细信息，请参阅下面的“键寻址”部分。
//
//
// 2. Local vs. Global Keys
//
// There are broadly two types of keys, "local" and "global":
// 密钥大致有两种类型：“本地”和“全局”：
//
//  (i) Local keys, such as store- and range-specific metadata, are keys that
//  must be physically collocated with the store and/or ranges they refer to but
//  also logically separated so that they do not pollute the user key space.
//  This is further elaborated on in the "Key Addressing" section below. Local
//  data also includes data "local" to a node, such as the store metadata and
//  the raft log, which is where the name originated.
//  本地键（例如特定于存储和范围的元数据）是必须与它们引用的存储和/或范围物理上并置的键，
//  但也必须在逻辑上分开，以便它们不会污染用户键空间。 这将在下面的“密钥寻址”部分中进一步详细阐述。
//  本地数据还包括节点“本地”的数据，例如存储元数据和 raft 日志，这也是名称的来源。
//
//  (ii) Non-local keys (for e.g. meta1, meta2, system, and SQL keys) are
//  collectively referred to as "global" keys.
//  非本地密钥（例如 meta1、meta2、系统和 SQL 密钥）统称为“全局”密钥。
//
// NB: The empty key (/Min) is a special case. No data is stored there, but it
// is used as the start key of the first range descriptor and as the starting
// point for some scans, in which case it acts like a global key.
// 空键 (/Min) 是一种特殊情况。 那里不存储任何数据，
// 但它用作第一个范围描述符的起始键以及某些扫描的起点，在这种情况下，它的作用类似于全局键。
//
// (Check `keymap` below for a more precise breakdown of the local and global
// keyspace.)
//（查看下面的“keymap”，了解本地和全局键空间的更精确细分。）
//
//
// 2. Key Addressing
//
// We also have this concept of the "address" for a key. Keys get "resolved"
// using `keys.Addr`, through which we're able to lookup the range "containing"
// the key. For global keys, the resolved key is the key itself.
// 我们也有密钥“地址”的概念。 键使用“keys.Addr”进行“解析”，通过它我们可以查找“包含”键的范围。
// 对于全局密钥，解析的密钥就是密钥本身。
//
// Local keys are special. For certain kinds of local keys (namely, addressable
// ones), the resolved key is obtained by stripping out the local key prefix,
// suffix, and optional details (refer to `keymap` below to understand how local
// keys are constructed). This level of indirection was introduced so that we
// could logically sort these local keys into a range other than what a
// strictly physical key based sort would entail. For example, the key
// /Local/Range/Table/1 would naturally sort into the range [/Min, /System), but
// its "address" is /Table/1, so it actually belongs to a range like [/Table1,
// /Table/2).
// 本地键很特殊。 对于某些类型的本地键（即可寻址的本地键），解析的键是通过去掉本地键前缀、
// 后缀和可选细节来获得的（请参阅下面的“keymap”以了解本地键是如何构造的）。
// 引入这种间接级别是为了我们可以在逻辑上将这些本地键排序到一个范围内，
// 而不是严格基于物理键的排序所需要的范围。 例如，
// 键 /Local/Range/Table/1 自然会排序到范围 [/Min, /System) 中，
// 但它的“地址”是 /Table/1，所以它实际上属于 [/Table1, /Table/2）。
//
// Consider the motivating example: we want to store a copy of the range
// descriptor in a key that's both (a) a part of the range, and (b) does not
// require us to remove a portion of the keyspace from the user (say by
// reserving some key suffix). Storing this information in the global keyspace
// would place the data on an arbitrary set of stores, with no guarantee of
// collocation. By being able to logically sort the range descriptor key next to
// the range itself, we're able to collocate the two.
// 考虑一个激励示例：我们希望将范围描述符的副本存储在一个键中，该键既 (a) 是范围的一部分，
// 又 (b) 不需要我们从用户中删除键空间的一部分 ( 比如说保留一些关键后缀）。
// 将此信息存储在全局密钥空间中会将数据放置在任意一组存储上，并且不保证搭配。
// 通过能够对范围本身旁边的范围描述符键进行逻辑排序，我们能够将两者并置。
//
//
// 3. (replicated) Range-ID local keys vs. Range local keys
//
// Deciding between replicated range-ID local keys and range local keys is not
// entirely straightforward, as the two key types serve similar purposes.
// Range-ID keys, as the name suggests, use the range-ID in the key. Range local
// keys instead use a key within the range bounds. Range-ID keys are not
// addressable whereas range-local keys are. Note that only addressable keys can
// be the target of KV operations, unaddressable keys can only be written as a
// side-effect of other KV operations. This can often makes the choice between
// the two clear (range descriptor keys needing to be addressable, and therefore
// being a range local key is one example of this). Not being addressable also
// implies not having multiple versions, and therefore never having intents.
// 在复制范围 ID 本地键和范围本地键之间做出决定并不完全简单，因为这两种键类型具有相似的用途。
// Range-ID 键，顾名思义，在键中使用 range-ID。 范围本地键改为使用范围边界内的键。
// 范围 ID 键不可寻址，而范围本地键则可以。 请注意，只有可寻址键才能成为 KV 操作的目标，
// 不可寻址键只能作为其他 KV 操作的副作用写入。 这通常可以使两者之间的选择变得清晰
// （范围描述符键需要可寻址，因此作为范围本地键就是一个例子）。 不可寻址还意味着没有多个版本，
// 因此永远不会有意图。
//
// The "behavioral" difference between range local keys and range-id local keys
// is that range local keys split and merge along range boundaries while
// range-id local keys don't. We want to move as little data as possible during
// splits and merges (in fact, we don't re-write any data during splits), and
// that generally determines which data sits where. If we want the split point
// of a range to dictate where certain keys end up, then they're likely meant to
// be range local keys. If not, they're meant to be range-ID local keys. Any key
// we need to re-write during splits/merges will needs to go through Raft. We
// have limits set on the size of Raft proposals so we generally don’t want to
// be re-writing lots of data. Range lock keys (see below) are separate from
// range local keys, but behave similarly in that they split and merge along
// range boundaries.
// range 本地键和 range-id 本地键之间的“行为”差异是 range 本地键沿着范围边界拆分和合并，
// 而 range-id 本地键则不然。 我们希望在拆分和合并期间移动尽可能少的数据
//（事实上，我们在拆分期间不会重写任何数据），这通常决定了哪些数据位于何处。
// 如果我们希望范围的分割点决定某些键的结束位置，那么它们很可能是范围本地键。
// 如果不是，它们就应该是范围 ID 本地键。 我们在拆分/合并期间需要重写的任何密钥都需要通过 Raft。
// 我们对 Raft 提案的大小设置了限制，因此我们通常不希望重写大量数据。
// 范围锁定键（见下文）与范围本地键分开，但其行为相似，因为它们沿着范围边界分割和合并。
//
// This naturally leads to range-id local keys being used to store metadata
// about a specific Range and range local keys being used to store metadata
// about specific "global" keys. Let us consider transaction record keys for
// example (ignoring for a second we also need them to be addressable). Hot
// ranges could potentially have lots of transaction keys. Keys destined for the
// RHS of the split need to be collocated with the RHS range. By categorizing
// them as as range local keys, we avoid needing to re-write them during splits
// as they automatically sort into the new range boundaries. If they were
// range-ID local keys, we'd have to update each transaction key with the new
// range ID.
// 这自然会导致 range-id 本地键用于存储有关特定 Range 的元数据，
// 而 range 本地键用于存储有关特定“全局”键的元数据。
// 让我们考虑一下交易记录键（暂时忽略，我们还需要它们是可寻址的）。
// 热范围可能有很多交易密钥。 指定给拆分的 RHS 的密钥需要与 RHS 范围并置。
// 通过将它们分类为范围本地键，我们可以避免在分割期间重写它们，因为它们会自动排序到新的范围边界。
// 如果它们是范围 ID 本地密钥，我们必须使用新的范围 ID 更新每个事务密钥。
package keys

// NB: The sorting order of the symbols below map to the physical layout.
// Preserve group-wise ordering when adding new constants.
// 下面符号的排序顺序映射到物理布局。 添加新常量时保留按组排序。
var _ = [...]interface{}{
	MinKey,

	// There are five types of local key data enumerated below: replicated
	// range-ID, unreplicated range-ID, range local, store-local, and range lock
	// keys. Range lock keys are required to be last category of keys in the
	// lock key space.
	// 下面列举了五种本地密钥数据：复制的 range-ID、非复制的 range-ID、范围本地、存储本地和范围锁定密钥。
	// 范围锁定键必须是锁定键空间中最后一类键。
	// Local keys are constructed using a prefix, an optional infix, and a
	// suffix. The prefix and infix are used to disambiguate between the four
	// types of local keys listed above, and determines inter-group ordering.
	// The string comment next to each symbol below is the suffix pertaining to
	// the corresponding key (and determines intra-group ordering).
	// 本地键是使用前缀、可选中缀和后缀构造的。 前缀和中缀用于消除上面列出的四种类型的本地键之间的歧义，
	// 并确定组间排序。 下面每个符号旁边的字符串注释是与相应键相关的后缀（并确定组内排序）。
	// 	  - RangeID replicated keys all share `LocalRangeIDPrefix` and
	// 		`LocalRangeIDReplicatedInfix`.
	// RangeID 复制密钥都共享“LocalRangeIDPrefix”和“LocalRangeIDReplicatedInfix”。
	// 	  - RangeID unreplicated keys all share `LocalRangeIDPrefix` and
	// 		`localRangeIDUnreplicatedInfix`.
	// RangeID 非复制密钥都共享 `LocalRangeIDPrefix` 和 `localRangeIDUnreplicatedInfix`。
	// 	  - Range local keys all share `LocalRangePrefix`.
	//	  - Store keys all share `localStorePrefix`.
	// 	  - Range lock (which are also local keys) all share
	//	  `LocalRangeLockTablePrefix`.
	//
	// `LocalRangeIDPrefix`, `localRangePrefix`, `localStorePrefix`, and
	// `LocalRangeLockTablePrefix` all in turn share `LocalPrefix`.
	// `LocalPrefix` was chosen arbitrarily. Local keys would work just as well
	// with a different prefix, like 0xff, or even with a suffix.

	//   1. Replicated range-ID local keys: These store metadata pertaining to a
	//   range as a whole. Though they are replicated, they are unaddressable.
	//   Typical examples are MVCC stats and the abort span. They all share
	//   `LocalRangeIDPrefix` and `LocalRangeIDReplicatedInfix`.
	AbortSpanKey,             // "abc-"
	RangeGCThresholdKey,      // "lgc-"
	RangeAppliedStateKey,     // "rask"
	RangeLeaseKey,            // "rll-"
	RangePriorReadSummaryKey, // "rprs"
	RangeVersionKey,          // "rver"

	//   2. Unreplicated range-ID local keys: These contain metadata that
	//   pertain to just one replica of a range. They are unreplicated and
	//   unaddressable. The typical example is the Raft log. They all share
	//   `LocalRangeIDPrefix` and `localRangeIDUnreplicatedInfix`.
	RangeTombstoneKey,              // "rftb"
	RaftHardStateKey,               // "rfth"
	RaftLogKey,                     // "rftl"
	RaftReplicaIDKey,               // "rftr"
	RaftTruncatedStateKey,          // "rftt"
	RangeLastReplicaGCTimestampKey, // "rlrt"

	//   3. Range local keys: These also store metadata that pertains to a range
	//   as a whole. They are replicated and addressable. Typical examples are
	//   the range descriptor and transaction records. They all share
	//   `LocalRangePrefix`.
	RangeProbeKey,         // "prbe"
	QueueLastProcessedKey, // "qlpt"
	RangeDescriptorKey,    // "rdsc"
	TransactionKey,        // "txn-"

	//   4. Store local keys: These contain metadata about an individual store.
	//   They are unreplicated and unaddressable. The typical example is the
	//   store 'ident' record. They all share `localStorePrefix`.
	StoreClusterVersionKey,        // "cver"
	StoreGossipKey,                // "goss"
	StoreHLCUpperBoundKey,         // "hlcu"
	StoreIdentKey,                 // "iden"
	StoreUnsafeReplicaRecoveryKey, // "loqr"
	StoreNodeTombstoneKey,         // "ntmb"
	StoreCachedSettingsKey,        // "stng"
	StoreLastUpKey,                // "uptm"

	//   5. Range lock keys for all replicated locks. All range locks share
	//   LocalRangeLockTablePrefix. Locks can be acquired on global keys and on
	//   range local keys. Currently, locks are only on single keys, i.e., not
	//   on a range of keys. Only exclusive locks are currently supported, and
	//   these additionally function as pointers to the provisional MVCC values.
	//   Single key locks use a byte, LockTableSingleKeyInfix, that follows
	//   the LocalRangeLockTablePrefix. This is to keep the single-key locks
	//   separate from (future) range locks.
	LockTableSingleKey,

	// The global keyspace includes the meta{1,2}, system, system tenant SQL
	// keys, and non-system tenant SQL keys.
	//
	// 	1. Meta keys: This is where we store all key addressing data.
	MetaMin,
	Meta1Prefix,
	Meta2Prefix,
	MetaMax,

	// 	2. System keys: This is where we store global, system data which is
	// 	replicated across the cluster.
	SystemPrefix,
	NodeLivenessPrefix,     // "\x00liveness-"
	BootstrapVersionKey,    // "bootstrap-version"
	descIDGenerator,        // "desc-idgen"
	NodeIDGenerator,        // "node-idgen"
	RangeIDGenerator,       // "range-idgen"
	StatusPrefix,           // "status-"
	StatusNodePrefix,       // "status-node-"
	StoreIDGenerator,       // "store-idgen"
	MigrationPrefix,        // "system-version/"
	MigrationLease,         // "system-version/lease"
	TimeseriesPrefix,       // "tsd"
	SystemSpanConfigPrefix, // "xffsys-scfg"
	SystemMax,

	// 	3. System tenant SQL keys: This is where we store all system-tenant
	// 	table data.
	TableDataMin,
	NamespaceTableMin,
	TableDataMax,

	//  4. Non-system tenant SQL keys: This is where we store all non-system
	//  tenant table data.
	TenantTableDataMin,
	TenantTableDataMax,

	MaxKey,
}

// Unused, deprecated keys.
var _ = [...]interface{}{
	localRaftLastIndexSuffix,
	localRangeFrozenStatusSuffix,
	localRangeLastVerificationTimestampSuffix,
	localRemovedLeakedRaftEntriesSuffix,
	localTxnSpanGCThresholdSuffix,
}
