package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/domain"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/pricing"
	"github.com/celikbros/kapsora/internal/rules/engine"
)

// The reason codes the AUTO stage decides and routes under. They are constants rather than
// literals because a screen groups by them, a provider disputes by them and the mock has to
// answer with exactly the same words.
const (
	// ReasonAutoApproved is a line nothing objected to.
	ReasonAutoApproved = "AUTO_APPROVED"
	// ReasonPriceReview is a line the pricing ladder could not price. It is never guessed
	// at: a number that looked like a price and was nobody's decision is worse than no
	// number.
	ReasonPriceReview = "PRICE_REVIEW_REQUIRED"
	// ReasonRuleRejected and ReasonRuleCut are what a rule decided about a line when it
	// named no reason of its own.
	ReasonRuleRejected = "RULE_REJECTED"
	ReasonRuleCut      = "RULE_CUT"
	// ReasonRuleMedicalReview and ReasonRuleFinancialReview are a rule asking for a person.
	ReasonRuleMedicalReview   = "RULE_MEDICAL_REVIEW"
	ReasonRuleFinancialReview = "RULE_FINANCIAL_REVIEW"
	// ReasonAuthorizationExceeded is the over-consumption exception: the line billed more
	// than the hold still had, and **nothing was consumed**.
	ReasonAuthorizationExceeded = "AUTHORIZATION_EXCEEDED"
	// ReasonStayOverAuthorization is WP-I5-03's reconciliation flag.
	ReasonStayOverAuthorization = "STAY_OVER_AUTHORIZATION"
	// ReasonDuplicateSuspected names another live claim for the same person, service and day.
	ReasonDuplicateSuspected = "DUPLICATE_SUSPECTED"
	// ReasonApprovalPolicy is the amount needing an approver the pipeline is not.
	ReasonApprovalPolicy = "APPROVAL_POLICY_REVIEW"
)

// ReasonClaimRejected is what a claim-level rejection writes on every line it closes.
const ReasonClaimRejected = "CLAIM_REJECTED"

// ActionCodeApprove is the approval policy action a claim's amount is looked up under.
const ActionCodeApprove = "claim.approve"

// QueueMedicalReview and QueueFinancialReview are the work queues a routed claim is raised
// into. They are codes rather than ids because a tenant configures its own queues and a
// deployment that has not configured one raises nothing rather than refusing the submit.
const (
	QueueMedicalReview   = "MEDICAL_REVIEW"
	QueueFinancialReview = "FINANCIAL_REVIEW"
)

// consumeReason is what a claim's draw on a hold is posted under in the ledger. It is part of
// the movement's idempotency key, so a claim consuming and a fulfilment consuming the same
// hold are two distinct movements and either of them replayed is still one.
const consumeReason = "CLAIM"

// snapshotVersion labels the frozen document, so a reader years from now knows which shape it
// is looking at.
const snapshotVersion = 1

// ClaimException is one thing the pipeline found that a person has to look at. It is not a
// decision — the line is still undecided — it is the answer to "why is this claim in front of
// me", line by line, which is the first thing either reviewer's screen has to say.
//
// `Detail` carries the fact the reviewer needs and nothing else: the reference of the other
// claim a duplicate suspicion found, the reference of the report that did not cover a line,
// the quantity a hold still had. Never a diagnosis, never a description.
type ClaimException struct {
	// LineNo is 0 for an exception about the claim as a whole.
	LineNo int
	Code   string
	// Stage is the review the exception routes to.
	Stage  string
	Detail string
}

// Clinical reports whether an exception is one the financial projection drops. The three
// report codes are: that a line leans on a treatment report at all is a fact about the
// patient, exactly as `medicalReportId` is, and an exception naming one would put it back on
// a screen the projection had just taken it off.
func (e ClaimException) Clinical() bool {
	switch e.Code {
	case healthapp.CoverageNotApproved, healthapp.CoverageOutOfWindow,
		healthapp.CoverageServiceNotCovered:
		return true
	default:
		return false
	}
}

