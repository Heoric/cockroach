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
Package telemetry contains helpers for capturing diagnostics information.
遥测包包含用于捕获诊断信息的帮助程序。

Telemetry is captured and shared with cockroach labs if enabled to help the team
prioritize new and existing features or configurations. The docs include more
information about this telemetry, what it includes and how to configure it.
如果能够帮助团队确定新的和现有的功能或配置的优先级，则会捕获遥测数据并与蟑螂实验室共享。
这些文档包含有关此遥测、其包含内容以及如何配置它的更多信息。

When trying to measure the usage of a given feature, the existing reporting of
"scrubbed" queries -- showing the structure but not the values -- can serve as a
means to measure eg. how many clusters use BACKUP or window functions, etc.
However many features cannot be easily measured just from these statements,
either because they are cannot be reliably inferred from a scrubbed query or
are simply not involved in a SQL statement execution at all.
当尝试测量给定功能的使用情况时，现有的“清理”查询报告（显示结构但不显示值）可以作为测量的手段，例如。
有多少集群使用 BACKUP 或窗口函数等。但是，许多功能无法仅通过这些语句轻松测量，
因为它们无法从清理的查询中可靠地推断出来，或者根本不参与 SQL 语句执行。

For such features we also have light-weight `telemetry.Count("some.feature")` to
track their usage. These increment in-memory counts that are then included with
existing diagnostics reporting if enabled. Some notes on using these:
对于此类功能，我们还使用轻量级 `telemetry.Count("some.feature")` 来跟踪它们的使用情况。
这些增量内存计数将包含在现有诊断报告中（如果启用）。 使用这些的一些注意事项：
  - "some.feature" should always be a literal string constant -- it must not
    include any user-submitted data.
		“some.feature”应该始终是一个文字字符串常量——它不能包含任何用户提交的数据。
  - Contention-sensitive, high-volume callers should use an initial `GetCounter`
		to get a Counter they can then `Inc` repeatedly instead to avoid contention
		and map lookup over around the name resolution on each increment.
    对争用敏感的大容量调用者应使用初始的“GetCounter”来获取计数器，然后可以重复使用“Inc”，
    以避免争用并在每个增量的名称解析周围进行映射查找。
	-	When naming a counter, by convention we use dot-separated, dashed names, eg.
		`feature-area.specific-feature`.
		在命名计数器时，按照惯例，我们使用点分隔的短划线名称，例如“feature-area.specific-feature”。
*/
package telemetry
