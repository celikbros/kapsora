package reporthttp

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// GetProviderStatement answers GET /providers/{providerId}/statement.
func (h *Handler) GetProviderStatement(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	providerID, ok := h.pathUUID(w, r, "providerId", application.ErrProviderScope)
	if !ok {
		return
	}
	var fields []domain.FieldError
	from := queryDate(r, "periodFrom", &fields)
	to := queryDate(r, "periodTo", &fields)
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	currency := r.URL.Query().Get("currencyCode")

	statement, err := h.svc.ProviderStatement(r.Context(), rc, providerID, from, to, currency)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, statementView(statement))
}

func statementView(s application.Statement) kapsorav1.ProviderStatement {
	invoices := make([]kapsorav1.StatementInvoice, 0, len(s.Invoices))
	for _, i := range s.Invoices {
		invoices = append(invoices, kapsorav1.StatementInvoice{
			Id: i.ID, InvoiceNumber: i.InvoiceNumber, InvoiceDate: dateOf(i.InvoiceDate),
			Status: kapsorav1.InvoiceStatus(i.Status), CurrencyCode: i.CurrencyCode,
			PayableAmount: i.PayableAmount, TaxAmount: i.TaxAmount,
			BatchId: i.BatchID, BatchReference: i.BatchReference, BatchStatus: i.BatchStatus,
			BatchDecision: i.BatchDecision, ApprovedAmount: i.ApprovedAmount,
			SettlementId: i.SettlementID, SettlementReference: i.SettlementReference,
			DueDate: optDateOf(i.DueDate), SettlementPaidAmount: i.SettlementPaidAmount,
		})
	}
	settlements := make([]kapsorav1.StatementSettlement, 0, len(s.Settlements))
	for _, st := range s.Settlements {
		settlements = append(settlements, kapsorav1.StatementSettlement{
			Id: st.ID, Reference: st.Reference, BatchId: st.BatchID,
			BatchReference: st.BatchReference, DueDate: dateOf(st.DueDate),
			Status: kapsorav1.SettlementStatus(st.Status), CurrencyCode: st.CurrencyCode,
			ApprovedAmount: st.ApprovedAmount, WithheldAmount: st.WithheldAmount,
			PayableAmount: st.PayableAmount, PaidAmount: st.PaidAmount,
			OpenAmount: st.OpenAmount, PaymentCount: int(st.PaymentCount),
			LastPaidAt: st.LastPaidAt,
		})
	}
	return kapsorav1.ProviderStatement{
		ProviderOrganizationId: s.ProviderOrganizationID,
		ProviderName:           s.Totals.ProviderName,
		PeriodFrom:             dateOf(s.PeriodFrom), PeriodTo: dateOf(s.PeriodTo),
		CurrencyCode: s.CurrencyCode,
		Totals: kapsorav1.StatementTotals{
			InvoicedTotal: s.Totals.InvoicedTotal, ApprovedTotal: s.Totals.ApprovedTotal,
			CutTotal: s.Totals.CutTotal, ReturnedTotal: s.Totals.ReturnedTotal,
			RejectedTotal: s.Totals.RejectedTotal, SettledTotal: s.Totals.SettledTotal,
			PaidTotal: s.Totals.PaidTotal, OpenBalance: s.Totals.OpenBalance,
			InvoiceCount: int(s.Totals.InvoiceCount), SettlementCount: int(s.Totals.SettlementCount),
		},
		Invoices: invoices, Settlements: settlements,
	}
}

func dateOf(t time.Time) openapi_types.Date { return openapi_types.Date{Time: t} }

func optDateOf(t *time.Time) *openapi_types.Date {
	if t == nil {
		return nil
	}
	return &openapi_types.Date{Time: *t}
}
