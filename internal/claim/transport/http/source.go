package claimhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/claim/application"
)

func sourceSummary(s application.CaseSourceSummary) kapsorav1.ClaimCaseSource {
	return kapsorav1.ClaimCaseSource{CaseId: s.ID, OpenedAt: s.OpenedAt, ServiceDate: dateOf(s.ServiceDate), RowVersion: s.RowVersion, RequestReference: s.RequestReference, PersonDisplayName: s.PersonDisplayName}
}

// ListCaseSources exposes only the financial identity of scoped candidates.
func (h *Handler) ListCaseSources(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	rows, next, err := h.svc.ListCaseSources(r.Context(), rc, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ClaimCaseSourcePage{Items: make([]kapsorav1.ClaimCaseSource, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, sourceSummary(row))
	}
	if next != "" {
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// GetCaseSource uses an allowlist: internal diagnosis/report/authorization IDs cannot leak.
func (h *Handler) GetCaseSource(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "caseId"))
	if err != nil {
		h.writeError(w, r, application.ErrSourceNotFound)
		return
	}
	source, err := h.svc.GetCaseSource(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ClaimCaseSourceDetail{Source: sourceSummary(source.CaseSourceSummary), Lines: make([]kapsorav1.ClaimCaseSourceLine, 0, len(source.Lines))}
	for _, line := range source.Lines {
		out.Lines = append(out.Lines, kapsorav1.ClaimCaseSourceLine{ServiceDefinitionId: line.ServiceID, ServiceCode: line.Code, ServiceName: line.Name, UnitType: line.UnitType, Quantity: line.Quantity})
	}
	w.Header().Set("ETag", etag(source.RowVersion))
	writeJSON(w, http.StatusOK, out)
}

// CreateFromCase accepts charges, never clinical or other-person reference IDs.
func (h *Handler) CreateFromCase(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "caseId"))
	if err != nil {
		h.writeError(w, r, application.ErrSourceNotFound)
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CreateClaimFromCase
	if !decodeJSON(w, r, &body) {
		return
	}
	charges := make([]application.CaseCharge, 0, len(body.Lines))
	for _, line := range body.Lines {
		charges = append(charges, application.CaseCharge{ServiceID: line.ServiceDefinitionId, Quantity: line.Quantity, LineAmount: line.LineAmount})
	}
	out, err := h.svc.CreateFromCase(r.Context(), rc, id, expected, charges)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Claim.RowVersion))
	writeJSON(w, http.StatusCreated, claimView(out))
}
