package main

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractdomain "github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
)

// The agreements the demo world runs on: what the hospital is paid for a consultation, what the
// hotel's cancellation costs, what the member is entitled to, and which plan they are enrolled
// in.
//
// Every one of them is published the way the real thing is — a draft written by one person and
// published by another — so the maker-checker rule of WP-I2-02 and WP-I3-02 is exercised rather
// than bypassed. That is why `admin.a` writes and `reviewer.a` publishes throughout: a seed that
// published its own draft would be a seed that proved the gate is not there.

// The prices the demo tariff carries. They are round numbers so that a cut of 500 on one invoice
// is arithmetic anybody watching the demo can follow, and they sit well below the tenant's
// decision and settlement thresholds so that no scenario stalls on a second pair of eyes the
// guide never mentions.
const (
	priceGPVisit      = "1500"
	pricePhysioUnit   = "400"
	priceMRI          = "2500"
	priceLodgingNight = "1800"
	// memberSharePercent is what the member carries of a consultation. It is not zero because a
	// benefit plan that covers everything shows the member app nothing worth showing.
	memberSharePercent = "20"
)

// contractValidFrom is the day both demo contracts and the demo plan version are valid from. It
// is far enough back that every claim, booking and reimbursement the seed writes falls inside
// it, whenever the seed happens to run.
func contractValidFrom(now time.Time) time.Time { return day(now).AddDate(-1, 0, 0) }

// contractValidTo is the day they run to. Two years out, so a demo database does not quietly
// expire while somebody is preparing a presentation with it.
func contractValidTo(now time.Time) time.Time { return day(now).AddDate(2, 0, 0) }

// ensureDemoContracts publishes one contract version for the hospital and one for the hotel.
//
// The hospital's carries the health tariff and the payment term a settlement's due date comes
// from; the hotel's carries the room night's price, its own payment term and the lodging terms
// WP-I6-04's booking freezes into every reservation.
func (s *seeder) ensureDemoContracts(ctx context.Context, sc *scenario) error {
	var err error
	if sc.hospitalVersion, err = s.ensureContract(ctx, sc, contractSpec{
		Code: "DEMO_HEALTH", Name: "Demo sağlık sözleşmesi", Domain: "HEALTH",
		ProfileID: sc.hospitalProfile,
		Items: []contractdomain.PriceItemInput{
			{
				ServiceDefinitionID: sc.services[serviceGPVisit].String(), UnitType: "COUNT",
				PricingMethod: "FIXED", Amount: priceGPVisit,
				MemberShareMethod: "PERCENT", MemberSharePercent: memberSharePercent,
				ValidFrom: contractValidFrom(s.clock.at),
			},
			{
				ServiceDefinitionID: sc.services[servicePhysio].String(), UnitType: "SESSION",
				PricingMethod: "UNIT", Amount: pricePhysioUnit, MemberShareMethod: "NONE",
				ValidFrom: contractValidFrom(s.clock.at),
			},
			{
				ServiceDefinitionID: sc.services[serviceMRI].String(), UnitType: "COUNT",
				PricingMethod: "FIXED", Amount: priceMRI, MemberShareMethod: "NONE",
				ValidFrom: contractValidFrom(s.clock.at),
			},
		},
		// Thirty days, which is what a health provider is ordinarily paid in — and what makes
		// an icmal decided a month ago a settlement that falls due today.
		DueDays: 30,
	}); err != nil {
		return err
	}
	if sc.hotelVersion, err = s.ensureContract(ctx, sc, contractSpec{
		Code: "DEMO_LODGING", Name: "Demo konaklama sözleşmesi", Domain: "ACCOMMODATION",
		ProfileID: sc.hotelProfile,
		Items: []contractdomain.PriceItemInput{
			{
				ServiceDefinitionID: sc.services[serviceLodgingNight].String(), UnitType: "NIGHT",
				PricingMethod: "UNIT", Amount: priceLodgingNight,
				MemberShareMethod: "NONE", ValidFrom: contractValidFrom(s.clock.at),
			},
		},
		DueDays: 14,
		Lodging: &contractdomain.LodgingTermsInput{
			FreeCancellationHoursBefore: 48,
			PenaltyKind:                 contractdomain.PenaltyNightsKind,
			PenaltyNights:               intPtr(1),
			NoShowPercent:               "100",
			HoldMinutes:                 intPtr(30),
			MinNights:                   1,
			MaxNights:                   intPtr(14),
			ChildFreeUnderAge:           intPtr(6),
		},
	}); err != nil {
		return err
	}
	return nil
}

