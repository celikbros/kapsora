package claimpg

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// sqlc gives each query its own row struct even when the columns are identical, so the four
// claim reads produce four types that differ in name only. `claimRow` is the one shape the
// mapper below works from, and the four small converters are what keep the mapper single —
// the alternative is four copies of it, which is four places for a column to be forgotten.
type claimRow struct {
	ID                     uuid.UUID
	Reference              string
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	DomainCode             string
	SourceType             *string
	SourceID               uuid.NullUUID
	CaseID                 uuid.NullUUID
	FulfilmentID           uuid.NullUUID
	AuthorizationID        uuid.NullUUID
	CurrentVersionNo       int32
	Status                 string
	ServiceDateFrom        pgtype.Date
	ServiceDateTo          pgtype.Date
	Channel                string
	RejectReasonCode       *string
	ReturnReasonCode       *string
	ReviewCommentMedical   *string
	ReviewCommentFinancial *string
	ClosedAt               *time.Time
	CreatedAt              time.Time
	RowVersion             int64
}

func createdClaimRow(r sqlcgen.CreateClaimRow) claimRow { return claimRow(r) }
func getClaimRow(r sqlcgen.GetClaimRow) claimRow        { return claimRow(r) }
func lockClaimRow(r sqlcgen.LockClaimRow) claimRow      { return claimRow(r) }
func listClaimRow(r sqlcgen.ListClaimsRow) claimRow     { return claimRow(r) }

// claimOf maps one row onto the application record. Every field is copied: the projection is
// applied one layer up, and a repository that dropped a clinical column here would be a second
// rule about visibility that could disagree with the first.
func claimOf(r claimRow) application.ClaimRecord {
	return application.ClaimRecord{
		ID: r.ID, Reference: r.Reference, PersonID: r.PersonID, ProgramID: r.ProgramID,
		EnrollmentID: r.EnrollmentID, ProviderOrganizationID: r.ProviderOrganizationID,
		DomainCode: r.DomainCode, SourceType: r.SourceType, SourceID: uuidPtr(r.SourceID),
		CaseID:       uuidPtr(r.CaseID),
		FulfilmentID: uuidPtr(r.FulfilmentID), AuthorizationID: uuidPtr(r.AuthorizationID),
		CurrentVersionNo: int(r.CurrentVersionNo), Status: r.Status,
		ServiceDateFrom: dateValue(r.ServiceDateFrom), ServiceDateTo: dateValue(r.ServiceDateTo),
		Channel: r.Channel, RejectReasonCode: r.RejectReasonCode,
		ReturnReasonCode: r.ReturnReasonCode, ReviewCommentMedical: r.ReviewCommentMedical,
		ReviewCommentFinancial: r.ReviewCommentFinancial, ClosedAt: r.ClosedAt,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

// versionRow is the same story for the four version reads.
type versionRow struct {
	ID               uuid.UUID
	ClaimID          uuid.UUID
	VersionNo        int32
	Status           string
	SubmittedAt      *time.Time
	SubmittedBy      uuid.NullUUID
	ReturnedAt       *time.Time
	ReturnedBy       uuid.NullUUID
	ReturnReasonCode *string
	ReturnReasonText *string
	Snapshot         []byte
	CreatedAt        time.Time
	RowVersion       int64
}

func createdVersionRow(r sqlcgen.CreateClaimVersionRow) versionRow { return versionRow(r) }
func draftVersionRow(r sqlcgen.GetClaimDraftVersionRow) versionRow { return versionRow(r) }
func versionByNoRow(r sqlcgen.GetClaimVersionByNoRow) versionRow   { return versionRow(r) }
func listVersionRow(r sqlcgen.ListClaimVersionsRow) versionRow     { return versionRow(r) }

func versionOf(r versionRow) application.VersionRecord {
	return application.VersionRecord{
		ID: r.ID, ClaimID: r.ClaimID, VersionNo: int(r.VersionNo), Status: r.Status,
		SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
		ReturnedAt: r.ReturnedAt, ReturnedBy: uuidPtr(r.ReturnedBy),
		ReturnReasonCode: r.ReturnReasonCode, ReturnReasonText: r.ReturnReasonText,
		Snapshot: r.Snapshot, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

func uuidPtr(v uuid.NullUUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := v.UUID
	return &id
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// emptyToNil is the other half of the COALESCE the queries wrap a nullable numeric in:
// the empty string is how a NULL column comes back, and it becomes a nil pointer
// rather than an amount of zero — which would be a different fact entirely.
func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	value := s
	return &value
}

func dateParam(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func optDate(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return dateParam(*t)
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

// pageSize clamps a repository page size; the service has already clamped the caller's.
func pageSize(n int) int32 {
	if n <= 0 {
		return 1
	}
	return int32(n) //nolint:gosec // clamped by httpx.ClampLimit before it reaches here
}

// adjustmentRow is the one shape the three adjustment reads share. Same story as the claim
// and the version above: sqlc gives each query its own struct, and one mapper over one shape
// is what keeps a column from being forgotten in the third copy of it.
type adjustmentRow struct {
	ID                   uuid.UUID
	ClaimID              uuid.UUID
	VersionNo            int32
	ClaimLineID          uuid.NullUUID
	AdjustmentType       string
	Amount               string
	PayerAmount          string
	MemberAmount         string
	CurrencyCode         string
	ReasonCode           string
	ReasonText           *string
	SourceType           string
	SourceID             uuid.NullUUID
	ReversesAdjustmentID uuid.NullUUID
	CreatedBy            uuid.NullUUID
	CreatedAt            time.Time
}

func createdAdjustmentRow(r sqlcgen.CreateClaimAdjustmentRow) adjustmentRow {
	return adjustmentRow(r)
}
func gotAdjustmentRow(r sqlcgen.GetClaimAdjustmentRow) adjustmentRow { return adjustmentRow(r) }
func listedAdjustmentRow(r sqlcgen.ListClaimAdjustmentsRow) adjustmentRow {
	return adjustmentRow(r)
}

func adjustmentOf(r adjustmentRow) application.AdjustmentRecord {
	return application.AdjustmentRecord{
		ID: r.ID, ClaimID: r.ClaimID, VersionNo: int(r.VersionNo),
		ClaimLineID: uuidPtr(r.ClaimLineID), AdjustmentType: r.AdjustmentType,
		Amount: r.Amount, PayerAmount: r.PayerAmount, MemberAmount: r.MemberAmount,
		CurrencyCode: r.CurrencyCode, ReasonCode: r.ReasonCode, ReasonText: r.ReasonText,
		SourceType: r.SourceType, SourceID: uuidPtr(r.SourceID),
		ReversesAdjustmentID: uuidPtr(r.ReversesAdjustmentID),
		CreatedBy:            uuidPtr(r.CreatedBy), CreatedAt: r.CreatedAt,
	}
}
