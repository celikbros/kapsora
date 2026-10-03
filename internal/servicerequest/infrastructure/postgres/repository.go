// Package servicerequestpg implements the service request repository with sqlc. It is
// stateless: every method takes the caller's tenant-bound transaction, so RLS is active for
// every statement and nothing here can read another tenant's request.
//
// The provider boundary lives here rather than above: every read takes the caller's scope
// and hands it to SQL, so a request outside it is genuinely not returned. That is what lets
// the application layer answer 404 without ever having held the row.
//
// Where a read already exists elsewhere it is reused rather than rewritten: the eligibility
// inputs come from the benefit module's own queries, the balances from the entitlement
// ledger's read side, and the plan version from the benefit application layer.
package servicerequestpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	rulesdomain "github.com/celikbros/kapsora/internal/rules/domain"
	"github.com/celikbros/kapsora/internal/rules/engine"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
)

// PostgreSQL error codes mapped to named application errors.
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
	// integrityViolation is what the version and item guards of migration 000006 raise
	// when somebody writes to a version that is no longer a draft.
	integrityViolation = "23000"
)

// Repository implements application.Repository.
type Repository struct {
	ledger *ledger.Ledger
}

// New returns the repository. The ledger is used for its read side only — ResolveAccounts
// lists what a person can already spend from — and never for a movement: submitting a
// request reserves nothing.
func New() *Repository { return &Repository{ledger: ledger.NewLedger(nil)} }

var _ application.Repository = (*Repository)(nil)

// CreateRequest implements application.Repository.
func (Repository) CreateRequest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewRequestRow,
) (application.RequestRecord, error) {
	row, err := sqlcgen.New(tx).CreateServiceRequest(ctx, sqlcgen.CreateServiceRequestParams{
		TenantID: tenantID, RequestReference: in.Reference, RequestType: in.RequestType,
		PersonID: in.PersonID, ProgramID: in.ProgramID, EnrollmentID: in.EnrollmentID,
		ProviderTenantOrganizationID: optUUID(in.ProviderOrganizationID),
		ServiceDate:                  dateOf(in.ServiceDate),
		RequestedStartAt:             in.RequestedStartAt, RequestedEndAt: in.RequestedEndAt,
		Channel: in.Channel, SupersedesRequestID: optUUID(in.SupersedesRequestID),
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return application.RequestRecord{}, application.ErrReferenceCollision
		}
		return application.RequestRecord{}, fmt.Errorf("servicerequest: create request: %w", err)
	}
	return application.RequestRecord{
		ID: row.ID, Reference: row.RequestReference, RequestType: in.RequestType,
		PersonID: in.PersonID, ProgramID: in.ProgramID, EnrollmentID: in.EnrollmentID,
		ProviderOrganizationID: in.ProviderOrganizationID, ServiceDate: in.ServiceDate,
		RequestedStartAt: in.RequestedStartAt, RequestedEndAt: in.RequestedEndAt,
		Channel: in.Channel, Status: "DRAFT", CurrentVersionNo: 1,
		SupersedesRequestID: in.SupersedesRequestID,
		CreatedAt:           row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// GetRequest implements application.Repository.
func (Repository) GetRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.RequestRecord, error) {
	row, err := sqlcgen.New(tx).GetServiceRequest(ctx, sqlcgen.GetServiceRequestParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RequestRecord{}, application.ErrRequestNotFound
	}
	if err != nil {
		return application.RequestRecord{}, fmt.Errorf("servicerequest: get request: %w", err)
	}
	return requestOf(getRow(row)), nil
}

// LockRequest implements application.Repository.
func (Repository) LockRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.RequestRecord, error) {
	row, err := sqlcgen.New(tx).LockServiceRequest(ctx, sqlcgen.LockServiceRequestParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RequestRecord{}, application.ErrRequestNotFound
	}
	if err != nil {
		return application.RequestRecord{}, fmt.Errorf("servicerequest: lock request: %w", err)
	}
	return requestOf(lockRow(row)), nil
}

