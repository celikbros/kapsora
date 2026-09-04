package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/provider/domain"
)

// PractitionerView is a practitioner together with the locations it is assigned to.
type PractitionerView struct {
	PractitionerRecord
	Locations []AssignmentRecord
}

// RegistrationSearch is the body of POST /api/v1/practitioners/search-by-registration.
type RegistrationSearch struct {
	Authority string
	Number    string
}

// CreatePractitioner registers a practitioner at a provider. The registration number is
// normalized, blind-indexed and encrypted here; only the envelope, the index and the mask
// travel further, so the plaintext exists in this function and nowhere else.
func (s *Service) CreatePractitioner(ctx context.Context, rc identity.RequestContext, providerID uuid.UUID, in domain.NewPractitioner) (PractitionerView, error) {
	if err := domain.ValidateNewPractitioner(in); err != nil {
		return PractitionerView{}, err
	}
	var personID *uuid.UUID
	if in.PersonID != "" {
		parsed, err := uuid.Parse(in.PersonID)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add("personId", "FORMAT", "geçerli bir kimlik olmalı")
			return PractitionerView{}, ve
		}
		personID = &parsed
	}

	normalized := domain.NormalizeRegistrationNumber(in.RegistrationNumber)
	hash, err := s.index.TenantIndex(ctx, rc.TenantID, crypto.PurposePractitionerRegistration,
		domain.RegistrationIndexInput(in.RegistrationAuthority, normalized))
	if err != nil {
		return PractitionerView{}, fmt.Errorf("provider: blind index: %w", err)
	}
	cipher, err := s.cipher.Encrypt(ctx, rc.TenantID, crypto.PurposePractitionerRegistration, []byte(normalized))
	if err != nil {
		return PractitionerView{}, fmt.Errorf("provider: encrypt registration number: %w", err)
	}

	var out PractitionerView
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		if _, err := s.repo.GetProvider(ctx, tx, rc.TenantID, scope, providerID); err != nil {
			return err
		}
		if personID != nil {
			exists, err := s.repo.PersonExists(ctx, tx, rc.TenantID, *personID)
			if err != nil {
				return err
			}
			if !exists {
				ve := &domain.ValidationError{}
				ve.Add("personId", "NOT_FOUND", "hak sahibi bulunamadı")
				return ve
			}
		}
		id, err := s.repo.CreatePractitioner(ctx, tx, rc.TenantID, NewPractitionerRow{
			ProviderID: providerID, PersonID: personID, FullName: in.FullName,
			Title: optional(in.Title), BranchCode: optional(in.BranchCode),
			RegistrationAuthority: in.RegistrationAuthority, Cipher: cipher, Hash: hash,
			Masked:    domain.MaskRegistrationNumber(normalized),
			ValidFrom: dayPtr(in.ValidFrom), ValidTo: dayPtr(in.ValidTo),
		})
		if err != nil {
			return err
		}
		// The detail names the issuing body and never the number, so a create is auditable
		// without the audit becoming a second copy of the identifier.
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.practitioner.create", "practitioner", id, map[string]any{
			"provider_id": providerID, "registration_authority": in.RegistrationAuthority,
			"branch_code": in.BranchCode,
		})); err != nil {
			return err
		}
		out, err = s.loadPractitioner(ctx, tx, rc, scope, id)
		return err
	})
	return out, err
}

// GetPractitioner returns one practitioner with its location assignments.
func (s *Service) GetPractitioner(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (PractitionerView, error) {
	var out PractitionerView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.loadPractitioner(ctx, tx, rc, scopeOf(rc), id)
		return err
	})
	return out, err
}

// ListPractitioners returns one page of a provider's practitioners, newest first. The page
// carries no assignments: a list of a hundred practitioners would otherwise cost a hundred
// extra queries for a panel the caller has not opened.
func (s *Service) ListPractitioners(ctx context.Context, rc identity.RequestContext, providerID uuid.UUID, f ListFilter) (PractitionerPage, error) {
	if err := validateListFilter(f); err != nil {
		return PractitionerPage{}, err
	}
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(domain.PractitionerStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz uygulayıcı durumu")
	}
	if err := ve.OrNil(); err != nil {
		return PractitionerPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return PractitionerPage{}, err
	}
	q := PractitionerQuery{
		ProviderID: providerID, Status: f.Status, BranchCode: f.BranchCode,
		Query: domain.LikePattern(f.Query), After: after, PageSize: pageSize + 1,
	}

	var rows []PractitionerRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		if _, err := s.repo.GetProvider(ctx, tx, rc.TenantID, scope, providerID); err != nil {
			return err
		}
		rows, err = s.repo.ListPractitioners(ctx, tx, rc.TenantID, scope, q)
		return err
	})
	if err != nil {
		return PractitionerPage{}, err
	}
	page := PractitionerPage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = s.nextCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// UpdatePractitioner applies a merge-patch under optimistic concurrency. The registration
