// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package tree

// AnnotationIdx is the 1-based index of an annotation. AST nodes that can
// be annotated store such an index (unique within that AST).
// AnnotationIdx 是注释的从 1 开始的索引。 可以注释的 AST 节点存储这样的索引（在该 AST 中是唯一的）。
type AnnotationIdx int32

// NoAnnotation is the uninitialized annotation index.
// NoAnnotation 是未初始化的注解索引。
const NoAnnotation AnnotationIdx = 0

// AnnotatedNode is embedded in AST nodes that have an annotation.
// AnnotatedNode 嵌入在具有注释的 AST 节点中。
type AnnotatedNode struct {
	AnnIdx AnnotationIdx
}

// GetAnnotation retrieves the annotation associated with this node.
func (n AnnotatedNode) GetAnnotation(ann *Annotations) interface{} {
	if n.AnnIdx == NoAnnotation {
		return nil
	}
	return ann.Get(n.AnnIdx)
}

// SetAnnotation sets the annotation associated with this node.
func (n AnnotatedNode) SetAnnotation(ann *Annotations, annotation interface{}) {
	ann.Set(n.AnnIdx, annotation)
}

// Annotations is a container for AST annotations.
// Annotations 是 AST 注释的容器。
type Annotations []interface{}

// MakeAnnotations allocates an annotations container of the given size.
func MakeAnnotations(numAnnotations AnnotationIdx) Annotations {
	return make(Annotations, numAnnotations)
}

// Set an annotation in the container.
func (a *Annotations) Set(idx AnnotationIdx, annotation interface{}) {
	(*a)[idx-1] = annotation
}

// Get an annotation from the container.
func (a *Annotations) Get(idx AnnotationIdx) interface{} {
	return (*a)[idx-1]
}