// contractSpec is one whole agreement as this file states it: the header, the tariff, the
// payment term and, for a hotel, the lodging terms.
type contractSpec struct {
	Code      string
	Name      string
	Domain    string
	ProfileID uuid.UUID
	Items     []contractdomain.PriceItemInput
	DueDays   int
	Lodging   *contractdomain.LodgingTermsInput
}

// ensureContract finds the contract by its code and returns its published version, creating and
// publishing one when there is none.
//
// The version is built in the order the schema insists on: a draft, then a price list, then its
// items, then the payment term and the lodging terms, and only then submitted and published. A
// published version is frozen, so everything that belongs to it has to be there first.
//
//nolint:funlen // one linear publishing sequence reads better whole
func (s *seeder) ensureContract(ctx context.Context, sc *scenario, spec contractSpec,
) (uuid.UUID, error) {
	author := rcTenant(sc.tenant, sc.admin, contractapp.PermissionRead, contractapp.PermissionManage)
	publisher := rcTenant(sc.tenant, sc.publisher, contractapp.PermissionRead,
		contractapp.PermissionPublish)

	contractID, err := s.findContract(ctx, author, spec.Code)
	if err != nil {
		return uuid.Nil, err
	}
	if contractID == uuid.Nil {
		record, err := s.biz.contracts.CreateContract(ctx, author, contractdomain.NewContract{
			Code: spec.Code, Name: spec.Name, PayerOrganizationID: sc.payerOrg.String(),
			ProviderProfileID:     spec.ProfileID.String(),
			SponsorOrganizationID: sc.sponsorOrg.String(), DomainCode: spec.Domain,
		})
		if err != nil {
			return uuid.Nil, fmt.Errorf("create contract %s: %w", spec.Code, err)
		}
		contractID = record.ID
	}

	versions, err := s.biz.contracts.ListVersions(ctx, author, contractID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list versions of %s: %w", spec.Code, err)
	}
	for _, v := range versions {
		if v.Status == contractdomain.VersionPublished {
			step("contract", spec.Code, "published version exists")
			return v.ID, s.activateContract(ctx, author, contractID, spec.Code)
		}
	}

	from, to := contractValidFrom(s.clock.at), contractValidTo(s.clock.at)
	version, err := s.biz.contracts.CreateVersion(ctx, author, contractID,
		contractapp.NewVersionInput{ValidFrom: &from, ValidTo: &to, CurrencyCode: "TRY"})
	if err != nil {
		return uuid.Nil, fmt.Errorf("open a version of %s: %w", spec.Code, err)
	}
	lists, err := s.biz.contracts.ReplacePriceLists(ctx, author, version.Version.ID,
		[]contractdomain.PriceListInput{{Code: "STANDART", Name: "Standart liste", Priority: 100}},
		version.Version.RowVersion)
	if err != nil {
		return uuid.Nil, fmt.Errorf("write the price list of %s: %w", spec.Code, err)
	}
	if len(lists.Items) != 1 {
		return uuid.Nil, fmt.Errorf("the price list of %s was not written", spec.Code)
	}
	if _, err := s.biz.contracts.ReplacePriceItems(ctx, author, lists.Items[0].ID, spec.Items,
		lists.Items[0].RowVersion); err != nil {
		return uuid.Nil, fmt.Errorf("write the tariff of %s: %w", spec.Code, err)
	}
	// Every write above touched the version's own ETag, so it is read back rather than
	// remembered: an If-Match built from a stale number is exactly the refusal this seed would
	// otherwise have to explain.
	current, err := s.biz.contracts.GetVersion(ctx, author, version.Version.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("re-read the version of %s: %w", spec.Code, err)
	}
	term, err := s.biz.contracts.PutPaymentTerm(ctx, author, current.Version.ID,
		contractdomain.PaymentTermInput{
			DueDays: spec.DueDays, SettlementMethod: "BANK_TRANSFER",
			TaxBehaviour: "EXCLUSIVE", VatRate: "20",
		}, current.Version.RowVersion)
	if err != nil {
		return uuid.Nil, fmt.Errorf("write the payment term of %s: %w", spec.Code, err)
	}
	rowVersion := term.RowVersion
	if spec.Lodging != nil {
		lodging, err := s.biz.contracts.PutLodgingTerms(ctx, author, current.Version.ID,
			*spec.Lodging, rowVersion)
		if err != nil {
			return uuid.Nil, fmt.Errorf("write the lodging terms of %s: %w", spec.Code, err)
		}
		rowVersion = lodging.RowVersion
	}
	submitted, err := s.biz.contracts.SubmitVersion(ctx, author, current.Version.ID, nil, rowVersion)
	if err != nil {
		return uuid.Nil, fmt.Errorf("submit the version of %s: %w", spec.Code, err)
	}
	// The checker is a different person, which is the whole of the gate.
	published, err := s.biz.contracts.PublishVersion(ctx, publisher, submitted.Version.ID, nil,
		submitted.Version.RowVersion)
	if err != nil {
		return uuid.Nil, fmt.Errorf("publish the version of %s: %w", spec.Code, err)
	}
	step("contract", spec.Code, "published")
	return published.Version.ID, s.activateContract(ctx, author, contractID, spec.Code)
}

