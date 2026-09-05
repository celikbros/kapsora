package servicerequestpg

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
)

// The four request reads and the three version reads select the same columns, and sqlc
// gives each of them its own row type. Rather than three copies of the same mapping — which
// is exactly how a column ends up carried in one read and dropped in another — every row
// is narrowed to one shape here and mapped once.

type requestRow struct {
	ID                           uuid.UUID
	RequestReference             string
	RequestType                  string
	PersonID                     uuid.UUID
	ProgramID                    uuid.UUID
	EnrollmentID                 uuid.UUID
	ProviderTenantOrganizationID uuid.NullUUID
	ServiceDate                  pgtype.Date
	RequestedStartAt             *time.Time
	RequestedEndAt               *time.Time
	Channel                      string
	Status                       string
	CurrentVersionNo             int32
	SupersedesRequestID          uuid.NullUUID
	EligibilityEvaluationID      uuid.NullUUID
	RuleEvaluationID             uuid.NullUUID
	RequiredDocumentTypes        []string
	ReturnReasonCode             *string
	RejectReasonCode             *string
	ReviewComment                *string
	SubmittedAt                  *time.Time
	ClosedAt                     *time.Time
	CreatedAt                    time.Time
	RowVersion                   int64
	PersonDisplayName            string
	ProviderDisplayName          *string
}

func getRow(r sqlcgen.GetServiceRequestRow) requestRow {
	return requestRow(r)
}

func lockRow(r sqlcgen.LockServiceRequestRow) requestRow {
	return requestRow(r)
}

func listRow(r sqlcgen.ListServiceRequestsRow) requestRow {
	return requestRow(r)
}

func requestOf(r requestRow) application.RequestRecord {
	return application.RequestRecord{
		ID: r.ID, Reference: r.RequestReference, RequestType: r.RequestType,
		PersonID: r.PersonID, ProgramID: r.ProgramID, EnrollmentID: r.EnrollmentID,
		ProviderOrganizationID: uuidPtr(r.ProviderTenantOrganizationID),
		ServiceDate:            dateValue(r.ServiceDate),
		RequestedStartAt:       r.RequestedStartAt, RequestedEndAt: r.RequestedEndAt,
		Channel: r.Channel, Status: r.Status, CurrentVersionNo: int(r.CurrentVersionNo),
		SupersedesRequestID:     uuidPtr(r.SupersedesRequestID),
		EligibilityEvaluationID: uuidPtr(r.EligibilityEvaluationID),
		RuleEvaluationID:        uuidPtr(r.RuleEvaluationID),
		RequiredDocumentTypes:   r.RequiredDocumentTypes,
		ReturnReasonCode:        r.ReturnReasonCode, RejectReasonCode: r.RejectReasonCode,
		ReviewComment: r.ReviewComment, SubmittedAt: r.SubmittedAt, ClosedAt: r.ClosedAt,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		PersonDisplayName: r.PersonDisplayName, ProviderDisplayName: r.ProviderDisplayName,
	}
}

type versionRow struct {
	ID               uuid.UUID
	ServiceRequestID uuid.UUID
	VersionNo        int32
	Status           string
	SnapshotJson     []byte
	SubmittedAt      *time.Time
	SubmittedBy      uuid.NullUUID
	ReturnedAt       *time.Time
	ReturnedBy       uuid.NullUUID
	ReturnReasonCode *string
	ReturnReasonText *string
	CreatedAt        time.Time
}

func draftVersionRow(r sqlcgen.GetServiceRequestDraftVersionRow) versionRow {
	return versionRow(r)
}

func numberedVersionRow(r sqlcgen.GetServiceRequestVersionByNoRow) versionRow {
	return versionRow(r)
}

func listVersionRow(r sqlcgen.ListServiceRequestVersionsRow) versionRow {
	return versionRow(r)
}

func versionOf(r versionRow) application.VersionRecord {
	return application.VersionRecord{
		ID: r.ID, ServiceRequestID: r.ServiceRequestID, VersionNo: int(r.VersionNo),
		Status: r.Status, Snapshot: r.SnapshotJson,
		SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
		ReturnedAt: r.ReturnedAt, ReturnedBy: uuidPtr(r.ReturnedBy),
		ReturnReasonCode: r.ReturnReasonCode, ReturnReasonText: r.ReturnReasonText,
		CreatedAt: r.CreatedAt,
	}
}