// Submit freezes the draft version and runs the whole pipeline, in one transaction.
//
// The order is section 2.2's and it is not arbitrary. Pricing first, because the rules
// compare against a contract amount and a rule that saw no price would be a rule deciding on
// a number it invented. The rules second, because a rule may reject or cut a line and there
// is no point cross-checking an authorization for a line nobody is going to pay for. The
// cross-checks third, because they are the ones that touch other aggregates — the hold, the
// report, the stay — and everything they do has to roll back with the submit. The route last,
// because it is the sum of what the first three found.
//
// Everything is one transaction for the reason WP-I4-01 wrote down: a claim that was frozen
// but not routed, or routed against a hold that was rolled back, is a claim nobody can
// explain afterwards.
func (s *Service) Submit(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	expected int64, req AccessRequest,
) (ClaimView, error) {
	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandSubmit, current.Status) {
			return ErrTransitionInvalid
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		version, err := s.repo.GetDraftVersion(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return ErrLineRequired
		}

		outcomes, err := s.runPipeline(ctx, tx, rc, current, version, lines)
		if err != nil {
			return err
		}
		if err := s.freezeAndRoute(ctx, tx, rc, current, version, lines, outcomes, expected); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// lineOutcome is one line's way through the pipeline: what it was priced at, whether anything
// decided it, and what still needs a person.
type lineOutcome struct {
	line   LineRecord
	priced pricing.LineResult
	// contract is the contract amount as exact decimal text, or nil when the ladder found no
	// contracted price — which is itself the reason the line goes to review.
	contract *string
	// decision is "" while nothing has decided the line.
	decision         string
	approvedQuantity benefitdomain.Quantity
	approvedAmount   benefitdomain.Quantity
	payerAmount      benefitdomain.Quantity
	memberAmount     benefitdomain.Quantity
	reasonCode       string
	needsMedical     bool
	needsFinancial   bool
	exceptions       []ClaimException
}

// decided reports whether the AUTO stage answered this line.
func (o lineOutcome) decided() bool { return o.decision != "" }

// pipeline is the whole outcome of a submit: the per-line answers and the two routing flags.
type pipeline struct {
	lines             []lineOutcome
	currency          string
	medicalRequired   bool
	financialRequired bool
	exceptions        []ClaimException
}

// runPipeline is steps 1 to 3 of section 2.2. It writes nothing except through the ports it
// is given — the authorization consume and the report usage row, both inside this
// transaction — and returns what step 4 routes on.
func (s *Service) runPipeline(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, version VersionRecord, lines []LineRecord,
) (pipeline, error) {
	out := pipeline{lines: make([]lineOutcome, 0, len(lines)), exceptions: []ClaimException{}}

	// ---- 1. Price -------------------------------------------------------
	priced, err := s.priceLines(ctx, tx, rc, record, lines)
	if err != nil {
		return pipeline{}, err
	}
	out.currency = priced.CurrencyCode
	for i, line := range lines {
		outcome := lineOutcome{
			line: line, approvedQuantity: zero(), approvedAmount: zero(),
			payerAmount: zero(), memberAmount: zero(), exceptions: []ClaimException{},
		}
		if i < len(priced.Lines) {
			outcome.priced = priced.Lines[i]
		}
		if outcome.priced.Outcome == pricing.OutcomeReviewRequired {
			// The pricing explanation travels with the exception, so a financial reviewer
			// is told PRICE_NOT_FOUND or PRICE_AMBIGUOUS rather than "review needed".
			outcome.needsFinancial = true
			outcome.exceptions = append(outcome.exceptions, ClaimException{
				LineNo: line.LineNo, Code: ReasonPriceReview, Stage: domain.StageFinancial,
				Detail: firstExplanation(outcome.priced),
			})
		} else {
			contract := outcome.priced.Contract.String()
			outcome.contract = &contract
		}
		out.lines = append(out.lines, outcome)
	}

	// ---- 2. Rules -------------------------------------------------------
	if err := s.applyRules(ctx, tx, rc, record, version, out.lines); err != nil {
		return pipeline{}, err
	}

	// ---- 3. Cross-checks that are not rules -----------------------------
	if err := s.crossCheck(ctx, tx, rc, record, out.lines); err != nil {
		return pipeline{}, err
	}

	for i := range out.lines {
		outcome := &out.lines[i]
		if outcome.decided() {
			// A line the rules decided needs nobody, whatever it was decided as.
			outcome.needsMedical, outcome.needsFinancial = false, false
			continue
		}
		if outcome.needsMedical {
			out.medicalRequired = true
		}
		if outcome.needsFinancial {
			out.financialRequired = true
		}
	}
	for _, outcome := range out.lines {
		if outcome.decided() {
			continue
		}
		out.exceptions = append(out.exceptions, outcome.exceptions...)
	}
	return out, nil
}

// priceLines runs WP-I3-05's ladder over the claim's lines. The provider profile is looked up
// here because a contract is signed with a profile and a claim names an organization; this is
// the one place the two meet.
func (s *Service) priceLines(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, lines []LineRecord,
) (PricingResult, error) {
	profile, err := s.repo.ProviderProfile(ctx, tx, rc.TenantID, record.ProviderOrganizationID)
	if err != nil {
		return PricingResult{}, err
	}
	wanted := make([]uuid.UUID, 0, len(lines))
	for _, line := range lines {
		wanted = append(wanted, line.ServiceDefinitionID)
	}
	// The entitlement each line draws on, from WP-I5-05's mapping. A service nobody mapped
	// has no code, which the ladder reads as "no balance is known to cover this" — the
	// honest answer, and one that shows up as a member share rather than as a silent zero.
	codes, err := s.repo.EntitlementCodes(ctx, tx, rc.TenantID, record.EnrollmentID,
		record.ServiceDateFrom, wanted)
	if err != nil {
		return PricingResult{}, err
	}
	items := make([]PricingItem, 0, len(lines))
	for _, line := range lines {
		quantity, err := quantityOf(line.Quantity, "quantity")
		if err != nil {
			return PricingResult{}, err
		}
		requested, err := quantityOf(line.LineAmount, "lineAmount")
		if err != nil {
			return PricingResult{}, err
		}
		items = append(items, PricingItem{
			ServiceDefinitionID: line.ServiceDefinitionID, Quantity: quantity,
			RequestedAmount: requested, EntitlementCode: codes[line.ServiceDefinitionID],
		})
	}
	return s.pricing.PriceLines(ctx, tx, rc.TenantID, PricingRequest{
		PersonID: record.PersonID, ProgramID: record.ProgramID,
		ProviderProfileID: profile.ID, ServiceDate: record.ServiceDateFrom, Items: items,
	})
}

// applyRules evaluates the published ADJUDICATION rule sets once per line and folds what they
// asked for onto that line.
//
// It is per line rather than per claim because the decisions are per line: a rule that cuts
// physiotherapy to eighty per cent has to cut the physiotherapy line and leave the
// consultation alone, and a claim-level pass would have no way to say which.
func (s *Service) applyRules(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, version VersionRecord, outcomes []lineOutcome,
) error {
	for i := range outcomes {
		outcome := &outcomes[i]
		result, err := s.rules.EvaluateClaimLine(ctx, tx, rc.TenantID, record.ID,
			record.ServiceDateFrom, ruleInput(record, version, *outcome))
		if err != nil {
			return err
		}
		for _, action := range result.Actions {
			s.applyAction(outcome, action)
		}
	}
	return nil
}

// applyAction folds one rule action onto one line.
//
// The five the work package names map onto the engine's closed action list (WP-I3-04 2.3),
// which is where they already live: REQUIRE_MEDICAL_REVIEW and REQUIRE_FINANCIAL_REVIEW route,
// REJECT is the reject-line action, PARTIAL_APPROVE is the cut, and APPROVE is the
// auto-approve. Nothing new was added to the closed list: an action type the rule author's own
// screen would refuse to write is an action type no rule can ever produce.
func (s *Service) applyAction(outcome *lineOutcome, action RuleAction) {
	switch action.Type {
	case engine.ActionRequireMedicalReview:
		outcome.needsMedical = true
		outcome.exceptions = append(outcome.exceptions, ClaimException{
			LineNo: outcome.line.LineNo, Code: ReasonRuleMedicalReview,
			Stage: domain.StageMedical, Detail: action.RuleCode,
		})
	case engine.ActionRequireFinancialReview:
		outcome.needsFinancial = true
		outcome.exceptions = append(outcome.exceptions, ClaimException{
			LineNo: outcome.line.LineNo, Code: ReasonRuleFinancialReview,
			Stage: domain.StageFinancial, Detail: action.RuleCode,
		})
	case engine.ActionReject:
		reason := action.ReasonCode
		if reason == "" {
			reason = ReasonRuleRejected
		}
		outcome.decision = domain.DecisionRejected
		outcome.reasonCode = reason
		outcome.approvedQuantity, outcome.approvedAmount = zero(), zero()
		outcome.payerAmount, outcome.memberAmount = zero(), zero()
	case engine.ActionPartialApprove:
		s.applyCut(outcome, action)
	case engine.ActionApprove:
		// The pipeline's own answer to "nothing objected" is below. An APPROVE from a rule
		// is a rule saying it has no objection, not a rule overriding a later one that has.
	}
}

// applyCut is PARTIAL_APPROVE: the payer carries less of the line than the ladder said it
// would. Either a percentage of the covered amount or a flat amount, never both — the action's
// own payload spec enforces that on write.
//
// The member does **not** pick up what the payer put down. A cut is the payer refusing part of
// a bill, and billing the member for the difference would turn a payer's decision into a
// member's debt. So the approved amount comes down with the payer amount and the member's
// share stays exactly what the ladder computed.
func (s *Service) applyCut(outcome *lineOutcome, action RuleAction) {
	payer := outcome.priced.Payer
	member := outcome.priced.Member
	switch {
	case action.Percent != "":
		percent, err := benefitdomain.ParseQuantity(action.Percent)
		if err != nil {
			return
		}
		payer = payer.Percent(percent)
	case action.Amount != "":
		amount, err := benefitdomain.ParseQuantity(action.Amount)
		if err != nil {
			return
		}
		payer = payer.Min(amount)
	default:
		return
	}
	if payer.IsNegative() {
		payer = zero()
	}
	outcome.decision = domain.DecisionCut
	outcome.reasonCode = ReasonRuleCut
	outcome.approvedQuantity = quantityOrZero(outcome.line.Quantity)
	outcome.payerAmount = payer
	outcome.memberAmount = member
	outcome.approvedAmount = payer.Add(member)
}

// crossCheck is section 2.2 step 3: the four things that are facts about other aggregates
// rather than rules about this one.
func (s *Service) crossCheck(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, outcomes []lineOutcome,
) error {
	// The stay's reconciliation is a fact about the case, not about any one line, so it is
	// asked once and attached to the claim rather than repeated on every line.
	if record.CaseID != nil {
		over, err := s.repo.CaseOverAuthorization(ctx, tx, rc.TenantID, *record.CaseID)
		if err != nil {
			return err
		}
		if over && len(outcomes) > 0 {
			outcomes[0].needsMedical = true
			outcomes[0].exceptions = append(outcomes[0].exceptions, ClaimException{
				Code: ReasonStayOverAuthorization, Stage: domain.StageMedical,
			})
		}
	}
	for i := range outcomes {
		outcome := &outcomes[i]
		if outcome.decided() {
			// A line the rules already rejected or cut consumes nothing and is checked
			// against nothing: there is no point drawing on a hold for a line nobody will
			// pay for, and an over-consumption exception on a rejected line would send a
			// reviewer a question with no consequence.
			continue
		}
		if err := s.checkAuthorization(ctx, tx, rc, record, outcome); err != nil {
			return err
		}
		if err := s.checkReport(ctx, tx, rc, record, outcome); err != nil {
			return err
		}
		if err := s.checkDuplicate(ctx, tx, rc, record, outcome); err != nil {
			return err
		}
	}
	return nil
}

// checkAuthorization draws the line's quantity out of the claim's hold.
//
// **An over-consumption is an exception line and nothing moves.** A hold for four sessions
// billed for six is either a mistake or a fraud, and consuming the four that were left would
// hide both — the claim would settle, the member's balance would be spent, and the two extra
// sessions would be invisible. So the port reports it, writes nothing, and the line goes to a
// person with the figure the hold still had.
func (s *Service) checkAuthorization(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, outcome *lineOutcome,
) error {
	if record.AuthorizationID == nil {
		return nil
	}
	quantity, err := quantityOf(outcome.line.Quantity, "quantity")
	if err != nil {
		return err
	}
	answer, err := s.authorizations.Consume(ctx, tx, ConsumeRequest{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
		AuthorizationID:     *record.AuthorizationID,
		ServiceDefinitionID: outcome.line.ServiceDefinitionID, Quantity: quantity,
		Key: consumeKey(outcome.line.ID), ReasonCode: consumeReason,
	})
	if err != nil {
		return err
	}
	if answer.OverConsumed {
		outcome.needsMedical = true
		outcome.exceptions = append(outcome.exceptions, ClaimException{
			LineNo: outcome.line.LineNo, Code: ReasonAuthorizationExceeded,
			Stage: domain.StageMedical,
			// The remaining quantity, exactly, so the reviewer reads "the hold had 2 left"
			// rather than "too much".
			Detail: answer.Remaining.String(),
		})
	}
	return nil
}

// checkReport asks WP-I5-02 whether the claim may lean on the report a line names, and writes
// the usage row that says it did.
func (s *Service) checkReport(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, outcome *lineOutcome,
) error {
	if outcome.line.MedicalReportID == nil {
		return nil
	}
	coverage, err := s.reports.ReportCoverage(ctx, tx, healthapp.CoverageRequest{
		TenantID: rc.TenantID, ReportID: *outcome.line.MedicalReportID,
		ServiceDefinitionID: outcome.line.ServiceDefinitionID,
		ServiceDate:         record.ServiceDateFrom,
		UsedByType:          "CLAIM", UsedByID: record.ID,
		ActorID: actorPtr(rc.Principal.ActorID),
	})
	if err != nil {
		return err
	}
	if coverage.Usable {
		return nil
	}
	outcome.needsMedical = true
	outcome.exceptions = append(outcome.exceptions, ClaimException{
		LineNo: outcome.line.LineNo, Code: coverage.ReasonCode, Stage: domain.StageMedical,
		Detail: coverage.Reference,
	})
	return nil
}

// checkDuplicate looks for another live claim of the same person carrying the same service on
// a day this claim's window covers.
//
// The other claim's reference travels with the exception, because "we think you have already
// billed this" is only actionable if the provider is told which one. A reference is not
// personal data and not clinical: it is the string on the provider's own paperwork.
func (s *Service) checkDuplicate(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, outcome *lineOutcome,
) error {
	other, found, err := s.repo.FindDuplicate(ctx, tx, rc.TenantID, record.ID, record.PersonID,
		outcome.line.ServiceDefinitionID, record.ServiceDateFrom, record.ServiceDateTo)
	if err != nil || !found {
		return err
	}
	outcome.needsFinancial = true
	outcome.exceptions = append(outcome.exceptions, ClaimException{
		LineNo: outcome.line.LineNo, Code: ReasonDuplicateSuspected,
		Stage: domain.StageFinancial, Detail: other.Reference,
	})
	return nil
}

// freezeAndRoute is step 4: the snapshot, the AUTO decisions, the status and the work item.
func (s *Service) freezeAndRoute(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, version VersionRecord, lines []LineRecord, result pipeline, expected int64,
) error {
	now := s.now().UTC()
	snapshot, err := buildSnapshot(record, version, lines, result)
	if err != nil {
		return err
	}
	frozen, err := s.repo.FreezeVersion(ctx, tx, rc.TenantID, version.ID, FreezeRow{
		Snapshot: snapshot, SubmittedAt: now, ActorID: actorPtr(rc.Principal.ActorID),
	})
	if err != nil {
		return err
	}
	if !frozen {
		// The DRAFT predicate did not match, which means somebody submitted this version
		// while this transaction was reading it.
		return ErrTransitionInvalid
	}

	// Every step that decided something writes its decision, at the AUTO stage and with no
	// actor: the rules decided and nobody looked.
	//
	// A line nothing objected to is decided here too, with the ladder's own figures — even
	// when another line of the same claim needs a person. That is what makes a reviewer's
	// screen show one line to decide rather than five, and it is why the AUTO stage exists at
	// all: "the system decided this and nobody looked" is a fact about a line, not about a
	// claim. A line that does need a person is left undecided, because nothing has decided it.
	for _, outcome := range result.lines {
		switch {
		case outcome.decided():
			if err := s.writeAutoDecision(ctx, tx, rc, version.VersionNo, outcome, now); err != nil {
				return err
			}
		case outcome.needsMedical || outcome.needsFinancial:
			continue
		default:
			if err := s.writeAutoApproval(ctx, tx, rc, version.VersionNo, outcome, now); err != nil {
				return err
			}
		}
	}

	status, reason, err := s.route(ctx, rc, record, result)
	if err != nil {
		return err
	}

	var closedAt *time.Time
	if domain.Closed(status) {
		closedAt = &now
	}
	statusRow := StatusRow{
		Status: status, CurrentVersionNo: version.VersionNo, ClosedAt: closedAt,
		FromStatuses: domain.From(domain.CommandSubmit), ActorID: actorPtr(rc.Principal.ActorID),
	}
	if status == domain.StatusRejected {
		code := reason
		statusRow.RejectReasonCode = &code
	}
	moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, record.ID, statusRow, expected)
	if err != nil {
		return err
	}
	if !moved {
		return ErrVersionMismatch
	}
	if err := s.raiseWork(ctx, tx, rc, record, status); err != nil {
		return err
	}
	// A claim the pipeline settled on its own is decided, and a decision is something the
	// member and the provider are told about.
	if domain.Decided(status) || status == domain.StatusRejected {
		if err := s.notifyDecided(ctx, tx, rc, record, status, result.totals(), result.currency, now); err != nil {
			return err
		}
	}
	return s.record(ctx, tx, rc, "claim.submit", record.ID, map[string]any{
		"reference": record.Reference, "version_no": version.VersionNo,
		"status": status, "reason_code": reason, "line_count": len(lines),
		"exception_count": len(result.exceptions),
	})
}

