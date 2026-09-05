package workflowhttp

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/workflow/application"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// ListWorkItems implements listWorkItems.
func (h *Handler) ListWorkItems(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ItemFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		QueueID: queryUUID(r, "queueId", &fields),
		Status:  r.URL.Query().Get("status"),
		// assignedToMe is resolved against the caller by the service; the query
		// parameter is a flag, never an actor id, so somebody else's list is not a
		// question this endpoint can be made to answer.
		AggregateType: strings.TrimSpace(r.URL.Query().Get("aggregateType")),
		AggregateID:   queryUUID(r, "aggregateId", &fields),
		Overdue:       queryBool(r, "overdue", &fields),
	}
	if mine := queryBool(r, "assignedToMe", &fields); mine != nil {
		filter.AssignedToMe = *mine
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListItems(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.WorkItemPage{Items: make([]kapsorav1.WorkItem, 0, len(page.Items))}
	for _, record := range page.Items {
		out.Items = append(out.Items, itemView(record))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// GetWorkItem implements getWorkItem.
func (h *Handler) GetWorkItem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "workItemId", application.ErrWorkItemNotFound)
	if !ok {
		return
	}
	record, err := h.svc.GetItem(r.Context(), rc, id)
	h.answerItem(w, r, record, err)
}

// ClaimWorkItem implements claimWorkItem. It carries no body: the whole command is the
// item in the path and the version in If-Match.
func (h *Handler) ClaimWorkItem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionClaim)
	if !ok {
		return
	}
	id, expected, ok := h.itemCommand(w, r)
	if !ok {
		return
	}
	record, err := h.svc.ClaimItem(r.Context(), rc, id, expected)
	h.answerItem(w, r, record, err)
}

// ReleaseWorkItem implements releaseWorkItem.
func (h *Handler) ReleaseWorkItem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionClaim)
	if !ok {
		return
	}
	id, expected, ok := h.itemCommand(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReleaseWorkItem
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	record, err := h.svc.ReleaseItem(r.Context(), rc, id,
		deref(body.ReasonCode), deref(body.ReasonText), expected)
	h.answerItem(w, r, record, err)
}

// ReassignWorkItem implements reassignWorkItem.
func (h *Handler) ReassignWorkItem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionReassign)
	if !ok {
		return
	}
	id, expected, ok := h.itemCommand(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReassignWorkItem
	if !decodeJSON(w, r, &body) {
		return
	}
	record, err := h.svc.ReassignItem(r.Context(), rc, id, body.AssigneeActorId,
		deref(body.ReasonCode), deref(body.ReasonText), expected)
	h.answerItem(w, r, record, err)
}

// CompleteWorkItem implements completeWorkItem.
func (h *Handler) CompleteWorkItem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionClaim)
	if !ok {
		return
	}
	id, expected, ok := h.itemCommand(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CompleteWorkItem
	if !decodeJSON(w, r, &body) {
		return
	}
	record, err := h.svc.CompleteItem(r.Context(), rc, id, body.OutcomeCode,
		deref(body.Comment), expected)
	h.answerItem(w, r, record, err)
}

// itemCommand reads the two things every work item command needs: which item, and which
// version the caller believes it is at.
func (h *Handler) itemCommand(w http.ResponseWriter, r *http.Request) (id uuid.UUID, expected int64, ok bool) {
	id, ok = h.pathUUID(w, r, "workItemId", application.ErrWorkItemNotFound)
	if !ok {
		return id, 0, false
	}
	expected, ok = requireIfMatch(w, r)
	return id, expected, ok
}

// answerItem writes the item with its new ETag, or the problem the command produced. Every
// work item route answers 200: none of them creates a resource, and the claim that loses
// its race leaves through writeError rather than here.
func (h *Handler) answerItem(w http.ResponseWriter, r *http.Request,
	record application.ItemRecord, err error,
) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, itemView(record))
}

func itemView(i application.ItemRecord) kapsorav1.WorkItem {
	return kapsorav1.WorkItem{
		Id: i.ID, QueueId: i.QueueID, AggregateType: i.AggregateType,
		AggregateId: i.AggregateID, Title: i.Title, Priority: i.Priority,
		AssigneeActorId: i.AssigneeActorID, AssigneeDisplayName: i.AssigneeDisplayName,
		AssignedAt: utcPtr(i.AssignedAt),
		DueAt:      utcPtr(i.DueAt), SlaMinutesSnapshot: i.SLAMinutesSnapshot,
		Status: kapsorav1.WorkItemStatus(i.Status), OutcomeCode: i.OutcomeCode,
		CompletedAt: utcPtr(i.CompletedAt), CompletedBy: i.CompletedBy,
		EscalatedAt: utcPtr(i.EscalatedAt), EscalatedFromQueueId: i.EscalatedFromQueueID,
		RowVersion: i.RowVersion, CreatedAt: i.CreatedAt.UTC(),
	}
}

// utcPtr normalises an optional instant to UTC, so a timestamp reads the same however the
// server is configured.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	value := t.UTC()
	return &value
}

// deref reads an optional body field as the empty string the service treats as absent.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
