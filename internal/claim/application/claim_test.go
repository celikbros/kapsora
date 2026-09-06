package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
)

// The actions the fixture publishes as ADJUDICATION rules. They are written here in the JSON
// an author's own screen would have stored, so the engine reads exactly what production reads.
const (
	actionFinancialReview = `[{"type":"REQUIRE_FINANCIAL_REVIEW","payload":{"queueCode":"FINANCIAL_REVIEW"}}]`
	actionMedicalReview   = `[{"type":"REQUIRE_MEDICAL_REVIEW","payload":{"queueCode":"MEDICAL_REVIEW"}}]`
)

// submitClaim submits and returns the routed view.
func submitClaim(t *testing.T, f *fixture, view application.ClaimView) application.ClaimView {
	t.Helper()
	out, err := f.claims.Submit(context.Background(), f.providerRC(), view.Claim.ID,
		view.Claim.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("submit claim: %v", err)
	}
	return out
}

// TestOutpatientClaimGoesFromDraftToInvoiceReady is the acceptance criterion of the work
// package, driven end to end.
//
// A case with one encounter, a hold taken against the real entitlement ledger, a claim with two
// lines — one the ladder priced and nothing objected to, one a published ADJUDICATION rule sent
// to financial review — decided, approved, and asked whether an invoice could be raised.
//
// Every figure below is exact decimal text. The consultation is 449.99 with a 12.5 % member
// share, so the share is 56.24875 and only a single rounding at the end gives 393.74 / 56.25;
// an implementation that rounded the two halves independently would be a kuruş out and would
// fail the split assertion rather than quietly publishing an invoice nobody can reconcile.
func TestOutpatientClaimGoesFromDraftToInvoiceReady(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "PHYSIO_FINANCIAL",
		`serviceCode == "PHYSIO_SESSION"`, actionFinancialReview)
	authorization := f.authorizeSessions(t, "4")

	draft := f.newClaim(t, []application.NewLineInput{
		f.consultLine(1),
		f.physioLine(2, "2", physioTwoContract),
	}, func(in *application.NewClaimInput) { in.AuthorizationID = &authorization })

	submitted := submitClaim(t, f, draft)
	if submitted.Claim.Status != domain.StatusPendingFinancial {
		t.Fatalf("submitted status = %s, want PENDING_FINANCIAL", submitted.Claim.Status)
	}

	// Line 1 is decided at the AUTO stage with nobody's name on it; line 2 waits for a person.
	auto := lineByNo(t, submitted, 1)
	if auto.Decision == nil {
		t.Fatal("line 1 carries no AUTO decision")
	}
	if auto.Decision.Stage != domain.StageAuto || auto.Decision.DecidedBy != nil {
		t.Errorf("line 1 decision stage = %s decidedBy = %v, want AUTO and nobody",
			auto.Decision.Stage, auto.Decision.DecidedBy)
	}
	if auto.Decision.PayerAmount != consultPayer || auto.Decision.MemberAmount != consultMember {
		t.Errorf("line 1 payer/member = %s/%s, want %s/%s",
			auto.Decision.PayerAmount, auto.Decision.MemberAmount, consultPayer, consultMember)
	}
	if auto.Decision.ApprovedAmount != consultPrice {
		t.Errorf("line 1 approved = %s, want %s", auto.Decision.ApprovedAmount, consultPrice)
	}
	if review := lineByNo(t, submitted, 2); review.Decision != nil {
		t.Errorf("line 2 was decided at %s before anybody reviewed it", review.Decision.Stage)
	}

	// The hold moved by the claimed quantity and by nothing else.
	if got := f.consumedTotal(t, authorization); got != "2" {
		t.Errorf("authorization consumed total = %s, want 2", got)
	}

	// The financial reviewer decides the line the rule sent them.
	decided, err := f.claims.DecideLines(context.Background(), f.financialRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 2, Decision: domain.DecisionApproved, ApprovedQuantity: "2",
				ApprovedAmount: physioTwoContract, PayerAmount: physioTwoContract,
				MemberAmount: "0", ReasonCode: "WITHIN_AUTHORIZATION",
			}},
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{})
	if err != nil {
		t.Fatalf("decide lines: %v", err)
	}

	approved, err := f.claims.Approve(context.Background(), f.financialRC(), decided.Claim.ID,
		application.ReasonInput{ReasonCode: "COMPLETE", ExpectedVersion: decided.Claim.RowVersion})
	if err != nil {
		t.Fatalf("approve claim: %v", err)
	}
	if approved.Claim.Status != domain.StatusApproved {
		t.Fatalf("approved status = %s, want APPROVED", approved.Claim.Status)
	}

	readiness, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), approved.Claim.ID)
	if err != nil {
		t.Fatalf("invoice readiness: %v", err)
	}
	if !readiness.Ready {
		t.Fatalf("readiness is not ready, blockers = %v", readiness.Blockers)
	}
	// 449.99 + 500 approved, 393.74 + 500 to the payer, 56.25 + 0 to the member.
	if readiness.ApprovedTotal != "949.99" || readiness.PayerTotal != "893.74" ||
		readiness.MemberTotal != "56.25" {
		t.Errorf("totals = %s / %s / %s, want 949.99 / 893.74 / 56.25",
			readiness.ApprovedTotal, readiness.PayerTotal, readiness.MemberTotal)
	}
	if readiness.LineCount != 2 || readiness.DecidedLineCount != 2 {
		t.Errorf("line counts = %d decided of %d, want 2 of 2",
			readiness.DecidedLineCount, readiness.LineCount)
	}

	// The totals are the sum of the line decisions, and every line's halves add up.
	sumApproved, sumPayer, sumMember := zeroSum(), zeroSum(), zeroSum()
	for _, line := range approved.Lines {
		if line.Decision == nil {
			t.Fatalf("line %d has no decision on an approved claim", line.Line.LineNo)
		}
		approvedAmount := mustQuantity(t, line.Decision.ApprovedAmount)
		payer := mustQuantity(t, line.Decision.PayerAmount)
		member := mustQuantity(t, line.Decision.MemberAmount)
		if payer.Add(member).Cmp(approvedAmount) != 0 {
			t.Errorf("line %d: payer %s + member %s != approved %s", line.Line.LineNo,
				line.Decision.PayerAmount, line.Decision.MemberAmount, line.Decision.ApprovedAmount)
		}
		sumApproved, sumPayer, sumMember = sumApproved.Add(approvedAmount),
			sumPayer.Add(payer), sumMember.Add(member)
	}
	if sumApproved.String() != readiness.ApprovedTotal || sumPayer.String() != readiness.PayerTotal ||
		sumMember.String() != readiness.MemberTotal {
		t.Errorf("readiness totals %s/%s/%s do not equal the line sums %s/%s/%s",
			readiness.ApprovedTotal, readiness.PayerTotal, readiness.MemberTotal,
			sumApproved, sumPayer, sumMember)
	}

	// The hold has not moved again: an approval consumes nothing, it only settles what the
	// submit already drew down.
	if got := f.consumedTotal(t, authorization); got != "2" {
		t.Errorf("authorization consumed total after approval = %s, want 2", got)
	}
	f.ledgerConserved(t)
}