// route is step 4's decision. Medical review precedes financial when both are needed, which is
// the whole reason the two flags are separate: a claim needing both lands in PENDING_MEDICAL
// and the snapshot remembers that financial is still owed, so finishing the medical stage
// moves it on rather than finishing the claim.
func (s *Service) route(ctx context.Context, rc identity.RequestContext, record ClaimRecord,
	result pipeline,
) (status, reason string, err error) {
	switch {
	case result.medicalRequired:
		return domain.StatusPendingMedical, ReasonRuleMedicalReview, nil
	case result.financialRequired:
		return domain.StatusPendingFinancial, ReasonRuleFinancialReview, nil
	}

	// Nothing needs a person on the merits. Whether the amount needs one is the tenant's
	// approval policy, not a threshold hard-coded here.
	totals := result.totals()
	policy, err := s.policies.Resolve(ctx, rc, PolicyLookup{
		ActionCode: ActionCodeApprove, Amount: totals.Approved.String(),
		AsOf: record.ServiceDateFrom,
	})
	if err != nil {
		return "", "", err
	}
	if policy.Found && policy.RequiredApproverCount > 0 {
		return domain.StatusPendingFinancial, ReasonApprovalPolicy, nil
	}
	return result.autoStatus(), ReasonAutoApproved, nil
}

