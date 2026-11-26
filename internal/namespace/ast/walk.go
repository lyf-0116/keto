// Copyright © 2026 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package ast

import (
	"context"
	"slices"

	"github.com/pkg/errors"
)

// ChildKind identifies a concrete rewrite expression node.
type ChildKind int

const (
	ChildKindUnknown ChildKind = iota
	ChildKindComputedSubjectSet
	ChildKindTupleToSubjectSet
	ChildKindSubjectSetRewrite
	ChildKindInvertResult
)

// ErrUnsupportedChild is returned when a rewrite contains an unknown node type.
var ErrUnsupportedChild = errors.New("not implemented: unknown rewrite child type")

// Dispatch resolves the concrete kind of a rewrite child. All consumers of
// rewrite expressions must use this function instead of switching on the AST
// type themselves.
func Dispatch(child Child) (ChildKind, error) {
	switch child := child.(type) {
	case *ComputedSubjectSet:
		if child == nil {
			return ChildKindUnknown, ErrUnsupportedChild
		}
		return ChildKindComputedSubjectSet, nil
	case *TupleToSubjectSet:
		if child == nil {
			return ChildKindUnknown, ErrUnsupportedChild
		}
		return ChildKindTupleToSubjectSet, nil
	case *SubjectSetRewrite:
		if child == nil {
			return ChildKindUnknown, ErrUnsupportedChild
		}
		return ChildKindSubjectSetRewrite, nil
	case *InvertResult:
		if child == nil {
			return ChildKindUnknown, ErrUnsupportedChild
		}
		return ChildKindInvertResult, nil
	default:
		return ChildKindUnknown, ErrUnsupportedChild
	}
}

// Cost returns the relative evaluation cost of a node kind. Cheaper nodes are
// visited first.
func (k ChildKind) Cost() int {
	switch k {
	case ChildKindComputedSubjectSet:
		return 0
	case ChildKindTupleToSubjectSet:
		return 1
	case ChildKindSubjectSetRewrite:
		return 2
	case ChildKindInvertResult:
		return 3
	default:
		return 4
	}
}

// CostOf returns the evaluation cost of a rewrite child.
func CostOf(child Child) int {
	kind, err := Dispatch(child)
	if err != nil {
		return ChildKindUnknown.Cost()
	}
	return kind.Cost()
}

// OrderedChildren returns a copy of the rewrite's children sorted by evaluation
// cost. The original expression is not modified, and children with equal cost
// keep their declaration order.
func OrderedChildren(rewrite *SubjectSetRewrite) Children {
	if rewrite == nil {
		return nil
	}
	children := slices.Clone(rewrite.Children)
	slices.SortStableFunc(children, func(a, b Child) int {
		return CostOf(a) - CostOf(b)
	})
	return children
}

// WalkFunc continues a traversal from a rewrite child.
type WalkFunc[T any] func(ctx context.Context, child Child) (T, error)

// Visitor controls the semantics of a rewrite traversal. Rewrite and invert
// visitors decide whether to continue traversal through walk. This keeps
// short-circuiting an evaluation concern while allowing full-tree consumers to
// visit every branch.
type Visitor[T any] interface {
	VisitComputedSubjectSet(ctx context.Context, node *ComputedSubjectSet) (T, error)
	VisitTupleToSubjectSet(ctx context.Context, node *TupleToSubjectSet) (T, error)
	VisitSubjectSetRewrite(ctx context.Context, node *SubjectSetRewrite, children Children, walk WalkFunc[T]) (T, error)
	VisitInvertResult(ctx context.Context, node *InvertResult, walk WalkFunc[T]) (T, error)
}

// Walk dispatches and traverses a rewrite expression. Subject-set rewrites
// receive their children in cost order. The visitor owns the recursion policy,
// so callers can stop after a decisive branch or visit the complete tree.
func Walk[T any](ctx context.Context, child Child, visitor Visitor[T]) (T, error) {
	var zero T

	kind, err := Dispatch(child)
	if err != nil {
		return zero, err
	}

	switch kind {
	case ChildKindComputedSubjectSet:
		return visitor.VisitComputedSubjectSet(ctx, child.(*ComputedSubjectSet))
	case ChildKindTupleToSubjectSet:
		return visitor.VisitTupleToSubjectSet(ctx, child.(*TupleToSubjectSet))
	case ChildKindSubjectSetRewrite:
		node := child.(*SubjectSetRewrite)
		children := OrderedChildren(node)
		return visitor.VisitSubjectSetRewrite(ctx, node, children, func(ctx context.Context, child Child) (T, error) {
			return Walk(ctx, child, visitor)
		})
	case ChildKindInvertResult:
		return visitor.VisitInvertResult(ctx, child.(*InvertResult), func(ctx context.Context, child Child) (T, error) {
			return Walk(ctx, child, visitor)
		})
	default:
		return zero, ErrUnsupportedChild
	}
}
