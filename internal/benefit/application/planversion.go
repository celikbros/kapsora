package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// Definition is one entitlement definition of a plan version.
type Definition struct {
	ID     uuid.UUID
	Status string
	Spec   domain.EntitlementDefinition
}

// PlanVersion is the view returned by the plan version endpoints. Definitions is empty
// on summaries (list, plan detail) and filled on the single-version reads.
type PlanVersion struct {
	ID                uuid.UUID
	PlanID            uuid.UUID
	VersionNo         int
	Status            string
	ValidFrom         *time.Time
	ValidTo           *time.Time
	ConfigurationHash string
	PublishedAt       *time.Time
	PublishedBy       *uuid.UUID
	SubmittedAt       *time.Time
	SubmittedBy       *uuid.UUID
	ReviewComment     *string
	RetireReasonCode  *string
	Notes             *string
	Definitions       []Definition
	RowVersion        int64
}

// NewPlanVersionInput is the create command; CopyFromVersionID copies the entitlement
// definitions of another version of the same plan.
type NewPlanVersionInput struct {
	CopyFromVersionID *uuid.UUID
	ValidFrom         *time.Time
	ValidTo           *time.Time
	Notes             *string
}

// PlanVersionPatch is a merge-patch of a draft version.
type PlanVersionPatch struct {
	ValidFrom       *time.Time
	ClearValidFrom  bool
	ValidTo         *time.Time
	ClearValidTo    bool
	Notes           *string
	ClearNotes      bool
	ExpectedVersion int64
}

// ListPlanVersions returns the version summaries of a plan, highest number first.
func (s *Service) ListPlanVersions(ctx context.Context, rc identity.RequestContext, planID uuid.UUID) ([]PlanVersion, error) {
	var out []PlanVersion
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPlan(ctx, tx, rc.TenantID, planID); err != nil {
			return err
		}
		rows, err := s.repo.ListPlanVersions(ctx, tx, rc.TenantID, planID)
		if err != nil {
			return err
		}
		out = make([]PlanVersion, 0, len(rows))
		for _, r := range rows {
			out = append(out, versionView(r, nil))
		}
		return nil
	})
	return out, err
}

