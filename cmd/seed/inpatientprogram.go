package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	catalogdomain "github.com/celikbros/kapsora/internal/catalog/domain"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractdomain "github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

const (
	inpatientCode     = "INPATIENT_DAY"
	inpatientPlanCode = "PC04_INPATIENT"
)

type inpatientProgramResult struct {
	ProgramID           uuid.UUID `json:"programId"`
	PlanID              uuid.UUID `json:"planId"`
	PlanVersionID       uuid.UUID `json:"planVersionId"`
	ContractID          uuid.UUID `json:"contractId"`
	ContractVersionID   uuid.UUID `json:"contractVersionId"`
	ServiceDefinitionID uuid.UUID `json:"serviceDefinitionId"`
}

func validInpatientProgramMarker(code string) bool {
	if !strings.HasPrefix(code, "PC04_") || len(code) != len("PC04_")+32 {
		return false
	}
	suffix := strings.TrimPrefix(code, "PC04_")
	if strings.ToUpper(suffix) != suffix {
		return false
	}
	_, err := uuid.Parse(suffix)
	return err == nil
}
func validateInpatientProgram(p benefitapp.Program, id uuid.UUID, baseline benefitapp.Program, now time.Time) error {
	if id == uuid.Nil || p.ID != id || !validInpatientProgramMarker(p.Code) || p.Name != "PC04 inpatient "+id.String() ||
		p.ProgramType != baseline.ProgramType || p.SponsorOrganizationID != baseline.SponsorOrganizationID ||
		p.PayerOrganizationID != baseline.PayerOrganizationID || p.ValidFrom == nil || p.ValidTo == nil {
		return fmt.Errorf("inpatient fixture refuses a non-dedicated program or changed parties")
	}
	if !p.ValidFrom.Before(*p.ValidTo) || p.ValidTo.Sub(*p.ValidFrom) > 31*24*time.Hour ||
		p.ValidFrom.After(now.Add(-25*time.Hour)) || p.ValidTo.Before(now.Add(8*24*time.Hour)) {
		return fmt.Errorf("inpatient fixture program dates must cover admission and extension within 31 days")
	}
	if p.Status != benefitdomain.ProgramDraft && p.Status != benefitdomain.ProgramActive {
		return fmt.Errorf("inpatient fixture program has unexpected status %s", p.Status)
	}
	return nil
}

func sameDates(a, b *time.Time) bool {
	return a != nil && b != nil && a.Equal(*b)
}

func decimalIs(value, want string) bool {
	a, ok := new(big.Rat).SetString(value)
	if !ok {
		return false
	}
	b, _ := new(big.Rat).SetString(want)
	return a.Cmp(b) == 0
}

