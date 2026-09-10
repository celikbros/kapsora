package reporthttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// ListReconciliationRuns answers GET /reconciliation-runs.
func (h *Handler) ListReconciliationRuns(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.RunFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		Scope: r.URL.Query().Get("scope"), Status: r.URL.Query().Get("status"),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		PeriodFrom:             queryDate(r, "periodFrom", &fields),
		PeriodTo:               queryDate(r, "periodTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	page, err := h.svc.ListReconciliationRuns(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]kapsorav1.ReconciliationRun, 0, len(page.Items))
	for _, run := range page.Items {
		items = append(items, runView(run))
	}
	writeJSON(w, http.StatusOK, kapsorav1.ReconciliationRunPage{
		Items: items, NextCursor: nextCursor(page.NextCursor),
	})
}

// GetReconciliationRun answers GET /reconciliation-runs/{runId}.
func (h *Handler) GetReconciliationRun(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "runId", application.ErrRunNotFound)
	if !ok {
		return
	}
	run, err := h.svc.GetReconciliationRun(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runView(run))
}

// runView renders one run. `erpTotal` is the empty string inside the service until M9 supplies a
// figure, and it becomes a JSON null here: absent and zero are different answers about money.
func runView(run application.ReconciliationRun) kapsorav1.ReconciliationRun {
	differences := make([]kapsorav1.ReconciliationDifference, 0, len(run.Differences))
	for _, d := range run.Differences {
		differences = append(differences, kapsorav1.ReconciliationDifference{
			Reference: d.SettlementReference, DueDate: dateOf(d.DueDate), Status: d.Status,
			Expected: d.ExpectedAmount, Actual: d.ActualAmount, Difference: d.DifferenceAmount,
			Kind: kapsorav1.ReconciliationDifferenceKind(d.Kind),
		})
	}
	var erp *string
	if run.ERPTotal != "" {
		value := run.ERPTotal
		erp = &value
	}
	return kapsorav1.ReconciliationRun{
		Id: run.ID, Scope: kapsorav1.ReconciliationScope(run.Scope),
		ProviderOrganizationId: run.ProviderOrganizationID, ProviderName: run.ProviderName,
		PeriodFrom: dateOf(run.PeriodFrom), PeriodTo: dateOf(run.PeriodTo),
		RunNo: run.RunNo, CurrencyCode: run.CurrencyCode,
		InvoicedTotal: run.InvoicedTotal, ApprovedTotal: run.ApprovedTotal,
		CutTotal: run.CutTotal, ReturnedTotal: run.ReturnedTotal,
		RejectedTotal: run.RejectedTotal, SettledTotal: run.SettledTotal,
		PaidTotal: run.PaidTotal, OpenTotal: run.OpenTotal, ErpTotal: erp,
		Difference: run.Difference, DifferenceCount: run.DifferenceCount,
		Differences: differences, Status: kapsorav1.ReconciliationRunStatus(run.Status),
		FailureCode: run.FailureCode, RanAt: run.RanAt, CreatedAt: run.CreatedAt,
	}
}

// nextCursor renders the paging cursor. An empty one is a JSON null rather than an empty string,
// because "there is no next page" and "the next page starts at the beginning" are different
// answers and every list in this contract gives the first one as null.
func nextCursor(cursor string) *string {
	if cursor == "" {
		return nil
	}
	value := cursor
	return &value
}
