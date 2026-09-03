package partyhttp

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
)

// Create implements createPerson.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreatePersonRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewPerson{FirstName: body.FirstName, LastName: body.LastName}
	if body.MiddleName != nil {
		in.MiddleName = *body.MiddleName
	}
	if body.BirthDate != nil {
		d := dateOnly(body.BirthDate.Time)
		in.BirthDate = &d
	}
	if body.SexAtBirth != nil {
		in.SexAtBirth = string(*body.SexAtBirth)
	}
	if body.Identifiers != nil {
		for _, id := range *body.Identifiers {
			in.Identifiers = append(in.Identifiers, domain.SubmittedIdentifier{
				Type: id.Type, Value: id.Value, Primary: id.Primary != nil && *id.Primary,
			})
		}
	}

	person, err := h.svc.Create(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err, rc.Has(PermissionIdentifierSearch))
		return
	}
	w.Header().Set("ETag", etag(person.RowVersion))
	w.Header().Set("Location", "/api/v1/people/"+person.ID.String())
	writeJSON(w, http.StatusCreated, personView(person))
}

// Get implements getPerson. A merged person answers 200 with status MERGED and the
// surviving person in mergedIntoId.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "personId")
	if !ok {
		return
	}
	person, err := h.svc.Get(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	w.Header().Set("ETag", etag(person.RowVersion))
	writeJSON(w, http.StatusOK, personView(person))
}

// List implements listPeople.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := application.ListFilter{
		Query: strings.TrimSpace(q.Get("q")), Status: q.Get("status"), Cursor: q.Get("cursor"),
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeValidation(w, r, []domain.FieldError{{Field: "limit", Code: "FORMAT", Message: "1-200 arası tam sayı olmalı"}})
			return
		}
		filter.Limit = n
	}
	if raw := q.Get("sponsorOrganizationId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeValidation(w, r, []domain.FieldError{{Field: "sponsorOrganizationId", Code: "FORMAT", Message: "geçerli bir kimlik olmalı"}})
			return
		}
		filter.SponsorID = id
	}

	page, err := h.svc.List(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	out := kapsorav1.PersonPage{Items: make([]kapsorav1.PersonSummary, 0, len(page.Items))}
	for _, s := range page.Items {
		out.Items = append(out.Items, summaryView(s))
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, out)
}

// Update implements updatePerson (application/merge-patch+json with If-Match).
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := pathUUID(w, r, "personId")
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
	patch, fields := decodePersonPatch(raw)
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	patch.ExpectedVersion = expected

	person, err := h.svc.Update(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err, rc.Has(PermissionIdentifierSearch))
		return
	}
	w.Header().Set("ETag", etag(person.RowVersion))
	writeJSON(w, http.StatusOK, personView(person))
}

// Search implements searchPeopleByIdentifier: permission plus a valid step-up window.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.RequireStepUp(r.Context(), PermissionIdentifierSearch)
	if err != nil {
		h.deny.Deny(w, r, err, PermissionIdentifierSearch)
		return
	}
	var body kapsorav1.IdentifierSearchRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.SearchInput{Type: body.Type, Value: body.Value}
	if body.SponsorOrganizationId != nil {
		in.SponsorID = *body.SponsorOrganizationId
	}
	summary, err := h.svc.SearchByIdentifier(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	writeJSON(w, http.StatusOK, summaryView(summary))
}

