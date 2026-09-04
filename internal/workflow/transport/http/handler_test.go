package workflowhttp_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/workflow/application"
	workflowpg "github.com/celikbros/kapsora/internal/workflow/infrastructure/postgres"
	workflowhttp "github.com/celikbros/kapsora/internal/workflow/transport/http"
)

// Test headers let each request choose its actor, its permissions and its queue grant,
// which is what a race between two people and a queue boundary need from an HTTP test.
const (
	actorHeader    = "X-Test-Actor"
	permsHeader    = "X-Test-Permissions"
	scopeHeader    = "X-Test-Queue-Scope"
	allPermissions = "worklist.read,worklist.claim,worklist.reassign,workflow.queue.manage,workflow.policy.manage"
	readOnly       = "worklist.read"
	patchType      = "application/merge-patch+json"
)

var testNow = time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h       *dbtest.Harness
	svc     *application.Service
	handler http.Handler
	denied  *denyRecorder

	tenant uuid.UUID
	actor  uuid.UUID
	rival  uuid.UUID

	queue      uuid.UUID
	otherQueue uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: workflowpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Now: func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{h: h, svc: svc, denied: &denyRecorder{}}
	s.tenant = h.CreateTenant("HTTP_WORKFLOW")
	s.actor = h.CreateActor("workflow-http-clerk", "Workflow Clerk")
	s.rival = h.CreateActor("workflow-http-rival", "Workflow Rival")
	s.queue = s.mustQueue(t, "REVIEW", "İnceleme", 60)
	s.otherQueue = s.mustQueue(t, "OTHER", "Başka", 0)

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := workflowhttp.NewHandler(svc, s.denied, logger)

	// Stand-in for RequireTenantContext: the actor, the permissions and the queue grant
	// come from test headers, and the step-up window is always fresh.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: s.actor},
				StepUpValid: true,
				Permissions: map[string]struct{}{},
			}
			if raw := r.Header.Get(actorHeader); raw != "" {
				id, err := uuid.Parse(raw)
				if err != nil {
					t.Fatalf("bad test actor header %q", raw)
				}
				rc.Principal.ActorID = id
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			if scope := r.Header.Get(scopeHeader); scope != "" {
				id, err := uuid.Parse(scope)
				if err != nil {
					t.Fatalf("bad test scope header %q", scope)
				}
				rc.Scopes = []identity.Scope{{
					Type: application.ScopeWorkQueue, ID: uuid.NullUUID{UUID: id, Valid: true},
				}}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(fakeContext)
		api.Route("/work-queues", func(rr chi.Router) {
			handler.QueueRoutes(rr, workflowhttp.Middlewares{})
		})
		api.Route("/work-items", func(rr chi.Router) {
			handler.ItemRoutes(rr, workflowhttp.Middlewares{})
		})
		api.Route("/approval-policies", func(rr chi.Router) {
			handler.PolicyRoutes(rr, workflowhttp.Middlewares{})
		})
	})
	s.handler = r
	return s
}

func (s *server) rc() identity.RequestContext {
	return identity.RequestContext{
		TenantID: s.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: s.actor},
	}
}

func (s *server) mustQueue(t *testing.T, code, name string, sla int) uuid.UUID {
	t.Helper()
	in := application.NewQueueInput{Code: code, Name: name, DomainCode: "GENERIC"}
	if sla > 0 {
		in.SLAMinutes = &sla
	}
	record, err := s.svc.CreateQueue(t.Context(), s.rc(), in)
	if err != nil {
		t.Fatalf("create queue %s: %v", code, err)
	}
	return record.ID
}

func (s *server) raise(t *testing.T, queueID uuid.UUID, title string) application.ItemRecord {
	t.Helper()
	record, err := s.svc.Raise(t.Context(), s.rc(), application.RaiseInput{
		QueueID: queueID, AggregateType: "SERVICE_REQUEST", AggregateID: uuid.New(), Title: title,
	})
	if err != nil {
		t.Fatalf("raise: %v", err)
	}
	return record
}

type request struct {
	method, path, body string
	permissions        string
	actor              *uuid.UUID
	scope              *uuid.UUID
	ifMatch            string
	contentType        string
}

func (s *server) do(t *testing.T, req request) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if req.body == "" {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(req.body)
	}
	r := httptest.NewRequest(req.method, req.path, body)
	if req.permissions == "" {
		req.permissions = allPermissions
	}
	r.Header.Set(permsHeader, req.permissions)
	if req.actor != nil {
		r.Header.Set(actorHeader, req.actor.String())
	}
	if req.scope != nil {
		r.Header.Set(scopeHeader, req.scope.String())
	}
	if req.ifMatch != "" {
		r.Header.Set("If-Match", req.ifMatch)
	}
	if req.body != "" {
		contentType := req.contentType
		if contentType == "" {
			contentType = "application/json"
		}
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

type problemBody struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Errors []struct {
		Field string `json:"field"`
		Code  string `json:"code"`
	} `json:"errors"`
}