// ListRequests implements application.Repository.
func (Repository) ListRequests(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.RequestQuery,
) ([]application.RequestRecord, error) {
	params := sqlcgen.ListServiceRequestsParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		Status: optionalString(q.Status), Channel: optionalString(q.Channel),
		PersonID: optUUID(q.PersonID), ProgramID: optUUID(q.ProgramID),
		ProviderID:      optUUID(q.ProviderOrganizationID),
		ServiceDateFrom: optDate(q.ServiceDateFrom), ServiceDateTo: optDate(q.ServiceDateTo),
		CreatedFrom: q.CreatedFrom, CreatedTo: q.CreatedTo,
		PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListServiceRequests(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("servicerequest: list requests: %w", err)
	}
	out := make([]application.RequestRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, requestOf(listRow(row)))
	}
	return out, nil
}

// UpdateDraftHeader implements application.Repository.
func (Repository) UpdateDraftHeader(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.DraftHeaderRow, expected int64,
) error {
	rows, err := sqlcgen.New(tx).UpdateServiceRequestDraftHeader(ctx, sqlcgen.UpdateServiceRequestDraftHeaderParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
		ProviderTenantOrganizationID: optUUID(in.ProviderOrganizationID),
		ServiceDate:                  dateOf(in.ServiceDate),
		RequestedStartAt:             in.RequestedStartAt, RequestedEndAt: in.RequestedEndAt,
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return fmt.Errorf("servicerequest: update draft header: %w", err)
	}
	return affected(rows, application.ErrVersionMismatch)
}

// TouchRequest implements application.Repository.
func (Repository) TouchRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID) error {
	rows, err := sqlcgen.New(tx).TouchServiceRequest(ctx, sqlcgen.TouchServiceRequestParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID),
	})
	if err != nil {
		return fmt.Errorf("servicerequest: touch request: %w", err)
	}
	return affected(rows, application.ErrRequestNotFound)
}

// MarkSubmitted implements application.Repository.
func (Repository) MarkSubmitted(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.SubmitRow, expected int64,
) error {
	submittedAt := in.SubmittedAt
	rows, err := sqlcgen.New(tx).MarkServiceRequestSubmitted(ctx, sqlcgen.MarkServiceRequestSubmittedParams{
		TenantID: tenantID, ID: id, RowVersion: expected, Status: in.Status,
		SubmittedAt:             &submittedAt,
		EligibilityEvaluationID: optUUID(in.EligibilityEvaluationID),
		RuleEvaluationID:        optUUID(in.RuleEvaluationID),
		RequiredDocumentTypes:   in.RequiredDocumentTypes,
		ReviewComment:           in.ReviewComment, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return fmt.Errorf("servicerequest: mark submitted: %w", err)
	}
	return affected(rows, application.ErrVersionMismatch)
}

// MarkReturned implements application.Repository.
func (Repository) MarkReturned(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.ReturnRow, expected int64,
) error {
	rows, err := sqlcgen.New(tx).MarkServiceRequestReturned(ctx, sqlcgen.MarkServiceRequestReturnedParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
		CurrentVersionNo: versionNo(in.CurrentVersionNo), ReturnReasonCode: &in.ReasonCode,
		ReviewComment: in.ReviewComment, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return fmt.Errorf("servicerequest: mark returned: %w", err)
	}
	return affected(rows, application.ErrVersionMismatch)
}

// MarkRejected implements application.Repository.
func (Repository) MarkRejected(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.RejectRow, expected int64,
) error {
	decidedAt := in.DecidedAt
	rows, err := sqlcgen.New(tx).MarkServiceRequestRejected(ctx, sqlcgen.MarkServiceRequestRejectedParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
		RejectReasonCode: &in.ReasonCode, ReviewComment: in.ReviewComment,
		DecidedAt: &decidedAt, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return fmt.Errorf("servicerequest: mark rejected: %w", err)
	}
	return affected(rows, application.ErrVersionMismatch)
}

// MarkApproved implements application.Repository.
func (Repository) MarkApproved(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.ApproveRow, expected int64,
) error {
	rows, err := sqlcgen.New(tx).MarkServiceRequestApproved(ctx, sqlcgen.MarkServiceRequestApprovedParams{
		TenantID: tenantID, ID: id, RowVersion: expected, Status: in.Status,
		ReviewComment: in.ReviewComment, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return fmt.Errorf("servicerequest: mark approved: %w", err)
	}
	return affected(rows, application.ErrVersionMismatch)
}

// MarkCancelled implements application.Repository.
func (Repository) MarkCancelled(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.CancelRow, expected int64,
) error {
	cancelledAt := in.CancelledAt
	rows, err := sqlcgen.New(tx).MarkServiceRequestCancelled(ctx, sqlcgen.MarkServiceRequestCancelledParams{
		TenantID: tenantID, ID: id, RowVersion: expected,
		DecidedAt: &cancelledAt, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return fmt.Errorf("servicerequest: mark cancelled: %w", err)
	}
	return affected(rows, application.ErrVersionMismatch)
}

// CreateVersion implements application.Repository.
func (Repository) CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewVersionRow,
) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).CreateServiceRequestVersion(ctx, sqlcgen.CreateServiceRequestVersionParams{
		TenantID: tenantID, ServiceRequestID: in.ServiceRequestID, VersionNo: versionNo(in.VersionNo),
		ReturnedAt: in.ReturnedAt, ReturnedBy: optUUID(in.ReturnedBy),
		ReturnReasonCode: in.ReturnReasonCode, ReturnReasonText: in.ReturnReasonText,
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return application.VersionRecord{}, application.ErrRequestNotFound
		}
		return application.VersionRecord{}, fmt.Errorf("servicerequest: create version: %w", err)
	}
	return application.VersionRecord{
		ID: row.ID, ServiceRequestID: in.ServiceRequestID, VersionNo: int(row.VersionNo),
		Status: row.Status, ReturnedAt: in.ReturnedAt, ReturnedBy: in.ReturnedBy,
		ReturnReasonCode: in.ReturnReasonCode, ReturnReasonText: in.ReturnReasonText,
		CreatedAt: row.CreatedAt,
	}, nil
}

