package documenthttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/document/application"
)

// PutLegalHold implements putLegalHold.
func (h *Handler) PutLegalHold(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionLegalHold)
	if !ok {
		return
	}
	var body kapsorav1.CreateLegalHold
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewLegalHoldInput{
		ObjectID: body.DocumentId, PersonID: body.PersonId,
		AggregateID: body.AggregateId, Reason: body.Reason,
	}
	if body.AggregateType != nil {
		in.AggregateType = *body.AggregateType
	}

	hold, err := h.svc.PutLegalHold(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(hold.RowVersion))
	writeJSON(w, http.StatusCreated, holdView(hold))
}

// ReleaseLegalHold implements releaseLegalHold. It carries If-Match like every other
// command on a versioned row: two people lifting one hold at the same time must not each
// believe they were the one who did it.
func (h *Handler) ReleaseLegalHold(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionLegalHold)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "legalHoldId", application.ErrHoldNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	hold, err := h.svc.ReleaseLegalHold(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(hold.RowVersion))
	writeJSON(w, http.StatusOK, holdView(hold))
}

func holdView(hold application.LegalHoldRecord) kapsorav1.LegalHold {
	return kapsorav1.LegalHold{
		Id: hold.ID, DocumentId: hold.ObjectID, PersonId: hold.PersonID,
		AggregateType: hold.AggregateType, AggregateId: hold.AggregateID,
		Reason: hold.Reason, PlacedBy: hold.PlacedBy, PlacedAt: hold.PlacedAt.UTC(),
		ReleasedAt: utcPtr(hold.ReleasedAt), ReleasedBy: hold.ReleasedBy,
		RowVersion: hold.RowVersion,
	}
}
