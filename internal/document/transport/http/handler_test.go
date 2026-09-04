package documenthttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	documenthttp "github.com/celikbros/kapsora/internal/document/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/antivirus"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// permsHeader lets each request choose what the caller holds, which is what a link's own
// permission and a plain document read need from an HTTP test.
const permsHeader = "X-Test-Permissions"

const allPermissions = "document.upload,document.read,document.link,document.legal_hold.manage"

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

// scanner always clears; the verdicts themselves are exercised in the application tests.
type cleanScanner struct{}

func (cleanScanner) Scan(_ context.Context, body io.Reader) (antivirus.Result, error) {
	if _, err := io.Copy(io.Discard, body); err != nil {
		return antivirus.Result{Outcome: antivirus.OutcomeError}, err
	}
	return antivirus.Result{Outcome: antivirus.OutcomeClean, Engine: "test", SignatureVersion: "1"}, nil
}

func (cleanScanner) Ping(context.Context) error { return nil }

type server struct {
	h       *dbtest.Harness
	svc     *application.Service
	store   objectstore.Store
	memory  *objectstore.Memory
	handler http.Handler
	denied  *denyRecorder

	tenant uuid.UUID
	actor  uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}

	s := &server{h: h, denied: &denyRecorder{}}
	// The HTTP layer is about status codes and bodies, so the in-memory store is the right
	// one here: the live store is exercised by the pipeline tests.
	s.memory = objectstore.NewMemory()
	s.store = s.memory

	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: documentpg.New(), Store: s.store, Scanner: cleanScanner{},
		Audit: auditpg.New(), Cursors: cursors,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Storage: application.Storage{
			QuarantineBucket: "quarantine", SecureBucket: "secure",
			UploadTTL: time.Minute, DownloadTTL: time.Minute,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.svc = svc
	s.tenant = h.CreateTenant("HTTP_DOC")
	s.actor = h.CreateActor("document-http-clerk", "Document Clerk")
	h.CreateMembership(s.tenant, s.actor)

	handler := documenthttp.NewHandler(svc, s.denied, slog.New(slog.NewTextHandler(io.Discard, nil)))

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

	router := chi.NewRouter()
	router.Use(fakeContext)
	router.Route("/api/v1/documents", func(r chi.Router) {
		handler.DocumentRoutes(r, documenthttp.Middlewares{})
	})
	router.Route("/api/v1/legal-holds", func(r chi.Router) {
		handler.LegalHoldRoutes(r, documenthttp.Middlewares{})
	})
	s.handler = router
	return s
}

func (s *server) do(t *testing.T, method, path, permissions string, body any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(permsHeader, permissions)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

type documentBody struct {
	Id           uuid.UUID `json:"id"`
	Bucket       string    `json:"bucket"`
	ScanStatus   string    `json:"scanStatus"`
	Downloadable bool      `json:"downloadable"`
	Sha256       *string   `json:"sha256"`
	Links        []struct {
		Id                 uuid.UUID `json:"id"`
		RequiredPermission *string   `json:"requiredPermission"`
	} `json:"links"`
}

type uploadBody struct {
	Document documentBody `json:"document"`
	Upload   *struct {
		Url     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
	} `json:"upload"`
}

type problemBody struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
}

