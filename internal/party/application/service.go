package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// MaskedIdentifier is the only identifier shape that leaves the service.
type MaskedIdentifier struct {
	Type        string
	MaskedValue string
	Primary     bool
}

// Person is the full view returned by create, get and update.
type Person struct {
	ID                      uuid.UUID
	FirstName               string
	MiddleName              *string
	LastName                string
	DisplayName             string
	BirthDate               *time.Time
	SexAtBirth              *string
	Status                  string
	MergedIntoID            *uuid.UUID
	MaskedPrimaryIdentifier string
	RowVersion              int64
	Identifiers             []MaskedIdentifier
}

// PersonSummary is one list row.
type PersonSummary struct {
	ID                      uuid.UUID
	DisplayName             string
	Status                  string
	MaskedPrimaryIdentifier string
}

// Page is a list result.
type Page struct {
	Items      []PersonSummary
	NextCursor string
}

// ListFilter is the API-level list request.
type ListFilter struct {
	Query     string
	Status    string
	SponsorID uuid.UUID
	Cursor    string
	Limit     int
}

// Service implements the person use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	cipher  crypto.FieldCipher
	index   crypto.BlindIndexer
	audit   audit.Recorder
	cursors *httpx.CursorCodec
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool    *pgxpool.Pool
	Repo    Repository
	Cipher  crypto.FieldCipher
	Index   crypto.BlindIndexer
	Audit   audit.Recorder
	Cursors *httpx.CursorCodec
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Cipher == nil || d.Index == nil || d.Cursors == nil {
		return nil, errors.New("party: pool, repository, cipher, blind indexer and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	return &Service{pool: d.Pool, repo: d.Repo, cipher: d.Cipher, index: d.Index, audit: d.Audit, cursors: d.Cursors}, nil
}

// Create registers a person and its identifiers in one transaction with the audit row.
func (s *Service) Create(ctx context.Context, rc identity.RequestContext, in domain.NewPerson) (Person, error) {
	if err := domain.ValidateNew(&in, time.Now().UTC()); err != nil {
		return Person{}, err
	}
	var out Person
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		personID, err := s.repo.CreatePerson(ctx, tx, NewPersonRow{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
			FirstName: in.FirstName, MiddleName: optString(in.MiddleName), LastName: in.LastName,
			NormalizedName: domain.NormalizedName(in.FirstName, in.MiddleName, in.LastName),
			BirthDate:      in.BirthDate, SexAtBirth: optString(in.SexAtBirth),
		})
		if err != nil {
			return err
		}
		ve := &domain.ValidationError{}
		adds, _, err := s.planIdentifiers(ctx, tx, rc.TenantID, personID, in.Identifiers, ve)
		if err != nil {
			return err
		}
		if ve.Len() > 0 {
			return ve
		}
		for _, add := range adds {
			add.PersonID = personID
			if err := s.repo.AddIdentifier(ctx, tx, add); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, rc, "person.create", personID, map[string]any{"id_count": len(adds)}); err != nil {
			return err
		}
		out, err = s.load(ctx, tx, rc.TenantID, personID)
		return err
	})
	return out, err
}

// Get returns one person of the caller's tenant with masked identifiers.
func (s *Service) Get(ctx context.Context, rc identity.RequestContext, personID uuid.UUID) (Person, error) {
	var out Person
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.load(ctx, tx, rc.TenantID, personID)
		return err
	})
	return out, err
}

// Update applies a merge-patch under optimistic concurrency. Identifier changes go through
// the same validation as create: adding a type replaces the person's rows of that type.
func (s *Service) Update(ctx context.Context, rc identity.RequestContext, personID uuid.UUID, patch domain.PersonPatch) (Person, error) {
	if err := domain.ValidatePatch(&patch, time.Now().UTC()); err != nil {
		return Person{}, err
	}
	var out Person
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetPerson(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		if current.RowVersion != patch.ExpectedVersion {
			return ErrVersionMismatch
		}
		next := merge(current, patch)
		if err := s.repo.UpdatePerson(ctx, tx, rc.TenantID, personID, next, patch.ExpectedVersion); err != nil {
			return err
		}

		ve := &domain.ValidationError{}
		adds, removes, err := s.planIdentifiers(ctx, tx, rc.TenantID, personID, patch.Identifiers, ve)
		if err != nil {
			return err
		}
		if ve.Len() > 0 {
			return ve
		}
		removed := int64(0)
		for _, typeCode := range removes {
			n, err := s.repo.RemoveIdentifiers(ctx, tx, rc.TenantID, personID, typeCode)
			if err != nil {
				return err
			}
			removed += n
		}
		for _, add := range adds {
			if err := s.repo.AddIdentifier(ctx, tx, add); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, rc, "person.update", personID, map[string]any{"id_added": len(adds), "id_removed": removed}); err != nil {
			return err
		}
		if len(adds) > 0 {
			if err := s.record(ctx, tx, rc, "person.identifier.add", personID, map[string]any{"id_count": len(adds)}); err != nil {
				return err
			}
		}
		if removed > 0 {
			if err := s.record(ctx, tx, rc, "person.identifier.remove", personID, map[string]any{"id_count": removed}); err != nil {
				return err
			}
		}
		out, err = s.load(ctx, tx, rc.TenantID, personID)
		return err
	})
	return out, err
}