// GetPlanVersion returns one version with its definitions, hash and review metadata.
func (s *Service) GetPlanVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (PlanVersion, error) {
	var out PlanVersion
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// CreatePlanVersion opens the next DRAFT version of a plan.
func (s *Service) CreatePlanVersion(ctx context.Context, rc identity.RequestContext, planID uuid.UUID, in NewPlanVersionInput) (PlanVersion, error) {
	in.ValidFrom, in.ValidTo = datePtr(in.ValidFrom), datePtr(in.ValidTo)
	ve := &domain.ValidationError{}
	domain.ValidatePeriod(ve, "valid", in.ValidFrom, in.ValidTo)
	if in.Notes != nil {
		domain.ValidateText(ve, "notes", *in.Notes, domain.MaxNotesLength)
	}
	if err := ve.OrNil(); err != nil {
		return PlanVersion{}, err
	}

	var out PlanVersion
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPlan(ctx, tx, rc.TenantID, planID); err != nil {
			return err
		}
		var source []DefinitionRow
		if in.CopyFromVersionID != nil {
			origin, err := s.repo.GetPlanVersion(ctx, tx, rc.TenantID, *in.CopyFromVersionID)
			if err != nil {
				return err
			}
			if origin.PlanID != planID {
				ve.Add("copyFromVersionId", "UNKNOWN", "kaynak sürüm bu plana ait değil")
				return ve
			}
			if source, err = s.repo.ListDefinitions(ctx, tx, rc.TenantID, origin.ID); err != nil {
				return err
			}
		}
		versionNo, err := s.repo.NextVersionNo(ctx, tx, rc.TenantID, planID)
		if err != nil {
			return err
		}
		versionID, err := s.repo.CreatePlanVersion(ctx, tx, NewPlanVersionRow{
			TenantID: rc.TenantID, PlanID: planID, ActorID: rc.Principal.ActorID, VersionNo: versionNo,
			ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, Notes: in.Notes,
		})
		if err != nil {
			return err
		}
		for _, d := range source {
			if _, err := s.repo.CreateDefinition(ctx, tx, NewDefinitionRow{
				TenantID: rc.TenantID, PlanVersionID: versionID, Definition: d.Definition,
			}); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, rc, "plan_version.create", "plan_version", versionID, map[string]any{
			"plan_id": planID, "version_no": versionNo, "definition_count": len(source),
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// UpdatePlanVersion applies a merge-patch of validity and notes to a DRAFT version.
func (s *Service) UpdatePlanVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID, patch PlanVersionPatch) (PlanVersion, error) {
	ve := &domain.ValidationError{}
	if patch.Notes != nil {
		domain.ValidateText(ve, "notes", *patch.Notes, domain.MaxNotesLength)
	}
	if err := ve.OrNil(); err != nil {
		return PlanVersion{}, err
	}

	var out PlanVersion
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, patch.ExpectedVersion)
		if err != nil {
			return err
		}
		next := PlanVersionDraftRow{ValidFrom: current.ValidFrom, ValidTo: current.ValidTo, Notes: current.Notes}
		switch {
		case patch.ClearValidFrom:
			next.ValidFrom = nil
		case patch.ValidFrom != nil:
			next.ValidFrom = datePtr(patch.ValidFrom)
		}
		switch {
		case patch.ClearValidTo:
			next.ValidTo = nil
		case patch.ValidTo != nil:
			next.ValidTo = datePtr(patch.ValidTo)
		}
		switch {
		case patch.ClearNotes:
			next.Notes = nil
		case patch.Notes != nil:
			next.Notes = optString(strings.TrimSpace(*patch.Notes))
		}
		domain.ValidatePeriod(ve, "valid", next.ValidFrom, next.ValidTo)
		if ve.Len() > 0 {
			return ve
		}
		if err := s.repo.UpdatePlanVersionDraft(ctx, tx, rc.TenantID, versionID, next); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan_version.update", "plan_version", versionID, map[string]any{
			"plan_id": current.PlanID, "version_no": current.VersionNo,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// ReplaceDefinitions rewrites the full entitlement definition list of a DRAFT version.
func (s *Service) ReplaceDefinitions(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	defs []domain.EntitlementDefinition, expectedVersion int64) (PlanVersion, error) {
	if err := domain.ValidateDefinitions(defs); err != nil {
		return PlanVersion{}, err
	}

	var out PlanVersion
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expectedVersion)
		if err != nil {
			return err
		}
		if _, err := s.repo.DeleteDefinitions(ctx, tx, rc.TenantID, versionID); err != nil {
			return err
		}
		for _, d := range defs {
			if _, err := s.repo.CreateDefinition(ctx, tx, NewDefinitionRow{
				TenantID: rc.TenantID, PlanVersionID: versionID, Definition: d,
			}); err != nil {
				return err
			}
		}
		// The definitions are child rows; touching the version moves its ETag as well.
		if err := s.repo.TouchPlanVersion(ctx, tx, rc.TenantID, versionID); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan_version.definitions.replace", "plan_version", versionID, map[string]any{
			"plan_id": current.PlanID, "definition_count": len(defs),
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// SubmitPlanVersion moves a draft to UNDER_REVIEW and records the maker.
func (s *Service) SubmitPlanVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	comment *string, expectedVersion int64) (PlanVersion, error) {
	ve := &domain.ValidationError{}
	if comment != nil {
		domain.ValidateText(ve, "comment", *comment, domain.MaxCommentLength)
	}
	if err := ve.OrNil(); err != nil {
		return PlanVersion{}, err
	}

	var out PlanVersion
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expectedVersion)
		if err != nil {
			return err
		}
		defs, err := s.repo.ListDefinitions(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if len(defs) == 0 {
			ve.Add("definitions", "REQUIRED", "en az bir hak tanımı gerekli")
		}
		if current.ValidFrom == nil {
			ve.Add("validFrom", "REQUIRED", "geçerlilik başlangıcı gerekli")
		}
		if ve.Len() > 0 {
			return ve
		}
		if err := s.repo.SubmitPlanVersion(ctx, tx, rc.TenantID, versionID, SubmitRow{
			ActorID: rc.Principal.ActorID, Comment: optString(strings.TrimSpace(deref(comment))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan_version.submit", "plan_version", versionID, map[string]any{
			"plan_id": current.PlanID, "version_no": current.VersionNo, "definition_count": len(defs),
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// PublishPlanVersion freezes the configuration of a version under review. The checker
// must differ from the maker; the refusal is audited before it is reported.
func (s *Service) PublishPlanVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	comment *string, expectedVersion int64) (PlanVersion, error) {
	ve := &domain.ValidationError{}
	if comment != nil {
		domain.ValidateText(ve, "comment", *comment, domain.MaxCommentLength)
	}
	if err := ve.OrNil(); err != nil {
		return PlanVersion{}, err
	}

	var out PlanVersion
	var denied *PlanVersionRow
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockPlanVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if current.RowVersion != expectedVersion {
			return ErrVersionMismatch
		}
		if current.Status != domain.VersionUnderReview {
			return statusError(current.Status)
		}
		if current.SubmittedBy != nil && *current.SubmittedBy == rc.Principal.ActorID {
			// The audit row cannot be written here: this transaction rolls back. It is
			// written by auditMakerCheckerDenial once the refusal is final.
			row := current
			denied = &row
			return ErrMakerCheckerSame
		}
		defs, err := s.repo.ListDefinitions(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if len(defs) == 0 {
			ve.Add("definitions", "REQUIRED", "en az bir hak tanımı gerekli")
			return ve
		}
		hash, err := domain.ConfigurationHash(current.ValidFrom, current.ValidTo, specs(defs))
		if err != nil {
			return err
		}
		if err := s.repo.PublishPlanVersion(ctx, tx, rc.TenantID, versionID, PublishRow{
			ActorID: rc.Principal.ActorID, ConfigurationHash: hash,
			Comment: optString(strings.TrimSpace(deref(comment))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan_version.publish", "plan_version", versionID, map[string]any{
			"plan_id": current.PlanID, "version_no": current.VersionNo,
			"configuration_hash": domain.HexHash(hash), "definition_count": len(defs),
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	if denied != nil {
		return PlanVersion{}, s.auditMakerCheckerDenial(ctx, rc, *denied, err)
	}
	return out, err
}

// auditMakerCheckerDenial records the refused publish in its own transaction, because the
// command's transaction rolled back with it. A failure to audit is reported alongside the
// refusal: a denial that leaves no trace is worse than a noisy error.
func (s *Service) auditMakerCheckerDenial(ctx context.Context, rc identity.RequestContext, row PlanVersionRow, cause error) error {
	auditErr := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		return s.recordDenied(ctx, tx, rc, "plan_version.publish", "plan_version", row.ID,
			"MAKER_CHECKER_SAME_ACTOR", map[string]any{
				"plan_id": row.PlanID, "version_no": row.VersionNo,
			})
	})
	if auditErr != nil {
		return errors.Join(cause, auditErr)
	}
	return cause
}

// RetirePlanVersion closes a published version with a reason; it stays readable.
func (s *Service) RetirePlanVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	reasonCode string, reasonText *string, expectedVersion int64) (PlanVersion, error) {
	reasonCode = strings.TrimSpace(reasonCode)
	ve := &domain.ValidationError{}
	if reasonCode == "" {
		ve.Add("reasonCode", "REQUIRED", "gerekçe kodu zorunlu")
	}
	if reasonText != nil {
		domain.ValidateText(ve, "reasonText", *reasonText, domain.MaxCommentLength)
	}
	if err := ve.OrNil(); err != nil {
		return PlanVersion{}, err
	}

	var out PlanVersion
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockPlanVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if current.RowVersion != expectedVersion {
			return ErrVersionMismatch
		}
		if current.Status != domain.VersionPublished {
			return statusError(current.Status)
		}
		if err := s.repo.RetirePlanVersion(ctx, tx, rc.TenantID, versionID, RetireRow{
			ReasonCode: reasonCode, ReasonText: optString(strings.TrimSpace(deref(reasonText))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan_version.retire", "plan_version", versionID, map[string]any{
			"plan_id": current.PlanID, "version_no": current.VersionNo, "reason_code": reasonCode,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// lockDraft loads a version FOR UPDATE and asserts that it is an editable draft matching
// the caller's If-Match.
func (s *Service) lockDraft(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, expected int64) (PlanVersionRow, error) {
	current, err := s.repo.LockPlanVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return PlanVersionRow{}, err
	}
	if current.RowVersion != expected {
		return PlanVersionRow{}, ErrVersionMismatch
	}
	if current.Status != domain.VersionDraft {
		return PlanVersionRow{}, statusError(current.Status)
	}
	return current, nil
}

// statusError distinguishes "the row is frozen" from "the command does not apply here".
func statusError(current string) error {
	if current == domain.VersionPublished || current == domain.VersionRetired {
		return ErrVersionImmutable
	}
	return ErrVersionTransition
}

func (s *Service) loadVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (PlanVersion, error) {
	row, err := s.repo.GetPlanVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return PlanVersion{}, err
	}
	defs, err := s.repo.ListDefinitions(ctx, tx, tenantID, versionID)
	if err != nil {
		return PlanVersion{}, err
	}
	return versionView(row, defs), nil
}

func versionView(r PlanVersionRow, defs []DefinitionRow) PlanVersion {
	out := PlanVersion{
		ID: r.ID, PlanID: r.PlanID, VersionNo: r.VersionNo, Status: r.Status,
		ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, ConfigurationHash: domain.HexHash(r.ConfigurationHash),
		PublishedAt: r.PublishedAt, PublishedBy: r.PublishedBy,
		SubmittedAt: r.SubmittedAt, SubmittedBy: r.SubmittedBy,
		ReviewComment: r.ReviewComment, RetireReasonCode: r.RetireReasonCode, Notes: r.Notes,
		Definitions: make([]Definition, 0, len(defs)), RowVersion: r.RowVersion,
	}
	for _, d := range defs {
		out.Definitions = append(out.Definitions, Definition{ID: d.ID, Status: d.Status, Spec: d.Definition})
	}
	return out
}

func specs(defs []DefinitionRow) []domain.EntitlementDefinition {
	out := make([]domain.EntitlementDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Definition)
	}
	return out
}
