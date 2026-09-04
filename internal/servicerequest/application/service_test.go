package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
	servicerequestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
)

// The fixture seeds the whole chain a submit reads: a sponsored member enrolled in a
// published plan with 300 units of the PHYSIO entitlement already granted, a provider
// organization, and a catalog definition whose code is PHYSIO — which is how a line is
// mapped onto a balance until a catalogue-to-entitlement table exists.
const (
	entitlementCode = "PHYSIO"
	serviceDateText = "2026-06-15"
)

var serviceDate = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

type fixture struct {
	h   *dbtest.Harness
	svc *application.Service

	tenant     uuid.UUID
	actor      uuid.UUID
	providerOr uuid.UUID
	otherOr    uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	definition uuid.UUID
	account    uuid.UUID
	membership uuid.UUID
	ruleSet    uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: servicerequestpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Programs: rulesapp.NewProgramCache(8),
		Now:      func() time.Time { return time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{h: h, svc: svc}
	f.seed(t)
	return f
}

func (f *fixture) seed(t *testing.T) { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := f.h
	ctx, cancel := h.Ctx()
	defer cancel()

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	f.tenant = h.CreateTenant("SERVICE_REQUEST")
	f.actor = h.CreateActor("request-clerk", "Request Clerk")
	sponsor := h.CreateTenantOrganization(f.tenant, "Request Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Request Payer", "PAYER")
	f.providerOr = h.CreateTenantOrganization(f.tenant, "Request Provider", "PROVIDER")
	f.otherOr = h.CreateTenantOrganization(f.tenant, "Another Provider", "PROVIDER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	scan(&f.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Aksoy', 'deniz aksoy') RETURNING id`, f.tenant)
	scan(&f.membership, "membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.person, sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, f.tenant)
	scan(&f.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))
		RETURNING id`, f.tenant, sponsor, payer)
	var planID, planVersion, definitionID uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3)
		RETURNING id`, f.tenant, planID, f.actor)
	scan(&definitionID, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            period_type, initial_quantity)
		VALUES ($1, $2, $3, $3, 'SESSION', 'CALENDAR_YEAR', 300) RETURNING id`,
		f.tenant, planVersion, entitlementCode)
	scan(&f.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.membership, planID)

	// The account is opened here rather than by a submit, which is the point: nothing in
	// this package may open one, because opening posts a GRANT movement onto the ledger.
	scan(&f.account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                         benefit_period, total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), 300, 300) RETURNING id`,
		f.tenant, f.enrollment, definitionID)
	h.AdminExec(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                        effective_at, delta_total, delta_available,
		                                        reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'GRANT', clock_timestamp(), 300, 300, 'ENROLLMENT', $3, 'grant:seed')`,
		f.tenant, f.account, f.enrollment)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenant)
	scan(&f.definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type, requires_provider)
		VALUES ($1, $2, $3, 'Fizyoterapi seansı', 'SESSION', 'SESSION', false) RETURNING id`,
		f.tenant, category, entitlementCode)
}

// rc is a back office clerk holding every request permission.
func (f *fixture) rc() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionRead: {}, application.PermissionCreate: {},
			application.PermissionSubmit: {}, application.PermissionReview: {},
			application.PermissionCancel: {},
		},
	}
}

// providerRC is a provider-scoped actor: its grants are bound to one organization.
func (f *fixture) providerRC(scope uuid.UUID) identity.RequestContext {
	rc := f.rc()
	rc.Scopes = []identity.Scope{{
		Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: scope, Valid: true},
	}}
	return rc
}

// request is the fixture's standard draft: one session of the seeded definition.
func (f *fixture) input() application.NewRequestInput {
	provider := f.providerOr
	return application.NewRequestInput{
		RequestType: "DIRECT_SERVICE", PersonID: f.person, ProgramID: f.program,
		EnrollmentID: f.enrollment, ProviderOrganizationID: &provider,
		ServiceDate: serviceDate, Channel: "BACKOFFICE",
		Items: []domain.ItemInput{{
			ServiceDefinitionID: f.definition.String(), RequestedQuantity: "1", UnitType: "SESSION",
		}},
	}
}

