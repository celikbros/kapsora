package partyhttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
)

// ListContacts implements listPersonContacts. What comes back is the mask; the address
// itself is returned by no endpoint at all.
func (h *Handler) ListContacts(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, application.PermissionContactRead)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	contacts, err := h.svc.ListContacts(r.Context(), rc, personID)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	writeJSON(w, http.StatusOK, contactList(contacts))
}

// PutContacts implements putPersonContacts.
func (h *Handler) PutContacts(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, application.PermissionContactManage)
	if !ok {
		return
	}
	personID, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body struct {
		Items []kapsorav1.PersonContactInput `json:"items"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.SubmittedContact, 0, len(body.Items))
	for _, item := range body.Items {
		next := domain.SubmittedContact{Channel: string(item.Channel), Value: item.Value}
		if item.Primary != nil {
			next.Primary = *item.Primary
		}
		if item.Verified != nil {
			next.Verified = *item.Verified
		}
		items = append(items, next)
	}

	contacts, err := h.svc.ReplaceContacts(r.Context(), rc, personID, items, expected)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	writeJSON(w, http.StatusOK, contactList(contacts))
}

func contactList(contacts []application.Contact) kapsorav1.ListPersonContacts200JSONResponse {
	out := kapsorav1.ListPersonContacts200JSONResponse{
		Items: make([]kapsorav1.PersonContact, 0, len(contacts)),
	}
	for _, c := range contacts {
		out.Items = append(out.Items, kapsorav1.PersonContact{
			Id: c.ID, PersonId: c.PersonID,
			Channel:     kapsorav1.PersonContactChannel(c.Channel),
			MaskedValue: c.MaskedValue, VerifiedAt: c.VerifiedAt, Primary: c.Primary,
			RowVersion: c.RowVersion, CreatedAt: c.CreatedAt.UTC(),
		})
	}
	return out
}
