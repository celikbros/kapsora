package authorizationhttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/authorization/application"
	"github.com/celikbros/kapsora/internal/authorization/domain"
)

// ListFulfilments implements listFulfilments.
func (h *Handler) ListFulfilments(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.FulfilmentFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		Status:            r.URL.Query().Get("status"),
		AuthorizationID:   queryUUID(r, "authorizationId", &fields),
		ProviderProfileID: queryUUID(r, "providerProfileId", &fields),
		PerformedFrom:     queryTimestamp(r, "performedFrom", &fields),
		PerformedTo:       queryTimestamp(r, "performedTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListFulfilments(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.FulfilmentPage{Items: make([]kapsorav1.Fulfilment, 0, len(page.Items))}
	for _, view := range page.Items {
		out.Items = append(out.Items, fulfilmentView(view))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateFulfilment implements createFulfilment.
func (h *Handler) CreateFulfilment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRecord)
	if !ok {
		return
	}
	var body kapsorav1.CreateFulfilment
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.RecordFulfilment(r.Context(), rc, application.NewFulfilmentInput{
		AuthorizationID: body.AuthorizationId, ProviderProfileID: body.ProviderProfileId,
		LocationID: body.LocationId, PractitionerID: body.PractitionerId,
		PerformedAt: body.PerformedAt, Items: fulfilmentItems(body.Items),
	})
	h.answerFulfilment(w, r, view, err, http.StatusCreated)
}

// GetFulfilment implements getFulfilment.
func (h *Handler) GetFulfilment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "fulfilmentId", application.ErrFulfilmentNotFound)
	if !ok {
		return
	}
	view, err := h.svc.GetFulfilment(r.Context(), rc, id)
	h.answerFulfilment(w, r, view, err, http.StatusOK)
}

// CompleteFulfilment implements completeFulfilment. It carries no body: what is consumed
// is what was recorded, and a body that could restate the quantities would be a second
// place for them to disagree.
func (h *Handler) CompleteFulfilment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRecord)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "fulfilmentId", application.ErrFulfilmentNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	view, err := h.svc.CompleteFulfilment(r.Context(), rc, id, expected)
	h.answerFulfilment(w, r, view, err, http.StatusOK)
}

// CancelFulfilment implements cancelFulfilment.
func (h *Handler) CancelFulfilment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRecord)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "fulfilmentId", application.ErrFulfilmentNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReasonCommand
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CancelFulfilment(r.Context(), rc, id, application.ReasonInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, ExpectedVersion: expected,
	})
	h.answerFulfilment(w, r, view, err, http.StatusOK)
}

func (h *Handler) answerFulfilment(w http.ResponseWriter, r *http.Request,
	view application.FulfilmentView, err error, status int,
) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Fulfilment.RowVersion))
	writeJSON(w, status, fulfilmentView(view))
}

// fulfilmentItems carries the contract's lines into the domain unchanged: the decimals
// stay the exact strings the caller sent, and the domain decides whether they parse.
func fulfilmentItems(in []kapsorav1.FulfilmentItemInput) []domain.FulfilmentItemInput {
	out := make([]domain.FulfilmentItemInput, 0, len(in))
	for _, item := range in {
		row := domain.FulfilmentItemInput{
			AuthorizationItemID: item.AuthorizationItemId.String(),
			ActualQuantity:      item.ActualQuantity,
		}
		if item.ActualAmount != nil {
			row.ActualAmount = *item.ActualAmount
		}
		out = append(out, row)
	}
	return out
}

func fulfilmentView(view application.FulfilmentView) kapsorav1.Fulfilment {
	f := view.Fulfilment
	out := kapsorav1.Fulfilment{
		Id: f.ID, Reference: f.Reference, AuthorizationId: f.AuthorizationID,
		ProviderProfileId: f.ProviderProfileID, LocationId: f.LocationID,
		PractitionerId: f.PractitionerID, PerformedAt: f.PerformedAt.UTC(),
		Status:      kapsorav1.FulfilmentStatus(f.Status),
		CompletedAt: utcPtr(f.CompletedAt), CancelledAt: utcPtr(f.CancelledAt),
		CancelReasonCode: f.CancelReasonCode, RecordedBy: f.RecordedBy,
		Items:      make([]kapsorav1.FulfilmentItem, 0, len(view.Items)),
		RowVersion: int(f.RowVersion), CreatedAt: f.CreatedAt.UTC(),
	}
	for _, item := range view.Items {
		out.Items = append(out.Items, kapsorav1.FulfilmentItem{
			Id: item.ID, AuthorizationItemId: item.AuthorizationItemID,
			ServiceDefinitionId: item.ServiceDefinitionID,
			ActualQuantity:      item.ActualQuantity, ActualAmount: item.ActualAmount,
		})
	}
	return out
}
