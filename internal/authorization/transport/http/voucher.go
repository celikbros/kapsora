package authorizationhttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/authorization/application"
)

// IssueVoucher implements issueVoucher. The response body is the only place the plaintext
// token ever appears; nothing reads it back, because nothing stores it.
func (h *Handler) IssueVoucher(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "authorizationId", application.ErrAuthorizationNotFound)
	if !ok {
		return
	}
	var body kapsorav1.IssueVoucher
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	issued, err := h.svc.IssueVoucher(r.Context(), rc, application.IssueVoucherInput{
		AuthorizationID: id, ValidFrom: utcPtr(body.ValidFrom), ValidTo: utcPtr(body.ValidTo),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// No ETag: a voucher is not something a caller edits, and the one thing this response
	// carries that no other response ever will is the token itself.
	writeJSON(w, http.StatusCreated, kapsorav1.IssuedVoucher{
		Voucher: voucherView(issued.Voucher), Token: issued.Token,
	})
}

// RedeemVoucher implements redeemVoucher. The token arrives in the body, is hashed
// immediately and is never written anywhere: not to a column, not to the request log, not
// to an audit row.
func (h *Handler) RedeemVoucher(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRedeem)
	if !ok {
		return
	}
	var body kapsorav1.RedeemVoucher
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.RedeemVoucher(r.Context(), rc, application.RedeemVoucherInput{
		Token: body.Token, ProviderProfileID: body.ProviderProfileId,
		LocationID: body.LocationId, PractitionerID: body.PractitionerId,
		PerformedAt: body.PerformedAt, Items: fulfilmentItems(body.Items),
	})
	h.answerFulfilment(w, r, view, err, http.StatusCreated)
}

func voucherView(v application.VoucherRecord) kapsorav1.Voucher {
	return kapsorav1.Voucher{
		Id: v.ID, AuthorizationId: v.AuthorizationID, MaskedToken: v.MaskedToken,
		ValidFrom: v.ValidFrom.UTC(), ValidTo: v.ValidTo.UTC(),
		Status:     kapsorav1.VoucherStatus(v.Status),
		RedeemedAt: utcPtr(v.RedeemedAt), RedeemedByActorId: v.RedeemedByActorID,
		IssuedAt: v.IssuedAt.UTC(),
	}
}
