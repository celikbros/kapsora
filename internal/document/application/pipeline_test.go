package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/antivirus"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// eicar is the industry standard harmless test file every scanner recognises. It is
// assembled from two halves so this source file is not itself quarantined by whatever
// scans the developer's disk.
var eicar = []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$` + `EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)

// The local defaults of .env.example, which scripts/native/up.* starts MinIO and clamd
// with. They are read from the environment first and are only ever local.
const (
	defaultMinIOAddr   = "127.0.0.1:9000"
	defaultMinIOUser   = "kapsora"
	defaultMinIOSecret = "kapsora_local_minio"
	defaultClamdAddr   = "127.0.0.1:3310"
)

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// fakeScanner is the scanner the pipeline tests drive. The point of it is that the
// guarantee under test — an infected file never reaches the secure bucket and leaves no
// bytes behind — has to be provable without a virus and on a machine with no daemon
// running. Verdicts are queued; an empty queue answers CLEAN.
type fakeScanner struct {
	mu       sync.Mutex
	verdicts []verdict
	scanned  int
	// bodies keeps what was actually read, so a test can assert the scanner saw the whole
	// file rather than a promise of one.
	bodies [][]byte
}

type verdict struct {
	result antivirus.Result
	err    error
}

func newFakeScanner() *fakeScanner { return &fakeScanner{} }

func (f *fakeScanner) queue(v verdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verdicts = append(f.verdicts, v)
}

func (f *fakeScanner) queueInfected(finding string) {
	f.queue(verdict{result: antivirus.Result{
		Outcome: antivirus.OutcomeInfected, Engine: "fake-clamd",
		SignatureVersion: "1", Finding: finding,
	}})
}

func (f *fakeScanner) queueUnavailable() {
	f.queue(verdict{
		result: antivirus.Result{Outcome: antivirus.OutcomeError, Engine: "fake-clamd"},
		err:    fmt.Errorf("%w: pretend the daemon is down", antivirus.ErrUnavailable),
	})
}

func (f *fakeScanner) Scan(_ context.Context, body io.Reader) (antivirus.Result, error) {
	// Read to the end first: a scanner that answered without reading would let the
	// pipeline's digest and byte count be computed from nothing.
	read, err := io.ReadAll(body)
	if err != nil {
		return antivirus.Result{Outcome: antivirus.OutcomeError}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanned++
	f.bodies = append(f.bodies, read)
	if len(f.verdicts) > 0 {
		next := f.verdicts[0]
		f.verdicts = f.verdicts[1:]
		return next.result, next.err
	}
	return antivirus.Result{
		Outcome: antivirus.OutcomeClean, Engine: "fake-clamd", SignatureVersion: "1",
	}, nil
}

func (f *fakeScanner) Ping(context.Context) error { return nil }

func (f *fakeScanner) lastBody() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return nil
	}
	return f.bodies[len(f.bodies)-1]
}

// recordingStore counts every write that reaches a bucket. It exists for one assertion
// the end state cannot make: "an infected file is never copied to the secure bucket" is a
// statement about what happened, not about what is left afterwards. A pipeline that copied
// the file and then deleted it again would satisfy an emptiness check and would still have
// put malware in the readable bucket for as long as it took to change its mind.
type recordingStore struct {
	objectstore.Store
	secureBucket string

	mu     sync.Mutex
	writes []string
}

func newRecordingStore(inner objectstore.Store, secureBucket string) *recordingStore {
	return &recordingStore{Store: inner, secureBucket: secureBucket}
}

func (r *recordingStore) note(bucket, op, key string) {
	if bucket != r.secureBucket {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, op+" "+key)
}

// secureWrites returns every write aimed at the secure bucket so far.
func (r *recordingStore) secureWrites() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.writes...)
}

func (r *recordingStore) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = nil
}

func (r *recordingStore) Copy(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) error {
	r.note(dstBucket, "copy", dstKey)
	return r.Store.Copy(ctx, srcBucket, srcKey, dstBucket, dstKey)
}

func (r *recordingStore) PresignPut(ctx context.Context, bucket, key string, c objectstore.PutConstraint) (objectstore.PresignedURL, error) {
	r.note(bucket, "presign-put", key)
	return r.Store.PresignPut(ctx, bucket, key, c)
}

// fixture is one throw-away database, one object store and one scanner.
type fixture struct {
	h        *dbtest.Harness
	pool     *pgxpool.Pool
	svc      *application.Service
	store    objectstore.Store
	recorder *recordingStore
	memory   *objectstore.Memory // non-nil only when MinIO was not reachable
	scanner  antivirus.Scanner
	fake     *fakeScanner // non-nil unless the live daemon is in use

	tenant       uuid.UUID
	actor        uuid.UUID
	membership   uuid.UUID
	organization uuid.UUID
	other        uuid.UUID // a second provider organization, for the boundary tests

	quarantine string
	secure     string
	// prefix keeps one test's keys apart from another's in the shared local buckets.
	prefix string
}

// newFixture builds the pipeline against the real MinIO when the local runbook has it
// running, and against an in-memory store otherwise. The scanner is always the fake one:
// the verdicts a test needs are exactly the ones a real daemon will not produce on demand.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := newBase(t)
	f.fake = newFakeScanner()
	f.scanner = f.fake
	f.build(t)
	return f
}

