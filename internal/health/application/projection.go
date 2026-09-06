package application

import (
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// Projection is which half of a record a caller has earned (WP-I5-01 section 2.2).
type Projection string

const (
	// ProjectionClinical is everything.
	ProjectionClinical Projection = "CLINICAL"
	// ProjectionFinancial is the case's identity, type, dates, provider and status and the
	// encounter's dates, location and practitioner — and nothing from which a diagnosis
	// could be inferred. Not the branch code, not the clinical notes, not the sensitivity,
	// and not a count of anything: a "3 diagnoses" beside a name is a diagnosis.
	ProjectionFinancial Projection = "FINANCIAL"
)

// AccessRequest is why a caller is opening clinical data, as it arrived on the request.
// Both halves travel onto the access event: "who read this" without "why" is not an answer
// a data protection review can use (v1.2 11.10).
type AccessRequest struct {
	PurposeCode string
	ReasonText  string
	// FinancialOnly is the caller declining to read clinical detail: "serve me the financial
	// half, I am not looking". It is answered with the financial projection whatever the
	// caller holds and whatever the case is — no purpose is demanded and nothing reaches the
	// access log, because choosing not to look is not a look.
	FinancialOnly bool
}

// decision is what one read was allowed to be.
type decision struct {
	projection Projection
	// refusedSensitive is true when the caller holds the clinical grant and was still
	// served the financial projection, because the case is sensitive and it may not read
	// that. It is what makes the refusal a row rather than a silence.
	refusedSensitive bool
}

// readMode says how one particular read treats a sensitive case it may not fully see. The
// three values below are the only ones: a caller opening a record, a caller paging a list,
// and the answer a write hands back.
type readMode struct {
	// demandPurpose refuses the read with ErrAccessPurposeRequired rather than narrowing
	// it, so a caller holding the sensitive grant has to say why it is looking.
	demandPurpose bool
	// recordSuccess writes the HEALTH access event when the clinical half was served.
	recordSuccess bool
	// recordRefusal writes the DENIED access event when the clinical half was withheld.
	recordRefusal bool
}

var (
	// singleRead is somebody opening one record. It has to say why, and both the look and
	// the refusal are rows — "who tried" is as much of the record as "who looked".
	singleRead = readMode{demandPurpose: true, recordSuccess: true, recordRefusal: true}
	// listRead is a page. Every clinical row on it is a look and is recorded; a refusal is
	// not, because refusing the page over one sensitive row would say which row is
	// sensitive, and fifty refusal rows per page would bury the reads that matter.
	listRead = readMode{recordSuccess: true}
	// commandRead is the answer a write hands back, and it records nothing at all. The
	// access log answers "who looked at this person's clinical data"; the person who just
	// wrote it is on the business audit row instead, where a write belongs. An event here
	// would put a row on a member's access log for something nobody read.
	commandRead = readMode{}
)

// decide is the single place the visibility rules of this package live. Everything that
// reads a case, an encounter or a diagnosis asks this function and nothing else, so there
// is one rule rather than one per endpoint.
//
// The order matters and is the order of section 2.2 and 2.3:
//
//   - No health.clinical.read at all: the financial projection, whatever the case is. The
//     caller learns nothing about sensitivity, because it is told nothing either way.
//   - A STANDARD case with the clinical grant: the clinical projection.
//   - A SENSITIVE case without health.sensitive.read: the financial projection again, not a
//     refusal. A refusal would itself say the case carries a protected category, which is
//     the fact being protected. The attempt is recorded as DENIED.
//   - A SENSITIVE case with the grant but no stated purpose: refused, 428. The caller holds
//     the sensitive grant, so knowing that this case is sensitive is its job; what is
//     missing is the reason, and a look with no reason is the thing the regulation forbids.
//   - A SENSITIVE case with the grant and a purpose: the clinical projection, on the record.
//
// One thing comes before all of it: a caller that asked for the financial projection gets
// the financial projection. It is not a refusal and it is not a narrowing — the caller asked
// for less than it is entitled to, which nothing here has any reason to argue with. A
// STANDARD case asked for financial-only is answered financially too, for the same reason.
func decide(rc identity.RequestContext, sensitivity string, req AccessRequest) (decision, error) {
	if req.FinancialOnly {
		return decision{projection: ProjectionFinancial}, nil
	}
	if !rc.Has(PermissionClinicalRead) {
		return decision{projection: ProjectionFinancial}, nil
	}
	if sensitivity != domain.SensitivitySensitive {
		return decision{projection: ProjectionClinical}, nil
	}
	if !rc.Has(PermissionSensitiveRead) {
		return decision{projection: ProjectionFinancial, refusedSensitive: true}, nil
	}
	if req.PurposeCode == "" {
		return decision{projection: ProjectionFinancial, refusedSensitive: true}, ErrAccessPurposeRequired
	}
	return decision{projection: ProjectionClinical}, nil
}

// projectCase returns the case as the caller may see it. The financial projection clears
// the field rather than leaving it set and trusting a mapper: a record that no longer
// carries the answer cannot leak it, however it is later serialised.
func projectCase(rec CaseRecord, p Projection) CaseRecord {
	if p == ProjectionClinical {
		return rec
	}
	rec.Sensitivity = ""
	return rec
}

// projectEncounter is projectCase for an encounter. branch_code and notes_clinical are the
// two clinical columns it has; both are cleared for the financial projection.
func projectEncounter(rec EncounterRecord, p Projection) EncounterRecord {
	if p == ProjectionClinical {
		return rec
	}
	rec.BranchCode = nil
	rec.NotesClinical = nil
	return rec
}

// projectEncounters applies projectEncounter over a slice, returning a new one so the
// caller's rows are never mutated in place.
func projectEncounters(rows []EncounterRecord, p Projection) []EncounterRecord {
	out := make([]EncounterRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectEncounter(row, p))
	}
	return out
}

