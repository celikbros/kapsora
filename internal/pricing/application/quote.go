package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/pricing"
)

// DefaultCurrency is what a quote is denominated in when no line found a contracted
// price to take a currency from. The column is NOT NULL and a quote with no winner
// carries no money anyway, so this only ever labels a row of zeroes.
const DefaultCurrency = "TRY"

// QuoteItemInput is one requested line. Exactly one of the two ids is set.
type QuoteItemInput struct {
	ServiceDefinitionID *uuid.UUID
	PackageDefinitionID *uuid.UUID
	Quantity            benefitdomain.Quantity
	// RequestedAmount is what the provider asked for, if anything. A PERCENT_OF_LIST
	// price is a percentage of it.
	RequestedAmount *benefitdomain.Quantity
}

// QuoteInput is one question: what does this cost, for this person, here, on this day.
type QuoteInput struct {
	PersonID          uuid.UUID
	ProgramID         *uuid.UUID
	ProviderProfileID uuid.UUID
	LocationID        *uuid.UUID
	ServiceDate       time.Time
	// EligibilityEvaluationID names a check the caller already made, recorded on the
	// quote so the two can be read together. The quote never makes one itself: opening an
	// entitlement account posts a GRANT movement, and the ledger has to stay untouched.
	EligibilityEvaluationID *uuid.UUID
	Items                   []QuoteItemInput
	// Context is the request's free-form context object; only the recognised hints are
	// read, stored or hashed.
	Context map[string]any
	// IdempotencyKey is the optional Idempotency-Key header. The quote table owns the key
	// itself (uq_price_quote_idempotency), so no middleware is involved: the same key with
	// the same question replays the stored quote, the same key with a different question
	// is refused.
	IdempotencyKey string
}

// CreateQuote prices a request and stores the answer.
func (s *Service) CreateQuote(ctx context.Context, rc identity.RequestContext, in QuoteInput) (QuoteView, error) {
	hints := parseContext(in.Context)
	if err := validateQuote(in, hints); err != nil {
		return QuoteView{}, err
	}
	hash, err := requestHash(in, hints)
	if err != nil {
		return QuoteView{}, err
	}
	classification := audit.ClassPersonal
	if hints.health() {
		classification = audit.ClassHealth
	}

	var out QuoteView
	err = s.withQuoteTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if in.IdempotencyKey != "" {
			replay, found, err := s.replay(ctx, tx, rc, in.IdempotencyKey, hash)
			if err != nil {
				return err
			}
			if found {
				out = replay
				return s.recordAccess(ctx, tx, rc, replay.PersonID, replay.ID,
					classification, replay.Outcome, audit.AccessView)
			}
		}
		if err := s.checkTargets(ctx, tx, rc, in); err != nil {
			return err
		}

		computed, err := s.price(ctx, tx, rc.TenantID, in, hints)
		if err != nil {
			return err
		}

		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("pricing: quote id: %w", err)
		}
		now := s.now().UTC()
		out = computed.view(id, in, now, now.Add(s.quoteTTL(ctx, tx, rc.TenantID)),
			actorPtr(rc.Principal.ActorID))
		if err := s.store(ctx, tx, rc, in, hints, computed, out, hash); err != nil {
			return err
		}
		return s.recordAccess(ctx, tx, rc, in.PersonID, id, classification, out.Outcome, audit.AccessView)
	})
	if err != nil {
		return QuoteView{}, err
	}
	return out, nil
}

// GetQuote reads one stored quote. A provider-scoped actor only sees the quotes made for
// its own provider; anything else is 404, so the endpoint cannot be used to find out what
// another provider was quoted.
func (s *Service) GetQuote(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (QuoteView, error) {
	var out QuoteView
	err := s.withQuoteTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, items, err := s.repo.GetQuote(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		provider, err := s.repo.GetProvider(ctx, tx, rc.TenantID, record.ProviderProfileID)
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, provider.TenantOrganizationID); err != nil {
			return ErrQuoteNotFound
		}
		out, err = s.storedView(record, items)
		if err != nil {
			return err
		}
		// The classification of a read is PERSONAL: the stored quote carries codes and
		// amounts, and whether the original question was clinical is not re-derivable
		// from it without keeping the domain hint, which the snapshot deliberately does.
		classification := audit.ClassPersonal
		if storedDomainIsHealth(record.RequestSnapshot) {
			classification = audit.ClassHealth
		}
		return s.recordAccess(ctx, tx, rc, record.PersonID, record.ID,
			classification, record.Outcome, audit.AccessView)
	})
	if err != nil {
		return QuoteView{}, err
	}
	return out, nil
}

