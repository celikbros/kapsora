package accommodationhttp

import (
	"net/http"
	"strings"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/accommodation/domain"
)

// ListProperties serves GET /accommodation/properties.
func (h *Handler) ListProperties(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.PropertyFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		Status:                 strings.TrimSpace(r.URL.Query().Get("status")),
		PropertyType:           strings.TrimSpace(r.URL.Query().Get("propertyType")),
		RegionCode:             strings.TrimSpace(r.URL.Query().Get("regionCode")),
		City:                   strings.TrimSpace(r.URL.Query().Get("city")),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListProperties(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.PropertyPage{Items: make([]kapsorav1.Property, 0, len(page.Items))}
	for _, item := range page.Items {
		body.Items = append(body.Items, propertyView(item))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		body.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, body)
}

// CreateProperty serves POST /accommodation/properties.
func (h *Handler) CreateProperty(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateProperty
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewPropertyInput{
		ProviderOrganizationID: body.ProviderOrganizationId,
		LocationID:             body.LocationId,
		Code:                   body.Code,
		Name:                   body.Name,
		PropertyType:           string(body.PropertyType),
		Timezone:               body.Timezone,
		City:                   body.City,
		RegionCode:             body.RegionCode,
		Amenities:              amenityCodes(body.Amenities),
		CostCenter:             body.CostCenter,
		Status:                 domain.StatusActive,
	}
	if body.Status != nil {
		in.Status = string(*body.Status)
	}
	record, err := h.svc.CreateProperty(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	w.Header().Set("Location", "/api/v1/accommodation/properties/"+record.ID.String())
	writeJSON(w, http.StatusCreated, propertyView(record))
}

// GetProperty serves GET /accommodation/properties/{propertyId}.
func (h *Handler) GetProperty(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "propertyId", application.ErrPropertyNotFound)
	if !ok {
		return
	}
	record, err := h.svc.GetProperty(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, propertyView(record))
}

// PatchProperty serves PATCH /accommodation/properties/{propertyId}.
func (h *Handler) PatchProperty(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "propertyId", application.ErrPropertyNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PatchProperty
	if !decodeJSON(w, r, &body) {
		return
	}
	record, err := h.svc.PatchProperty(r.Context(), rc, id, application.PropertyPatchInput{
		LocationID:   body.LocationId,
		Name:         body.Name,
		PropertyType: string(body.PropertyType),
		Timezone:     body.Timezone,
		City:         body.City,
		RegionCode:   body.RegionCode,
		Amenities:    amenityCodes(body.Amenities),
		CostCenter:   body.CostCenter,
		Status:       string(body.Status),
	}, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, propertyView(record))
}

// propertyView renders one property. The amenities are re-typed rather than cast wholesale
// so a key the domain no longer knows cannot reach the wire as a value the contract's enum
// does not admit.
func propertyView(record application.PropertyRecord) kapsorav1.Property {
	out := kapsorav1.Property{
		Id:                     record.ID,
		ProviderOrganizationId: record.ProviderOrganizationID,
		LocationId:             record.LocationID,
		Code:                   record.Code,
		Name:                   record.Name,
		PropertyType:           kapsorav1.PropertyType(record.PropertyType),
		Timezone:               record.Timezone,
		City:                   record.City,
		RegionCode:             record.RegionCode,
		Amenities:              make([]kapsorav1.PropertyAmenity, 0, len(record.Amenities)),
		CostCenter:             record.CostCenter,
		Status:                 kapsorav1.PropertyStatus(record.Status),
		CreatedAt:              record.CreatedAt,
		RowVersion:             record.RowVersion,
	}
	for _, key := range record.Amenities {
		out.Amenities = append(out.Amenities, kapsorav1.PropertyAmenity(key))
	}
	if !record.UpdatedAt.IsZero() {
		updated := record.UpdatedAt
		out.UpdatedAt = &updated
	}
	return out
}

// amenityCodes turns the contract's enum values back into plain keys. An unknown one is not
// dropped here: the application service refuses it on the field, so a client that sent a key
// the product does not have is told which key.
func amenityCodes(in *[]kapsorav1.PropertyAmenity) []string {
	if in == nil {
		return []string{}
	}
	out := make([]string, 0, len(*in))
	for _, key := range *in {
		out = append(out, string(key))
	}
	return out
}