// inpatientProgram only changes objects belonging to one explicitly marked DEMO_A program.
// The harness creates that program and handles person/enrollment through the API.
func (s *seeder) inpatientProgram(ctx context.Context, id uuid.UUID) (inpatientProgramResult, error) {
	out := inpatientProgramResult{ProgramID: id}
	if s.biz == nil || s.biz.contracts == nil || s.biz.providers == nil {
		return out, fmt.Errorf("inpatient fixture needs contract and provider services")
	}
	tenant, err := sqlcgen.New(s.pool).GetTenantIDByCode(ctx, "DEMO_A")
	if err != nil {
		return out, err
	}
	maker, err := s.credentials.FindByUsername(ctx, "admin.a")
	if err != nil {
		return out, err
	}
	checker, err := s.credentials.FindByUsername(ctx, "reviewer.a")
	if err != nil {
		return out, err
	}
	if maker.ActorID == checker.ActorID {
		return out, fmt.Errorf("fixture needs distinct maker and checker")
	}
	author := rcTenant(tenant, maker.ActorID, contractapp.PermissionRead, contractapp.PermissionManage)
	publisher := rcTenant(tenant, checker.ActorID, contractapp.PermissionRead, contractapp.PermissionPublish)
	program, err := s.benefits.GetProgram(ctx, author, id)
	if err != nil {
		return out, err
	}
	baselineID, err := s.findProgram(ctx, author, "DEMO_BENEFIT")
	if err != nil {
		return out, err
	}
	if baselineID == uuid.Nil {
		return out, fmt.Errorf("DEMO_BENEFIT program is required")
	}
	baseline, err := s.benefits.GetProgram(ctx, author, baselineID)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	if s.nowFn != nil {
		now = s.nowFn().UTC()
	}
	if err := validateInpatientProgram(program, id, baseline, now); err != nil {
		return out, err
	}

	hospitalContractID, err := s.findContract(ctx, author, "DEMO_HEALTH")
	if err != nil {
		return out, err
	}
	if hospitalContractID == uuid.Nil {
		return out, fmt.Errorf("DEMO_HEALTH contract is required")
	}
	hospitalContract, err := s.biz.contracts.GetContract(ctx, author, hospitalContractID)
	if err != nil {
		return out, err
	}
	provider, err := s.biz.providers.GetProvider(ctx, author, hospitalContract.ProviderProfileID)
	if err != nil {
		return out, err
	}
	if provider.Status != "ACTIVE" || provider.ProviderType != "HOSPITAL" || hospitalContract.Status != contractdomain.StatusActive ||
		hospitalContract.DomainCode != "HEALTH" || hospitalContract.PayerOrganizationID != program.PayerOrganizationID ||
		hospitalContract.SponsorOrganizationID == nil || *hospitalContract.SponsorOrganizationID != program.SponsorOrganizationID {
		return out, fmt.Errorf("active demo hospital contract/profile with matching parties is required")
	}
	if err := s.validateProviderStaffGrant(ctx, tenant, provider.TenantOrganizationID); err != nil {
		return out, err
	}
	if err := s.preflightInpatientContract(ctx, author, program, hospitalContract.ProviderProfileID); err != nil {
		return out, err
	}
	serviceID, err := s.inpatientService(ctx, author)
	if err != nil {
		return out, err
	}
	out.ServiceDefinitionID = serviceID
	if err := s.refuseCompetingInpatientTariff(ctx, tenant, hospitalContract.ProviderProfileID, serviceID, program); err != nil {
		return out, err
	}
	if err := s.inpatientContract(ctx, author, publisher, program, hospitalContract.ProviderProfileID, serviceID, &out); err != nil {
		return out, err
	}
	if program.Status == benefitdomain.ProgramDraft {
		active := benefitdomain.ProgramActive
		program, err = s.benefits.UpdateProgram(ctx, author, id, benefitapp.ProgramPatch{Status: &active, ExpectedVersion: program.RowVersion})
		if err != nil {
			return out, err
		}
	}
	if err := s.inpatientPlan(ctx, author, publisher, program, serviceID, &out); err != nil {
		return out, err
	}

	// This is a program override; the default and every other program setting is retained.
	err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
		return writeProgramReview(ctx, tx, tenant, id, true)
	})
	return out, err
}