// totals sums what the pipeline decided. Lines nobody decided count as nothing: an
// auto-adjudicated claim has no such line, and a routed claim's totals are answered by the
// readiness endpoint after a person has decided them.
type pipelineTotals struct {
	Approved benefitdomain.Quantity
	Payer    benefitdomain.Quantity
	Member   benefitdomain.Quantity
}

func (p pipeline) totals() pipelineTotals {
	out := pipelineTotals{Approved: zero(), Payer: zero(), Member: zero()}
	for _, outcome := range p.lines {
		approved, payer, member := outcome.finalAmounts()
		out.Approved = out.Approved.Add(approved)
		out.Payer = out.Payer.Add(payer)
		out.Member = out.Member.Add(member)
	}
	return out
}

// finalAmounts is what a line is worth once the AUTO stage has had its say: the decision's own
// figures when something decided it, the ladder's when nothing did.
func (o lineOutcome) finalAmounts() (approved, payer, member benefitdomain.Quantity) {
	if o.decided() {
		return o.approvedAmount, o.payerAmount, o.memberAmount
	}
	return o.priced.Payer.Add(o.priced.Member), o.priced.Payer, o.priced.Member
}

// autoStatus is what a claim nobody has to look at lands on: APPROVED when every line was
// approved, REJECTED when every line was refused, and PARTIALLY_APPROVED in between.
func (p pipeline) autoStatus() string {
	approvedLines, rejectedLines := 0, 0
	for _, outcome := range p.lines {
		if outcome.decision == domain.DecisionRejected {
			rejectedLines++
			continue
		}
		approvedLines++
	}
	switch {
	case approvedLines == 0:
		return domain.StatusRejected
	case rejectedLines > 0:
		return domain.StatusPartiallyApproved
	default:
		return domain.StatusApproved
	}
}

