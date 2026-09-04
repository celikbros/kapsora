package contracthttp

import (
	"encoding/json"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
)

// ListContracts implements listContracts.
func (h *Handler) ListContracts(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListContracts(r.Context(), rc, application.ListFilter{
		Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
		ProviderProfileID: q.Get("providerProfileId"), PayerOrganizationID: q.Get("payerOrganizationId"),
		DomainCode: q.Get("domainCode"), Status: q.Get("status"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ContractPage{Items: make([]kapsorav1.Contract, 0, len(page.Items))}
	for _, c := range page.Items {
		out.Items = append(out.Items, contractView(c))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateContract implements createContract.
func (h *Handler) CreateContract(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateContractRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewContract{
		Code: body.Code, Name: body.Name,
		PayerOrganizationID: body.PayerOrganizationId.String(),
		ProviderProfileID:   body.ProviderProfileId.String(),
		DomainCode:          string(body.DomainCode),
	}
	if body.SponsorOrganizationId != nil {
		in.SponsorOrganizationID = body.SponsorOrganizationId.String()
	}

	contract, err := h.svc.CreateContract(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(contract.RowVersion))
	w.Header().Set("Location", "/api/v1/contracts/"+contract.ID.String())
	writeJSON(w, http.StatusCreated, contractView(contract))
}

// GetContract implements getContract.
func (h *Handler) GetContract(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "contractId", application.ErrContractNotFound)
	if !ok {
		return
	}
	contract, err := h.svc.GetContract(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(contract.RowVersion))
	writeJSON(w, http.StatusOK, contractView(contract))
}

// PatchContract implements patchContract (merge-patch with If-Match).
func (h *Handler) PatchContract(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "contractId", application.ErrContractNotFound)
	if !ok {
		return
	}
	if !requireMergePatch(w, r) {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}

	patch := domain.ContractPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "sponsorOrganizationId":
			if isJSONNull(value) {
				patch.ClearSponsor = true
			} else {
				patch.SponsorOrganizationID = decodeString(value, key, &fields)
			}
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "code", "payerOrganizationId", "providerProfileId", "domainCode":
			// These are what the contract is, and every published version was agreed
			// under them; changing one would rewrite history rather than the record.
			immutable(key, &fields)
		default:
			unknownField(key, &fields)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	contract, err := h.svc.UpdateContract(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(contract.RowVersion))
	writeJSON(w, http.StatusOK, contractView(contract))
}

func contractView(c application.ContractRecord) kapsorav1.Contract {
	out := kapsorav1.Contract{
		Id: c.ID, Code: c.Code, Name: c.Name,
		PayerOrganizationId: c.PayerOrganizationID, PayerName: optionalString(c.PayerName),
		ProviderProfileId: c.ProviderProfileID, ProviderName: optionalString(c.ProviderName),
		SponsorName: c.SponsorName,
		DomainCode:  kapsorav1.ServiceDomain(c.DomainCode),
		Status:      kapsorav1.ContractStatus(c.Status),
		RowVersion:  int(c.RowVersion),
	}
	if c.SponsorOrganizationID != nil {
		sponsor := *c.SponsorOrganizationID
		out.SponsorOrganizationId = &sponsor
	}
	return out
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
