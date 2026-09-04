package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// NewVersionInput is the create command; CopyFromVersionID copies the whole price content
// of another version of the same contract, which is how a yearly revision starts from last
// year's sheet rather than from an empty page.
type NewVersionInput struct {
	CopyFromVersionID *uuid.UUID
	ValidFrom         *time.Time
	ValidTo           *time.Time
	CurrencyCode      string
	Notes             *string
}

// VersionPatch is a merge-patch of a draft version.
type VersionPatch struct {
	ValidFrom       *time.Time
	ClearValidFrom  bool
	ValidTo         *time.Time
	ClearValidTo    bool
	CurrencyCode    *string
	Notes           *string
	ClearNotes      bool
	ExpectedVersion int64
}

// ListVersions returns the version summaries of a contract, highest number first.
func (s *Service) ListVersions(ctx context.Context, rc identity.RequestContext, contractID uuid.UUID) ([]VersionRecord, error) {
	var out []VersionRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetContract(ctx, tx, rc.TenantID, contractID); err != nil {
			return err
		}
		rows, err := s.repo.ListVersions(ctx, tx, rc.TenantID, contractID)
		if err != nil {
			return err
		}
		out = make([]VersionRecord, 0, len(rows))
		for _, v := range rows {
			if visibleTo(rc, v.Status) {
				out = append(out, v)
			}
		}
		return nil
	})
	return out, err
}

// visibleTo decides whether a caller may see a version at all. A published or retired
// version is the agreement itself and anyone who may read contracts may read it. A draft
// or a version under review is an unagreed negotiation, and a read-only caller has no
// business knowing the terms are being renegotiated — so it is hidden entirely rather
// than refused, which would confirm it exists.
func visibleTo(rc identity.RequestContext, status string) bool {
	if status == domain.VersionPublished || status == domain.VersionRetired {
		return true
	}
	return rc.Has(PermissionManage)
}

// GetVersion returns one version with its price lists.
func (s *Service) GetVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (VersionView, error) {
	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		loaded, err := s.loadVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if !visibleTo(rc, loaded.Version.Status) {
			return ErrVersionNotFound
		}
		out = loaded
		return nil
	})
	return out, err
}

