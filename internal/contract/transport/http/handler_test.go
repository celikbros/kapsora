package contracthttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	contracthttp "github.com/celikbros/kapsora/internal/contract/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Test headers let each request choose its permissions and which of the two actors it is,
// which is what a maker-checker flow needs from an HTTP test.
const (
	permsHeader    = "X-Test-Permissions"
	actorHeader    = "X-Test-Actor"
	readOnly       = "contract.read"
	managePerms    = "contract.read,contract.manage"
	allPermissions = "contract.read,contract.manage,contract.publish"
	patchType      = "application/merge-patch+json"
)

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h        *dbtest.Harness
	handler  http.Handler
	denied   *denyRecorder
	tenant   uuid.UUID
	maker    uuid.UUID
	checker  uuid.UUID
	payer    uuid.UUID
	provider uuid.UUID
	category uuid.UUID
	physio   uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: contractpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		h: h, denied: &denyRecorder{},
		tenant:  h.CreateTenant("HTTP_CONTRACT"),
		maker:   h.CreateActor("contract-http-maker", "Contract Maker"),
		checker: h.CreateActor("contract-http-checker", "Contract Checker"),
	}
	s.payer = h.CreateTenantOrganization(s.tenant, "Sponsor", "SPONSOR")
	providerOrg := h.CreateTenantOrganization(s.tenant, "Hastane", "PROVIDER")

	ctx, cancel := h.Ctx()
	defer cancel()
	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOSPITAL', 'ACTIVE') RETURNING id`, s.tenant, providerOrg).Scan(&s.provider), "provider")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant).Scan(&s.category), "category")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Fizyoterapi', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, s.category).Scan(&s.physio), "definition")

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := contracthttp.NewHandler(svc, s.denied, logger)

	// Stand-in for RequireTenantContext: the permissions and the acting user come from
	// test headers, and the step-up window is always fresh.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := s.maker
			if r.Header.Get(actorHeader) == "checker" {
				actor = s.checker
			}
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: actor},
				StepUpValid: true,
				Permissions: map[string]struct{}{},
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(fakeContext)
		api.Route("/contracts", func(rr chi.Router) { handler.ContractRoutes(rr, contracthttp.Middlewares{}) })
		api.Route("/contract-versions", handler.VersionRoutes)
		api.Route("/price-lists", handler.PriceListRoutes)
		handler.PriceRoutes(api)
	})
	s.handler = r
	return s
}

type response struct {
	code int
	etag string
	body map[string]any
}