// activateContract moves the agreement itself from DRAFT to ACTIVE.
//
// It is a separate act from publishing a version, and it is the one the price selection reads:
// `ListPriceCandidates` narrows to `c.status = 'ACTIVE'`, so a published tariff on a draft
// contract prices nothing at all. A claim against it would be routed to financial review with
// PRICE_NOT_FOUND, which is the correct answer to "we never signed this" and the wrong state for
// a demo world to be in.
func (s *seeder) activateContract(ctx context.Context, rc identity.RequestContext,
	contractID uuid.UUID, code string,
) error {
	current, err := s.biz.contracts.GetContract(ctx, rc, contractID)
	if err != nil {
		return fmt.Errorf("read contract %s: %w", code, err)
	}
	if current.Status != contractdomain.StatusDraft {
		return nil
	}
	active := contractdomain.StatusActive
	if _, err := s.biz.contracts.UpdateContract(ctx, rc, contractID, contractdomain.ContractPatch{
		Status: &active, ExpectedVersion: current.RowVersion,
	}); err != nil {
		return fmt.Errorf("activate contract %s: %w", code, err)
	}
	step("contract", code, "active")
	return nil
}

func (s *seeder) findContract(ctx context.Context, rc identity.RequestContext, code string,
) (uuid.UUID, error) {
	page, err := s.biz.contracts.ListContracts(ctx, rc, contractapp.ListFilter{Limit: 100})
	if err != nil {
		return uuid.Nil, fmt.Errorf("list contracts: %w", err)
	}
	for _, item := range page.Items {
		if item.Code == code {
			return item.ID, nil
		}
	}
	return uuid.Nil, nil
}

