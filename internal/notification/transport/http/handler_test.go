package notificationhttp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	notificationpg "github.com/celikbros/kapsora/internal/notification/infrastructure/postgres"
	notificationhttp "github.com/celikbros/kapsora/internal/notification/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Test headers let each request choose its permissions, which is the whole question these
// routes answer differently: writing the message everybody gets and reading what one
// person was actually sent are separate grants.
const (
	permsHeader   = "X-Test-Permissions"
	manageAndRead = "notification.manage,notification.read"
	readOnly      = "notification.read"
	nothing       = "member.read"
)

var testNow = time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)

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

	tenant    uuid.UUID
	actor     uuid.UUID
	recipient uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: notificationpg.New(nil), Audit: auditpg.New(), Cursors: cursors,
		LinkBase: "https://kapsora.example",
		Now:      func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &server{h: h, svc: svc, denied: &denyRecorder{}}
	s.tenant = h.CreateTenant("HTTP_NOTIF")
	s.actor = h.CreateActor("notification-http-admin", "Notification Admin")
	s.recipient = h.CreateActor("notification-http-member", "Uye")
	h.CreateMembership(s.tenant, s.recipient)

	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := notificationhttp.NewHandler(svc, s.denied, logger)

	// Stand-in for RequireTenantContext: the permissions come from a test header.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: s.actor},
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
		api.Route("/notification-templates", func(rr chi.Router) {
			handler.TemplateRoutes(rr, notificationhttp.Middlewares{})
		})
		api.Route("/notification-messages", func(rr chi.Router) {
			handler.MessageRoutes(rr, notificationhttp.Middlewares{})
		})
		api.Route("/notification-preferences", func(rr chi.Router) {
			handler.PreferenceRoutes(rr, notificationhttp.Middlewares{})
		})
	})
	s.handler = r
	return s
}

type request struct {
	method, path, body string
	permissions        string
	ifMatch            string
}