// TestInvoiceReadinessNamesItsBlockers is the other half of the readiness answer: a claim that
// is decided but not invoiceable says which of the three things is missing, by name.
//
// The provider's tax identity is cleared here rather than never seeded, so the test runs
// against a fixture where the blocker is genuinely the difference between ready and not.
func TestInvoiceReadinessNamesItsBlockers(t *testing.T) {
	f := newFixture(t)
	draft := f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil)
	submitted := submitClaim(t, f, draft)
	if submitted.Claim.Status != domain.StatusApproved {
		t.Fatalf("submitted status = %s, want APPROVED", submitted.Claim.Status)
	}

	ready, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), submitted.Claim.ID)
	if err != nil {
		t.Fatalf("invoice readiness: %v", err)
	}
	if !ready.Ready || len(ready.Blockers) != 0 {
		t.Fatalf("a fully priced approved claim is not ready: %v", ready.Blockers)
	}

	f.h.AdminExec(`
		UPDATE directory.organization o
		   SET tax_number_cipher = NULL, tax_number_hash = NULL
		  FROM directory.tenant_organization t
		 WHERE t.id = $1 AND o.id = t.organization_id`, f.provider)

	blocked, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), submitted.Claim.ID)
	if err != nil {
		t.Fatalf("invoice readiness after clearing the tax identity: %v", err)
	}
	if blocked.Ready {
		t.Fatal("a provider with no tax identity is still invoice-ready")
	}
	if len(blocked.Blockers) != 1 || blocked.Blockers[0] != application.BlockerProviderTaxIdentity {
		t.Errorf("blockers = %v, want exactly PROVIDER_TAX_IDENTITY_MISSING", blocked.Blockers)
	}

	// A claim nobody has decided has no readiness answer at all.
	other := f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil)
	if _, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), other.Claim.ID); !errors.Is(err, application.ErrNotDecided) {
		t.Errorf("readiness of a draft = %v, want ErrNotDecided", err)
	}
	f.ledgerConserved(t)
}

// TestAnUndecidedLineBlocksTheInvoice is the third of section 2.4's named blockers, and the
// only one no command can produce: approve decides every line, so a claim that reaches
// readiness has them all. The check exists for exactly the case its own comment describes —
// a row written by something that did not go through the command — and this test writes one,
// because a blocker nothing can reach is a blocker nobody can trust.
//
// M7's invoice is what leans on this answer. An invoice built over a line with no decision
// would bill an amount nobody approved.
func TestAnUndecidedLineBlocksTheInvoice(t *testing.T) {
	f := newFixture(t)
	draft := f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil)
	submitted := submitClaim(t, f, draft)
	if submitted.Claim.Status != domain.StatusApproved {
		t.Fatalf("submitted status = %s, want APPROVED", submitted.Claim.Status)
	}
	ready, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), submitted.Claim.ID)
	if err != nil {
		t.Fatalf("invoice readiness: %v", err)
	}
	if !ready.Ready {
		t.Fatalf("the fixture is not ready before the undecided line: %v", ready.Blockers)
	}

	// A second line on the decided version, written the way a migration or a repair script
	// would write it: no decision comes with it.
	f.h.AdminExec(`
		INSERT INTO claim.claim_line (tenant_id, version_id, line_no, service_definition_id,
		                              unit_type, quantity, unit_amount, line_amount, currency_code)
		SELECT l.tenant_id, l.version_id, 99, l.service_definition_id,
		       l.unit_type, l.quantity, l.unit_amount, l.line_amount, l.currency_code
		  FROM claim.claim_line l
		  JOIN claim.claim_version v ON v.id = l.version_id
		 WHERE v.claim_id = $1 AND v.version_no = $2
		 LIMIT 1`, submitted.Claim.ID, submitted.Claim.CurrentVersionNo)

	blocked, err := f.claims.InvoiceReadiness(context.Background(), f.financialRC(), submitted.Claim.ID)
	if err != nil {
		t.Fatalf("invoice readiness with an undecided line: %v", err)
	}
	if blocked.Ready {
		t.Fatal("a claim with a line nobody decided is still invoice-ready")
	}
	if len(blocked.Blockers) != 1 || blocked.Blockers[0] != application.BlockerLineNotDecided {
		t.Fatalf("blockers = %v, want exactly LINE_NOT_DECIDED", blocked.Blockers)
	}
	// The totals still answer for the lines that were decided, so a screen can show what is
	// approved and what is missing at the same time.
	if blocked.ApprovedTotal != ready.ApprovedTotal {
		t.Errorf("approved total moved from %s to %s because of an undecided line",
			ready.ApprovedTotal, blocked.ApprovedTotal)
	}
	f.ledgerConserved(t)
}

