package providerhttp

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/provider/application"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// ListProviderLocations implements listProviderLocations.
func (h *Handler) ListProviderLocations(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	providerID, ok := h.pathUUID(w, r, "providerId", application.ErrProviderNotFound)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListLocations(r.Context(), rc, providerID, application.ListFilter{
		Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
		Status: q.Get("status"), City: q.Get("city"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ProviderLocationPage{Items: make([]kapsorav1.ProviderLocation, 0, len(page.Items))}
	for _, l := range page.Items {
		out.Items = append(out.Items, locationView(l))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProviderLocation implements createProviderLocation.
func (h *Handler) CreateProviderLocation(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	providerID, ok := h.pathUUID(w, r, "providerId", application.ErrProviderNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreateProviderLocationRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewLocation{
		Code: body.Code, Name: body.Name, Latitude: body.Latitude, Longitude: body.Longitude,
	}
	assignString(&in.AddressLine, body.AddressLine)
	assignString(&in.District, body.District)
	assignString(&in.City, body.City)
	assignString(&in.CountryCode, body.CountryCode)
	assignString(&in.PostalCode, body.PostalCode)
	assignString(&in.Timezone, body.Timezone)
	assignString(&in.Phone, body.Phone)

	location, err := h.svc.CreateLocation(r.Context(), rc, providerID, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(location.RowVersion))
	w.Header().Set("Location", "/api/v1/provider-locations/"+location.ID.String())
	writeJSON(w, http.StatusCreated, locationView(location))
}

// GetProviderLocation implements getProviderLocation.
func (h *Handler) GetProviderLocation(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "locationId", application.ErrLocationNotFound)
	if !ok {
		return
	}
	location, err := h.svc.GetLocation(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(location.RowVersion))
	writeJSON(w, http.StatusOK, locationView(location))
}

// PatchProviderLocation implements patchProviderLocation (merge-patch with If-Match).
func (h *Handler) PatchProviderLocation(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "locationId", application.ErrLocationNotFound)
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

	patch := domain.LocationPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "addressLine":
			if isJSONNull(value) {
				patch.ClearAddressLine = true
			} else {
				patch.AddressLine = decodeString(value, key, &fields)
			}
		case "district":
			if isJSONNull(value) {
				patch.ClearDistrict = true
			} else {
				patch.District = decodeString(value, key, &fields)
			}
		case "city":
			if isJSONNull(value) {
				patch.ClearCity = true
			} else {
				patch.City = decodeString(value, key, &fields)
			}
		case "countryCode":
			patch.CountryCode = decodeString(value, key, &fields)
		case "postalCode":
			if isJSONNull(value) {
				patch.ClearPostalCode = true
			} else {
				patch.PostalCode = decodeString(value, key, &fields)
			}
		case "latitude":
			if isJSONNull(value) {
				patch.ClearLatitude = true
			} else {
				patch.Latitude = decodeFloat(value, key, &fields)
			}
		case "longitude":
			if isJSONNull(value) {
				patch.ClearLongitude = true
			} else {
				patch.Longitude = decodeFloat(value, key, &fields)
			}
		case "timezone":
			patch.Timezone = decodeString(value, key, &fields)
		case "phone":
			if isJSONNull(value) {
				patch.ClearPhone = true
			} else {
				patch.Phone = decodeString(value, key, &fields)
			}
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "code":
			// Capabilities, contracts and service requests are read against the code.
			immutable(key, &fields)
		default:
			unknownField(key, &fields)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	location, err := h.svc.UpdateLocation(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(location.RowVersion))
	writeJSON(w, http.StatusOK, locationView(location))
}

// ListProviderCapabilities implements listProviderCapabilities.
func (h *Handler) ListProviderCapabilities(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "locationId", application.ErrLocationNotFound)
	if !ok {
		return
	}
	result, err := h.svc.ListCapabilities(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, capabilityListView(result))
}

// PutProviderCapabilities implements putProviderCapabilities; If-Match carries the ETag of
// the location, which the replacement moves on.
func (h *Handler) PutProviderCapabilities(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "locationId", application.ErrLocationNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplaceProviderCapabilitiesRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	items := make([]domain.CapabilityInput, 0, len(body.Items))
	for _, row := range body.Items {
		in := domain.CapabilityInput{ValidFrom: domain.DateOnly(row.ValidFrom.Time)}
		if row.ServiceDefinitionId != nil {
			in.ServiceDefinitionID = row.ServiceDefinitionId.String()
		}
		if row.ServiceCategoryId != nil {
			in.ServiceCategoryID = row.ServiceCategoryId.String()
		}
		if row.ValidTo != nil {
			to := domain.DateOnly(row.ValidTo.Time)
			in.ValidTo = &to
		}
		if row.Notes != nil {
			in.Notes = *row.Notes
		}
		items = append(items, in)
	}

	result, err := h.svc.ReplaceCapabilities(r.Context(), rc, id, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, capabilityListView(result))
}

func locationView(l application.LocationRecord) kapsorav1.ProviderLocation {
	return kapsorav1.ProviderLocation{
		Id: l.ID, ProviderId: l.ProviderID, Code: l.Code, Name: l.Name,
		AddressLine: l.AddressLine, District: l.District, City: l.City,
		CountryCode: l.CountryCode, PostalCode: l.PostalCode, Latitude: l.Latitude,
		Longitude: l.Longitude, Timezone: l.Timezone, Phone: l.Phone,
		Status: kapsorav1.ProviderLocationStatus(l.Status), RowVersion: int(l.RowVersion),
	}
}

func capabilityListView(result application.CapabilityResult) kapsorav1.ProviderCapabilityList {
	out := kapsorav1.ProviderCapabilityList{
		Items: make([]kapsorav1.ProviderCapability, 0, len(result.Items)),
	}
	for _, c := range result.Items {
		item := kapsorav1.ProviderCapability{
			Id: c.ID, LocationId: c.LocationID, ValidFrom: openapi_types.Date{Time: c.ValidFrom},
			ServiceDefinitionCode: c.ServiceDefinitionCode, ServiceCategoryCode: c.ServiceCategoryCode,
			Notes: c.Notes,
		}
		item.ServiceDefinitionId = optionalUUID(c.ServiceDefinitionID)
		item.ServiceCategoryId = optionalUUID(c.ServiceCategoryID)
		if c.ValidTo != nil {
			item.ValidTo = &openapi_types.Date{Time: *c.ValidTo}
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// assignString copies an optional request field over a default the domain filled in.
func assignString(dst *string, value *string) {
	if value != nil {
		*dst = *value
	}
}

func optionalUUID(id *uuid.UUID) *openapi_types.UUID {
	if id == nil {
		return nil
	}
	value := *id
	return &value
}

// parseUUID is uuid.Parse behind a name the query readers can share.
func parseUUID(raw string) (uuid.UUID, error) { return uuid.Parse(raw) }
