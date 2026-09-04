package workflowhttp

import (
	"encoding/json"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/workflow/application"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// ListWorkQueues implements listWorkQueues.
func (h *Handler) ListWorkQueues(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireAny(w, r, queueReadPermissions)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.QueueFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		DomainCode: r.URL.Query().Get("domainCode"),
		Active:     queryBool(r, "active", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListQueues(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.WorkQueuePage{Items: make([]kapsorav1.WorkQueue, 0, len(page.Items))}
	for _, queue := range page.Items {
		out.Items = append(out.Items, queueView(queue))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateWorkQueue implements createWorkQueue.
func (h *Handler) CreateWorkQueue(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionQueueManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateWorkQueue
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewQueueInput{
		Code: body.Code, Name: body.Name, DomainCode: string(body.DomainCode),
		SLAMinutes: body.SlaMinutes, EscalationQueueID: body.EscalationQueueId,
		Active: body.Active,
	}
	if body.AssignmentPolicy != nil {
		in.AssignmentPolicy = string(*body.AssignmentPolicy)
	}
	record, err := h.svc.CreateQueue(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusCreated, queueView(record))
}

// PatchWorkQueue implements patchWorkQueue (merge-patch with If-Match).
//
// The body is decoded field by field rather than into the generated struct, because the
// answer to `{"code":"OTHER"}` has to be a 422 naming the field: a struct with no code
// field would either reject the whole body as unknown or accept it silently.
func (h *Handler) PatchWorkQueue(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionQueueManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "queueId", application.ErrQueueNotFound)
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

	var (
		patch  domain.QueuePatch
		fields []domain.FieldError
	)
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "assignmentPolicy":
			patch.AssignmentPolicy = decodeString(value, key, &fields)
		case "slaMinutes":
			patch.SLAMinutes = decodeNullableInt(value, key, &fields)
		case "escalationQueueId":
			patch.EscalationQueueID = decodeNullableUUID(value, key, &fields)
		case "active":
			patch.Active = decodeBool(value, key, &fields)
		case "code", "domainCode", "id", "rowVersion", "createdAt":
			fields = append(fields, domain.FieldError{
				Field: key, Code: "IMMUTABLE", Message: "bu alan değiştirilemez",
			})
		default:
			fields = append(fields, domain.FieldError{
				Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan",
			})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	record, err := h.svc.PatchQueue(r.Context(), rc, id, patch, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, queueView(record))
}

func queueView(q application.QueueRecord) kapsorav1.WorkQueue {
	return kapsorav1.WorkQueue{
		Id: q.ID, Code: q.Code, Name: q.Name,
		DomainCode:       kapsorav1.WorkQueueDomain(q.DomainCode),
		AssignmentPolicy: kapsorav1.AssignmentPolicy(q.AssignmentPolicy),
		SlaMinutes:       q.SLAMinutes, EscalationQueueId: q.EscalationQueueID,
		Active: q.Active, RowVersion: q.RowVersion, CreatedAt: q.CreatedAt.UTC(),
	}
}