// columns are never part of it.
func (s *Service) UpdatePractitioner(ctx context.Context, rc identity.RequestContext, id uuid.UUID, p domain.PractitionerPatch) (PractitionerView, error) {
	if err := domain.ValidatePractitionerPatch(p); err != nil {
		return PractitionerView{}, err
	}
	var newPerson *uuid.UUID
	if p.PersonID != nil {
		parsed, err := uuid.Parse(*p.PersonID)
		if err != nil {
			ve := &domain.ValidationError{}
			ve.Add("personId", "FORMAT", "geçerli bir kimlik olmalı")
			return PractitionerView{}, ve
		}
		newPerson = &parsed
	}

	var out PractitionerView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		current, err := s.repo.GetPractitioner(ctx, tx, rc.TenantID, scope, id)
		if err != nil {
			return err
		}
		if current.RowVersion != p.ExpectedVersion {
			return ErrVersionMismatch
		}

		next := PractitionerUpdateRow{
			PersonID: current.PersonID, FullName: current.FullName, Title: current.Title,
			BranchCode: current.BranchCode, ValidFrom: current.ValidFrom, ValidTo: current.ValidTo,
			Status: current.Status,
		}
		var fields []string
		if p.FullName != nil && *p.FullName != current.FullName {
			next.FullName = *p.FullName
			fields = append(fields, "fullName")
		}
		next.Title = mergeText(p.Title, p.ClearTitle, current.Title, "title", &fields)
		next.BranchCode = mergeText(p.BranchCode, p.ClearBranchCode, current.BranchCode, "branchCode", &fields)
		switch {
		case p.ClearPersonID:
			if current.PersonID != nil {
				fields = append(fields, "personId")
			}
			next.PersonID = nil
		case newPerson != nil:
			exists, err := s.repo.PersonExists(ctx, tx, rc.TenantID, *newPerson)
			if err != nil {
				return err
			}
			if !exists {
				ve := &domain.ValidationError{}
				ve.Add("personId", "NOT_FOUND", "hak sahibi bulunamadı")
				return ve
			}
			if current.PersonID == nil || *current.PersonID != *newPerson {
				fields = append(fields, "personId")
			}
			next.PersonID = newPerson
		}
		next.ValidFrom = mergeDate(p.ValidFrom, p.ClearValidFrom, current.ValidFrom, "validFrom", &fields)
		next.ValidTo = mergeDate(p.ValidTo, p.ClearValidTo, current.ValidTo, "validTo", &fields)
		if p.Status != nil && *p.Status != current.Status {
			next.Status = *p.Status
			fields = append(fields, "status")
		}
		if err := domain.ValidatePractitionerPeriod(next.ValidFrom, next.ValidTo); err != nil {
			return err
		}

		if err := s.repo.UpdatePractitioner(ctx, tx, rc.TenantID, scope, id, next, p.ExpectedVersion); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.practitioner.update", "practitioner", id, map[string]any{
			"provider_id": current.ProviderID, "changed_count": len(fields), "changed_fields": changed(fields),
		})); err != nil {
			return err
		}
		out, err = s.loadPractitioner(ctx, tx, rc, scope, id)
		return err
	})
	return out, err
}

// ReplaceAssignments swaps the whole set of locations a practitioner works at, under the
// practitioner's own optimistic-concurrency token. Every location must belong to the
// practitioner's own provider; a foreign one is a field error on the array rather than a
// 404 naming it, so the set of another provider's locations stays unknowable.
func (s *Service) ReplaceAssignments(ctx context.Context, rc identity.RequestContext, practitionerID uuid.UUID,
	items []domain.AssignmentInput, expected int64,
) (AssignmentResult, error) {
	if err := domain.ValidateAssignmentSet(items); err != nil {
		return AssignmentResult{}, err
	}
	rows := make([]AssignmentRow, 0, len(items))
	locationIDs := make([]uuid.UUID, 0, len(items))
	for i, it := range items {
		id, err := uuid.Parse(it.LocationID)
		if err != nil {
			return AssignmentResult{}, itemFieldError(i, "locationId", "FORMAT", "geçerli bir kimlik olmalı")
		}
		locationIDs = append(locationIDs, id)
		rows = append(rows, AssignmentRow{
			LocationID: id, Role: it.Role, ValidFrom: domain.DateOnly(it.ValidFrom), ValidTo: dayPtr(it.ValidTo),
		})
	}

	var out AssignmentResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		current, err := s.repo.GetPractitioner(ctx, tx, rc.TenantID, scope, practitionerID)
		if err != nil {
			return err
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		if len(locationIDs) > 0 {
			outside, err := s.repo.CountLocationsOutsideProvider(ctx, tx, rc.TenantID, current.ProviderID, locationIDs)
			if err != nil {
				return err
			}
			if outside > 0 {
				ve := &domain.ValidationError{}
				ve.Add("items", "NOT_FOUND", "lokasyon bu sağlayıcıya ait değil")
				return ve
			}
		}
		if err := s.repo.ReplaceAssignments(ctx, tx, rc.TenantID, practitionerID, rows); err != nil {
			return err
		}
		// The assignments are children, so the practitioner is touched explicitly to move
		// the ETag the caller holds.
		if err := s.repo.TouchPractitioner(ctx, tx, rc.TenantID, practitionerID, expected); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, s.event(rc, "provider.practitioner.assignments", "practitioner", practitionerID, map[string]any{
			"provider_id": current.ProviderID, "assignment_count": len(rows),
		})); err != nil {
			return err
		}
		updated, err := s.repo.GetPractitioner(ctx, tx, rc.TenantID, scope, practitionerID)
		if err != nil {
			return err
		}
		out.RowVersion = updated.RowVersion
		out.Items, err = s.repo.ListAssignments(ctx, tx, rc.TenantID, practitionerID)
		return err
	})
	if err != nil {
		return AssignmentResult{}, err
	}
	return out, nil
}