// GetDraftVersion implements application.Repository.
func (Repository) GetDraftVersion(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetServiceRequestDraftVersion(ctx, sqlcgen.GetServiceRequestDraftVersionParams{
		TenantID: tenantID, ServiceRequestID: requestID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrDraftNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("servicerequest: get draft version: %w", err)
	}
	return versionOf(draftVersionRow(row)), nil
}

// GetVersionByNo implements application.Repository.
func (Repository) GetVersionByNo(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID,
	number int,
) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetServiceRequestVersionByNo(ctx, sqlcgen.GetServiceRequestVersionByNoParams{
		TenantID: tenantID, ServiceRequestID: requestID, VersionNo: versionNo(number),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrVersionNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("servicerequest: get version: %w", err)
	}
	return versionOf(numberedVersionRow(row)), nil
}

// ListVersions implements application.Repository.
func (Repository) ListVersions(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) ([]application.VersionRecord, error) {
	rows, err := sqlcgen.New(tx).ListServiceRequestVersions(ctx, sqlcgen.ListServiceRequestVersionsParams{
		TenantID: tenantID, ServiceRequestID: requestID,
	})
	if err != nil {
		return nil, fmt.Errorf("servicerequest: list versions: %w", err)
	}
	out := make([]application.VersionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, versionOf(listVersionRow(row)))
	}
	return out, nil
}

// FreezeVersion implements application.Repository.
func (Repository) FreezeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	in application.FreezeRow,
) error {
	submittedAt := in.SubmittedAt
	rows, err := sqlcgen.New(tx).FreezeServiceRequestVersion(ctx, sqlcgen.FreezeServiceRequestVersionParams{
		TenantID: tenantID, ID: versionID, SnapshotJson: in.Snapshot,
		SubmittedAt: &submittedAt, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return guardError(err, fmt.Errorf("servicerequest: freeze version: %w", err))
	}
	return affected(rows, application.ErrVersionImmutable)
}

// SupersedeVersion implements application.Repository.
func (Repository) SupersedeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) error {
	rows, err := sqlcgen.New(tx).SupersedeServiceRequestVersion(ctx, sqlcgen.SupersedeServiceRequestVersionParams{
		TenantID: tenantID, ID: versionID,
	})
	if err != nil {
		return guardError(err, fmt.Errorf("servicerequest: supersede version: %w", err))
	}
	return affected(rows, application.ErrVersionNotFound)
}

