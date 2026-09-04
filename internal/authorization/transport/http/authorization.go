package authorizationhttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/authorization/application"
	"github.com/celikbros/kapsora/internal/authorization/domain"
)

// ListAuthorizations implements listAuthorizations.
func (h *Handler) ListAuthorizations(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.AuthorizationFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		Status:                 r.URL.Query().Get("status"),
		RequestID:              queryUUID(r, "requestId", &fields),
		PersonID:               queryUUID(r, "personId", &fields),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		ValidFrom:              queryTimestamp(r, "validFrom", &fields),
		ValidTo:                queryTimestamp(r, "validTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.List(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.AuthorizationPage{Items: make([]kapsorav1.Authorization, 0, len(page.Items))}
	for _, view := range page.Items {
		out.Items = append(out.Items, authorizationView(view))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateAuthorization implements createAuthorization.
func (h *Handler) CreateAuthorization(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateAuthorization
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewAuthorizationInput{
		RequestID: body.RequestId, ValidFrom: utcPtr(body.ValidFrom), ValidTo: body.ValidTo,
		PriceQuoteID: body.PriceQuoteId, IdempotencyKey: idempotencyKey(r),
	}
	if body.Items != nil {
		in.MemberAmounts = make(map[int]string, len(*body.Items))
		for _, item := range *body.Items {
			in.MemberAmounts[item.LineNo] = item.MemberAmount
		}
	}
	view, err := h.svc.Create(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Authorization.RowVersion))
	writeJSON(w, http.StatusCreated, authorizationView(view))
}

// GetAuthorization implements getAuthorization.
func (h *Handler) GetAuthorization(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, readPermissions)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "authorizationId", application.ErrAuthorizationNotFound)
	if !ok {
		return
	}
	view, err := h.svc.Get(r.Context(), rc, id)
	h.answerAuthorization(w, r, view, err, http.StatusOK)
}

// ExtendAuthorization implements extendAuthorization.
func (h *Handler) ExtendAuthorization(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "authorizationId", application.ErrAuthorizationNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ExtendAuthorization
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.ExtendInput{
		ValidTo: body.ValidTo, ReasonText: body.ReasonText, ExpectedVersion: expected,
	}
	if body.ReasonCode != nil {
		in.ReasonCode = *body.ReasonCode
	}
	view, err := h.svc.Extend(r.Context(), rc, id, in)
	h.answerAuthorization(w, r, view, err, http.StatusOK)
}

// CancelAuthorization implements cancelAuthorization.
func (h *Handler) CancelAuthorization(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "authorizationId", application.ErrAuthorizationNotFound)
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
	view, err := h.svc.Cancel(r.Context(), rc, id, application.ReasonInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, ExpectedVersion: expected,
	})
	h.answerAuthorization(w, r, view, err, http.StatusOK)
}

// answerAuthorization writes the authorization with its new ETag, or the problem the
// command produced.
func (h *Handler) answerAuthorization(w http.ResponseWriter, r *http.Request,
	view application.AuthorizationView, err error, status int,
) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Authorization.RowVersion))
	writeJSON(w, status, authorizationView(view))
}

func authorizationView(view application.AuthorizationView) kapsorav1.Authorization {
	a := view.Authorization
	out := kapsorav1.Authorization{
		Id: a.ID, RequestId: a.RequestID, Reference: a.Reference,
		ValidFrom: a.ValidFrom.UTC(), ValidTo: a.ValidTo.UTC(),
		Status:        kapsorav1.AuthorizationStatus(a.Status),
		ReservedTotal: a.ReservedTotal, ConsumedTotal: a.ConsumedTotal,
		CurrencyCode: a.CurrencyCode, PriceQuoteId: a.PriceQuoteID,
		ApprovedBy: a.ApprovedBy, ApprovedAt: a.ApprovedAt.UTC(),
		CancelReasonCode: a.CancelReasonCode,
		Items:            make([]kapsorav1.AuthorizationItem, 0, len(view.Items)),
		Vouchers:         make([]kapsorav1.Voucher, 0, len(view.Vouchers)),
		RowVersion:       int(a.RowVersion),
		CreatedAt:        a.CreatedAt.UTC(),
	}
	for _, item := range view.Items {
		out.Items = append(out.Items, kapsorav1.AuthorizationItem{
			Id: item.ID, RequestItemId: item.RequestItemID,
			ServiceDefinitionId: item.ServiceDefinitionID,
			ApprovedQuantity:    item.ApprovedQuantity, ApprovedAmount: item.ApprovedAmount,
			MemberAmount:             item.MemberAmount,
			EntitlementReservationId: item.ReservationID,
			ConsumedQuantity:         item.ConsumedQuantity,
		})
	}
	for _, v := range view.Vouchers {
		out.Vouchers = append(out.Vouchers, voucherView(v))
	}
	return out
}
