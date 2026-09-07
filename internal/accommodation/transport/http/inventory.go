package accommodationhttp

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/accommodation/domain"
)

// GetRoomTypeInventory serves GET /accommodation/room-types/{roomTypeId}/inventory.
func (h *Handler) GetRoomTypeInventory(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "roomTypeId", application.ErrRoomTypeNotFound)
	if !ok {
		return
	}
	var fields []domain.FieldError
	from := queryDate(r, "from", &fields)
	to := queryDate(r, "to", &fields)
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	rangeView, err := h.svc.GetRoomTypeInventory(r.Context(), rc, id, from, to)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inventoryRangeView(rangeView))
}

// PutRoomTypeInventory serves PUT /accommodation/room-types/{roomTypeId}/inventory.
func (h *Handler) PutRoomTypeInventory(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathID(w, r, "roomTypeId", application.ErrRoomTypeNotFound)
	if !ok {
		return
	}
	var body kapsorav1.PutRoomTypeInventory
	if !decodeJSON(w, r, &body) {
		return
	}
	rangeView, err := h.svc.PutRoomTypeInventory(r.Context(), rc, id, application.PutInventoryInput{
		From: body.From.Time, To: body.To.Time, Capacity: body.Capacity,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, inventoryRangeView(rangeView))
}

func inventoryRangeView(in application.InventoryRange) kapsorav1.RoomTypeInventoryRange {
	out := kapsorav1.RoomTypeInventoryRange{
		RoomTypeId: in.RoomTypeID,
		From:       openapi_types.Date{Time: in.From},
		To:         openapi_types.Date{Time: in.To},
		Days:       make([]kapsorav1.InventoryDay, 0, len(in.Days)),
	}
	for _, day := range in.Days {
		entry := kapsorav1.InventoryDay{
			StayDate: openapi_types.Date{Time: day.StayDate},
			Allotted: day.Allotted, Capacity: day.Capacity, Held: day.Held,
			Confirmed: day.Confirmed, Available: day.Available,
		}
		if day.Allotted {
			version := day.RowVersion
			entry.RowVersion = &version
			if day.UpdatedAt != nil {
				updated := *day.UpdatedAt
				entry.UpdatedAt = &updated
			}
		}
		out.Days = append(out.Days, entry)
	}
	return out
}
