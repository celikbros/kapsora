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
	"github.com/celikbros/kapsora/internal/organization/domain"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Organization is the full view returned by create, get and update.
type Organization struct {
	ID                 uuid.UUID
	OrganizationID     uuid.UUID
	LegalName          string
	DisplayName        string
	OrganizationKind   string
	CountryCode        string
	OrganizationStatus string
	RelationshipRole   string
	RelationshipStatus string
	TenantCode         *string
	ValidFrom          time.Time
	ValidTo            *time.Time
	RowVersion         int64
	Identifiers        []MaskedIdentifier
}

// MaskedIdentifier is what responses and audit may carry.
type MaskedIdentifier struct {
	Type        domain.IdentifierType
	MaskedValue string
	Primary     bool
}

// ListFilter is the API-level list request.
type ListFilter struct {
	Role   string
	Query  string
	Cursor string
	Limit  int
}

// Page is a list result.
type Page struct {
	Items      []Summary
	NextCursor string
}

// UpdateInput is a merge-patch. Nil pointers leave a field unchanged; ClearTenantCode
// represents an explicit JSON null.
type UpdateInput struct {
	DisplayName        *string
	RelationshipStatus *string
	TenantCode         *string
	ClearTenantCode    bool
	ExpectedVersion    int64
}

// Service implements the use cases.
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
		return nil, errors.New("organization: pool, repository, cipher, blind indexer and cursor codec are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	return &Service{pool: d.Pool, repo: d.Repo, cipher: d.Cipher, index: d.Index, audit: d.Audit, cursors: d.Cursors}, nil
}

// Create validates the command, finds or creates the global legal entity by tax-number
// blind index, and records the tenant relationship. Everything commits together with the
// audit row.
func (s *Service) Create(ctx context.Context, rc identity.RequestContext, in domain.NewOrganization) (Organization, error) {
	if err := domain.ValidateNew(&in); err != nil {
		return Organization{}, err
	}

	var taxCipher, taxHash []byte
	if tax, ok := in.TaxNumber(); ok {
		var err error
		if taxHash, err = s.index.GlobalIndex(ctx, crypto.PurposeOrganizationTax, tax.Value); err != nil {
			return Organization{}, fmt.Errorf("organization: index tax number: %w", err)
		}
		if taxCipher, err = s.cipher.Encrypt(ctx, uuid.Nil, crypto.PurposeOrganizationTax, []byte(tax.Value)); err != nil {
			return Organization{}, fmt.Errorf("organization: encrypt tax number: %w", err)
		}
	}

	var out Organization
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		orgID, reused, err := s.findOrCreateGlobal(ctx, tx, in, taxCipher, taxHash)
		if err != nil {
			return err
		}
		var tenantCode *string
		if in.TenantCode != "" {
			tenantCode = &in.TenantCode
		}
		relID, err := s.repo.CreateRelationship(ctx, tx, NewRelationship{
			TenantID: rc.TenantID, OrganizationID: orgID, RelationshipRole: in.RelationshipRole, TenantCode: tenantCode,
		})
		if err != nil {
			return err
		}
		for _, id := range in.OtherIdentifiers() {
			if err := s.repo.AddIdentifier(ctx, tx, orgID, id, in.CountryCode); err != nil {
				return err
			}
		}
		if err := s.audit.Record(ctx, tx, audit.Event{
			TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
			Category: audit.CategoryBusiness, ActionCode: "organization.create",
			ResourceType: "organization_relationship", ResourceID: nullUUID(relID), Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{
				"organization_id": orgID, "relationship_role": in.RelationshipRole,
				"country_code": in.CountryCode, "global_reused": reused,
			},
		}); err != nil {
			return err
		}
		rec, err := s.repo.Get(ctx, tx, rc.TenantID, relID)
		if err != nil {
			return err
		}
		out, err = s.view(ctx, rec)
		return err
	})
	return out, err
}

func (s *Service) findOrCreateGlobal(ctx context.Context, tx pgx.Tx, in domain.NewOrganization, taxCipher, taxHash []byte) (uuid.UUID, bool, error) {
	if taxHash != nil {
		id, found, err := s.repo.FindGlobalByTaxHash(ctx, tx, in.CountryCode, taxHash)
		if err != nil {
			return uuid.Nil, false, err
		}
		if found {
			return id, true, nil
		}
	}
	id, err := s.repo.CreateGlobal(ctx, tx, NewGlobal{
		LegalName: in.LegalName, DisplayName: in.DisplayName, OrganizationKind: in.OrganizationKind,
		CountryCode: in.CountryCode, TaxCipher: taxCipher, TaxHash: taxHash,
	})
	return id, false, err
}

// Get returns one relationship of the caller's tenant.
func (s *Service) Get(ctx context.Context, rc identity.RequestContext, relationshipID uuid.UUID) (Organization, error) {
	var out Organization
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		rec, err := s.repo.Get(ctx, tx, rc.TenantID, relationshipID)
		if err != nil {
			return err
		}
		out, err = s.view(ctx, rec)
		return err
	})
	return out, err
}

