package providerhttp

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/provider/application"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// ListPractitioners implements listPractitioners.
func (h *Handler) ListPractitioners(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	providerID, ok := h.pathUUID(w, r, "providerId", application.ErrProviderNotFound)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListPractitioners(r.Context(), rc, providerID, application.ListFilter{
		Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
		Status: q.Get("status"), BranchCode: q.Get("branchCode"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.PractitionerPage{Items: make([]kapsorav1.Practitioner, 0, len(page.Items))}
	for _, p := range page.Items {
		out.Items = append(out.Items, practitionerView(application.PractitionerView{PractitionerRecord: p}))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// CreatePractitioner implements createPractitioner. The submitted registration number is
// handed straight to the service, which normalizes, encrypts and indexes it; it is never
// written to a log or echoed back.
func (h *Handler) CreatePractitioner(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPractitionerManage)
	if !ok {
		return
	}
	providerID, ok := h.pathUUID(w, r, "providerId", application.ErrProviderNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreatePractitionerRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewPractitioner{
		FullName:              body.FullName,
		RegistrationAuthority: string(body.RegistrationAuthority),
		RegistrationNumber:    body.RegistrationNumber,
	}
	if body.Title != nil {
		in.Title = *body.Title
	}
	if body.BranchCode != nil {
		in.BranchCode = *body.BranchCode
	}
	if body.PersonId != nil {
		in.PersonID = body.PersonId.String()
	}
	if body.ValidFrom != nil {
		from := body.ValidFrom.Time
		in.ValidFrom = &from
	}
	if body.ValidTo != nil {
		to := body.ValidTo.Time
		in.ValidTo = &to
	}

	practitioner, err := h.svc.CreatePractitioner(r.Context(), rc, providerID, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(practitioner.RowVersion))
	w.Header().Set("Location", "/api/v1/practitioners/"+practitioner.ID.String())
	writeJSON(w, http.StatusCreated, practitionerView(practitioner))
}

// GetPractitioner implements getPractitioner.
func (h *Handler) GetPractitioner(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "practitionerId", application.ErrPractitionerNotFound)
	if !ok {
		return
	}
	practitioner, err := h.svc.GetPractitioner(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(practitioner.RowVersion))
	writeJSON(w, http.StatusOK, practitionerView(practitioner))
}

// PatchPractitioner implements patchPractitioner (merge-patch with If-Match).
func (h *Handler) PatchPractitioner(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPractitionerManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "practitionerId", application.ErrPractitionerNotFound)
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

	patch := domain.PractitionerPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "fullName":
			patch.FullName = decodeString(value, key, &fields)
		case "title":
			if isJSONNull(value) {
				patch.ClearTitle = true
			} else {
				patch.Title = decodeString(value, key, &fields)
			}
		case "branchCode":
			if isJSONNull(value) {
				patch.ClearBranchCode = true
			} else {
				patch.BranchCode = decodeString(value, key, &fields)
			}
		case "personId":
			if isJSONNull(value) {
				patch.ClearPersonID = true
			} else {
				patch.PersonID = decodeString(value, key, &fields)
			}
		case "validFrom":
			if isJSONNull(value) {
				patch.ClearValidFrom = true
			} else {
				patch.ValidFrom = decodeDate(value, key, &fields)
			}
		case "validTo":
			if isJSONNull(value) {
				patch.ClearValidTo = true
			} else {
				patch.ValidTo = decodeDate(value, key, &fields)
			}
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "registrationAuthority", "registrationNumber":
			// A wrong number is ended and re-registered: reports already signed under it
			// must keep resolving.
			immutable(key, &fields)
		default:
			unknownField(key, &fields)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	practitioner, err := h.svc.UpdatePractitioner(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(practitioner.RowVersion))
	writeJSON(w, http.StatusOK, practitionerView(practitioner))
}

// PutPractitionerLocations implements putPractitionerLocations; If-Match carries the ETag
// of the practitioner, which the replacement moves on.
func (h *Handler) PutPractitionerLocations(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPractitionerManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "practitionerId", application.ErrPractitionerNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplacePractitionerLocationsRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	items := make([]domain.AssignmentInput, 0, len(body.Items))
	for _, row := range body.Items {
		in := domain.AssignmentInput{
			LocationID: row.LocationId.String(), Role: string(row.Role),
			ValidFrom: domain.DateOnly(row.ValidFrom.Time),
		}
		if row.ValidTo != nil {
			to := domain.DateOnly(row.ValidTo.Time)
			in.ValidTo = &to
		}
		items = append(items, in)
	}

	result, err := h.svc.ReplaceAssignments(r.Context(), rc, id, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, kapsorav1.PractitionerLocationList{Items: assignmentViews(result.Items)})
}

// SearchPractitionerByRegistration implements searchPractitionerByRegistration: the
// practitioner permission plus a valid step-up window. The number arrives in the body, so
// it never reaches an access log, a proxy or a browser history.
func (h *Handler) SearchPractitionerByRegistration(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.RequireStepUp(r.Context(), PermissionPractitionerManage)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionPractitionerManage)
		return
	}
	var body kapsorav1.PractitionerRegistrationSearchRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	practitioner, err := h.svc.SearchByRegistration(r.Context(), rc, application.RegistrationSearch{
		Authority: string(body.RegistrationAuthority), Number: body.RegistrationNumber,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, practitionerView(practitioner))
}

func practitionerView(p application.PractitionerView) kapsorav1.Practitioner {
	out := kapsorav1.Practitioner{
		Id: p.ID, ProviderId: p.ProviderID, PersonId: optionalUUID(p.PersonID),
		FullName: p.FullName, Title: p.Title, BranchCode: p.BranchCode,
		RegistrationAuthority: kapsorav1.RegistrationAuthority(p.RegistrationAuthority),
		// The masked value is the only representation of the number that leaves the server.
		MaskedRegistrationNumber: p.MaskedRegistration,
		Status:                   kapsorav1.PractitionerStatus(p.Status),
		RowVersion:               int(p.RowVersion),
	}
	if p.ValidFrom != nil {
		out.ValidFrom = &openapi_types.Date{Time: *p.ValidFrom}
	}
	if p.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *p.ValidTo}
	}
	if p.Locations != nil {
		locations := assignmentViews(p.Locations)
		out.Locations = &locations
	}
	return out
}

func assignmentViews(items []application.AssignmentRecord) []kapsorav1.PractitionerLocation {
	out := make([]kapsorav1.PractitionerLocation, 0, len(items))
	for _, a := range items {
		item := kapsorav1.PractitionerLocation{
			Id: a.ID, PractitionerId: a.PractitionerID, LocationId: a.LocationID,
			LocationCode: a.LocationCode, LocationName: a.LocationName,
			Role: kapsorav1.PractitionerRole(a.Role), ValidFrom: openapi_types.Date{Time: a.ValidFrom},
		}
		if a.ValidTo != nil {
			item.ValidTo = &openapi_types.Date{Time: *a.ValidTo}
		}
		out = append(out, item)
	}
	return out
}