// checkTargets refuses a request naming something this tenant, or this provider, does not
// have, before anything is priced.
func (s *Service) checkTargets(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, in QuoteInput) error {
	provider, err := s.repo.GetProvider(ctx, tx, rc.TenantID, in.ProviderProfileID)
	if err != nil {
		return err
	}
	if err := checkProviderScope(rc, provider.TenantOrganizationID); err != nil {
		return err
	}
	if in.LocationID != nil {
		ok, err := s.repo.LocationBelongsToProvider(ctx, tx, rc.TenantID, *in.LocationID, in.ProviderProfileID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrLocationNotFound
		}
	}
	if in.EligibilityEvaluationID != nil {
		ok, err := s.repo.EligibilityEvaluationExists(ctx, tx, rc.TenantID, *in.EligibilityEvaluationID, in.PersonID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrEvaluationNotFound
		}
	}
	return s.checkPackages(ctx, tx, rc.TenantID, in)
}

// checkPackages refuses a package line naming a bundle this tenant does not have. It is a
// single read for the whole request rather than one per line, because a request quoting
// twenty lines of the same package should not cost twenty round trips to find that out.
func (s *Service) checkPackages(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in QuoteInput) error {
	wanted := make([]uuid.UUID, 0, len(in.Items))
	seen := make(map[uuid.UUID]bool, len(in.Items))
	for _, item := range in.Items {
		if item.PackageDefinitionID == nil || seen[*item.PackageDefinitionID] {
			continue
		}
		seen[*item.PackageDefinitionID] = true
		wanted = append(wanted, *item.PackageDefinitionID)
	}
	if len(wanted) == 0 {
		return nil
	}
	found, err := s.repo.PackagesExist(ctx, tx, tenantID, wanted)
	if err != nil {
		return err
	}
	for i, item := range in.Items {
		if item.PackageDefinitionID == nil || found[*item.PackageDefinitionID] {
			continue
		}
		return fieldError(fmt.Sprintf("items[%d].packageDefinitionId", i), "NOT_FOUND",
			"paket tanımı bulunamadı")
	}
	return nil
}

// computation is the whole answer before it is given an id and a lifetime.
type computation struct {
	currency          string
	result            pricing.Result
	lines             []lineIdentity
	contractVersionID *uuid.UUID
	planVersionID     *uuid.UUID
	ruleSetVersionIDs []uuid.UUID
}

// lineIdentity is what a priced line points at, alongside the figures the calculation
// produced for it.
type lineIdentity struct {
	ServiceDefinitionID *uuid.UUID
	PackageDefinitionID *uuid.UUID
	PriceItemID         *uuid.UUID
	Quantity            benefitdomain.Quantity
}