// TestCorrectionIsANewVersionAndTheOldDecisionIsKept is the first rule the package carries.
//
// Submit; the financial reviewer cuts line 2; return. Version 2 is a DRAFT with the lines
// copied, version 1 is SUPERSEDED, and the cut it was given is unchanged and still readable —
// figure for figure, reason for reason.
func TestCorrectionIsANewVersionAndTheOldDecisionIsKept(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "PHYSIO_FINANCIAL",
		`serviceCode == "PHYSIO_SESSION"`, actionFinancialReview)

	draft := f.newClaim(t, []application.NewLineInput{
		f.consultLine(1),
		f.physioLine(2, "2", physioTwoContract),
	}, nil)
	submitted := submitClaim(t, f, draft)

	// A submitted version is frozen, and the refusal says so rather than being a version
	// mismatch — which is what a provider needs to hear, because the fix is a correction
	// rather than a re-read. Both write commands are checked: the lines and the header.
	_, err := f.claims.PutLines(context.Background(), f.providerRC(), submitted.Claim.ID,
		[]application.NewLineInput{f.consultLine(1)}, submitted.Claim.RowVersion)
	if !errors.Is(err, application.ErrVersionFrozen) {
		t.Fatalf("replacing the lines of a submitted version = %v, want ErrVersionFrozen", err)
	}
	_, err = f.claims.PatchDraft(context.Background(), f.providerRC(), submitted.Claim.ID,
		application.DraftInput{
			ServiceDateFrom: serviceDay, ServiceDateTo: serviceDay,
			ExpectedVersion: submitted.Claim.RowVersion,
		})
	if !errors.Is(err, application.ErrVersionFrozen) {
		t.Fatalf("patching the header of a submitted version = %v, want ErrVersionFrozen", err)
	}

	cut, err := f.claims.DecideLines(context.Background(), f.financialRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 2, Decision: domain.DecisionCut, ApprovedQuantity: "2",
				ApprovedAmount: "400", PayerAmount: "400", MemberAmount: "0",
				ReasonCode: "TARIFF_EXCEEDED",
			}},
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{})
	if err != nil {
		t.Fatalf("cut line 2: %v", err)
	}

	returned, err := f.claims.Return(context.Background(), f.financialRC(), cut.Claim.ID,
		application.ReasonInput{
			ReasonCode: "DOCUMENT_MISSING", ExpectedVersion: cut.Claim.RowVersion,
		})
	if err != nil {
		t.Fatalf("return claim: %v", err)
	}
	if returned.Claim.Status != domain.StatusReturned {
		t.Fatalf("returned status = %s, want RETURNED", returned.Claim.Status)
	}
	if returned.Claim.CurrentVersionNo != 2 {
		t.Fatalf("current version = %d, want 2", returned.Claim.CurrentVersionNo)
	}

	versions, err := f.claims.ListVersions(context.Background(), f.providerRC(), returned.Claim.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("claim has %d versions, want 2", len(versions))
	}
	byNo := map[int]string{}
	for _, v := range versions {
		byNo[v.VersionNo] = v.Status
	}
	if byNo[1] != domain.VersionSuperseded || byNo[2] != domain.VersionDraft {
		t.Errorf("version statuses = 1:%s 2:%s, want SUPERSEDED and DRAFT", byNo[1], byNo[2])
	}

	// Version 1 still says exactly what it was decided to say.
	old, err := f.claims.GetVersion(context.Background(), f.medicalRC(), returned.Claim.ID, 1,
		application.AccessRequest{PurposeCode: "CLAIM_REVIEW", ReasonText: "denetim"})
	if err != nil {
		t.Fatalf("read superseded version: %v", err)
	}
	if len(old.Lines) != 2 {
		t.Fatalf("superseded version has %d lines, want 2", len(old.Lines))
	}
	oldLine2 := versionLine(t, old, 2)
	if oldLine2.Decision == nil {
		t.Fatal("the superseded version's line 2 lost its decision")
	}
	if oldLine2.Decision.Decision != domain.DecisionCut ||
		oldLine2.Decision.ApprovedAmount != "400" ||
		oldLine2.Decision.ReasonCode != "TARIFF_EXCEEDED" ||
		oldLine2.Decision.Stage != domain.StageFinancial {
		t.Errorf("the cut changed: %+v", *oldLine2.Decision)
	}
	if old.Version.ReturnReasonCode == nil || *old.Version.ReturnReasonCode != "DOCUMENT_MISSING" {
		t.Errorf("the superseded version does not say why it came back: %v", old.Version.ReturnReasonCode)
	}

	// Version 2 is a draft carrying the same lines, clinical fields and all, and no decision.
	fresh, err := f.claims.GetVersion(context.Background(), f.medicalRC(), returned.Claim.ID, 2,
		application.AccessRequest{PurposeCode: "CLAIM_REVIEW", ReasonText: "düzeltme"})
	if err != nil {
		t.Fatalf("read the correction: %v", err)
	}
	if len(fresh.Lines) != 2 {
		t.Fatalf("the correction has %d lines, want the 2 that were copied", len(fresh.Lines))
	}
	for _, line := range fresh.Lines {
		if line.Decision != nil {
			t.Errorf("copied line %d arrived already decided", line.Line.LineNo)
		}
		if line.Line.Description == nil {
			t.Errorf("copied line %d lost its description", line.Line.LineNo)
		}
		if line.Line.DiagnosisID == nil {
			t.Errorf("copied line %d lost its diagnosis", line.Line.LineNo)
		}
	}
	if got := versionLine(t, fresh, 2).Line.LineAmount; got != physioTwoContract {
		t.Errorf("copied line 2 amount = %s, want %s", got, physioTwoContract)
	}
	f.ledgerConserved(t)
}

