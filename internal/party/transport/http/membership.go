package partyhttp

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
)

// ListMemberships implements listSponsorMemberships.
func (h *Handler) ListMemberships(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	items, err := h.svc.ListMemberships(r.Context(), rc, personID)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	out := kapsorav1.ListSponsorMemberships200JSONResponse{Items: make([]kapsorav1.SponsorMembership, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, membershipView(it))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateMembership implements createSponsorMembership.
func (h *Handler) CreateMembership(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionMembershipManage)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	var body kapsorav1.CreateMembershipRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewMembershipInput{
		SponsorOrganizationID: body.SponsorOrganizationId, MembershipType: body.MembershipType,
		PrincipalMembershipID: body.PrincipalMembershipId, ValidFrom: dateOnly(body.ValidFrom.Time),
	}
	if body.ExternalMemberNo != nil {
		in.ExternalMemberNo = *body.ExternalMemberNo
	}
	if body.Status != nil {
		in.Status = string(*body.Status)
	}
	if body.ValidTo != nil {
		to := dateOnly(body.ValidTo.Time)
		in.ValidTo = &to
	}

	membership, err := h.svc.CreateMembership(r.Context(), rc, personID, in)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	w.Header().Set("ETag", etag(membership.RowVersion))
	writeJSON(w, http.StatusCreated, membershipView(membership))
}

// UpdateMembership implements updateSponsorMembership (merge-patch with If-Match).
func (h *Handler) UpdateMembership(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionMembershipManage)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	membershipID, ok := pathUUID(w, r, "membershipId")
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

	patch := application.MembershipPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "externalMemberNo":
			if isJSONNull(value) {
				patch.ClearExternalMemberNo = true
			} else {
				patch.ExternalMemberNo = decodeString(value, key, &fields)
			}
		case "validTo":
			if isJSONNull(value) {
				patch.ClearValidTo = true
			} else {
				patch.ValidTo = decodeDate(value, key, &fields)
			}
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	membership, err := h.svc.UpdateMembership(r.Context(), rc, personID, membershipID, patch)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	w.Header().Set("ETag", etag(membership.RowVersion))
	writeJSON(w, http.StatusOK, membershipView(membership))
}

func membershipView(m application.Membership) kapsorav1.SponsorMembership {
	out := kapsorav1.SponsorMembership{
		Id: m.ID, PersonId: m.PersonID, SponsorOrganizationId: m.SponsorOrganizationID,
		SponsorDisplayName: m.SponsorDisplayName, MembershipType: m.MembershipType,
		PrincipalMembershipId: m.PrincipalMembershipID, ExternalMemberNo: m.ExternalMemberNo,
		Status:     kapsorav1.SponsorMembershipStatus(m.Status),
		ValidFrom:  openapi_types.Date{Time: m.ValidFrom},
		RowVersion: int(m.RowVersion), SourceSystem: m.SourceSystem,
	}
	if m.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *m.ValidTo}
	}
	return out
}