// writeAutoDecision records what a rule decided about a line.
func (s *Service) writeAutoDecision(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	versionNo int, outcome lineOutcome, now time.Time,
) error {
	_, err := s.repo.CreateDecision(ctx, tx, rc.TenantID, NewDecisionRow{
		LineID: outcome.line.ID, DecidedInVersionNo: versionNo, Decision: outcome.decision,
		ApprovedQuantity: outcome.approvedQuantity.String(),
		ApprovedAmount:   outcome.approvedAmount.String(), ContractAmount: outcome.contract,
		PayerAmount: outcome.payerAmount.String(), MemberAmount: outcome.memberAmount.String(),
		ReasonCode: outcome.reasonCode, DecidedAt: now, Stage: domain.StageAuto,
	})
	return err
}

// writeAutoApproval records a line nothing objected to, with the ladder's own figures. The
// payer and member halves are the ones `pricing.Calculate` rounded once, so they add up to the
// approved amount exactly and the database CHECK never fires.
func (s *Service) writeAutoApproval(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	versionNo int, outcome lineOutcome, now time.Time,
) error {
	approved, payer, member := outcome.finalAmounts()
	_, err := s.repo.CreateDecision(ctx, tx, rc.TenantID, NewDecisionRow{
		LineID: outcome.line.ID, DecidedInVersionNo: versionNo,
		Decision:         domain.DecisionApproved,
		ApprovedQuantity: quantityOrZero(outcome.line.Quantity).String(),
		ApprovedAmount:   approved.String(), ContractAmount: outcome.contract,
		PayerAmount: payer.String(), MemberAmount: member.String(),
		ReasonCode: ReasonAutoApproved, DecidedAt: now, Stage: domain.StageAuto,
	})
	return err
}

