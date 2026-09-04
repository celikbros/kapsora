package documenthttp

import (
	"net/http"
	"time"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/document/application"
)

// LinkDocument implements linkDocument.
func (h *Handler) LinkDocument(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionLink)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "documentId", application.ErrObjectNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreateDocumentLink
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewLinkInput{
		AggregateType: body.AggregateType, AggregateID: body.AggregateId,
		DocumentTypeCode: body.DocumentTypeCode,
	}
	if body.Purpose != nil {
		in.Purpose = *body.Purpose
	}
	if body.RequiredPermission != nil {
		in.RequiredPermission = *body.RequiredPermission
	}

	link, err := h.svc.LinkDocument(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, linkView(link))
}

// UnlinkDocument implements unlinkDocument.
func (h *Handler) UnlinkDocument(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionLink)
	if !ok {
		return
	}
	documentID, ok := h.pathUUID(w, r, "documentId", application.ErrObjectNotFound)
	if !ok {
		return
	}
	linkID, ok := h.pathUUID(w, r, "linkId", application.ErrLinkNotFound)
	if !ok {
		return
	}
	if err := h.svc.UnlinkDocument(r.Context(), rc, documentID, linkID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func linkView(link application.LinkRecord) kapsorav1.DocumentLink {
	return kapsorav1.DocumentLink{
		Id: link.ID, DocumentId: link.ObjectID,
		AggregateType: link.AggregateType, AggregateId: link.AggregateID,
		DocumentTypeCode: link.DocumentTypeCode, Purpose: link.Purpose,
		RequiredPermission: link.RequiredPermission, CreatedBy: link.CreatedBy,
		CreatedAt: link.CreatedAt.UTC(),
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	moment := t.UTC()
	return &moment
}