// price runs the six steps of WP-I3-05 2.1 in order: eligibility, price selection,
// contract amount, rules, balance cap, split. The last four are the pure calculation in
// internal/pricing and are not repeated here; this function gathers what it needs.
//
// The calculation is run twice on purpose. The first pass carries no adjustments and
// exists only to produce each line's contract amount, which is what a PRICE rule condition
// compares against; the second pass is the answer, with the rule adjustments and any
// formula the rules resolved. Running it twice is how the rules see a real contract amount
// without a second, drifting copy of the arithmetic living in this package.
func (s *Service) price(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in QuoteInput, hints contextHints,
) (computation, error) {
	day := benefitdomain.DateOnly(in.ServiceDate)

	eligible, available, accounts, planVersionID, err := s.resolveEligibility(ctx, tx, tenantID, in, hints, day)
	if err != nil {
		return computation{}, err
	}

	items := make([]pricing.Item, 0, len(in.Items))
	lines := make([]lineIdentity, 0, len(in.Items))
	facts := make([]lineFacts, 0, len(in.Items))
	out := computation{currency: "", ruleSetVersionIDs: []uuid.UUID{}, planVersionID: planVersionID}

	for i, requested := range in.Items {
		winner, reason, err := s.selectPrice(ctx, tx, tenantID, in, requested, i, day)
		if err != nil {
			return computation{}, err
		}
		item := pricing.Item{
			LineNo: i + 1, Quantity: requested.Quantity,
			Available: available[i], AccountKey: accounts[i], Eligible: eligible[i],
		}
		if requested.RequestedAmount != nil {
			item.Requested = *requested.RequestedAmount
		}
		line := lineIdentity{
			ServiceDefinitionID: requested.ServiceDefinitionID,
			PackageDefinitionID: requested.PackageDefinitionID,
			Quantity:            requested.Quantity,
		}
		fact := lineFacts{
			LineNo: i + 1, ServiceDefinitionID: requested.ServiceDefinitionID,
			PackageDefinitionID: requested.PackageDefinitionID,
			EntitlementCode:     hints.codeFor(i), Quantity: requested.Quantity,
			Requested: item.Requested, Eligible: eligible[i],
		}
		if winner == nil {
			item.NoPriceReason = string(reason)
		} else {
			price, err := priceOf(winner.Detail)
			if err != nil {
				return computation{}, err
			}
			item.Price = &price
			line.PriceItemID = uuidPtr(winner.Candidate.PriceItemID)
			if out.currency == "" {
				out.currency = winner.Detail.CurrencyCode
			}
			if out.contractVersionID == nil {
				out.contractVersionID = uuidPtr(winner.Candidate.ContractVersionID)
			}
			if winner.Detail.FormulaKey != nil {
				fact.FormulaKey = *winner.Detail.FormulaKey
			}
		}
		items = append(items, item)
		lines = append(lines, line)
		facts = append(facts, fact)
	}
	if out.currency == "" {
		out.currency = DefaultCurrency
	}
	minorUnits := MinorUnits(out.currency)

	// Pass one: the contract amounts the rules are allowed to see.
	first := pricing.Calculate(items, minorUnits)

	programs, err := s.loadPricePrograms(ctx, tx, tenantID, day)
	if err != nil {
		return computation{}, err
	}
	for _, p := range programs {
		out.ruleSetVersionIDs = append(out.ruleSetVersionIDs, p.version.ID)
	}
	if len(programs) > 0 {
		for i := range items {
			facts[i].Contract = first.Items[i].Contract
			formulaWanted := items[i].Price != nil && items[i].Price.Method == pricing.MethodFormula
			adjustments, formula := s.lineAdjustments(ctx, programs, in, facts[i], formulaWanted)
			items[i].Adjustments = adjustments
			if formula != nil && items[i].Price != nil {
				items[i].Price.FormulaKnownAmount = formula
			}
		}
	}

	// Pass two: the answer.
	out.result = pricing.Calculate(items, minorUnits)
	out.lines = lines
	return out, nil
}

// resolveEligibility runs the pure resolver of WP-I2-04 over data this transaction
// loaded. It opens nothing: an entitlement account that was never opened simply has no
// balance here, and the line is priced with nothing available rather than with an account
// this quote created. That is the whole reason the eligibility service is not called —
// its own check opens accounts lazily, and opening one posts a GRANT ledger entry.
func (s *Service) resolveEligibility(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in QuoteInput, hints contextHints, day time.Time,
) (eligible []bool, available []benefitdomain.Quantity, accounts []string, planVersionID *uuid.UUID, err error) {
	loaded, err := s.repo.LoadEligibility(ctx, tx, tenantID, in.PersonID, in.ProgramID, day)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	resolverInput := eligibility.Input{
		ServiceDate: day, Person: loaded.Person, Memberships: loaded.Memberships,
		Enrollments: loaded.Enrollments, PlanVersion: loaded.PlanVersion,
		Accounts: loaded.Accounts, Items: make([]eligibility.Item, 0, len(in.Items)),
	}
	if in.ProgramID != nil {
		resolverInput.ProgramID = *in.ProgramID
	}
	for i, item := range in.Items {
		resolverInput.Items = append(resolverInput.Items, eligibility.Item{
			Index: i, EntitlementCode: hints.codeFor(i), Quantity: item.Quantity,
		})
	}
	result := eligibility.Resolve(resolverInput)

	eligible = make([]bool, len(in.Items))
	available = make([]benefitdomain.Quantity, len(in.Items))
	// The entitlement code names the account behind the balance. Two lines carrying the
	// same code draw on one account, and the calculation shares the balance between them.
	accounts = make([]string, len(in.Items))
	for _, item := range result.Items {
		if item.Index < 0 || item.Index >= len(in.Items) {
			continue
		}
		// Only an outright ELIGIBLE line lets the plan carry anything. A line nobody could
		// map to an entitlement is not "probably covered": nothing is known to be covered,
		// and the quote says so instead of implying a number.
		eligible[item.Index] = item.Outcome == eligibility.ItemEligible
		if item.AvailableQuantity != nil {
			available[item.Index] = *item.AvailableQuantity
			accounts[item.Index] = item.EntitlementCode
		}
	}
	if result.PlanVersionID != uuid.Nil {
		planVersionID = uuidPtr(result.PlanVersionID)
	}
	return eligible, available, accounts, planVersionID, nil
}