func (s *seeder) validateProviderStaffGrant(ctx context.Context, tenant, hospital uuid.UUID) error {
	account, err := s.credentials.FindByUsername(ctx, "provider.a")
	if err != nil {
		return err
	}
	return db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant g
            JOIN iam.tenant_membership m ON m.id=g.tenant_membership_id AND m.tenant_id=g.tenant_id
            JOIN iam.role r ON r.id=g.role_id AND r.tenant_id=g.tenant_id
            WHERE g.tenant_id=$1 AND m.actor_id=$2 AND m.membership_status='ACTIVE'
              AND m.valid_period @> CURRENT_DATE AND r.code='PROVIDER_STAFF'
              AND g.scope_type='ORGANIZATION' AND g.scope_id=$3
              AND g.valid_period @> clock_timestamp()`, tenant, account.ActorID, hospital).Scan(&count)
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("provider.a must have one active staff grant for demo hospital")
		}
		return nil
	})
}

// Pricing does not carry a program key. An active published exact-definition tariff at
// this hospital would compete even if it belonged to a different payer or sponsor.
func (s *seeder) refuseCompetingInpatientTariff(ctx context.Context, tenant, profileID, serviceID uuid.UUID, program benefitapp.Program) error {
	return db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*)
            FROM contract.contract c
            JOIN contract.contract_version v ON v.tenant_id=c.tenant_id AND v.contract_id=c.id
            JOIN contract.price_list l ON l.tenant_id=v.tenant_id AND l.contract_version_id=v.id
            JOIN contract.price_item i ON i.tenant_id=l.tenant_id AND i.price_list_id=l.id
            WHERE c.tenant_id=$1 AND c.provider_profile_id=$2 AND c.code<>'PC04_INPATIENT'
              AND c.status='ACTIVE' AND v.status='PUBLISHED'
              AND i.service_definition_id=$3 AND i.location_id IS NULL
              AND daterange(v.valid_from,v.valid_to,'[)') && daterange($4::date,$5::date,'[)')
              AND daterange(i.valid_from,i.valid_to,'[)') && daterange($4::date,$5::date,'[)')`,
			tenant, profileID, serviceID, *program.ValidFrom, *program.ValidTo).Scan(&count)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("another active published INPATIENT_DAY tariff overlaps this hospital and window")
		}
		return nil
	})
}
func (s *seeder) inpatientService(ctx context.Context, rc identity.RequestContext) (uuid.UUID, error) {
	page, err := s.catalog.ListCategories(ctx, rc, catalogapp.ListFilter{Query: "HEALTH_ROOT", Limit: 50})
	if err != nil {
		return uuid.Nil, err
	}
	var categoryID uuid.UUID
	for _, c := range page.Items {
		if c.Code == "HEALTH_ROOT" {
			if c.Domain != "HEALTH" || !c.Active {
				return uuid.Nil, fmt.Errorf("HEALTH_ROOT category is not active HEALTH")
			}
			categoryID = c.ID
		}
	}
	if categoryID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("HEALTH_ROOT category is required")
	}
	definitions, err := s.catalog.ListDefinitions(ctx, rc, catalogapp.ListFilter{Query: inpatientCode, Limit: 50})
	if err != nil {
		return uuid.Nil, err
	}
	var definitionID uuid.UUID
	for _, d := range definitions.Items {
		if d.Code == inpatientCode {
			definitionID = d.ID
		}
	}
	if definitionID == uuid.Nil {
		d, err := s.catalog.CreateDefinition(ctx, rc, catalogdomain.NewDefinition{
			CategoryID: categoryID.String(), Code: inpatientCode, Name: "Yatış günü",
			FulfillmentMode: "DIRECT", DefaultUnitType: "NIGHT", RequiresProvider: true, Active: true,
		})
		if err != nil {
			return uuid.Nil, err
		}
		definitionID = d.ID
	}
	d, err := s.catalog.GetDefinition(ctx, rc, definitionID)
	if err != nil {
		return uuid.Nil, err
	}
	if d.CategoryID != categoryID || d.Domain != "HEALTH" || d.FulfillmentMode != "DIRECT" || d.DefaultUnitType != "NIGHT" ||
		!d.RequiresProvider || !d.Active {
		return uuid.Nil, fmt.Errorf("INPATIENT_DAY definition has unexpected category, mode, unit, provider requirement or status")
	}
	return definitionID, nil
}