// raiseWork puts the claim in front of somebody. The title is the reference and nothing else:
// a queue is a list people read across a room.
func (s *Service) raiseWork(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, status string,
) error {
	queue := ""
	switch status {
	case domain.StatusPendingMedical:
		queue = QueueMedicalReview
	case domain.StatusPendingFinancial:
		queue = QueueFinancialReview
	default:
		return nil
	}
	return s.workItems.Raise(ctx, tx, rc.TenantID, RaiseWorkItem{
		QueueCode: queue, AggregateType: domain.AggregateClaim, AggregateID: record.ID,
		Title: "Hasar dosyası " + record.Reference, ActorID: actorPtr(rc.Principal.ActorID),
	})
}

// releaseHold gives back whatever the claim's authorization is still holding. It is a ceiling
// rather than an amount, so a hold something else has already consumed is not released twice.
func (s *Service) releaseHold(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, reasonCode string,
) error {
	if record.AuthorizationID == nil {
		return nil
	}
	_, err := s.authorizations.ReleaseUnused(ctx, tx, ReleaseRequest{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
		AuthorizationID: *record.AuthorizationID, Quantity: maxRelease, ReasonCode: reasonCode,
	})
	return err
}

// maxRelease is the ceiling a full release asks for. `ReleaseUnused` gives back what is
// outstanding up to this, so a number larger than any hold means "everything that is left".
var maxRelease = mustQuantity("999999999")

