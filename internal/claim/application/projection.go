package application

import (
	"github.com/celikbros/kapsora/internal/claim/domain"
)

// The projections here are WP-I5-01's applied to this package's records. The *decision* about
// which projection a caller has earned is not made here — it is `healthapp.DecideProjection`,
// asked in exactly one place — and what follows is only the clearing.
//
// Clearing rather than "not rendering" is deliberate and is the rule WP-I5-01 wrote down: a
// record that no longer carries the answer cannot leak it, however it is later serialised. A
// mapper that remembered to omit a field is a mapper somebody will later change.

// projectClaim returns the header as the caller may see it. The financial projection drops
// the medical reviewer's comment and nothing else: every date, every reason code, every
// status and every id on the header is money or process, and a financial reviewer needs all
// of it to reconcile a bill.
func projectClaim(rec ClaimRecord, p Projection) ClaimRecord {
	if p == ProjectionClinical {
		return rec
	}
	rec.ReviewCommentMedical = nil
	return rec
}

// projectLine returns one line as the caller may see it.
//
// Three fields go, and each of them is a different way of saying the same thing about the
// patient:
//
//   - `Description` — the provider's own words. "Sol diz artroskopi sonrası kontrol" is a
//     diagnosis in a sentence, and the provider typing it is not thinking about who will read
//     it. v1.2 marks the line description as possibly clinical, and possibly clinical is
//     clinical: the only safe reading of a free-text field is the worst one.
//   - `DiagnosisID` — an id beside a name is an invitation to go and look it up.
//   - `MedicalReportID` — that a line leans on a treatment report at all is a fact about the
//     patient, and a count of them beside a name is worth more to a curious HR user than any
//     one of them.
//
// The quantity, the amount, the currency, the service and the practitioner survive both
// projections. They are what a bill is, and a financial reviewer who could not see them could
// not do the job the financial projection exists for.
func projectLine(rec LineRecord, p Projection) LineRecord {
	if p == ProjectionClinical {
		return rec
	}
	rec.Description = nil
	rec.DiagnosisID = nil
	rec.MedicalReportID = nil
	return rec
}

// projectDecision returns one line decision as the caller may see it.
//
// Every figure survives both projections — a cut is money and money is the financial
// reviewer's business — and the *reason text of a medical decision* does not. A medical
// reviewer writing "fizik tedavi endikasyonu yok" has written a clinical judgement about a
// person, and it is exactly as protected as the diagnosis it rests on. The reason *code*
// survives: it is a code from a closed list, it is what a screen groups and counts by, and a
// provider disputing a cut has to be told which rule cut it.
func projectDecision(rec DecisionRecord, p Projection) DecisionRecord {
	if p == ProjectionClinical || rec.Stage != domain.StageMedical {
		return rec
	}
	rec.ReasonText = nil
	return rec
}

// LineView is one line with the decision it carries, both already projected.
type LineView struct {
	Line LineRecord
	// Decision is nil while nobody has decided the line. The history is append-only in the
	// database; this is its head.
	Decision *DecisionRecord
}

// ClaimView is one claim with the lines of its current version, already projected. There is
// no way to build one except through the service, and nothing downstream re-reads the
// unprojected row.
type ClaimView struct {
	Projection Projection
	Claim      ClaimRecord
	Lines      []LineView
	// Exceptions is what the submit pipeline found that a person has to look at, read back
	// from the frozen snapshot of the current version. It is the answer to "why is this
	// claim in front of me", which is the first thing either reviewer's screen has to say.
	Exceptions []ClaimException
}

// ClaimPage is one page of claims, each already projected.
type ClaimPage struct {
	Items      []ClaimView
	NextCursor string
}

// VersionView is one version with the lines it carried and the decision each was given.
type VersionView struct {
	Projection Projection
	Version    VersionRecord
	Lines      []LineView
	Exceptions []ClaimException
}

// buildLineViews pairs each line with the head of its decision history and applies the
// projection to both, returning new values so the caller's rows are never mutated in place.
func buildLineViews(lines []LineRecord, decisions []DecisionRecord, p Projection) []LineView {
	byLine := make(map[string]DecisionRecord, len(decisions))
	for _, d := range decisions {
		byLine[d.LineID.String()] = d
	}
	out := make([]LineView, 0, len(lines))
	for _, line := range lines {
		view := LineView{Line: projectLine(line, p)}
		if decision, ok := byLine[line.ID.String()]; ok {
			projected := projectDecision(decision, p)
			view.Decision = &projected
		}
		out = append(out, view)
	}
	return out
}
