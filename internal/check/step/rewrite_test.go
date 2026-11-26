// Copyright © 2026 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package step

import (
	"context"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"

	"github.com/ory/keto/internal/check"
	"github.com/ory/keto/internal/namespace/ast"
	"github.com/ory/keto/internal/relationtuple"
)

func TestPlanRewriteUsesSharedCostOrder(t *testing.T) {
	t.Parallel()

	rewrite := &ast.SubjectSetRewrite{
		Operation: ast.OperatorAnd,
		Children: ast.Children{
			&ast.InvertResult{Child: &ast.ComputedSubjectSet{Relation: "denied"}},
			&ast.SubjectSetRewrite{Children: ast.Children{
				&ast.ComputedSubjectSet{Relation: "nested"},
			}},
			&ast.TupleToSubjectSet{Relation: "parent", ComputedSubjectSetRelation: "viewer"},
			&ast.ComputedSubjectSet{Relation: "viewer"},
		},
	}

	steps, err := planRewrite(context.Background(), nil, check.CheckRequest{
		Tuple: &relationtuple.RelationTuple{
			Namespace: "File",
			Object:    uuid.Must(uuid.NewV4()),
			Relation:  "view",
			Subject:   &relationtuple.SubjectID{ID: uuid.Must(uuid.NewV4())},
		},
		RestDepth: 10,
	}, rewrite)
	require.NoError(t, err)

	kinds := make([]check.StepKind, len(steps))
	for i, step := range steps {
		kinds[i] = step.Step.Kind()
	}
	require.Equal(t, []check.StepKind{
		check.StepComputed,
		check.StepTraverse,
		check.StepRewrite,
		check.StepInvert,
	}, kinds)
}
