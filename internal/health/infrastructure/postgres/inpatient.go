package healthpg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The two constraints this package turns into errors of its own rather than into a 500. Both
// are rules the schema states and the service states again, and both are reached by a caller
// racing another one: the message they get has to be the same one the service would have
// given, or a duplicate would read as an outage.
const (
	// uniqueViolation is declared beside the report repository's chain index; this file
	// reuses it rather than spelling the SQLSTATE twice.
	exclusionViolation = "23P01"

	constraintOpenStay        = "uq_inpatient_stay_open"
	constraintPendingExtend   = "uq_stay_extension_pending"
	constraintSegmentOverlap  = "ex_stay_segment_overlap"
	constraintExtensionSeqNo  = "uq_stay_extension_sequence"
	constraintStayRequestOnce = "uq_inpatient_stay_request"
)

// Stays implements application.StayRepository over the tables of migration 000033. It is
// stateless: every method takes the caller's tenant-bound transaction, so RLS is active for
// every statement and nothing here can read another tenant's admissions.
type Stays struct{}

// NewStays returns the inpatient stay repository.
func NewStays() *Stays { return &Stays{} }

var _ application.StayRepository = (*Stays)(nil)

// CreateStay implements application.StayRepository.
//
// The insert is followed by a read of the same row, because the record this package hands
// back carries two facts the insert cannot return: whose case it is and how sensitive that
// case is. Both come from a join, and `INSERT … RETURNING` has nothing to join to. One extra
// round trip on a create is a better trade than a record that is whole in five reads and
// short in the sixth — in this package a missing sensitivity would mean a projection decided
// on a blank.
func (r Stays) CreateStay(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewStayRow,
) (application.StayRecord, error) {
	row, err := sqlcgen.New(tx).CreateInpatientStay(ctx, sqlcgen.CreateInpatientStayParams{
		TenantID: tenantID, CaseID: in.CaseID,
		ProviderOrganizationID: in.ProviderOrganizationID, LocationID: optUUID(in.LocationID),
		AttendingPractitionerID: optUUID(in.AttendingPractitionerID),
		AdmissionAt:             in.AdmissionAt, EstimatedDays: dayCount(in.EstimatedDays),
		ExpectedDischargeAt: in.ExpectedDischargeAt, ServiceRequestID: in.ServiceRequestID,
		AdmissionDiagnosisID: optUUID(in.AdmissionDiagnosisID), ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			switch pgErr.ConstraintName {
			case constraintOpenStay, constraintStayRequestOnce:
				// The rule of v1.2 10.4 step 3, refused by the index rather than by a
				// count somebody took first. Two callers arriving in the same millisecond
				// both pass a count; only one of them passes this.
				return application.StayRecord{}, application.ErrStayAlreadyOpen
			}
		}
		return application.StayRecord{}, fmt.Errorf("health: create inpatient stay: %w", err)
	}
	return r.GetStay(ctx, tx, tenantID, row.ID, application.Scope{})
}

// GetStay implements application.StayRepository.
func (Stays) GetStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.StayRecord, error) {
	row, err := sqlcgen.New(tx).GetInpatientStay(ctx, sqlcgen.GetInpatientStayParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StayRecord{}, application.ErrStayNotFound
	}
	if err != nil {
		return application.StayRecord{}, fmt.Errorf("health: get inpatient stay: %w", err)
	}
	return stayOf(fetchedStayRow(row)), nil
}

// LockStay implements application.StayRepository.
func (Stays) LockStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.StayRecord, error) {
	row, err := sqlcgen.New(tx).LockInpatientStay(ctx, sqlcgen.LockInpatientStayParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StayRecord{}, application.ErrStayNotFound
	}
	if err != nil {
		return application.StayRecord{}, fmt.Errorf("health: lock inpatient stay: %w", err)
	}
	return stayOf(lockedStayRow(row)), nil
}

// LockStayByRequest implements application.StayRepository.
func (Stays) LockStayByRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (
	application.StayRecord, error,
) {
	row, err := sqlcgen.New(tx).LockInpatientStayByRequest(ctx,
		sqlcgen.LockInpatientStayByRequestParams{TenantID: tenantID, ServiceRequestID: requestID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StayRecord{}, application.ErrStayNotFound
	}
	if err != nil {
		return application.StayRecord{}, fmt.Errorf("health: lock inpatient stay by request: %w", err)
	}
	return stayOf(lockedStayByRequestRow(row)), nil
}

// ListStays implements application.StayRepository.
func (Stays) ListStays(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.StayQuery,
) ([]application.StayRecord, error) {
	params := sqlcgen.ListInpatientStaysParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		CaseID: optUUID(q.CaseID), PersonID: optUUID(q.PersonID),
		ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		Status:                 optionalString(q.Status),
		AdmittedFrom:           q.AdmittedFrom, AdmittedTo: q.AdmittedTo,
		PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListInpatientStays(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("health: list inpatient stays: %w", err)
	}
	out := make([]application.StayRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, stayOf(listedStayRow(row)))
	}
	return out, nil
}