// CaseView is one case with its encounters, already projected. There is no way to build one
// except through the service, and nothing downstream re-reads the unprojected row.
type CaseView struct {
	Projection Projection
	Case       CaseRecord
	Encounters []EncounterRecord
}

// EncounterView is one encounter, already projected.
type EncounterView struct {
	Projection Projection
	Encounter  EncounterRecord
}

// CasePage is one page of cases, each already projected.
type CasePage struct {
	Items      []CaseView
	NextCursor string
}

// AccessLogPage is one page of the health access log.
type AccessLogPage struct {
	Items      []AccessEventRecord
	NextCursor string
}

// projectReport returns the report as the caller may see it. It is `projectCase` for a
// treatment report, and it clears the field rather than leaving it set and trusting a
// mapper: a record that no longer carries the answer cannot leak it, however it is later
// serialised.
//
// What the financial projection keeps is what a financial reviewer needs to reconcile a
// claim against a report: the reference, the version, the dates, the provider, the status
// and the covered services with their limits. What it drops is everything that says what
// was wrong with the person — the summary, the reviewer's comment, the type and the
// subtype, and, in projectReportServices below, the line notes.
func projectReport(rec ReportRecord, p Projection) ReportRecord {
	// The case's sensitivity is what decided this projection, and it is itself clinical. It
	// is cleared in both projections because it is not a field of the report at all: no
	// mapper below has anything to render it into.
	rec.CaseSensitivity = ""
	if p == ProjectionClinical {
		return rec
	}
	rec.ReportType = ""
	rec.ReportSubtype = nil
	rec.ClinicalSummary = nil
	rec.ReviewComment = nil
	return rec
}

// projectReportServices applies the projection over the report's lines, returning a new
// slice so the caller's rows are never mutated in place. The covered quantity and amount
// survive both projections — they are the limits a claim is reconciled against — and the
// note does not.
func projectReportServices(rows []ReportServiceRecord, p Projection) []ReportServiceRecord {
	out := make([]ReportServiceRecord, 0, len(rows))
	for _, row := range rows {
		if p != ProjectionClinical {
			row.Notes = nil
		}
		out = append(out, row)
	}
	return out
}

// projectReportDocuments answers the attachments only in the clinical projection. An empty
// slice rather than a shortened one: a count of documents is a fact about the patient too,
// and "this report has four attachments" beside a name is worth more to a curious HR user
// than any one of them.
func projectReportDocuments(rows []ReportDocumentRecord, p Projection) []ReportDocumentRecord {
	if p != ProjectionClinical {
		return []ReportDocumentRecord{}
	}
	out := make([]ReportDocumentRecord, len(rows))
	copy(out, rows)
	return out
}

// ReportView is one report with its lines and attachments, already projected. There is no
// way to build one except through the service, and nothing downstream re-reads the
// unprojected row.
type ReportView struct {
	Projection Projection
	Report     ReportRecord
	Services   []ReportServiceRecord
	Documents  []ReportDocumentRecord
}