func (s *server) do(t *testing.T, method, path, perms, body string, headers map[string]string) response {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(permsHeader, perms)
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)

	out := response{code: rec.Code, etag: rec.Header().Get("ETag")}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out.body); err != nil {
			t.Fatalf("%s %s: decode body %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return out
}

func problemCode(t *testing.T, r response) string {
	t.Helper()
	code, _ := r.body["code"].(string)
	return code
}

func firstFieldError(t *testing.T, r response) (field, code string) {
	t.Helper()
	list, ok := r.body["errors"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("expected field errors, got %v", r.body)
	}
	first, _ := list[0].(map[string]any)
	field, _ = first["field"].(string)
	code, _ = first["code"].(string)
	return field, code
}

func id(t *testing.T, r response) string {
	t.Helper()
	value, _ := r.body["id"].(string)
	if value == "" {
		t.Fatalf("response carries no id: %v", r.body)
	}
	return value
}

func ifMatch(etag string) map[string]string { return map[string]string{"If-Match": etag} }

// createContract posts one contract and returns its id and ETag.
func (s *server) createContract(t *testing.T, code string) (string, string) {
	t.Helper()
	body := fmt.Sprintf(`{"code":%q,"name":"Sözleşme","payerOrganizationId":%q,"providerProfileId":%q,"domainCode":"HEALTH"}`,
		code, s.payer, s.provider)
	res := s.do(t, http.MethodPost, "/api/v1/contracts", managePerms, body, nil)
	if res.code != http.StatusCreated {
		t.Fatalf("create contract: %d %v", res.code, res.body)
	}
	return id(t, res), res.etag
}

// draftSheet builds a contract with one draft version holding one price list and one item,
// and returns the version id and its current ETag.
func (s *server) draftSheet(t *testing.T, code string) (versionID, versionETag string) {
	t.Helper()
	contractID, _ := s.createContract(t, code)
	res := s.do(t, http.MethodPost, "/api/v1/contracts/"+contractID+"/versions", managePerms,
		`{"validFrom":"2026-01-01"}`, nil)
	if res.code != http.StatusCreated {
		t.Fatalf("create version: %d %v", res.code, res.body)
	}
	versionID, versionETag = id(t, res), res.etag

	res = s.do(t, http.MethodPut, "/api/v1/contract-versions/"+versionID+"/price-lists", managePerms,
		`{"items":[{"code":"STANDART","name":"Standart liste","priority":100}]}`, ifMatch(versionETag))
	if res.code != http.StatusOK {
		t.Fatalf("put price lists: %d %v", res.code, res.body)
	}
	lists, _ := res.body["items"].([]any)
	if len(lists) != 1 {
		t.Fatalf("stored %d price lists, want 1", len(lists))
	}
	first, _ := lists[0].(map[string]any)
	listID, _ := first["id"].(string)
	listVersion := fmt.Sprintf(`"%d"`, int(first["rowVersion"].(float64)))

	item := fmt.Sprintf(`{"items":[{"serviceDefinitionId":%q,"unitType":"SESSION","pricingMethod":"FIXED",`+
		`"amount":"250.500000","validFrom":"2026-01-01"}]}`, s.physio)
	res = s.do(t, http.MethodPut, "/api/v1/price-lists/"+listID+"/items", managePerms, item, ifMatch(listVersion))
	if res.code != http.StatusOK {
		t.Fatalf("put price items: %d %v", res.code, res.body)
	}

	// The item write touched the version, so its ETag has moved on.
	res = s.do(t, http.MethodGet, "/api/v1/contract-versions/"+versionID, managePerms, "", nil)
	if res.code != http.StatusOK {
		t.Fatalf("reload version: %d %v", res.code, res.body)
	}
	return versionID, res.etag
}

func TestCreateContractAnswersTheCreatedRow(t *testing.T) {
	s := newServer(t)
	contractID, tag := s.createContract(t, "HEALTH_2026")
	if tag == "" {
		t.Fatal("create answered no ETag")
	}

	res := s.do(t, http.MethodGet, "/api/v1/contracts/"+contractID, readOnly, "", nil)
	if res.code != http.StatusOK {
		t.Fatalf("get contract: %d %v", res.code, res.body)
	}
	if res.body["code"] != "HEALTH_2026" || res.body["status"] != "DRAFT" {
		t.Fatalf("contract %v", res.body)
	}
	if res.body["providerName"] == nil {
		t.Fatalf("contract carries no provider name: %v", res.body)
	}

	// A second contract under the same code is a conflict, not a silent second row.
	body := fmt.Sprintf(`{"code":"HEALTH_2026","name":"İkinci","payerOrganizationId":%q,"providerProfileId":%q,"domainCode":"HEALTH"}`,
		s.payer, s.provider)
	res = s.do(t, http.MethodPost, "/api/v1/contracts", managePerms, body, nil)
	if res.code != http.StatusConflict || problemCode(t, res) != "CONTRACT_CODE_TAKEN" {
		t.Fatalf("duplicate code: %d %s", res.code, problemCode(t, res))
	}
}

func TestPatchContractRefusesTheIdentifyingFields(t *testing.T) {
	s := newServer(t)
	contractID, tag := s.createContract(t, "PATCH")

	res := s.do(t, http.MethodPatch, "/api/v1/contracts/"+contractID, managePerms,
		`{"code":"YENI"}`, map[string]string{"Content-Type": patchType, "If-Match": tag})
	if res.code != http.StatusUnprocessableEntity {
		t.Fatalf("patching the code: %d %v", res.code, res.body)
	}
	if field, code := firstFieldError(t, res); field != "code" || code != "IMMUTABLE" {
		t.Fatalf("field error %s/%s, want code/IMMUTABLE", field, code)
	}

	res = s.do(t, http.MethodPatch, "/api/v1/contracts/"+contractID, managePerms,
		`{"status":"ACTIVE"}`, map[string]string{"Content-Type": patchType, "If-Match": tag})
	if res.code != http.StatusOK || res.body["status"] != "ACTIVE" {
		t.Fatalf("activating: %d %v", res.code, res.body)
	}

	// The stale ETag no longer matches.
	res = s.do(t, http.MethodPatch, "/api/v1/contracts/"+contractID, managePerms,
		`{"name":"Yeni ad"}`, map[string]string{"Content-Type": patchType, "If-Match": tag})
	if res.code != http.StatusPreconditionFailed || problemCode(t, res) != "ETAG_MISMATCH" {
		t.Fatalf("stale If-Match: %d %s", res.code, problemCode(t, res))
	}

	// A mutation without If-Match at all is refused before anything is read.
	res = s.do(t, http.MethodPatch, "/api/v1/contracts/"+contractID, managePerms,
		`{"name":"Yeni ad"}`, map[string]string{"Content-Type": patchType})
	if res.code != http.StatusPreconditionRequired || problemCode(t, res) != "IF_MATCH_REQUIRED" {
		t.Fatalf("missing If-Match: %d %s", res.code, problemCode(t, res))
	}
}

func TestSubmitRefusesAnEmptyPriceSheet(t *testing.T) {
	s := newServer(t)
	contractID, _ := s.createContract(t, "EMPTY")
	res := s.do(t, http.MethodPost, "/api/v1/contracts/"+contractID+"/versions", managePerms,
		`{"validFrom":"2026-01-01"}`, nil)
	versionID, tag := id(t, res), res.etag

	res = s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/submit", managePerms, "", ifMatch(tag))
	if res.code != http.StatusUnprocessableEntity {
		t.Fatalf("submit an empty sheet: %d %v", res.code, res.body)
	}
	if field, code := firstFieldError(t, res); field != "priceLists" || code != "REQUIRED" {
		t.Fatalf("field error %s/%s, want priceLists/REQUIRED", field, code)
	}
}

func TestPublishNeedsASecondPersonAndFreezesTheSheet(t *testing.T) {
	s := newServer(t)
	versionID, tag := s.draftSheet(t, "PUBLISH")

	res := s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/submit", managePerms, "", ifMatch(tag))
	if res.code != http.StatusOK || res.body["status"] != "UNDER_REVIEW" {
		t.Fatalf("submit: %d %v", res.code, res.body)
	}
	tag = res.etag

	// The submitter may not publish, and the refusal names the rule.
	res = s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/publish", allPermissions, "",
		map[string]string{"If-Match": tag, actorHeader: "maker"})
	if res.code != http.StatusForbidden || problemCode(t, res) != "MAKER_CHECKER_SAME_ACTOR" {
		t.Fatalf("publish by the submitter: %d %s", res.code, problemCode(t, res))
	}

	// Publishing needs contract.publish; the manage permission alone is not enough.
	res = s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/publish", managePerms, "",
		map[string]string{"If-Match": tag, actorHeader: "checker"})
	if res.code != http.StatusForbidden {
		t.Fatalf("publish without contract.publish: %d %v", res.code, res.body)
	}

	res = s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/publish", allPermissions, "",
		map[string]string{"If-Match": tag, actorHeader: "checker"})
	if res.code != http.StatusOK || res.body["status"] != "PUBLISHED" {
		t.Fatalf("publish by a second actor: %d %v", res.code, res.body)
	}
	if hash, _ := res.body["configurationHash"].(string); len(hash) != 64 {
		t.Fatalf("configuration hash %q, want 64 hex characters", hash)
	}
	tag = res.etag

	// Every write to the published version is refused with the one problem code that is
	// the point of this package.
	frozen := []struct {
		method, path, body string
		headers            map[string]string
	}{
		{http.MethodPatch, "/api/v1/contract-versions/" + versionID, `{"notes":"sonradan"}`,
			map[string]string{"Content-Type": patchType, "If-Match": tag}},
		{http.MethodPut, "/api/v1/contract-versions/" + versionID + "/price-lists",
			`{"items":[{"code":"YENI","name":"Yeni liste"}]}`, ifMatch(tag)},
		{http.MethodPut, "/api/v1/contract-versions/" + versionID + "/package-definitions",
			`{"items":[]}`, ifMatch(tag)},
		{http.MethodPut, "/api/v1/contract-versions/" + versionID + "/provider-quotas",
			`{"items":[]}`, ifMatch(tag)},
		{http.MethodPut, "/api/v1/contract-versions/" + versionID + "/payment-term",
			`{"dueDays":30,"settlementMethod":"BANK_TRANSFER","taxBehaviour":"EXEMPT"}`, ifMatch(tag)},
	}
	for _, tc := range frozen {
		res := s.do(t, tc.method, tc.path, managePerms, tc.body, tc.headers)
		if res.code != http.StatusConflict || problemCode(t, res) != "CONTRACT_VERSION_IMMUTABLE" {
			t.Fatalf("%s %s on a published version: %d %s", tc.method, tc.path, res.code, problemCode(t, res))
		}
	}

	// Retiring needs contract.publish and a reason; the version stays readable afterwards.
	res = s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/retire", allPermissions,
		`{"reasonCode":"TARIFF_WITHDRAWN"}`, map[string]string{"If-Match": tag, actorHeader: "checker"})
	if res.code != http.StatusOK || res.body["status"] != "RETIRED" {
		t.Fatalf("retire: %d %v", res.code, res.body)
	}
	if res.body["retireReasonCode"] != "TARIFF_WITHDRAWN" {
		t.Fatalf("retired without its reason: %v", res.body)
	}
}

