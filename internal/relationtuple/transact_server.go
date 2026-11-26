// Copyright © 2023 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package relationtuple

import (
	"context"
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/trace"

	"github.com/ory/herodot"

	rts "github.com/ory/keto/gen/go/ory/keto/relation_tuples/v1alpha2"
	"github.com/ory/keto/gen/go/ory/keto/relation_tuples/v1alpha2/relationtuplesconnect"
	"github.com/ory/keto/internal/x/validate"
	"github.com/ory/keto/ketoapi"
	"github.com/ory/keto/x/events"
)

var _ relationtuplesconnect.WriteServiceHandler = (*WriteHandler)(nil)

type tupleDelta struct {
	action        ketoapi.PatchAction
	relationTuple *ketoapi.RelationTuple
}

func protoAction(action rts.RelationTupleDelta_Action) ketoapi.PatchAction {
	switch action {
	case rts.RelationTupleDelta_ACTION_INSERT:
		return ketoapi.ActionInsert
	case rts.RelationTupleDelta_ACTION_DELETE:
		return ketoapi.ActionDelete
	default:
		return ketoapi.PatchAction(action.String())
	}
}

func protoDeltas(deltas []*rts.RelationTupleDelta) ([]tupleDelta, error) {
	result := make([]tupleDelta, 0, len(deltas))
	for _, d := range deltas {
		if d.GetAction() == rts.RelationTupleDelta_ACTION_UNSPECIFIED {
			continue
		}

		var relationTuple *ketoapi.RelationTuple
		if d.GetRelationTuple() != nil {
			var err error
			relationTuple, err = (&ketoapi.RelationTuple{}).FromDataProvider(d.GetRelationTuple())
			if err != nil {
				return nil, err
			}
		}
		result = append(result, tupleDelta{
			action:        protoAction(d.GetAction()),
			relationTuple: relationTuple,
		})
	}
	return result, nil
}

func patchDeltas(deltas []*ketoapi.PatchDelta) []tupleDelta {
	result := make([]tupleDelta, len(deltas))
	for i, d := range deltas {
		if d == nil {
			continue
		}
		result[i] = tupleDelta{
			action:        d.Action,
			relationTuple: d.RelationTuple,
		}
	}
	return result
}

func validateAndFilterDeltas(deltas []tupleDelta) (insert, delete []*ketoapi.RelationTuple, err error) {
	for _, d := range deltas {
		if d.relationTuple == nil {
			return nil, nil, errors.WithStack(herodot.ErrBadRequest().WithError("relation_tuple is missing"))
		}
		if err := d.relationTuple.Validate(); err != nil {
			return nil, nil, err
		}

		switch d.action {
		case ketoapi.ActionInsert:
			insert = append(insert, d.relationTuple)
		case ketoapi.ActionDelete:
			delete = append(delete, d.relationTuple)
		default:
			return nil, nil, errors.WithStack(herodot.ErrBadRequest().WithError("unknown action " + string(d.action)))
		}
	}
	return insert, delete, nil
}

func (h *WriteHandler) transactRelationTuples(ctx context.Context, insert, delete []*ketoapi.RelationTuple) error {
	return h.d.RelationTupleManager().Transaction(ctx, func(ctx context.Context) error {
		tuples := make([]*ketoapi.RelationTuple, 0, len(insert)+len(delete))
		tuples = append(tuples, insert...)
		tuples = append(tuples, delete...)

		its, err := h.d.Mapper().FromTuple(ctx, tuples...)
		if err != nil {
			return err
		}
		return h.d.RelationTupleManager().TransactRelationTuples(ctx, its[:len(insert)], its[len(insert):])
	})
}

func (h *WriteHandler) TransactRelationTuples(ctx context.Context, req *connect.Request[rts.TransactRelationTuplesRequest]) (*connect.Response[rts.TransactRelationTuplesResponse], error) {
	deltas, err := protoDeltas(req.Msg.GetRelationTupleDeltas())
	if err != nil {
		return nil, err
	}

	insertTuples, deleteTuples, err := validateAndFilterDeltas(deltas)
	if err != nil {
		return nil, err
	}

	if err := h.transactRelationTuples(ctx, insertTuples, deleteTuples); err != nil {
		return nil, err
	}

	trace.SpanFromContext(ctx).AddEvent(events.NewRelationtuplesChanged(ctx))

	snaptokens := make([]string, len(insertTuples))
	for i := range insertTuples {
		snaptokens[i] = "not yet implemented"
	}
	return connect.NewResponse(&rts.TransactRelationTuplesResponse{
		Snaptokens: snaptokens,
	}), nil
}

func (h *WriteHandler) DeleteRelationTuples(ctx context.Context, req *connect.Request[rts.DeleteRelationTuplesRequest]) (*connect.Response[rts.DeleteRelationTuplesResponse], error) {
	var q ketoapi.RelationQuery

	switch {
	case req.Msg.RelationQuery != nil:
		q.FromDataProvider(&queryWrapper{req.Msg.RelationQuery})
		//lint:ignore SA1019 required for compatibility
	case req.Msg.Query != nil: //nolint:staticcheck
		//lint:ignore SA1019 backwards compatibility
		q.FromDataProvider(&deprecatedQueryWrapper{(*rts.ListRelationTuplesRequest_Query)(req.Msg.Query)}) //nolint:staticcheck
	default:
		return nil, errors.WithStack(herodot.ErrBadRequest().WithReason("invalid request"))
	}

	iq, err := h.d.ReadOnlyMapper().FromQuery(ctx, &q)
	if err != nil {
		return nil, err
	}
	if err := h.d.RelationTupleManager().DeleteAllRelationTuples(ctx, iq); err != nil {
		return nil, errors.WithStack(herodot.ErrInternalServerError().WithError(err.Error()))
	}

	trace.SpanFromContext(ctx).AddEvent(events.NewRelationtuplesDeleted(ctx))

	return connect.NewResponse(&rts.DeleteRelationTuplesResponse{}), nil
}