// ensureDemoProgram publishes the benefit programme, its plan and the plan version that grants
// the three entitlements the demo member spends: money for health, sessions for physiotherapy
// and nights for accommodation.
func (s *seeder) ensureDemoProgram(ctx context.Context, sc *scenario) error {
	author := rcTenant(sc.tenant, sc.admin)
	publisher := rcTenant(sc.tenant, sc.publisher)

	programID, err := s.findProgram(ctx, author, "DEMO_BENEFIT")
	if err != nil {
		return err
	}
	from, to := contractValidFrom(s.clock.at), contractValidTo(s.clock.at)
	if programID == uuid.Nil {
		program, err := s.benefits.CreateProgram(ctx, author, benefitapp.NewProgramInput{
			Code: "DEMO_BENEFIT", Name: "Demo çalışan faydası", ProgramType: "EMPLOYEE_BENEFIT",
			SponsorOrganizationID: sc.sponsorOrg, PayerOrganizationID: sc.payerOrg,
			ValidFrom: &from, ValidTo: &to,
		})
		if err != nil {
			return fmt.Errorf("create the demo programme: %w", err)
		}
		programID = program.ID
	}
	sc.program = programID
	if err := s.activateProgram(ctx, author, programID); err != nil {
		return err
	}

	plans, err := s.benefits.ListPlans(ctx, author, programID)
	if err != nil {
		return fmt.Errorf("list plans: %w", err)
	}
	for _, p := range plans {
		if p.Code == "DEMO_STANDARD" {
			sc.plan = p.ID
		}
	}
	if sc.plan == uuid.Nil {
		plan, err := s.benefits.CreatePlan(ctx, author, programID, benefitapp.NewPlanInput{
			Code: "DEMO_STANDARD", Name: "Standart paket",
		})
		if err != nil {
			return fmt.Errorf("create the demo plan: %w", err)
		}
		sc.plan = plan.ID
	}

	versions, err := s.benefits.ListPlanVersions(ctx, author, sc.plan)
	if err != nil {
		return fmt.Errorf("list plan versions: %w", err)
	}
	for _, v := range versions {
		if v.Status == benefitdomain.VersionPublished {
			sc.planVersion = v.ID
			step("plan", "DEMO_STANDARD", "published version exists")
			return s.activatePlan(ctx, author, sc.plan)
		}
	}

	draft, err := s.benefits.CreatePlanVersion(ctx, author, sc.plan,
		benefitapp.NewPlanVersionInput{ValidFrom: &from, ValidTo: &to})
	if err != nil {
		return fmt.Errorf("open a plan version: %w", err)
	}
	withDefs, err := s.benefits.ReplaceDefinitions(ctx, author, draft.ID,
		demoEntitlements(), draft.RowVersion)
	if err != nil {
		return fmt.Errorf("write the entitlement definitions: %w", err)
	}
	// The service → entitlement mappings, written through the same step `seed demo` already
	// runs for every tenant. It is called again here because it is what turns "the member has
	// 40 000 lira of health budget" into "a consultation draws on it".
	mappings := []benefitapp.MappingInput{
		{ServiceDefinitionID: sc.services[serviceGPVisit], EntitlementCode: entitlementMoney, UnitFactor: "1"},
		{ServiceDefinitionID: sc.services[serviceMRI], EntitlementCode: entitlementMoney, UnitFactor: "1"},
		{ServiceDefinitionID: sc.services[servicePhysio], EntitlementCode: entitlementPhysio, UnitFactor: "1"},
		{ServiceDefinitionID: sc.services[serviceLodgingNight], EntitlementCode: entitlementNights, UnitFactor: "1"},
	}
	if _, err := s.benefits.ReplaceMappings(ctx, author, withDefs.ID, mappings,
		withDefs.RowVersion); err != nil {
		return fmt.Errorf("map the demo services onto entitlements: %w", err)
	}
	current, err := s.benefits.GetPlanVersion(ctx, author, withDefs.ID)
	if err != nil {
		return fmt.Errorf("re-read the plan version: %w", err)
	}
	submitted, err := s.benefits.SubmitPlanVersion(ctx, author, current.ID, nil, current.RowVersion)
	if err != nil {
		return fmt.Errorf("submit the plan version: %w", err)
	}
	published, err := s.benefits.PublishPlanVersion(ctx, publisher, submitted.ID, nil,
		submitted.RowVersion)
	if err != nil {
		return fmt.Errorf("publish the plan version: %w", err)
	}
	sc.planVersion = published.ID
	step("plan", "DEMO_STANDARD", "published")
	return s.activatePlan(ctx, author, sc.plan)
}