// ListItems implements application.Repository.
func (Repository) ListItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.ItemRecord, error) {
	rows, err := sqlcgen.New(tx).ListServiceRequestItems(ctx, sqlcgen.ListServiceRequestItemsParams{
		TenantID: tenantID, ServiceRequestVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("servicerequest: list items: %w", err)
	}
	out := make([]application.ItemRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ItemRecord{
			ID: row.ID, VersionID: row.ServiceRequestVersionID, LineNo: int(row.LineNo),
			ServiceDefinitionID: row.ServiceDefinitionID,
			RequestedQuantity:   trimDecimal(row.RequestedQuantity), UnitType: row.UnitType,
			RequestedAmount: trimDecimalPtr(row.RequestedAmount), CurrencyCode: row.CurrencyCode,
			Status:             row.Status,
			ApprovedQuantity:   trimDecimalPtr(row.ApprovedQuantity),
			ApprovedAmount:     trimDecimalPtr(row.ApprovedAmount),
			DecisionReasonCode: row.DecisionReasonCode,
		})
	}
	return out, nil
}

// ReplaceItems implements application.Repository.
func (Repository) ReplaceItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	rows []application.NewItemRow,
) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeleteServiceRequestItems(ctx, sqlcgen.DeleteServiceRequestItemsParams{
		TenantID: tenantID, ServiceRequestVersionID: versionID,
	}); err != nil {
		return guardError(err, fmt.Errorf("servicerequest: delete items: %w", err))
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreateServiceRequestItemParams, 0, len(rows))
	for _, row := range rows {
		item := sqlcgen.CreateServiceRequestItemParams{
			TenantID: tenantID, ServiceRequestVersionID: versionID,
			LineNo:              lineNo(row.LineNo),
			ServiceDefinitionID: row.ServiceDefinitionID,
			RequestedQuantity:   row.RequestedQuantity.String(),
			UnitType:            row.UnitType, CurrencyCode: row.CurrencyCode,
		}
		if row.RequestedAmount != nil {
			amount := row.RequestedAmount.String()
			item.RequestedAmount = &amount
		}
		params = append(params, item)
	}
	if err := execBatch(q.CreateServiceRequestItem(ctx, params)); err != nil {
		return guardError(err, fmt.Errorf("servicerequest: create items: %w", err))
	}
	return nil
}

// DecideItems implements application.Repository.
func (Repository) DecideItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	rows []application.ItemDecisionRow,
) error {
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.DecideServiceRequestItemParams, 0, len(rows))
	for _, row := range rows {
		item := sqlcgen.DecideServiceRequestItemParams{
			TenantID: tenantID, ServiceRequestVersionID: versionID,
			LineNo: lineNo(row.LineNo), Status: row.Status,
			DecisionReasonCode: row.DecisionReasonCode,
		}
		if row.ApprovedQuantity != nil {
			quantity := row.ApprovedQuantity.String()
			item.ApprovedQuantity = &quantity
		}
		if row.ApprovedAmount != nil {
			amount := row.ApprovedAmount.String()
			item.ApprovedAmount = &amount
		}
		params = append(params, item)
	}
	if err := execBatch(sqlcgen.New(tx).DecideServiceRequestItem(ctx, params)); err != nil {
		return guardError(err, fmt.Errorf("servicerequest: decide items: %w", err))
	}
	return nil
}

// AppendStatusEvent implements application.Repository.
func (Repository) AppendStatusEvent(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.StatusEventRow,
) error {
	metadata, err := json.Marshal(nonNilMap(in.Metadata))
	if err != nil {
		return fmt.Errorf("servicerequest: encode status event metadata: %w", err)
	}
	if err := sqlcgen.New(tx).CreateServiceRequestStatusEvent(ctx, sqlcgen.CreateServiceRequestStatusEventParams{
		TenantID: tenantID, AggregateID: in.AggregateID,
		FromStatus: optionalString(in.FromStatus), ToStatus: in.ToStatus,
		TransitionCode: in.TransitionCode, ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		ActorID: optUUID(in.ActorID), MetadataJson: metadata,
	}); err != nil {
		return fmt.Errorf("servicerequest: append status event: %w", err)
	}
	return nil
}

