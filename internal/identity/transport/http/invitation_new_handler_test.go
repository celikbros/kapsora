package identityhttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/ratelimit"
)

const invitationNewPassword = "correct horse battery staple 2026"

type secretFailingInvitationAudit struct{ message string }

func (a secretFailingInvitationAudit) Record(context.Context, pgx.Tx, audit.Event) error {
	return errors.New(a.message)
}
func (secretFailingInvitationAudit) RecordAccess(context.Context, pgx.Tx, audit.AccessEvent) error {
	return nil
}

func newInvitationCall(s *authzServer, path string, payload any, key string, cookie *http.Cookie) (int, map[string]any, http.Header) {
	encoded, _ := json.Marshal(payload)
	headers := map[string]string{"Origin": "http://127.0.0.1:5181", "X-Invitation-Request": "1"}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	rec := s.do(call{method: http.MethodPost, path: path, body: string(encoded), headers: headers, cookie: cookie})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func newInvitationFixture(t *testing.T, s *authzServer, suffix string) (uuid.UUID, string) {
	t.Helper()
	if _, err := s.prov.GrantRole(context.Background(), application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	manager, csrf := directorySession(t, s)
	stepUpDirectory(t, s, manager, csrf)
	status, body, _ := invitationCreateCall(s, manager, csrf, s.tenantA, "invitation-new-create-"+suffix, "new-"+suffix+"@example.test")
	if status != 200 {
		t.Fatalf("create invitation: %d %v", status, body)
	}
	id := uuid.MustParse(body["invitationId"].(string))
	return id, invitationProof(t, s, s.tenantA, id)
}

func TestInvitationNewAccountAtomicRecoveryAndPrivacy(t *testing.T) {
	s := newAuthzServer(t)
	id, code := newInvitationFixture(t, s, "atomic-0001")
	ctx := context.Background()
	if status, body, _ := newInvitationCall(s, "/api/v1/invitations/inspect-new", map[string]any{"code": code}, "", nil); status != 200 || body["invitationStatus"] != "PENDING" || len(body) != 3 {
		t.Fatalf("inspect: %d %v", status, body)
	}
	request := map[string]any{"code": code, "displayName": "  New Recipient  ", "password": invitationNewPassword, "confirmed": true}
	validCookie, _ := directorySession(t, s)
	sessionHash := sha256.Sum256([]byte(validCookie.Value))
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.session SET last_seen_at=clock_timestamp()-interval '2 minutes' WHERE id_hash=$1`, sessionHash[:]); err != nil {
		t.Fatal(err)
	}
	var beforeSeen, afterSeen, beforeExpiry, afterExpiry time.Time
	if err := s.h.Admin.QueryRow(ctx, `SELECT last_seen_at,expires_at FROM iam.session WHERE id_hash=$1`, sessionHash[:]).Scan(&beforeSeen, &beforeExpiry); err != nil {
		t.Fatal(err)
	}
	status, body, headers := newInvitationCall(s, "/api/v1/invitations/accept-new", request, "new-accept-atomic-0001", validCookie)
	if err := s.h.Admin.QueryRow(ctx, `SELECT last_seen_at,expires_at FROM iam.session WHERE id_hash=$1`, sessionHash[:]).Scan(&afterSeen, &afterExpiry); err != nil {
		t.Fatal(err)
	}
	if !beforeSeen.Equal(afterSeen) || !beforeExpiry.Equal(afterExpiry) {
		t.Fatal("anonymous acceptance mutated an existing session")
	}
	if status != 200 || len(body) != 7 || body["membershipStatus"] != "ACTIVE" || body["accessPending"] != true || headers.Get("Cache-Control") != "no-store" || headers.Get("Set-Cookie") != "" {
		t.Fatalf("accept: %d %v %#v", status, body, headers)
	}
	handle, _ := body["loginHandle"].(string)
	if !regexp.MustCompile(`^k_[0-9a-f]{32}$`).MatchString(handle) {
		t.Fatalf("identifying or invalid handle %q", handle)
	}
	var actorID uuid.UUID
	var email *string
	var name, hash string
	var mustChange bool
	var actors, members, grants, sessions, receipts int
	if err := s.h.Admin.QueryRow(ctx, `SELECT a.id,a.display_name,a.email,c.password_hash,c.must_change_password FROM iam.actor a JOIN iam.credential c ON c.actor_id=a.id WHERE a.identity_subject=$1`, handle).Scan(&actorID, &name, &email, &hash, &mustChange); err != nil {
		t.Fatal(err)
	}
	if name != "New Recipient" || email != nil || mustChange || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatal("account fields invalid")
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.actor WHERE identity_subject=$1`, handle).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2 AND membership_status='ACTIVE'`, s.tenantA, actorID).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, uuid.MustParse(body["membershipId"].(string))).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.session WHERE actor_id=$1`, actorID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM system.idempotency_record WHERE command_code LIKE '%invitation%' AND idempotency_key='new-accept-atomic-0001'`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if actors != 1 || members != 1 || grants != 0 || sessions != 0 || receipts != 0 {
		t.Fatalf("actor=%d member=%d grants=%d sessions=%d generic receipts=%d", actors, members, grants, sessions, receipts)
	}
	var auditDetail string
	if err := s.h.Admin.QueryRow(ctx, `SELECT detail_json::text FROM audit.event WHERE resource_id=$1 AND action_code='tenant_invitation.accept_new'`, id).Scan(&auditDetail); err != nil {
		t.Fatal(err)
	}
	returned, _ := json.Marshal(body)
	for _, secret := range []string{code, invitationNewPassword} {
		if strings.Contains(auditDetail, secret) || strings.Contains(string(returned), secret) {
			t.Fatal("secret escaped into audit or response")
		}
	}
	if retry, again, _ := newInvitationCall(s, "/api/v1/invitations/accept-new", request, "new-accept-atomic-0001", nil); retry != 200 || again["loginHandle"] != handle {
		t.Fatalf("retry: %d %v", retry, again)
	}
	request["password"] = "incorrect password 123456"
	if denied, result, _ := newInvitationCall(s, "/api/v1/invitations/accept-new", request, "new-accept-atomic-0001", nil); denied != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("wrong retry: %d %v", denied, result)
	}
	var failures int
	if err := s.h.Admin.QueryRow(ctx, `SELECT failed_attempts FROM iam.credential WHERE actor_id=$1`, actorID).Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("wrong password counter rolled back: %d %v", failures, err)
	}
	badCode, _, err := application.GenerateInvitationCode(s.tenantA, id)
	if err != nil {
		t.Fatal(err)
	}
	if denied, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": badCode, "password": "incorrect password 123456"}, "", nil); denied != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("bad proof receipt %d %v", denied, result)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT failed_attempts FROM iam.credential WHERE actor_id=$1`, actorID).Scan(&failures); err != nil || failures != 1 {
		t.Fatal("bad proof selector incremented actor counter")
	}
	unknownCode, _, err := application.GenerateInvitationCode(s.tenantA, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if denied, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": unknownCode, "password": "incorrect password 123456"}, "", nil); denied != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("unknown selector receipt %d %v", denied, result)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT failed_attempts FROM iam.credential WHERE actor_id=$1`, actorID).Scan(&failures); err != nil || failures != 1 {
		t.Fatal("unknown selector incremented actor counter")
	}
	if denied, result, hdr := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": "incorrect password 123456"}, "", &http.Cookie{Name: s.cookies.Name(), Value: "invalid-session-cookie"}); denied != 404 || result["code"] != "INVITATION_UNAVAILABLE" || hdr.Get("Set-Cookie") != "" {
		t.Fatalf("invalid cookie changed on anonymous path %d %v", denied, result)
	}
	request["password"] = invitationNewPassword
	if denied, result, _ := newInvitationCall(s, "/api/v1/invitations/accept-new", request, "different-key-atomic-0001", nil); denied != 409 || result["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("changed key: %d %v", denied, result)
	}
	request["displayName"] = "Changed Recipient"
	if denied, result, _ := newInvitationCall(s, "/api/v1/invitations/accept-new", request, "new-accept-atomic-0001", nil); denied != 409 || result["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("changed display name: %d %v", denied, result)
	}
	request["displayName"] = "  New Recipient  "
	if received, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": invitationNewPassword}, "", nil); received != 200 || result["loginHandle"] != handle {
		t.Fatalf("receipt: %d %v", received, result)
	}
	newHash, err := domain.HashPassword("updated current password 2026", domain.DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.credential SET password_hash=$2 WHERE actor_id=$1`, actorID, newHash); err != nil {
		t.Fatal(err)
	}
	if received, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": invitationNewPassword}, "", nil); received != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("old password receipt: %d %v", received, result)
	}
	if received, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": "updated current password 2026"}, "", nil); received != 200 || result["loginHandle"] != handle {
		t.Fatalf("current password receipt: %d %v", received, result)
	}
	if inspected, result, _ := newInvitationCall(s, "/api/v1/invitations/inspect-new", map[string]any{"code": code}, "", nil); inspected != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("accepted inspect: %d %v", inspected, result)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_invitation SET terminal_at=clock_timestamp()-interval '25 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if received, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": "updated current password 2026"}, "", nil); received != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("expired receipt: %d %v", received, result)
	}
	if _, err := s.invitationRepo.CleanupInvitations(ctx); err != nil {
		t.Fatal(err)
	}
	var proof, fingerprint []byte
	var key *string
	if err := s.h.Admin.QueryRow(ctx, `SELECT proof_digest,accept_key,accept_new_fingerprint FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&proof, &key, &fingerprint); err != nil || proof != nil || key != nil || fingerprint != nil {
		t.Fatal("recovery secrets survived cleanup")
	}
}

