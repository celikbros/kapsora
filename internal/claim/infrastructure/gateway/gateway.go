// Package claimgw is where the claim meets the five modules it cannot do without: the
// pricing ladder (WP-I3-05), the rule engine (WP-I3-04), the authorization hold (WP-I4-02),
// the approval policy (WP-I4-03) and the work queue it is raised into.
//
// It exists so that none of those modules knows a claim exists and the claim module holds no
// copy of what they do. The ports are declared in claim/application in this package's own
// vocabulary — a priced line, a rule action, a consume, a policy band — and the adapters
// below are the only code in the repository that speaks both languages.
//
// Nothing here decides anything. Every refusal comes from the module being called: the price
// selection is the contract module's, the no-double-spend rule is the ledger's, the closed
// action list is the rule author's screen. An adapter that added a rule would be a rule
// nobody reading either module could find.
package claimgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	pricingapp "github.com/celikbros/kapsora/internal/pricing/application"
	rulesdomain "github.com/celikbros/kapsora/internal/rules/domain"
	"github.com/celikbros/kapsora/internal/rules/engine"
	workflowapp "github.com/celikbros/kapsora/internal/workflow/application"
	workflowdomain "github.com/celikbros/kapsora/internal/workflow/domain"
)

// ---------------------------------------------------------------------------
// Pricing
// ---------------------------------------------------------------------------

// Pricing is WP-I3-05's ladder seen from the claim: price these lines, for this provider, on
// this day, and store nothing.
type Pricing struct{ svc *pricingapp.Service }

// NewPricing wraps the pricing module.
func NewPricing(svc *pricingapp.Service) *Pricing { return &Pricing{svc: svc} }

var _ claimapp.PricingPort = (*Pricing)(nil)