// ListStatusEvents implements application.Repository.
func (Repository) ListStatusEvents(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) ([]application.StatusEventRecord, error) {
	rows, err := sqlcgen.New(tx).ListServiceRequestStatusEvents(ctx, sqlcgen.ListServiceRequestStatusEventsParams{
		TenantID: tenantID, AggregateID: requestID,
	})
	if err != nil {
		return nil, fmt.Errorf("servicerequest: list status events: %w", err)
	}
	out := make([]application.StatusEventRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.StatusEventRecord{
			ID: row.ID, FromStatus: row.FromStatus, ToStatus: row.ToStatus,
			TransitionCode: row.TransitionCode, ReasonCode: row.ReasonCode,
			ReasonText: row.ReasonText, OccurredAt: row.OccurredAt,
			ActorID: uuidPtr(row.ActorID),
		})
	}
	return out, nil
}

// GetEnrollment implements application.Repository.
func (Repository) GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID) (application.EnrollmentRecord, error) {
	row, err := sqlcgen.New(tx).GetServiceRequestEnrollment(ctx, sqlcgen.GetServiceRequestEnrollmentParams{
		TenantID: tenantID, ID: enrollmentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EnrollmentRecord{}, application.ErrEnrollmentMismatch
	}
	if err != nil {
		return application.EnrollmentRecord{}, fmt.Errorf("servicerequest: get enrollment: %w", err)
	}
	return application.EnrollmentRecord{
		ID: row.ID, PlanID: row.PlanID, ProgramID: row.ProgramID, Status: row.Status,
		PersonID: row.PersonID, ValidFrom: dateValue(row.ValidFrom), ValidTo: datePtr(row.ValidTo),
	}, nil
}

// ProviderOrganizationExists implements application.Repository.
func (Repository) ProviderOrganizationExists(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).ServiceRequestProviderOrganizationExists(ctx,
		sqlcgen.ServiceRequestProviderOrganizationExistsParams{TenantID: tenantID, ID: orgID})
	if err != nil {
		return false, fmt.Errorf("servicerequest: check provider organization: %w", err)
	}
	return ok, nil
}

// ListServiceDefinitions implements application.Repository.
func (Repository) ListServiceDefinitions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	ids []uuid.UUID,
) ([]application.ServiceDefinitionRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := sqlcgen.New(tx).ListServiceRequestServiceDefinitions(ctx,
		sqlcgen.ListServiceRequestServiceDefinitionsParams{TenantID: tenantID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("servicerequest: list service definitions: %w", err)
	}
	out := make([]application.ServiceDefinitionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ServiceDefinitionRecord{
			ID: row.ID, Code: row.Code, DefaultUnitType: row.DefaultUnitType,
			RequiresProvider: row.RequiresProvider, Active: row.Active,
		})
	}
	return out, nil
}

