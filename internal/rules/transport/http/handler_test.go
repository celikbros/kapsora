package ruleshttp_test

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
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/rules/application"
	rulespg "github.com/celikbros/kapsora/internal/rules/infrastructure/postgres"
	ruleshttp "github.com/celikbros/kapsora/internal/rules/transport/http"
)

// Test headers let each request choose its permissions and which of the two actors it is,
// which is what a maker-checker flow needs from an HTTP test.
const (
	permsHeader    = "X-Test-Permissions"
	actorHeader    = "X-Test-Actor"
	readOnly       = "rule.read"
	draftPerms     = "rule.read,rule.draft"
	allPermissions = "rule.read,rule.draft,rule.publish"
	patchType      = "application/merge-patch+json"
)

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h       *dbtest.Harness
	handler http.Handler
	denied  *denyRecorder
	tenant  uuid.UUID
	maker   uuid.UUID
	checker uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: rulespg.New(), Audit: auditpg.New(), Cursors: cursors,
		Programs: application.NewProgramCache(8),
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{
		h: h, denied: &denyRecorder{},
		tenant:  h.CreateTenant("HTTP_RULES"),
		maker:   h.CreateActor("rule-http-maker", "Rule Maker"),
		checker: h.CreateActor("rule-http-checker", "Rule Checker"),
	}

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := ruleshttp.NewHandler(svc, s.denied, logger)

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
		api.Route("/rule-sets", func(rr chi.Router) { handler.RuleSetRoutes(rr, ruleshttp.Middlewares{}) })
		api.Route("/rule-set-versions", handler.VersionRoutes)
		api.Route("/rule-evaluations", handler.EvaluationRoutes)
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

func firstFieldError(t *testing.T, r response) (field, code, message string) {
	t.Helper()
	list, ok := r.body["errors"].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("expected field errors, got %v", r.body)
	}
	first, _ := list[0].(map[string]any)
	field, _ = first["field"].(string)
	code, _ = first["code"].(string)
	message, _ = first["message"].(string)
	return field, code, message
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

func asChecker(headers map[string]string) map[string]string {
	out := map[string]string{actorHeader: "checker"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

const ruleBody = `{"items":[{"code":"INVOICE_REQUIRED","name":"Fatura zorunlu","priority":10,` +
	`"condition":"claim.amount > 100.0","explanationCode":"DOCUMENT_REQUIRED",` +
	`"actions":[{"type":"REQUIRE_DOCUMENT","payload":{"documentTypeCode":"INVOICE"}}]}]}`

const testCaseBody = `{"items":[{"code":"OVER_THRESHOLD","input":{"claim":{"amount":150}},` +
	`"expectedOutcome":"REVIEW_REQUIRED","expectedExplanations":["DOCUMENT_REQUIRED"]}]}`

// draft creates a rule set and a version with one rule and one passing case, returning the
// version id and the ETag the next command needs.
func (s *server) draft(t *testing.T, code string) (versionID, etag string) {
	t.Helper()
	res := s.do(t, http.MethodPost, "/api/v1/rule-sets", draftPerms,
		fmt.Sprintf(`{"code":%q,"name":"Kural seti","domainCode":"HEALTH","purpose":"DOCUMENT"}`, code), nil)
	if res.code != http.StatusCreated {
		t.Fatalf("create rule set: %d %v", res.code, res.body)
	}
	setID := id(t, res)

	res = s.do(t, http.MethodPost, "/api/v1/rule-sets/"+setID+"/versions", draftPerms,
		`{"validFrom":"2026-01-01","inputSchema":{"claim":"map","serviceDate":"timestamp"}}`, nil)
	if res.code != http.StatusCreated {
		t.Fatalf("create version: %d %v", res.code, res.body)
	}
	versionID = id(t, res)

	res = s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/rules", draftPerms,
		ruleBody, ifMatch(res.etag))
	if res.code != http.StatusOK {
		t.Fatalf("put rules: %d %v", res.code, res.body)
	}
	res = s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/test-cases", draftPerms,
		testCaseBody, ifMatch(res.etag))
	if res.code != http.StatusOK {
		t.Fatalf("put test cases: %d %v", res.code, res.body)
	}
	return versionID, res.etag
}

