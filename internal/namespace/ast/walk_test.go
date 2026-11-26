// Copyright © 2026 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package ast_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ory/keto/internal/namespace/ast"
)

func TestOrderedChildren(t *testing.T) {
	t.Parallel()

	invert := &ast.InvertResult{Child: &ast.ComputedSubjectSet{Relation: "inverted"}}
	nested := &ast.SubjectSetRewrite{Children: ast.Children{
		&ast.ComputedSubjectSet{Relation: "nested"},
	}}
	traverse := &ast.TupleToSubjectSet{Relation: "parent", ComputedSubjectSetRelation: "viewer"}
	computed := &ast.ComputedSubjectSet{Relation: "viewer"}
	rewrite := &ast.SubjectSetRewrite{Children: ast.Children{invert, nested, traverse, computed}}

	assert.Equal(t, ast.Children{computed, traverse, nested, invert}, ast.OrderedChildren(rewrite))
	assert.Equal(t, ast.Children{invert, nested, traverse, computed}, rewrite.Children,
		"ordering must not mutate the parsed expression")
}

func TestWalkCanVisitTheFullTree(t *testing.T) {
	t.Parallel()

	var visited []string
	visitor := recordingVisitor{
		onNode: func(name string) {
			visited = append(visited, name)
		},
	}
	rewrite := &ast.SubjectSetRewrite{Children: ast.Children{
		&ast.InvertResult{Child: &ast.ComputedSubjectSet{Relation: "inverted"}},
		&ast.TupleToSubjectSet{Relation: "parent", ComputedSubjectSetRelation: "viewer"},
		&ast.ComputedSubjectSet{Relation: "viewer"},
	}}

	_, err := ast.Walk(context.Background(), rewrite, visitor)
	require.NoError(t, err)
	assert.Equal(t, []string{"rewrite", "computed:viewer", "traverse:parent", "invert", "computed:inverted"}, visited)
}

func TestWalkLeavesShortCircuitingToTheVisitor(t *testing.T) {
	t.Parallel()

	var visited []string
	visitor := recordingVisitor{
		onNode: func(name string) {
			visited = append(visited, name)
		},
		stopAfterFirstChild: true,
	}
	rewrite := &ast.SubjectSetRewrite{Children: ast.Children{
		&ast.TupleToSubjectSet{Relation: "parent", ComputedSubjectSetRelation: "viewer"},
		&ast.ComputedSubjectSet{Relation: "viewer"},
	}}

	_, err := ast.Walk(context.Background(), rewrite, visitor)
	require.NoError(t, err)
	assert.Equal(t, []string{"rewrite", "computed:viewer"}, visited)
}

type recordingVisitor struct {
	onNode              func(string)
	stopAfterFirstChild bool
}

func (v recordingVisitor) VisitComputedSubjectSet(_ context.Context, node *ast.ComputedSubjectSet) (struct{}, error) {
	v.onNode("computed:" + node.Relation)
	return struct{}{}, nil
}

func (v recordingVisitor) VisitTupleToSubjectSet(_ context.Context, node *ast.TupleToSubjectSet) (struct{}, error) {
	v.onNode("traverse:" + node.Relation)
	return struct{}{}, nil
}

func (v recordingVisitor) VisitSubjectSetRewrite(ctx context.Context, _ *ast.SubjectSetRewrite, children ast.Children, walk ast.WalkFunc[struct{}]) (struct{}, error) {
	v.onNode("rewrite")
	for i, child := range children {
		if _, err := walk(ctx, child); err != nil {
			return struct{}{}, err
		}
		if v.stopAfterFirstChild && i == 0 {
			break
		}
	}
	return struct{}{}, nil
}

func (v recordingVisitor) VisitInvertResult(ctx context.Context, node *ast.InvertResult, walk ast.WalkFunc[struct{}]) (struct{}, error) {
	v.onNode("invert")
	return walk(ctx, node.Child)
}