// TestOverConsumingAnAuthorizationIsAnExceptionNeverASilentConsume is the rule that keeps a
// mistake or a fraud visible.
//
// The hold is for two sessions and the claim bills three. Nothing is consumed — not the two
// that were left, not anything — the line goes to a person, and the exception quotes the
// figure the hold still had.
func TestOverConsumingAnAuthorizationIsAnExceptionNeverASilentConsume(t *testing.T) {
	f := newFixture(t)
	authorization := f.authorizeSessions(t, "2")

	draft := f.newClaim(t, []application.NewLineInput{f.physioLine(1, "3", "750")},
		func(in *application.NewClaimInput) { in.AuthorizationID = &authorization })
	submitted := submitClaim(t, f, draft)

	if submitted.Claim.Status != domain.StatusPendingMedical {
		t.Fatalf("status = %s, want PENDING_MEDICAL: an over-consumption needs a person",
			submitted.Claim.Status)
	}
	exception, found := exceptionByCode(submitted, application.ReasonAuthorizationExceeded)
	if !found {
		t.Fatalf("no AUTHORIZATION_EXCEEDED exception; got %+v", submitted.Exceptions)
	}
	if exception.LineNo != 1 {
		t.Errorf("exception names line %d, want 1", exception.LineNo)
	}
	// The figure the hold still had, exactly — "the hold had 2 left", not "too much".
	if exception.Detail != "2" {
		t.Errorf("exception detail = %q, want the remaining quantity 2", exception.Detail)
	}
	if exception.Stage != domain.StageMedical {
		t.Errorf("exception stage = %s, want MEDICAL", exception.Stage)
	}

	// Nothing moved. Not three, and — the point of the rule — not the two that were left.
	if got := f.consumedTotal(t, authorization); got != "0" {
		t.Fatalf("authorization consumed total = %s after an over-consumption, want 0", got)
	}
	if line := lineByNo(t, submitted, 1); line.Decision != nil {
		t.Errorf("an over-consumed line was decided as %s", line.Decision.Decision)
	}
	f.ledgerConserved(t)
}

// TestDuplicateSuspicionRoutesToFinancialReviewWithTheOtherReference: a second claim for the
// same person, the same service and the same day is not refused and is not settled — it is put
// in front of a person, with the reference of the claim it looks like a duplicate of.
func TestDuplicateSuspicionRoutesToFinancialReviewWithTheOtherReference(t *testing.T) {
	f := newFixture(t)

	first := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil))
	if first.Claim.Status != domain.StatusApproved {
		t.Fatalf("first claim status = %s, want APPROVED", first.Claim.Status)
	}

	second := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil))
	if second.Claim.Status != domain.StatusPendingFinancial {
		t.Fatalf("second claim status = %s, want PENDING_FINANCIAL", second.Claim.Status)
	}
	exception, found := exceptionByCode(second, application.ReasonDuplicateSuspected)
	if !found {
		t.Fatalf("no DUPLICATE_SUSPECTED exception; got %+v", second.Exceptions)
	}
	if exception.Detail != first.Claim.Reference {
		t.Errorf("exception detail = %q, want the other claim's reference %q",
			exception.Detail, first.Claim.Reference)
	}
	if exception.Stage != domain.StageFinancial || exception.LineNo != 1 {
		t.Errorf("exception = stage %s line %d, want FINANCIAL line 1", exception.Stage, exception.LineNo)
	}

	// A rejected claim is not a duplicate of anything: the third claim is raised after both
	// earlier ones have been refused, and nothing objects to it.
	rejectClaim(t, f, first.Claim.ID)
	rejectClaim(t, f, second.Claim.ID)
	third := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil))
	if _, found := exceptionByCode(third, application.ReasonDuplicateSuspected); found {
		t.Error("a rejected claim was still counted as a duplicate")
	}
	f.ledgerConserved(t)
}

