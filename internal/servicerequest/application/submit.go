package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/rules/engine"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// The purposes of the rule sets the submit gate consults. Nothing else is evaluated here:
// a PRICE rule belongs to the quote and an ADJUDICATION rule to the claim, and a request
// that had been silently repriced on its way through this gate would be a request nobody
// could explain.
var gatePurposes = []string{"DOCUMENT", "PREAUTH"}

// Variables the gate supplies to a DOCUMENT or PREAUTH rule set version. A version
// declaring anything else still compiles; the missing variable then fails that one rule at
// run time with a RULE_ERROR line naming it, which is more use to the author than a
// blanket refusal from here.
//
// Every amount and quantity arrives as an exact decimal string, never as a JSON number.
const (
	varServiceDate            = "serviceDate"
	varRequestType            = "requestType"
	varChannel                = "channel"
	varPersonID               = "personId"
	varProgramID              = "programId"
	varEnrollmentID           = "enrollmentId"
	varProviderOrganizationID = "providerOrganizationId"
	varItemCount              = "itemCount"
	varTotalQuantity          = "totalQuantity"
	varTotalAmount            = "totalAmount"
	varCurrencyCode           = "currencyCode"
	varServiceCodes           = "serviceCodes"
	varUnitTypes              = "unitTypes"
	varEligibilityOutcome     = "eligibilityOutcome"
	varEligible               = "eligible"
)

// payloadDocumentType and payloadDocumentTypes are the two shapes a REQUIRE_DOCUMENT
// action may name its documents in: one code, or a list of them.
const (
	payloadDocumentType  = "documentTypeCode"
	payloadDocumentTypes = "documentTypeCodes"
)

// snapshotVersion labels the frozen document, so a reader years from now knows which shape
// it is looking at.
const snapshotVersion = 1