func (s *seeder) inpatientPlan(ctx context.Context, author, publisher identity.RequestContext, program benefitapp.Program, serviceID uuid.UUID, out *inpatientProgramResult) error {
	plans, err := s.benefits.ListPlans(ctx, author, program.ID)
	if err != nil {
		return err
	}
	if len(plans) > 1 {
		return fmt.Errorf("inpatient fixture expects one plan")
	}
	var plan benefitapp.Plan
	if len(plans) == 0 {
		plan, err = s.benefits.CreatePlan(ctx, author, program.ID, benefitapp.NewPlanInput{Code: inpatientPlanCode, Name: "PC04 inpatient plan"})
		if err != nil {
			return err
		}
	} else {
		plan = plans[0]
	}
	if plan.Code != inpatientPlanCode || plan.Name != "PC04 inpatient plan" {
		return fmt.Errorf("unexpected inpatient fixture plan")
	}
	out.PlanID = plan.ID
	versions, err := s.benefits.ListPlanVersions(ctx, author, plan.ID)
	if err != nil {
		return err
	}
	if len(versions) > 1 {
		return fmt.Errorf("inpatient fixture expects one plan version")
	}
	var v benefitapp.PlanVersion
	if len(versions) == 0 {
		v, err = s.benefits.CreatePlanVersion(ctx, author, plan.ID, benefitapp.NewPlanVersionInput{ValidFrom: program.ValidFrom, ValidTo: program.ValidTo})
	} else {
		v, err = s.benefits.GetPlanVersion(ctx, author, versions[0].ID)
	}
	if err != nil {
		return err
	}
	if !sameDates(v.ValidFrom, program.ValidFrom) || !sameDates(v.ValidTo, program.ValidTo) {
		return fmt.Errorf("inpatient plan version dates drifted")
	}
	if v.Status == benefitdomain.VersionDraft {
		v, err = s.benefits.ReplaceDefinitions(ctx, author, v.ID, []benefitdomain.EntitlementDefinition{{
			Code: inpatientCode, Name: "Yatış günü", UnitType: "NIGHT", PeriodType: "CALENDAR_YEAR", InitialQuantity: "20",
		}}, v.RowVersion)
		if err != nil {
			return err
		}
		if _, err = s.benefits.ReplaceMappings(ctx, author, v.ID, []benefitapp.MappingInput{{ServiceDefinitionID: serviceID, EntitlementCode: inpatientCode, UnitFactor: "1"}}, v.RowVersion); err != nil {
			return err
		}
		v, err = s.benefits.GetPlanVersion(ctx, author, v.ID)
		if err != nil {
			return err
		}
		v, err = s.benefits.SubmitPlanVersion(ctx, author, v.ID, nil, v.RowVersion)
		if err != nil {
			return err
		}
	}
	if v.Status == benefitdomain.VersionUnderReview {
		v, err = s.benefits.PublishPlanVersion(ctx, publisher, v.ID, nil, v.RowVersion)
		if err != nil {
			return err
		}
	}
	if v.Status != benefitdomain.VersionPublished {
		return fmt.Errorf("inpatient plan version is not published")
	}
	if err := s.validateInpatientPlan(ctx, author, v, serviceID); err != nil {
		return err
	}
	out.PlanVersionID = v.ID
	if plan.Status == benefitdomain.PlanDraft {
		active := benefitdomain.PlanActive
		_, err = s.benefits.UpdatePlan(ctx, author, plan.ID, benefitapp.PlanPatch{Status: &active, ExpectedVersion: plan.RowVersion})
		if err != nil {
			return err
		}
	} else if plan.Status != benefitdomain.PlanActive {
		return fmt.Errorf("inpatient plan has unexpected status")
	}
	return nil
}

func (s *seeder) validateInpatientPlan(ctx context.Context, rc identity.RequestContext, version benefitapp.PlanVersion, serviceID uuid.UUID) error {
	v, err := s.benefits.GetPlanVersion(ctx, rc, version.ID)
	if err != nil {
		return err
	}
	if len(v.Definitions) != 1 {
		return fmt.Errorf("inpatient plan must have one entitlement")
	}
	d := v.Definitions[0].Spec
	if d.Code != inpatientCode || d.Name != "Yatış günü" || d.UnitType != "NIGHT" || d.CurrencyCode != "" ||
		d.PeriodType != "CALENDAR_YEAR" || d.PeriodLength != nil || !decimalIs(d.InitialQuantity, "20") ||
		d.AllowOverdraft || d.RolloverPolicy != "NONE" || d.RolloverCap != "" || d.FamilyShared || v.Definitions[0].Status != "ACTIVE" {
		return fmt.Errorf("published inpatient entitlement drifted")
	}
	mappings, err := s.benefits.ListMappings(ctx, rc, v.ID)
	if err != nil {
		return err
	}
	if len(mappings) != 1 || mappings[0].ServiceDefinitionID != serviceID || mappings[0].EntitlementCode != inpatientCode || !decimalIs(mappings[0].UnitFactor, "1") || mappings[0].UnitType != "NIGHT" || mappings[0].ValidFrom != nil || mappings[0].ValidTo != nil {
		return fmt.Errorf("published inpatient service mapping drifted")
	}
	return nil
}