// CountOpenStays implements application.StayRepository.
func (Stays) CountOpenStays(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).CountOpenInpatientStays(ctx, sqlcgen.CountOpenInpatientStaysParams{
		TenantID: tenantID, CaseID: caseID,
	})
	if err != nil {
		return 0, fmt.Errorf("health: count open inpatient stays: %w", err)
	}
	return int(n), nil
}

// AuthorizeStay implements application.StayRepository.
func (Stays) AuthorizeStay(ctx context.Context, tx pgx.Tx, tenantID, id, authorizationID uuid.UUID,
	authorizedDays string, actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).AuthorizeInpatientStay(ctx, sqlcgen.AuthorizeInpatientStayParams{
		TenantID: tenantID, ID: id, AuthorizationID: uuid.NullUUID{UUID: authorizationID, Valid: true},
		AuthorizedDays: authorizedDays, ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("health: authorize inpatient stay: %w", err)
	}
	return affected > 0, nil
}

// RejectStay implements application.StayRepository.
func (Stays) RejectStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).RejectInpatientStay(ctx, sqlcgen.RejectInpatientStayParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("health: reject inpatient stay: %w", err)
	}
	return affected > 0, nil
}

// AdmitStay implements application.StayRepository.
func (Stays) AdmitStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).AdmitInpatientStay(ctx, sqlcgen.AdmitInpatientStayParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("health: admit inpatient stay: %w", err)
	}
	return affected > 0, nil
}

// AddAuthorizedDays implements application.StayRepository.
func (Stays) AddAuthorizedDays(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	additionalDays string, expectedDischargeAt time.Time, actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ExtendInpatientStayAuthorization(ctx,
		sqlcgen.ExtendInpatientStayAuthorizationParams{
			TenantID: tenantID, ID: id, AdditionalDays: additionalDays,
			ExpectedDischargeAt: expectedDischargeAt, ActorID: optUUID(actorID),
		})
	if err != nil {
		return false, fmt.Errorf("health: extend inpatient stay authorization: %w", err)
	}
	return affected > 0, nil
}

// DischargeStay implements application.StayRepository. The statement's predicate is the whole
// precondition, so "no row matched" is the answer to both "somebody else moved it" and "it
// was already discharged"; the caller has read the row under FOR UPDATE and has already
// separated those two, so what is left here is the version.
func (Stays) DischargeStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.DischargeRow,
) error {
	affected, err := sqlcgen.New(tx).DischargeInpatientStay(ctx, sqlcgen.DischargeInpatientStayParams{
		TenantID: tenantID, ID: id, DischargeAt: &in.DischargeAt,
		ActualDays: in.ActualDays, ReleasedDays: in.ReleasedDays,
		OverAuthorization: in.OverAuthorization, ActorID: optUUID(in.ActorID),
		ExpectedRowVersion: in.Expected,
	})
	if err != nil {
		return fmt.Errorf("health: discharge inpatient stay: %w", err)
	}
	if affected == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// CancelStay implements application.StayRepository.
func (Stays) CancelStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, reasonCode string,
	actorID *uuid.UUID, expected int64,
) error {
	affected, err := sqlcgen.New(tx).CancelInpatientStay(ctx, sqlcgen.CancelInpatientStayParams{
		TenantID: tenantID, ID: id, ReasonCode: &reasonCode, ActorID: optUUID(actorID),
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("health: cancel inpatient stay: %w", err)
	}
	if affected == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// CreateExtension implements application.StayRepository. The partial unique index is what
// refuses a second undecided extension, so two callers racing each other are refused by the
// database rather than by whichever count happened to run first.
func (Stays) CreateExtension(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewStayExtensionRow,
) (application.StayExtensionRecord, error) {
	row, err := sqlcgen.New(tx).CreateStayExtension(ctx, sqlcgen.CreateStayExtensionParams{
		TenantID: tenantID, StayID: in.StayID, SequenceNo: dayCount(in.SequenceNo),
		AdditionalDays: dayCount(in.AdditionalDays), ReasonCode: in.ReasonCode,
		ReasonText: in.ReasonText, ServiceRequestID: in.ServiceRequestID,
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			switch pgErr.ConstraintName {
			case constraintPendingExtend, constraintExtensionSeqNo:
				return application.StayExtensionRecord{}, application.ErrStayExtensionPending
			}
		}
		return application.StayExtensionRecord{}, fmt.Errorf("health: create stay extension: %w", err)
	}
	return extensionOf(createdExtensionRow(row)), nil
}

// ListExtensions implements application.StayRepository.
func (Stays) ListExtensions(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) (
	[]application.StayExtensionRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListStayExtensions(ctx, sqlcgen.ListStayExtensionsParams{
		TenantID: tenantID, StayID: stayID,
	})
	if err != nil {
		return nil, fmt.Errorf("health: list stay extensions: %w", err)
	}
	out := make([]application.StayExtensionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, extensionOf(listedExtensionRow(row)))
	}
	return out, nil
}

// LockExtensionByRequest implements application.StayRepository.
func (Stays) LockExtensionByRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (
	application.StayExtensionRecord, error,
) {
	row, err := sqlcgen.New(tx).LockStayExtensionByRequest(ctx,
		sqlcgen.LockStayExtensionByRequestParams{TenantID: tenantID, ServiceRequestID: requestID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StayExtensionRecord{}, application.ErrExtensionNotFound
	}
	if err != nil {
		return application.StayExtensionRecord{}, fmt.Errorf("health: lock stay extension by request: %w", err)
	}
	return extensionOf(lockedExtensionRow(row)), nil
}

// NextExtensionSequence implements application.StayRepository.
func (Stays) NextExtensionSequence(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).NextStayExtensionSequence(ctx, sqlcgen.NextStayExtensionSequenceParams{
		TenantID: tenantID, StayID: stayID,
	})
	if err != nil {
		return 0, fmt.Errorf("health: next stay extension sequence: %w", err)
	}
	return int(n), nil
}

// CountPendingExtensions implements application.StayRepository.
func (Stays) CountPendingExtensions(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).CountPendingStayExtensions(ctx, sqlcgen.CountPendingStayExtensionsParams{
		TenantID: tenantID, StayID: stayID,
	})
	if err != nil {
		return 0, fmt.Errorf("health: count pending stay extensions: %w", err)
	}
	return int(n), nil
}