// CreateVersion opens the next DRAFT version of a contract.
func (s *Service) CreateVersion(ctx context.Context, rc identity.RequestContext, contractID uuid.UUID, in NewVersionInput) (VersionView, error) {
	in.ValidFrom, in.ValidTo = domain.DayPtr(in.ValidFrom), domain.DayPtr(in.ValidTo)
	if in.CurrencyCode == "" {
		in.CurrencyCode = domain.DefaultCurrency
	}
	if err := domain.ValidateVersion(domain.VersionInput{
		ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, CurrencyCode: in.CurrencyCode, Notes: in.Notes,
	}); err != nil {
		return VersionView{}, err
	}

	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetContract(ctx, tx, rc.TenantID, contractID); err != nil {
			return err
		}
		var source *VersionRecord
		if in.CopyFromVersionID != nil {
			origin, err := s.repo.GetVersion(ctx, tx, rc.TenantID, *in.CopyFromVersionID)
			if err != nil {
				return err
			}
			if origin.ContractID != contractID {
				return fieldError("copyFromVersionId", "UNKNOWN", "kaynak sürüm bu sözleşmeye ait değil")
			}
			source = &origin
		}
		versionNo, err := s.repo.NextVersionNo(ctx, tx, rc.TenantID, contractID)
		if err != nil {
			return err
		}
		currency := in.CurrencyCode
		if source != nil && in.CurrencyCode == domain.DefaultCurrency {
			// A copy keeps the currency it was agreed in unless the caller says otherwise.
			currency = source.CurrencyCode
		}
		versionID, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, NewVersionRow{
			ContractID: contractID, VersionNo: versionNo, ValidFrom: in.ValidFrom,
			ValidTo: in.ValidTo, CurrencyCode: currency, Notes: in.Notes,
		})
		if err != nil {
			return err
		}
		copied := 0
		if source != nil {
			if copied, err = s.copyPriceContent(ctx, tx, rc.TenantID, source.ID, versionID); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, rc, "contract_version.create", "contract_version", versionID, map[string]any{
			"contract_id": contractID, "version_no": versionNo, "copied_price_lists": copied,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// UpdateVersion applies a merge-patch of period, currency and notes to a DRAFT version.
func (s *Service) UpdateVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID, p VersionPatch) (VersionView, error) {
	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, p.ExpectedVersion)
		if err != nil {
			return err
		}
		next := VersionDraftRow{
			ValidFrom: current.ValidFrom, ValidTo: current.ValidTo,
			CurrencyCode: current.CurrencyCode, Notes: current.Notes,
		}
		switch {
		case p.ClearValidFrom:
			next.ValidFrom = nil
		case p.ValidFrom != nil:
			next.ValidFrom = domain.DayPtr(p.ValidFrom)
		}
		switch {
		case p.ClearValidTo:
			next.ValidTo = nil
		case p.ValidTo != nil:
			next.ValidTo = domain.DayPtr(p.ValidTo)
		}
		if p.CurrencyCode != nil {
			next.CurrencyCode = *p.CurrencyCode
		}
		switch {
		case p.ClearNotes:
			next.Notes = nil
		case p.Notes != nil:
			next.Notes = optString(strings.TrimSpace(*p.Notes))
		}
		if err := domain.ValidateVersion(domain.VersionInput{
			ValidFrom: next.ValidFrom, ValidTo: next.ValidTo,
			CurrencyCode: next.CurrencyCode, Notes: next.Notes,
		}); err != nil {
			return err
		}
		if err := s.repo.UpdateVersionDraft(ctx, tx, rc.TenantID, versionID, next); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "contract_version.update", "contract_version", versionID, map[string]any{
			"contract_id": current.ContractID, "version_no": current.VersionNo,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// SubmitVersion moves a draft to UNDER_REVIEW and records the maker. A price sheet with no
// item in it is refused: reviewing an empty tariff means nothing, and publishing one would
// make every later lookup answer PRICE_NOT_FOUND.
func (s *Service) SubmitVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	comment *string, expected int64,
) (VersionView, error) {
	if err := domain.ValidateComment(comment); err != nil {
		return VersionView{}, err
	}

	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		items, err := s.repo.CountPriceItems(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		ve := &domain.ValidationError{}
		if items == 0 {
			ve.Add("priceLists", "REQUIRED", "en az bir fiyat kalemi içeren bir fiyat listesi gerekli")
		}
		if current.ValidFrom == nil {
			ve.Add("validFrom", "REQUIRED", "geçerlilik başlangıcı gerekli")
		}
		if err := ve.OrNil(); err != nil {
			return err
		}
		if err := s.repo.SubmitVersion(ctx, tx, rc.TenantID, versionID, SubmitRow{
			ActorID: rc.Principal.ActorID, Comment: optString(strings.TrimSpace(deref(comment))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "contract_version.submit", "contract_version", versionID, map[string]any{
			"contract_id": current.ContractID, "version_no": current.VersionNo, "price_item_count": items,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// PublishVersion freezes the price content of a version under review. The checker must
// differ from the maker; the refusal is audited before it is reported. The hash is taken
// over the whole sheet, so "what was agreed" can be proved after the fact.
func (s *Service) PublishVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	comment *string, expected int64,
) (VersionView, error) {
	if err := domain.ValidateComment(comment); err != nil {
		return VersionView{}, err
	}

	var out VersionView
	var denied *VersionRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if current.RowVersion != expected {
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
		items, err := s.repo.CountPriceItems(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if items == 0 {
			return fieldError("priceLists", "REQUIRED", "en az bir fiyat kalemi içeren bir fiyat listesi gerekli")
		}
		content, err := s.priceContent(ctx, tx, rc.TenantID, current)
		if err != nil {
			return err
		}
		hash, err := domain.ConfigurationHash(content)
		if err != nil {
			return err
		}
		if err := s.repo.PublishVersion(ctx, tx, rc.TenantID, versionID, PublishRow{
			ActorID: rc.Principal.ActorID, ConfigurationHash: hash,
			Comment: optString(strings.TrimSpace(deref(comment))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "contract_version.publish", "contract_version", versionID, map[string]any{
			"contract_id": current.ContractID, "version_no": current.VersionNo,
			"configuration_hash": hash, "price_item_count": items,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	if denied != nil {
		return VersionView{}, s.auditMakerCheckerDenial(ctx, rc, *denied, err)
	}
	return out, err
}

// auditMakerCheckerDenial records the refused publish in its own transaction, because the
// command's transaction rolled back with it. A failure to audit is reported alongside the
// refusal: a denial that leaves no trace is worse than a noisy error.
func (s *Service) auditMakerCheckerDenial(ctx context.Context, rc identity.RequestContext, row VersionRecord, cause error) error {
	auditErr := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		return s.recordDenied(ctx, tx, rc, "contract_version.publish", "contract_version", row.ID,
			"MAKER_CHECKER_SAME_ACTOR", map[string]any{
				"contract_id": row.ContractID, "version_no": row.VersionNo,
			})
	})
	if auditErr != nil {
		return errors.Join(cause, auditErr)
	}
	return cause
}

// RetireVersion closes a published version with a reason; it stays readable for every
// quote and claim that was priced from it.
func (s *Service) RetireVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	reasonCode string, reasonText *string, expected int64,
) (VersionView, error) {
	reasonCode = strings.TrimSpace(reasonCode)
	if err := domain.ValidateReasonCode(reasonCode, reasonText); err != nil {
		return VersionView{}, err
	}

	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		if current.Status != domain.VersionPublished {
			return statusError(current.Status)
		}
		if err := s.repo.RetireVersion(ctx, tx, rc.TenantID, versionID, RetireRow{
			ReasonCode: reasonCode, ReasonText: optString(strings.TrimSpace(deref(reasonText))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "contract_version.retire", "contract_version", versionID, map[string]any{
			"contract_id": current.ContractID, "version_no": current.VersionNo, "reason_code": reasonCode,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// lockDraft loads a version FOR UPDATE and asserts that it is an editable draft matching
// the caller's If-Match. Every write to a version or to any part of its price sheet goes
// through here, which is what makes CONTRACT_VERSION_IMMUTABLE one rule rather than nine.
func (s *Service) lockDraft(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, expected int64) (VersionRecord, error) {
	current, err := s.repo.LockVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return VersionRecord{}, err
	}
	if current.RowVersion != expected {
		return VersionRecord{}, ErrVersionMismatch
	}
	if current.Status != domain.VersionDraft {
		return VersionRecord{}, statusError(current.Status)
	}
	return current, nil
}

// loadVersion reads a version with the price lists under it.
func (s *Service) loadVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (VersionView, error) {
	version, err := s.repo.GetVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return VersionView{}, err
	}
	lists, err := s.repo.ListPriceLists(ctx, tx, tenantID, versionID)
	if err != nil {
		return VersionView{}, err
	}
	return VersionView{Version: version, PriceLists: lists}, nil
}