// Submit freezes the draft version and runs the gate. Everything below happens in one
// transaction: the version, the eligibility evaluation, the rule evaluation, the status the
// request lands in and the two status events are one atomic fact, because a request that
// was frozen but not decided, or decided against an evaluation that was rolled back, is a
// request nobody can explain afterwards.
//
// Nothing here touches benefit.entitlement_ledger. The eligibility resolver is the pure one
// of WP-I2-04 and the accounts it reads are the ones that are already open; the service's
// own Check opens them lazily, and opening one posts a GRANT movement.
func (s *Service) Submit(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	comment *string, expected int64,
) (RequestView, error) {
	if err := domain.ValidateComment(comment); err != nil {
		return RequestView{}, err
	}

	var out RequestView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockRequest(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if _, ok := domain.Target(domain.CommandSubmit, current.Status); !ok {
			return ErrTransitionInvalid
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		version, err := s.repo.GetDraftVersion(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		items, err := s.repo.ListItems(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		definitions, err := s.definitionsOf(ctx, tx, rc.TenantID, items)
		if err != nil {
			return err
		}
		if err := validateForSubmit(current, items); err != nil {
			return err
		}

		decision, err := s.runGate(ctx, tx, rc, current, items, definitions)
		if err != nil {
			return err
		}

		now := s.now().UTC()
		snapshot, err := buildSnapshot(current, items, decision, now)
		if err != nil {
			return err
		}
		if err := s.repo.FreezeVersion(ctx, tx, rc.TenantID, version.ID, FreezeRow{
			Snapshot: snapshot, SubmittedAt: now, ActorID: actorPtr(rc.Principal.ActorID),
		}); err != nil {
			return err
		}
		if err := s.repo.MarkSubmitted(ctx, tx, rc.TenantID, id, SubmitRow{
			Status: decision.Status, SubmittedAt: now,
			EligibilityEvaluationID: decision.EligibilityEvaluationID,
			RuleEvaluationID:        decision.RuleEvaluationID,
			RequiredDocumentTypes:   decision.RequiredDocumentTypes,
			ReviewComment:           trimmedPtr(comment),
			ActorID:                 actorPtr(rc.Principal.ActorID),
		}, expected); err != nil {
			return err
		}

		// Two events, because two things happened: somebody submitted, and then the gate
		// decided. Folding them into one would lose the moment the request was handed over.
		if err := s.transition(ctx, tx, rc, id, domain.StatusDraft, domain.StatusSubmitted,
			domain.CommandSubmit, "", trimmedPtr(comment),
			map[string]any{"version_no": version.VersionNo}); err != nil {
			return err
		}
		if err := s.transition(ctx, tx, rc, id, domain.StatusSubmitted, decision.Status,
			domain.CommandGate, decision.ReasonCode, nil, decision.metadata(version.VersionNo)); err != nil {
			return err
		}
		// The gate decided, so somebody has to be told what it decided. A request the
		// gate approved outright is a decision like any other; one waiting for a document
		// is the provider being asked for something.
		switch decision.Status {
		case domain.StatusPendingDocument:
			if err := s.notifyPendingDocument(ctx, tx, rc, current); err != nil {
				return err
			}
		case domain.StatusApproved:
			if err := s.notifyDecided(ctx, tx, rc, current, decision.Status); err != nil {
				return err
			}
		}
		out, err = s.reload(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	return out, err
}

// validateForSubmit is the first step of the gate: a version nobody could act on never
// reaches the resolver.
func validateForSubmit(request RequestRecord, items []ItemRecord) error {
	ve := &domain.ValidationError{}
	if len(items) == 0 {
		ve.Add("items", "REQUIRED", "gönderim için en az bir kalem gerekli")
	}
	if request.ServiceDate.IsZero() {
		ve.Add("serviceDate", "REQUIRED", "hizmet tarihi zorunlu")
	}
	if domain.Contains(domain.RequiresProviderTypes, request.RequestType) &&
		request.ProviderOrganizationID == nil {
		ve.Add("providerOrganizationId", "REQUIRED", "bu talep türü için sağlayıcı zorunlu")
	}
	return ve.OrNil()
}

// gateDecision is everything the gate produced: where the request lands and what decided it.
type gateDecision struct {
	Status                  string
	ReasonCode              string
	RequiredDocumentTypes   []string
	EligibilityEvaluationID *uuid.UUID
	RuleEvaluationID        *uuid.UUID
	EligibilityOutcome      string
	Eligibility             eligibility.Result
	RuleTrace               []RuleEvaluationResultRow
	RuleVersionIDs          []uuid.UUID
}

func (d gateDecision) metadata(versionNo int) map[string]any {
	out := map[string]any{
		"version_no": versionNo, "eligibility_outcome": d.EligibilityOutcome,
		"rule_set_version_count": len(d.RuleVersionIDs),
	}
	if len(d.RequiredDocumentTypes) > 0 {
		out["required_document_types"] = len(d.RequiredDocumentTypes)
	}
	return out
}

// runGate is section 2.3 of the work package in order: eligibility, then the rules, then
// the plan's own answer to "does a person still have to look at this".
func (s *Service) runGate(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	request RequestRecord, items []ItemRecord, definitions map[uuid.UUID]ServiceDefinitionRecord,
) (gateDecision, error) {
	day := domain.DateOnly(request.ServiceDate)
	out := gateDecision{RuleVersionIDs: []uuid.UUID{}}

	result, evaluationID, err := s.resolveEligibility(ctx, tx, rc, request, items, definitions, day)
	if err != nil {
		return gateDecision{}, err
	}
	out.Eligibility, out.EligibilityOutcome, out.EligibilityEvaluationID = result, result.Outcome, &evaluationID

	// A person who may not use the benefit at all is told so now. Running the document and
	// preauthorization rules on top would ask for paperwork nobody needs to produce.
	if result.Outcome == eligibility.OutcomeIneligible || result.Outcome == eligibility.OutcomeMissingData {
		out.Status = domain.StatusEligibilityFailed
		out.ReasonCode = firstExplanationCode(result)
		return out, nil
	}

	documents, review, err := s.runRules(ctx, tx, rc, request, items, definitions, result, day, &out)
	if err != nil {
		return gateDecision{}, err
	}

	switch {
	case len(documents) > 0:
		// A missing document blocks before a review does: nobody can review what has not
		// been produced yet, and asking for both at once tells a member two things to do
		// when only one of them is possible.
		out.Status = domain.StatusPendingDocument
		out.RequiredDocumentTypes = documents
		out.ReasonCode = "DOCUMENT_REQUIRED"
		return out, nil
	case review:
		out.Status = domain.StatusPendingReview
		out.ReasonCode = "RULE_REVIEW_REQUIRED"
		return out, nil
	case result.Outcome != eligibility.OutcomeEligible:
		// PARTIALLY_ELIGIBLE and REVIEW_REQUIRED both mean something could not be settled
		// by arithmetic, which is exactly what a reviewer is for.
		out.Status = domain.StatusPendingReview
		out.ReasonCode = "ELIGIBILITY_REVIEW_REQUIRED"
		return out, nil
	}

	// Nothing objected. Whether that is an approval or still a review is the program's
	// setting, not a rule hard-coded here.
	required, err := s.repo.ReviewRequired(ctx, tx, rc.TenantID, request.ProgramID)
	if err != nil {
		return gateDecision{}, err
	}
	// An empty non-nil slice says "the rules were asked and required nothing", which is a
	// different statement from the NULL a request that has never been submitted carries.
	out.RequiredDocumentTypes = []string{}
	if required {
		out.Status, out.ReasonCode = domain.StatusPendingReview, "PROGRAM_REVIEW_REQUIRED"
		return out, nil
	}
	out.Status, out.ReasonCode = domain.StatusApproved, "AUTO_APPROVED"
	return out, nil
}

// resolveEligibility runs the pure resolver over data this transaction loaded and stores
// the evaluation, so the answer the gate was given survives the balances moving on.
func (s *Service) resolveEligibility(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	request RequestRecord, items []ItemRecord, definitions map[uuid.UUID]ServiceDefinitionRecord,
	day time.Time,
) (eligibility.Result, uuid.UUID, error) {
	program := request.ProgramID
	loaded, err := s.repo.LoadEligibility(ctx, tx, rc.TenantID, request.PersonID, &program, day)
	if err != nil {
		return eligibility.Result{}, uuid.Nil, err
	}
	input := eligibility.Input{
		ServiceDate: day, ProgramID: program, Person: loaded.Person,
		Memberships: loaded.Memberships, Enrollments: loaded.Enrollments,
		PlanVersion: loaded.PlanVersion, Accounts: loaded.Accounts,
		Mappings: loaded.Mappings,
		Items:    make([]eligibility.Item, 0, len(items)),
	}
	for i, item := range items {
		quantity, err := benefitdomain.ParseQuantity(item.RequestedQuantity)
		if err != nil {
			return eligibility.Result{}, uuid.Nil, fmt.Errorf(
				"servicerequest: line %d quantity: %w", item.LineNo, err)
		}
		input.Items = append(input.Items, eligibility.Item{
			Index: i, ServiceDefinitionID: item.ServiceDefinitionID,
			EntitlementCode: entitlementCodeOf(item, definitions), Quantity: quantity,
		})
	}
	result := eligibility.Resolve(input)

	id, err := uuid.NewV7()
	if err != nil {
		return eligibility.Result{}, uuid.Nil, fmt.Errorf("servicerequest: evaluation id: %w", err)
	}
	now := s.now().UTC()
	requestSnapshot, err := eligibilityRequestSnapshot(request, items, definitions, day)
	if err != nil {
		return eligibility.Result{}, uuid.Nil, err
	}
	resultSnapshot, err := eligibilityResultSnapshot(id, now, result)
	if err != nil {
		return eligibility.Result{}, uuid.Nil, err
	}
	row := NewEligibilityEvaluationRow{
		ID: id, PersonID: request.PersonID, ProgramID: &program,
		PlanVersionID: uuidOrNil(result.PlanVersionID), ProviderOrgID: request.ProviderOrganizationID,
		ServiceDate: day, Outcome: result.Outcome, RequestHash: hashOf(requestSnapshot),
		RequestSnapshot: requestSnapshot, ResultSnapshot: resultSnapshot,
		EvaluatedAt: now, EvaluatedBy: actorPtr(rc.Principal.ActorID),
	}
	// The resolver names the enrollment it chose; the request names the one it was raised
	// under. They agree in every ordinary case, and where they do not, what actually
	// decided the outcome is the one stored.
	if id := uuidOrNil(result.EnrollmentID); id != nil {
		row.EnrollmentID = id
	}
	if err := s.repo.CreateEligibilityEvaluation(ctx, tx, rc.TenantID, row); err != nil {
		return eligibility.Result{}, uuid.Nil, err
	}
	return result, id, nil
}

// runRules evaluates every published DOCUMENT and PREAUTH version covering the service date
// and stores one evaluation carrying the whole trace. It reports the document types that
// were asked for and whether anything asked for a person.
func (s *Service) runRules(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	request RequestRecord, items []ItemRecord, definitions map[uuid.UUID]ServiceDefinitionRecord,
	result eligibility.Result, day time.Time, out *gateDecision,
) (documents []string, review bool, err error) {
	versions, err := s.repo.ListRuleVersions(ctx, tx, rc.TenantID, gatePurposes, day)
	if err != nil {
		return nil, false, err
	}
	if len(versions) == 0 {
		return nil, false, nil
	}

	input := ruleInput(request, items, definitions, result, day)
	trace := make([]RuleEvaluationResultRow, 0, len(versions))
	seen := map[string]bool{}
	decidingVersion := versions[0].ID
	sequence := 0
	total := time.Duration(0)

	for _, version := range versions {
		out.RuleVersionIDs = append(out.RuleVersionIDs, version.ID)
		program, err := s.programOf(version)
		if err != nil {
			// A published version passed the publish gate, which compiles it. A failure
			// here means the stored rules and the engine have genuinely diverged, and
			// deciding a request against rules that could not be run would be worse than
			// refusing to decide it.
			return nil, false, fmt.Errorf("servicerequest: compile rule set %s v%d: %w",
				version.RuleSetCode, version.VersionNo, err)
		}
		evaluation := program.Evaluate(ctx, input)
		total += evaluation.Duration
		for _, line := range evaluation.Results {
			sequence++
			trace = append(trace, traceRow(sequence, line))
			if !line.Matched || line.ActionType == "" {
				continue
			}
			switch line.ActionType {
			case engine.ActionApprove, engine.ActionWarn:
				// An approval or a warning from a rule does not decide anything here: the
				// gate's own answer to "nothing objected" is below, and a warning is a note
				// on the trace, not a reason to stop.
				continue
			case engine.ActionRequireDocument:
				for _, code := range documentTypesOf(line.ActionPayload) {
					if seen[code] {
						continue
					}
					seen[code] = true
					documents = append(documents, code)
				}
				if len(documents) > 0 && decidingVersion == versions[0].ID {
					decidingVersion = version.ID
				}
			default:
				// REQUIRE_PREAUTH, the two review actions, REJECT, PARTIAL_APPROVE and the
				// entitlement and price actions all mean the same thing to this gate: a
				// person decides. A rule never refuses a request outright, because a
				// refusal is a decision somebody has to be answerable for.
				if !review {
					decidingVersion = version.ID
				}
				review = true
			}
		}
	}

	outcome := string(engine.OutcomeApproved)
	if review || len(documents) > 0 {
		outcome = string(engine.OutcomeReviewRequired)
	}
	snapshot, err := json.Marshal(input)
	if err != nil {
		return nil, false, fmt.Errorf("servicerequest: encode rule input: %w", err)
	}
	evaluationID, err := s.repo.CreateRuleEvaluation(ctx, tx, rc.TenantID, NewRuleEvaluationRow{
		SubjectID: request.ID, RuleSetVersionID: decidingVersion, InputHash: hashOf(snapshot),
		InputSnapshot: snapshot, Outcome: outcome, DurationMs: int(total.Milliseconds()),
		EvaluatedBy: actorPtr(rc.Principal.ActorID),
	}, trace)
	if err != nil {
		return nil, false, err
	}
	out.RuleEvaluationID = &evaluationID
	sort.Strings(documents)
	return documents, review, nil
}

// programOf compiles a version, reusing the shared cache. Only published versions reach
// here and a published version never changes, so a cached program can never be stale.
func (s *Service) programOf(v RuleVersion) (*engine.Program, error) {
	if s.programs != nil {
		if p, ok := s.programs.Get(v.ID); ok {
			return p, nil
		}
	}
	p, err := engine.Compile(v.ID, v.InputSchema, v.Rules)
	if err != nil {
		return nil, err
	}
	if s.programs != nil {
		s.programs.Put(v.ID, p)
	}
	return p, nil
}

// ruleInput builds the one document every DOCUMENT and PREAUTH version of this submit sees.
func ruleInput(request RequestRecord, items []ItemRecord,
	definitions map[uuid.UUID]ServiceDefinitionRecord, result eligibility.Result, day time.Time,
) map[string]any {
	totalQuantity := benefitdomain.ZeroQuantity()
	totalAmount := benefitdomain.ZeroQuantity()
	currency := ""
	codes := make([]any, 0, len(items))
	units := make([]any, 0, len(items))
	for _, item := range items {
		if q, err := benefitdomain.ParseQuantity(item.RequestedQuantity); err == nil {
			totalQuantity = totalQuantity.Add(q)
		}
		if item.RequestedAmount != nil {
			if a, err := benefitdomain.ParseQuantity(*item.RequestedAmount); err == nil {
				totalAmount = totalAmount.Add(a)
			}
		}
		if currency == "" && item.CurrencyCode != nil {
			currency = *item.CurrencyCode
		}
		codes = append(codes, definitionCode(item.ServiceDefinitionID, definitions))
		units = append(units, item.UnitType)
	}
	doc := map[string]any{
		varServiceDate:            day,
		varRequestType:            request.RequestType,
		varChannel:                request.Channel,
		varPersonID:               request.PersonID.String(),
		varProgramID:              request.ProgramID.String(),
		varEnrollmentID:           request.EnrollmentID.String(),
		varProviderOrganizationID: "",
		varItemCount:              int64(len(items)),
		varTotalQuantity:          totalQuantity.String(),
		varTotalAmount:            totalAmount.String(),
		varCurrencyCode:           currency,
		varServiceCodes:           codes,
		varUnitTypes:              units,
		varEligibilityOutcome:     result.Outcome,
		varEligible:               result.Eligible,
	}
	if request.ProviderOrganizationID != nil {
		doc[varProviderOrganizationID] = request.ProviderOrganizationID.String()
	}
	return doc
}

// documentTypesOf reads the document codes off a REQUIRE_DOCUMENT payload. A rule that
// asks for a document without naming one is answered with a code that says exactly that,
// rather than with an empty list that would let the request through.
func documentTypesOf(payload map[string]any) []string {
	out := []string{}
	if code, ok := payload[payloadDocumentType].(string); ok && code != "" {
		out = append(out, code)
	}
	if list, ok := payload[payloadDocumentTypes].([]any); ok {
		for _, raw := range list {
			if code, ok := raw.(string); ok && code != "" {
				out = append(out, code)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, "UNSPECIFIED")
	}
	return out
}

func traceRow(sequence int, line engine.Result) RuleEvaluationResultRow {
	row := RuleEvaluationResultRow{
		Sequence: sequence, RuleCode: line.RuleCode, Matched: line.Matched,
		ExplanationCode: line.ExplanationCode, Severity: string(line.Severity),
	}
	if line.RuleID != uuid.Nil {
		id := line.RuleID
		row.RuleID = &id
	}
	if line.ActionType != "" {
		action := line.ActionType
		row.ActionType = &action
		row.ActionPayload = line.ActionPayload
	}
	return row
}

// definitionsOf reads the catalog rows behind the lines, which the gate needs for the
// entitlement hint and for the service codes the rules see.
func (s *Service) definitionsOf(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	items []ItemRecord,
) (map[uuid.UUID]ServiceDefinitionRecord, error) {
	ids := make([]uuid.UUID, 0, len(items))
	seen := make(map[uuid.UUID]bool, len(items))
	for _, item := range items {
		if seen[item.ServiceDefinitionID] {
			continue
		}
		seen[item.ServiceDefinitionID] = true
		ids = append(ids, item.ServiceDefinitionID)
	}
	out := map[uuid.UUID]ServiceDefinitionRecord{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.repo.ListServiceDefinitions(ctx, tx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out, nil
}

// entitlementCodeOf is the fallback the eligibility resolver maps a line onto a balance
// with when the plan version has no mapping for the service (WP-I5-05 added the table;
// this is what answers for the versions nobody has mapped yet). The convention is that the
// service definition's own code names the entitlement: a line whose code matches an open
// account is judged against that balance, and a line whose code matches nothing is left
// REVIEW_REQUIRED with SERVICE_MAPPING_PENDING, which is exactly what the resolver was
// built to say. Nothing is guessed and nothing is silently treated as covered.
func entitlementCodeOf(item ItemRecord, definitions map[uuid.UUID]ServiceDefinitionRecord) string {
	return definitionCode(item.ServiceDefinitionID, definitions)
}

func definitionCode(id uuid.UUID, definitions map[uuid.UUID]ServiceDefinitionRecord) string {
	if d, ok := definitions[id]; ok {
		return d.Code
	}
	return ""
}

func firstExplanationCode(result eligibility.Result) string {
	for _, e := range result.Explanations {
		if e.Severity == eligibility.SeverityError {
			return e.Code
		}
	}
	if len(result.Explanations) > 0 {
		return result.Explanations[0].Code
	}
	return "ELIGIBILITY_FAILED"
}

func uuidOrNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func hashOf(payload []byte) []byte {
	sum := sha256.Sum256(payload)
	return sum[:]
}