// TestClaimAnswersTheLoserWithTheWinnersId is the whole point of the 409: telling somebody
// the item is taken without saying by whom is what makes two people keep clicking.
func TestClaimAnswersTheLoserWithTheWinnersId(t *testing.T) {
	s := newServer(t)
	item := s.raise(t, s.queue, "İki kişinin uzandığı iş")
	path := "/api/v1/work-items/" + item.ID.String() + "/claim"

	won := s.do(t, request{method: http.MethodPost, path: path, ifMatch: `"1"`, actor: &s.actor})
	if won.Code != http.StatusOK {
		t.Fatalf("first claim = %d %s", won.Code, won.Body)
	}
	if etag := won.Header().Get("ETag"); etag != `"2"` {
		t.Fatalf("ETag = %q, want \"2\"", etag)
	}

	lost := s.do(t, request{method: http.MethodPost, path: path, ifMatch: `"1"`, actor: &s.rival})
	if lost.Code != http.StatusConflict {
		t.Fatalf("second claim = %d %s, want 409", lost.Code, lost.Body)
	}
	p := decode[problemBody](t, lost)
	if p.Code != "WORK_ITEM_ALREADY_CLAIMED" {
		t.Fatalf("code = %s, want WORK_ITEM_ALREADY_CLAIMED", p.Code)
	}
	if !strings.Contains(p.Detail, s.actor.String()) {
		t.Fatalf("detail %q does not name the winner %s", p.Detail, s.actor)
	}
}

// TestClaimNeedsAVersion: a command with no If-Match is a command that can overwrite
// whoever got there first.
func TestClaimNeedsAVersion(t *testing.T) {
	s := newServer(t)
	item := s.raise(t, s.queue, "Sürümsüz istek")
	w := s.do(t, request{
		method: http.MethodPost, path: "/api/v1/work-items/" + item.ID.String() + "/claim",
	})
	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d %s, want 428", w.Code, w.Body)
	}
	if code := decode[problemBody](t, w).Code; code != "IF_MATCH_REQUIRED" {
		t.Fatalf("code = %s, want IF_MATCH_REQUIRED", code)
	}
}