// ApproveExtension implements application.StayRepository.
func (Stays) ApproveExtension(ctx context.Context, tx pgx.Tx, tenantID, id, authorizationID uuid.UUID,
	actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ApproveStayExtension(ctx, sqlcgen.ApproveStayExtensionParams{
		TenantID: tenantID, ID: id,
		AuthorizationID: uuid.NullUUID{UUID: authorizationID, Valid: true},
		ActorID:         optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("health: approve stay extension: %w", err)
	}
	return affected > 0, nil
}

// RejectExtension implements application.StayRepository.
func (Stays) RejectExtension(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).RejectStayExtension(ctx, sqlcgen.RejectStayExtensionParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("health: reject stay extension: %w", err)
	}
	return affected > 0, nil
}

// CancelExtensions implements application.StayRepository.
func (Stays) CancelExtensions(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID,
	actorID *uuid.UUID,
) (int, error) {
	affected, err := sqlcgen.New(tx).CancelStayExtensions(ctx, sqlcgen.CancelStayExtensionsParams{
		TenantID: tenantID, StayID: stayID, ActorID: optUUID(actorID),
	})
	if err != nil {
		return 0, fmt.Errorf("health: cancel stay extensions: %w", err)
	}
	return int(affected), nil
}

// ReplaceSegments implements application.StayRepository. The delete and the inserts run in the
// caller's transaction, so a stay is never momentarily without its segments as far as any
// other reader is concerned.
//
// The exclusion constraint is what refuses an overlapping set. The service checks the same
// rule first so the caller learns which two lines collided, but this is the authority: a set
// written by anything else is refused too.
func (Stays) ReplaceSegments(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID,
	rows []application.NewSegmentRow,
) error {
	q := sqlcgen.New(tx)
	if err := q.DeleteStaySegments(ctx, sqlcgen.DeleteStaySegmentsParams{
		TenantID: tenantID, StayID: stayID,
	}); err != nil {
		return fmt.Errorf("health: delete stay segments: %w", err)
	}
	for _, row := range rows {
		if _, err := q.CreateStaySegment(ctx, sqlcgen.CreateStaySegmentParams{
			TenantID: tenantID, StayID: row.StayID, SegmentType: row.SegmentType,
			StartsAt: row.StartsAt, EndsAt: row.EndsAt, RoomCode: row.RoomCode,
			BedCode: row.BedCode, ActorID: optUUID(row.ActorID),
		}); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation &&
				pgErr.ConstraintName == constraintSegmentOverlap {
				return application.ErrSegmentOverlap
			}
			return fmt.Errorf("health: create stay segment: %w", err)
		}
	}
	return nil
}

// ListSegments implements application.StayRepository.
func (Stays) ListSegments(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) (
	[]application.StaySegmentRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListStaySegments(ctx, sqlcgen.ListStaySegmentsParams{
		TenantID: tenantID, StayID: stayID,
	})
	if err != nil {
		return nil, fmt.Errorf("health: list stay segments: %w", err)
	}
	out := make([]application.StaySegmentRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.StaySegmentRecord{
			ID: row.ID, StayID: row.StayID, SegmentType: row.SegmentType,
			StartsAt: row.StartsAt, EndsAt: row.EndsAt, RoomCode: row.RoomCode,
			BedCode: row.BedCode, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
		})
	}
	return out, nil
}

