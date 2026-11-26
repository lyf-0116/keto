// Copyright © 2026 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package step

import (
	"context"

	"github.com/ory/keto/internal/check"
	"github.com/ory/keto/internal/namespace"
	"github.com/ory/keto/internal/namespace/ast"
	"github.com/ory/keto/internal/relationtuple"
)

// RewriteStep evaluates a SubjectSetRewrite node (OR or AND) using the shared
// rewrite traversal. It translates the rewrite's children into PlannedSteps and
// then delegates to RunUnion (OR) or RunIntersection (AND), preserving the
// check engine's short-circuit semantics.
type RewriteStep struct {
	Rewrite *ast.SubjectSetRewrite
}

func (RewriteStep) Kind() check.StepKind { return check.StepRewrite }

func (s RewriteStep) Execute(ctx context.Context, req check.CheckRequest, ex check.Executor) check.Result {
	if req.RestDepth <= 0 {
		return maxDepthReached(ex, req)
	}

	ex.Deps().Logger().
		WithField("request", req.Tuple.String()).
		Trace("check subject-set rewrite")

	result, err := ast.Walk(ctx, s.Rewrite, rewriteCheckVisitor{ex: ex, req: req})
	if err != nil {
		return check.Result{Err: err}
	}
	return result
}

type rewriteCheckVisitor struct {
	ex  check.Executor
	req check.CheckRequest
}

func (v rewriteCheckVisitor) VisitComputedSubjectSet(ctx context.Context, node *ast.ComputedSubjectSet) (check.Result, error) {
	return v.ex.Run(ctx, ComputedUsersetStep{Relation: node.Relation}, v.req), nil
}

func (v rewriteCheckVisitor) VisitTupleToSubjectSet(ctx context.Context, node *ast.TupleToSubjectSet) (check.Result, error) {
	return v.ex.Run(ctx, TraverseStep{TTU: node}, v.req), nil
}

func (v rewriteCheckVisitor) VisitInvertResult(ctx context.Context, node *ast.InvertResult, _ ast.WalkFunc[check.Result]) (check.Result, error) {
	return v.ex.Run(ctx, InvertStep{Child: node.Child}, v.req), nil
}

func (v rewriteCheckVisitor) VisitSubjectSetRewrite(ctx context.Context, node *ast.SubjectSetRewrite, children ast.Children, _ ast.WalkFunc[check.Result]) (check.Result, error) {
	steps, err := planRewriteChildren(ctx, v.ex, v.req, node, children)
	if err != nil {
		return check.Result{}, err
	}

	if node.Operation == ast.OperatorAnd {
		return v.ex.RunIntersection(ctx, steps...), nil
	}
	return v.ex.RunUnion(ctx, steps...), nil
}

// planRewrite translates a SubjectSetRewrite AST node into a flat list of
// PlannedSteps ready for execution. It applies the OR-shortcut optimization:
// ComputedSubjectSet children in an OR are batched into a single
// DirectMultiStep followed by individual IsAllowedStep calls.
func planRewrite(ctx context.Context, ex check.Executor, req check.CheckRequest, rewrite *ast.SubjectSetRewrite) ([]check.PlannedStep, error) {
	return planRewriteChildren(ctx, ex, req, rewrite, ast.OrderedChildren(rewrite))
}

func planRewriteChildren(ctx context.Context, ex check.Executor, req check.CheckRequest, rewrite *ast.SubjectSetRewrite, children ast.Children) ([]check.PlannedStep, error) {
	var steps []check.PlannedStep
	handled := make(map[int]struct{}, len(children))

	// OR shortcut: batch all ComputedSubjectSet children into one DirectMulti
	// check plus individual IsAllowed calls (matching the current engine's
	// OR-of-computed-usersets optimization).
	if rewrite.Operation == ast.OperatorOr {
		var computedRelations []string
		for i, child := range children {
			if kind, _ := ast.Dispatch(child); kind == ast.ChildKindComputedSubjectSet {
				handled[i] = struct{}{}
				computedRelations = append(computedRelations, child.(*ast.ComputedSubjectSet).Relation)
			}
		}

		if len(computedRelations) > 0 {
			namespaceManager, err := ex.Deps().Config(ctx).NamespaceManager()
			if err != nil {
				return nil, err
			}

			// In strict mode, skip direct checks for relations where OPL does not
			// allow a direct subject match, mirroring the DirectStep gate in
			// IsAllowedStep so nested and top-level checks agree.
			var directRelations []string
			isStrictMode := ex.Deps().Config(ctx).StrictMode()
			for _, rel := range computedRelations {
				astRel, _ := namespace.ASTRelationFor(ctx, namespaceManager, req.Tuple.Namespace, rel)
				if isStrictMode && !AllowsDirectMember(astRel, req.Tuple.Subject) {
					continue
				}
				directRelations = append(directRelations, rel)
			}

			if len(directRelations) > 0 {
				steps = append(steps, check.PlannedStep{
					Step: DirectMultiStep{Relations: directRelations},
					Req:  req,
				})
			}

			// Each computed relation also gets a full IsAllowed check
			// (skipDirect=true since DirectMulti already covers direct hits).
			for _, rel := range computedRelations {
				steps = append(steps, check.PlannedStep{
					Step: IsAllowedStep{skipDirect: true},
					Req: check.CheckRequest{
						Tuple: &relationtuple.RelationTuple{
							Namespace: req.Tuple.Namespace,
							Object:    req.Tuple.Object,
							Relation:  rel,
							Subject:   req.Tuple.Subject,
						},
						RestDepth: req.RestDepth - 1,
					},
				})
			}
		}
	}

	// Process remaining (non-shortcut) children.
	for i, child := range children {
		if _, ok := handled[i]; ok {
			continue
		}
		step, err := rewriteChildToStep(ctx, child)
		if err != nil {
			return nil, err
		}

		childReq := req
		if kind, _ := ast.Dispatch(child); kind == ast.ChildKindSubjectSetRewrite {
			childReq = req.WithDepth(req.RestDepth - 1)
		}

		steps = append(steps, check.PlannedStep{Step: step, Req: childReq})
	}

	return steps, nil
}

// rewriteChildToStep converts a single rewrite AST child node into an
// executable Step. Returns an error for unrecognised node types.
func rewriteChildToStep(ctx context.Context, child ast.Child) (check.Step, error) {
	return ast.Walk(ctx, child, rewriteStepVisitor{})
}

type rewriteStepVisitor struct{}

func (rewriteStepVisitor) VisitComputedSubjectSet(_ context.Context, node *ast.ComputedSubjectSet) (check.Step, error) {
	return ComputedUsersetStep{Relation: node.Relation}, nil
}

func (rewriteStepVisitor) VisitTupleToSubjectSet(_ context.Context, node *ast.TupleToSubjectSet) (check.Step, error) {
	return TraverseStep{TTU: node}, nil
}

func (rewriteStepVisitor) VisitSubjectSetRewrite(_ context.Context, node *ast.SubjectSetRewrite, _ ast.Children, _ ast.WalkFunc[check.Step]) (check.Step, error) {
	return RewriteStep{Rewrite: node}, nil
}

func (rewriteStepVisitor) VisitInvertResult(_ context.Context, node *ast.InvertResult, _ ast.WalkFunc[check.Step]) (check.Step, error) {
	return InvertStep{Child: node.Child}, nil
}