func (s *server) do(t *testing.T, req request) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(req.method, req.path, strings.NewReader(req.body))
	if req.permissions == "" {
		req.permissions = manageAndRead
	}
	r.Header.Set(permsHeader, req.permissions)
	if req.body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if req.ifMatch != "" {
		r.Header.Set("If-Match", req.ifMatch)
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

func problemCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code   string `json:"code"`
		Errors []struct {
			Field, Code string
		} `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %s: %v", w.Body.String(), err)
	}
	return p.Code
}

const draftBody = `{
  "eventCode": "authorization.approved",
  "channel": "EMAIL",
  "locale": "tr-TR",
  "subject": "{{reference_no}} numaralı başvurunuz",
  "body": "Sayın {{given_name}}, {{reference_no}} numaralı başvurunuz onaylandı.",
  "declaredVariables": ["given_name", "reference_no"]
}`

// TestTemplateLifecycleOverHTTP walks a template from a draft to the published version
// that renders an event, and asserts the two things the contract promises about it: the
// ETag a publish needs, and one published version per slot.
func TestTemplateLifecycleOverHTTP(t *testing.T) {
	s := newServer(t)

	created := s.do(t, request{method: http.MethodPost, path: "/api/v1/notification-templates", body: draftBody})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	type template struct {
		Id                string   `json:"id"`
		Status            string   `json:"status"`
		VersionNo         int      `json:"versionNo"`
		RowVersion        int64    `json:"rowVersion"`
		DeclaredVariables []string `json:"declaredVariables"`
	}
	draft := decode[template](t, created)
	if draft.Status != domain.TemplateDraft || draft.VersionNo != 1 {
		t.Fatalf("draft = %+v", draft)
	}
	if created.Header().Get("ETag") != fmt.Sprintf(`"%d"`, draft.RowVersion) {
		t.Fatalf("ETag = %q, want the row version", created.Header().Get("ETag"))
	}

	// Publishing without an If-Match is refused: the version is what stops two people
	// publishing different drafts into one slot.
	missing := s.do(t, request{
		method: http.MethodPost, path: "/api/v1/notification-templates/" + draft.Id + "/publish",
	})
	if missing.Code != http.StatusPreconditionRequired || problemCode(t, missing) != "IF_MATCH_REQUIRED" {
		t.Fatalf("publish with no If-Match = %d %s", missing.Code, missing.Body)
	}
	stale := s.do(t, request{
		method: http.MethodPost, path: "/api/v1/notification-templates/" + draft.Id + "/publish",
		ifMatch: `"999"`,
	})
	if stale.Code != http.StatusPreconditionFailed || problemCode(t, stale) != "ETAG_MISMATCH" {
		t.Fatalf("publish with a stale If-Match = %d %s", stale.Code, stale.Body)
	}

	published := s.do(t, request{
		method: http.MethodPost, path: "/api/v1/notification-templates/" + draft.Id + "/publish",
		ifMatch: fmt.Sprintf(`"%d"`, draft.RowVersion),
	})
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", published.Code, published.Body)
	}
	if decode[template](t, published).Status != domain.TemplatePublished {
		t.Fatalf("status after publish = %s", decode[template](t, published).Status)
	}

	// A published template cannot be published again.
	again := s.do(t, request{
		method: http.MethodPost, path: "/api/v1/notification-templates/" + draft.Id + "/publish",
		ifMatch: fmt.Sprintf(`"%d"`, decode[template](t, published).RowVersion),
	})
	if again.Code != http.StatusConflict || problemCode(t, again) != "NOTIFICATION_TEMPLATE_NOT_DRAFT" {
		t.Fatalf("publishing twice = %d %s", again.Code, again.Body)
	}

	list := s.do(t, request{method: http.MethodGet, path: "/api/v1/notification-templates?status=PUBLISHED"})
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	page := decode[struct {
		Items []template `json:"items"`
	}](t, list)
	if len(page.Items) != 1 {
		t.Fatalf("%d published templates, want one", len(page.Items))
	}

	// An unknown id is a 404 and so is a malformed one: which of the two it was is not
	// something the caller needs to know.
	for _, path := range []string{
		"/api/v1/notification-templates/" + uuid.NewString(),
		"/api/v1/notification-templates/not-a-uuid",
	} {
		w := s.do(t, request{method: http.MethodGet, path: path})
		if w.Code != http.StatusNotFound || problemCode(t, w) != "NOTIFICATION_TEMPLATE_NOT_FOUND" {
			t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
		}
	}
}

// TestTemplateWithUnsafeVariablesIsRefusedOverHTTP: the contract's enum keeps a diagnosis
// out of declaredVariables, and the handler refuses one that gets past it anyway — which is
// what a client sending raw JSON does.
func TestTemplateWithUnsafeVariablesIsRefusedOverHTTP(t *testing.T) {
	s := newServer(t)
	for _, body := range []string{
		`{"eventCode":"authorization.approved","channel":"EMAIL","locale":"tr-TR",
		  "subject":"Konu","body":"Tanı: {{diagnosis}}","declaredVariables":["diagnosis"]}`,
		`{"eventCode":"authorization.approved","channel":"EMAIL","locale":"tr-TR",
		  "subject":"Konu","body":"Kimlik numaranız 10000000146","declaredVariables":[]}`,
		`{"eventCode":"authorization.approved","channel":"EMAIL","locale":"tr-TR",
		  "subject":"Konu","body":"Sayın {{given_name}}","declaredVariables":[]}`,
	} {
		w := s.do(t, request{method: http.MethodPost, path: "/api/v1/notification-templates", body: body})
		if w.Code != http.StatusUnprocessableEntity || problemCode(t, w) != "VALIDATION_FAILED" {
			t.Fatalf("create with %s = %d %s", body, w.Code, w.Body)
		}
	}
}

// TestReadingAndWritingAreSeparateGrants: notification.read opens the log, and only
// notification.manage writes a template or resends a message.
func TestReadingAndWritingAreSeparateGrants(t *testing.T) {
	s := newServer(t)

	for _, c := range []struct {
		name        string
		req         request
		wantStatus  int
		wantDenials int
	}{
		{"reading templates with read only", request{
			method: http.MethodGet, path: "/api/v1/notification-templates", permissions: readOnly,
		}, http.StatusOK, 0},
		{"reading the message log with read only", request{
			method: http.MethodGet, path: "/api/v1/notification-messages", permissions: readOnly,
		}, http.StatusOK, 0},
		{"writing a template with read only", request{
			method: http.MethodPost, path: "/api/v1/notification-templates",
			body: draftBody, permissions: readOnly,
		}, http.StatusForbidden, 1},
		{"writing preferences with read only", request{
			method: http.MethodPut, path: "/api/v1/notification-preferences",
			body:        `{"recipientType":"ACTOR","recipientId":"` + uuid.NewString() + `","preferences":[]}`,
			permissions: readOnly,
		}, http.StatusForbidden, 1},
		{"reading the log with neither grant", request{
			method: http.MethodGet, path: "/api/v1/notification-messages", permissions: nothing,
		}, http.StatusForbidden, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(s.denied.permissions)
			w := s.do(t, c.req)
			if w.Code != c.wantStatus {
				t.Fatalf("%s %s = %d %s", c.req.method, c.req.path, w.Code, w.Body)
			}
			if got := len(s.denied.permissions) - before; got != c.wantDenials {
				t.Fatalf("%d denials audited, want %d", got, c.wantDenials)
			}
		})
	}

	// A refused read is audited against the permission the endpoint is really about.
	if len(s.denied.permissions) == 0 {
		t.Fatal("no denial was audited at all")
	}
	last := s.denied.permissions[len(s.denied.permissions)-1]
	if last != application.PermissionRead {
		t.Fatalf("the message log denial was audited against %q, want notification.read", last)
	}
}

// TestPreferencesAreReplacedWholesale: a merge would leave behind a channel the person
// believed they had turned off.
func TestPreferencesAreReplacedWholesale(t *testing.T) {
	s := newServer(t)
	recipient := s.recipient.String()
	path := "/api/v1/notification-preferences?recipientType=ACTOR&recipientId=" + recipient

	put := func(t *testing.T, preferences string) *httptest.ResponseRecorder {
		t.Helper()
		return s.do(t, request{
			method: http.MethodPut, path: "/api/v1/notification-preferences",
			body: `{"recipientType":"ACTOR","recipientId":"` + recipient + `","preferences":` + preferences + `}`,
		})
	}
	type preference struct {
		Channel         string  `json:"channel"`
		EventCode       *string `json:"eventCode"`
		Enabled         bool    `json:"enabled"`
		QuietHoursStart *string `json:"quietHoursStart"`
		QuietHoursEnd   *string `json:"quietHoursEnd"`
		Timezone        string  `json:"timezone"`
	}
	type list struct {
		Items []preference `json:"items"`
	}

	first := put(t, `[
	  {"channel":"EMAIL","enabled":false},
	  {"channel":"SMS","enabled":true,"quietHoursStart":"22:00","quietHoursEnd":"08:00","timezone":"Europe/Istanbul"}
	]`)
	if first.Code != http.StatusOK {
		t.Fatalf("put = %d %s", first.Code, first.Body)
	}
	if got := len(decode[list](t, first).Items); got != 2 {
		t.Fatalf("%d preferences written, want two", got)
	}

	// The replace drops what the second body does not mention.
	second := put(t, `[{"channel":"EMAIL","enabled":true}]`)
	if second.Code != http.StatusOK {
		t.Fatalf("second put = %d %s", second.Code, second.Body)
	}
	read := s.do(t, request{method: http.MethodGet, path: path})
	if read.Code != http.StatusOK {
		t.Fatalf("get = %d %s", read.Code, read.Body)
	}
	items := decode[list](t, read).Items
	if len(items) != 1 || items[0].Channel != "EMAIL" || !items[0].Enabled {
		t.Fatalf("preferences after the replace = %+v", items)
	}

	// Two rows about the same channel are refused by name rather than by a unique
	// violation the caller cannot act on.
	clash := put(t, `[{"channel":"EMAIL","enabled":true},{"channel":"EMAIL","enabled":false}]`)
	if clash.Code != http.StatusUnprocessableEntity || problemCode(t, clash) != "VALIDATION_FAILED" {
		t.Fatalf("two preferences for one channel = %d %s", clash.Code, clash.Body)
	}

	// So are a half window, a zero length one and a zone nobody can resolve.
	for _, preferences := range []string{
		`[{"channel":"SMS","enabled":true,"quietHoursStart":"22:00"}]`,
		`[{"channel":"SMS","enabled":true,"quietHoursStart":"22:00","quietHoursEnd":"22:00"}]`,
		`[{"channel":"SMS","enabled":true,"timezone":"Europe/Istanbull"}]`,
		`[{"channel":"SMS","enabled":true,"quietHoursStart":"gece","quietHoursEnd":"08:00"}]`,
	} {
		w := put(t, preferences)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("put %s = %d %s", preferences, w.Code, w.Body)
		}
	}

	// A read that names no recipient is a field error rather than an empty list somebody
	// might read as "this person has no preferences".
	bare := s.do(t, request{method: http.MethodGet, path: "/api/v1/notification-preferences?recipientType=ACTOR"})
	if bare.Code != http.StatusUnprocessableEntity {
		t.Fatalf("get with no recipient = %d %s", bare.Code, bare.Body)
	}
}

// TestResendingSomethingUnsendableIsRefusedOverHTTP: the two rules the resend has, seen
// from the outside.
func TestResendingSomethingUnsendableIsRefusedOverHTTP(t *testing.T) {
	s := newServer(t)
	w := s.do(t, request{
		method: http.MethodPost,
		path:   "/api/v1/notification-messages/" + uuid.NewString() + "/resend",
	})
	if w.Code != http.StatusNotFound || problemCode(t, w) != "NOTIFICATION_MESSAGE_NOT_FOUND" {
		t.Fatalf("resending an unknown message = %d %s", w.Code, w.Body)
	}

	// A suppressed message has no body to send. It is written straight through the
	// repository, because the only way to produce one through the service is to run the
	// worker, and this test is about the HTTP answer.
	s.h.AdminExec(`
		INSERT INTO notification.message (tenant_id, event_code, recipient_type, recipient_id,
		                                  channel, locale, safe_variables, status, suppressed_reason)
		VALUES ($1, 'authorization.approved', 'ACTOR', $2, 'EMAIL', 'tr-TR',
		        '{"given_name":"Ayşe"}'::jsonb, 'SUPPRESSED', 'PREFERENCE_DISABLED')`,
		s.tenant, s.recipient)

	list := s.do(t, request{method: http.MethodGet, path: "/api/v1/notification-messages?status=SUPPRESSED"})
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	page := decode[struct {
		Items []struct {
			Id               string  `json:"id"`
			Status           string  `json:"status"`
			SuppressedReason *string `json:"suppressedReason"`
		} `json:"items"`
	}](t, list)
	if len(page.Items) != 1 {
		t.Fatalf("%d suppressed messages, want one", len(page.Items))
	}
	// The reason is on the wire: an operator asked "was this member told" gets the answer
	// and the reason in the same row.
	if page.Items[0].SuppressedReason == nil || *page.Items[0].SuppressedReason != domain.SuppressedPreferenceDisabled {
		t.Fatalf("the suppression reason is %v", page.Items[0].SuppressedReason)
	}

	resend := s.do(t, request{
		method: http.MethodPost,
		path:   "/api/v1/notification-messages/" + page.Items[0].Id + "/resend",
	})
	if resend.Code != http.StatusConflict || problemCode(t, resend) != "NOTIFICATION_MESSAGE_NOT_RESENDABLE" {
		t.Fatalf("resending a suppressed message = %d %s", resend.Code, resend.Body)
	}
}