// TestMedicalReviewPrecedesFinancialAndTheFinancialReviewerSeesNoClinicalDetail is section 2.3
// as two screens' worth of data.
//
// The claim needs both reviews. It lands in medical review; the financial reviewer cannot
// decide it at all, and what they can read carries no line description, no diagnosis reference
// and none of the medical reviewer's own words. The medical reviewer, reading the same claim
// through the same service, sees all three — which is what makes the absences above an
// assertion about the projection rather than about an empty fixture.
func TestMedicalReviewPrecedesFinancialAndTheFinancialReviewerSeesNoClinicalDetail(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "CONSULT_MEDICAL", `serviceCode == "CONSULT"`, actionMedicalReview)
	f.publishAdjudicationRule(t, "PHYSIO_FINANCIAL",
		`serviceCode == "PHYSIO_SESSION"`, actionFinancialReview)

	draft := f.newClaim(t, []application.NewLineInput{
		f.consultLine(1),
		f.physioLine(2, "2", physioTwoContract),
	}, nil)
	submitted := submitClaim(t, f, draft)
	if submitted.Claim.Status != domain.StatusPendingMedical {
		t.Fatalf("status = %s, want PENDING_MEDICAL when both reviews are needed",
			submitted.Claim.Status)
	}

	// The financial reviewer cannot reach a claim medical review has not finished.
	_, err := f.claims.DecideLines(context.Background(), f.financialRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 1, Decision: domain.DecisionApproved, ApprovedQuantity: "1",
				ApprovedAmount: consultPrice, PayerAmount: consultPayer,
				MemberAmount: consultMember, ReasonCode: "WITHIN_TARIFF",
			}},
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{})
	if !errors.Is(err, application.ErrStageMismatch) {
		t.Fatalf("financial decision on a claim in medical review = %v, want ErrStageMismatch", err)
	}

	// The medical reviewer decides both lines, in their own clinical words.
	clinicalReason := "Menisküs onarımı sonrası endikasyon uygundur."
	clinicalComment := "Ameliyat notu ve MR bulguları ile uyumlu."
	afterMedical, err := f.claims.DecideLines(context.Background(), f.medicalRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{
				{
					LineNo: 1, Decision: domain.DecisionApproved, ApprovedQuantity: "1",
					ApprovedAmount: consultPrice, PayerAmount: consultPayer,
					MemberAmount: consultMember, ReasonCode: "MEDICALLY_NECESSARY",
					ReasonText: &clinicalReason,
				},
				{
					LineNo: 2, Decision: domain.DecisionApproved, ApprovedQuantity: "2",
					ApprovedAmount: physioTwoContract, PayerAmount: physioTwoContract,
					MemberAmount: "0", ReasonCode: "MEDICALLY_NECESSARY",
					ReasonText: &clinicalReason,
				},
			},
			ReviewComment:   &clinicalComment,
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{PurposeCode: "MEDICAL_REVIEW", ReasonText: "inceleme"})
	if err != nil {
		t.Fatalf("medical decision: %v", err)
	}
	if afterMedical.Claim.Status != domain.StatusPendingFinancial {
		t.Fatalf("status after medical review = %s, want PENDING_FINANCIAL",
			afterMedical.Claim.Status)
	}

	// What the medical reviewer sees: everything. This is the half that proves the fixture
	// really carries the fields the next half asserts are gone.
	clinical, err := f.claims.GetClaim(context.Background(), f.medicalRC(), submitted.Claim.ID,
		application.AccessRequest{PurposeCode: "MEDICAL_REVIEW", ReasonText: "inceleme"})
	if err != nil {
		t.Fatalf("clinical read: %v", err)
	}
	if clinical.Projection != application.ProjectionClinical {
		t.Fatalf("medical reviewer got the %s projection", clinical.Projection)
	}
	for _, line := range clinical.Lines {
		if line.Line.Description == nil || *line.Line.Description == "" {
			t.Fatalf("line %d has no description in the fixture: the absence test below would prove nothing",
				line.Line.LineNo)
		}
		if line.Line.DiagnosisID == nil {
			t.Fatalf("line %d has no diagnosis in the fixture: the absence test below would prove nothing",
				line.Line.LineNo)
		}
		if line.Decision == nil || line.Decision.ReasonText == nil || *line.Decision.ReasonText != clinicalReason {
			t.Fatalf("line %d has no medical reason text in the fixture", line.Line.LineNo)
		}
	}
	if clinical.Claim.ReviewCommentMedical == nil || *clinical.Claim.ReviewCommentMedical != clinicalComment {
		t.Fatal("the medical review comment was not stored: the absence test below would prove nothing")
	}

	// What the financial reviewer sees: the money, and none of the above.
	financial, err := f.claims.GetClaim(context.Background(), f.financialRC(), submitted.Claim.ID,
		application.AccessRequest{})
	if err != nil {
		t.Fatalf("financial read: %v", err)
	}
	if financial.Projection != application.ProjectionFinancial {
		t.Fatalf("financial reviewer got the %s projection", financial.Projection)
	}
	if financial.Claim.ReviewCommentMedical != nil {
		t.Errorf("the financial projection carries the medical review comment: %q",
			*financial.Claim.ReviewCommentMedical)
	}
	for _, line := range financial.Lines {
		if line.Line.Description != nil {
			t.Errorf("line %d: the financial projection carries a description: %q",
				line.Line.LineNo, *line.Line.Description)
		}
		if line.Line.DiagnosisID != nil {
			t.Errorf("line %d: the financial projection carries a diagnosis reference",
				line.Line.LineNo)
		}
		if line.Decision == nil {
			t.Fatalf("line %d lost its decision in the financial projection", line.Line.LineNo)
		}
		// The reason *code* survives — a provider disputing a cut has to be told which rule
		// cut it — and the reviewer's own sentence does not.
		if line.Decision.ReasonCode != "MEDICALLY_NECESSARY" {
			t.Errorf("line %d lost its reason code", line.Line.LineNo)
		}
		if line.Decision.ReasonText != nil {
			t.Errorf("line %d: the financial projection carries a medical reason text: %q",
				line.Line.LineNo, *line.Decision.ReasonText)
		}
		// The money is all there: a financial reviewer who could not see it could not do the
		// job the projection exists for.
		if line.Decision.ApprovedAmount == "" || line.Decision.PayerAmount == "" {
			t.Errorf("line %d lost its figures in the financial projection", line.Line.LineNo)
		}
	}
	f.ledgerConserved(t)
}

