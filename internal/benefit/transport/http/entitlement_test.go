package benefithttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
)

// The response shapes of the entitlement endpoints. They are declared here rather than
// taken from the generated contract types because the contract's quantities are
// json.Number and the generated struct fields are not comparable as text; asserting on
// the exact decimal string is the point of these tests.
type entitlementAccountBody struct {
	Id           uuid.UUID `json:"id"`
	EnrollmentId uuid.UUID `json:"enrollmentId"`
	PersonId     uuid.UUID `json:"personId"`
	Definition   struct {
		Code           string `json:"code"`
		UnitType       string `json:"unitType"`
		AllowOverdraft bool   `json:"allowOverdraft"`
	} `json:"definition"`
	BenefitPeriodFrom string      `json:"benefitPeriodFrom"`
	BenefitPeriodTo   *string     `json:"benefitPeriodTo"`
	TotalGranted      json.Number `json:"totalGranted"`
	Available         json.Number `json:"available"`
	Reserved          json.Number `json:"reserved"`
	Status            string      `json:"status"`
	Shared            bool        `json:"shared"`
	RowVersion        int64       `json:"rowVersion"`
}

type entitlementAccountList struct {
	Items []entitlementAccountBody `json:"items"`
}

type ledgerEntryBody struct {
	Id           uuid.UUID   `json:"id"`
	MovementType string      `json:"movementType"`
	DeltaTotal   json.Number `json:"deltaTotal"`
	ReferenceID  uuid.UUID   `json:"referenceId"`
}

type ledgerPageBody struct {
	Items      []ledgerEntryBody `json:"items"`
	NextCursor *string           `json:"nextCursor"`
}

type adjustmentBody struct {
	Id            uuid.UUID   `json:"id"`
	AccountId     uuid.UUID   `json:"accountId"`
	DeltaQuantity json.Number `json:"deltaQuantity"`
	Status        string      `json:"status"`
	RequestedBy   uuid.UUID   `json:"requestedBy"`
	DecidedBy     *uuid.UUID  `json:"decidedBy"`
	LedgerEntryId *uuid.UUID  `json:"ledgerEntryId"`
	RowVersion    int64       `json:"rowVersion"`
}

// openAccounts drives the plan and enrollment flow over HTTP and then opens the
// entitlement accounts the way the worker's outbox consumer does.
func (s *server) openAccounts(t *testing.T, code string) entitlementAccountBody {
	t.Helper()
	planID, _ := s.activePlan(t, code)
	s.publish(t, planID)

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/people/" + s.person.String() + "/enrollments",
		body: fmt.Sprintf(`{"sponsorMembershipId":%q,"planId":%q,"validFrom":"2026-03-01"}`, s.membership, planID)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create enrollment: %d %s", rec.Code, rec.Body.String())
	}
	var enrollment kapsorav1.Enrollment
	decode(t, rec, &enrollment)

	asOf := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.entitlements.EnsureAccounts(context.Background(), s.tenant, enrollment.Id, asOf); err != nil {
		t.Fatalf("ensure accounts: %v", err)
	}

	rec = s.do(call{method: http.MethodGet,
		path: "/api/v1/people/" + s.person.String() + "/entitlements?asOf=2026-06-01"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list entitlements: %d %s", rec.Code, rec.Body.String())
	}
	var list entitlementAccountList
	decode(t, rec, &list)
	if len(list.Items) != 2 {
		t.Fatalf("accounts = %d, want 2 (DENTAL, CHECKUP)", len(list.Items))
	}
	for _, a := range list.Items {
		if a.Definition.Code == "DENTAL" {
			return a
		}
	}
	t.Fatalf("DENTAL account missing from %+v", list.Items)
	return entitlementAccountBody{}
}