func (f *fixture) create(t *testing.T) application.RequestView {
	t.Helper()
	view, err := f.svc.Create(context.Background(), f.rc(), f.input())
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	return view
}

func (f *fixture) submit(t *testing.T, view application.RequestView) application.RequestView {
	t.Helper()
	out, err := f.svc.Submit(context.Background(), f.rc(), view.Request.ID, nil, view.Request.RowVersion)
	if err != nil {
		t.Fatalf("submit request: %v", err)
	}
	return out
}

// publishRule puts one published rule set version live over the service date. The condition
// is written against the variables the submit gate supplies.
func (f *fixture) publishRule(t *testing.T, purpose, code, condition, actions string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if f.ruleSet == uuid.Nil {
		f.ruleSet = uuid.New()
	}
	var setID uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set (tenant_id, code, name, domain_code, purpose, status)
		VALUES ($1, $2, $2, 'HEALTH', $3, 'ACTIVE') RETURNING id`,
		f.tenant, purpose+"_RULES", purpose).Scan(&setID); err != nil {
		t.Fatalf("seed rule set: %v", err)
	}
	var versionID uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no, status, valid_from,
		                                    input_schema, content_hash, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01',
		        '{"serviceDate":"timestamp","requestType":"string","itemCount":"int","totalQuantity":"string","eligible":"bool"}'::jsonb,
		        'deadbeef', clock_timestamp(), $3)
		RETURNING id`, f.tenant, setID, f.actor).Scan(&versionID); err != nil {
		t.Fatalf("seed rule version: %v", err)
	}
	f.h.AdminExec(`
		INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
		                        actions, explanation_code)
		VALUES ($1, $2, $3, $3, 10, $4, $5::jsonb, $3)`,
		f.tenant, versionID, code, condition, actions)
	return versionID
}

// autoApprove switches the tenant's review setting off, which is what makes the gate's last
// step an approval rather than a queue.
func (f *fixture) autoApprove(t *testing.T) {
	t.Helper()
	f.h.AdminExec(`
		INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
		VALUES ($1, 'service_request.review_required', '{"default": false}'::jsonb)`, f.tenant)
}

// ledgerState is every fact about the entitlement ledger this package must leave alone.
type ledgerState struct {
	entries      int
	accounts     int
	reservations int
	balances     []string
}