// Catalogs implements listPartyCatalogs.
func (h *Handler) Catalogs(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	catalogs, err := h.svc.Catalogs(r.Context(), rc)
	if err != nil {
		h.writeError(w, r, err, false)
		return
	}
	out := kapsorav1.PartyCatalogs{
		IdentifierTypes:   make([]kapsorav1.PartyCatalogEntry, 0, len(catalogs.IdentifierTypes)),
		RelationshipTypes: make([]kapsorav1.PartyCatalogEntry, 0, len(catalogs.RelationshipTypes)),
		MembershipTypes:   make([]kapsorav1.PartyCatalogEntry, 0, len(catalogs.MembershipTypes)),
	}
	for _, c := range catalogs.IdentifierTypes {
		entry := catalogEntry(c)
		sensitive, scope := c.IsSensitive, kapsorav1.PartyCatalogEntryUniquenessScope(c.UniquenessScope)
		entry.IsSensitive, entry.UniquenessScope = &sensitive, &scope
		out.IdentifierTypes = append(out.IdentifierTypes, entry)
	}
	for _, c := range catalogs.RelationshipTypes {
		entry := catalogEntry(c)
		directional := c.IsDirectional
		entry.IsDirectional = &directional
		out.RelationshipTypes = append(out.RelationshipTypes, entry)
	}
	for _, c := range catalogs.MembershipTypes {
		entry := catalogEntry(c)
		principal := c.RequiresPrincipal
		entry.RequiresPrincipal = &principal
		out.MembershipTypes = append(out.MembershipTypes, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

// decodePersonPatch reads a merge-patch body; an explicit null clears a nullable field.
func decodePersonPatch(raw map[string]json.RawMessage) (domain.PersonPatch, []domain.FieldError) {
	var patch domain.PersonPatch
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "firstName":
			patch.FirstName = decodeString(value, key, &fields)
		case "lastName":
			patch.LastName = decodeString(value, key, &fields)
		case "middleName":
			if isJSONNull(value) {
				patch.ClearMiddleName = true
			} else {
				patch.MiddleName = decodeString(value, key, &fields)
			}
		case "sexAtBirth":
			if isJSONNull(value) {
				patch.ClearSexAtBirth = true
			} else {
				patch.SexAtBirth = decodeString(value, key, &fields)
			}
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "birthDate":
			if isJSONNull(value) {
				patch.ClearBirthDate = true
			} else {
				patch.BirthDate = decodeDate(value, key, &fields)
			}
		case "identifiers":
			patch.Identifiers = decodeIdentifiers(value, key, &fields)
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	return patch, fields
}

func decodeIdentifiers(raw json.RawMessage, field string, fields *[]domain.FieldError) []domain.SubmittedIdentifier {
	var items []struct {
		Type    string  `json:"type"`
		Value   *string `json:"value"`
		Primary *bool   `json:"primary"`
		Remove  *bool   `json:"remove"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "dizi olmalı"})
		return nil
	}
	out := make([]domain.SubmittedIdentifier, 0, len(items))
	for _, it := range items {
		sub := domain.SubmittedIdentifier{
			Type: it.Type, Primary: it.Primary != nil && *it.Primary, Remove: it.Remove != nil && *it.Remove,
		}
		if it.Value != nil {
			sub.Value = *it.Value
		}
		out = append(out, sub)
	}
	return out
}

func decodeString(raw json.RawMessage, field string, fields *[]domain.FieldError) *string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "metin olmalı"})
		return nil
	}
	return &s
}

func decodeDate(raw json.RawMessage, field string, fields *[]domain.FieldError) *time.Time {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "TYPE", Message: "tarih metni olmalı"})
		return nil
	}
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		*fields = append(*fields, domain.FieldError{Field: field, Code: "FORMAT", Message: "YYYY-MM-DD biçiminde olmalı"})
		return nil
	}
	return &d
}

func personView(p application.Person) kapsorav1.Person {
	out := kapsorav1.Person{
		Id: p.ID, FirstName: p.FirstName, LastName: p.LastName, MiddleName: p.MiddleName,
		DisplayName: p.DisplayName, Status: kapsorav1.PersonStatus(p.Status), RowVersion: int(p.RowVersion),
	}
	if p.BirthDate != nil {
		out.BirthDate = &openapi_types.Date{Time: *p.BirthDate}
	}
	if p.SexAtBirth != nil {
		sex := kapsorav1.PersonSexAtBirth(*p.SexAtBirth)
		out.SexAtBirth = &sex
	}
	if p.MergedIntoID != nil {
		out.MergedIntoId = p.MergedIntoID
	}
	if p.MaskedPrimaryIdentifier != "" {
		masked := p.MaskedPrimaryIdentifier
		out.MaskedPrimaryIdentifier = &masked
	}
	identifiers := make([]kapsorav1.MaskedIdentifier, 0, len(p.Identifiers))
	for _, id := range p.Identifiers {
		identifiers = append(identifiers, kapsorav1.MaskedIdentifier{
			Type: id.Type, MaskedValue: id.MaskedValue, Primary: id.Primary,
		})
	}
	out.Identifiers = &identifiers
	return out
}

func summaryView(s application.PersonSummary) kapsorav1.PersonSummary {
	out := kapsorav1.PersonSummary{
		Id: s.ID, DisplayName: s.DisplayName, Status: kapsorav1.PersonSummaryStatus(s.Status),
	}
	if s.MaskedPrimaryIdentifier != "" {
		masked := s.MaskedPrimaryIdentifier
		out.MaskedPrimaryIdentifier = &masked
	}
	return out
}

func catalogEntry(c application.CatalogType) kapsorav1.PartyCatalogEntry {
	return kapsorav1.PartyCatalogEntry{
		Code: c.Code, DisplayName: c.DisplayName, Status: kapsorav1.PartyCatalogEntryStatus(c.Status),
	}
}