// TestQueueGrantHidesOtherQueuesWork: a caller outside the boundary is answered 404, not
// 403. That such an item exists at all is somebody else's business.
func TestQueueGrantHidesOtherQueuesWork(t *testing.T) {
	s := newServer(t)
	item := s.raise(t, s.queue, "Başka kuyruğun işi")

	w := s.do(t, request{
		method: http.MethodGet, path: "/api/v1/work-items/" + item.ID.String(),
		permissions: readOnly, scope: &s.otherQueue,
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d %s, want 404", w.Code, w.Body)
	}
	if code := decode[problemBody](t, w).Code; code != "WORK_ITEM_NOT_FOUND" {
		t.Fatalf("code = %s, want WORK_ITEM_NOT_FOUND", code)
	}

	// The same caller granted the right queue sees it.
	ok := s.do(t, request{
		method: http.MethodGet, path: "/api/v1/work-items/" + item.ID.String(),
		permissions: readOnly, scope: &s.queue,
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("in-scope read = %d %s", ok.Code, ok.Body)
	}

	// And the list drawn under the other grant is empty rather than filtered afterwards.
	page := s.do(t, request{
		method: http.MethodGet, path: "/api/v1/work-items", permissions: readOnly, scope: &s.otherQueue,
	})
	if page.Code != http.StatusOK {
		t.Fatalf("list = %d %s", page.Code, page.Body)
	}
	items := decode[struct {
		Items []json.RawMessage `json:"items"`
	}](t, page)
	if len(items.Items) != 0 {
		t.Fatalf("items = %d, want none", len(items.Items))
	}
}

// TestClaimNeedsItsOwnPermission: reading a worklist is not being allowed to take work off
// it, and the denial is audited against the permission the route is really about.
func TestClaimNeedsItsOwnPermission(t *testing.T) {
	s := newServer(t)
	item := s.raise(t, s.queue, "Yetkisiz üstlenme")
	w := s.do(t, request{
		method: http.MethodPost, path: "/api/v1/work-items/" + item.ID.String() + "/claim",
		permissions: readOnly, ifMatch: `"1"`,
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d %s, want 403", w.Code, w.Body)
	}
	if len(s.denied.permissions) != 1 || s.denied.permissions[0] != "worklist.claim" {
		t.Fatalf("denied permissions = %v, want [worklist.claim]", s.denied.permissions)
	}
}

// TestPatchWorkQueueRefusesTheImmutableFields: a field that is silently dropped is a field
// somebody will keep sending.
func TestPatchWorkQueueRefusesTheImmutableFields(t *testing.T) {
	s := newServer(t)
	path := "/api/v1/work-queues/" + s.queue.String()

	w := s.do(t, request{
		method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"1"`,
		body: `{"code":"SOMETHING_ELSE","domainCode":"HEALTH","nonsense":1}`,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d %s, want 422", w.Code, w.Body)
	}
	codes := map[string]string{}
	for _, f := range decode[problemBody](t, w).Errors {
		codes[f.Field] = f.Code
	}
	if codes["code"] != "IMMUTABLE" || codes["domainCode"] != "IMMUTABLE" {
		t.Fatalf("immutable fields = %v", codes)
	}
	if codes["nonsense"] != "UNKNOWN_FIELD" {
		t.Fatalf("unknown field = %v", codes)
	}

	// A plain JSON body is refused: the endpoint is a merge patch.
	wrong := s.do(t, request{
		method: http.MethodPatch, path: path, ifMatch: `"1"`, body: `{"name":"Yeni ad"}`,
	})
	if wrong.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d %s, want 415", wrong.Code, wrong.Body)
	}

	// Clearing the clock is an explicit null, and it is allowed.
	cleared := s.do(t, request{
		method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"1"`,
		body: `{"slaMinutes":null,"name":"Yeni ad"}`,
	})
	if cleared.Code != http.StatusOK {
		t.Fatalf("clearing the SLA = %d %s", cleared.Code, cleared.Body)
	}
	queue := decode[struct {
		Name       string `json:"name"`
		SLAMinutes *int   `json:"slaMinutes"`
	}](t, cleared)
	if queue.SLAMinutes != nil || queue.Name != "Yeni ad" {
		t.Fatalf("queue = %+v, want the clock cleared and the name changed", queue)
	}
}

// TestApprovalPolicyAmountsAreStringsOnTheWire: a band edge is exact decimal text in both
// directions, never a JSON number.
func TestApprovalPolicyAmountsAreStringsOnTheWire(t *testing.T) {
	s := newServer(t)
	w := s.do(t, request{
		method: http.MethodPut, path: "/api/v1/approval-policies",
		body: `{"actionCode":"service_request.approve","policies":[
			{"scopeCode":"STANDARD","minAmount":"0","maxAmount":"1000.500000",
			 "requiredRoleCodes":["MEDICAL_REVIEWER"],"requiredApproverCount":2,
			 "validFrom":"2026-01-01"}]}`,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body)
	}
	// The raw body, not a decoded struct: a number here would be a number on the wire.
	if !strings.Contains(w.Body.String(), `"maxAmount":"1000.5"`) {
		t.Fatalf("body %s does not carry maxAmount as an exact decimal string", w.Body)
	}
	if strings.Contains(w.Body.String(), `"maxAmount":1000`) {
		t.Fatal("an amount was serialised as a JSON number")
	}

	list := s.do(t, request{method: http.MethodGet, path: "/api/v1/approval-policies"})
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	if !strings.Contains(list.Body.String(), `"minAmount":"0"`) {
		t.Fatalf("list body %s does not carry minAmount as a string", list.Body)
	}

	// A number in the request is refused by the decoder rather than silently rounded.
	bad := s.do(t, request{
		method: http.MethodPut, path: "/api/v1/approval-policies",
		body: `{"actionCode":"service_request.approve","policies":[
			{"scopeCode":"STANDARD","maxAmount":1000.5,"validFrom":"2026-01-01"}]}`,
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("status = %d %s, want 400", bad.Code, bad.Body)
	}
}

// TestCommentCarriesItsVisibility, and the list can be narrowed to one audience so a
// provider-facing screen is not handed the internal notes by accident.
func TestCommentCarriesItsVisibility(t *testing.T) {
	s := newServer(t)
	item := s.raise(t, s.queue, "Konuşulan iş")
	base := "/api/v1/work-items/" + item.ID.String() + "/comments"

	for _, body := range []string{
		`{"visibility":"INTERNAL","body":"İç not"}`,
		`{"visibility":"PROVIDER","body":"Rapor eksik"}`,
	} {
		w := s.do(t, request{method: http.MethodPost, path: base, body: body})
		if w.Code != http.StatusCreated {
			t.Fatalf("add comment %s = %d %s", body, w.Code, w.Body)
		}
	}

	all := decode[struct {
		Items []struct {
			Visibility string `json:"visibility"`
			Body       string `json:"body"`
		} `json:"items"`
	}](t, s.do(t, request{method: http.MethodGet, path: base}))
	if len(all.Items) != 2 {
		t.Fatalf("comments = %d, want 2", len(all.Items))
	}

	providerOnly := decode[struct {
		Items []struct {
			Visibility string `json:"visibility"`
		} `json:"items"`
	}](t, s.do(t, request{method: http.MethodGet, path: base + "?visibility=PROVIDER"}))
	if len(providerOnly.Items) != 1 || providerOnly.Items[0].Visibility != "PROVIDER" {
		t.Fatalf("provider comments = %+v, want exactly the provider one", providerOnly.Items)
	}
}