func mustQuantity(raw string) benefitdomain.Quantity {
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		panic("claim: " + raw + " is not a quantity")
	}
	return value
}

// quantityOrZero parses an exact decimal that the database has already validated, so a parse
// failure here is impossible and zero is the safe reading of it.
func quantityOrZero(raw string) benefitdomain.Quantity {
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return zero()
	}
	return value
}

// consumeKey is the idempotency key a claim line's draw on a hold is posted under. It is
// derived from the line rather than from a clock, so a redelivered submit consumes once.
func consumeKey(lineID uuid.UUID) string { return "claim-line:" + lineID.String() }

// firstExplanation is the pricing explanation a review exception quotes. The first is the one
// that matters: `Calculate` puts the reason a line could not be priced at the front.
func firstExplanation(line pricing.LineResult) string {
	for _, explanation := range line.Explanations {
		if explanation.Severity == pricing.SeverityError {
			return explanation.Code
		}
	}
	if len(line.Explanations) > 0 {
		return line.Explanations[0].Code
	}
	return ""
}

// ruleInput is the document a CLAIM rule decides on.
//
// Every amount and quantity is an exact decimal string, never a JSON number: a rule comparing
// a float against a band edge would fire on one machine and not on another.
//
// **Nothing clinical is in it.** There is no diagnosis, no description and no report id — the
// evaluation's input snapshot is stored, and a stored document is exactly where a clinical
// fact must never end up. A rule that needs a diagnosis is a DIAGNOSIS_SERVICE rule, which is
// somebody else's purpose and somebody else's input.
func ruleInput(record ClaimRecord, version VersionRecord, outcome lineOutcome) map[string]any {
	quantity := quantityOrZero(outcome.line.Quantity)
	contract := ""
	if outcome.contract != nil {
		contract = *outcome.contract
	}
	serviceCode := ""
	if outcome.line.ServiceCode != nil {
		serviceCode = *outcome.line.ServiceCode
	}
	return map[string]any{
		"claimReference":         record.Reference,
		"serviceDate":            record.ServiceDateFrom.UTC().Format(time.DateOnly),
		"channel":                record.Channel,
		"domainCode":             record.DomainCode,
		"personId":               record.PersonID.String(),
		"programId":              record.ProgramID.String(),
		"enrollmentId":           record.EnrollmentID.String(),
		"providerOrganizationId": record.ProviderOrganizationID.String(),
		"versionNo":              version.VersionNo,
		"lineNo":                 outcome.line.LineNo,
		"serviceDefinitionId":    outcome.line.ServiceDefinitionID.String(),
		"serviceCode":            serviceCode,
		"unitType":               outcome.line.UnitType,
		"quantity":               quantity.String(),
		"lineAmount":             outcome.line.LineAmount,
		"currencyCode":           outcome.line.CurrencyCode,
		"contractAmount":         contract,
		"coveredAmount":          outcome.priced.Covered.String(),
		"payerAmount":            outcome.priced.Payer.String(),
		"memberAmount":           outcome.priced.Member.String(),
		"pricingOutcome":         string(outcome.priced.Outcome),
	}
}

// The frozen document. Nothing personal goes into it: every field is an id, a date, a code, a
// quantity, an amount or an outcome. A name or an identifier in an immutable snapshot is a
// name nobody can ever remove — and a description or a diagnosis in one is worse.

type snapshotLine struct {
	LineNo              int    `json:"lineNo"`
	ServiceDefinitionID string `json:"serviceDefinitionId"`
	ServiceCode         string `json:"serviceCode,omitempty"`
	UnitType            string `json:"unitType"`
	Quantity            string `json:"quantity"`
	UnitAmount          string `json:"unitAmount,omitempty"`
	LineAmount          string `json:"lineAmount"`
	CurrencyCode        string `json:"currencyCode"`
}

type snapshotPricedLine struct {
	LineNo       int      `json:"lineNo"`
	Outcome      string   `json:"outcome"`
	Contract     string   `json:"contractAmount"`
	Covered      string   `json:"coveredAmount"`
	Payer        string   `json:"payerAmount"`
	Member       string   `json:"memberAmount"`
	Explanations []string `json:"explanations"`
}

type snapshotException struct {
	LineNo int    `json:"lineNo,omitempty"`
	Code   string `json:"code"`
	Stage  string `json:"stage"`
	Detail string `json:"detail,omitempty"`
}