// A second PC04 program may reuse the one published tariff only when its whole
// admission/extension window fits. Refuse before publishing a new plan or tariff.
func (s *seeder) preflightInpatientContract(ctx context.Context, rc identity.RequestContext, program benefitapp.Program, profileID uuid.UUID) error {
	id, err := s.findContract(ctx, rc, "PC04_INPATIENT")
	if err != nil || id == uuid.Nil {
		return err
	}
	c, err := s.biz.contracts.GetContract(ctx, rc, id)
	if err != nil {
		return err
	}
	if c.Name != "PC04 inpatient contract" || c.DomainCode != "HEALTH" || c.ProviderProfileID != profileID ||
		c.PayerOrganizationID != program.PayerOrganizationID || c.SponsorOrganizationID == nil ||
		*c.SponsorOrganizationID != program.SponsorOrganizationID {
		return fmt.Errorf("shared inpatient contract parties or domain drifted")
	}
	versions, err := s.biz.contracts.ListVersions(ctx, rc, id)
	if err != nil {
		return err
	}
	if len(versions) > 1 {
		return fmt.Errorf("shared inpatient contract has multiple versions")
	}
	if len(versions) == 0 {
		return nil
	}
	v := versions[0]
	if v.ValidFrom == nil || v.ValidTo == nil || v.ValidFrom.After(*program.ValidFrom) || v.ValidTo.Before(*program.ValidTo) ||
		v.ValidTo.Sub(*v.ValidFrom) > 31*24*time.Hour || v.CurrencyCode != "TRY" {
		return fmt.Errorf("shared inpatient contract does not cover program window")
	}
	if v.Status == contractdomain.VersionDraft && (!sameDates(v.ValidFrom, program.ValidFrom) || !sameDates(v.ValidTo, program.ValidTo)) {
		return fmt.Errorf("draft inpatient contract belongs to a different program window")
	}
	if v.Status != contractdomain.VersionDraft && v.Status != contractdomain.VersionPublished {
		return fmt.Errorf("shared inpatient contract is not ready for reuse")
	}
	return nil
}
func (s *seeder) inpatientContract(ctx context.Context, author, publisher identity.RequestContext, program benefitapp.Program, profileID, serviceID uuid.UUID, out *inpatientProgramResult) error {
	code := "PC04_INPATIENT"
	name := "PC04 inpatient contract"
	contractID, err := s.findContract(ctx, author, code)
	if err != nil {
		return err
	}
	if contractID == uuid.Nil {
		record, err := s.biz.contracts.CreateContract(ctx, author, contractdomain.NewContract{
			Code: code, Name: name, PayerOrganizationID: program.PayerOrganizationID.String(),
			ProviderProfileID: profileID.String(), SponsorOrganizationID: program.SponsorOrganizationID.String(), DomainCode: "HEALTH",
		})
		if err != nil {
			return err
		}
		contractID = record.ID
	}
	c, err := s.biz.contracts.GetContract(ctx, author, contractID)
	if err != nil {
		return err
	}
	if c.Code != code || c.Name != name || c.DomainCode != "HEALTH" || c.ProviderProfileID != profileID || c.PayerOrganizationID != program.PayerOrganizationID || c.SponsorOrganizationID == nil || *c.SponsorOrganizationID != program.SponsorOrganizationID {
		return fmt.Errorf("inpatient contract header drifted")
	}
	out.ContractID = contractID
	versions, err := s.biz.contracts.ListVersions(ctx, author, contractID)
	if err != nil {
		return err
	}
	if len(versions) > 1 {
		return fmt.Errorf("inpatient contract expects one version")
	}
	var version contractapp.VersionView
	if len(versions) == 0 {
		version, err = s.biz.contracts.CreateVersion(ctx, author, contractID, contractapp.NewVersionInput{
			ValidFrom: program.ValidFrom, ValidTo: program.ValidTo, CurrencyCode: "TRY",
		})
	} else {
		version, err = s.biz.contracts.GetVersion(ctx, author, versions[0].ID)
	}
	if err != nil {
		return err
	}
	v := version.Version
	if v.ValidFrom == nil || v.ValidTo == nil || v.ValidFrom.After(*program.ValidFrom) || v.ValidTo.Before(*program.ValidTo) ||
		v.ValidTo.Sub(*v.ValidFrom) > 31*24*time.Hour || v.CurrencyCode != "TRY" {
		return fmt.Errorf("inpatient contract version dates or currency drifted")
	}
	if v.Status == contractdomain.VersionDraft {
		if !sameDates(v.ValidFrom, program.ValidFrom) || !sameDates(v.ValidTo, program.ValidTo) {
			return fmt.Errorf("draft inpatient contract belongs to a different window")
		}
		lists, err := s.biz.contracts.ReplacePriceLists(ctx, author, v.ID, []contractdomain.PriceListInput{{Code: "STANDART", Name: "Standart liste", Priority: 100}}, v.RowVersion)
		if err != nil {
			return err
		}
		if len(lists.Items) != 1 {
			return fmt.Errorf("inpatient price list was not written")
		}
		_, err = s.biz.contracts.ReplacePriceItems(ctx, author, lists.Items[0].ID, []contractdomain.PriceItemInput{{
			ServiceDefinitionID: serviceID.String(), UnitType: "NIGHT", PricingMethod: "UNIT", Amount: "400", MemberShareMethod: "NONE", ValidFrom: *program.ValidFrom,
		}}, lists.Items[0].RowVersion)
		if err != nil {
			return err
		}
		current, err := s.biz.contracts.GetVersion(ctx, author, v.ID)
		if err != nil {
			return err
		}
		term, err := s.biz.contracts.PutPaymentTerm(ctx, author, v.ID, contractdomain.PaymentTermInput{
			DueDays: 30, SettlementMethod: "BANK_TRANSFER", TaxBehaviour: "EXCLUSIVE", VatRate: "20",
		}, current.Version.RowVersion)
		if err != nil {
			return err
		}
		submitted, err := s.biz.contracts.SubmitVersion(ctx, author, v.ID, nil, term.RowVersion)
		if err != nil {
			return err
		}
		version, err = s.biz.contracts.PublishVersion(ctx, publisher, v.ID, nil, submitted.Version.RowVersion)
		if err != nil {
			return err
		}
		v = version.Version
	} else if v.Status != contractdomain.VersionPublished {
		return fmt.Errorf("inpatient contract version is neither draft nor published")
	}
	if err := s.validateInpatientContract(ctx, author, v.ID, serviceID, *v.ValidFrom); err != nil {
		return err
	}
	out.ContractVersionID = v.ID
	if c.Status == contractdomain.StatusDraft {
		active := contractdomain.StatusActive
		_, err = s.biz.contracts.UpdateContract(ctx, author, c.ID, contractdomain.ContractPatch{Status: &active, ExpectedVersion: c.RowVersion})
		if err != nil {
			return err
		}
	} else if c.Status != contractdomain.StatusActive {
		return fmt.Errorf("inpatient contract has unexpected status")
	}
	return nil
}

