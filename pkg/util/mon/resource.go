// Copyright 2017 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package mon

import (
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgcode"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
	"github.com/cockroachdb/errors"
)

// Resource is an interface used to abstract the specifics of tracking bytes
// usage by different types of resources.
// Resource 是一个接口，用于抽象跟踪不同类型资源的字节使用情况的细节。
type Resource interface {
	NewBudgetExceededError(requestedBytes int64, reservedBytes int64, budgetBytes int64) error
}

func newMemoryBudgetExceededError(
	requestedBytes int64, reservedBytes int64, budgetBytes int64,
) error {
	return pgerror.WithCandidateCode(
		errors.Newf(
			"memory budget exceeded: %d bytes requested, %d currently allocated, %d bytes in budget",
			errors.Safe(requestedBytes),
			errors.Safe(reservedBytes),
			errors.Safe(budgetBytes),
		),
		pgcode.OutOfMemory,
	)
}

// memoryResource is a Resource that represents memory.
// memoryResource 是代表内存的 Resource。
type memoryResource struct{}

// MemoryResource is a utility singleton used as an argument when creating a
// BytesMonitor to indicate that the monitor will be tracking memory usage.
// MemoryResource 是一个实用单例，在创建 BytesMonitor 时用作参数以指示监视器将跟踪内存使用情况。
var MemoryResource Resource = memoryResource{}

// NewBudgetExceededError implements the Resource interface.
func (m memoryResource) NewBudgetExceededError(
	requestedBytes int64, reservedBytes int64, budgetBytes int64,
) error {
	return newMemoryBudgetExceededError(requestedBytes, reservedBytes, budgetBytes)
}

// memoryResourceWithErrorHint is a Resource that represents memory and augments
// the "budget exceeded" error with a hint.
// memoryResourceWithErrorHint 是一种资源，代表内存并通过提示增加“超出预算”错误。
type memoryResourceWithErrorHint struct {
	hint string
}

// NewMemoryResourceWithErrorHint returns a new memory Resource that augments
// all "budget exceeded" errors with the given hint.
// NewMemoryResourceWithErrorHint 返回一个新的内存资源，它使用给定的提示增加所有“超出预算”的错误。
func NewMemoryResourceWithErrorHint(hint string) Resource {
	return memoryResourceWithErrorHint{hint: hint}
}

// NewBudgetExceededError implements the Resource interface.
func (m memoryResourceWithErrorHint) NewBudgetExceededError(
	requestedBytes int64, reservedBytes int64, budgetBytes int64,
) error {
	return errors.WithHint(
		newMemoryBudgetExceededError(requestedBytes, reservedBytes, budgetBytes),
		m.hint,
	)
}

// diskResource is a Resource that represents disk.
type diskResource struct{}

// DiskResource is a utility singleton used as an argument when creating a
// BytesMonitor to indicate that the monitor will be tracking disk usage.
var DiskResource Resource = diskResource{}

// NewBudgetExceededError implements the Resource interface.
func (d diskResource) NewBudgetExceededError(
	requestedBytes int64, reservedBytes int64, budgetBytes int64,
) error {
	return pgerror.WithCandidateCode(
		errors.Newf(
			"disk budget exceeded: %d bytes requested, %d currently allocated, %d bytes in budget",
			errors.Safe(requestedBytes),
			errors.Safe(reservedBytes),
			errors.Safe(budgetBytes),
		), pgcode.DiskFull)
}