func TestDraftPriceSheetNeedsManagePermission(t *testing.T) {
	s := newServer(t)
	versionID, _ := s.draftSheet(t, "DRAFTREAD")

	// A draft is hidden, not refused: 403 would confirm that a version exists at this id
	// and that terms are being renegotiated, which is exactly what a read-only caller
	// must not learn.
	res := s.do(t, http.MethodGet, "/api/v1/contract-versions/"+versionID, readOnly, "", nil)
	if res.code != http.StatusNotFound || problemCode(t, res) != "CONTRACT_VERSION_NOT_FOUND" {
		t.Fatalf("reading a draft with contract.read only: %d %s", res.code, problemCode(t, res))
	}
	res = s.do(t, http.MethodGet, "/api/v1/contract-versions/"+versionID, managePerms, "", nil)
	if res.code != http.StatusOK {
		t.Fatalf("reading a draft with contract.manage: %d %v", res.code, res.body)
	}
}

func TestPriceItemsCrossTheWireAsDecimalStrings(t *testing.T) {
	s := newServer(t)
	versionID, _ := s.draftSheet(t, "MONEY")

	res := s.do(t, http.MethodGet, "/api/v1/contract-versions/"+versionID, managePerms, "", nil)
	lists, _ := res.body["priceLists"].([]any)
	if len(lists) != 1 {
		t.Fatalf("version carries %d price lists, want 1", len(lists))
	}
	first, _ := lists[0].(map[string]any)
	listID, _ := first["id"].(string)

	res = s.do(t, http.MethodGet, "/api/v1/price-lists/"+listID+"/items", managePerms, "", nil)
	if res.code != http.StatusOK {
		t.Fatalf("list price items: %d %v", res.code, res.body)
	}
	items, _ := res.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("listed %d items, want 1", len(items))
	}
	item, _ := items[0].(map[string]any)
	amount, isString := item["amount"].(string)
	if !isString {
		t.Fatalf("amount came back as %T, want a decimal string", item["amount"])
	}
	if amount != "250.5" {
		t.Fatalf("amount %q, want the canonical 250.5", amount)
	}

	// A malformed amount is a field error rather than a database constraint failure.
	listVersion := fmt.Sprintf(`"%d"`, int(first["rowVersion"].(float64)))
	bad := fmt.Sprintf(`{"items":[{"serviceDefinitionId":%q,"unitType":"SESSION","pricingMethod":"FIXED",`+
		`"amount":"12.3456789","validFrom":"2026-01-01"}]}`, s.physio)
	res = s.do(t, http.MethodPut, "/api/v1/price-lists/"+listID+"/items", managePerms, bad, ifMatch(listVersion))
	if res.code != http.StatusUnprocessableEntity {
		t.Fatalf("over-scaled amount: %d %v", res.code, res.body)
	}
	if field, code := firstFieldError(t, res); field != "items[0].amount" || code != "FORMAT" {
		t.Fatalf("field error %s/%s, want items[0].amount/FORMAT", field, code)
	}
}