type snapshotRouting struct {
	MedicalRequired   bool                `json:"medicalRequired"`
	FinancialRequired bool                `json:"financialRequired"`
	Exceptions        []snapshotException `json:"exceptions"`
}

type claimSnapshot struct {
	SnapshotVersion int                  `json:"snapshotVersion"`
	ClaimID         string               `json:"claimId"`
	Reference       string               `json:"reference"`
	VersionNo       int                  `json:"versionNo"`
	ServiceDateFrom string               `json:"serviceDateFrom"`
	ServiceDateTo   string               `json:"serviceDateTo"`
	CurrencyCode    string               `json:"currencyCode"`
	Lines           []snapshotLine       `json:"lines"`
	Pricing         []snapshotPricedLine `json:"pricing"`
	Routing         snapshotRouting      `json:"routing"`
}

// buildSnapshot freezes the version: what was sent, what it was priced at, and what still
// needed a person. It is the document a dispute years later is answered from, and it is what
// makes "financial review is still owed" survive the medical stage — the routing flags are
// read back at every decision, so a claim that needs both stages cannot forget the second one
// because a process restarted.
func buildSnapshot(record ClaimRecord, version VersionRecord, lines []LineRecord,
	result pipeline,
) ([]byte, error) {
	doc := claimSnapshot{
		SnapshotVersion: snapshotVersion, ClaimID: record.ID.String(),
		Reference: record.Reference, VersionNo: version.VersionNo,
		ServiceDateFrom: record.ServiceDateFrom.UTC().Format(time.DateOnly),
		ServiceDateTo:   record.ServiceDateTo.UTC().Format(time.DateOnly),
		CurrencyCode:    result.currency,
		Lines:           make([]snapshotLine, 0, len(lines)),
		Pricing:         make([]snapshotPricedLine, 0, len(result.lines)),
		Routing: snapshotRouting{
			MedicalRequired: result.medicalRequired, FinancialRequired: result.financialRequired,
			Exceptions: make([]snapshotException, 0, len(result.exceptions)),
		},
	}
	for _, line := range lines {
		row := snapshotLine{
			LineNo: line.LineNo, ServiceDefinitionID: line.ServiceDefinitionID.String(),
			UnitType: line.UnitType, Quantity: line.Quantity, LineAmount: line.LineAmount,
			CurrencyCode: line.CurrencyCode,
		}
		if line.ServiceCode != nil {
			row.ServiceCode = *line.ServiceCode
		}
		if line.UnitAmount != nil {
			row.UnitAmount = *line.UnitAmount
		}
		doc.Lines = append(doc.Lines, row)
	}
	for _, outcome := range result.lines {
		codes := make([]string, 0, len(outcome.priced.Explanations))
		for _, explanation := range outcome.priced.Explanations {
			codes = append(codes, explanation.Code)
		}
		doc.Pricing = append(doc.Pricing, snapshotPricedLine{
			LineNo: outcome.line.LineNo, Outcome: string(outcome.priced.Outcome),
			Contract: outcome.priced.Contract.String(), Covered: outcome.priced.Covered.String(),
			Payer: outcome.priced.Payer.String(), Member: outcome.priced.Member.String(),
			Explanations: codes,
		})
	}
	for _, exception := range result.exceptions {
		doc.Routing.Exceptions = append(doc.Routing.Exceptions, snapshotException(exception))
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("claim: encode version snapshot: %w", err)
	}
	return out, nil
}

// readRouting reads back whether financial review is still owed and what the pipeline found. A
// version that has never been submitted carries an empty document, which reads as "nothing is
// owed and nothing was found" — the right answer for a draft.
//
// Only the financial flag is read back. The medical one decided where the submit routed the
// claim and is spent the moment it did; the financial one has to survive the medical stage,
// because "medical first, then financial" is a fact about a claim that outlives the request
// that established it.
func readRouting(snapshot []byte) (financialRequired bool, exceptions []ClaimException) {
	exceptions = []ClaimException{}
	if len(snapshot) == 0 {
		return false, exceptions
	}
	var doc claimSnapshot
	if err := json.Unmarshal(snapshot, &doc); err != nil {
		return false, exceptions
	}
	for _, row := range doc.Routing.Exceptions {
		exceptions = append(exceptions, ClaimException(row))
	}
	return doc.Routing.FinancialRequired, exceptions
}

// projectExceptions drops the exceptions the financial projection may not carry. The three
// report codes are the clinical ones: an exception naming a treatment report says the line
// leans on one, which is the fact `medicalReportId` was just taken off the record for.
func projectExceptions(rows []ClaimException, p Projection) []ClaimException {
	out := make([]ClaimException, 0, len(rows))
	for _, row := range rows {
		if p != ProjectionClinical && row.Clinical() {
			continue
		}
		out = append(out, row)
	}
	return out
}