// LoadEligibility implements application.Repository. Every statement is a read: the
// accounts are listed, never opened, because opening one posts a GRANT ledger entry and
// this package must leave the ledger exactly as it found it.
func (r Repository) LoadEligibility(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
	programID *uuid.UUID, serviceDate time.Time,
) (application.EligibilityInput, error) {
	day := benefitdomain.DateOnly(serviceDate)
	q := sqlcgen.New(tx)
	out := application.EligibilityInput{Person: eligibility.Person{ID: personID}}

	person, err := q.GetPersonForEligibility(ctx, sqlcgen.GetPersonForEligibilityParams{
		TenantID: tenantID, ID: personID,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return out, nil
	case err != nil:
		return application.EligibilityInput{}, fmt.Errorf("servicerequest: read person: %w", err)
	}
	out.Person = eligibility.Person{ID: person.ID, Found: true, Status: person.Status}

	memberships, err := q.ListMembershipsForEligibility(ctx, sqlcgen.ListMembershipsForEligibilityParams{
		TenantID: tenantID, PersonID: personID,
	})
	if err != nil {
		return application.EligibilityInput{}, fmt.Errorf("servicerequest: list memberships: %w", err)
	}
	for _, m := range memberships {
		out.Memberships = append(out.Memberships, eligibility.Membership{
			ID: m.ID, Status: m.Status, ValidFrom: dateValue(m.ValidFrom), ValidTo: datePtr(m.ValidTo),
		})
	}

	enrollments, err := q.ListEnrollmentsForEligibility(ctx, sqlcgen.ListEnrollmentsForEligibilityParams{
		TenantID: tenantID, PersonID: personID,
	})
	if err != nil {
		return application.EligibilityInput{}, fmt.Errorf("servicerequest: list enrollments: %w", err)
	}
	for _, e := range enrollments {
		out.Enrollments = append(out.Enrollments, eligibility.Enrollment{
			ID: e.ID, PlanID: e.PlanID, PlanCode: e.PlanCode, PlanName: e.PlanName,
			ProgramID: e.ProgramID, Status: e.Status,
			ValidFrom: dateValue(e.ValidFrom), ValidTo: datePtr(e.ValidTo),
		})
	}

	// The enrollment the resolver will choose decides which plan version applies; it
	// derives the same choice from the same slice, so the two can never disagree.
	program := uuid.Nil
	if programID != nil {
		program = *programID
	}
	active := eligibility.SelectEnrollments(out.Enrollments, program, day)
	if len(active) == 0 {
		return out, nil
	}
	version, err := benefitapp.ResolvePlanVersion(ctx, tx, tenantID, active[0].PlanID, day)
	switch {
	case errors.Is(err, benefitapp.ErrNoPublishedVersion):
		return out, nil
	case err != nil:
		return application.EligibilityInput{}, err
	}
	out.PlanVersion = &eligibility.PlanVersion{ID: version.ID}

	// The submit gate reads the same mapping the check reads, out of the same table: the
	// two answers must not be able to differ, because the gate is what turns the check's
	// answer into a decision the member lives with.
	mappings, err := q.ListEligibilityMappings(ctx, sqlcgen.ListEligibilityMappingsParams{
		TenantID: tenantID, PlanVersionID: version.ID, ServiceDate: dateOf(day),
	})
	if err != nil {
		return application.EligibilityInput{}, fmt.Errorf("servicerequest: list entitlement mappings: %w", err)
	}
	out.Mappings = make(map[uuid.UUID]eligibility.Mapping, len(mappings))
	for _, m := range mappings {
		factor, err := benefitdomain.ParseQuantity(m.UnitFactor)
		if err != nil {
			return application.EligibilityInput{}, fmt.Errorf("servicerequest: entitlement mapping factor: %w", err)
		}
		out.Mappings[m.ServiceDefinitionID] = eligibility.Mapping{
			EntitlementCode: m.EntitlementCode, UnitFactor: factor,
		}
	}

	accounts, err := r.ledger.ResolveAccounts(ctx, tx, tenantID, personID, day)
	if err != nil {
		return application.EligibilityInput{}, err
	}
	for _, a := range accounts {
		if a.Status != ledger.AccountOpen {
			continue
		}
		out.Accounts = append(out.Accounts, eligibility.Account{
			ID: a.ID, EntitlementCode: a.Definition.Code, UnitType: a.Definition.UnitType,
			Available: a.Balances.Available, AllowOverdraft: a.Definition.AllowOverdraft, Shared: a.Shared,
		})
	}
	return out, nil
}

// CreateEligibilityEvaluation implements application.Repository.
func (Repository) CreateEligibilityEvaluation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewEligibilityEvaluationRow,
) error {
	if _, err := sqlcgen.New(tx).CreateEligibilityEvaluation(ctx, sqlcgen.CreateEligibilityEvaluationParams{
		ID: in.ID, TenantID: tenantID, PersonID: in.PersonID, ProgramID: optUUID(in.ProgramID),
		EnrollmentID: optUUID(in.EnrollmentID), PlanVersionID: optUUID(in.PlanVersionID),
		ProviderTenantOrganizationID: optUUID(in.ProviderOrgID),
		ServiceDate:                  dateOf(in.ServiceDate), Outcome: in.Outcome,
		RequestHash: in.RequestHash, RequestSnapshot: in.RequestSnapshot,
		ResultSnapshot: in.ResultSnapshot, DataClassification: "PERSONAL",
		EvaluatedAt: in.EvaluatedAt, EvaluatedBy: optUUID(in.EvaluatedBy),
	}); err != nil {
		return fmt.Errorf("servicerequest: create eligibility evaluation: %w", err)
	}
	return nil
}

