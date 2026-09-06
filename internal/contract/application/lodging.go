package application

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// PermissionLodgingManage guards writing a version's lodging terms (migration 000039). It
// is separate from contract.manage so a tenant can hand the accommodation desk the terms
// of a stay without handing it the price sheet; every role that may write a contract
// version holds both, and a test asserts that pairing.
const PermissionLodgingManage = "contract.lodging_terms.manage"

// LodgingTermsResult is the version's terms together with the version's own ETag, which is
// what a caller sends back as If-Match. The terms are a child of the version, so writing
// them moves the version's row_version rather than a row_version of their own: a screen
// holding a stale version ETag is a screen that has not seen the last change to any part
// of the sheet.
type LodgingTermsResult struct {
	Terms      LodgingTermsRecord
	RowVersion int64
}

// GetLodgingTerms returns the single lodging terms row of a version. A version with none
// answers ErrLodgingTermsNotFound, which the transport turns into a 404: "not agreed yet"
// and "agreed as zero" are different answers and a screen has to be able to tell them
// apart. It is also the answer WP-I6-02 refuses a confirmation on.
func (s *Service) GetLodgingTerms(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (LodgingTermsResult, error) {
	var out LodgingTermsResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Terms, err = s.repo.GetLodgingTerms(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// PutLodgingTerms writes the single lodging terms row of a DRAFT version, creating it or
// replacing it in place.
//
// The draft rule is checked here so the caller gets CONTRACT_VERSION_IMMUTABLE with the
// version's status rather than a raw constraint violation — and it is *also* enforced by
// the trigger of migration 000039, which is the one that still holds when this function is
// bypassed. Two statements of the same rule are acceptable here in a way they would not be
// elsewhere, because one of them is a message and the other is a guarantee.
func (s *Service) PutLodgingTerms(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	in domain.LodgingTermsInput, expected int64,
) (LodgingTermsResult, error) {
	if err := domain.ValidateLodgingTerms(in); err != nil {
		return LodgingTermsResult{}, err
	}
	row := LodgingTermsRow{
		FreeCancellationHoursBefore: in.FreeCancellationHoursBefore,
		PenaltyKind:                 in.PenaltyKind,
		PenaltyNights:               in.PenaltyNights,
		PenaltyPercent:              optString(domain.CanonicalDecimal(in.PenaltyPercent)),
		NoShowPercent:               domain.CanonicalDecimal(in.NoShowPercent),
		HoldMinutes:                 in.HoldMinutes,
		MinNights:                   in.MinNights,
		MaxNights:                   in.MaxNights,
		ChildFreeUnderAge:           in.ChildFreeUnderAge,
	}

	var out LodgingTermsResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		if err := s.repo.UpsertLodgingTerms(ctx, tx, rc.TenantID, versionID, row); err != nil {
			return err
		}
		// The audit detail carries the shape of the policy and not a person, a price or a
		// property: these terms are a commercial clause of an agreement between two
		// organizations, and the row itself is the record of what they say.
		if err := s.touchAndAudit(ctx, tx, rc, current, "contract_version.lodging_terms.write",
			map[string]any{"penalty_kind": in.PenaltyKind}); err != nil {
			return err
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		out.RowVersion = version.RowVersion
		out.Terms, err = s.repo.GetLodgingTerms(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// LodgingPolicySnapshot is what a booking freezes at confirmation (WP-I6-02) and what a
// cancellation or a no-show is judged by afterwards (WP-I6-03).
//
// It is the terms plus the three facts that make them re-readable a year later: which
// contract version they came from, when the copy was taken, and the zone the hours in it
// are counted in. A snapshot without the zone would be a free-cancellation window that
// moved with the reader's clock, which is the one thing a cancellation fee may not do.
type LodgingPolicySnapshot struct {
	ContractVersionID uuid.UUID
	SnapshotAt        time.Time
	TimeZone          string
	Terms             LodgingTermsRecord
}

// SnapshotLodgingPolicy reads a version's terms and stamps them with the moment and the
// zone. It lives here rather than in the accommodation module because the terms are the
// contract's, and a second reader of contract.lodging_terms would be a second answer to
// "what did this version promise".
func (s *Service) SnapshotLodgingPolicy(ctx context.Context, rc identity.RequestContext,
	versionID uuid.UUID, timeZone string,
) (LodgingPolicySnapshot, error) {
	result, err := s.GetLodgingTerms(ctx, rc, versionID)
	if err != nil {
		return LodgingPolicySnapshot{}, err
	}
	// A snapshot without a zone is not a snapshot: the hours in it would be counted against
	// whichever clock happened to read them. A caller whose context carries none gets the
	// documented default rather than an empty string nobody downstream could interpret.
	if strings.TrimSpace(timeZone) == "" {
		timeZone = domain.DefaultTimeZone
	}
	return LodgingPolicySnapshot{
		ContractVersionID: versionID,
		SnapshotAt:        s.now().UTC(),
		TimeZone:          timeZone,
		Terms:             result.Terms,
	}, nil
}