func TestPriceItemMustNameExactlyOneTarget(t *testing.T) {
	s := newServer(t)
	versionID, _ := s.draftSheet(t, "TARGET")
	res := s.do(t, http.MethodGet, "/api/v1/contract-versions/"+versionID, managePerms, "", nil)
	lists, _ := res.body["priceLists"].([]any)
	first, _ := lists[0].(map[string]any)
	listID, _ := first["id"].(string)
	listVersion := fmt.Sprintf(`"%d"`, int(first["rowVersion"].(float64)))

	body := `{"items":[{"unitType":"SESSION","pricingMethod":"FIXED","amount":"10","validFrom":"2026-01-01"}]}`
	res = s.do(t, http.MethodPut, "/api/v1/price-lists/"+listID+"/items", managePerms, body, ifMatch(listVersion))
	if res.code != http.StatusUnprocessableEntity {
		t.Fatalf("price item naming nothing: %d %v", res.code, res.body)
	}
	if field, code := firstFieldError(t, res); field != "items[0].serviceDefinitionId" || code != "REQUIRED" {
		t.Fatalf("field error %s/%s, want items[0].serviceDefinitionId/REQUIRED", field, code)
	}
}

func TestResolvePriceExplainsItsChoice(t *testing.T) {
	s := newServer(t)
	versionID, tag := s.draftSheet(t, "RESOLVE")

	// The contract has to be ACTIVE for its published versions to be candidates.
	contract := s.do(t, http.MethodGet, "/api/v1/contract-versions/"+versionID, managePerms, "", nil)
	contractID, _ := contract.body["contractId"].(string)
	head := s.do(t, http.MethodGet, "/api/v1/contracts/"+contractID, readOnly, "", nil)
	s.do(t, http.MethodPatch, "/api/v1/contracts/"+contractID, managePerms, `{"status":"ACTIVE"}`,
		map[string]string{"Content-Type": patchType, "If-Match": head.etag})

	res := s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/submit", managePerms, "", ifMatch(tag))
	if res.code != http.StatusOK {
		t.Fatalf("submit: %d %v", res.code, res.body)
	}
	res = s.do(t, http.MethodPost, "/api/v1/contract-versions/"+versionID+"/publish", allPermissions, "",
		map[string]string{"If-Match": res.etag, actorHeader: "checker"})
	if res.code != http.StatusOK {
		t.Fatalf("publish: %d %v", res.code, res.body)
	}

	body := fmt.Sprintf(`{"serviceDate":"2026-06-15","providerProfileId":%q,"serviceDefinitionId":%q}`,
		s.provider, s.physio)
	res = s.do(t, http.MethodPost, "/api/v1/prices:resolve", readOnly, body, nil)
	if res.code != http.StatusOK {
		t.Fatalf("resolve price: %d %v", res.code, res.body)
	}
	if res.body["outcome"] != "MATCHED" {
		t.Fatalf("outcome %v, want MATCHED", res.body["outcome"])
	}
	winner, _ := res.body["winner"].(map[string]any)
	if winner == nil {
		t.Fatalf("no winner in %v", res.body)
	}
	if winner["amount"] != "250.5" || winner["matchedVia"] != "DEFINITION" {
		t.Fatalf("winner %v", winner)
	}
	considered, _ := res.body["considered"].([]any)
	if len(considered) != 1 {
		t.Fatalf("considered %d candidates, want the one that was loaded", len(considered))
	}

	// An unknown service is a field error rather than an empty answer that looks like a
	// configured "no price".
	body = fmt.Sprintf(`{"serviceDate":"2026-06-15","providerProfileId":%q,"serviceDefinitionId":%q}`,
		s.provider, uuid.New())
	res = s.do(t, http.MethodPost, "/api/v1/prices:resolve", readOnly, body, nil)
	if res.code != http.StatusUnprocessableEntity {
		t.Fatalf("resolve for an unknown service: %d %v", res.code, res.body)
	}
	if field, code := firstFieldError(t, res); field != "serviceDefinitionId" || code != "NOT_FOUND" {
		t.Fatalf("field error %s/%s, want serviceDefinitionId/NOT_FOUND", field, code)
	}
}

