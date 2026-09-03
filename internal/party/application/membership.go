package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// Membership is one sponsor membership of a person.
type Membership struct {
	ID                    uuid.UUID
	PersonID              uuid.UUID
	SponsorOrganizationID uuid.UUID
	SponsorDisplayName    string
	MembershipType        string
	PrincipalMembershipID *uuid.UUID
	ExternalMemberNo      *string
	Status                string
	ValidFrom             time.Time
	ValidTo               *time.Time
	SourceSystem          *string
	RowVersion            int64
}

// NewMembershipInput is the create command.
type NewMembershipInput struct {
	SponsorOrganizationID uuid.UUID
	MembershipType        string
	PrincipalMembershipID *uuid.UUID
	ExternalMemberNo      string
	Status                string
	ValidFrom             time.Time
	ValidTo               *time.Time
}

// MembershipPatch is a merge-patch of an existing membership.
type MembershipPatch struct {
	Status                *string
	ValidTo               *time.Time
	ClearValidTo          bool
	ExternalMemberNo      *string
	ClearExternalMemberNo bool
	ExpectedVersion       int64
}

// ListMemberships returns the person's sponsor memberships, newest first.
func (s *Service) ListMemberships(ctx context.Context, rc identity.RequestContext, personID uuid.UUID) ([]Membership, error) {
	var out []Membership
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		rows, err := s.repo.ListMemberships(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out = make([]Membership, 0, len(rows))
		for _, r := range rows {
			out = append(out, membershipView(r))
		}
		return nil
	})
	return out, err
}

// CreateMembership adds a membership under a sponsor or payer organization of the tenant.
func (s *Service) CreateMembership(ctx context.Context, rc identity.RequestContext, personID uuid.UUID, in NewMembershipInput) (Membership, error) {
	if in.Status == "" {
		in.Status = domain.PersonActive
	}
	ve := &domain.ValidationError{}
	if !domain.ValidTypeCode(in.MembershipType) {
		ve.Add("membershipType", "MEMBERSHIP_TYPE_UNKNOWN", "geçersiz üyelik türü")
	}
	if !domain.Contains(domain.MembershipCreate, in.Status) {
		ve.Add("status", "ENUM", "PENDING veya ACTIVE olmalı")
	}
	if err := domain.ValidatePeriod("valid", in.ValidFrom, in.ValidTo); err != nil {
		var pe *domain.ValidationError
		if errors.As(err, &pe) {
			ve.Fields = append(ve.Fields, pe.Fields...)
		}
	}
	if err := ve.OrNil(); err != nil {
		return Membership{}, err
	}

	var out Membership
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		sponsor, err := s.repo.GetSponsorOrganization(ctx, tx, rc.TenantID, in.SponsorOrganizationID)
		switch {
		case errors.Is(err, ErrNotFound):
			ve.Add("sponsorOrganizationId", domain.CodeSponsorOrganizationBad, "sponsor kurum bulunamadı")
		case err != nil:
			return err
		case !domain.Contains(domain.SponsorRoles, sponsor.Role):
			ve.Add("sponsorOrganizationId", domain.CodeSponsorOrganizationBad, "kurum SPONSOR veya PAYER rolünde olmalı")
		}
		catalog, err := s.repo.GetMembershipType(ctx, tx, rc.TenantID, in.MembershipType)
		switch {
		case errors.Is(err, ErrCatalogEntryNotFound):
			ve.Add("membershipType", "MEMBERSHIP_TYPE_UNKNOWN", "üyelik türü tanımlı değil")
		case err != nil:
			return err
		case catalog.Status != domain.StatusActive:
			ve.Add("membershipType", "MEMBERSHIP_TYPE_UNKNOWN", "üyelik türü kullanım dışı")
		case catalog.RequiresPrincipal && in.PrincipalMembershipID == nil:
			ve.Add("principalMembershipId", domain.CodePrincipalRequired, "bu üyelik türü için asıl üyelik zorunlu")
		}
		if in.PrincipalMembershipID != nil {
			if _, err := s.repo.GetMembership(ctx, tx, rc.TenantID, *in.PrincipalMembershipID); err != nil {
				if !errors.Is(err, ErrNotFound) {
					return err
				}
				ve.Add("principalMembershipId", "UNKNOWN", "asıl üyelik bulunamadı")
			}
		}
		if ve.Len() > 0 {
			return ve
		}

		membershipID, err := s.repo.CreateMembership(ctx, tx, NewMembershipRow{
			TenantID: rc.TenantID, PersonID: personID, SponsorOrganizationID: in.SponsorOrganizationID,
			PrincipalMembershipID: in.PrincipalMembershipID, MembershipType: in.MembershipType,
			ExternalMemberNo: optString(domain.NormalizeIdentifier(in.ExternalMemberNo)), Status: in.Status,
			ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
		})
		if err != nil {
			return err
		}
		if err := s.recordMembership(ctx, tx, rc, "membership.create", membershipID, map[string]any{
			"person_id": personID, "sponsor_organization_id": in.SponsorOrganizationID, "membership_type": in.MembershipType,
		}); err != nil {
			return err
		}
		row, err := s.repo.GetMembership(ctx, tx, rc.TenantID, membershipID)
		if err != nil {
			return err
		}
		out = membershipView(row)
		return nil
	})
	return out, err
}