// TestPublishGateOverHTTP walks the whole flow the work package is about: a version with
// no test case cannot be submitted, one with a failing case cannot either, and the person
// who submitted it cannot publish it.
func TestPublishGateOverHTTP(t *testing.T) {
	s := newServer(t)

	res := s.do(t, http.MethodPost, "/api/v1/rule-sets", draftPerms,
		`{"code":"GATE","name":"Kural seti","domainCode":"HEALTH","purpose":"DOCUMENT"}`, nil)
	setID := id(t, res)
	res = s.do(t, http.MethodPost, "/api/v1/rule-sets/"+setID+"/versions", draftPerms,
		`{"validFrom":"2026-01-01","inputSchema":{"claim":"map"}}`, nil)
	versionID := id(t, res)
	res = s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/rules", draftPerms,
		ruleBody, ifMatch(res.etag))
	if res.code != http.StatusOK {
		t.Fatalf("put rules: %d %v", res.code, res.body)
	}

	// No test case at all.
	submit := s.do(t, http.MethodPost, "/api/v1/rule-set-versions/"+versionID+"/submit", draftPerms,
		"", ifMatch(res.etag))
	if submit.code != http.StatusUnprocessableEntity {
		t.Fatalf("submit without tests answered %d, want 422", submit.code)
	}
	field, code, _ := firstFieldError(t, submit)
	if field != "testCases" || code != "TESTS_REQUIRED" {
		t.Fatalf("submit without tests answered %s/%s, want testCases/TESTS_REQUIRED", field, code)
	}

	// A case that does not pass.
	failing := `{"items":[{"code":"SHOULD_APPROVE","input":{"claim":{"amount":150}},` +
		`"expectedOutcome":"APPROVED"}]}`
	res = s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/test-cases", draftPerms,
		failing, ifMatch(res.etag))
	if res.code != http.StatusOK {
		t.Fatalf("put failing test case: %d %v", res.code, res.body)
	}
	submit = s.do(t, http.MethodPost, "/api/v1/rule-set-versions/"+versionID+"/submit", draftPerms,
		"", ifMatch(res.etag))
	field, code, message := firstFieldError(t, submit)
	if field != "testCases" || code != "TESTS_FAILING" {
		t.Fatalf("submit with a failing test answered %s/%s, want testCases/TESTS_FAILING", field, code)
	}
	if !strings.Contains(message, "SHOULD_APPROVE") {
		t.Fatalf("refusal %q does not name the failing case", message)
	}

	// Fix the case, then submit and try to publish as the same person.
	res = s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/test-cases", draftPerms,
		testCaseBody, ifMatch(res.etag))
	submit = s.do(t, http.MethodPost, "/api/v1/rule-set-versions/"+versionID+"/submit", draftPerms,
		"", ifMatch(res.etag))
	if submit.code != http.StatusOK {
		t.Fatalf("submit answered %d %v", submit.code, submit.body)
	}

	self := s.do(t, http.MethodPost, "/api/v1/rule-set-versions/"+versionID+"/publish", allPermissions,
		"", ifMatch(submit.etag))
	if self.code != http.StatusForbidden || problemCode(t, self) != "MAKER_CHECKER_SAME_ACTOR" {
		t.Fatalf("self publish answered %d %s, want 403 MAKER_CHECKER_SAME_ACTOR",
			self.code, problemCode(t, self))
	}

	published := s.do(t, http.MethodPost, "/api/v1/rule-set-versions/"+versionID+"/publish", allPermissions,
		"", asChecker(ifMatch(submit.etag)))
	if published.code != http.StatusOK {
		t.Fatalf("publish answered %d %v", published.code, published.body)
	}
	if published.body["status"] != "PUBLISHED" {
		t.Fatalf("status is %v, want PUBLISHED", published.body["status"])
	}
	if hash, _ := published.body["contentHash"].(string); len(hash) != 64 {
		t.Fatalf("content hash is %v, want 64 hex characters", published.body["contentHash"])
	}

	// Everything about a published version is frozen.
	frozen := s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/rules", draftPerms,
		ruleBody, ifMatch(published.etag))
	if frozen.code != http.StatusConflict || problemCode(t, frozen) != "RULE_VERSION_IMMUTABLE" {
		t.Fatalf("writing a published version answered %d %s, want 409 RULE_VERSION_IMMUTABLE",
			frozen.code, problemCode(t, frozen))
	}
}

// TestPutRulesReportsTheCompilerError, with the failing rule's code and the compiler's own
// message, at the position of the rule in the submitted array.
func TestPutRulesReportsTheCompilerError(t *testing.T) {
	s := newServer(t)
	res := s.do(t, http.MethodPost, "/api/v1/rule-sets", draftPerms,
		`{"code":"COMPILE","name":"Kural seti","domainCode":"HEALTH","purpose":"DOCUMENT"}`, nil)
	setID := id(t, res)
	res = s.do(t, http.MethodPost, "/api/v1/rule-sets/"+setID+"/versions", draftPerms,
		`{"validFrom":"2026-01-01","inputSchema":{"claim":"map"}}`, nil)
	versionID := id(t, res)

	broken := `{"items":[{"code":"UNDECLARED","name":"Bilinmeyen değişken","priority":10,` +
		`"condition":"member.age > 18","explanationCode":"NOT_ELIGIBLE"}]}`
	out := s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/rules", draftPerms,
		broken, ifMatch(res.etag))
	if out.code != http.StatusUnprocessableEntity {
		t.Fatalf("a condition that does not compile answered %d, want 422", out.code)
	}
	field, code, message := firstFieldError(t, out)
	if field != "items[0].condition" || code != "RULE_COMPILE_FAILED" {
		t.Fatalf("answered %s/%s, want items[0].condition/RULE_COMPILE_FAILED", field, code)
	}
	if !strings.Contains(message, "UNDECLARED") || !strings.Contains(message, "member") {
		t.Fatalf("message %q names neither the rule nor the undeclared variable", message)
	}
}