// SearchByRegistration finds the single practitioner carrying a registration number through
// the tenant-salted blind index. The caller must hold provider.practitioner.manage with a
// valid step-up; every call is written to the access audit with the issuing authority only,
// never the number.
func (s *Service) SearchByRegistration(ctx context.Context, rc identity.RequestContext, in RegistrationSearch) (PractitionerView, error) {
	ve := &domain.ValidationError{}
	if !domain.Contains(domain.RegistrationAuthorities, in.Authority) {
		ve.Add("registrationAuthority", "ENUM", "geçersiz kayıt otoritesi")
	}
	normalized := domain.NormalizeRegistrationNumber(in.Number)
	if err := domain.ValidateRegistrationNumber(normalized); err != nil {
		ve.Add("registrationNumber", "FORMAT", "sicil numarası doğrulanamadı")
	}
	if err := ve.OrNil(); err != nil {
		return PractitionerView{}, err
	}
	hash, err := s.index.TenantIndex(ctx, rc.TenantID, crypto.PurposePractitionerRegistration,
		domain.RegistrationIndexInput(in.Authority, normalized))
	if err != nil {
		return PractitionerView{}, fmt.Errorf("provider: blind index: %w", err)
	}

	var out PractitionerView
	var missing bool
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		id, found, err := s.repo.FindPractitionerByRegistration(ctx, tx, rc.TenantID, scope, in.Authority, hash)
		if err != nil {
			return err
		}
		if err := s.recordRegistrationSearch(ctx, tx, rc, in.Authority, id, found); err != nil {
			return err
		}
		if !found {
			// The access event of a miss has to commit too, so the miss is reported after
			// the transaction rather than by rolling it back.
			missing = true
			return nil
		}
		out, err = s.loadPractitioner(ctx, tx, rc, scope, id)
		return err
	})
	if err != nil {
		return PractitionerView{}, err
	}
	if missing {
		return PractitionerView{}, ErrPractitionerNotFound
	}
	return out, nil
}

// recordRegistrationSearch writes the SENSITIVE access event. It carries the issuing body
// and never the number, so a search is auditable without becoming a second copy of the
// identifier.
func (s *Service) recordRegistrationSearch(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	authority string, practitionerID uuid.UUID, found bool,
) error {
	ev := audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, MembershipID: nullUUID(rc.MembershipID),
		ResourceType: "practitioner_registration", AccessType: audit.AccessSearch,
		Classification: audit.ClassPersonal, PurposeCode: "PRACTITIONER_LOOKUP",
		ReasonText: "registration_authority=" + authority, Outcome: audit.OutcomeSuccess,
	}
	if found {
		ev.ResourceID = nullUUID(practitionerID)
	}
	return s.audit.RecordAccess(ctx, tx, ev)
}

// loadPractitioner reads a practitioner and its assignments inside the caller's transaction.
func (s *Service) loadPractitioner(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, scope Scope, id uuid.UUID) (PractitionerView, error) {
	record, err := s.repo.GetPractitioner(ctx, tx, rc.TenantID, scope, id)
	if err != nil {
		return PractitionerView{}, err
	}
	assignments, err := s.repo.ListAssignments(ctx, tx, rc.TenantID, id)
	if err != nil {
		return PractitionerView{}, err
	}
	return PractitionerView{PractitionerRecord: record, Locations: assignments}, nil
}

// mergeDate applies one nullable date field of a merge-patch and records whether it moved.
func mergeDate(value *time.Time, clear bool, current *time.Time, field string, fields *[]string) *time.Time {
	switch {
	case clear:
		if current != nil {
			*fields = append(*fields, field)
		}
		return nil
	case value != nil:
		next := dayPtr(value)
		if !sameDate(next, current) {
			*fields = append(*fields, field)
		}
		return next
	default:
		return current
	}
}
