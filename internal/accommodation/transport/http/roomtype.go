package accommodationhttp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/accommodation/domain"
)

// ListRoomTypes serves GET /accommodation/properties/{propertyId}/room-types.
func (h *Handler) ListRoomTypes(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	propertyID, ok := h.pathID(w, r, "propertyId", application.ErrPropertyNotFound)
	if !ok {
		return
	}
	rows, err := h.svc.ListRoomTypes(r.Context(), rc, propertyID,
		strings.TrimSpace(r.URL.Query().Get("status")))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.RoomTypeList{Items: make([]kapsorav1.RoomType, 0, len(rows))}
	for _, row := range rows {
		body.Items = append(body.Items, roomTypeView(row))
	}
	writeJSON(w, http.StatusOK, body)
}

// CreateRoomType serves POST /accommodation/properties/{propertyId}/room-types.
func (h *Handler) CreateRoomType(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	propertyID, ok := h.pathID(w, r, "propertyId", application.ErrPropertyNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreateRoomType
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewRoomTypeInput{
		PropertyID:          propertyID,
		Code:                body.Code,
		Name:                body.Name,
		MaxAdults:           body.MaxAdults,
		MaxOccupancy:        body.MaxOccupancy,
		Attributes:          attributesOf(body.Attributes),
		ServiceDefinitionID: body.ServiceDefinitionId,
		Status:              domain.StatusActive,
	}
	if body.MaxChildren != nil {
		in.MaxChildren = *body.MaxChildren
	}
	if body.Status != nil {
		in.Status = string(*body.Status)
	}
	record, err := h.svc.CreateRoomType(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	w.Header().Set("Location", "/api/v1/accommodation/room-types/"+record.ID.String())
	writeJSON(w, http.StatusCreated, roomTypeView(record))
}

// PatchRoomType serves PATCH /accommodation/room-types/{roomTypeId}.
func (h *Handler) PatchRoomType(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "roomTypeId", application.ErrRoomTypeNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PatchRoomType
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.RoomTypePatchInput{
		Name:         body.Name,
		MaxAdults:    body.MaxAdults,
		MaxOccupancy: body.MaxOccupancy,
		Attributes:   attributesOf(body.Attributes),
		Status:       string(body.Status),
	}
	if body.MaxChildren != nil {
		in.MaxChildren = *body.MaxChildren
	}
	record, err := h.svc.PatchRoomType(r.Context(), rc, id, in, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, roomTypeView(record))
}

func roomTypeView(record application.RoomTypeRecord) kapsorav1.RoomType {
	out := kapsorav1.RoomType{
		Id:                  record.ID,
		PropertyId:          record.PropertyID,
		Code:                record.Code,
		Name:                record.Name,
		MaxAdults:           record.MaxAdults,
		MaxChildren:         record.MaxChildren,
		MaxOccupancy:        record.MaxOccupancy,
		Attributes:          attributesMap(record.Attributes),
		ServiceDefinitionId: record.ServiceDefinitionID,
		Status:              kapsorav1.PropertyStatus(record.Status),
		CreatedAt:           record.CreatedAt,
		RowVersion:          record.RowVersion,
	}
	if !record.UpdatedAt.IsZero() {
		updated := record.UpdatedAt
		out.UpdatedAt = &updated
	}
	return out
}

// attributesOf turns the contract's free-form object into the raw JSON the service stores.
// A body that carried none stores `{}` rather than null, because the column's CHECK says an
// object and a null would be a room type nobody could read back.
func attributesOf(in *map[string]interface{}) json.RawMessage {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(*in)
	if err != nil {
		return nil
	}
	return raw
}

// attributesMap renders them back. A stored value that no longer parses is answered as an
// empty object rather than failing the read: the room type's own facts are what the caller
// asked for, and a bed layout nobody can decode is not worth refusing them over.
func attributesMap(raw []byte) map[string]interface{} {
	out := map[string]interface{}{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]interface{}{}
	}
	return out
}