// TestSimulateAndRunTestsAreQueries: both are reachable at the colon paths the contract
// declares, both answer without an If-Match, and neither leaves a row behind.
func TestSimulateAndRunTestsAreQueries(t *testing.T) {
	s := newServer(t)
	versionID, _ := s.draft(t, "QUERIES")

	countEvaluations := func() int {
		t.Helper()
		ctx, cancel := s.h.Ctx()
		defer cancel()
		var n int
		if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM rules.evaluation`).Scan(&n); err != nil {
			t.Fatalf("count evaluations: %v", err)
		}
		return n
	}
	before := countEvaluations()

	run := s.do(t, http.MethodPost, "/api/v1/rule-set-versions/"+versionID+"/tests:run", draftPerms, "", nil)
	if run.code != http.StatusOK {
		t.Fatalf("tests:run answered %d %v", run.code, run.body)
	}
	if run.body["passed"] != true || run.body["total"] != float64(1) {
		t.Fatalf("test run is %v, want one passing case", run.body)
	}

	sim := s.do(t, http.MethodPost, "/api/v1/rule-set-versions/"+versionID+":simulate", draftPerms,
		`{"input":{"claim":{"amount":150}}}`, nil)
	if sim.code != http.StatusOK {
		t.Fatalf("simulate answered %d %v", sim.code, sim.body)
	}
	if sim.body["outcome"] != "REVIEW_REQUIRED" {
		t.Fatalf("simulated outcome is %v, want REVIEW_REQUIRED", sim.body["outcome"])
	}
	results, _ := sim.body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("simulated trace is %v, want one line", sim.body["results"])
	}

	if after := countEvaluations(); after != before {
		t.Fatalf("the two queries wrote %d evaluation rows", after-before)
	}

	// The colon paths do not swallow the plain GET of the same version.
	get := s.do(t, http.MethodGet, "/api/v1/rule-set-versions/"+versionID, draftPerms, "", nil)
	if get.code != http.StatusOK || get.body["id"] != versionID {
		t.Fatalf("get after the colon routes answered %d %v", get.code, get.body)
	}
}

// TestReadOnlyCallerCannotSeeOrWriteADraft.
func TestReadOnlyCallerCannotSeeOrWriteADraft(t *testing.T) {
	s := newServer(t)
	versionID, etag := s.draft(t, "VISIBILITY")

	hidden := s.do(t, http.MethodGet, "/api/v1/rule-set-versions/"+versionID, readOnly, "", nil)
	if hidden.code != http.StatusNotFound || problemCode(t, hidden) != "RULE_SET_VERSION_NOT_FOUND" {
		t.Fatalf("reading a draft with rule.read answered %d %s, want 404", hidden.code, problemCode(t, hidden))
	}
	refused := s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/rules", readOnly,
		ruleBody, ifMatch(etag))
	if refused.code != http.StatusForbidden {
		t.Fatalf("writing rules with rule.read answered %d, want 403", refused.code)
	}
	if len(s.denied.permissions) == 0 || s.denied.permissions[len(s.denied.permissions)-1] != "rule.draft" {
		t.Fatalf("the denial recorded %v, want rule.draft last", s.denied.permissions)
	}
}

// TestConcurrencyPreconditions: every mutation carries If-Match, and a stale one is a 412.
func TestConcurrencyPreconditions(t *testing.T) {
	s := newServer(t)
	versionID, etag := s.draft(t, "ETAG")

	missing := s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/rules", draftPerms, ruleBody, nil)
	if missing.code != http.StatusPreconditionRequired || problemCode(t, missing) != "IF_MATCH_REQUIRED" {
		t.Fatalf("a write without If-Match answered %d %s, want 428", missing.code, problemCode(t, missing))
	}
	stale := s.do(t, http.MethodPut, "/api/v1/rule-set-versions/"+versionID+"/rules", draftPerms,
		ruleBody, ifMatch(`"1"`))
	if stale.code != http.StatusPreconditionFailed || problemCode(t, stale) != "ETAG_MISMATCH" {
		t.Fatalf("a stale If-Match answered %d %s, want 412", stale.code, problemCode(t, stale))
	}

	// A patch also insists on the merge-patch media type.
	wrongType := s.do(t, http.MethodPatch, "/api/v1/rule-set-versions/"+versionID, draftPerms,
		`{"notes":"x"}`, ifMatch(etag))
	if wrongType.code != http.StatusUnsupportedMediaType {
		t.Fatalf("a patch as application/json answered %d, want 415", wrongType.code)
	}
	patched := s.do(t, http.MethodPatch, "/api/v1/rule-set-versions/"+versionID, draftPerms,
		`{"notes":"gözden geçirildi"}`, map[string]string{"If-Match": etag, "Content-Type": patchType})
	if patched.code != http.StatusOK {
		t.Fatalf("patch answered %d %v", patched.code, patched.body)
	}
	// A field the module never rewrites is a 422 rather than a silent no-op.
	frozen := s.do(t, http.MethodPatch, "/api/v1/rule-set-versions/"+versionID, draftPerms,
		`{"status":"PUBLISHED"}`, map[string]string{"If-Match": patched.etag, "Content-Type": patchType})
	if field, code, _ := firstFieldError(t, frozen); field != "status" || code != "IMMUTABLE" {
		t.Fatalf("patching the status answered %s/%s, want status/IMMUTABLE", field, code)
	}
}

// TestRuleSetListAndPatch covers the plain CRUD surface of the set itself.
func TestRuleSetListAndPatch(t *testing.T) {
	s := newServer(t)
	created := s.do(t, http.MethodPost, "/api/v1/rule-sets", draftPerms,
		`{"code":"LIST_ME","name":"Kural seti","domainCode":"HEALTH","purpose":"DOCUMENT"}`, nil)
	setID := id(t, created)

	page := s.do(t, http.MethodGet, "/api/v1/rule-sets?purpose=DOCUMENT", readOnly, "", nil)
	items, _ := page.body["items"].([]any)
	if page.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("list answered %d with %d items, want 200 and 1", page.code, len(items))
	}
	empty := s.do(t, http.MethodGet, "/api/v1/rule-sets?purpose=PRICE", readOnly, "", nil)
	if items, _ = empty.body["items"].([]any); len(items) != 0 {
		t.Fatalf("filtering by another purpose returned %d items, want 0", len(items))
	}
	bad := s.do(t, http.MethodGet, "/api/v1/rule-sets?purpose=NONSENSE", readOnly, "", nil)
	if bad.code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown purpose answered %d, want 422", bad.code)
	}

	duplicate := s.do(t, http.MethodPost, "/api/v1/rule-sets", draftPerms,
		`{"code":"LIST_ME","name":"Kural seti","domainCode":"HEALTH","purpose":"DOCUMENT"}`, nil)
	if duplicate.code != http.StatusConflict || problemCode(t, duplicate) != "RULE_SET_CODE_TAKEN" {
		t.Fatalf("a duplicate code answered %d %s, want 409 RULE_SET_CODE_TAKEN",
			duplicate.code, problemCode(t, duplicate))
	}

	patched := s.do(t, http.MethodPatch, "/api/v1/rule-sets/"+setID, draftPerms,
		`{"status":"INACTIVE"}`, map[string]string{"If-Match": created.etag, "Content-Type": patchType})
	if patched.code != http.StatusOK || patched.body["status"] != "INACTIVE" {
		t.Fatalf("patch answered %d %v", patched.code, patched.body)
	}
	immutable := s.do(t, http.MethodPatch, "/api/v1/rule-sets/"+setID, draftPerms,
		`{"purpose":"PRICE"}`, map[string]string{"If-Match": patched.etag, "Content-Type": patchType})
	if field, code, _ := firstFieldError(t, immutable); field != "purpose" || code != "IMMUTABLE" {
		t.Fatalf("patching the purpose answered %s/%s, want purpose/IMMUTABLE", field, code)
	}
}

// TestUnknownIdsAnswerNotFound rather than leaking whether the row is missing or the id is
// malformed.
func TestUnknownIdsAnswerNotFound(t *testing.T) {
	s := newServer(t)
	for path, want := range map[string]string{
		"/api/v1/rule-sets/not-a-uuid":                   "RULE_SET_NOT_FOUND",
		"/api/v1/rule-sets/" + uuid.NewString():          "RULE_SET_NOT_FOUND",
		"/api/v1/rule-set-versions/" + uuid.NewString():  "RULE_SET_VERSION_NOT_FOUND",
		"/api/v1/rule-evaluations/" + uuid.NewString():   "RULE_EVALUATION_NOT_FOUND",
		"/api/v1/rule-evaluations/definitely-not-a-uuid": "RULE_EVALUATION_NOT_FOUND",
	} {
		res := s.do(t, http.MethodGet, path, allPermissions, "", nil)
		if res.code != http.StatusNotFound || problemCode(t, res) != want {
			t.Fatalf("GET %s answered %d %s, want 404 %s", path, res.code, problemCode(t, res), want)
		}
	}
}