// uploadThrough runs createUpload, writes the bytes and completes, all over HTTP.
func (s *server) uploadThrough(t *testing.T, body []byte, classification string) documentBody {
	t.Helper()
	rec := s.do(t, http.MethodPost, "/api/v1/documents", allPermissions, map[string]any{
		"originalFilename": "fatura.pdf", "contentType": "application/pdf",
		"byteSize": len(body), "classification": classification,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create upload = %d: %s", rec.Code, rec.Body.String())
	}
	created := decode[uploadBody](t, rec)
	if created.Upload == nil || created.Upload.Method != "PUT" {
		t.Fatalf("no upload url in %s", rec.Body.String())
	}
	// The key is not in the response on purpose, so the test reads it from the row the way
	// the worker does.
	var key string
	if err := s.h.Admin.QueryRow(t.Context(),
		`SELECT object_key FROM document.object WHERE id = $1`, created.Document.Id).Scan(&key); err != nil {
		t.Fatalf("read object key: %v", err)
	}
	s.memory.Put("quarantine", key, body)

	digest := sha256.Sum256(body)
	rec = s.do(t, http.MethodPost, "/api/v1/documents/"+created.Document.Id.String()+"/complete",
		allPermissions, map[string]any{
			"sha256": hex.EncodeToString(digest[:]), "byteSize": len(body),
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("complete upload = %d: %s", rec.Code, rec.Body.String())
	}
	return decode[documentBody](t, rec)
}

// TestUploadResponseCarriesNoFileAndNoKey: the contract hands out a URL and never a body,
// and it never says where in the bucket the file is either.
func TestUploadResponseCarriesNoFileAndNoKey(t *testing.T) {
	s := newServer(t)
	rec := s.do(t, http.MethodPost, "/api/v1/documents", allPermissions, map[string]any{
		"originalFilename": "..\\..\\etc\\passwd", "contentType": "application/pdf",
		"byteSize": 12,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create upload = %d: %s", rec.Code, rec.Body.String())
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("Cache-Control = %q; a response carrying a presigned URL must not be cached", cache)
	}
	raw := rec.Body.String()
	for _, forbidden := range []string{"objectKey", "object_key"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the response names the storage key: %s", raw)
		}
	}
	// The filename is stored as a label with any directory part stripped.
	var stored string
	created := decode[uploadBody](t, rec)
	if err := s.h.Admin.QueryRow(t.Context(),
		`SELECT original_filename FROM document.object WHERE id = $1`, created.Document.Id).Scan(&stored); err != nil {
		t.Fatalf("read filename: %v", err)
	}
	if stored != "passwd" {
		t.Fatalf("stored filename = %q, want the bare name", stored)
	}
	if created.Document.Downloadable || created.Document.ScanStatus != "PENDING" {
		t.Fatalf("a reserved upload is already downloadable: %+v", created.Document)
	}
}

// TestDownloadBeforeAndAfterTheScan is the status-code half of "nothing is downloadable
// until it is scanned clean": 409 before, 200 after.
func TestDownloadBeforeAndAfterTheScan(t *testing.T) {
	s := newServer(t)
	doc := s.uploadThrough(t, []byte("fatura icerigi"), "INTERNAL")

	rec := s.do(t, http.MethodPost, "/api/v1/documents/"+doc.Id.String()+"/download",
		allPermissions, map[string]any{"reasonText": "kontrol"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("download before the scan = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "DOCUMENT_NOT_SCANNED" {
		t.Fatalf("problem code = %s, want DOCUMENT_NOT_SCANNED", code)
	}

	if _, err := s.svc.ScanObject(t.Context(), s.tenant, doc.Id); err != nil {
		t.Fatalf("scan: %v", err)
	}

	rec = s.do(t, http.MethodPost, "/api/v1/documents/"+doc.Id.String()+"/download",
		allPermissions, map[string]any{"reasonText": "kontrol"})
	if rec.Code != http.StatusOK {
		t.Fatalf("download after the scan = %d: %s", rec.Code, rec.Body.String())
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("Cache-Control = %q on a download URL", cache)
	}
	var download struct {
		Url            string `json:"url"`
		Method         string `json:"method"`
		Classification string `json:"classification"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &download); err != nil {
		t.Fatalf("decode download: %v", err)
	}
	if download.Method != "GET" || download.Url == "" || download.Classification != "INTERNAL" {
		t.Fatalf("download body = %+v", download)
	}

	// And the document now says so itself.
	rec = s.do(t, http.MethodGet, "/api/v1/documents/"+doc.Id.String(), allPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}
	got := decode[documentBody](t, rec)
	if !got.Downloadable || got.ScanStatus != "CLEAN" || got.Bucket != "secure" {
		t.Fatalf("document after the scan = %+v", got)
	}
	if got.Sha256 == nil || len(*got.Sha256) != 64 {
		t.Fatalf("digest = %v, want 64 hex characters", got.Sha256)
	}
}

// TestLinkPermissionIsAnswered403 keeps the two refusals apart: a document the caller
// cannot see is 404, and one it can see but may not open through the link is 403.
func TestLinkPermissionIsAnswered403(t *testing.T) {
	s := newServer(t)
	doc := s.uploadThrough(t, []byte("klinik ek"), "HEALTH")
	if _, err := s.svc.ScanObject(t.Context(), s.tenant, doc.Id); err != nil {
		t.Fatalf("scan: %v", err)
	}

	rec := s.do(t, http.MethodPost, "/api/v1/documents/"+doc.Id.String()+"/links",
		allPermissions, map[string]any{
			"aggregateType": "SERVICE_REQUEST", "aggregateId": uuid.NewString(),
			"documentTypeCode": "MEDICAL_REPORT", "requiredPermission": "health.clinical.read",
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("link = %d: %s", rec.Code, rec.Body.String())
	}
	link := decode[struct {
		Id uuid.UUID `json:"id"`
	}](t, rec)

	rec = s.do(t, http.MethodPost, "/api/v1/documents/"+doc.Id.String()+"/download",
		allPermissions, map[string]any{"reasonText": "deneme"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("download without the link's permission = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "DOCUMENT_LINK_PERMISSION_DENIED" {
		t.Fatalf("problem code = %s", code)
	}

	rec = s.do(t, http.MethodPost, "/api/v1/documents/"+doc.Id.String()+"/download",
		allPermissions+",health.clinical.read", map[string]any{"reasonText": "yetkili"})
	if rec.Code != http.StatusOK {
		t.Fatalf("download with the link's permission = %d: %s", rec.Code, rec.Body.String())
	}

	// Unlinking removes the narrowing, and a plain document.read caller can open it again.
	rec = s.do(t, http.MethodDelete,
		"/api/v1/documents/"+doc.Id.String()+"/links/"+link.Id.String(), allPermissions, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unlink = %d: %s", rec.Code, rec.Body.String())
	}
	rec = s.do(t, http.MethodPost, "/api/v1/documents/"+doc.Id.String()+"/download",
		allPermissions, map[string]any{"reasonText": "yeniden"})
	if rec.Code != http.StatusOK {
		t.Fatalf("download after unlinking = %d: %s", rec.Code, rec.Body.String())
	}
	// A link that is gone is not found rather than silently ignored.
	rec = s.do(t, http.MethodDelete,
		"/api/v1/documents/"+doc.Id.String()+"/links/"+link.Id.String(), allPermissions, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unlink twice = %d", rec.Code)
	}
}

// TestPermissionsAreRoutingFacts: each command answers 403 through the auditing denier when
// the caller does not hold its own permission.
func TestPermissionsAreRoutingFacts(t *testing.T) {
	s := newServer(t)
	for _, c := range []struct{ method, path, permissions, want string }{
		{http.MethodPost, "/api/v1/documents", "document.read", "document.upload"},
		{http.MethodGet, "/api/v1/documents", "document.upload", "document.read"},
		{http.MethodPost, "/api/v1/documents/" + uuid.NewString() + "/links", "document.read", "document.link"},
		{http.MethodPost, "/api/v1/legal-holds", "document.read", "document.legal_hold.manage"},
	} {
		before := len(s.denied.permissions)
		var body any
		if c.method == http.MethodPost {
			body = map[string]any{}
		}
		rec := s.do(t, c.method, c.path, c.permissions, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s without %s = %d", c.method, c.path, c.want, rec.Code)
		}
		if len(s.denied.permissions) != before+1 || s.denied.permissions[before] != c.want {
			t.Fatalf("%s %s audited %v, want a denial of %s", c.method, c.path, s.denied.permissions, c.want)
		}
	}
}

// TestLegalHoldRoundTrip: placing, the If-Match a release needs, and the refusal of a
// second release.
func TestLegalHoldRoundTrip(t *testing.T) {
	s := newServer(t)
	doc := s.uploadThrough(t, []byte("saklanacak"), "INTERNAL")

	rec := s.do(t, http.MethodPost, "/api/v1/legal-holds", allPermissions, map[string]any{
		"documentId": doc.Id, "reason": "dava dosyası",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("put legal hold = %d: %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on a created legal hold")
	}
	hold := decode[struct {
		Id         uuid.UUID `json:"id"`
		RowVersion int64     `json:"rowVersion"`
	}](t, rec)

	// A hold that names nothing is a validation error, not a row.
	rec = s.do(t, http.MethodPost, "/api/v1/legal-holds", allPermissions, map[string]any{
		"reason": "hedefsiz",
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hold with no target = %d: %s", rec.Code, rec.Body.String())
	}

	// A second active hold on the same document is a conflict.
	rec = s.do(t, http.MethodPost, "/api/v1/legal-holds", allPermissions, map[string]any{
		"documentId": doc.Id, "reason": "ikinci",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("second hold = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "LEGAL_HOLD_EXISTS" {
		t.Fatalf("problem code = %s", code)
	}

	release := "/api/v1/legal-holds/" + hold.Id.String() + "/release"
	if rec = s.do(t, http.MethodPost, release, allPermissions, nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("release with no If-Match = %d", rec.Code)
	}
	if rec = s.do(t, http.MethodPost, release, allPermissions, nil, "If-Match", `"99"`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("release with a stale If-Match = %d", rec.Code)
	}
	if rec = s.do(t, http.MethodPost, release, allPermissions, nil, "If-Match", etag); rec.Code != http.StatusOK {
		t.Fatalf("release = %d: %s", rec.Code, rec.Body.String())
	}
	released := decode[struct {
		ReleasedAt *time.Time `json:"releasedAt"`
		RowVersion int64      `json:"rowVersion"`
	}](t, rec)
	if released.ReleasedAt == nil {
		t.Fatal("the released hold has no release time")
	}
	newETag := rec.Header().Get("ETag")
	if rec = s.do(t, http.MethodPost, release, allPermissions, nil, "If-Match", newETag); rec.Code != http.StatusConflict {
		t.Fatalf("second release = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "LEGAL_HOLD_ALREADY_RELEASED" {
		t.Fatalf("problem code = %s", code)
	}
}

// TestUnknownDocumentIsNotFound: a malformed id and an unknown one answer the same way.
func TestUnknownDocumentIsNotFound(t *testing.T) {
	s := newServer(t)
	for _, path := range []string{
		"/api/v1/documents/" + uuid.NewString(),
		"/api/v1/documents/not-a-uuid",
	} {
		rec := s.do(t, http.MethodGet, path, allPermissions, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		if code := decode[problemBody](t, rec).Code; code != "DOCUMENT_NOT_FOUND" {
			t.Fatalf("problem code = %s", code)
		}
	}
}