// List returns one page ordered by creation time, newest first.
func (s *Service) List(ctx context.Context, rc identity.RequestContext, f ListFilter) (Page, error) {
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(domain.PersonStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	if n := len([]rune(f.Query)); f.Query != "" && (n < 2 || n > domain.MaxQueryRunes) {
		ve.Add("q", "LENGTH", fmt.Sprintf("2-%d karakter olmalı", domain.MaxQueryRunes))
	}
	if err := ve.OrNil(); err != nil {
		return Page{}, err
	}
	cursor, hasCursor, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return Page{}, err
	}
	pageSize := httpx.ClampLimit(f.Limit)
	q := ListQuery{Status: f.Status, SponsorID: f.SponsorID, PageSize: pageSize + 1}
	if f.Query != "" {
		q.Pattern = domain.SearchPattern(f.Query)
	}
	if hasCursor {
		q.After = &cursor
	}

	var rows []Summary
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListPeople(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: make([]PersonSummary, 0, len(rows))}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		rows = rows[:pageSize]
	}
	for _, r := range rows {
		page.Items = append(page.Items, summaryView(r))
	}
	return page, nil
}

// load reads a person and its identifiers into the response model.
func (s *Service) load(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (Person, error) {
	row, err := s.repo.GetPerson(ctx, tx, tenantID, personID)
	if err != nil {
		return Person{}, err
	}
	ids, err := s.repo.ListIdentifiers(ctx, tx, tenantID, personID)
	if err != nil {
		return Person{}, err
	}
	out := Person{
		ID: row.ID, FirstName: row.FirstName, MiddleName: row.MiddleName, LastName: row.LastName,
		DisplayName: domain.DisplayName(row.FirstName, deref(row.MiddleName), row.LastName),
		BirthDate:   row.BirthDate, SexAtBirth: row.SexAtBirth, Status: row.Status,
		MergedIntoID: row.MergedIntoID, RowVersion: row.RowVersion,
		Identifiers: make([]MaskedIdentifier, 0, len(ids)),
	}
	for _, id := range ids {
		masked, err := s.maskOf(ctx, tenantID, id)
		if err != nil {
			return Person{}, err
		}
		if id.Primary && out.MaskedPrimaryIdentifier == "" {
			out.MaskedPrimaryIdentifier = masked
		}
		out.Identifiers = append(out.Identifiers, MaskedIdentifier{Type: id.Type, MaskedValue: masked, Primary: id.Primary})
	}
	return out, nil
}

// maskOf returns the stored mask, decrypting only when an older row has none.
func (s *Service) maskOf(ctx context.Context, tenantID uuid.UUID, id StoredIdentifier) (string, error) {
	if id.MaskedValue != "" {
		return id.MaskedValue, nil
	}
	plain, err := s.cipher.Decrypt(ctx, tenantID, crypto.PurposePersonIdentifier, id.Cipher)
	if err != nil {
		return "", fmt.Errorf("party: decrypt identifier for masking: %w", err)
	}
	return domain.MaskIdentifier(id.Type, string(plain)), nil
}

// record writes one business audit row; detail carries ids, codes and counts only.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, action string, resourceID uuid.UUID, detail map[string]any) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
		Category: audit.CategoryBusiness, ActionCode: action,
		ResourceType: "person", ResourceID: nullUUID(resourceID), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

func merge(current PersonRow, patch domain.PersonPatch) PersonUpdateRow {
	next := PersonUpdateRow{
		FirstName: current.FirstName, MiddleName: current.MiddleName, LastName: current.LastName,
		BirthDate: current.BirthDate, SexAtBirth: current.SexAtBirth, Status: current.Status,
	}
	if patch.FirstName != nil {
		next.FirstName = *patch.FirstName
	}
	switch {
	case patch.ClearMiddleName:
		next.MiddleName = nil
	case patch.MiddleName != nil:
		next.MiddleName = optString(*patch.MiddleName)
	}
	if patch.LastName != nil {
		next.LastName = *patch.LastName
	}
	switch {
	case patch.ClearBirthDate:
		next.BirthDate = nil
	case patch.BirthDate != nil:
		next.BirthDate = patch.BirthDate
	}
	switch {
	case patch.ClearSexAtBirth:
		next.SexAtBirth = nil
	case patch.SexAtBirth != nil:
		next.SexAtBirth = patch.SexAtBirth
	}
	if patch.Status != nil {
		next.Status = *patch.Status
	}
	next.NormalizedName = domain.NormalizedName(next.FirstName, deref(next.MiddleName), next.LastName)
	return next
}

func summaryView(r Summary) PersonSummary {
	return PersonSummary{
		ID: r.ID, Status: r.Status, MaskedPrimaryIdentifier: r.MaskedPrimaryIdentifier,
		DisplayName: domain.DisplayName(r.FirstName, deref(r.MiddleName), r.LastName),
	}
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