// ReportPage is one page of reports, each already projected.
type ReportPage struct {
	Items      []ReportView
	NextCursor string
}

// UsagePage is one page of a report's usage trace.
type UsagePage struct {
	Items      []ReportUsageRecord
	NextCursor string
}

// projectStay returns the stay as the caller may see it.
//
// What the financial projection keeps is what a claims reviewer needs to reconcile a bill:
// the dates, the provider, the location, the status, the request and authorization it hangs
// off, and every figure of the reconciliation. What it drops is the one field from which a
// diagnosis could be inferred — that the admission has a recorded diagnosis at all is a fact
// about the patient, and an id beside a name is an invitation to go and look it up.
func projectStay(rec StayRecord, p Projection) StayRecord {
	// The case's sensitivity is what decided this projection, and it is itself clinical. It
	// is cleared in both projections because it is not a field of the stay at all: no mapper
	// below has anything to render it into.
	rec.CaseSensitivity = ""
	if p == ProjectionClinical {
		return rec
	}
	rec.AdmissionDiagnosisID = nil
	return rec
}

// projectStayExtensions applies the projection over a stay's extensions, returning a new
// slice so the caller's rows are never mutated in place. The day count, the reason code and
// the decision survive both projections — they are what the reconciliation and the claim are
// checked against — and the reason text does not: "solunum sıkıntısı devam ediyor" is a
// diagnosis in a sentence.
func projectStayExtensions(rows []StayExtensionRecord, p Projection) []StayExtensionRecord {
	out := make([]StayExtensionRecord, 0, len(rows))
	for _, row := range rows {
		if p != ProjectionClinical {
			row.ReasonText = nil
		}
		out = append(out, row)
	}
	return out
}

// StayView is one stay with its extensions and segments, already projected. There is no way
// to build one except through the service, and nothing downstream re-reads the unprojected
// row.
type StayView struct {
	Projection Projection
	Stay       StayRecord
	Extensions []StayExtensionRecord
	Segments   []StaySegmentRecord
}

// StayPage is one page of stays, each already projected.
type StayPage struct {
	Items      []StayView
	NextCursor string
}

// StayReconciliation is what a discharge settled: what was promised, what was used, what was
// given back, and whether the admission ran over what anybody approved. Every figure is the
// exact decimal text the numeric column holds, because a day count that passed through a
// float would be a day count two systems disagree about.
//
// It carries nothing clinical, so it has no projection of its own: it is the financial half
// of a stay by construction.
type StayReconciliation struct {
	StayID          uuid.UUID
	AuthorizationID *uuid.UUID
	AdmissionAt     time.Time
	DischargeAt     time.Time
	AuthorizedDays  string
	ActualDays      string
	ReleasedDays    string
	// OverAuthorization is what the claim raises as an exception (WP-I5-04): the admission
	// used more days than anybody approved, and nothing was released because there was
	// nothing left to release.
	OverAuthorization bool
}

// ClinicalDecision is what the visibility rules answered, for the modules outside this
// package that hold clinically-classified records of their own. WP-I5-04's claim carries a
// line description, a diagnosis reference and a medical reviewer's comment, and every one of
// them is exactly as protected as an encounter's clinical note.
type ClinicalDecision struct {
	// Projection is which half the caller has earned.
	Projection Projection
	// RefusedSensitive is true when the caller holds the clinical grant and was still
	// served the financial projection because the case is sensitive. It is what makes the
	// refusal a row rather than a silence.
	RefusedSensitive bool
}

// DecideProjection is `decide` under an exported name, and it exists so that there is one
// rule about clinical visibility rather than one per module. A second copy of the ladder in
// the claim package would be a second place for it to disagree — and the disagreement would
// be a diagnosis on somebody's screen, discovered by the person it belongs to.
//
// The sensitivity is the case's, read from `health.health_case`; a record hanging off no case
// is STANDARD, which is the only honest answer for a record with no episode of care behind
// it. The error is ErrAccessPurposeRequired and nothing else, and the caller owes the same
// DENIED access event this package writes for its own reads.
func DecideProjection(rc identity.RequestContext, sensitivity string, req AccessRequest) (ClinicalDecision, error) {
	d, err := decide(rc, sensitivity, req)
	return ClinicalDecision{Projection: d.projection, RefusedSensitive: d.refusedSensitive}, err
}
