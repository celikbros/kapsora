package healthpg

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The four reads of health.health_case select the same columns and sqlc gives each of them
// its own row type; the encounters do the same. Rather than several copies of the same
// mapping — which is exactly how a column ends up carried in one read and dropped in
// another, and in this package a dropped column would be a clinical field that silently
// stopped arriving — every row is narrowed to one shape here and mapped once.

type healthCase struct {
	ID                     uuid.UUID
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	CaseType               string
	ProviderOrganizationID uuid.NullUUID
	OpenedAt               time.Time
	ClosedAt               *time.Time
	Status                 string
	Sensitivity            string
	ServiceRequestID       uuid.NullUUID
	CreatedAt              time.Time
	RowVersion             int64
}

func createdCaseRow(r sqlcgen.CreateHealthCaseRow) healthCase { return healthCase(r) }
func fetchedCaseRow(r sqlcgen.GetHealthCaseRow) healthCase    { return healthCase(r) }
func lockedCaseRow(r sqlcgen.LockHealthCaseRow) healthCase    { return healthCase(r) }
func listedCaseRow(r sqlcgen.ListHealthCasesRow) healthCase   { return healthCase(r) }

func caseOf(r healthCase) application.CaseRecord {
	return application.CaseRecord{
		ID: r.ID, PersonID: r.PersonID, ProgramID: r.ProgramID, EnrollmentID: r.EnrollmentID,
		CaseType: r.CaseType, ProviderOrganizationID: uuidPtr(r.ProviderOrganizationID),
		OpenedAt: r.OpenedAt, ClosedAt: r.ClosedAt, Status: r.Status,
		Sensitivity: r.Sensitivity, ServiceRequestID: uuidPtr(r.ServiceRequestID),
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

type encounter struct {
	ID             uuid.UUID
	CaseID         uuid.UUID
	EncounterType  string
	StartedAt      time.Time
	EndedAt        *time.Time
	LocationID     uuid.NullUUID
	PractitionerID uuid.NullUUID
	BranchCode     *string
	NotesClinical  *string
	CreatedAt      time.Time
	RowVersion     int64
}

func createdEncounterRow(r sqlcgen.CreateEncounterRow) encounter   { return encounter(r) }
func fetchedEncounterRow(r sqlcgen.GetEncounterRow) encounter      { return encounter(r) }
func listedEncounterRow(r sqlcgen.ListCaseEncountersRow) encounter { return encounter(r) }

func encounterOf(r encounter) application.EncounterRecord {
	return application.EncounterRecord{
		ID: r.ID, CaseID: r.CaseID, EncounterType: r.EncounterType,
		StartedAt: r.StartedAt, EndedAt: r.EndedAt,
		LocationID: uuidPtr(r.LocationID), PractitionerID: uuidPtr(r.PractitionerID),
		BranchCode: r.BranchCode, NotesClinical: r.NotesClinical,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

func diagnosisOf(r sqlcgen.ListEncounterDiagnosesRow) application.DiagnosisRecord {
	return application.DiagnosisRecord{
		ID: r.ID, EncounterID: r.EncounterID, CodeSystemID: r.CodeSystemID,
		CodeSystemCode: r.CodeSystemCode, CodeValueID: r.CodeValueID,
		Code: r.Code, Display: r.Display, DiagnosisType: r.DiagnosisType,
		Sensitive: r.Sensitive, RecordedAt: r.RecordedAt, RecordedBy: uuidPtr(r.RecordedBy),
	}
}

func accessEventOf(r sqlcgen.ListHealthAccessEventsRow) application.AccessEventRecord {
	return application.AccessEventRecord{
		ID: r.ID, OccurredAt: r.OccurredAt, ActorID: r.ActorID,
		MembershipID: uuidPtr(r.MembershipID), PersonID: uuidPtr(r.PersonID),
		ResourceType: r.ResourceType, ResourceID: uuidPtr(r.ResourceID),
		AccessType: r.AccessType, PurposeCode: r.PurposeCode, ReasonText: r.ReasonText,
		Outcome: r.Outcome,
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

// pageSize clamps a repository page size; the service has already clamped the caller's.
func pageSize(n int) int32 {
	if n <= 0 {
		return 50
	}
	if n > 500 {
		return 500
	}
	return int32(n)
}

// The four reads of health.medical_report select the same columns and sqlc gives each of
// them its own row type. They are narrowed to one shape here and mapped once, for the same
// reason the case rows above are: a column carried in one read and dropped in another would
// be a clinical field that silently stopped arriving in half the endpoints.

type medicalReport struct {
	ID                            uuid.UUID
	PersonID                      uuid.UUID
	CaseID                        uuid.NullUUID
	Reference                     string
	VersionNo                     int32
	RootReportID                  uuid.UUID
	SupersedesReportID            uuid.NullUUID
	ReportType                    string
	ReportSubtype                 *string
	IssuingPractitionerID         uuid.NullUUID
	IssuingProviderOrganizationID uuid.NullUUID
	IssuedAt                      pgtype.Date
	ValidFrom                     pgtype.Date
	ValidTo                       pgtype.Date
	Status                        string
	ClinicalSummary               *string
	ReviewComment                 *string
	RejectReasonCode              *string
	ReviewedBy                    uuid.NullUUID
	ReviewedAt                    *time.Time
	SubmittedAt                   *time.Time
	SubmittedBy                   uuid.NullUUID
	CreatedAt                     time.Time
	RowVersion                    int64
	CaseSensitivity               string
}

func fetchedReportRow(r sqlcgen.GetMedicalReportRow) medicalReport     { return medicalReport(r) }
func lockedReportRow(r sqlcgen.LockMedicalReportRow) medicalReport     { return medicalReport(r) }
func listedReportRow(r sqlcgen.ListMedicalReportsRow) medicalReport    { return medicalReport(r) }
func chainReportRow(r sqlcgen.ListMedicalReportChainRow) medicalReport { return medicalReport(r) }

func reportOf(r medicalReport) application.ReportRecord {
	return application.ReportRecord{
		ID: r.ID, PersonID: r.PersonID, CaseID: uuidPtr(r.CaseID), Reference: r.Reference,
		VersionNo: int(r.VersionNo), RootReportID: r.RootReportID,
		SupersedesReportID: uuidPtr(r.SupersedesReportID), ReportType: r.ReportType,
		ReportSubtype:                 r.ReportSubtype,
		IssuingPractitionerID:         uuidPtr(r.IssuingPractitionerID),
		IssuingProviderOrganizationID: uuidPtr(r.IssuingProviderOrganizationID),
		IssuedAt:                      dateValue(r.IssuedAt), ValidFrom: dateValue(r.ValidFrom),
		ValidTo: dateValue(r.ValidTo), Status: r.Status, ClinicalSummary: r.ClinicalSummary,
		ReviewComment: r.ReviewComment, RejectReasonCode: r.RejectReasonCode,
		ReviewedBy: uuidPtr(r.ReviewedBy), ReviewedAt: r.ReviewedAt,
		SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion, CaseSensitivity: r.CaseSensitivity,
	}
}