// EndOpenSegments implements application.StayRepository.
func (Stays) EndOpenSegments(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID,
	endsAt time.Time, actorID *uuid.UUID,
) (int, error) {
	affected, err := sqlcgen.New(tx).EndOpenStaySegments(ctx, sqlcgen.EndOpenStaySegmentsParams{
		TenantID: tenantID, StayID: stayID, EndsAt: &endsAt, ActorID: optUUID(actorID),
	})
	if err != nil {
		return 0, fmt.Errorf("health: end open stay segments: %w", err)
	}
	return int(affected), nil
}

// GetStayCase implements application.StayRepository.
func (Stays) GetStayCase(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID,
	scope application.Scope,
) (application.StayCaseRecord, error) {
	row, err := sqlcgen.New(tx).GetInpatientStayCase(ctx, sqlcgen.GetInpatientStayCaseParams{
		TenantID: tenantID, ID: caseID, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.StayCaseRecord{}, application.ErrCaseNotFound
	}
	if err != nil {
		return application.StayCaseRecord{}, fmt.Errorf("health: get stay case: %w", err)
	}
	return application.StayCaseRecord{
		ID: row.ID, PersonID: row.PersonID, ProgramID: row.ProgramID,
		EnrollmentID: row.EnrollmentID, CaseType: row.CaseType,
		ProviderOrganizationID: uuidPtr(row.ProviderOrganizationID),
		Status:                 row.Status, Sensitivity: row.Sensitivity,
	}, nil
}

// GetAdmissionService implements application.StayRepository.
func (Stays) GetAdmissionService(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (
	application.AdmissionServiceRecord, error,
) {
	row, err := sqlcgen.New(tx).GetInpatientAdmissionService(ctx,
		sqlcgen.GetInpatientAdmissionServiceParams{TenantID: tenantID, Code: code})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.AdmissionServiceRecord{}, application.ErrAdmissionServiceUnknown
	}
	if err != nil {
		return application.AdmissionServiceRecord{}, fmt.Errorf("health: get admission service: %w", err)
	}
	return application.AdmissionServiceRecord{
		ID: row.ID, Code: row.Code, DefaultUnitType: row.DefaultUnitType,
		RequiresProvider: row.RequiresProvider, Active: row.Active,
	}, nil
}

// GetDiagnosisCase implements application.StayRepository.
func (Stays) GetDiagnosisCase(ctx context.Context, tx pgx.Tx, tenantID, diagnosisID uuid.UUID) (
	uuid.UUID, error,
) {
	row, err := sqlcgen.New(tx).GetStayAdmissionDiagnosis(ctx,
		sqlcgen.GetStayAdmissionDiagnosisParams{TenantID: tenantID, ID: diagnosisID})
	if errors.Is(err, pgx.ErrNoRows) {
		// An unknown diagnosis and one belonging to another case are the same refusal: the
		// caller has named something that is not this admission's, and which of the two it
		// was is not its business.
		return uuid.Nil, application.ErrAdmissionDiagnosisMismatch
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("health: get admission diagnosis: %w", err)
	}
	return row.CaseID, nil
}

// WindowSetting implements application.StayRepository. A missing key is not an error: the
// service falls back to its documented default, and a tenant that has never thought about the
// admission window needs no row at all.
func (Stays) WindowSetting(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (int, bool, error) {
	raw, err := sqlcgen.New(tx).GetInpatientWindowSetting(ctx, sqlcgen.GetInpatientWindowSettingParams{
		TenantID: tenantID, SettingKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("health: read %s: %w", key, err)
	}
	value, ok := wholeNumber(raw)
	if !ok {
		// A setting that is not a whole number of days is a setting nobody can honour.
		// Falling back is better than refusing every admission of that tenant until an
		// operator notices.
		return 0, false, nil
	}
	return value, true, nil
}

// wholeNumber parses a settings value that is meant to be a count of days. It is deliberately
// strict: anything with a decimal point, a sign or a stray character is not a day count.
func wholeNumber(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	value := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
		value = value*10 + int(r-'0')
		if value > 1_000_000 {
			return 0, false
		}
	}
	return value, true
}

// dayCount narrows a day count or a sequence number to the int32 the column is. The domain
// has already bounded both — a day count to a year and a sequence to what the table holds —
// so the clamp is unreachable; it exists because a silent wrap on a 64-bit platform is not a
// failure mode worth leaving open for whatever calls this next.
func dayCount(n int) int32 {
	if n < 1 {
		return 1
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}
