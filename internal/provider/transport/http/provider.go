package providerhttp

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/provider/application"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// ListProviders implements listProviders.
func (h *Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListProviders(r.Context(), rc, application.ListFilter{
		Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
		ProviderType: q.Get("providerType"), Status: q.Get("status"), NetworkTier: q.Get("networkTier"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ProviderPage{Items: make([]kapsorav1.Provider, 0, len(page.Items))}
	for _, p := range page.Items {
		out.Items = append(out.Items, providerView(p))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProvider implements createProvider.
func (h *Handler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateProviderRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewProvider{
		TenantOrganizationID: body.TenantOrganizationId.String(),
		ProviderType:         string(body.ProviderType),
	}
	if body.NetworkTier != nil {
		in.NetworkTier = *body.NetworkTier
	}
	if body.Notes != nil {
		in.Notes = *body.Notes
	}
	if body.ContractedFrom != nil {
		from := body.ContractedFrom.Time
		in.ContractedFrom = &from
	}
	if body.ContractedTo != nil {
		to := body.ContractedTo.Time
		in.ContractedTo = &to
	}

	provider, err := h.svc.CreateProvider(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(provider.RowVersion))
	w.Header().Set("Location", "/api/v1/providers/"+provider.ID.String())
	writeJSON(w, http.StatusCreated, providerView(provider))
}

// GetProvider implements getProvider.
func (h *Handler) GetProvider(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "providerId", application.ErrProviderNotFound)
	if !ok {
		return
	}
	provider, err := h.svc.GetProvider(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(provider.RowVersion))
	writeJSON(w, http.StatusOK, providerView(provider))
}

// PatchProvider implements patchProvider (merge-patch with If-Match).
func (h *Handler) PatchProvider(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "providerId", application.ErrProviderNotFound)
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

	patch := domain.ProviderPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "providerType":
			patch.ProviderType = decodeString(value, key, &fields)
		case "networkTier":
			if isJSONNull(value) {
				patch.ClearNetworkTier = true
			} else {
				patch.NetworkTier = decodeString(value, key, &fields)
			}
		case "contractedFrom":
			if isJSONNull(value) {
				patch.ClearContractedFrom = true
			} else {
				patch.ContractedFrom = decodeDate(value, key, &fields)
			}
		case "contractedTo":
			if isJSONNull(value) {
				patch.ClearContractedTo = true
			} else {
				patch.ContractedTo = decodeDate(value, key, &fields)
			}
		case "notes":
			if isJSONNull(value) {
				patch.ClearNotes = true
			} else {
				patch.Notes = decodeString(value, key, &fields)
			}
		case "status":
			// The status moves through activate, suspend and terminate, so that every
			// transition is recorded as an intent with a reason.
			immutable(key, &fields)
		case "tenantOrganizationId":
			// The organization relationship is what the profile is, not a property of it.
			immutable(key, &fields)
		default:
			unknownField(key, &fields)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	provider, err := h.svc.UpdateProvider(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(provider.RowVersion))
	writeJSON(w, http.StatusOK, providerView(provider))
}

// ActivateProvider implements activateProvider.
func (h *Handler) ActivateProvider(w http.ResponseWriter, r *http.Request) {
	h.move(w, r, application.CommandActivate, false)
}

// SuspendProvider implements suspendProvider.
func (h *Handler) SuspendProvider(w http.ResponseWriter, r *http.Request) {
	h.move(w, r, application.CommandSuspend, true)
}

// TerminateProvider implements terminateProvider.
func (h *Handler) TerminateProvider(w http.ResponseWriter, r *http.Request) {
	h.move(w, r, application.CommandTerminate, true)
}

// move runs one status command; the three routes differ only in the target and in whether
// a reason is required.
func (h *Handler) move(w http.ResponseWriter, r *http.Request, cmd application.StatusCommand, needsReason bool) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "providerId", application.ErrProviderNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var reasonCode, reasonText string
	if needsReason {
		var body kapsorav1.ReasonCommand
		if !decodeJSON(w, r, &body) {
			return
		}
		reasonCode = body.ReasonCode
		if body.ReasonText != nil {
			reasonText = *body.ReasonText
		}
	}

	provider, err := h.svc.MoveProvider(r.Context(), rc, id, cmd, expected, reasonCode, reasonText)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(provider.RowVersion))
	writeJSON(w, http.StatusOK, providerView(provider))
}

// SearchProviders implements searchProviders.
func (h *Handler) SearchProviders(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	var fields []domain.FieldError
	filter := application.SearchFilter{
		City: q.Get("city"), Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
		AsOf: queryDate(r, "asOf", &fields),
	}
	if raw := q.Get("serviceDefinitionId"); raw != "" {
		id, err := parseUUID(raw)
		if err != nil {
			fields = append(fields, domain.FieldError{
				Field: "serviceDefinitionId", Code: "FORMAT", Message: "geçerli bir kimlik olmalı",
			})
		} else {
			filter.ServiceDefinitionID = id
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	page, err := h.svc.Search(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ProviderSearchPage{
		AsOf:  openapi_types.Date{Time: page.AsOf},
		Items: make([]kapsorav1.ProviderSearchResult, 0, len(page.Items)),
	}
	for _, hit := range page.Items {
		out.Items = append(out.Items, searchView(hit))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

func providerView(p application.ProviderRecord) kapsorav1.Provider {
	out := kapsorav1.Provider{
		Id: p.ID, TenantOrganizationId: p.TenantOrganizationID, OrganizationName: p.OrganizationName,
		ProviderType: kapsorav1.ProviderType(p.ProviderType), Status: kapsorav1.ProviderStatus(p.Status),
		NetworkTier: p.NetworkTier, Notes: p.Notes, RowVersion: int(p.RowVersion),
	}
	if p.ContractedFrom != nil {
		out.ContractedFrom = &openapi_types.Date{Time: *p.ContractedFrom}
	}
	if p.ContractedTo != nil {
		out.ContractedTo = &openapi_types.Date{Time: *p.ContractedTo}
	}
	return out
}

func searchView(h application.SearchHit) kapsorav1.ProviderSearchResult {
	return kapsorav1.ProviderSearchResult{
		ProviderId: h.ProviderID, OrganizationName: h.OrganizationName,
		ProviderType: kapsorav1.ProviderType(h.ProviderType), NetworkTier: h.NetworkTier,
		LocationId: h.LocationID, LocationCode: h.LocationCode, LocationName: h.LocationName,
		City: h.City, District: h.District, Latitude: h.Latitude, Longitude: h.Longitude,
		MatchedVia: kapsorav1.ProviderSearchResultMatchedVia(h.MatchedVia),
	}
}