// TestSponsorHRSeesNoClinicalFieldOfAClaimThatHasThemAll is the acceptance criterion of the
// visibility half, run against a claim that carries every clinical field there is: a
// description on both lines, a diagnosis reference on both, a medical reviewer's reason text
// and a medical review comment on the claim.
//
// The sponsor's HR user holds claim.read and nothing clinical. It sees the money and the
// process, and not one of the five.
func TestSponsorHRSeesNoClinicalFieldOfAClaimThatHasThemAll(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "CONSULT_MEDICAL", `serviceCode == "CONSULT"`, actionMedicalReview)

	draft := f.newClaim(t, []application.NewLineInput{
		f.consultLine(1),
		f.physioLine(2, "2", physioTwoContract),
	}, nil)
	submitted := submitClaim(t, f, draft)

	clinicalReason := "Sol diz menisküs onarımı sonrası kontrol gereklidir."
	clinicalComment := "MR raporu ve ameliyat notu incelendi."
	if _, err := f.claims.DecideLines(context.Background(), f.medicalRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 1, Decision: domain.DecisionApproved, ApprovedQuantity: "1",
				ApprovedAmount: consultPrice, PayerAmount: consultPayer,
				MemberAmount: consultMember, ReasonCode: "MEDICALLY_NECESSARY",
				ReasonText: &clinicalReason,
			}},
			ReviewComment:   &clinicalComment,
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{PurposeCode: "MEDICAL_REVIEW", ReasonText: "inceleme"}); err != nil {
		t.Fatalf("medical decision: %v", err)
	}

	// The fixture really carries all five. Without this half, everything below is a test over
	// an empty claim.
	clinical, err := f.claims.GetClaim(context.Background(), f.medicalRC(), submitted.Claim.ID,
		application.AccessRequest{PurposeCode: "MEDICAL_REVIEW", ReasonText: "inceleme"})
	if err != nil {
		t.Fatalf("clinical read: %v", err)
	}
	present := 0
	for _, line := range clinical.Lines {
		if line.Line.Description != nil {
			present++
		}
		if line.Line.DiagnosisID != nil {
			present++
		}
	}
	if present != 4 {
		t.Fatalf("the fixture carries %d of the 4 clinical line fields, not all of them", present)
	}
	if lineByNo(t, clinical, 1).Decision.ReasonText == nil {
		t.Fatal("the fixture carries no medical reason text")
	}
	if clinical.Claim.ReviewCommentMedical == nil {
		t.Fatal("the fixture carries no medical review comment")
	}

	// The scan: one read, and one page.
	hr := f.sponsorHRRC()
	single, err := f.claims.GetClaim(context.Background(), hr, submitted.Claim.ID,
		application.AccessRequest{})
	if err != nil {
		t.Fatalf("sponsor HR read: %v", err)
	}
	page, err := f.claims.ListClaims(context.Background(), hr, application.ClaimFilter{Limit: 50},
		application.AccessRequest{})
	if err != nil {
		t.Fatalf("sponsor HR list: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("sponsor HR sees no claim at all")
	}
	for _, view := range append([]application.ClaimView{single}, page.Items...) {
		if view.Projection != application.ProjectionFinancial {
			t.Errorf("sponsor HR got the %s projection", view.Projection)
		}
		if view.Claim.ReviewCommentMedical != nil {
			t.Errorf("sponsor HR can read the medical review comment: %q",
				*view.Claim.ReviewCommentMedical)
		}
		for _, line := range view.Lines {
			if line.Line.Description != nil {
				t.Errorf("sponsor HR can read line %d's description: %q",
					line.Line.LineNo, *line.Line.Description)
			}
			if line.Line.DiagnosisID != nil {
				t.Errorf("sponsor HR can read line %d's diagnosis reference", line.Line.LineNo)
			}
			if line.Line.MedicalReportID != nil {
				t.Errorf("sponsor HR can read line %d's medical report reference", line.Line.LineNo)
			}
			if line.Decision != nil && line.Decision.ReasonText != nil {
				t.Errorf("sponsor HR can read line %d's medical reason text: %q",
					line.Line.LineNo, *line.Decision.ReasonText)
			}
			// The money is not clinical, and hiding it would make the screen useless.
			if line.Line.LineAmount == "" {
				t.Errorf("sponsor HR lost line %d's amount", line.Line.LineNo)
			}
		}
	}

	// The clinical read left the trace it owes, and the sponsor's read did not: the access log
	// answers "who looked at this person's clinical data", and a financial read is not one.
	var clinicalReads int
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit.access_event
		 WHERE tenant_id = $1 AND resource_type = 'CLAIM' AND outcome = 'SUCCESS'`,
		f.tenant).Scan(&clinicalReads); err != nil {
		t.Fatalf("read the access log: %v", err)
	}
	if clinicalReads == 0 {
		t.Error("a clinical read of a claim left no access event")
	}
	f.ledgerConserved(t)
}

// TestCancellingAClaimReleasesWhatItWasHolding: a withdrawn claim must not keep a member's
// entitlement reserved, and the ledger has to still add up afterwards.
func TestCancellingAClaimReleasesWhatItWasHolding(t *testing.T) {
	f := newFixture(t)
	// The claim is routed to a reviewer, because that is the state a provider actually
	// withdraws from: a claim the payer has already answered is not withdrawn, it is appealed.
	f.publishAdjudicationRule(t, "PHYSIO_FINANCIAL",
		`serviceCode == "PHYSIO_SESSION"`, actionFinancialReview)
	authorization := f.authorizeSessions(t, "4")

	draft := f.newClaim(t, []application.NewLineInput{f.physioLine(1, "2", physioTwoContract)},
		func(in *application.NewClaimInput) { in.AuthorizationID = &authorization })
	submitted := submitClaim(t, f, draft)
	if got := f.consumedTotal(t, authorization); got != "2" {
		t.Fatalf("consumed total = %s, want 2", got)
	}

	cancelled, err := f.claims.Cancel(context.Background(), f.providerRC(), submitted.Claim.ID,
		application.ReasonInput{
			ReasonCode: "PROVIDER_WITHDREW", ExpectedVersion: submitted.Claim.RowVersion,
		})
	if err != nil {
		t.Fatalf("cancel claim: %v", err)
	}
	if cancelled.Claim.Status != domain.StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", cancelled.Claim.Status)
	}
	if cancelled.Claim.ClosedAt == nil {
		t.Error("a cancelled claim carries no closed_at")
	}
	f.ledgerConserved(t)
}

// TestALineDecidedTwiceKeepsBothRows: the append-only rule, asserted from the outside. A
// reviewer who cut a line and a reviewer who later restored it are both on the record, and the
// latest is the decision.
func TestALineDecidedTwiceKeepsBothRows(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "CONSULT_FINANCIAL", `serviceCode == "CONSULT"`, actionFinancialReview)

	submitted := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil))
	lineID := lineByNo(t, submitted, 1).Line.ID

	first, err := f.claims.DecideLines(context.Background(), f.financialRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 1, Decision: domain.DecisionCut, ApprovedQuantity: "1",
				ApprovedAmount: "300", PayerAmount: "300", MemberAmount: "0",
				ReasonCode: "TARIFF_EXCEEDED",
			}},
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{})
	if err != nil {
		t.Fatalf("first decision: %v", err)
	}
	second, err := f.claims.DecideLines(context.Background(), f.financialRC(), first.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 1, Decision: domain.DecisionApproved, ApprovedQuantity: "1",
				ApprovedAmount: consultPrice, PayerAmount: consultPayer,
				MemberAmount: consultMember, ReasonCode: "APPEAL_UPHELD",
			}},
			ExpectedVersion: first.Claim.RowVersion,
		}, application.AccessRequest{})
	if err != nil {
		t.Fatalf("second decision: %v", err)
	}

	head := lineByNo(t, second, 1)
	if head.Decision == nil || head.Decision.ReasonCode != "APPEAL_UPHELD" {
		t.Fatalf("the latest decision is not the head: %+v", head.Decision)
	}
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var rows int
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM claim.line_decision WHERE line_id = $1`, lineID).Scan(&rows); err != nil {
		t.Fatalf("count decisions: %v", err)
	}
	// One from the reviewer's cut, one from the restoration. The rule sent the line to
	// review, so there is no AUTO row beside them.
	if rows != 2 {
		t.Errorf("line carries %d decision rows, want the 2 that were written", rows)
	}
	f.ledgerConserved(t)
}

