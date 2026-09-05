package healthpg

import (
	"time"

	"github.com/google/uuid"

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