// PriceLines implements claimapp.PricingPort. It runs in the claim's own transaction, which
// is what makes a claim priced against balances that then moved a state no reader observes.
func (p *Pricing) PriceLines(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in claimapp.PricingRequest,
) (claimapp.PricingResult, error) {
	items := make([]pricingapp.QuoteItemInput, 0, len(in.Items))
	// The ladder takes the entitlement codes positionally, through the same free-form context
	// object an eligibility check sends it. A counter that asks "is this covered" and then
	// "what does it cost" sends one object to both, and the claim speaks the same vocabulary
	// rather than inventing a second one.
	codes := make([]any, 0, len(in.Items))
	for _, item := range in.Items {
		definition := item.ServiceDefinitionID
		requested := item.RequestedAmount
		items = append(items, pricingapp.QuoteItemInput{
			ServiceDefinitionID: &definition, Quantity: item.Quantity,
			RequestedAmount: &requested,
		})
		if item.EntitlementCode == "" {
			codes = append(codes, nil)
			continue
		}
		codes = append(codes, item.EntitlementCode)
	}
	program := in.ProgramID
	priced, err := p.svc.PriceLines(ctx, tx, tenantID, pricingapp.LinePricingInput{
		PersonID: in.PersonID, ProgramID: &program,
		ProviderProfileID: in.ProviderProfileID, ServiceDate: in.ServiceDate, Items: items,
		Context: map[string]any{"entitlementCodes": codes},
	})
	if err != nil {
		return claimapp.PricingResult{}, fmt.Errorf("claim: price lines: %w", err)
	}
	return claimapp.PricingResult{
		CurrencyCode: priced.CurrencyCode, Lines: priced.Result.Items,
	}, nil
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

// PurposeAdjudication is the rule set purpose a claim is decided by.
//
// The work package calls it the CLAIM rule set; the schema's closed list (migration 000022)
// has no such purpose and its own comment says an ADJUDICATION rule belongs to the claim. This
// constant is where the two names meet, and it is a constant rather than a literal so the
// choice is visible in one place rather than implied by a string in a query.
const PurposeAdjudication = "ADJUDICATION"

// Rules evaluates the tenant's published ADJUDICATION versions over one claim line, inside the
// claim's transaction.
//
// It compiles the versions on every call rather than caching them. A claim submit is a rare,
// heavyweight command — it prices, it consumes a hold, it writes a snapshot — and a cache that
// had to be invalidated correctly is a cache that could serve a rule an approver has retired.
type Rules struct{ logger *slog.Logger }

// NewRules returns the rules port.
func NewRules(logger *slog.Logger) *Rules {
	if logger == nil {
		logger = slog.Default()
	}
	return &Rules{logger: logger}
}

var _ claimapp.RulesPort = (*Rules)(nil)

// EvaluateClaimLine implements claimapp.RulesPort.
//
// A version that will not compile is an error rather than a shrug: a published version passed
// the publish gate, which compiles it, so a failure here means the stored rules and the engine
// have genuinely diverged — and deciding a claim against rules that could not be run would be
// worse than refusing to decide it.
func (r *Rules) EvaluateClaimLine(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	subjectID uuid.UUID, serviceDate time.Time, input map[string]any,
) (claimapp.RuleOutcome, error) {
	q := sqlcgen.New(tx)
	versions, err := q.ListPublishedRuleSetVersionsForPurposes(ctx,
		sqlcgen.ListPublishedRuleSetVersionsForPurposesParams{
			TenantID: tenantID, Purposes: []string{PurposeAdjudication},
			ServiceDate: dateParam(serviceDate),
		})
	if err != nil {
		return claimapp.RuleOutcome{}, fmt.Errorf("claim: list adjudication rule versions: %w", err)
	}
	out := claimapp.RuleOutcome{EvaluatedVersions: len(versions), Actions: []claimapp.RuleAction{}}
	for _, version := range versions {
		schema := map[string]string{}
		if len(version.InputSchema) > 0 {
			if err := json.Unmarshal(version.InputSchema, &schema); err != nil {
				return claimapp.RuleOutcome{}, fmt.Errorf("claim: decode rule input schema: %w", err)
			}
		}
		rows, err := q.ListRules(ctx, sqlcgen.ListRulesParams{
			TenantID: tenantID, RuleSetVersionID: version.ID,
		})
		if err != nil {
			return claimapp.RuleOutcome{}, fmt.Errorf("claim: list rules: %w", err)
		}
		rules := make([]engine.Rule, 0, len(rows))
		for _, row := range rows {
			actions, err := decodeActions(row.Actions)
			if err != nil {
				return claimapp.RuleOutcome{}, err
			}
			params := map[string]any{}
			if len(row.ExplanationParams) > 0 {
				if err := json.Unmarshal(row.ExplanationParams, &params); err != nil {
					return claimapp.RuleOutcome{}, fmt.Errorf("claim: decode explanation params: %w", err)
				}
			}
			rules = append(rules, engine.Rule{
				ID: row.ID, Code: row.Code, Priority: int(row.Priority), Condition: row.Condition,
				Actions: rulesdomain.EngineActions(actions), ExplanationCode: row.ExplanationCode,
				ExplanationParams: params, StopOnMatch: row.StopOnMatch, Active: row.Active,
			})
		}
		program, err := engine.Compile(version.ID, schema, rules)
		if err != nil {
			return claimapp.RuleOutcome{}, fmt.Errorf("claim: compile rule set %s v%d: %w",
				version.RuleSetCode, version.VersionNo, err)
		}
		evaluation := program.Evaluate(ctx, coerce(schema, input))
		for _, line := range evaluation.Results {
			if !line.Matched || line.ActionType == "" {
				continue
			}
			out.Actions = append(out.Actions, actionOf(line))
		}
	}
	return out, nil
}

// actionOf reads the part of an action's payload the claim understands. An unknown field is
// ignored rather than refused: the payload was validated on write against the closed spec, and
// a consumer that failed on a field it did not know would break the day one was added.
func actionOf(line engine.Result) claimapp.RuleAction {
	out := claimapp.RuleAction{Type: line.ActionType, RuleCode: line.RuleCode}
	if code, ok := line.ActionPayload["reasonCode"].(string); ok {
		out.ReasonCode = code
	}
	if percent, ok := line.ActionPayload["percent"].(string); ok {
		out.Percent = percent
	}
	if amount, ok := line.ActionPayload["amount"].(string); ok {
		out.Amount = amount
	}
	return out
}

// decodeActions reads the stored action list of one rule.
func decodeActions(raw []byte) ([]rulesdomain.ActionInput, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var actions []rulesdomain.ActionInput
	if err := json.Unmarshal(raw, &actions); err != nil {
		return nil, fmt.Errorf("claim: decode rule actions: %w", err)
	}
	return actions, nil
}

// coerce drops the variables a version did not declare, so a rule set that knows nothing about
// `pricingOutcome` is not handed one. A version declaring a variable the claim does not supply
// still compiles; the missing variable then fails that one rule at run time with a RULE_ERROR
// line naming it, which is more use to the author than a blanket refusal from here.
func coerce(schema map[string]string, input map[string]any) map[string]any {
	if len(schema) == 0 {
		return input
	}
	out := make(map[string]any, len(schema))
	for name := range schema {
		if value, ok := input[name]; ok {
			out[name] = value
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Authorizations
// ---------------------------------------------------------------------------

// Authorizations is WP-I4-02 seen from the claim: draw a line's quantity out of the hold, and
// give back what a refused or withdrawn claim was holding.
type Authorizations struct{ svc *authorizationapp.Service }

// NewAuthorizations wraps the authorization module.
func NewAuthorizations(svc *authorizationapp.Service) *Authorizations {
	return &Authorizations{svc: svc}
}

var _ claimapp.AuthorizationPort = (*Authorizations)(nil)

// Consume implements claimapp.AuthorizationPort. The over-consumption answer is passed through
// unchanged, because the whole point of it is that nothing was written and the claim decides
// what to do about that.
func (a *Authorizations) Consume(ctx context.Context, tx pgx.Tx, in claimapp.ConsumeRequest,
) (claimapp.ConsumeAnswer, error) {
	answer, err := a.svc.Consume(ctx, tx, authorizationapp.ConsumeInput{
		TenantID: in.TenantID, ActorID: in.ActorID, AuthorizationID: in.AuthorizationID,
		ServiceDefinitionID: in.ServiceDefinitionID, Quantity: in.Quantity,
		Key: in.Key, ReasonCode: in.ReasonCode,
	})
	if err != nil {
		return claimapp.ConsumeAnswer{}, fmt.Errorf("claim: consume authorization: %w", err)
	}
	return claimapp.ConsumeAnswer{
		Matched: answer.Matched, Remaining: answer.Remaining, Consumed: answer.Consumed,
		OverConsumed: answer.OverConsumed,
	}, nil
}

// ReleaseUnused implements claimapp.AuthorizationPort.
func (a *Authorizations) ReleaseUnused(ctx context.Context, tx pgx.Tx, in claimapp.ReleaseRequest,
) (benefitdomain.Quantity, error) {
	released, err := a.svc.ReleaseUnused(ctx, tx, authorizationapp.ReleaseUnusedInput{
		TenantID: in.TenantID, ActorID: in.ActorID, AuthorizationID: in.AuthorizationID,
		Quantity: in.Quantity, ReasonCode: in.ReasonCode,
	})
	if err != nil {
		return benefitdomain.Quantity{}, fmt.Errorf("claim: release authorization: %w", err)
	}
	return released, nil
}

// ---------------------------------------------------------------------------
// Approval policy
// ---------------------------------------------------------------------------

// Policies is WP-I4-03 section 2.4's approval policy seen from the claim.
type Policies struct{ svc *workflowapp.Service }

// NewPolicies wraps the workflow module.
func NewPolicies(svc *workflowapp.Service) *Policies { return &Policies{svc: svc} }

var _ claimapp.PolicyPort = (*Policies)(nil)

// Resolve implements claimapp.PolicyPort.
//
// "No band covers this amount" is not a refusal and is not an error: a tenant that has
// configured no policy for `claim.approve` has said nothing about it, and the pipeline
// auto-adjudicates. Turning silence into a refusal would mean a tenant that never opened the
// approval policy screen could settle nothing.
func (p *Policies) Resolve(ctx context.Context, rc identity.RequestContext,
	in claimapp.PolicyLookup,
) (claimapp.PolicyAnswer, error) {
	record, err := p.svc.ResolvePolicy(ctx, rc, workflowapp.PolicyLookup{
		ActionCode: in.ActionCode, Amount: in.Amount, AsOf: in.AsOf,
	})
	if errors.Is(err, workflowapp.ErrPolicyNotFound) {
		return claimapp.PolicyAnswer{}, nil
	}
	if err != nil {
		return claimapp.PolicyAnswer{}, fmt.Errorf("claim: resolve approval policy: %w", err)
	}
	return claimapp.PolicyAnswer{
		Found: true, RequiredRoleCodes: record.RequiredRoleCodes,
		RequiredApproverCount: record.RequiredApproverCount,
	}, nil
}

// ---------------------------------------------------------------------------
// Work items
// ---------------------------------------------------------------------------

// WorkItems raises work into a queue named by its code, inside the caller's transaction. It is
// WP-I4-03's work item written from this side of the boundary rather than a call into the
// worklist service, because that service opens a transaction of its own and a work item that
// committed while the submit it belongs to rolled back would be work nobody can explain.
type WorkItems struct{ logger *slog.Logger }

// NewWorkItems returns the work item port.
func NewWorkItems(logger *slog.Logger) *WorkItems {
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkItems{logger: logger}
}

var _ claimapp.WorkItemPort = (*WorkItems)(nil)

// Raise implements claimapp.WorkItemPort.
//
// A tenant with no queue under that code, or one whose queue has been switched off, raises
// nothing and the command continues. The claim has been submitted either way, refusing the
// submission would not make anybody watch the queue, and a provider told "your claim cannot be
// submitted because the payer has not configured a work queue" is a provider told about
// somebody else's configuration.
func (w *WorkItems) Raise(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in claimapp.RaiseWorkItem,
) error {
	queue, err := sqlcgen.New(tx).GetWorkQueueByCode(ctx, sqlcgen.GetWorkQueueByCodeParams{
		TenantID: tenantID, Code: in.QueueCode,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		w.logger.Warn("claim: no work queue to raise the claim into",
			"tenant_id", tenantID, "queue_code", in.QueueCode)
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim: find work queue %s: %w", in.QueueCode, err)
	}
	if !queue.Active {
		w.logger.Warn("claim: the review queue is not active",
			"tenant_id", tenantID, "queue_code", in.QueueCode)
		return nil
	}
	var actor uuid.NullUUID
	if in.ActorID != nil {
		actor = uuid.NullUUID{UUID: *in.ActorID, Valid: true}
	}
	if _, err := sqlcgen.New(tx).CreateWorkItem(ctx, sqlcgen.CreateWorkItemParams{
		TenantID: tenantID, QueueID: queue.ID, AggregateType: in.AggregateType,
		AggregateID: in.AggregateID, Title: title(in.Title),
		Priority: workflowdomain.DefaultPriority, ActorID: actor,
	}); err != nil {
		return fmt.Errorf("claim: raise work item: %w", err)
	}
	return nil
}

// maxWorkItemTitle mirrors ck_work_item_title. A title is a reference and two words, so this
// is never reached; cutting rather than failing is still the right answer, because a claim that
// could not be submitted over the length of a queue label would be a bad trade.
const maxWorkItemTitle = 200

func title(s string) string {
	runes := []rune(s)
	if len(runes) <= maxWorkItemTitle {
		return s
	}
	return string(runes[:maxWorkItemTitle])
}

// dateParam renders a service date for a date-typed query parameter.
func dateParam(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}