// Create Relationship Request Parameters
//
// swagger:parameters createRelationship
//
//lint:ignore U1000 required for OpenAPI
type createRelationship struct {
	// in: body
	Body createRelationshipBody
}

// Create Relationship Request Body
//
// swagger:model createRelationshipBody
type createRelationshipBody struct {
	ketoapi.RelationQuery
}

// swagger:route PUT /admin/relation-tuples relationship createRelationship
//
// # Create a Relationship
//
// Use this endpoint to create a relationship.
//
//	Consumes:
//	-  application/json
//
//	Produces:
//	- application/json
//
//	Schemes: http, https
//
//	Responses:
//	  201: relationship
//	  400: errorGeneric
//	  default: errorGeneric
//
//	Extensions:
//	  x-ory-ratelimit-bucket: keto-admin-low
func (h *WriteHandler) createRelation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var rt ketoapi.RelationTuple
	if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
		h.d.Writer().WriteError(w, r, errors.WithStack(herodot.ErrBadRequest().WithError(err.Error())))
		return
	}

	if err := rt.Validate(); err != nil {
		h.d.Writer().WriteError(w, r, err)
		return
	}

	h.d.Logger().WithFields(rt.ToLoggerFields()).Debug("creating relation tuple")

	err := h.d.RelationTupleManager().Transaction(ctx, func(ctx context.Context) error {
		it, err := h.d.Mapper().FromTuple(ctx, &rt)
		if err != nil {
			h.d.Logger().WithError(err).WithFields(rt.ToLoggerFields()).Errorf("could not map relation tuple to UUIDs")
			return err
		}
		if err := h.d.RelationTupleManager().WriteRelationTuples(ctx, it...); err != nil {
			h.d.Logger().WithError(err).WithFields(rt.ToLoggerFields()).Errorf("got an error while creating the relation tuple")
			return err
		}
		return nil
	})

	if err != nil {
		h.d.Writer().WriteError(w, r, err)
		return
	}

	trace.SpanFromContext(ctx).AddEvent(events.NewRelationtuplesCreated(ctx))

	h.d.Writer().WriteCreated(w, r,
		ReadRouteBase+"?"+rt.ToURLQuery().Encode(),
		&rt,
	)
}

// swagger:route DELETE /admin/relation-tuples relationship deleteRelationships
//
// # Delete Relationships
//
// Use this endpoint to delete relationships
//
//	Consumes:
//	-  application/x-www-form-urlencoded
//
//	Produces:
//	- application/json
//
//	Schemes: http, https
//
//	Responses:
//	  204: emptyResponse
//	  400: errorGeneric
//	  default: errorGeneric
//
//	Extensions:
//	  x-ory-ratelimit-bucket: keto-admin-low
func (h *WriteHandler) deleteRelations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if err := validate.All(r,
		validate.NoExtraQueryParams(ketoapi.RelationQueryKeys...),
		validate.QueryParamsContainsOneOf(ketoapi.NamespaceKey),
		validate.HasEmptyBody(),
	); err != nil {
		h.d.Writer().WriteError(w, r, err)
		return
	}

	q := r.URL.Query()
	query, err := (&ketoapi.RelationQuery{}).FromURLQuery(q)
	if err != nil {
		h.d.Writer().WriteError(w, r, herodot.ErrBadRequest().WithError(err.Error()))
		return
	}

	l := h.d.Logger()
	for k := range q {
		l = l.WithField(k, q.Get(k))
	}
	l.Debug("deleting relationships")

	iq, err := h.d.ReadOnlyMapper().FromQuery(ctx, query)
	if err != nil {
		h.d.Logger().WithError(err).Errorf("could not map fields to UUIDs")
		h.d.Writer().WriteError(w, r, err)
		return
	}
	if err := h.d.RelationTupleManager().DeleteAllRelationTuples(ctx, iq); err != nil {
		l.WithError(err).Errorf("got an error while deleting relationships")
		h.d.Writer().WriteError(w, r, herodot.ErrInternalServerError().WithError(err.Error()))
		return
	}

	trace.SpanFromContext(ctx).AddEvent(events.NewRelationtuplesDeleted(ctx))

	w.WriteHeader(http.StatusNoContent)
}

// swagger:route PATCH /admin/relation-tuples relationship patchRelationships
//
// # Patch Multiple Relationships
//
// Use this endpoint to patch one or more relationships.
//
//	Consumes:
//	- application/json
//
//	Produces:
//	- application/json
//
//	Schemes: http, https
//
//	Responses:
//	  204: emptyResponse
//	  400: errorGeneric
//	  404: errorGeneric
//	  default: errorGeneric
//
//	Extensions:
//	  x-ory-ratelimit-bucket: keto-admin-low
func (h *WriteHandler) patchRelationTuples(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var deltas []*ketoapi.PatchDelta
	if err := json.NewDecoder(r.Body).Decode(&deltas); err != nil {
		h.d.Writer().WriteError(w, r, herodot.ErrBadRequest().WithError(err.Error()))
		return
	}

	insertTuples, deleteTuples, err := validateAndFilterDeltas(patchDeltas(deltas))
	if err != nil {
		h.d.Writer().WriteError(w, r, err)
		return
	}

	if err := h.transactRelationTuples(ctx, insertTuples, deleteTuples); err != nil {
		h.d.Writer().WriteError(w, r, err)
		return
	}

	trace.SpanFromContext(ctx).AddEvent(events.NewRelationtuplesChanged(ctx))

	w.WriteHeader(http.StatusNoContent)
}
