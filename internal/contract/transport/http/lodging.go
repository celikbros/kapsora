package contracthttp

import (
	"net/http"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
)

// GetLodgingTerms implements getContractVersionLodgingTerms.
//
// contract.read is enough to read them, as it is for the payment term: what a cancellation
// costs is a term of an agreement everybody downstream is bound by, and a member support
// desk that may see the price sheet may see the cancellation policy beside it.
func (h *Handler) GetLodgingTerms(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	result, err := h.svc.GetLodgingTerms(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, lodgingTermsView(result.Terms, result.RowVersion))
}

// PutLodgingTerms implements putContractVersionLodgingTerms.
//
// Writing takes contract.lodging_terms.manage rather than contract.manage. They are held
// together by every role that may write a version, so nothing about who can do this
// changes; what the separate code buys is an access review that can see the accommodation
// clause of an agreement as a thing of its own.
func (h *Handler) PutLodgingTerms(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionLodgingManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PutLodgingTermsRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.LodgingTermsInput{
		FreeCancellationHoursBefore: body.FreeCancellationHoursBefore,
		PenaltyKind:                 string(body.PenaltyKind),
		PenaltyNights:               body.PenaltyNights,
		NoShowPercent:               body.NoShowPercent,
		HoldMinutes:                 body.HoldMinutes,
		MinNights:                   body.MinNights,
		MaxNights:                   body.MaxNights,
		ChildFreeUnderAge:           body.ChildFreeUnderAge,
	}
	if body.PenaltyPercent != nil {
		in.PenaltyPercent = *body.PenaltyPercent
	}

	result, err := h.svc.PutLodgingTerms(r.Context(), rc, versionID, in, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, lodgingTermsView(result.Terms, result.RowVersion))
}

// GetLodgingPolicy implements getContractVersionLodgingPolicy: the same terms stamped with
// the moment and the zone, which is the shape WP-I6-02 freezes onto a booking. The zone is
// the tenant's own rather than a value the caller may pass, because a caller that could
// choose the zone could choose when the free-cancellation window closed.
func (h *Handler) GetLodgingPolicy(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "contractVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	snapshot, err := h.svc.SnapshotLodgingPolicy(r.Context(), rc, versionID, rc.TimeZone)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lodgingPolicyView(snapshot))
}

func lodgingTermsView(t application.LodgingTermsRecord, rowVersion int64) kapsorav1.LodgingTerms {
	return kapsorav1.LodgingTerms{
		Id: t.ID, ContractVersionId: t.ContractVersionID,
		FreeCancellationHoursBefore: t.FreeCancellationHoursBefore,
		PenaltyKind:                 kapsorav1.LodgingPenaltyKind(t.PenaltyKind),
		PenaltyNights:               t.PenaltyNights,
		PenaltyPercent:              lodgingPercent(t.PenaltyPercent),
		NoShowPercent:               t.NoShowPercent,
		HoldMinutes:                 t.HoldMinutes,
		MinNights:                   t.MinNights,
		MaxNights:                   t.MaxNights,
		ChildFreeUnderAge:           t.ChildFreeUnderAge,
		RowVersion:                  int(rowVersion),
	}
}

func lodgingPolicyView(s application.LodgingPolicySnapshot) kapsorav1.LodgingPolicySnapshot {
	return kapsorav1.LodgingPolicySnapshot{
		ContractVersionId: s.ContractVersionID,
		SnapshotAt:        s.SnapshotAt,
		Timezone:          s.TimeZone,

		FreeCancellationHoursBefore: s.Terms.FreeCancellationHoursBefore,
		PenaltyKind:                 kapsorav1.LodgingPenaltyKind(s.Terms.PenaltyKind),
		PenaltyNights:               s.Terms.PenaltyNights,
		PenaltyPercent:              lodgingPercent(s.Terms.PenaltyPercent),
		NoShowPercent:               s.Terms.NoShowPercent,
		HoldMinutes:                 s.Terms.HoldMinutes,
		MinNights:                   s.Terms.MinNights,
		MaxNights:                   s.Terms.MaxNights,
		ChildFreeUnderAge:           s.Terms.ChildFreeUnderAge,
	}
}

// lodgingPercent renders the optional percentage. It stays a string the whole way -- the
// generated LodgingPercent is an alias for string, the record holds the canonical decimal
// the column does, and nothing between the two parses it into a number.
func lodgingPercent(raw string) *kapsorav1.LodgingPercent {
	if raw == "" {
		return nil
	}
	v := raw
	return &v
}