func (f *fixture) ledgerState(t *testing.T) ledgerState {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	out := ledgerState{}
	row := f.h.Admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM benefit.entitlement_ledger WHERE tenant_id = $1),
		       (SELECT count(*) FROM benefit.entitlement_account WHERE tenant_id = $1),
		       (SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id = $1)`, f.tenant)
	if err := row.Scan(&out.entries, &out.accounts, &out.reservations); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	rows, err := f.h.Admin.Query(ctx, `
		SELECT id::text, total_granted::text, available_quantity::text, reserved_quantity::text,
		       consumed_quantity::text, expired_quantity::text, row_version::text
		  FROM benefit.entitlement_account WHERE tenant_id = $1 ORDER BY id`, f.tenant)
	if err != nil {
		t.Fatalf("read balances: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, total, available, reserved, consumed, expired, version string
		if err := rows.Scan(&id, &total, &available, &reserved, &consumed, &expired, &version); err != nil {
			t.Fatalf("scan balance: %v", err)
		}
		out.balances = append(out.balances,
			strings.Join([]string{id, total, available, reserved, consumed, expired, version}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return out
}

func equalLedger(a, b ledgerState) bool {
	if a.entries != b.entries || a.accounts != b.accounts || a.reservations != b.reservations {
		return false
	}
	if len(a.balances) != len(b.balances) {
		return false
	}
	for i := range a.balances {
		if a.balances[i] != b.balances[i] {
			return false
		}
	}
	return true
}

func TestCreateOpensADraftWithItsFirstVersion(t *testing.T) {
	f := newFixture(t)
	view := f.create(t)

	if view.Request.Status != domain.StatusDraft {
		t.Fatalf("status = %q, want DRAFT", view.Request.Status)
	}
	if view.Request.CurrentVersionNo != 1 {
		t.Fatalf("currentVersionNo = %d, want 1", view.Request.CurrentVersionNo)
	}
	if !strings.HasPrefix(view.Request.Reference, "SR-2026") {
		t.Fatalf("reference = %q, want an SR-2026… number", view.Request.Reference)
	}
	if len(view.Items) != 1 || view.Items[0].RequestedQuantity != "1" {
		t.Fatalf("items = %+v, want one line of quantity 1", view.Items)
	}
	if view.Request.SubmittedAt != nil {
		t.Fatal("a draft has never been submitted")
	}
	versions, err := f.svc.ListVersions(context.Background(), f.rc(), view.Request.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Status != domain.VersionDraft {
		t.Fatalf("versions = %+v, want one draft", versions)
	}
}

func TestCreateRefusesAnEnrollmentThatIsNotThePersons(t *testing.T) {
	f := newFixture(t)
	in := f.input()
	in.PersonID = uuid.New()

	_, err := f.svc.Create(context.Background(), f.rc(), in)
	if !errors.Is(err, application.ErrEnrollmentMismatch) {
		t.Fatalf("error = %v, want ErrEnrollmentMismatch", err)
	}
}

func TestSubmitWithoutRulesWaitsForReviewAndNamesItsEvaluation(t *testing.T) {
	f := newFixture(t)
	submitted := f.submit(t, f.create(t))

	if submitted.Request.Status != domain.StatusPendingReview {
		t.Fatalf("status = %q, want PENDING_REVIEW", submitted.Request.Status)
	}
	if submitted.Request.EligibilityEvaluationID == nil {
		t.Fatal("the submit did not record which eligibility evaluation decided it")
	}
	if submitted.Request.SubmittedAt == nil {
		t.Fatal("a submitted request carries the moment it was submitted")
	}

	// The stored evaluation is readable and says what the resolver said.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var outcome string
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT outcome FROM benefit.eligibility_evaluation WHERE tenant_id = $1 AND id = $2`,
		f.tenant, *submitted.Request.EligibilityEvaluationID).Scan(&outcome); err != nil {
		t.Fatalf("read eligibility evaluation: %v", err)
	}
	if outcome != "ELIGIBLE" {
		t.Fatalf("eligibility outcome = %q, want ELIGIBLE", outcome)
	}
}

func TestSubmitAutoApprovesWhenTheProgramDoesNotRequireReview(t *testing.T) {
	f := newFixture(t)
	f.autoApprove(t)
	submitted := f.submit(t, f.create(t))

	if submitted.Request.Status != domain.StatusApproved {
		t.Fatalf("status = %q, want APPROVED", submitted.Request.Status)
	}
	if submitted.Request.RequiredDocumentTypes == nil || len(submitted.Request.RequiredDocumentTypes) != 0 {
		t.Fatalf("requiredDocumentTypes = %v, want an empty list: the rules were asked and required nothing",
			submitted.Request.RequiredDocumentTypes)
	}
}