// selectPrice asks the deterministic selection of WP-I3-03 which contracted price applies
// to one line. A tie is never broken here: two equally specific prices leave no winner and
// the reason PRICE_AMBIGUOUS, which the calculation turns into REVIEW_REQUIRED.
func (s *Service) selectPrice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in QuoteInput,
	item QuoteItemInput, index int, day time.Time,
) (*contractapp.CandidateRecord, selection.Reason, error) {
	query := contractapp.CandidateQuery{ProviderProfileID: in.ProviderProfileID, ServiceDate: day}
	request := selection.Request{ServiceDate: day}
	if in.LocationID != nil {
		request.LocationID = *in.LocationID
	}

	switch {
	case item.ServiceDefinitionID != nil:
		exists, err := s.repo.ServiceDefinitionExists(ctx, tx, tenantID, *item.ServiceDefinitionID)
		if err != nil {
			return nil, "", err
		}
		if !exists {
			return nil, "", fieldError(fmt.Sprintf("items[%d].serviceDefinitionId", index),
				"NOT_FOUND", "hizmet tanımı bulunamadı")
		}
		categories, err := s.repo.CategoryPath(ctx, tx, tenantID, *item.ServiceDefinitionID)
		if err != nil {
			return nil, "", err
		}
		packages, err := s.repo.PackagesContaining(ctx, tx, tenantID, *item.ServiceDefinitionID)
		if err != nil {
			return nil, "", err
		}
		query.ServiceDefinitionID = *item.ServiceDefinitionID
		query.CategoryIDs, query.PackageIDs = categories, packages
		request.DefinitionID = *item.ServiceDefinitionID
		request.CategoryPath, request.PackagesContaining = categories, packages
	default:
		// A package line is priced against the bundle itself, so the selection is asked
		// about the package and nothing else: no definition, no category chain.
		query.PackageIDs = []uuid.UUID{*item.PackageDefinitionID}
		request.PackagesContaining = []uuid.UUID{*item.PackageDefinitionID}
	}

	candidates, err := s.repo.ListCandidates(ctx, tx, tenantID, query)
	if err != nil {
		return nil, "", err
	}
	rows := make([]selection.Candidate, 0, len(candidates))
	details := make(map[uuid.UUID]contractapp.CandidateRecord, len(candidates))
	for _, c := range candidates {
		rows = append(rows, c.Candidate)
		details[c.Candidate.PriceItemID] = c
	}
	result := selection.Select(request, rows)
	if result.Winner == nil {
		return nil, result.Reason, nil
	}
	winner := details[result.Winner.PriceItemID]
	return &winner, "", nil
}