// rejectClaim refuses a claim as the financial reviewer would.
func rejectClaim(t *testing.T, f *fixture, id uuid.UUID) {
	t.Helper()
	current, err := f.claims.GetClaim(context.Background(), f.financialRC(), id,
		application.AccessRequest{})
	if err != nil {
		t.Fatalf("read claim before rejecting: %v", err)
	}
	// A claim the pipeline already approved cannot be rejected through the command, so the
	// test moves it back into review the way a return would and refuses it there. Doing it
	// through the database keeps the test about the duplicate check rather than about the
	// lifecycle, which has its own tests.
	f.h.AdminExec(`UPDATE claim.claim SET status = 'REJECTED', reject_reason_code = 'NOT_COVERED',
	                      closed_at = clock_timestamp() WHERE id = $1`, current.Claim.ID)
}

// versionLine finds one line of a version view by its number.
func versionLine(t *testing.T, view application.VersionView, lineNo int) application.LineView {
	t.Helper()
	for _, line := range view.Lines {
		if line.Line.LineNo == lineNo {
			return line
		}
	}
	t.Fatalf("version has no line %d", lineNo)
	return application.LineView{}
}

// TestAnUnpriceableLineGoesToFinancialReviewWithTheReason is step 1 of the pipeline on its own:
// a line the ladder cannot price is never guessed at. It goes to a person, and the exception
// carries the pricing explanation rather than "review needed" — PRICE_NOT_FOUND is what tells a
// financial reviewer that nobody contracted this service, which is a different job from
// disputing a price that exists.
func TestAnUnpriceableLineGoesToFinancialReviewWithTheReason(t *testing.T) {
	f := newFixture(t)
	// The laboratory test is deliberately left out of the contract's price list, so the
	// selection finds nothing at all.
	submitted := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{
		f.consultLine(1),
		f.labLine(2),
	}, nil))

	if submitted.Claim.Status != domain.StatusPendingFinancial {
		t.Fatalf("status = %s, want PENDING_FINANCIAL", submitted.Claim.Status)
	}
	exception, found := exceptionByCode(submitted, application.ReasonPriceReview)
	if !found {
		t.Fatalf("no PRICE_REVIEW_REQUIRED exception; got %+v", submitted.Exceptions)
	}
	if exception.LineNo != 2 || exception.Stage != domain.StageFinancial {
		t.Errorf("exception = line %d stage %s, want line 2 FINANCIAL", exception.LineNo, exception.Stage)
	}
	if exception.Detail != "PRICE_NOT_FOUND" {
		t.Errorf("exception detail = %q, want the pricing explanation PRICE_NOT_FOUND", exception.Detail)
	}
	// The priced line beside it was still decided, with nobody's name on it.
	priced := lineByNo(t, submitted, 1)
	if priced.Decision == nil || priced.Decision.Stage != domain.StageAuto {
		t.Fatalf("line 1 was not decided at the AUTO stage: %+v", priced.Decision)
	}
	if priced.Decision.ContractAmount == nil || *priced.Decision.ContractAmount != consultPrice {
		t.Errorf("line 1 contract amount = %v, want %s", priced.Decision.ContractAmount, consultPrice)
	}
	// And the unpriced one carries no contract amount at all rather than a zero somebody
	// could mistake for a price.
	if unpriced := lineByNo(t, submitted, 2); unpriced.Decision != nil {
		t.Errorf("an unpriceable line was decided as %s", unpriced.Decision.Decision)
	}
	f.ledgerConserved(t)
}