func TestInvitationNewCrossModeAndAuditRollback(t *testing.T) {
	s := newAuthzServer(t)
	_, code := newInvitationFixture(t, s, "cross-mode-0001")
	ctx := context.Background()
	proof, err := application.ParseInvitationCode(code)
	if err != nil {
		t.Fatal(err)
	}
	failing := identitypg.NewInvitationRepository(s.h.App, s.invitationKeys, s.invitationKeys, failingDirectoryAudit{}).WithDeliveryEnabled(true)
	if _, err := failing.AcceptNewInvitation(ctx, proof, "Rollback Recipient", invitationNewPassword, "new-rollback-accept-0001", "v1|accept-new|Rollback Recipient|true"); err == nil {
		t.Fatal("audit failure committed")
	}
	var logs bytes.Buffer
	secretError := "pg constraint detail " + code + " " + invitationNewPassword
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	secretRepo := identitypg.NewInvitationRepository(s.h.App, s.invitationKeys, s.invitationKeys, secretFailingInvitationAudit{message: secretError}).WithDeliveryEnabled(true)
	secretHandler := identityhttp.NewInvitationHandler(application.NewInvitationService(secretRepo), nil, nil, logger)
	router := chi.NewRouter()
	router.Group(func(anon chi.Router) { secretHandler.AnonymousRecipientRoutes(anon, "http://127.0.0.1:5181") })
	encoded, _ := json.Marshal(map[string]any{"code": code, "displayName": "Rollback Recipient", "password": invitationNewPassword, "confirmed": true})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept-new", bytes.NewReader(encoded))
	req.Header.Set("Origin", "http://127.0.0.1:5181")
	req.Header.Set("X-Invitation-Request", "1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "new-rollback-http-0001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("injected error response %d", rec.Code)
	}
	for _, secret := range []string{code, invitationNewPassword, secretError} {
		if strings.Contains(rec.Body.String(), secret) || strings.Contains(logs.String(), secret) {
			t.Fatal("secret leaked from internal error")
		}
	}
	var state string
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.tenant_invitation WHERE id=$1`, proof.InvitationID).Scan(&state); err != nil || state != "PENDING" {
		t.Fatal("audit rollback did not restore invitation")
	}
	for _, query := range []string{
		`SELECT count(*) FROM iam.actor WHERE display_name='Rollback Recipient'`,
		`SELECT count(*) FROM iam.credential c JOIN iam.actor a ON a.id=c.actor_id WHERE a.display_name='Rollback Recipient'`,
		`SELECT count(*) FROM iam.tenant_membership m JOIN iam.actor a ON a.id=m.actor_id WHERE a.display_name='Rollback Recipient'`,
		`SELECT count(*) FROM audit.event WHERE resource_id=$1 AND action_code='tenant_invitation.accept_new'`,
	} {
		var count int
		args := []any{}
		if strings.Contains(query, "$1") {
			args = append(args, proof.InvitationID)
		}
		if err := s.h.Admin.QueryRow(ctx, query, args...).Scan(&count); err != nil || count != 0 {
			t.Fatalf("audit rollback left row: %d %v", count, err)
		}
	}
	if accepted, err := s.invitationRepo.AcceptExistingInvitation(ctx, s.actor, proof, "existing-cross-mode-0001"); err != nil || accepted.MembershipID == uuid.Nil {
		t.Fatalf("B1 acceptance: %v", err)
	}
	if status, result, _ := newInvitationCall(s, "/api/v1/invitations/inspect-new", map[string]any{"code": code}, "", nil); status != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("cross-mode inspect: %d %v", status, result)
	}
	if status, result, _ := newInvitationCall(s, "/api/v1/invitations/accept-new", map[string]any{"code": code, "displayName": "New Recipient", "password": invitationNewPassword, "confirmed": true}, "cross-mode-new-0001", nil); status != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("cross-mode accept: %d %v", status, result)
	}
	if status, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": invitationNewPassword}, "", nil); status != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("cross-mode receipt: %d %v", status, result)
	}
}

func TestInvitationNewOriginAndBodyBoundary(t *testing.T) {
	s := newAuthzServer(t)
	_, code := newInvitationFixture(t, s, "boundary-0001")
	for _, headers := range []map[string]string{
		{"X-Invitation-Request": "1"},
		{"Origin": "null", "X-Invitation-Request": "1"},
		{"Origin": "https://evil.example", "X-Invitation-Request": "1"},
		{"Origin": "http://127.0.0.1:5181"},
		{"Origin": "http://127.0.0.1:5181", "X-Invitation-Request": "1", "Sec-Fetch-Site": "cross-site"},
		{"Origin": "http://127.0.0.1:5181", "X-Invitation-Request": "1", "Content-Type": "text/plain"},
	} {
		rec := s.do(call{method: http.MethodPost, path: "/api/v1/invitations/inspect-new", body: `{"code":"` + code + `"}`, headers: headers})
		if rec.Code != 403 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("boundary: %d %#v", rec.Code, headers)
		}
	}
	for _, repeated := range []string{"Origin", "X-Invitation-Request", "Content-Type"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/inspect-new", strings.NewReader(`{"code":"`+code+`"}`))
		req.Header.Set("Origin", "http://127.0.0.1:5181")
		req.Header.Set("X-Invitation-Request", "1")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Add(repeated, req.Header.Get(repeated))
		rec := httptest.NewRecorder()
		s.handler.ServeHTTP(rec, req)
		if rec.Code != 403 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("repeated %s: %d", repeated, rec.Code)
		}
	}
	for _, body := range []string{`{"code":"` + code + `","unknown":1}`, `{"code":"` + code + `"} {}`, strings.Repeat("x", 16*1024+1)} {
		rec := s.do(call{method: http.MethodPost, path: "/api/v1/invitations/inspect-new", body: body, headers: map[string]string{"Origin": "http://127.0.0.1:5181", "X-Invitation-Request": "1"}})
		if rec.Code != 400 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("body boundary: %d", rec.Code)
		}
	}
}

func TestInvitationNewConcurrentAcceptanceAndLockout(t *testing.T) {
	s := newAuthzServer(t)
	_, code := newInvitationFixture(t, s, "concurrent-0001")
	request := map[string]any{"code": code, "displayName": "Concurrent Recipient", "password": invitationNewPassword, "confirmed": true}
	var status [2]int
	var body [2]map[string]any
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range 2 {
		go func(i int) {
			defer wg.Done()
			status[i], body[i], _ = newInvitationCall(s, "/api/v1/invitations/accept-new", request,
				[]string{"concurrent-key-first-0001", "concurrent-key-other-0001"}[i], nil)
		}(i)
	}
	wg.Wait()
	validOutcome := (status[0] == 200 && status[1] == 409) || (status[1] == 200 && status[0] == 409)
	if !validOutcome {
		t.Fatalf("concurrent outcome %v %v", status, body)
	}
	winner := 0
	if status[1] == 200 {
		winner = 1
	}
	handle := body[winner]["loginHandle"].(string)
	var actorID uuid.UUID
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT id FROM iam.actor WHERE identity_subject=$1`, handle).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT count(*) FROM iam.actor WHERE display_name='Concurrent Recipient'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent duplicate actor %d %v", count, err)
	}
	wrong := map[string]any{"code": code, "password": "wrong-password-recovery-123"}
	var wrongStatuses [10]int
	wg.Add(len(wrongStatuses))
	for i := range wrongStatuses {
		go func(i int) {
			defer wg.Done()
			wrongStatuses[i], _, _ = newInvitationCall(s,
				"/api/v1/invitations/acceptance-receipt", wrong, "", nil)
		}(i)
	}
	wg.Wait()
	for _, refused := range wrongStatuses {
		if refused != 404 {
			t.Fatalf("parallel wrong password: %v", wrongStatuses)
		}
	}
	var failed int
	var lockedUntil *time.Time
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT failed_attempts,locked_until FROM iam.credential WHERE actor_id=$1`, actorID).Scan(&failed, &lockedUntil); err != nil || failed != 10 || lockedUntil == nil || !lockedUntil.After(time.Now()) {
		t.Fatalf("lockout %d %v %v", failed, lockedUntil, err)
	}
	if refused, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": invitationNewPassword}, "", nil); refused != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("locked correct password %d %v", refused, result)
	}
	if _, err := s.h.Admin.Exec(context.Background(), `UPDATE iam.credential SET locked_until=clock_timestamp()-interval '1 second' WHERE actor_id=$1`, actorID); err != nil {
		t.Fatal(err)
	}
	if recovered, result, _ := newInvitationCall(s, "/api/v1/invitations/acceptance-receipt", map[string]any{"code": code, "password": invitationNewPassword}, "", nil); recovered != 200 || result["loginHandle"] != handle {
		t.Fatalf("post-lockout recovery %d %v", recovered, result)
	}
}

func TestInvitationNewRateLimitUsesRemoteAddrBeforeWork(t *testing.T) {
	limiter := ratelimit.NewMemory(func() time.Time { return time.Unix(1000, 0) })
	work := 0
	limited := ratelimit.Middleware(limiter, ratelimit.ScopedKey("invitation.anonymous", func(*http.Request) (ratelimit.Scope, bool) { return ratelimit.Scope{}, false }),
		ratelimit.Policy{PerMinute: 12, Burst: 5}, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		work++
		w.WriteHeader(http.StatusOK)
	}))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		limited.ServeHTTP(w, r)
	})
	for i := range 6 {
		req := httptest.NewRequest(http.MethodPost, "http://example.test/api/v1/invitations/accept-new", strings.NewReader("secret"))
		req.RemoteAddr = "192.0.2.4:1234"
		req.Header.Set("Forwarded", "for=198.51.100."+strconv.Itoa(i))
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if (i < 5 && rec.Code != 200) || (i == 5 && rec.Code != 429) || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("request %d status %d", i, rec.Code)
		}
	}
	if work != 5 {
		t.Fatalf("limited request performed work: %d", work)
	}
}

func TestInvitationNewExactSiblingRoutesPreserveB1SessionPath(t *testing.T) {
	r := chi.NewRouter()
	sessionLoads := 0
	r.Group(func(anon chi.Router) {
		anon.Post("/api/v1/invitations/accept-new", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	})
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				sessionLoads++
				next.ServeHTTP(w, req)
			})
		})
		api.Route("/invitations", func(recipient chi.Router) {
			recipient.Post("/inspect", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
		})
	})
	for _, tc := range []struct {
		path                  string
		wantStatus, wantLoads int
	}{
		{"/api/v1/invitations/inspect", 204, 1},
		{"/api/v1/invitations/accept-new", 200, 1},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, nil))
		if rec.Code != tc.wantStatus || sessionLoads != tc.wantLoads {
			t.Fatalf("route %s: status=%d loads=%d", tc.path, rec.Code, sessionLoads)
		}
	}
}

func TestInvitationNewCancelAcceptanceRace(t *testing.T) {
	s := newAuthzServer(t)
	id, code := newInvitationFixture(t, s, "cancel-race-0001")
	manager, csrf := directorySession(t, s)
	stepUpDirectory(t, s, manager, csrf)
	var version int64
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT row_version FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var statuses [2]int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rec := s.do(call{method: http.MethodPost, path: "/api/v1/admin/invitations/" + id.String() + "/cancel", cookie: manager, csrf: csrf,
			headers: map[string]string{"X-Tenant-ID": s.tenantA.String(), "If-Match": fmt.Sprintf(`"%d"`, version), "Idempotency-Key": "new-cancel-race-0001"}})
		statuses[0] = rec.Code
	}()
	go func() {
		defer wg.Done()
		statuses[1], _, _ = newInvitationCall(s, "/api/v1/invitations/accept-new",
			map[string]any{"code": code, "displayName": "Cancel Race Recipient", "password": invitationNewPassword, "confirmed": true}, "new-accept-race-0001", nil)
	}()
	wg.Wait()
	validOutcome := (statuses[0] == 200 && statuses[1] == 404) || (statuses[1] == 200 && (statuses[0] == 409 || statuses[0] == 412))
	if !validOutcome {
		t.Fatalf("cancel/accept race %v", statuses)
	}
	var state string
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT status FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	var members int
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT count(*) FROM iam.tenant_membership m JOIN iam.actor a ON a.id=m.actor_id WHERE a.display_name='Cancel Race Recipient'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if (state == "ACCEPTED" && members != 1) || (state == "CANCELLED" && members != 0) {
		t.Fatalf("cancel/accept persisted state=%s members=%d", state, members)
	}
}

func TestInvitationNewExistingAcceptanceRace(t *testing.T) {
	s := newAuthzServer(t)
	id, code := newInvitationFixture(t, s, "existing-race-0001")
	proof, err := application.ParseInvitationCode(code)
	if err != nil {
		t.Fatal(err)
	}
	var statuses [2]int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.invitationRepo.AcceptExistingInvitation(context.Background(), s.actor, proof, "existing-race-accept-0001")
		statuses[0] = 500
		if err == nil {
			statuses[0] = 200
		}
		if errors.Is(err, application.ErrInvitationUnavailable) {
			statuses[0] = 404
		}
	}()
	go func() {
		defer wg.Done()
		statuses[1], _, _ = newInvitationCall(s, "/api/v1/invitations/accept-new",
			map[string]any{"code": code, "displayName": "Existing Race Recipient", "password": invitationNewPassword, "confirmed": true}, "new-existing-race-0001", nil)
	}()
	wg.Wait()
	validOutcome := (statuses[0] == 200 && statuses[1] == 404) || (statuses[1] == 200 && statuses[0] == 404)
	if !validOutcome {
		t.Fatalf("existing/new race %v", statuses)
	}
	var mode string
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT accepted_mode FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	var newActors int
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT count(*) FROM iam.actor WHERE display_name='Existing Race Recipient'`).Scan(&newActors); err != nil {
		t.Fatal(err)
	}
	if (mode == "EXISTING" && newActors != 0) || (mode == "NEW" && newActors != 1) {
		t.Fatalf("race mode=%s actors=%d", mode, newActors)
	}
}
