package partypg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ListRelationships implements application.Repository.
func (Repository) ListRelationships(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]application.RelationshipRow, error) {
	rows, err := sqlcgen.New(tx).ListPersonRelationships(ctx, sqlcgen.ListPersonRelationshipsParams{
		TenantID: tenantID, SourcePersonID: personID,
	})
	if err != nil {
		return nil, fmt.Errorf("party: list relationships: %w", err)
	}
	out := make([]application.RelationshipRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.RelationshipRow{
			ID: r.ID, SourcePersonID: r.SourcePersonID, TargetPersonID: r.TargetPersonID,
			RelationshipType: r.RelationshipType, IsDirectional: r.IsDirectional, Status: r.Status,
			ValidFrom: dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			EndReasonCode: r.EndReasonCode, RowVersion: r.RowVersion,
			Other: application.Summary{
				ID: r.OtherPersonID, FirstName: r.OtherFirstName, MiddleName: r.OtherMiddleName,
				LastName: r.OtherLastName, Status: r.OtherStatus, MaskedPrimaryIdentifier: r.OtherMaskedIdentifier,
			},
		})
	}
	return out, nil
}

// GetRelationship implements application.Repository.
func (Repository) GetRelationship(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID) (application.RelationshipRow, error) {
	r, err := sqlcgen.New(tx).GetPersonRelationship(ctx, sqlcgen.GetPersonRelationshipParams{TenantID: tenantID, ID: relationshipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RelationshipRow{}, application.ErrNotFound
	}
	if err != nil {
		return application.RelationshipRow{}, fmt.Errorf("party: get relationship: %w", err)
	}
	return application.RelationshipRow{
		ID: r.ID, SourcePersonID: r.SourcePersonID, TargetPersonID: r.TargetPersonID,
		RelationshipType: r.RelationshipType, IsDirectional: r.IsDirectional, Status: r.Status,
		ValidFrom: dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		EndReasonCode: r.EndReasonCode, RowVersion: r.RowVersion,
	}, nil
}

// CreateRelationship implements application.Repository; the exclusion constraint on the
// validity period becomes ErrRelationshipOverlap.
func (Repository) CreateRelationship(ctx context.Context, tx pgx.Tx, in application.NewRelationshipRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreatePersonRelationship(ctx, sqlcgen.CreatePersonRelationshipParams{
		TenantID: in.TenantID, SourcePersonID: in.SourcePersonID, TargetPersonID: in.TargetPersonID,
		RelationshipType: in.RelationshipType, ValidFrom: date(&in.ValidFrom), ValidTo: date(in.ValidTo),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation {
			return uuid.Nil, application.ErrRelationshipOverlap
		}
		return uuid.Nil, fmt.Errorf("party: create relationship: %w", err)
	}
	return row.ID, nil
}

// EndRelationship implements application.Repository.
func (Repository) EndRelationship(ctx context.Context, tx pgx.Tx, tenantID, relationshipID uuid.UUID, in application.EndRelationshipRow) error {
	_, err := sqlcgen.New(tx).EndPersonRelationship(ctx, sqlcgen.EndPersonRelationshipParams{
		TenantID: tenantID, ID: relationshipID, RowVersion: in.Expected,
		EndsOn: date(&in.EndsOn), ReasonCode: &in.ReasonCode, ReasonText: in.ReasonText,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation {
			return application.ErrRelationshipOverlap
		}
		return fmt.Errorf("party: end relationship: %w", err)
	}
	return nil
}

// GetSponsorOrganization implements application.Repository.
func (Repository) GetSponsorOrganization(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (application.SponsorOrganization, error) {
	row, err := sqlcgen.New(tx).GetSponsorOrganization(ctx, sqlcgen.GetSponsorOrganizationParams{TenantID: tenantID, ID: organizationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SponsorOrganization{}, application.ErrNotFound
	}
	if err != nil {
		return application.SponsorOrganization{}, fmt.Errorf("party: sponsor organization: %w", err)
	}
	return application.SponsorOrganization{
		ID: row.ID, Role: row.RelationshipRole, Status: row.Status, DisplayName: row.DisplayName,
	}, nil
}

// ListMemberships implements application.Repository.
func (Repository) ListMemberships(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]application.MembershipRow, error) {
	rows, err := sqlcgen.New(tx).ListSponsorMemberships(ctx, sqlcgen.ListSponsorMembershipsParams{TenantID: tenantID, PersonID: personID})
	if err != nil {
		return nil, fmt.Errorf("party: list memberships: %w", err)
	}
	out := make([]application.MembershipRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.MembershipRow{
			ID: r.ID, PersonID: r.PersonID, SponsorOrganizationID: r.SponsorTenantOrganizationID,
			SponsorDisplayName: r.SponsorDisplayName, PrincipalMembershipID: uuidPtr(r.PrincipalMembershipID),
			MembershipType: r.MembershipType, ExternalMemberNo: r.ExternalMemberNo, Status: r.Status,
			ValidFrom: dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			SourceSystem: r.SourceSystem, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// GetMembership implements application.Repository.
func (Repository) GetMembership(ctx context.Context, tx pgx.Tx, tenantID, membershipID uuid.UUID) (application.MembershipRow, error) {
	r, err := sqlcgen.New(tx).GetSponsorMembership(ctx, sqlcgen.GetSponsorMembershipParams{TenantID: tenantID, ID: membershipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MembershipRow{}, application.ErrNotFound
	}
	if err != nil {
		return application.MembershipRow{}, fmt.Errorf("party: get membership: %w", err)
	}
	return application.MembershipRow{
		ID: r.ID, PersonID: r.PersonID, SponsorOrganizationID: r.SponsorTenantOrganizationID,
		SponsorDisplayName: r.SponsorDisplayName, PrincipalMembershipID: uuidPtr(r.PrincipalMembershipID),
		MembershipType: r.MembershipType, ExternalMemberNo: r.ExternalMemberNo, Status: r.Status,
		ValidFrom: dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		SourceSystem: r.SourceSystem, RowVersion: r.RowVersion,
	}, nil
}

// CreateMembership implements application.Repository.
func (Repository) CreateMembership(ctx context.Context, tx pgx.Tx, in application.NewMembershipRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateSponsorMembership(ctx, sqlcgen.CreateSponsorMembershipParams{
		TenantID: in.TenantID, PersonID: in.PersonID, SponsorTenantOrganizationID: in.SponsorOrganizationID,
		PrincipalMembershipID: nullUUIDPtr(in.PrincipalMembershipID), MembershipType: in.MembershipType,
		ExternalMemberNo: in.ExternalMemberNo, Status: in.Status,
		ValidFrom: date(&in.ValidFrom), ValidTo: date(in.ValidTo),
	})
	if err != nil {
		return uuid.Nil, membershipError(err, "create membership")
	}
	return row.ID, nil
}

// UpdateMembership implements application.Repository.
func (Repository) UpdateMembership(ctx context.Context, tx pgx.Tx, tenantID, membershipID uuid.UUID, in application.MembershipUpdateRow) error {
	_, err := sqlcgen.New(tx).UpdateSponsorMembership(ctx, sqlcgen.UpdateSponsorMembershipParams{
		TenantID: tenantID, ID: membershipID, RowVersion: in.Expected,
		Status: in.Status, ExternalMemberNo: in.ExternalMemberNo, ValidTo: date(in.ValidTo),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return membershipError(err, "update membership")
	}
	return nil
}

// membershipError maps the two database guards of party.sponsor_membership.
func membershipError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == exclusionViolation:
			return application.ErrMembershipOverlap
		case pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintExternalMemberNo:
			return application.ErrMemberNoTaken
		}
	}
	return fmt.Errorf("party: %s: %w", what, err)
}