// priceOf maps the money on a winning price item onto the calculation's own Price. Every
// field is an exact decimal; the empty string is how the contract repository renders a
// NULL column, and it becomes zero or an absent bound rather than a parse failure.
func priceOf(d contractapp.PriceDetail) (pricing.Price, error) {
	amount, err := decimalOrZero(d.Amount, "amount")
	if err != nil {
		return pricing.Price{}, err
	}
	percent, err := decimalOrZero(d.Percent, "percent")
	if err != nil {
		return pricing.Price{}, err
	}
	shareAmount, err := decimalOrZero(d.MemberShareAmount, "memberShareAmount")
	if err != nil {
		return pricing.Price{}, err
	}
	sharePercent, err := decimalOrZero(d.MemberSharePercent, "memberSharePercent")
	if err != nil {
		return pricing.Price{}, err
	}
	minAmount, err := decimalPtr(d.MinAmount, "minAmount")
	if err != nil {
		return pricing.Price{}, err
	}
	maxAmount, err := decimalPtr(d.MaxAmount, "maxAmount")
	if err != nil {
		return pricing.Price{}, err
	}
	return pricing.Price{
		Method: pricing.Method(d.PricingMethod), Amount: amount, Percent: percent,
		MinAmount: minAmount, MaxAmount: maxAmount,
		ShareMethod: pricing.ShareMethod(d.MemberShareMethod),
		ShareAmount: shareAmount, SharePercent: sharePercent,
	}, nil
}

func decimalOrZero(raw, field string) (benefitdomain.Quantity, error) {
	if raw == "" {
		return benefitdomain.ZeroQuantity(), nil
	}
	q, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return benefitdomain.ZeroQuantity(), fmt.Errorf("pricing: price item %s: %w", field, err)
	}
	return q, nil
}

