package application

import (
	"bytes"
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

// Relationship directions of the contract.
const (
	DirectionOutgoing = "OUTGOING"
	DirectionIncoming = "INCOMING"
	DirectionMutual   = "MUTUAL"
)

// Relationship is one family or care relationship seen from a person.
type Relationship struct {
	ID               uuid.UUID
	RelationshipType string
	Direction        string
	Other            PersonSummary
	Status           string
	ValidFrom        time.Time
	ValidTo          *time.Time
	EndReasonCode    *string
	RowVersion       int64
}

// NewRelationshipInput is the create command.
type NewRelationshipInput struct {
	TargetPersonID   uuid.UUID
	RelationshipType string
	ValidFrom        time.Time
	ValidTo          *time.Time
}

// EndRelationshipInput closes a relationship period with a reason.
type EndRelationshipInput struct {
	EndsOn          time.Time
	ReasonCode      string
	ReasonText      string
	ExpectedVersion int64
}

// ListRelationships returns the person's relationships in both directions.
func (s *Service) ListRelationships(ctx context.Context, rc identity.RequestContext, personID uuid.UUID) ([]Relationship, error) {
	var out []Relationship
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		rows, err := s.repo.ListRelationships(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out = make([]Relationship, 0, len(rows))
		for _, r := range rows {
			out = append(out, relationshipView(r, personID))
		}
		return nil
	})
	return out, err
}

// CreateRelationship links two persons of the tenant for a validity period. Non-directional
// types are stored with the lower id as source, so a mirrored duplicate hits the same
// exclusion constraint as a repeated one.
func (s *Service) CreateRelationship(ctx context.Context, rc identity.RequestContext, personID uuid.UUID, in NewRelationshipInput) (Relationship, error) {
	ve := &domain.ValidationError{}
	if in.TargetPersonID == personID {
		ve.Add("targetPersonId", "SELF_RELATIONSHIP", "kişi kendisiyle ilişkilendirilemez")
	}
	if !domain.ValidTypeCode(in.RelationshipType) {
		ve.Add("relationshipType", "RELATIONSHIP_TYPE_UNKNOWN", "geçersiz ilişki türü")
	}
	if err := domain.ValidatePeriod("valid", in.ValidFrom, in.ValidTo); err != nil {
		var pe *domain.ValidationError
		if errors.As(err, &pe) {
			ve.Fields = append(ve.Fields, pe.Fields...)
		}
	}
	if err := ve.OrNil(); err != nil {
		return Relationship{}, err
	}

	var out Relationship
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		if _, err := s.repo.GetPerson(ctx, tx, rc.TenantID, in.TargetPersonID); err != nil {
			return err
		}
		catalog, err := s.repo.GetRelationshipType(ctx, tx, rc.TenantID, in.RelationshipType)
		if errors.Is(err, ErrCatalogEntryNotFound) || (err == nil && catalog.Status != domain.StatusActive) {
			ve.Add("relationshipType", "RELATIONSHIP_TYPE_UNKNOWN", "ilişki türü tanımlı değil")
			return ve
		}
		if err != nil {
			return err
		}
		source, target := personID, in.TargetPersonID
		if !catalog.IsDirectional && bytes.Compare(target[:], source[:]) < 0 {
			source, target = target, source
		}
		relID, err := s.repo.CreateRelationship(ctx, tx, NewRelationshipRow{
			TenantID: rc.TenantID, SourcePersonID: source, TargetPersonID: target,
			RelationshipType: in.RelationshipType, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
		})
		if err != nil {
			return err
		}
		if err := s.recordRelationship(ctx, tx, rc, "relationship.create", relID, map[string]any{
			"person_id": personID, "target_person_id": in.TargetPersonID, "relationship_type": in.RelationshipType,
		}); err != nil {
			return err
		}
		row, err := s.repo.GetRelationship(ctx, tx, rc.TenantID, relID)
		if err != nil {
			return err
		}
		out, err = s.relationshipWithOther(ctx, tx, rc.TenantID, row, personID)
		return err
	})
	return out, err
}

// EndRelationship sets the upper bound of the validity period and the ENDED status.
func (s *Service) EndRelationship(ctx context.Context, rc identity.RequestContext, personID, relationshipID uuid.UUID, in EndRelationshipInput) (Relationship, error) {
	ve := &domain.ValidationError{}
	if !domain.ValidTypeCode(in.ReasonCode) {
		ve.Add("reasonCode", "FORMAT", "geçersiz gerekçe kodu")
	}
	if err := ve.OrNil(); err != nil {
		return Relationship{}, err
	}

	var out Relationship
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		row, err := s.repo.GetRelationship(ctx, tx, rc.TenantID, relationshipID)
		if err != nil {
			return err
		}
		if row.SourcePersonID != personID && row.TargetPersonID != personID {
			return ErrNotFound
		}
		if row.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		if !in.EndsOn.After(row.ValidFrom) {
			ve.Add("endsOn", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
			return ve
		}
		if err := s.repo.EndRelationship(ctx, tx, rc.TenantID, relationshipID, EndRelationshipRow{
			EndsOn: in.EndsOn, ReasonCode: in.ReasonCode, ReasonText: optString(in.ReasonText), Expected: in.ExpectedVersion,
		}); err != nil {
			return err
		}
		if err := s.recordRelationship(ctx, tx, rc, "relationship.end", relationshipID, map[string]any{
			"person_id": personID, "relationship_type": row.RelationshipType, "reason_code": in.ReasonCode,
		}); err != nil {
			return err
		}
		updated, err := s.repo.GetRelationship(ctx, tx, rc.TenantID, relationshipID)
		if err != nil {
			return err
		}
		out, err = s.relationshipWithOther(ctx, tx, rc.TenantID, updated, personID)
		return err
	})
	return out, err
}

// relationshipWithOther fills the other person's summary on a single-row read.
func (s *Service) relationshipWithOther(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, row RelationshipRow, personID uuid.UUID) (Relationship, error) {
	otherID := row.TargetPersonID
	if otherID == personID {
		otherID = row.SourcePersonID
	}
	other, err := s.load(ctx, tx, tenantID, otherID)
	if err != nil {
		return Relationship{}, err
	}
	view := relationshipView(row, personID)
	view.Other = PersonSummary{
		ID: other.ID, DisplayName: other.DisplayName, Status: other.Status,
		MaskedPrimaryIdentifier: other.MaskedPrimaryIdentifier,
	}
	return view, nil
}

func relationshipView(r RelationshipRow, personID uuid.UUID) Relationship {
	direction := DirectionMutual
	if r.IsDirectional {
		direction = DirectionIncoming
		if r.SourcePersonID == personID {
			direction = DirectionOutgoing
		}
	}
	return Relationship{
		ID: r.ID, RelationshipType: r.RelationshipType, Direction: direction, Status: r.Status,
		ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, EndReasonCode: r.EndReasonCode, RowVersion: r.RowVersion,
		Other: summaryView(r.Other),
	}
}

func (s *Service) recordRelationship(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, action string, id uuid.UUID, detail map[string]any) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID), MembershipID: nullUUID(rc.MembershipID),
		Category: audit.CategoryBusiness, ActionCode: action,
		ResourceType: "person_relationship", ResourceID: nullUUID(id), Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}