// ListRuleVersions implements application.Repository.
func (Repository) ListRuleVersions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	purposes []string, serviceDate time.Time,
) ([]application.RuleVersion, error) {
	q := sqlcgen.New(tx)
	versions, err := q.ListPublishedRuleSetVersionsForPurposes(ctx,
		sqlcgen.ListPublishedRuleSetVersionsForPurposesParams{
			TenantID: tenantID, Purposes: purposes, ServiceDate: dateOf(serviceDate),
		})
	if err != nil {
		return nil, fmt.Errorf("servicerequest: list published rule set versions: %w", err)
	}
	out := make([]application.RuleVersion, 0, len(versions))
	for _, v := range versions {
		schema := map[string]string{}
		if len(v.InputSchema) > 0 {
			if err := json.Unmarshal(v.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("servicerequest: decode rule input schema: %w", err)
			}
		}
		rules, err := q.ListRules(ctx, sqlcgen.ListRulesParams{TenantID: tenantID, RuleSetVersionID: v.ID})
		if err != nil {
			return nil, fmt.Errorf("servicerequest: list rules: %w", err)
		}
		engineRules := make([]engine.Rule, 0, len(rules))
		for _, row := range rules {
			actions, err := decodeActions(row.Actions)
			if err != nil {
				return nil, err
			}
			params := map[string]any{}
			if len(row.ExplanationParams) > 0 {
				if err := json.Unmarshal(row.ExplanationParams, &params); err != nil {
					return nil, fmt.Errorf("servicerequest: decode rule explanation params: %w", err)
				}
			}
			engineRules = append(engineRules, engine.Rule{
				ID: row.ID, Code: row.Code, Priority: int(row.Priority), Condition: row.Condition,
				Actions: rulesdomain.EngineActions(actions), ExplanationCode: row.ExplanationCode,
				ExplanationParams: params, StopOnMatch: row.StopOnMatch, Active: row.Active,
			})
		}
		out = append(out, application.RuleVersion{
			ID: v.ID, RuleSetID: v.RuleSetID, RuleSetCode: v.RuleSetCode, Purpose: v.Purpose,
			VersionNo: int(v.VersionNo), InputSchema: schema, Rules: engineRules,
		})
	}
	return out, nil
}

// CreateRuleEvaluation implements application.Repository.
func (Repository) CreateRuleEvaluation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewRuleEvaluationRow, results []application.RuleEvaluationResultRow,
) (uuid.UUID, error) {
	q := sqlcgen.New(tx)
	duration := int32(in.DurationMs) //nolint:gosec // a rule pass is milliseconds, bounded by the engine budget
	header, err := q.CreateRuleEvaluation(ctx, sqlcgen.CreateRuleEvaluationParams{
		TenantID: tenantID, SubjectType: "SERVICE_REQUEST",
		SubjectID:        uuid.NullUUID{UUID: in.SubjectID, Valid: in.SubjectID != uuid.Nil},
		RuleSetVersionID: in.RuleSetVersionID, InputHash: in.InputHash,
		InputSnapshot: in.InputSnapshot, Outcome: in.Outcome, DurationMs: &duration,
		EvaluatedBy: optUUID(in.EvaluatedBy),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("servicerequest: create rule evaluation: %w", err)
	}
	if len(results) == 0 {
		return header.ID, nil
	}
	params := make([]sqlcgen.CreateRuleEvaluationResultParams, 0, len(results))
	for _, r := range results {
		payload, err := optionalObject(r.ActionPayload)
		if err != nil {
			return uuid.Nil, err
		}
		params = append(params, sqlcgen.CreateRuleEvaluationResultParams{
			TenantID: tenantID, EvaluationID: header.ID,
			Sequence: int32(r.Sequence), //nolint:gosec // one line per rule, bounded by the rule set size
			RuleID:   optUUID(r.RuleID), RuleCode: r.RuleCode, Matched: r.Matched,
			ActionType: r.ActionType, ActionPayload: payload,
			ExplanationCode: r.ExplanationCode, Severity: r.Severity,
		})
	}
	if err := execBatch(q.CreateRuleEvaluationResult(ctx, params)); err != nil {
		return uuid.Nil, fmt.Errorf("servicerequest: create rule evaluation results: %w", err)
	}
	return header.ID, nil
}

// ListDocumentEvidence implements application.Repository.
func (Repository) ListDocumentEvidence(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID,
	providerID *uuid.UUID, requiredTypes []string,
) ([]application.DocumentEvidence, error) {
	rows, err := sqlcgen.New(tx).ListServiceRequestDocumentEvidence(ctx, sqlcgen.ListServiceRequestDocumentEvidenceParams{
		TenantID: tenantID, RequestID: requestID, ProviderID: optUUID(providerID), RequiredTypes: requiredTypes,
	})
	if err != nil {
		return nil, fmt.Errorf("servicerequest: read document evidence: %w", err)
	}
	out := make([]application.DocumentEvidence, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.DocumentEvidence{DocumentID: row.ID, DocumentTypeCode: row.DocumentTypeCode})
	}
	return out, nil
}