func decimalPtr(raw, field string) (*benefitdomain.Quantity, error) {
	if raw == "" {
		return nil, nil //nolint:nilnil // an absent bound is genuinely "no value, no error"
	}
	q, err := decimalOrZero(raw, field)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// view renders a finished computation as the answer the caller is given and the document
// that is stored beside it.
func (c computation) view(id uuid.UUID, in QuoteInput, quotedAt, expiresAt time.Time,
	quotedBy *uuid.UUID,
) QuoteView {
	out := QuoteView{
		ID: id, PersonID: in.PersonID, ProgramID: in.ProgramID,
		ProviderProfileID: in.ProviderProfileID, LocationID: in.LocationID,
		ServiceDate: benefitdomain.DateOnly(in.ServiceDate), CurrencyCode: c.currency,
		Outcome:         string(c.result.Outcome),
		RequestedAmount: c.result.Requested.String(), ContractAmount: c.result.Contract.String(),
		CoveredAmount: c.result.Covered.String(), PayerAmount: c.result.Payer.String(),
		MemberAmount:      c.result.Member.String(),
		Items:             make([]ItemView, 0, len(c.result.Items)),
		ContractVersionID: c.contractVersionID, PlanVersionID: c.planVersionID,
		RuleSetVersionIDs:       c.ruleSetVersionIDs,
		EligibilityEvaluationID: in.EligibilityEvaluationID,
		ExpiresAt:               expiresAt.UTC(), Expired: !quotedAt.Before(expiresAt),
		QuotedAt: quotedAt.UTC(), QuotedBy: quotedBy, Disclaimer: Disclaimer,
	}
	for i, line := range c.result.Items {
		named := lineIdentity{}
		if i < len(c.lines) {
			named = c.lines[i]
		}
		out.Items = append(out.Items, ItemView{
			LineNo:              line.LineNo,
			ServiceDefinitionID: named.ServiceDefinitionID,
			PackageDefinitionID: named.PackageDefinitionID,
			PriceItemID:         named.PriceItemID,
			Quantity:            named.Quantity.String(),
			RequestedAmount:     line.Requested.String(), ContractAmount: line.Contract.String(),
			CoveredAmount: line.Covered.String(), PayerAmount: line.Payer.String(),
			MemberAmount: line.Member.String(), Outcome: string(line.Outcome),
			Explanations: explanationViews(line.Explanations),
		})
	}
	return out
}

func explanationViews(in []pricing.Explanation) []ExplanationView {
	out := make([]ExplanationView, 0, len(in))
	for _, e := range in {
		view := ExplanationView{Code: e.Code, Severity: string(e.Severity)}
		if e.Source != "" {
			source := e.Source
			view.Source = &source
		}
		out = append(out, view)
	}
	return out
}

// store writes the header, the lines and both snapshots.
func (s *Service) store(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, in QuoteInput,
	hints contextHints, c computation, view QuoteView, hash []byte,
) error {
	request, err := requestSnapshot(in, hints)
	if err != nil {
		return err
	}
	result, err := resultSnapshot(view)
	if err != nil {
		return err
	}
	row := QuoteRow{
		ID: view.ID, PersonID: in.PersonID, ProgramID: in.ProgramID,
		ProviderProfileID: in.ProviderProfileID, LocationID: in.LocationID,
		ServiceDate: view.ServiceDate, CurrencyCode: view.CurrencyCode, Outcome: view.Outcome,
		Requested: c.result.Requested, Contract: c.result.Contract, Covered: c.result.Covered,
		Payer: c.result.Payer, Member: c.result.Member,
		RequestHash: hash, RequestSnapshot: request, ResultSnapshot: result,
		ContractVersionID: c.contractVersionID, PlanVersionID: c.planVersionID,
		EligibilityEvaluationID: in.EligibilityEvaluationID,
		ExpiresAt:               view.ExpiresAt, QuotedAt: view.QuotedAt,
		QuotedBy: actorPtr(rc.Principal.ActorID),
	}
	if in.IdempotencyKey != "" {
		key := in.IdempotencyKey
		row.IdempotencyKey = &key
	}

	items := make([]QuoteItemRow, 0, len(view.Items))
	for i, line := range c.result.Items {
		explanations, err := itemExplanations(view.Items[i].Explanations)
		if err != nil {
			return err
		}
		named := c.lines[i]
		items = append(items, QuoteItemRow{
			LineNo:              line.LineNo,
			ServiceDefinitionID: named.ServiceDefinitionID,
			PackageDefinitionID: named.PackageDefinitionID,
			PriceItemID:         named.PriceItemID,
			Quantity:            named.Quantity,
			Requested:           line.Requested, Contract: line.Contract, Covered: line.Covered,
			Payer: line.Payer, Member: line.Member, Outcome: string(line.Outcome),
			Explanations: explanations,
		})
	}
	return s.repo.CreateQuote(ctx, tx, rc.TenantID, row, items)
}

// replay answers an Idempotency-Key that has already been used. The stored request hash
// decides: the same question replays its quote, a different one is a 409. The rows are
// append-only and the key is unique per tenant, so a replay needs no time window — a
// second row under one key could never have been written.
func (s *Service) replay(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	key string, hash []byte,
) (QuoteView, bool, error) {
	record, items, found, err := s.repo.FindQuoteByKey(ctx, tx, rc.TenantID, key)
	if err != nil || !found {
		return QuoteView{}, false, err
	}
	if !bytes.Equal(record.RequestHash, hash) {
		return QuoteView{}, false, ErrIdempotencyKeyReuse
	}
	provider, err := s.repo.GetProvider(ctx, tx, rc.TenantID, record.ProviderProfileID)
	if err != nil {
		return QuoteView{}, false, err
	}
	if err := checkProviderScope(rc, provider.TenantOrganizationID); err != nil {
		return QuoteView{}, false, err
	}
	view, err := s.storedView(record, items)
	if err != nil {
		return QuoteView{}, false, err
	}
	return view, true, nil
}

// storedView rebuilds the answer from the stored row. The columns are the authority for
// every figure and every id; only the rule set versions are read out of the result
// snapshot, because they have no column of their own.
func (s *Service) storedView(record QuoteRecord, items []QuoteItemRecord) (QuoteView, error) {
	stored := storedResult{}
	if len(record.ResultSnapshot) > 0 {
		if err := json.Unmarshal(record.ResultSnapshot, &stored); err != nil {
			return QuoteView{}, fmt.Errorf("pricing: decode result snapshot: %w", err)
		}
	}
	if stored.RuleSetVersionIDs == nil {
		stored.RuleSetVersionIDs = []uuid.UUID{}
	}
	out := QuoteView{
		ID: record.ID, PersonID: record.PersonID, ProgramID: record.ProgramID,
		ProviderProfileID: record.ProviderProfileID, LocationID: record.LocationID,
		ServiceDate: record.ServiceDate, CurrencyCode: record.CurrencyCode,
		Outcome:         record.Outcome,
		RequestedAmount: record.Requested, ContractAmount: record.Contract,
		CoveredAmount: record.Covered, PayerAmount: record.Payer, MemberAmount: record.Member,
		Items:             make([]ItemView, 0, len(items)),
		ContractVersionID: record.ContractVersionID, PlanVersionID: record.PlanVersionID,
		RuleSetVersionIDs: stored.RuleSetVersionIDs,
		// An expired quote reads back and says so. Hiding it would leave a member who was
		// given a number with no way of finding out why it no longer holds.
		EligibilityEvaluationID: record.EligibilityEvaluationID,
		ExpiresAt:               record.ExpiresAt.UTC(), Expired: !s.now().UTC().Before(record.ExpiresAt),
		QuotedAt: record.QuotedAt.UTC(), QuotedBy: record.QuotedBy, Disclaimer: Disclaimer,
	}
	for _, item := range items {
		explanations := []ExplanationView{}
		if len(item.Explanations) > 0 {
			if err := json.Unmarshal(item.Explanations, &explanations); err != nil {
				return QuoteView{}, fmt.Errorf("pricing: decode line explanations: %w", err)
			}
		}
		out.Items = append(out.Items, ItemView{
			LineNo:              item.LineNo,
			ServiceDefinitionID: item.ServiceDefinitionID,
			PackageDefinitionID: item.PackageDefinitionID,
			PriceItemID:         item.PriceItemID,
			Quantity:            item.Quantity, RequestedAmount: item.Requested,
			ContractAmount: item.Contract, CoveredAmount: item.Covered,
			PayerAmount: item.Payer, MemberAmount: item.Member,
			Outcome: item.Outcome, Explanations: explanations,
		})
	}
	return out, nil
}

// storedDomainIsHealth reads the one hint that decides how a read of this quote is
// classified. It is best effort: a snapshot that cannot be parsed classifies the read
// PERSONAL, which is the conservative answer for an audit row rather than a silent one.
func storedDomainIsHealth(snapshot []byte) bool {
	if len(snapshot) == 0 {
		return false
	}
	var doc struct {
		Context struct {
			Domain string `json:"domain"`
		} `json:"context"`
	}
	if err := json.Unmarshal(snapshot, &doc); err != nil {
		return false
	}
	return doc.Context.Domain == DomainHealth
}

// validateQuote answers 422 before anything is read or written.
func validateQuote(in QuoteInput, hints contextHints) error {
	ve := &benefitdomain.ValidationError{}
	if in.PersonID == uuid.Nil {
		ve.Add("personId", "REQUIRED", "hak sahibi kimliği zorunlu")
	}
	if in.ProviderProfileID == uuid.Nil {
		ve.Add("providerProfileId", "REQUIRED", "sağlayıcı profili zorunlu")
	}
	if in.ServiceDate.IsZero() {
		ve.Add("serviceDate", "REQUIRED", "hizmet tarihi zorunlu")
	}
	switch {
	case len(in.Items) == 0:
		ve.Add("items", "REQUIRED", "en az bir kalem gerekli")
	case len(in.Items) > maxQuoteItems:
		ve.Add("items", "RANGE", "en fazla 100 kalem gönderilebilir")
	}
	for i, item := range in.Items {
		field := fmt.Sprintf("items[%d]", i)
		named := 0
		if item.ServiceDefinitionID != nil && *item.ServiceDefinitionID != uuid.Nil {
			named++
		}
		if item.PackageDefinitionID != nil && *item.PackageDefinitionID != uuid.Nil {
			named++
		}
		if named != 1 {
			ve.Add(field, "REQUIRED", "hizmet tanımı veya paket tanımından yalnız biri verilmeli")
		}
		if !item.Quantity.IsPositive() {
			ve.Add(field+".quantity", "RANGE", "miktar sıfırdan büyük olmalı")
		}
		if item.RequestedAmount != nil && item.RequestedAmount.IsNegative() {
			ve.Add(field+".requestedAmount", "RANGE", "tutar negatif olamaz")
		}
	}
	if len(hints.codes) > len(in.Items) {
		ve.Add("context.entitlementCodes", "RANGE", "kalem sayısından fazla hak kodu gönderilemez")
	}
	return ve.OrNil()
}