func TestEntitlementReadsThroughHTTP(t *testing.T) {
	s := newServer(t)
	account := s.openAccounts(t, "HTTPENT")

	// The money definition keeps its exact decimal text end to end.
	if account.TotalGranted.String() != "1500.5" || account.Available.String() != "1500.5" {
		t.Fatalf("balances = %+v", account)
	}
	if account.PersonId != s.person || account.Shared || account.Status != "OPEN" {
		t.Fatalf("account = %+v", account)
	}
	if account.BenefitPeriodFrom != "2026-01-01" || account.BenefitPeriodTo == nil || *account.BenefitPeriodTo != "2027-01-01" {
		t.Fatalf("calendar-year period = %s..%v", account.BenefitPeriodFrom, account.BenefitPeriodTo)
	}

	rec := s.do(call{method: http.MethodGet, path: "/api/v1/entitlement-accounts/" + account.Id.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("get account: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != fmt.Sprintf(`"%d"`, account.RowVersion) {
		t.Fatalf("ETag = %q, row version %d", rec.Header().Get("ETag"), account.RowVersion)
	}

	rec = s.do(call{method: http.MethodGet, path: "/api/v1/entitlement-accounts/" + account.Id.String() + "/ledger"})
	if rec.Code != http.StatusOK {
		t.Fatalf("ledger: %d %s", rec.Code, rec.Body.String())
	}
	var page ledgerPageBody
	decode(t, rec, &page)
	if len(page.Items) != 1 || page.Items[0].MovementType != "GRANT" || page.Items[0].DeltaTotal.String() != "1500.5" {
		t.Fatalf("ledger page = %+v", page.Items)
	}
	if page.NextCursor != nil {
		t.Fatalf("nextCursor = %v on a single-page ledger", *page.NextCursor)
	}

	// An unknown account and an unknown person are both 404, never a probe.
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/entitlement-accounts/" + uuid.New().String()})
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "ENTITLEMENT_ACCOUNT_NOT_FOUND" {
		t.Fatalf("unknown account = %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/entitlement-accounts/not-a-uuid/ledger"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("malformed account id = %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/people/" + uuid.New().String() + "/entitlements"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown person = %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodGet,
		path: "/api/v1/entitlement-accounts/" + account.Id.String() + "/ledger?cursor=nope"})
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "CURSOR_INVALID" {
		t.Fatalf("bad cursor = %d %s", rec.Code, rec.Body.String())
	}
}

func TestEntitlementReadsRequireEntitlementRead(t *testing.T) {
	s := newServer(t)
	account := s.openAccounts(t, "HTTPPERM")

	// program.read is not enough: balances have their own permission.
	for _, path := range []string{
		"/api/v1/people/" + s.person.String() + "/entitlements",
		"/api/v1/entitlement-accounts/" + account.Id.String(),
		"/api/v1/entitlement-accounts/" + account.Id.String() + "/ledger",
		"/api/v1/entitlement-adjustments",
	} {
		rec := s.do(call{method: http.MethodGet, path: path, perms: "program.read"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without entitlement.read = %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := s.do(call{method: http.MethodPost, perms: "entitlement.read",
		path: "/api/v1/entitlement-accounts/" + account.Id.String() + "/adjustments",
		body: `{"deltaQuantity":10,"reasonCode":"GOODWILL"}`})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("adjustment without entitlement.adjust = %d %s", rec.Code, rec.Body.String())
	}
	if len(s.denied.permissions) == 0 {
		t.Fatalf("no denial was audited")
	}
}

func TestAdjustmentMakerCheckerThroughHTTP(t *testing.T) {
	s := newServer(t)
	account := s.openAccounts(t, "HTTPADJ")
	accountPath := "/api/v1/entitlement-accounts/" + account.Id.String()

	rec := s.do(call{method: http.MethodPost, path: accountPath + "/adjustments",
		body: `{"deltaQuantity":-250.25,"reasonCode":"DATA_ENTRY_ERROR","reasonText":"Yanlış tutar girildi"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create adjustment: %d %s", rec.Code, rec.Body.String())
	}
	var adjustment adjustmentBody
	decode(t, rec, &adjustment)
	etag := rec.Header().Get("ETag")
	if adjustment.Status != "PENDING" || adjustment.DeltaQuantity.String() != "-250.25" || adjustment.LedgerEntryId != nil {
		t.Fatalf("adjustment = %+v", adjustment)
	}
	// A zero delta is a validation error, not a no-op movement.
	rec = s.do(call{method: http.MethodPost, path: accountPath + "/adjustments",
		body: `{"deltaQuantity":0,"reasonCode":"NOOP"}`})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("zero delta = %d %s", rec.Code, rec.Body.String())
	}

	approvePath := "/api/v1/entitlement-adjustments/" + adjustment.Id.String() + "/approve"
	// Step-up is required even for the requester's own attempt.
	rec = s.do(call{method: http.MethodPost, path: approvePath, ifMatch: etag})
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "STEP_UP_REQUIRED" {
		t.Fatalf("approve without step-up = %d %s", rec.Code, rec.Body.String())
	}
	// If-Match is mandatory.
	rec = s.do(call{method: http.MethodPost, path: approvePath, actor: "checker", stepUp: true})
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("approve without If-Match = %d %s", rec.Code, rec.Body.String())
	}
	// The requester may not approve their own correction.
	rec = s.do(call{method: http.MethodPost, path: approvePath, ifMatch: etag, stepUp: true})
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "MAKER_CHECKER_SAME_ACTOR" {
		t.Fatalf("self-approval = %d %s", rec.Code, rec.Body.String())
	}
	// A stale ETag is a 412.
	rec = s.do(call{method: http.MethodPost, path: approvePath, ifMatch: `"99"`, actor: "checker", stepUp: true})
	if rec.Code != http.StatusPreconditionFailed || problemOf(t, rec).Code != "ETAG_MISMATCH" {
		t.Fatalf("stale ETag = %d %s", rec.Code, rec.Body.String())
	}

	rec = s.do(call{method: http.MethodPost, path: approvePath, ifMatch: etag, actor: "checker", stepUp: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	var approved adjustmentBody
	decode(t, rec, &approved)
	if approved.Status != "APPROVED" || approved.LedgerEntryId == nil || approved.DecidedBy == nil {
		t.Fatalf("approved = %+v", approved)
	}

	// The balance moved by exactly the adjustment and the ADJUST row is on the ledger.
	rec = s.do(call{method: http.MethodGet, path: accountPath})
	var after entitlementAccountBody
	decode(t, rec, &after)
	if after.TotalGranted.String() != "1250.25" || after.Available.String() != "1250.25" {
		t.Fatalf("balances after adjustment = %+v", after)
	}
	rec = s.do(call{method: http.MethodGet, path: accountPath + "/ledger"})
	var page ledgerPageBody
	decode(t, rec, &page)
	if len(page.Items) != 2 || page.Items[0].MovementType != "ADJUST" ||
		page.Items[0].DeltaTotal.String() != "-250.25" || page.Items[0].ReferenceID != adjustment.Id {
		t.Fatalf("ledger after adjustment = %+v", page.Items)
	}

	// Deciding twice is refused, and the pending list is empty again.
	rec = s.do(call{method: http.MethodPost, path: approvePath, ifMatch: fmt.Sprintf(`"%d"`, approved.RowVersion),
		actor: "checker", stepUp: true})
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "ENTITLEMENT_ADJUSTMENT_DECIDED" {
		t.Fatalf("second approval = %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/entitlement-adjustments"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list pending: %d %s", rec.Code, rec.Body.String())
	}
	var pending struct {
		Items []adjustmentBody `json:"items"`
	}
	decode(t, rec, &pending)
	if len(pending.Items) != 0 {
		t.Fatalf("pending adjustments = %+v", pending.Items)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/entitlement-adjustments?status=APPROVED"})
	decode(t, rec, &pending)
	if len(pending.Items) != 1 || pending.Items[0].Id != adjustment.Id {
		t.Fatalf("approved adjustments = %+v", pending.Items)
	}
}

func TestAdjustmentRejectionThroughHTTP(t *testing.T) {
	s := newServer(t)
	account := s.openAccounts(t, "HTTPREJ")
	accountPath := "/api/v1/entitlement-accounts/" + account.Id.String()

	rec := s.do(call{method: http.MethodPost, path: accountPath + "/adjustments",
		body: `{"deltaQuantity":100,"reasonCode":"GOODWILL"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create adjustment: %d %s", rec.Code, rec.Body.String())
	}
	var adjustment adjustmentBody
	decode(t, rec, &adjustment)

	rec = s.do(call{method: http.MethodPost, ifMatch: rec.Header().Get("ETag"), actor: "checker", stepUp: true,
		path: "/api/v1/entitlement-adjustments/" + adjustment.Id.String() + "/reject",
		body: `{"reasonCode":"NOT_ELIGIBLE","reasonText":"Kapsam dışı"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	var rejected adjustmentBody
	decode(t, rec, &rejected)
	if rejected.Status != "REJECTED" || rejected.LedgerEntryId != nil {
		t.Fatalf("rejected = %+v", rejected)
	}

	// Nothing moved: the ledger still holds only the opening grant.
	rec = s.do(call{method: http.MethodGet, path: accountPath + "/ledger"})
	var page ledgerPageBody
	decode(t, rec, &page)
	if len(page.Items) != 1 || page.Items[0].MovementType != "GRANT" {
		t.Fatalf("ledger after rejection = %+v", page.Items)
	}
}