// reviewSetting is the shape of the platform.tenant_setting document. A program named in
// `programs` overrides the tenant default; anything absent falls back to `default`, and an
// absent default means a person reviews it.
type reviewSetting struct {
	Default  *bool           `json:"default"`
	Programs map[string]bool `json:"programs"`
}

// ReviewRequired implements application.Repository.
func (Repository) ReviewRequired(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID) (bool, error) {
	raw, err := sqlcgen.New(tx).GetServiceRequestReviewSetting(ctx, tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("servicerequest: read review setting: %w", err)
	}
	setting := reviewSetting{}
	if err := json.Unmarshal(raw, &setting); err != nil {
		// A setting nobody can parse is a configuration mistake, not a licence to approve
		// something automatically; the safe reading is that a person looks at it.
		return true, nil
	}
	if value, ok := setting.Programs[programID.String()]; ok {
		return value, nil
	}
	if setting.Default != nil {
		return *setting.Default, nil
	}
	return true, nil
}

// decodeActions reads the stored action list of a rule.
func decodeActions(raw []byte) ([]rulesdomain.ActionInput, error) {
	out := []rulesdomain.ActionInput{}
	if len(raw) == 0 {
		return out, nil
	}
	var rows []struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("servicerequest: decode rule actions: %w", err)
	}
	for _, row := range rows {
		out = append(out, rulesdomain.ActionInput{Type: row.Type, Payload: row.Payload})
	}
	return out, nil
}

func optionalObject(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("servicerequest: encode action payload: %w", err)
	}
	return raw, nil
}

type batchExecutor interface {
	Exec(func(int, error))
	Close() error
}

func execBatch(batch batchExecutor) error {
	var firstErr error
	batch.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// guardError turns the immutability triggers of migration 000006 into the named error the
// transport answers 409 with; anything else is wrapped as it was.
func guardError(err, wrapped error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == integrityViolation ||
		strings.Contains(pgErr.Message, "immutable") ||
		strings.Contains(pgErr.Message, "not a draft")) {
		return application.ErrVersionImmutable
	}
	return wrapped
}

func affected(rows int64, none error) error {
	if rows == 0 {
		return none
	}
	return nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func dateOf(t time.Time) pgtype.Date {
	return pgtype.Date{Time: benefitdomain.DateOnly(t), Valid: true}
}

func optDate(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return dateOf(*t)
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return benefitdomain.DateOnly(d.Time)
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	value := benefitdomain.DateOnly(d.Time)
	return &value
}

// trimDecimal drops the trailing zeroes PostgreSQL prints for a numeric(20,6), so a
// quantity reads as "2" rather than "2.000000" wherever it is shown.
func trimDecimal(raw string) string { return benefitdomain.TrimDecimal(raw) }

// trimDecimalPtr turns the empty string the queries render a NULL numeric as back into the
// absence it stands for; anything else keeps its exact value with the trailing zeroes gone.
func trimDecimalPtr(raw string) *string {
	if raw == "" {
		return nil
	}
	value := benefitdomain.TrimDecimal(raw)
	return &value
}

func pageSize(n int) int32 {
	if n <= 0 {
		return 1
	}
	return int32(n) //nolint:gosec // clamped by httpx.ClampLimit before it reaches here
}

func versionNo(n int) int32 {
	return int32(n) //nolint:gosec // version numbers are small positive integers
}

func lineNo(n int) int32 {
	return int32(n) //nolint:gosec // line numbers are bounded by the 100 item cap
}
