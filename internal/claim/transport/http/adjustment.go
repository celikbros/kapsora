package claimhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
)

// ListClaimAdjustments serves GET /claims/{claimId}/adjustments.
//
// It is `claim.read` rather than a review grant: the ledger is what a provider disputes, and
// a provider who could not see why their claim is worth less than they billed could not
// dispute anything.
func (h *Handler) ListClaimAdjustments(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ListAdjustments(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.ClaimAdjustmentList{
		Items: make([]kapsorav1.ClaimAdjustment, 0, len(rows)),
	}
	for _, row := range rows {
		body.Items = append(body.Items, adjustmentView(row))
	}
	writeJSON(w, http.StatusOK, body)
}

// CreateClaimAdjustment serves POST /claims/{claimId}/adjustments.
//
// The grant is `claim.financial.review`. An adjustment is money, it is the payer's side of the
// claim, and the medical reviewer's grant is about a clinical judgement rather than about a
// figure — a package that let either reviewer write one would be a package in which a doctor
// could cut a bill.
func (h *Handler) CreateClaimAdjustment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionFinancialReview)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CreateClaimAdjustment
	if !decodeJSON(w, r, &body) {
		return
	}

	in := application.AdjustmentInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, LineNo: body.LineNo,
	}
	if body.ReversesAdjustmentId != nil {
		reversed := *body.ReversesAdjustmentId
		in.ReversesAdjustmentID = &reversed
	}
	if body.AdjustmentType != nil {
		in.AdjustmentType = string(*body.AdjustmentType)
	}
	if body.Amount != nil {
		in.Amount = *body.Amount
	}
	if body.PayerAmount != nil {
		in.PayerAmount = *body.PayerAmount
	}
	if body.MemberAmount != nil {
		in.MemberAmount = *body.MemberAmount
	}
	if body.CurrencyCode != nil {
		in.CurrencyCode = *body.CurrencyCode
	}
	// A reversal carries no figures of its own: the amount and the split come off the row it
	// reverses. Sending one is refused rather than ignored, because ignoring it would let a
	// caller believe they had reversed a cut for a different amount.
	if in.ReversesAdjustmentID != nil &&
		(body.Amount != nil || body.PayerAmount != nil || body.MemberAmount != nil ||
			body.AdjustmentType != nil) {
		writeValidation(w, r, []domain.FieldError{{
			Field: "amount", Code: "CONFLICT",
			Message: "iptal kaydı tutar taşımaz; tutar iptal edilen satırdan okunur",
		}})
		return
	}

	view, err := h.svc.CreateAdjustment(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, kapsorav1.ClaimAdjustmentResult{
		Adjustment: adjustmentView(view.Adjustment),
		Readiness:  readinessView(view.Readiness),
	})
}

// GetProviderEarnings serves GET /providers/{providerId}/earnings.
func (h *Handler) GetProviderEarnings(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	providerID, err := uuid.Parse(chi.URLParam(r, "providerId"))
	if err != nil {
		h.writeError(w, r, application.ErrProviderUnknown)
		return
	}
	var fields []domain.FieldError
	filter := application.EarningsFilter{
		From:         queryDate(r, "from", &fields),
		To:           queryDate(r, "to", &fields),
		CurrencyCode: r.URL.Query().Get("currency"),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	earnings, err := h.svc.ProviderEarnings(r.Context(), rc, providerID, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, earningsView(earnings))
}

// adjustmentView maps one ledger row onto the wire.
func adjustmentView(row application.AdjustmentRecord) kapsorav1.ClaimAdjustment {
	out := kapsorav1.ClaimAdjustment{
		Id: row.ID, ClaimId: row.ClaimID, VersionNo: row.VersionNo,
		ClaimLineId:    row.ClaimLineID,
		AdjustmentType: kapsorav1.ClaimAdjustmentType(row.AdjustmentType),
		Amount:         row.Amount, PayerAmount: row.PayerAmount,
		MemberAmount: row.MemberAmount, CurrencyCode: row.CurrencyCode,
		ReasonCode: row.ReasonCode, ReasonText: row.ReasonText,
		SourceType:           kapsorav1.ClaimAdjustmentSource(row.SourceType),
		SourceId:             row.SourceID,
		ReversesAdjustmentId: row.ReversesAdjustmentID,
		CreatedBy:            row.CreatedBy, CreatedAt: row.CreatedAt,
	}
	return out
}

// readinessView maps the totals onto the wire. It is shared with the readiness endpoint, so
// the two answers cannot drift into two shapes.
func readinessView(readiness application.InvoiceReadiness) kapsorav1.ClaimInvoiceReadiness {
	body := kapsorav1.ClaimInvoiceReadiness{
		ClaimId: readiness.ClaimID, Status: kapsorav1.ClaimStatus(readiness.Status),
		CurrencyCode: readiness.CurrencyCode, LineTotal: readiness.LineTotal,
		AdjustmentTotal: readiness.AdjustmentTotal, ApprovedTotal: readiness.ApprovedTotal,
		PayerTotal: readiness.PayerTotal, MemberTotal: readiness.MemberTotal,
		LineCount: readiness.LineCount, AdjustmentCount: readiness.AdjustmentCount,
		DecidedLineCount: readiness.DecidedLineCount, Ready: readiness.Ready,
		Blockers: make([]kapsorav1.ClaimInvoiceBlocker, 0, len(readiness.Blockers)),
	}
	for _, blocker := range readiness.Blockers {
		body.Blockers = append(body.Blockers, kapsorav1.ClaimInvoiceBlocker(blocker))
	}
	return body
}

// earningsView maps the provider's earnings onto the wire.
func earningsView(in application.ProviderEarnings) kapsorav1.ProviderEarnings {
	out := kapsorav1.ProviderEarnings{
		ProviderOrganizationId: in.ProviderOrganizationID, ProviderName: in.ProviderName,
		Currencies: make([]kapsorav1.ProviderEarningsCurrency, 0, len(in.Currencies)),
	}
	if in.From != nil {
		from := openapi_types.Date{Time: *in.From}
		out.From = &from
	}
	if in.To != nil {
		to := openapi_types.Date{Time: *in.To}
		out.To = &to
	}
	for _, currency := range in.Currencies {
		row := kapsorav1.ProviderEarningsCurrency{
			CurrencyCode: currency.CurrencyCode, ClaimCount: currency.ClaimCount,
			ApprovedTotal: currency.ApprovedTotal, PayerTotal: currency.PayerTotal,
			MemberTotal: currency.MemberTotal, AdjustmentTotal: currency.AdjustmentTotal,
			InvoiceableTotal:    currency.InvoiceableTotal,
			InvoiceableClaimIds: currency.InvoiceableClaimIDs,
			ByStatus: make([]kapsorav1.ProviderEarningsStatusTotal, 0,
				len(currency.ByStatus)),
		}
		if row.InvoiceableClaimIds == nil {
			row.InvoiceableClaimIds = []uuid.UUID{}
		}
		for _, status := range currency.ByStatus {
			row.ByStatus = append(row.ByStatus, kapsorav1.ProviderEarningsStatusTotal{
				Status:        kapsorav1.ClaimStatus(status.Status),
				ClaimCount:    status.ClaimCount,
				ApprovedTotal: status.ApprovedTotal,
			})
		}
		out.Currencies = append(out.Currencies, row)
	}
	return out
}