func (s *seeder) validateInpatientContract(ctx context.Context, rc identity.RequestContext, versionID, serviceID uuid.UUID, from time.Time) error {
	version, err := s.biz.contracts.GetVersion(ctx, rc, versionID)
	if err != nil {
		return err
	}
	if version.Version.Status != contractdomain.VersionPublished || len(version.PriceLists) != 1 {
		return fmt.Errorf("inpatient published contract contents drifted")
	}
	list := version.PriceLists[0]
	if list.Code != "STANDART" || list.Name != "Standart liste" || list.Priority != 100 || list.ItemCount != 1 ||
		list.SeasonFrom != nil || list.SeasonTo != nil || list.WeekdayMask != nil {
		return fmt.Errorf("inpatient price list drifted")
	}
	items, err := s.biz.contracts.ListPriceItems(ctx, rc, list.ID, "", 10)
	if err != nil {
		return err
	}
	if len(items.Items) != 1 {
		return fmt.Errorf("inpatient tariff item count drifted")
	}
	item := items.Items[0]
	if item.ServiceDefinitionID == nil || *item.ServiceDefinitionID != serviceID || item.UnitType != "NIGHT" || item.PricingMethod != "UNIT" ||
		!decimalIs(item.Amount, "400") || item.MemberShareMethod != "NONE" || !item.ValidFrom.Equal(from) || item.ValidTo != nil || item.Priority != 100 ||
		item.ServiceCategoryID != nil || item.PackageDefinitionID != nil || item.LocationID != nil ||
		item.Percent != "" || item.FormulaKey != nil || item.MinAmount != "" || item.MaxAmount != "" ||
		item.MemberShareAmount != "" || item.MemberSharePercent != "" {
		return fmt.Errorf("inpatient tariff drifted")
	}
	term, err := s.biz.contracts.GetPaymentTerm(ctx, rc, versionID)
	if err != nil {
		return err
	}
	if term.Term.DueDays != 30 || term.Term.SettlementMethod != "BANK_TRANSFER" || term.Term.TaxBehaviour != "EXCLUSIVE" || !decimalIs(term.Term.VatRate, "20") || term.Term.LateFeePercent != "" {
		return fmt.Errorf("inpatient payment term drifted")
	}
	return nil
}