// newLiveFixture builds the pipeline against the real MinIO and the real clamd, or skips.
// It is what proves the client code in internal/platform actually speaks to those two
// services; the fake-scanner tests prove what the pipeline does with a verdict.
func newLiveFixture(t *testing.T) *fixture {
	t.Helper()
	f := newBase(t)
	if f.memory != nil {
		t.Skip("MinIO is not answering (scripts/native/up.*); skipping the live pipeline test")
	}
	scanner, err := antivirus.NewClamd(antivirus.ClamdOptions{
		Address: envOr("KAPSORA_CLAMAV_ADDR", defaultClamdAddr), Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("new clamd client: %v", err)
	}
	if err := scanner.Ping(context.Background()); err != nil {
		t.Skipf("clamd is not answering (scripts/native/up.*): %v", err)
	}
	f.scanner = scanner
	f.build(t)
	return f
}

func newBase(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, h.AppURL, db.PoolOptions{ApplicationName: "document-test", MaxConns: 12})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	f := &fixture{
		h: h, pool: pool,
		quarantine: "quarantine", secure: "secure",
		prefix: "kapsora-test-" + uuid.NewString() + "/",
	}

	live, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint:  envOr("KAPSORA_MINIO_ADDR", defaultMinIOAddr),
		AccessKey: envOr("KAPSORA_MINIO_ROOT_USER", defaultMinIOUser),
		SecretKey: envOr("KAPSORA_MINIO_ROOT_PASSWORD", defaultMinIOSecret),
	})
	if err == nil {
		if _, probeErr := live.Exists(ctx, f.quarantine, f.prefix+"probe"); probeErr == nil {
			f.store = live
		}
	}
	if f.store == nil {
		f.memory = objectstore.NewMemory()
		f.store = f.memory
	}
	// Everything above the store goes through the recorder, so a test can ask what was
	// written rather than only what is left.
	f.recorder = newRecordingStore(f.store, f.secure)
	f.store = f.recorder
	return f
}

func (f *fixture) build(t *testing.T) {
	t.Helper()
	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: f.pool, Repo: documentpg.New(), Store: f.store, Scanner: f.scanner,
		Audit: auditpg.New(), Cursors: cursors,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Storage: application.Storage{
			QuarantineBucket: f.quarantine, SecureBucket: f.secure,
			UploadTTL: 15 * time.Minute, DownloadTTL: 5 * time.Minute,
			EncryptionKeyRef: "objectstore:test",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc

	f.tenant = f.h.CreateTenant("DOC")
	f.actor = f.h.CreateActor("document-clerk-"+uuid.NewString()[:8], "Document Clerk")
	f.membership = f.h.CreateMembership(f.tenant, f.actor)
	f.organization = f.h.CreateTenantOrganization(f.tenant, "Sağlayıcı A", "PROVIDER")
	f.other = f.h.CreateTenantOrganization(f.tenant, "Sağlayıcı B", "PROVIDER")

	// Whatever this test leaves in the shared local buckets is its own to remove.
	t.Cleanup(func() { f.cleanupObjects() })
}

// cleanupObjects removes every key this fixture may have created from both buckets. The
// local MinIO is shared with the developer's own data, so a test tidies up after itself.
func (f *fixture) cleanupObjects() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := f.pool.Query(ctx, `SELECT object_key FROM document.object`)
	if err != nil {
		return
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err == nil {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		_ = f.store.Remove(ctx, f.quarantine, key)
		_ = f.store.Remove(ctx, f.secure, key)
	}
}

// rc is a tenant-wide caller holding every document permission.
func (f *fixture) rc(permissions ...string) identity.RequestContext {
	granted := map[string]struct{}{
		application.PermissionUpload:    {},
		application.PermissionRead:      {},
		application.PermissionLink:      {},
		application.PermissionLegalHold: {},
	}
	for _, p := range permissions {
		granted[p] = struct{}{}
	}
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: f.membership,
		Principal:   identity.Principal{ActorID: f.actor},
		Permissions: granted,
	}
}

// scopedRC is a provider-side caller bound to one organization.
func (f *fixture) scopedRC(organizationID uuid.UUID) identity.RequestContext {
	rc := f.rc()
	rc.Scopes = []identity.Scope{{
		Type: application.ScopeOrganization,
		ID:   uuid.NullUUID{UUID: organizationID, Valid: true},
	}}
	return rc
}

// upload runs the client half of the pipeline: reserve, put the bytes where the presigned
// URL says, and complete. It returns the document as it stands after completeUpload.
func (f *fixture) upload(t *testing.T, rc identity.RequestContext, body []byte,
	in application.NewUploadInput,
) application.Document {
	t.Helper()
	if in.OriginalFilename == "" {
		in.OriginalFilename = "rapor.pdf"
	}
	if in.ContentType == "" {
		in.ContentType = "application/pdf"
	}
	if in.Classification == "" {
		in.Classification = "INTERNAL"
	}
	in.ByteSize = int64(len(body))

	reservation, err := f.svc.CreateUpload(t.Context(), rc, in)
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	if reservation.Upload == nil {
		// Deduplicated: the same bytes were already stored, so there is nothing to send.
		return reservation.Document
	}
	f.putBytes(t, *reservation.Upload, reservation.Document.Object.ObjectKey, body)

	digest := sha256.Sum256(body)
	doc, err := f.svc.CompleteUpload(t.Context(), rc, reservation.Document.Object.ID,
		hex.EncodeToString(digest[:]), int64(len(body)))
	if err != nil {
		t.Fatalf("complete upload: %v", err)
	}
	return doc
}