// UpdateMembership applies a merge-patch of status, validity end and member number.
func (s *Service) UpdateMembership(ctx context.Context, rc identity.RequestContext, personID, membershipID uuid.UUID, patch MembershipPatch) (Membership, error) {
	ve := &domain.ValidationError{}
	if patch.Status != nil && !domain.Contains(domain.MembershipUpdates, *patch.Status) {
		ve.Add("status", "ENUM", "ACTIVE, SUSPENDED veya ENDED olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return Membership{}, err
	}

	var out Membership
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetMembership(ctx, tx, rc.TenantID, membershipID)
		if err != nil {
			return err
		}
		if current.PersonID != personID {
			return ErrNotFound
		}
		if current.RowVersion != patch.ExpectedVersion {
			return ErrVersionMismatch
		}
		next := MembershipUpdateRow{
			Status: current.Status, ExternalMemberNo: current.ExternalMemberNo,
			ValidTo: current.ValidTo, Expected: patch.ExpectedVersion,
		}
		if patch.Status != nil {
			next.Status = *patch.Status
		}
		switch {
		case patch.ClearValidTo:
			next.ValidTo = nil
		case patch.ValidTo != nil:
			next.ValidTo = patch.ValidTo
		}
		switch {
		case patch.ClearExternalMemberNo:
			next.ExternalMemberNo = nil
		case patch.ExternalMemberNo != nil:
			next.ExternalMemberNo = optString(domain.NormalizeIdentifier(*patch.ExternalMemberNo))
		}
		if next.ValidTo != nil && !next.ValidTo.After(current.ValidFrom) {
			ve.Add("validTo", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
			return ve
		}
		if err := s.repo.UpdateMembership(ctx, tx, rc.TenantID, membershipID, next); err != nil {
			return err
		}
		if err := s.recordMembership(ctx, tx, rc, "membership.update", membershipID, map[string]any{
			"person_id": personID, "membership_status": next.Status,
		}); err != nil {
			return err
		}
		row, err := s.repo.GetMembership(ctx, tx, rc.TenantID, membershipID)
		if err != nil {
			return err
		}
		out = membershipView(row)
		return nil
	})
	return out, err
}

func membershipView(r MembershipRow) Membership {
	return Membership{
		ID: r.ID, PersonID: r.PersonID, SponsorOrganizationID: r.SponsorOrganizationID,
		SponsorDisplayName: r.SponsorDisplayName, MembershipType: r.MembershipType,
		PrincipalMembershipID: r.PrincipalMembershipID, ExternalMemberNo: r.ExternalMemberNo,
		Status: r.Status, ValidFrom: r.ValidFrom, ValidTo: r.ValidTo,
		SourceSystem: r.SourceSystem, RowVersion: r.RowVersion,
	}
}

func (s *Service) recordMembership(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, action string, id uuid.UUID, detail map[string]any) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
		Category: audit.CategoryBusiness, ActionCode: action,
		ResourceType: "sponsor_membership", ResourceID: nullUUID(id), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}
