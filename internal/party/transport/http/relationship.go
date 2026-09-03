package partyhttp

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/party/application"
)

// ListRelationships implements listPersonRelationships.
func (h *Handler) ListRelationships(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	items, err := h.svc.ListRelationships(r.Context(), rc, personID)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	out := kapsorav1.ListPersonRelationships200JSONResponse{Items: make([]kapsorav1.PersonRelationship, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, relationshipView(it))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateRelationship implements createPersonRelationship.
func (h *Handler) CreateRelationship(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRelationshipManage)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	var body kapsorav1.CreateRelationshipRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewRelationshipInput{
		TargetPersonID: body.TargetPersonId, RelationshipType: body.RelationshipType,
		ValidFrom: dateOnly(body.ValidFrom.Time),
	}
	if body.ValidTo != nil {
		to := dateOnly(body.ValidTo.Time)
		in.ValidTo = &to
	}

	rel, err := h.svc.CreateRelationship(r.Context(), rc, personID, in)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	w.Header().Set("ETag", etag(rel.RowVersion))
	writeJSON(w, http.StatusCreated, relationshipView(rel))
}

// EndRelationship implements endPersonRelationship.
func (h *Handler) EndRelationship(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRelationshipManage)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	relationshipID, ok := pathUUID(w, r, "relationshipId")
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.EndPeriodCommand
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.EndRelationshipInput{
		EndsOn: dateOnly(body.EndsOn.Time), ReasonCode: body.ReasonCode, ExpectedVersion: expected,
	}
	if body.ReasonText != nil {
		in.ReasonText = *body.ReasonText
	}

	rel, err := h.svc.EndRelationship(r.Context(), rc, personID, relationshipID, in)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	w.Header().Set("ETag", etag(rel.RowVersion))
	writeJSON(w, http.StatusOK, relationshipView(rel))
}

func relationshipView(rel application.Relationship) kapsorav1.PersonRelationship {
	out := kapsorav1.PersonRelationship{
		Id: rel.ID, RelationshipType: rel.RelationshipType,
		Direction:   kapsorav1.PersonRelationshipDirection(rel.Direction),
		OtherPerson: summaryView(rel.Other),
		Status:      kapsorav1.PersonRelationshipStatus(rel.Status),
		ValidFrom:   openapi_types.Date{Time: rel.ValidFrom},
		RowVersion:  int(rel.RowVersion),
	}
	if rel.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *rel.ValidTo}
	}
	out.EndReasonCode = rel.EndReasonCode
	return out
}