func TestSubmitOfAnIneligiblePersonFailsTheGate(t *testing.T) {
	f := newFixture(t)
	view := f.create(t)
	// The membership is suspended after the draft was written, which is exactly the case
	// the gate exists for: nothing about the request changed, the person's standing did.
	f.h.AdminExec(`UPDATE party.sponsor_membership SET status = 'SUSPENDED' WHERE tenant_id = $1 AND id = $2`,
		f.tenant, f.membership)

	submitted := f.submit(t, view)
	if submitted.Request.Status != domain.StatusEligibilityFailed {
		t.Fatalf("status = %q, want ELIGIBILITY_FAILED", submitted.Request.Status)
	}
	if submitted.Request.RuleEvaluationID != nil {
		t.Fatal("the rules were evaluated for somebody who may not use the benefit at all")
	}

	// The explanation codes are stored on the version, so the answer survives the version
	// being superseded and the balances moving on.
	version, err := f.svc.GetVersion(context.Background(), f.rc(), submitted.Request.ID, 1)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	var doc struct {
		Gate struct {
			Status       string `json:"status"`
			Explanations []struct {
				Code string `json:"code"`
			} `json:"explanations"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(version.Version.Snapshot, &doc); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if doc.Gate.Status != domain.StatusEligibilityFailed {
		t.Fatalf("snapshot gate status = %q", doc.Gate.Status)
	}
	found := false
	for _, e := range doc.Gate.Explanations {
		if e.Code == "MEMBERSHIP_SUSPENDED" {
			found = true
		}
	}
	if !found {
		t.Fatalf("snapshot explanations = %+v, want MEMBERSHIP_SUSPENDED", doc.Gate.Explanations)
	}
}

func TestSubmitWithADocumentRuleWaitsForTheDocument(t *testing.T) {
	f := newFixture(t)
	f.autoApprove(t)
	f.publishRule(t, "DOCUMENT", "INVOICE_REQUIRED", "itemCount > 0",
		`[{"type":"REQUIRE_DOCUMENT","payload":{"documentTypeCode":"INVOICE"}}]`)

	submitted := f.submit(t, f.create(t))
	if submitted.Request.Status != domain.StatusPendingDocument {
		t.Fatalf("status = %q, want PENDING_DOCUMENT", submitted.Request.Status)
	}
	if len(submitted.Request.RequiredDocumentTypes) != 1 ||
		submitted.Request.RequiredDocumentTypes[0] != "INVOICE" {
		t.Fatalf("requiredDocumentTypes = %v, want [INVOICE]", submitted.Request.RequiredDocumentTypes)
	}
	if submitted.Request.RuleEvaluationID == nil {
		t.Fatal("the submit did not record which rule evaluation asked for the document")
	}
}

func TestSubmitWithAPreauthRuleWaitsForReview(t *testing.T) {
	f := newFixture(t)
	f.autoApprove(t)
	f.publishRule(t, "PREAUTH", "PREAUTH_REQUIRED", "itemCount > 0",
		`[{"type":"REQUIRE_PREAUTH","payload":{}}]`)

	submitted := f.submit(t, f.create(t))
	if submitted.Request.Status != domain.StatusPendingReview {
		t.Fatalf("status = %q, want PENDING_REVIEW", submitted.Request.Status)
	}
	if submitted.Request.RuleEvaluationID == nil {
		t.Fatal("the submit did not record which rule evaluation sent it to review")
	}
}

// TestSubmitTouchesNothingInTheLedger is the single most important property of the package:
// reserving entitlement is WP-I4-02's job, and a submit that moved a balance would decide
// something nobody asked it to decide.
func TestSubmitTouchesNothingInTheLedger(t *testing.T) {
	f := newFixture(t)
	f.autoApprove(t)
	before := f.ledgerState(t)

	view := f.submit(t, f.create(t))
	if view.Request.Status != domain.StatusApproved {
		t.Fatalf("status = %q, want APPROVED", view.Request.Status)
	}
	after := f.ledgerState(t)
	if !equalLedger(before, after) {
		t.Fatalf("a submit moved the ledger:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestReturnIsNotRejectAndOpensTheNextVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	submitted := f.submit(t, f.create(t))
	reference := submitted.Request.Reference

	returned, err := f.svc.Return(ctx, f.rc(), submitted.Request.ID, application.ReasonInput{
		ReasonCode: "MISSING_DETAIL", ReasonText: strPtr("Tarih eksik"),
		ExpectedVersion: submitted.Request.RowVersion,
	})
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if returned.Request.Status != domain.StatusDraft {
		t.Fatalf("status = %q, want DRAFT: a returned request is corrected, not refused", returned.Request.Status)
	}
	if returned.Request.Reference != reference {
		t.Fatalf("reference changed from %q to %q", reference, returned.Request.Reference)
	}
	if returned.Request.CurrentVersionNo != 2 {
		t.Fatalf("currentVersionNo = %d, want 2: the frozen version is never reopened",
			returned.Request.CurrentVersionNo)
	}
	if returned.Request.ReturnReasonCode == nil || *returned.Request.ReturnReasonCode != "MISSING_DETAIL" {
		t.Fatalf("returnReasonCode = %v, want MISSING_DETAIL", returned.Request.ReturnReasonCode)
	}
	if len(returned.Items) != 1 {
		t.Fatalf("the new draft carries %d lines, want the one that was sent back", len(returned.Items))
	}

	// Version 1 is still exactly what was submitted.
	first, err := f.svc.GetVersion(ctx, f.rc(), submitted.Request.ID, 1)
	if err != nil {
		t.Fatalf("get version 1: %v", err)
	}
	if first.Version.Status != domain.VersionSuperseded {
		t.Fatalf("version 1 status = %q, want SUPERSEDED", first.Version.Status)
	}
	if len(first.Version.Snapshot) == 0 {
		t.Fatal("version 1 lost the snapshot it was frozen with")
	}
	second, err := f.svc.GetVersion(ctx, f.rc(), submitted.Request.ID, 2)
	if err != nil {
		t.Fatalf("get version 2: %v", err)
	}
	if second.Version.ReturnReasonCode == nil || *second.Version.ReturnReasonCode != "MISSING_DETAIL" {
		t.Fatalf("the reason it was sent back is not on version 2: %+v", second.Version)
	}

	// The corrected request is submitted again, and version 1 is still readable as it was.
	resubmitted, err := f.svc.Submit(ctx, f.rc(), returned.Request.ID, nil, returned.Request.RowVersion)
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if resubmitted.Request.CurrentVersionNo != 2 {
		t.Fatalf("currentVersionNo = %d after the second submit, want 2", resubmitted.Request.CurrentVersionNo)
	}
	again, err := f.svc.GetVersion(ctx, f.rc(), submitted.Request.ID, 1)
	if err != nil {
		t.Fatalf("get version 1 after resubmit: %v", err)
	}
	if string(again.Version.Snapshot) != string(first.Version.Snapshot) {
		t.Fatal("version 1 changed after version 2 was submitted")
	}
}

func TestRejectIsFinalAndOnlyItMayBeSuperseded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	submitted := f.submit(t, f.create(t))

	rejected, err := f.svc.Reject(ctx, f.rc(), submitted.Request.ID, application.ReasonInput{
		ReasonCode: "NOT_COVERED", ExpectedVersion: submitted.Request.RowVersion,
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.Request.Status != domain.StatusRejected {
		t.Fatalf("status = %q, want REJECTED", rejected.Request.Status)
	}
	if rejected.Request.ClosedAt == nil {
		t.Fatal("a rejected request is closed")
	}
	if rejected.Request.RejectReasonCode == nil || *rejected.Request.RejectReasonCode != "NOT_COVERED" {
		t.Fatalf("rejectReasonCode = %v", rejected.Request.RejectReasonCode)
	}

	// A rejected request cannot be returned, resubmitted or corrected.
	_, err = f.svc.Return(ctx, f.rc(), rejected.Request.ID, application.ReasonInput{
		ReasonCode: "FIX_IT", ExpectedVersion: rejected.Request.RowVersion,
	})
	if !errors.Is(err, application.ErrTransitionInvalid) {
		t.Fatalf("return of a rejected request: %v, want ErrTransitionInvalid", err)
	}

	// The new attempt is a new request naming the old one.
	in := f.input()
	in.SupersedesRequestID = &rejected.Request.ID
	next, err := f.svc.Create(ctx, f.rc(), in)
	if err != nil {
		t.Fatalf("create the superseding request: %v", err)
	}
	if next.Request.SupersedesRequestID == nil || *next.Request.SupersedesRequestID != rejected.Request.ID {
		t.Fatalf("supersedesRequestId = %v", next.Request.SupersedesRequestID)
	}
	if next.Request.Reference == rejected.Request.Reference {
		t.Fatal("the new attempt reused the refused request's number")
	}
}

func TestSupersedingALiveRequestIsRefused(t *testing.T) {
	f := newFixture(t)
	live := f.create(t)

	in := f.input()
	in.SupersedesRequestID = &live.Request.ID
	_, err := f.svc.Create(context.Background(), f.rc(), in)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if ve.Fields[0].Field != "supersedesRequestId" {
		t.Fatalf("field = %q, want supersedesRequestId", ve.Fields[0].Field)
	}
}

func TestASubmittedVersionCannotBeEdited(t *testing.T) {
	f := newFixture(t)
	submitted := f.submit(t, f.create(t))

	_, err := f.svc.ReplaceItems(context.Background(), f.rc(), submitted.Request.ID,
		[]domain.ItemInput{{
			ServiceDefinitionID: f.definition.String(), RequestedQuantity: "5", UnitType: "SESSION",
		}}, submitted.Request.RowVersion)
	if !errors.Is(err, application.ErrVersionImmutable) {
		t.Fatalf("error = %v, want ErrVersionImmutable", err)
	}

	_, err = f.svc.Patch(context.Background(), f.rc(), submitted.Request.ID, application.PatchInput{
		ServiceDate: &serviceDate, ExpectedVersion: submitted.Request.RowVersion,
	})
	if !errors.Is(err, application.ErrVersionImmutable) {
		t.Fatalf("patch error = %v, want ErrVersionImmutable", err)
	}
}

func TestEveryIllegalTransitionIsRefused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	draft := f.create(t)

	reason := application.ReasonInput{ReasonCode: "ANY", ExpectedVersion: draft.Request.RowVersion}
	decision := application.DecisionInput{ReasonCode: "ANY", ExpectedVersion: draft.Request.RowVersion}
	cases := []struct {
		name string
		run  func() error
	}{
		{"return a draft", func() error {
			_, err := f.svc.Return(ctx, f.rc(), draft.Request.ID, reason)
			return err
		}},
		{"reject a draft", func() error {
			_, err := f.svc.Reject(ctx, f.rc(), draft.Request.ID, reason)
			return err
		}},
		{"approve a draft", func() error {
			_, err := f.svc.Approve(ctx, f.rc(), draft.Request.ID, decision)
			return err
		}},
		{"partially approve a draft", func() error {
			in := decision
			in.Items = []domain.DecisionItem{{LineNo: 1, Status: domain.ItemRejected}}
			_, err := f.svc.PartiallyApprove(ctx, f.rc(), draft.Request.ID, in)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, application.ErrTransitionInvalid) {
				t.Fatalf("error = %v, want ErrTransitionInvalid", err)
			}
		})
	}

	// And a draft may be cancelled, which is the one command that does apply here.
	cancelled, err := f.svc.Cancel(ctx, f.rc(), draft.Request.ID, reason)
	if err != nil {
		t.Fatalf("cancel a draft: %v", err)
	}
	if cancelled.Request.Status != domain.StatusCancelled {
		t.Fatalf("status = %q, want CANCELLED", cancelled.Request.Status)
	}
	// Cancelling twice is not a second cancellation.
	_, err = f.svc.Cancel(ctx, f.rc(), cancelled.Request.ID, application.ReasonInput{
		ReasonCode: "ANY", ExpectedVersion: cancelled.Request.RowVersion,
	})
	if !errors.Is(err, application.ErrTransitionInvalid) {
		t.Fatalf("second cancel: %v, want ErrTransitionInvalid", err)
	}
}

func TestApproveDecidesEveryLineAndPartialApprovalRefusesAFullOne(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	submitted := f.submit(t, f.create(t))

	// A partial approval that approves everything is not a partial approval.
	_, err := f.svc.PartiallyApprove(ctx, f.rc(), submitted.Request.ID, application.DecisionInput{
		ReasonCode: "PARTIAL", ExpectedVersion: submitted.Request.RowVersion,
		Items: []domain.DecisionItem{{LineNo: 1, Status: domain.ItemApproved}},
	})
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Fields[0].Code != "NOT_PARTIAL" {
		t.Fatalf("error = %v, want a NOT_PARTIAL validation error", err)
	}

	approved, err := f.svc.Approve(ctx, f.rc(), submitted.Request.ID, application.DecisionInput{
		ReasonCode: "COVERED", ExpectedVersion: submitted.Request.RowVersion,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Request.Status != domain.StatusApproved {
		t.Fatalf("status = %q, want APPROVED", approved.Request.Status)
	}
	if approved.Request.ClosedAt != nil {
		t.Fatal("an approved request is not closed: nothing has been delivered yet")
	}
	line := approved.Items[0]
	if line.Status != domain.ItemApproved || line.ApprovedQuantity == nil || *line.ApprovedQuantity != "1" {
		t.Fatalf("line = %+v, want APPROVED for the quantity it asked for", line)
	}
}

func TestPartialApprovalReducesALine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	view, err := f.svc.Create(ctx, f.rc(), func() application.NewRequestInput {
		in := f.input()
		in.Items[0].RequestedQuantity = "4"
		return in
	}())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	submitted := f.submit(t, view)

	decided, err := f.svc.PartiallyApprove(ctx, f.rc(), submitted.Request.ID, application.DecisionInput{
		ReasonCode: "LIMIT_APPLIED", ExpectedVersion: submitted.Request.RowVersion,
		Items: []domain.DecisionItem{{
			LineNo: 1, Status: domain.ItemPartiallyApproved, ApprovedQuantity: "2",
		}},
	})
	if err != nil {
		t.Fatalf("partially approve: %v", err)
	}
	if decided.Request.Status != domain.StatusPartiallyApproved {
		t.Fatalf("status = %q, want PARTIALLY_APPROVED", decided.Request.Status)
	}
	line := decided.Items[0]
	if line.ApprovedQuantity == nil || *line.ApprovedQuantity != "2" {
		t.Fatalf("approvedQuantity = %v, want 2", line.ApprovedQuantity)
	}
	if line.RequestedQuantity != "4" {
		t.Fatalf("requestedQuantity = %q, want the 4 that was asked for", line.RequestedQuantity)
	}

	// A reviewer cannot approve more than was asked for.
	_, err = f.svc.PartiallyApprove(ctx, f.rc(), submitted.Request.ID, application.DecisionInput{
		ReasonCode: "LIMIT_APPLIED", ExpectedVersion: decided.Request.RowVersion,
		Items: []domain.DecisionItem{{
			LineNo: 1, Status: domain.ItemPartiallyApproved, ApprovedQuantity: "9",
		}},
	})
	if err == nil {
		t.Fatal("approving more than was requested was accepted")
	}
}

func TestAnotherProvidersRequestIsNotFoundRatherThanForbidden(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	view := f.create(t)

	_, err := f.svc.Get(ctx, f.providerRC(f.otherOr), view.Request.ID)
	if !errors.Is(err, application.ErrRequestNotFound) {
		t.Fatalf("error = %v, want ErrRequestNotFound: the existence of the request is itself information", err)
	}
	page, err := f.svc.List(ctx, f.providerRC(f.otherOr), application.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("another provider saw %d requests", len(page.Items))
	}
	// Its own provider sees it.
	own, err := f.svc.Get(ctx, f.providerRC(f.providerOr), view.Request.ID)
	if err != nil {
		t.Fatalf("the owning provider could not read its own request: %v", err)
	}
	if own.Request.ID != view.Request.ID {
		t.Fatal("the owning provider was given a different request")
	}
}

func TestEveryTransitionWroteItsStatusEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	submitted := f.submit(t, f.create(t))
	if _, err := f.svc.Reject(ctx, f.rc(), submitted.Request.ID, application.ReasonInput{
		ReasonCode: "NOT_COVERED", ExpectedVersion: submitted.Request.RowVersion,
	}); err != nil {
		t.Fatalf("reject: %v", err)
	}

	events, err := f.svc.GetStatusHistory(ctx, f.rc(), submitted.Request.ID)
	if err != nil {
		t.Fatalf("status history: %v", err)
	}
	want := []string{"CREATE", domain.CommandSubmit, domain.CommandGate, domain.CommandReject}
	if len(events) != len(want) {
		t.Fatalf("status events = %d, want %d: %+v", len(events), len(want), events)
	}
	for i, code := range want {
		if events[i].TransitionCode != code {
			t.Fatalf("event %d transition = %q, want %q", i, events[i].TransitionCode, code)
		}
		if events[i].ActorID == nil || *events[i].ActorID != f.actor {
			t.Fatalf("event %d has no actor", i)
		}
	}
	if events[3].ReasonCode == nil || *events[3].ReasonCode != "NOT_COVERED" {
		t.Fatalf("the rejection event carries no reason: %+v", events[3])
	}
}

func TestListFiltersAndPagesByServiceDate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.create(t)
	second := f.create(t)

	from, _ := time.Parse(time.DateOnly, serviceDateText)
	page, err := f.svc.List(ctx, f.rc(), application.ListFilter{
		Limit: 1, ServiceDateFrom: &from, ServiceDateTo: &from, Status: domain.StatusDraft,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page = %d items, next %q, want one item and a cursor", len(page.Items), page.NextCursor)
	}
	next, err := f.svc.List(ctx, f.rc(), application.ListFilter{Limit: 1, Cursor: page.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(next.Items) != 1 {
		t.Fatalf("second page = %d items, want 1", len(next.Items))
	}
	seen := map[uuid.UUID]bool{page.Items[0].Request.ID: true, next.Items[0].Request.ID: true}
	if !seen[first.Request.ID] || !seen[second.Request.ID] {
		t.Fatalf("paging did not return both requests: %v", seen)
	}

	empty, err := f.svc.List(ctx, f.rc(), application.ListFilter{Status: domain.StatusApproved})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("a status filter matched %d drafts", len(empty.Items))
	}
}

func TestPatchUpdatesTheDraftHeaderAndMovesTheETag(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	view := f.create(t)

	later := serviceDate.AddDate(0, 0, 1)
	patched, err := f.svc.Patch(ctx, f.rc(), view.Request.ID, application.PatchInput{
		ServiceDate: &later, ClearProviderOrganization: true,
		ExpectedVersion: view.Request.RowVersion,
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !patched.Request.ServiceDate.Equal(later) {
		t.Fatalf("serviceDate = %v, want %v", patched.Request.ServiceDate, later)
	}
	if patched.Request.ProviderOrganizationID != nil {
		t.Fatal("the provider was not cleared")
	}
	if patched.Request.RowVersion == view.Request.RowVersion {
		t.Fatal("the ETag did not move")
	}
	// The stale ETag is refused.
	_, err = f.svc.Patch(ctx, f.rc(), view.Request.ID, application.PatchInput{
		ServiceDate: &serviceDate, ExpectedVersion: view.Request.RowVersion,
	})
	if !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale If-Match: %v, want ErrVersionMismatch", err)
	}
}

func TestReplacingTheLinesMovesTheRequestETag(t *testing.T) {
	f := newFixture(t)
	view := f.create(t)

	replaced, err := f.svc.ReplaceItems(context.Background(), f.rc(), view.Request.ID,
		[]domain.ItemInput{
			{ServiceDefinitionID: f.definition.String(), RequestedQuantity: "2", UnitType: "SESSION"},
			{ServiceDefinitionID: f.definition.String(), RequestedQuantity: "3", UnitType: "SESSION",
				RequestedAmount: "150.5", CurrencyCode: "TRY"},
		}, view.Request.RowVersion)
	if err != nil {
		t.Fatalf("replace items: %v", err)
	}
	if len(replaced.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(replaced.Items))
	}
	if replaced.Items[1].RequestedAmount == nil || *replaced.Items[1].RequestedAmount != "150.5" {
		t.Fatalf("requestedAmount = %v, want the exact decimal 150.5", replaced.Items[1].RequestedAmount)
	}
	if replaced.Request.RowVersion == view.Request.RowVersion {
		t.Fatal("replacing the lines left the request's ETag where it was")
	}
}

func strPtr(s string) *string { return &s }

// A grant of organization scope that names no organization is the dangerous case: scopeOf
// returns an empty non-nil slice for it, and the whole boundary then rests on that slice
// reaching Postgres as an empty array rather than as NULL. If it arrived as NULL the
// `scope_ids IS NULL` branch would read "tenant-wide" and the actor with the emptiest
// possible grant would see everything. This asserts the safe reading end to end.
func TestAGrantNamingNoOrganizationSeesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	view := f.create(t)

	rc := f.rc()
	rc.Scopes = []identity.Scope{{Type: application.ScopeOrganization}}

	if _, err := f.svc.Get(ctx, rc, view.Request.ID); !errors.Is(err, application.ErrRequestNotFound) {
		t.Fatalf("error = %v, want ErrRequestNotFound: an empty grant must restrict, not widen", err)
	}
	page, err := f.svc.List(ctx, rc, application.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("an actor whose grant names no organization saw %d requests", len(page.Items))
	}
}
