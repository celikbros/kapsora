package partyhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/application"
)

// MyPersonRoutes registers GET /me/person on the tenant router. It hangs off /me rather
// than under /people/{personId} on purpose: there is no path segment for the caller to
// change, so the only person this route can ever answer with is the one the caller's grant
// names.
//
// The pattern is registered flat rather than mounted with chi.Route("/me", ...), and that
// is not a style choice. Mounting a subrouter at /me makes it the handler for the whole
// /me subtree and shadows the pre-tenant GET /api/v1/me that answers the tenant picker --
// silently, with a 404 that no compiler and no unit test of this package would catch.
func (h *Handler) MyPersonRoutes(r chi.Router) {
	r.Get("/me/person", h.GetMyPerson)
}

// GetMyPerson implements getMyPerson.
//
// It takes no permission beyond an authenticated tenant context. member.read is the grant
// to read *other* people's records and a member does not hold it; reading one's own record
// is not that act, and requiring the permission here would either lock every member out of
// their own name or force member.read onto every member account — which would be the same
// as giving each of them the tenant's address book.
func (h *Handler) GetMyPerson(w http.ResponseWriter, r *http.Request) {
	rc, ok := identity.FromContext(r.Context())
	if !ok {
		h.writeError(w, r, identity.ErrUnauthenticated, false)
		return
	}
	out, err := h.svc.LoadMyPerson(r.Context(), rc, h.enrollments)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	writeJSON(w, http.StatusOK, myPersonView(out))
}

func myPersonView(m application.MyPerson) kapsorav1.MyPerson {
	out := kapsorav1.MyPerson{
		Person:      personSummaryOf(m.Person),
		Enrollments: make([]kapsorav1.Enrollment, 0, len(m.Enrollments)),
		Contacts:    make([]kapsorav1.PersonContact, 0, len(m.Contacts)),
	}
	for _, e := range m.Enrollments {
		out.Enrollments = append(out.Enrollments, enrollmentView(e))
	}
	for _, c := range m.Contacts {
		out.Contacts = append(out.Contacts, kapsorav1.PersonContact{
			Id: c.ID, PersonId: c.PersonID,
			Channel:     kapsorav1.PersonContactChannel(c.Channel),
			MaskedValue: c.MaskedValue, VerifiedAt: c.VerifiedAt, Primary: c.Primary,
			RowVersion: c.RowVersion, CreatedAt: c.CreatedAt.UTC(),
		})
	}
	return out
}

// personSummaryOf is the summary shape and not the full person: a member reading their own
// record gets the masked identifier and never the list of every identifier the tenant holds
// for them, because that list is what an operator uses to correlate one person across
// sponsors and it is not a thing this endpoint exists to hand out.
func personSummaryOf(p application.Person) kapsorav1.PersonSummary {
	out := kapsorav1.PersonSummary{
		Id: p.ID, DisplayName: p.DisplayName, Status: kapsorav1.PersonSummaryStatus(p.Status),
	}
	if p.MaskedPrimaryIdentifier != "" {
		masked := p.MaskedPrimaryIdentifier
		out.MaskedPrimaryIdentifier = &masked
	}
	return out
}

func enrollmentView(e benefitapp.Enrollment) kapsorav1.Enrollment {
	out := kapsorav1.Enrollment{
		Id: e.ID, PersonId: e.PersonID, SponsorMembershipId: e.SponsorMembershipID,
		PlanId: e.PlanID, PlanCode: e.PlanCode,
		Status:     kapsorav1.EnrollmentStatus(e.Status),
		ValidFrom:  openapi_types.Date{Time: e.ValidFrom},
		RowVersion: int(e.RowVersion),
	}
	if e.ProgramID != uuid.Nil {
		programID := e.ProgramID
		out.ProgramId = &programID
	}
	if e.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *e.ValidTo}
	}
	out.EnrollmentReason = e.EnrollmentReason
	out.SourceSystem = e.SourceSystem
	return out
}
