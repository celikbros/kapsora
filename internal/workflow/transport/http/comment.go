package workflowhttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/workflow/application"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// ListWorkItemComments implements listWorkItemComments.
func (h *Handler) ListWorkItemComments(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "workItemId", application.ErrWorkItemNotFound)
	if !ok {
		return
	}
	rows, err := h.svc.ListComments(r.Context(), rc, id, r.URL.Query()["visibility"], 0)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.WorkItemCommentList{Items: make([]kapsorav1.WorkItemComment, 0, len(rows))}
	for _, row := range rows {
		out.Items = append(out.Items, commentView(row))
	}
	writeJSON(w, http.StatusOK, out)
}

// AddWorkItemComment implements addWorkItemComment. It carries no If-Match: a comment adds
// to the conversation rather than changing the item, and the item's own version is left
// exactly where it was.
func (h *Handler) AddWorkItemComment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "workItemId", application.ErrWorkItemNotFound)
	if !ok {
		return
	}
	var body kapsorav1.AddWorkItemComment
	if !decodeJSON(w, r, &body) {
		return
	}
	record, err := h.svc.AddComment(r.Context(), rc, id, domain.NewComment{
		Visibility: string(body.Visibility), Body: body.Body,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, commentView(record))
}

func commentView(c application.CommentRecord) kapsorav1.WorkItemComment {
	return kapsorav1.WorkItemComment{
		Id: c.ID, AggregateType: c.AggregateType, AggregateId: c.AggregateID,
		WorkItemId: c.WorkItemID, Visibility: kapsorav1.CommentVisibility(c.Visibility),
		Body: c.Body, AuthorActorId: c.AuthorActorID, CreatedAt: c.CreatedAt.UTC(),
	}
}