// activatePlan moves the plan itself from DRAFT to ACTIVE.
//
// It is a separate act from publishing a version, and deliberately: a version is a tariff of
// entitlements and a plan is the thing people are enrolled in, so "this package is ready to
// publish" and "this package is open for enrollment" are two decisions. Nobody can be enrolled
// in a DRAFT plan, which is what the refusal says.
func (s *seeder) activatePlan(ctx context.Context, rc identity.RequestContext, planID uuid.UUID,
) error {
	plan, err := s.benefits.GetPlan(ctx, rc, planID)
	if err != nil {
		return fmt.Errorf("read the demo plan: %w", err)
	}
	if plan.Status != benefitdomain.PlanDraft {
		return nil
	}
	active := benefitdomain.PlanActive
	if _, err := s.benefits.UpdatePlan(ctx, rc, planID, benefitapp.PlanPatch{
		Status: &active, ExpectedVersion: plan.RowVersion,
	}); err != nil {
		return fmt.Errorf("open the demo plan for enrollment: %w", err)
	}
	step("plan", "DEMO_STANDARD", "active")
	return nil
}

// activateProgram moves the programme from DRAFT to ACTIVE.
//
// It is what the availability search reads: `ListPersonProgramPayers` narrows to `ACTIVE`
// programmes, so a member enrolled in a draft programme is shown no hotel at all — which is
// right, because nobody has agreed to pay for their stay yet.
func (s *seeder) activateProgram(ctx context.Context, rc identity.RequestContext,
	programID uuid.UUID,
) error {
	program, err := s.benefits.GetProgram(ctx, rc, programID)
	if err != nil {
		return fmt.Errorf("read the demo programme: %w", err)
	}
	if program.Status != benefitdomain.ProgramDraft {
		return nil
	}
	active := benefitdomain.ProgramActive
	if _, err := s.benefits.UpdateProgram(ctx, rc, programID, benefitapp.ProgramPatch{
		Status: &active, ExpectedVersion: program.RowVersion,
	}); err != nil {
		return fmt.Errorf("open the demo programme: %w", err)
	}
	step("program", "DEMO_BENEFIT", "active")
	return nil
}

// demoEntitlements is what the standard package grants for a calendar year.
//
// The money balance is large enough to cover every claim the seed writes and still leave a
// remaining figure on the member's home screen; the nights are deliberately few, because "three
// nights booked against four remaining" is the partial-cover case the member app exists to show.
func demoEntitlements() []benefitdomain.EntitlementDefinition {
	return []benefitdomain.EntitlementDefinition{
		{
			Code: entitlementMoney, Name: "Sağlık bütçesi", UnitType: "MONEY",
			CurrencyCode: "TRY", PeriodType: "CALENDAR_YEAR", InitialQuantity: "40000",
		},
		{
			Code: entitlementPhysio, Name: "Fizyoterapi seansı", UnitType: "SESSION",
			PeriodType: "CALENDAR_YEAR", InitialQuantity: "20",
		},
		{
			Code: entitlementNights, Name: "Konaklama gecesi", UnitType: "NIGHT",
			PeriodType: "CALENDAR_YEAR", InitialQuantity: "10",
		},
	}
}

func (s *seeder) findProgram(ctx context.Context, rc identity.RequestContext, code string,
) (uuid.UUID, error) {
	page, err := s.benefits.ListPrograms(ctx, rc, benefitapp.ProgramFilter{Limit: 100})
	if err != nil {
		return uuid.Nil, fmt.Errorf("list programmes: %w", err)
	}
	for _, item := range page.Items {
		if item.Code == code {
			return item.ID, nil
		}
	}
	return uuid.Nil, nil
}

// ensureDemoEnrollment puts the bound member into the plan and opens the entitlement accounts
// that follow from it.
//
// The accounts are opened by the ledger's own handler rather than by a write here: an enrollment
// publishes `benefit.enrollment.created` and the worker's subscriber turns the plan's definitions
// into balances. The seed delivers that event to the same handler, with the payload the command
// actually published, so a demo database's wallets were opened by the code that opens real ones.
func (s *seeder) ensureDemoEnrollment(ctx context.Context, sc *scenario) error {
	personID, err := s.ensureDemoPerson(ctx, identity.RequestContext{TenantID: sc.tenant})
	if err != nil {
		return err
	}
	sc.person = personID
	sc.membership, sc.enrollment, err = s.ensurePersonEnrollment(ctx, sc, personID, "")
	return err
}

