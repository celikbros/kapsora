package accommodationhttp

import (
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/identity"
)

// SearchAvailability serves POST /accommodation/availability/search.
//
// The one thing this handler decides is whose stay the search is about, and it decides it
// the same way every member-side command in the vertical does.
//
// A caller bound to a person goes through identity.RequirePerson: the person is the one its
// account is bound to, a body echoing that person back is accepted, and a body naming
// anybody else is refused with PERSON_SCOPE. That refusal is the whole reason the binding
// exists — without it a member could search, and then hold, a room for their neighbour.
//
// A caller bound to no person is a desk: a reservation clerk, a programme manager, the
// sponsor's HR user. It has to name the person it is acting for, because the search answers
// what one member may have and there is no such answer without a member.
func (h *Handler) SearchAvailability(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var body kapsorav1.AvailabilitySearchRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	requested := uuid.Nil
	if body.PersonId != nil {
		requested = *body.PersonId
	}
	personID := requested
	if _, bound := rc.PersonScope(); bound {
		var err error
		if _, personID, err = identity.RequirePerson(r.Context(), requested); err != nil {
			h.writeError(w, r, err)
			return
		}
	}
	if personID == uuid.Nil {
		h.writeError(w, r, application.ErrPersonRequired)
		return
	}

	in := application.SearchInput{
		PersonID: personID, CheckIn: body.CheckIn.Time, CheckOut: body.CheckOut.Time,
		Adults: body.Adults, PropertyID: body.PropertyId, ProgramID: body.ProgramId,
	}
	if body.Children != nil {
		in.Children = *body.Children
	}
	if body.RegionCode != nil {
		in.RegionCode = *body.RegionCode
	}

	result, err := h.svc.SearchAvailability(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, searchView(result))
}

func searchView(in application.SearchResult) kapsorav1.AvailabilitySearchResult {
	out := kapsorav1.AvailabilitySearchResult{
		PersonId: in.PersonID,
		CheckIn:  openapi_types.Date{Time: in.CheckIn},
		CheckOut: openapi_types.Date{Time: in.CheckOut},
		Nights:   in.Nights,
		Eligible: in.Eligible,
		Results:  make([]kapsorav1.AvailabilityRoomTypeResult, 0, len(in.Items)),
	}
	if in.EvaluationID != nil {
		id := *in.EvaluationID
		out.EvaluationId = &id
	}
	if in.Entitlement != nil {
		out.Entitlement = &kapsorav1.AvailabilityEntitlement{
			EntitlementCode: in.Entitlement.EntitlementCode,
			Unit:            in.Entitlement.Unit,
			Remaining:       in.Entitlement.Remaining,
		}
	}
	for _, item := range in.Items {
		entry := kapsorav1.AvailabilityRoomTypeResult{
			Property:  propertyView(item.Property),
			RoomType:  availabilityRoomTypeView(item.RoomType),
			Available: item.Available,
		}
		if item.Quote != nil {
			entry.Quote = quoteView(*item.Quote)
		}
		if item.QuoteUnavailableReason != "" {
			reason := kapsorav1.QuoteUnavailableReason(item.QuoteUnavailableReason)
			entry.QuoteUnavailableReason = &reason
		}
		out.Results = append(out.Results, entry)
	}
	return out
}

func quoteView(in application.QuoteView) *kapsorav1.AvailabilityQuote {
	out := &kapsorav1.AvailabilityQuote{
		CurrencyCode:   in.CurrencyCode,
		TotalAmount:    in.TotalAmount,
		PayerAmount:    in.PayerAmount,
		MemberAmount:   in.MemberAmount,
		NightlyAmounts: make([]kapsorav1.AvailabilityNightAmount, 0, len(in.NightlyAmounts)),
	}
	for _, night := range in.NightlyAmounts {
		out.NightlyAmounts = append(out.NightlyAmounts, kapsorav1.AvailabilityNightAmount{
			StayDate:     openapi_types.Date{Time: night.StayDate},
			Amount:       night.Amount,
			PayerAmount:  night.PayerAmount,
			MemberAmount: night.MemberAmount,
		})
	}
	return out
}

// availabilityRoomTypeView renders the room type of a search result. It is the same shape a
// direct read answers with, down to the row version: a screen that offers a room and then
// wants to open its allotment should not have to fetch it again to learn its ETag.
func availabilityRoomTypeView(in application.AvailabilityRoomType) kapsorav1.RoomType {
	out := kapsorav1.RoomType{
		Id:                  in.ID,
		PropertyId:          in.PropertyID,
		Code:                in.Code,
		Name:                in.Name,
		MaxAdults:           in.MaxAdults,
		MaxChildren:         in.MaxChildren,
		MaxOccupancy:        in.MaxOccupancy,
		Attributes:          attributesMap(in.Attributes),
		ServiceDefinitionId: in.ServiceDefinitionID,
		Status:              kapsorav1.PropertyStatus(in.Status),
		CreatedAt:           in.CreatedAt,
		RowVersion:          in.RowVersion,
	}
	if !in.UpdatedAt.IsZero() {
		updated := in.UpdatedAt
		out.UpdatedAt = &updated
	}
	return out
}
