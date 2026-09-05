package application

import (
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
func decide(rc identity.RequestContext, sensitivity string, req AccessRequest) (decision, error) {
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