// ensurePersonEnrollment makes one demo person an employee of the demo sponsor, enrols them in
// the demo plan and opens their entitlement accounts. It is the whole of ensureDemoEnrollment
// for any person, so a second demo person is eligible for exactly what the first one is.
//
// who labels the step lines; empty keeps the labels the first demo person has always printed.
func (s *seeder) ensurePersonEnrollment(ctx context.Context, sc *scenario, personID uuid.UUID,
	who string,
) (membershipID, enrollmentID uuid.UUID, err error) {
	rc := rcTenant(sc.tenant, sc.admin)
	label := func(what string) string {
		if who == "" {
			return what
		}
		return who + " " + what
	}

	memberships, err := s.party.ListMemberships(ctx, rc, personID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("list the memberships of %s: %w", personID, err)
	}
	for _, m := range memberships {
		if m.SponsorOrganizationID == sc.sponsorOrg {
			membershipID = m.ID
		}
	}
	from := contractValidFrom(s.clock.at)
	if membershipID == uuid.Nil {
		membership, err := s.party.CreateMembership(ctx, rc, personID, partyapp.NewMembershipInput{
			SponsorOrganizationID: sc.sponsorOrg, MembershipType: "EMPLOYEE",
			Status: "ACTIVE", ValidFrom: from,
		})
		if err != nil {
			return uuid.Nil, uuid.Nil, fmt.Errorf("make %s an employee of the sponsor: %w", personID, err)
		}
		membershipID = membership.ID
	}

	enrollments, err := s.benefits.ListPersonEnrollments(ctx, rc, personID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("list the enrollments of %s: %w", personID, err)
	}
	for _, e := range enrollments {
		if e.PlanID == sc.plan {
			step("member", label("enrollment"), "exists")
			return membershipID, e.ID, s.openEntitlementAccounts(ctx, sc.tenant, e.ID, label("entitlements"))
		}
	}
	// The enrollment starts on the first day of the current calendar year rather than on the
	// day the contract did. The entitlement definitions are CALENDAR_YEAR, and the accounts the
	// ledger opens cover the period containing the enrollment's start — so an enrollment dated
	// a year back would give the member a wallet for last year and nothing to spend today.
	enrollment, err := s.benefits.CreateEnrollment(ctx, rc, personID, benefitapp.NewEnrollmentInput{
		SponsorMembershipID: membershipID, PlanID: sc.plan, Status: "ACTIVE",
		ValidFrom: startOfYear(s.clock.at),
	})
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("enrol %s: %w", personID, err)
	}
	step("member", label("enrollment"), "created")
	return membershipID, enrollment.ID,
		s.openEntitlementAccounts(ctx, sc.tenant, enrollment.ID, label("entitlements"))
}

// openEntitlementAccounts hands the enrollment's own `benefit.enrollment.created` event to the
// handler the worker subscribes with. It is idempotent by design — the handler looks for the
// accounts a first delivery opened — so running it on a second seed pass finds them already open.
func (s *seeder) openEntitlementAccounts(ctx context.Context, tenantID, enrollmentID uuid.UUID,
	label string,
) error {
	delivery, err := s.readOutboxEvent(ctx, tenantID, enrollmentID,
		benefitapp.EnrollmentCreatedEvent)
	if err != nil {
		return err
	}
	if err := s.biz.entitlements.HandleEnrollmentCreated(ctx, delivery); err != nil {
		return fmt.Errorf("open the entitlement accounts of enrollment %s: %w", enrollmentID, err)
	}
	step("member", label, "accounts open")
	return nil
}

// startOfYear is the first day of the calendar year the clock is in, which is the period every
// CALENDAR_YEAR entitlement of this world is granted for.
func startOfYear(at time.Time) time.Time {
	return time.Date(at.UTC().Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
}

func intPtr(v int) *int { return &v }