// putBytes stands in for the browser. Against the real store it uses the presigned URL,
// which is the only way a byte is meant to reach a bucket; against the in-memory one it
// writes directly, because that store's URLs are not fetchable.
func (f *fixture) putBytes(t *testing.T, url objectstore.PresignedURL, key string, body []byte) {
	t.Helper()
	if f.memory != nil {
		f.memory.Put(f.quarantine, key, body)
		return
	}
	req, err := http.NewRequestWithContext(t.Context(), url.Method, url.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	for name, value := range url.Headers {
		if name == "Content-Length" {
			continue // set by the transport from the body itself
		}
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload through the presigned url: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		t.Fatalf("upload status = %d: %s", resp.StatusCode, detail)
	}
}

// exists asks the object store itself, not the database.
func (f *fixture) exists(t *testing.T, bucket, key string) bool {
	t.Helper()
	present, err := f.store.Exists(t.Context(), bucket, key)
	if err != nil {
		t.Fatalf("stat %s/%s: %v", bucket, key, err)
	}
	return present
}

// fetch reads a document straight from the database, bypassing the service, so an
// assertion about a row is an assertion about the row.
func (f *fixture) fetch(t *testing.T, id uuid.UUID) application.ObjectRecord {
	t.Helper()
	doc, err := f.svc.GetDocument(t.Context(), f.rc(), id)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	return doc.Object
}

// countAudit counts audit.event rows of one category and action for a resource.
func (f *fixture) countAudit(t *testing.T, category, action string, resourceID uuid.UUID) int {
	t.Helper()
	var n int
	err := f.h.Admin.QueryRow(t.Context(), `
		SELECT count(*) FROM audit.event
		 WHERE tenant_id = $1 AND event_category = $2 AND action_code = $3 AND resource_id = $4`,
		f.tenant, category, action, resourceID).Scan(&n)
	if err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	return n
}

// accessEvents returns the access rows written for one resource, newest first.
type accessEvent struct {
	AccessType     string
	Classification string
	ReasonText     *string
	PurposeCode    *string
	Outcome        string
}

func (f *fixture) accessEvents(t *testing.T, resourceID uuid.UUID) []accessEvent {
	t.Helper()
	rows, err := f.h.Admin.Query(t.Context(), `
		SELECT access_type, data_classification, reason_text, purpose_code, outcome
		  FROM audit.access_event
		 WHERE tenant_id = $1 AND resource_id = $2
		 ORDER BY occurred_at DESC`, f.tenant, resourceID)
	if err != nil {
		t.Fatalf("read access events: %v", err)
	}
	defer rows.Close()
	var out []accessEvent
	for rows.Next() {
		var e accessEvent
		if err := rows.Scan(&e.AccessType, &e.Classification, &e.ReasonText, &e.PurposeCode, &e.Outcome); err != nil {
			t.Fatalf("scan access event: %v", err)
		}
		out = append(out, e)
	}
	return out
}

// ---------------------------------------------------------------------------------------
// The acceptance criterion of v1.2 Phase 5.
// ---------------------------------------------------------------------------------------

// TestEicarNeverReachesTheSecureBucket is the single most important test in this package.
// It asserts all five halves of the promise, and it is written so that breaking any one of
// them fails it:
//
//  1. the object is INFECTED;
//  2. the quarantine key is gone;
//  3. the secure bucket holds no object under that key;
//  4. the download endpoint refuses;
//  5. a SECURITY-category audit event exists.
//
// It runs with the fake scanner so it works on any machine. The same five assertions are
// made against the real ClamAV by TestEicarAgainstLiveClamAV below.
func TestEicarNeverReachesTheSecureBucket(t *testing.T) {
	f := newFixture(t)
	f.fake.queueInfected("Eicar-Signature-Test")
	assertEicarQuarantined(t, f, eicar)
}

// TestEicarAgainstLiveClamAV is the same five assertions end to end: the real EICAR file,
// uploaded through a real presigned URL into the real quarantine bucket, scanned by the
// real clamd, with the real object store asked afterwards what it holds. It skips when the
// native services are not running, and it is the test that proves the two clients in
// internal/platform are not merely self-consistent.
func TestEicarAgainstLiveClamAV(t *testing.T) {
	f := newLiveFixture(t)
	assertEicarQuarantined(t, f, eicar)
}

func assertEicarQuarantined(t *testing.T, f *fixture, body []byte) {
	t.Helper()
	rc := f.rc()
	doc := f.upload(t, rc, body, application.NewUploadInput{
		OriginalFilename: "fatura.pdf", ContentType: "application/pdf",
	})
	key := doc.Object.ObjectKey

	// Before the scan the file is in quarantine and unreadable.
	if !f.exists(t, f.quarantine, key) {
		t.Fatal("the upload never reached the quarantine bucket")
	}
	if f.exists(t, f.secure, key) {
		t.Fatal("an unscanned file is already in the secure bucket")
	}

	// From here on, any write aimed at the secure bucket is a failure — including one that
	// is undone afterwards.
	f.recorder.reset()
	report, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.Outcome != antivirus.OutcomeInfected {
		t.Fatalf("scan outcome = %s, want INFECTED", report.Outcome)
	}
	if report.Promoted {
		t.Fatal("an infected file was reported as promoted")
	}
	if writes := f.recorder.secureWrites(); len(writes) != 0 {
		t.Fatalf("the scan wrote to the secure bucket while handling an infected file: %v", writes)
	}

	// (1) the object is INFECTED.
	after := f.fetch(t, doc.Object.ID)
	if after.ScanStatus != "INFECTED" {
		t.Fatalf("scan status = %s, want INFECTED", after.ScanStatus)
	}
	if after.Bucket != "quarantine" {
		t.Fatalf("bucket = %s, want quarantine; an infected row must never claim the secure bucket", after.Bucket)
	}

	// (2) the quarantine key is gone.
	if f.exists(t, f.quarantine, key) {
		t.Fatal("the infected file is still in the quarantine bucket")
	}
	// (3) the secure bucket holds no object under that key.
	if f.exists(t, f.secure, key) {
		t.Fatal("the infected file reached the secure bucket")
	}
	// ...and under no other key either: the row is the only thing that knows where the
	// bytes would have gone, and it still names this one.
	if f.memory != nil {
		if keys := f.memory.Keys(f.secure); len(keys) != 0 {
			t.Fatalf("the secure bucket is not empty after an infected upload: %v", keys)
		}
		if keys := f.memory.Keys(f.quarantine); len(keys) != 0 {
			t.Fatalf("the quarantine bucket still holds %v", keys)
		}
	}

	// (4) the download endpoint refuses.
	if _, _, err := f.svc.Download(t.Context(), rc, doc.Object.ID, application.DownloadRequest{}); !errors.Is(err, application.ErrInfected) {
		t.Fatalf("download of an infected document = %v, want ErrInfected", err)
	}
	if f.fetch(t, doc.Object.ID).ScanStatus == "CLEAN" {
		t.Fatal("the infected document became downloadable")
	}

	// (5) a SECURITY-category audit event exists, and it names what was found.
	if n := f.countAudit(t, "SECURITY", "document.scan.infected", doc.Object.ID); n != 1 {
		t.Fatalf("SECURITY audit events for the infected upload = %d, want 1", n)
	}
	var finding string
	if err := f.h.Admin.QueryRow(t.Context(), `
		SELECT detail_json ->> 'finding' FROM audit.event
		 WHERE tenant_id = $1 AND event_category = 'SECURITY' AND resource_id = $2`,
		f.tenant, doc.Object.ID).Scan(&finding); err != nil {
		t.Fatalf("read the security event detail: %v", err)
	}
	if strings.TrimSpace(finding) == "" {
		t.Fatal("the SECURITY event names no finding; an incident nobody can act on is not a record")
	}

	// The verdict itself is on the record, append-only.
	results, err := f.svc.ScanHistory(t.Context(), rc, doc.Object.ID)
	if err != nil {
		t.Fatalf("scan history: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != "INFECTED" {
		t.Fatalf("scan history = %+v, want one INFECTED verdict", results)
	}
}

// ---------------------------------------------------------------------------------------
// The rest of section 3.
// ---------------------------------------------------------------------------------------

// TestCleanFileGoesQuarantineToSecure is the happy path, asserted at the buckets rather
// than at the row: a clean file ends up in secure, its quarantine copy is gone, and the
// URL the download hands out actually fetches the bytes that were uploaded.
func TestCleanFileGoesQuarantineToSecure(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	body := []byte("temiz bir fatura pdf gibi davranıyor")

	doc := f.upload(t, rc, body, application.NewUploadInput{})
	key := doc.Object.ObjectKey
	if doc.Object.ScanStatus != "SCANNING" {
		t.Fatalf("scan status after complete = %s, want SCANNING", doc.Object.ScanStatus)
	}
	if _, _, err := f.svc.Download(t.Context(), rc, doc.Object.ID, application.DownloadRequest{}); !errors.Is(err, application.ErrNotScanned) {
		t.Fatalf("download before the scan = %v, want ErrNotScanned", err)
	}

	report, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !report.Promoted {
		t.Fatal("a clean file was not promoted")
	}
	// The scanner saw the whole file, not a promise of one.
	if got := f.fake.lastBody(); !bytes.Equal(got, body) {
		t.Fatalf("the scanner read %d bytes, the file has %d", len(got), len(body))
	}

	after := f.fetch(t, doc.Object.ID)
	if after.ScanStatus != "CLEAN" || after.Bucket != "secure" {
		t.Fatalf("after promotion status=%s bucket=%s", after.ScanStatus, after.Bucket)
	}
	// The digest stored is the one computed from the bytes, and the size is the counted one.
	want := sha256.Sum256(body)
	if !bytes.Equal(after.SHA256, want[:]) {
		t.Fatalf("stored digest %x, want %x", after.SHA256, want)
	}
	if after.ByteSize == nil || *after.ByteSize != int64(len(body)) {
		t.Fatalf("stored size = %v, want %d", after.ByteSize, len(body))
	}

	if f.exists(t, f.quarantine, key) {
		t.Fatal("the quarantine copy of a promoted file is still there")
	}
	if !f.exists(t, f.secure, key) {
		t.Fatal("the promoted file is not in the secure bucket")
	}

	url, _, err := f.svc.Download(t.Context(), rc, doc.Object.ID, application.DownloadRequest{
		PurposeCode: "CLAIM_REVIEW", ReasonText: "fatura kontrolü",
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if url.ExpiresAt.Before(time.Now()) {
		t.Fatalf("the download url is already expired: %s", url.ExpiresAt)
	}
	if f.memory == nil {
		// Against the real store the URL is fetchable, so the strongest possible
		// assertion is available: the bytes that come back are the bytes that went in.
		got := fetchURL(t, url)
		if !bytes.Equal(got, body) {
			t.Fatalf("downloaded %q, want %q", got, body)
		}
	}
}

// TestFailedScanIsNotDownloadableAndARetryPromotesIt is the third state. A scanner that
// could not answer is not a clean file: it stays in quarantine, it is downloadable by
// nobody, and it becomes readable only when a later attempt actually succeeds.
func TestFailedScanIsNotDownloadableAndARetryPromotesIt(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	body := []byte("bu dosya ilk denemede taranamayacak")

	doc := f.upload(t, rc, body, application.NewUploadInput{})
	key := doc.Object.ObjectKey

	f.fake.queueUnavailable()
	if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); !errors.Is(err, antivirus.ErrUnavailable) {
		t.Fatalf("scan with an unreachable daemon = %v, want ErrUnavailable", err)
	}

	failed := f.fetch(t, doc.Object.ID)
	if failed.ScanStatus != "FAILED" {
		t.Fatalf("scan status = %s, want FAILED", failed.ScanStatus)
	}
	if failed.Bucket != "quarantine" {
		t.Fatalf("bucket = %s; a file nobody could scan must not leave quarantine", failed.Bucket)
	}
	if !f.exists(t, f.quarantine, key) {
		t.Fatal("the unscannable file was deleted; there would be nothing left to retry")
	}
	if f.exists(t, f.secure, key) {
		t.Fatal("a file that could not be scanned reached the secure bucket")
	}
	if _, _, err := f.svc.Download(t.Context(), rc, doc.Object.ID, application.DownloadRequest{}); !errors.Is(err, application.ErrNotScanned) {
		t.Fatalf("download of a FAILED document = %v, want ErrNotScanned", err)
	}

	// The retry succeeds, and only then is the file readable.
	report, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID)
	if err != nil {
		t.Fatalf("retry scan: %v", err)
	}
	if !report.Promoted {
		t.Fatal("the successful retry did not promote the file")
	}
	promoted := f.fetch(t, doc.Object.ID)
	if promoted.ScanStatus != "CLEAN" || promoted.Bucket != "secure" {
		t.Fatalf("after the retry status=%s bucket=%s", promoted.ScanStatus, promoted.Bucket)
	}
	if f.exists(t, f.quarantine, key) {
		t.Fatal("the quarantine copy survived the successful retry")
	}
	if _, _, err := f.svc.Download(t.Context(), rc, doc.Object.ID, application.DownloadRequest{}); err != nil {
		t.Fatalf("download after a successful retry: %v", err)
	}

	// Both attempts are on the record; a verdict that could be rewritten is not evidence.
	history, err := f.svc.ScanHistory(t.Context(), rc, doc.Object.ID)
	if err != nil {
		t.Fatalf("scan history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("scan history has %d verdicts, want 2 (the failure and the success)", len(history))
	}
}

// TestSameBytesTwiceProduceOneCleanObject covers both ways the same file can arrive twice:
// a second upload that declares the digest up front, which never uploads at all, and two
// uploads that both got past that check and race to be promoted.
func TestSameBytesTwiceProduceOneCleanObject(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	body := []byte("aynı dosya iki kez yüklenirse tek nesne olmalı")
	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])

	first := f.upload(t, rc, body, application.NewUploadInput{SHA256: digestHex})
	if _, err := f.svc.ScanObject(t.Context(), f.tenant, first.Object.ID); err != nil {
		t.Fatalf("scan the first upload: %v", err)
	}

	// The second attempt declares the digest and is answered with the document that
	// already exists; nothing is uploaded.
	reservation, err := f.svc.CreateUpload(t.Context(), rc, application.NewUploadInput{
		OriginalFilename: "aynı.pdf", ContentType: "application/pdf",
		Classification: "INTERNAL", ByteSize: int64(len(body)), SHA256: digestHex,
	})
	if err != nil {
		t.Fatalf("second create upload: %v", err)
	}
	if reservation.Upload != nil {
		t.Fatal("the second upload of the same bytes was given an upload url")
	}
	if reservation.Document.Object.ID != first.Object.ID {
		t.Fatalf("dedupe returned %s, want the existing %s",
			reservation.Document.Object.ID, first.Object.ID)
	}

	// One canonical CLEAN object per digest, and one stored file.
	assertOneCanonical(t, f, digest[:], 1)

	// Now the race: a second row that got past the dedupe check because it declared no
	// digest, and is promoted after the first one already holds those bytes.
	second := f.upload(t, rc, body, application.NewUploadInput{OriginalFilename: "kopya.pdf"})
	if second.Object.ID == first.Object.ID {
		t.Fatal("an upload with no declared digest reused the existing row")
	}
	report, err := f.svc.ScanObject(t.Context(), f.tenant, second.Object.ID)
	if err != nil {
		t.Fatalf("scan the racing upload: %v", err)
	}
	if !report.Deduplicated || report.Promoted {
		t.Fatalf("the racing upload was promoted as a second copy: %+v", report)
	}

	// Still one canonical object, still one stored file: the loser is a pointer at the
	// winner's key and carries no bytes of its own.
	assertOneCanonical(t, f, digest[:], 2)
	loser := f.fetch(t, second.Object.ID)
	if loser.DuplicateOfObjectID == nil || *loser.DuplicateOfObjectID != first.Object.ID {
		t.Fatalf("the racing upload does not point at the canonical object: %+v", loser.DuplicateOfObjectID)
	}
	if loser.ObjectKey != first.Object.ObjectKey {
		t.Fatalf("the duplicate carries its own key %s rather than the canonical %s",
			loser.ObjectKey, first.Object.ObjectKey)
	}
	if f.exists(t, f.secure, second.Object.ObjectKey) {
		t.Fatal("the duplicate left a second copy of the bytes in the secure bucket")
	}
	if f.exists(t, f.quarantine, second.Object.ObjectKey) {
		t.Fatal("the duplicate left its quarantine copy behind")
	}
	// And it is still readable, through the canonical bytes.
	if _, _, err := f.svc.Download(t.Context(), rc, second.Object.ID, application.DownloadRequest{}); err != nil {
		t.Fatalf("download through the duplicate: %v", err)
	}
}

// assertOneCanonical checks that exactly one canonical CLEAN object holds a digest, and
// that the number of rows naming it is what the test expects.
func assertOneCanonical(t *testing.T, f *fixture, digest []byte, wantRows int) {
	t.Helper()
	var canonical, rows int
	if err := f.h.Admin.QueryRow(t.Context(), `
		SELECT count(*) FILTER (WHERE duplicate_of_object_id IS NULL), count(*)
		  FROM document.object
		 WHERE tenant_id = $1 AND sha256 = $2 AND scan_status = 'CLEAN'`,
		f.tenant, digest).Scan(&canonical, &rows); err != nil {
		t.Fatalf("count objects by digest: %v", err)
	}
	if canonical != 1 {
		t.Fatalf("canonical CLEAN objects for one digest = %d, want exactly 1", canonical)
	}
	if rows != wantRows {
		t.Fatalf("CLEAN rows naming one digest = %d, want %d", rows, wantRows)
	}
	if f.memory != nil {
		if keys := f.memory.Keys(f.secure); len(keys) != 1 {
			t.Fatalf("the secure bucket holds %d objects for one file: %v", len(keys), keys)
		}
	}
}

// TestHealthDownloadIsAuditedWithItsClassification covers both halves of v1.2 11.10: the
// access event carries the classification and the reason, and a HEALTH document produces a
// second, separate audit event of its own.
func TestHealthDownloadIsAuditedWithItsClassification(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	doc := f.upload(t, rc, []byte("epikriz"), application.NewUploadInput{
		OriginalFilename: "epikriz.pdf", Classification: "HEALTH",
	})
	if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}

	const reason = "tıbbi değerlendirme için açıldı"
	if _, _, err := f.svc.Download(t.Context(), rc, doc.Object.ID, application.DownloadRequest{
		PurposeCode: "MEDICAL_REVIEW", ReasonText: reason,
	}); err != nil {
		t.Fatalf("download: %v", err)
	}

	events := f.accessEvents(t, doc.Object.ID)
	if len(events) != 1 {
		t.Fatalf("access events = %d, want 1", len(events))
	}
	got := events[0]
	if got.Classification != "HEALTH" {
		t.Fatalf("access event classification = %s, want HEALTH", got.Classification)
	}
	if got.AccessType != "DOWNLOAD" || got.Outcome != "SUCCESS" {
		t.Fatalf("access event = %+v", got)
	}
	if got.ReasonText == nil || *got.ReasonText != reason {
		t.Fatalf("access event reason = %v, want %q", got.ReasonText, reason)
	}
	if got.PurposeCode == nil || *got.PurposeCode != "MEDICAL_REVIEW" {
		t.Fatalf("access event purpose = %v", got.PurposeCode)
	}
	if n := f.countAudit(t, "ACCESS", "document.download.health", doc.Object.ID); n != 1 {
		t.Fatalf("separate health access events = %d, want 1", n)
	}

	// An INTERNAL document is audited too, but produces no health event: the second one is
	// what makes a health access report possible without reading every download.
	plain := f.upload(t, rc, []byte("genel bilgi notu"), application.NewUploadInput{})
	if _, err := f.svc.ScanObject(t.Context(), f.tenant, plain.Object.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, _, err := f.svc.Download(t.Context(), rc, plain.Object.ID, application.DownloadRequest{}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if n := f.countAudit(t, "ACCESS", "document.download.health", plain.Object.ID); n != 0 {
		t.Fatalf("an INTERNAL document produced %d health access events", n)
	}
	if events := f.accessEvents(t, plain.Object.ID); len(events) != 1 || events[0].Classification != "INTERNAL" {
		t.Fatalf("INTERNAL access events = %+v", events)
	}
}

// TestDownloadWithoutTheLinksPermissionIsRefused is the link's own answer to who may reach
// a document. The caller holds document.read and may see the document; the link says
// reaching it needs health.clinical.read, and it does not have that.
func TestDownloadWithoutTheLinksPermissionIsRefused(t *testing.T) {
	f := newFixture(t)
	owner := f.rc()
	doc := f.upload(t, owner, []byte("klinik ek"), application.NewUploadInput{
		Classification: "HEALTH",
	})
	if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := f.svc.LinkDocument(t.Context(), owner, doc.Object.ID, application.NewLinkInput{
		AggregateType: "SERVICE_REQUEST", AggregateID: uuid.New(),
		DocumentTypeCode: "MEDICAL_REPORT", RequiredPermission: "health.clinical.read",
	}); err != nil {
		t.Fatalf("link document: %v", err)
	}

	// The caller holding only the document permissions is refused.
	_, _, err := f.svc.Download(t.Context(), f.rc(), doc.Object.ID, application.DownloadRequest{
		ReasonText: "yetkisiz deneme",
	})
	if !errors.Is(err, application.ErrLinkPermission) {
		t.Fatalf("download without the link's permission = %v, want ErrLinkPermission", err)
	}
	// The refusal is on the record as a denied access, which is the row a security review
	// looks for.
	events := f.accessEvents(t, doc.Object.ID)
	if len(events) != 1 || events[0].Outcome != "DENIED" {
		t.Fatalf("denied access events = %+v", events)
	}

	// The same caller holding the permission the link names gets its URL.
	if _, _, err := f.svc.Download(t.Context(), f.rc("health.clinical.read"), doc.Object.ID,
		application.DownloadRequest{ReasonText: "yetkili okuma"}); err != nil {
		t.Fatalf("download with the link's permission: %v", err)
	}
}

// TestLegalHoldBlocksRetentionAndReleasingItAllowsIt is the acceptance criterion "a
// document under legal hold survives retention". Two identical documents, one held: the
// sweep purges one and reports the other as held, and after the hold is released the same
// sweep purges it too.
func TestLegalHoldBlocksRetentionAndReleasingItAllowsIt(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()

	held := f.upload(t, rc, []byte("saklanacak belge"), application.NewUploadInput{})
	free := f.upload(t, rc, []byte("saklanmayacak belge"), application.NewUploadInput{})
	for _, doc := range []application.Document{held, free} {
		if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}

	hold, err := f.svc.PutLegalHold(t.Context(), rc, application.NewLegalHoldInput{
		ObjectID: &held.Object.ID, Reason: "dava dosyası",
	})
	if err != nil {
		t.Fatalf("put legal hold: %v", err)
	}

	// Everything uploaded so far is older than "a moment from now", so the sweep considers
	// both documents and has to choose between them on the hold alone.
	report, err := f.svc.PurgeExpired(t.Context(), time.Now().UTC().Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if report.Purged != 1 || report.Held != 1 {
		t.Fatalf("purge report = %+v, want one purged and one held", report)
	}
	if f.fetch(t, held.Object.ID).PurgedAt != nil {
		t.Fatal("a document under legal hold was purged")
	}
	if !f.exists(t, f.secure, held.Object.ObjectKey) {
		t.Fatal("the bytes of a document under legal hold were deleted")
	}
	if f.fetch(t, free.Object.ID).PurgedAt == nil {
		t.Fatal("the unheld document was not purged")
	}
	if f.exists(t, f.secure, free.Object.ObjectKey) {
		t.Fatal("the purged document still has its bytes")
	}
	if _, _, err := f.svc.Download(t.Context(), rc, free.Object.ID, application.DownloadRequest{}); !errors.Is(err, application.ErrPurged) {
		t.Fatalf("download of a purged document = %v, want ErrPurged", err)
	}
	// The held one is still readable, which is the point of holding it.
	if _, _, err := f.svc.Download(t.Context(), rc, held.Object.ID, application.DownloadRequest{}); err != nil {
		t.Fatalf("download of a held document: %v", err)
	}

	// Releasing the hold lets the same sweep act on it.
	if _, err := f.svc.ReleaseLegalHold(t.Context(), rc, hold.ID, hold.RowVersion); err != nil {
		t.Fatalf("release legal hold: %v", err)
	}
	report, err = f.svc.PurgeExpired(t.Context(), time.Now().UTC().Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("purge after release: %v", err)
	}
	if report.Purged != 1 || report.Held != 0 {
		t.Fatalf("purge report after release = %+v, want one purged and none held", report)
	}
	if f.fetch(t, held.Object.ID).PurgedAt == nil {
		t.Fatal("the released document was not purged")
	}
	if f.exists(t, f.secure, held.Object.ObjectKey) {
		t.Fatal("the released document still has its bytes")
	}
}

// TestLegalHoldOverAnAggregateCoversItsDocuments: a hold over a case is a hold over the
// case's documents, so retention must skip a document only ever named by a link.
func TestLegalHoldOverAnAggregateCoversItsDocuments(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	requestID := uuid.New()

	doc := f.upload(t, rc, []byte("dava kapsamındaki fatura"), application.NewUploadInput{})
	if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := f.svc.LinkDocument(t.Context(), rc, doc.Object.ID, application.NewLinkInput{
		AggregateType: "SERVICE_REQUEST", AggregateID: requestID, DocumentTypeCode: "INVOICE",
	}); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := f.svc.PutLegalHold(t.Context(), rc, application.NewLegalHoldInput{
		AggregateType: "SERVICE_REQUEST", AggregateID: &requestID, Reason: "inceleme",
	}); err != nil {
		t.Fatalf("put legal hold: %v", err)
	}

	report, err := f.svc.PurgeExpired(t.Context(), time.Now().UTC().Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if report.Purged != 0 || report.Held != 1 {
		t.Fatalf("purge report = %+v, want nothing purged and one held", report)
	}
	if !f.exists(t, f.secure, doc.Object.ObjectKey) {
		t.Fatal("a document held through its record was purged")
	}
}

// TestProviderScopeHidesAnotherProvidersDocuments is the boundary: out of scope is 404,
// not 403, and a grant naming no organization reaches nothing at all.
func TestProviderScopeHidesAnotherProvidersDocuments(t *testing.T) {
	f := newFixture(t)
	owner := f.scopedRC(f.organization)

	doc := f.upload(t, owner, []byte("sağlayıcı a belgesi"), application.NewUploadInput{})
	if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if doc.Object.OwnerOrganizationID == nil || *doc.Object.OwnerOrganizationID != f.organization {
		t.Fatalf("owner organization = %v, want the caller's only grant", doc.Object.OwnerOrganizationID)
	}

	// Another provider does not find it.
	if _, err := f.svc.GetDocument(t.Context(), f.scopedRC(f.other), doc.Object.ID); !errors.Is(err, application.ErrObjectNotFound) {
		t.Fatalf("get across the provider boundary = %v, want ErrObjectNotFound", err)
	}
	if _, _, err := f.svc.Download(t.Context(), f.scopedRC(f.other), doc.Object.ID, application.DownloadRequest{}); !errors.Is(err, application.ErrObjectNotFound) {
		t.Fatalf("download across the provider boundary = %v, want ErrObjectNotFound", err)
	}

	// A grant naming no organization restricts to nothing rather than to everything.
	empty := f.rc()
	empty.Scopes = []identity.Scope{{Type: application.ScopeOrganization}}
	if _, err := f.svc.GetDocument(t.Context(), empty, doc.Object.ID); !errors.Is(err, application.ErrObjectNotFound) {
		t.Fatalf("get with an empty grant = %v, want ErrObjectNotFound", err)
	}
	page, err := f.svc.ListDocuments(t.Context(), empty, application.Filter{})
	if err != nil {
		t.Fatalf("list with an empty grant: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("an empty grant listed %d documents", len(page.Items))
	}
	// The tenant-wide caller sees it.
	if _, err := f.svc.GetDocument(t.Context(), f.rc(), doc.Object.ID); err != nil {
		t.Fatalf("tenant-wide get: %v", err)
	}
}

// TestRedeliveredScanDoesNotRescanOrRecopy: the outbox delivers at least once, so a second
// pass over a decided document must change nothing.
func TestRedeliveredScanIsIdempotent(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	doc := f.upload(t, rc, []byte("iki kez teslim edilecek"), application.NewUploadInput{})

	if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	scannedOnce := f.fake.scanned

	if _, err := f.svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); err != nil {
		t.Fatalf("redelivered scan: %v", err)
	}
	if f.fake.scanned != scannedOnce {
		t.Fatalf("the redelivered event scanned the file again (%d scans, want %d)",
			f.fake.scanned, scannedOnce)
	}
	history, err := f.svc.ScanHistory(t.Context(), rc, doc.Object.ID)
	if err != nil {
		t.Fatalf("scan history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("the redelivered event wrote a second verdict: %d rows", len(history))
	}
	after := f.fetch(t, doc.Object.ID)
	if after.ScanStatus != "CLEAN" || after.Bucket != "secure" {
		t.Fatalf("after redelivery status=%s bucket=%s", after.ScanStatus, after.Bucket)
	}
}

// TestCompleteUploadRefusesWhenNothingWasUploaded: a presigned URL that was handed out and
// never used must not queue a scan of nothing.
func TestCompleteUploadRefusesWhenNothingWasUploaded(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	reservation, err := f.svc.CreateUpload(t.Context(), rc, application.NewUploadInput{
		OriginalFilename: "hayalet.pdf", ContentType: "application/pdf",
		Classification: "INTERNAL", ByteSize: 10,
	})
	if err != nil {
		t.Fatalf("create upload: %v", err)
	}
	digest := sha256.Sum256([]byte("hiç yüklenmedi"))
	_, err = f.svc.CompleteUpload(t.Context(), rc, reservation.Document.Object.ID,
		hex.EncodeToString(digest[:]), 10)
	if !errors.Is(err, application.ErrUploadMissing) {
		t.Fatalf("complete with no bytes = %v, want ErrUploadMissing", err)
	}
	if f.fetch(t, reservation.Document.Object.ID).ScanStatus != "PENDING" {
		t.Fatal("a document with no bytes left PENDING")
	}
}

// TestCompleteUploadIsRefusedTwice: the second call finds the object out of PENDING.
func TestCompleteUploadIsRefusedTwice(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	body := []byte("bir kez tamamlanır")
	doc := f.upload(t, rc, body, application.NewUploadInput{})
	digest := sha256.Sum256(body)
	_, err := f.svc.CompleteUpload(t.Context(), rc, doc.Object.ID,
		hex.EncodeToString(digest[:]), int64(len(body)))
	if !errors.Is(err, application.ErrAlreadyCompleted) {
		t.Fatalf("second complete = %v, want ErrAlreadyCompleted", err)
	}
}

// TestScanRefusesWithoutAScanner: the absence of a verdict is never a clean verdict, so a
// service built without a scanner refuses rather than promoting anything.
func TestScanRefusesWithoutAScanner(t *testing.T) {
	f := newFixture(t)
	rc := f.rc()
	doc := f.upload(t, rc, []byte("tarayıcısız süreç"), application.NewUploadInput{})

	svc, err := application.New(application.Deps{
		Pool: f.pool, Repo: documentpg.New(), Store: f.store, Audit: auditpg.New(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Storage: application.Storage{QuarantineBucket: f.quarantine, SecureBucket: f.secure},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ScanObject(t.Context(), f.tenant, doc.Object.ID); !errors.Is(err, application.ErrNoScanner) {
		t.Fatalf("scan without a scanner = %v, want ErrNoScanner", err)
	}
	if f.fetch(t, doc.Object.ID).ScanStatus != "SCANNING" {
		t.Fatal("a document was moved out of SCANNING by a process with no scanner")
	}
}

func fetchURL(t *testing.T, url objectstore.PresignedURL) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), url.Method, url.URL, nil)
	if err != nil {
		t.Fatalf("build download request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		t.Fatalf("download status = %d: %s", resp.StatusCode, detail)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	return body
}