func TestPaymentTermAndQuotaSetsAreWrittenUnderTheVersionETag(t *testing.T) {
	s := newServer(t)
	versionID, tag := s.draftSheet(t, "TERMS")

	res := s.do(t, http.MethodPut, "/api/v1/contract-versions/"+versionID+"/payment-term", managePerms,
		`{"dueDays":45,"settlementMethod":"BANK_TRANSFER","taxBehaviour":"EXCLUSIVE","vatRate":"20"}`, ifMatch(tag))
	if res.code != http.StatusOK {
		t.Fatalf("put payment term: %d %v", res.code, res.body)
	}
	if res.body["vatRate"] != "20" {
		t.Fatalf("vat rate %v, want the decimal string 20", res.body["vatRate"])
	}
	tag = res.etag

	// EXCLUSIVE without a VAT rate is refused by the domain, not by the column CHECK.
	res = s.do(t, http.MethodPut, "/api/v1/contract-versions/"+versionID+"/payment-term", managePerms,
		`{"dueDays":45,"settlementMethod":"BANK_TRANSFER","taxBehaviour":"EXCLUSIVE"}`, ifMatch(tag))
	if res.code != http.StatusUnprocessableEntity {
		t.Fatalf("payment term without a VAT rate: %d %v", res.code, res.body)
	}
	if field, code := firstFieldError(t, res); field != "vatRate" || code != "REQUIRED" {
		t.Fatalf("field error %s/%s, want vatRate/REQUIRED", field, code)
	}

	res = s.do(t, http.MethodPut, "/api/v1/contract-versions/"+versionID+"/provider-quotas", managePerms,
		`{"items":[{"periodType":"YEAR","periodFrom":"2026-01-01","periodTo":"2027-01-01","capacity":"100"}]}`,
		ifMatch(tag))
	if res.code != http.StatusOK {
		t.Fatalf("put quotas: %d %v", res.code, res.body)
	}
	items, _ := res.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("stored %d quotas, want 1", len(items))
	}
	quota, _ := items[0].(map[string]any)
	if quota["capacity"] != "100" || quota["consumed"] != "0" {
		t.Fatalf("quota %v, want capacity 100 and nothing consumed", quota)
	}
}

func TestReadingWithoutPermissionIsDeniedAndRecorded(t *testing.T) {
	s := newServer(t)
	res := s.do(t, http.MethodGet, "/api/v1/contracts", "", "", nil)
	if res.code != http.StatusForbidden {
		t.Fatalf("listing contracts without permission: %d %v", res.code, res.body)
	}
	if len(s.denied.permissions) != 1 || s.denied.permissions[0] != "contract.read" {
		t.Fatalf("denied permissions %v, want one contract.read", s.denied.permissions)
	}
}