// List returns one page ordered by creation time, newest first.
func (s *Service) List(ctx context.Context, rc identity.RequestContext, f ListFilter) (Page, error) {
	ve := &domain.ValidationError{}
	if f.Role != "" && !containsString(domain.RelationshipRoles, f.Role) {
		ve.Fields = append(ve.Fields, domain.FieldError{Field: "role", Code: "ENUM", Message: "geçersiz ilişki rolü"})
	}
	if f.Query != "" && (len([]rune(f.Query)) < 2 || len([]rune(f.Query)) > 120) {
		ve.Fields = append(ve.Fields, domain.FieldError{Field: "q", Code: "LENGTH", Message: "2-120 karakter olmalı"})
	}
	if len(ve.Fields) > 0 {
		return Page{}, ve
	}
	cursor, hasCursor, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return Page{}, err
	}
	pageSize := httpx.ClampLimit(f.Limit)
	q := ListQuery{Role: f.Role, Query: f.Query, PageSize: pageSize + 1}
	if hasCursor {
		q.After = &cursor
	}

	var rows []Summary
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.List(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}

// Update applies a merge-patch under optimistic concurrency. Renaming the shared legal
// entity is allowed only while this tenant is its sole relationship holder.
func (s *Service) Update(ctx context.Context, rc identity.RequestContext, relationshipID uuid.UUID, in UpdateInput) (Organization, error) {
	if err := domain.ValidateUpdate(in.DisplayName, in.RelationshipStatus, in.TenantCode); err != nil {
		return Organization{}, err
	}
	var out Organization
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.Get(ctx, tx, rc.TenantID, relationshipID)
		if err != nil {
			return err
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		changed := []string{}

		if in.DisplayName != nil && *in.DisplayName != current.DisplayName {
			n, err := s.repo.RelationshipCount(ctx, tx, current.OrganizationID)
			if err != nil {
				return err
			}
			if n > 1 {
				return ErrSharedReadOnly
			}
			if err := s.repo.UpdateGlobalDisplayName(ctx, tx, current.OrganizationID, *in.DisplayName); err != nil {
				return err
			}
			changed = append(changed, "displayName")
		}

		status := current.RelationshipStatus
		if in.RelationshipStatus != nil && *in.RelationshipStatus != status {
			status = *in.RelationshipStatus
			changed = append(changed, "relationshipStatus")
		}
		tenantCode := current.TenantCode
		switch {
		case in.ClearTenantCode:
			if tenantCode != nil {
				changed = append(changed, "tenantCode")
			}
			tenantCode = nil
		case in.TenantCode != nil:
			if tenantCode == nil || *tenantCode != *in.TenantCode {
				changed = append(changed, "tenantCode")
			}
			tenantCode = in.TenantCode
		}

		if _, err := s.repo.UpdateRelationship(ctx, tx, rc.TenantID, relationshipID, status, tenantCode, in.ExpectedVersion); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Event{
			TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
			Category: audit.CategoryBusiness, ActionCode: "organization.update",
			ResourceType: "organization_relationship", ResourceID: nullUUID(relationshipID), Outcome: audit.OutcomeSuccess,
			Detail: map[string]any{"organization_id": current.OrganizationID, "changed_count": len(changed), "changed_fields": joinFields(changed)},
		}); err != nil {
			return err
		}
		rec, err := s.repo.Get(ctx, tx, rc.TenantID, relationshipID)
		if err != nil {
			return err
		}
		out, err = s.view(ctx, rec)
		return err
	})
	return out, err
}

// view builds the response model; the tax number is decrypted only to be masked.
func (s *Service) view(ctx context.Context, r Record) (Organization, error) {
	out := Organization{
		ID: r.ID, OrganizationID: r.OrganizationID, LegalName: r.LegalName, DisplayName: r.DisplayName,
		OrganizationKind: r.OrganizationKind, CountryCode: r.CountryCode, OrganizationStatus: r.OrganizationStatus,
		RelationshipRole: r.RelationshipRole, RelationshipStatus: r.RelationshipStatus, TenantCode: r.TenantCode,
		ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, RowVersion: r.RowVersion,
		Identifiers: make([]MaskedIdentifier, 0, len(r.Identifiers)+1),
	}
	if len(r.TaxCipher) > 0 {
		plain, err := s.cipher.Decrypt(ctx, uuid.Nil, crypto.PurposeOrganizationTax, r.TaxCipher)
		if err != nil {
			return Organization{}, fmt.Errorf("organization: decrypt tax number: %w", err)
		}
		t := domain.TaxNumberType(string(plain))
		out.Identifiers = append(out.Identifiers, MaskedIdentifier{Type: t, MaskedValue: domain.Mask(t, string(plain)), Primary: true})
	}
	for _, id := range r.Identifiers {
		out.Identifiers = append(out.Identifiers, MaskedIdentifier{Type: id.Type, MaskedValue: domain.Mask(id.Type, id.Value), Primary: id.Primary})
	}
	return out, nil
}

func tenantCtx(rc identity.RequestContext) db.TenantContext {
	return db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func joinFields(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ","
		}
		out += f
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