// TestDecidingAClaimPublishesClaimDecidedWithOnlySafeVariables is WP-I5-05's other half: the
// event has a template and recipients and no publisher, and this package is its publisher.
//
// The assertion is about what does *not* travel. A decision carries a reference, a status word,
// a day, a total, a currency and a link — six of the ten safe variables — and the claim it was
// published from carries a line description, a diagnosis and a medical reviewer's sentence. If
// any of those three reached the payload, this fails.
func TestDecidingAClaimPublishesClaimDecidedWithOnlySafeVariables(t *testing.T) {
	f := newFixture(t)
	f.publishAdjudicationRule(t, "CONSULT_MEDICAL", `serviceCode == "CONSULT"`, actionMedicalReview)

	submitted := submitClaim(t, f, f.newClaim(t, []application.NewLineInput{f.consultLine(1)}, nil))
	clinicalReason := "Sol diz menisküs onarımı sonrası kontrol gereklidir."
	decided, err := f.claims.DecideLines(context.Background(), f.medicalRC(), submitted.Claim.ID,
		application.DecideInput{
			Decisions: []application.DecisionInput{{
				LineNo: 1, Decision: domain.DecisionApproved, ApprovedQuantity: "1",
				ApprovedAmount: consultPrice, PayerAmount: consultPayer,
				MemberAmount: consultMember, ReasonCode: "MEDICALLY_NECESSARY",
				ReasonText: &clinicalReason,
			}},
			ExpectedVersion: submitted.Claim.RowVersion,
		}, application.AccessRequest{PurposeCode: "MEDICAL_REVIEW", ReasonText: "inceleme"})
	if err != nil {
		t.Fatalf("medical decision: %v", err)
	}
	approved, err := f.claims.Approve(context.Background(), f.medicalRC(), decided.Claim.ID,
		application.ReasonInput{ReasonCode: "COMPLETE", ExpectedVersion: decided.Claim.RowVersion})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT payload_json::text FROM system.outbox_event
		 WHERE tenant_id = $1 AND payload_json->>'eventCode' = 'claim.decided'`, f.tenant)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer rows.Close()
	payloads := []string{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the outbox: %v", err)
	}
	// The member and the provider organization, on both default channels.
	if len(payloads) != 4 {
		t.Fatalf("claim.decided produced %d outbox rows, want 4 (two recipients × two channels)",
			len(payloads))
	}
	for _, payload := range payloads {
		var envelope struct {
			Variables map[string]string `json:"variables"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		want := map[string]string{
			"reference_no": approved.Claim.Reference,
			"status_code":  domain.StatusApproved,
			"amount":       consultPrice,
			"currency":     "TRY",
		}
		for name, value := range want {
			if envelope.Variables[name] != value {
				t.Errorf("variable %s = %q, want %q", name, envelope.Variables[name], value)
			}
		}
		for name := range envelope.Variables {
			switch name {
			case "reference_no", "status_code", "event_date", "amount", "currency", "deep_link":
			default:
				t.Errorf("claim.decided carries the variable %q, which is not one of the six", name)
			}
		}
		// The three clinical facts the claim actually carries, none of which may travel.
		for _, forbidden := range []string{
			clinicalReason, "Sol diz artroskopi sonrası kontrol muayenesi", f.diagnosisID.String(),
		} {
			if strings.Contains(payload, forbidden) {
				t.Errorf("claim.decided payload carries clinical text: %q", forbidden)
			}
		}
	}
}
