package identityhttp_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity/application"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/mail"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

type invitationSender struct {
	messages []mail.Message
	err      error
}

func (s *invitationSender) Send(_ context.Context, m mail.Message) (mail.Result, error) {
	s.messages = append(s.messages, m)
	if s.err != nil {
		return mail.Result{}, s.err
	}
	return mail.Result{}, nil
}
func (s *invitationSender) Ping(context.Context) error { return nil }

func invitationCreateCall(s *authzServer, cookie *http.Cookie, csrf string, tenant uuid.UUID, key, email string) (int, map[string]any, http.Header) {
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/admin/invitations", cookie: cookie, csrf: csrf,
		headers: map[string]string{identityhttp.TenantHeader: tenant.String(), "Idempotency-Key": key},
		body:    `{"email":"` + email + `"}`})
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body, rec.Header()
}

func invitationProof(t *testing.T, s *authzServer, tenant, id uuid.UUID) string {
	t.Helper()
	var envelope []byte
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT delivery_cipher FROM iam.tenant_invitation WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	plain, err := s.invitationKeys.Decrypt(context.Background(), tenant, crypto.PurposeInvitationDelivery, envelope)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Code
}

func invitationPost(s *authzServer, cookie *http.Cookie, csrf, path, body, key string) (int, map[string]any) {
	headers := map[string]string{}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	rec := s.do(call{method: http.MethodPost, path: path, cookie: cookie, csrf: csrf, headers: headers, body: body})
	var result map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	return rec.Code, result
}

func TestInvitationExistingAccountConsentPrivacyAndReplay(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	managerCookie, managerCSRF := directorySession(t, s)
	const createKey = "invitation-create-case-0001"
	if status, body, _ := invitationCreateCall(s, managerCookie, managerCSRF, s.tenantA, createKey, " Recipient+Team@Example.TEST "); status != http.StatusForbidden || body["code"] != "STEP_UP_REQUIRED" {
		t.Fatalf("create without step-up: %d %v", status, body["code"])
	}
	stepUpDirectory(t, s, managerCookie, managerCSRF)
	status, body, header := invitationCreateCall(s, managerCookie, managerCSRF, s.tenantA, createKey, " Recipient+Team@Example.TEST ")
	if status != http.StatusOK || body["status"] != "PENDING" || body["deliveryStatus"] != "QUEUED" || header.Get("ETag") == "" || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %v", status, body["code"])
	}
	if len(body) != 7 {
		t.Fatalf("manager response field count %d", len(body))
	}
	id, err := uuid.Parse(body["invitationId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	code := invitationProof(t, s, s.tenantA, id)
	if !regexp.MustCompile(`^v1\.[0-9a-f-]{36}\.[0-9a-f-]{36}\.[A-Za-z0-9_-]{43}$`).MatchString(code) {
		t.Fatal("invalid code format")
	}
	if replay, again, etag := invitationCreateCall(s, managerCookie, managerCSRF, s.tenantA, createKey, "Recipient+Team@example.test"); replay != http.StatusOK || again["invitationId"] != id.String() || etag.Get("ETag") != header.Get("ETag") {
		t.Fatal("exact create replay changed the invitation")
	}
	if altered, errBody, _ := invitationCreateCall(s, managerCookie, managerCSRF, s.tenantA, createKey, "other@example.test"); altered != http.StatusConflict || errBody["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("altered create key: %d %v", altered, errBody["code"])
	}
	if duplicate, errBody, _ := invitationCreateCall(s, managerCookie, managerCSRF, s.tenantA, "invitation-create-case-0002", "Recipient+Team@example.test"); duplicate != http.StatusConflict || errBody["code"] != "INVITATION_PENDING_EXISTS" {
		t.Fatalf("duplicate contact: %d %v", duplicate, errBody["code"])
	}
	var cipher, index, fingerprint []byte
	var rows int
	if err := s.h.Admin.QueryRow(ctx, `SELECT contact_cipher,contact_hash FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&cipher, &index); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT fingerprint FROM iam.tenant_invitation_create_receipt WHERE invitation_id=$1`, id).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	plainHash := sha256.Sum256([]byte("Recipient+Team@example.test"))
	if string(cipher) == "Recipient+Team@example.test" || string(index) == string(plainHash[:]) || string(fingerprint) == string(plainHash[:]) {
		t.Fatal("invitation persisted plaintext or unkeyed contact digest")
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM system.outbox_event WHERE event_type=$1 AND aggregate_id=$2`, application.InvitationDeliveryEvent, id).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("outbox count %d err %v", rows, err)
	}
	sender := &invitationSender{}
	delivery, _ := json.Marshal(map[string]any{"invitationId": id, "generation": 1})
	event := outbox.Delivery{TenantID: uuid.NullUUID{UUID: s.tenantA, Valid: true}, AggregateID: id, Payload: delivery}
	if err := s.invitationRepo.DeliverInvitation(ctx, event, sender, "http://127.0.0.1:5181", true); err != nil {
		t.Fatal(err)
	}
	if err := s.invitationRepo.DeliverInvitation(ctx, event, sender, "http://127.0.0.1:5181", true); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || sender.messages[0].To != "Recipient+Team@example.test" || !regexp.MustCompile(`http://127\.0\.0\.1:5181/invitation`).MatchString(sender.messages[0].Body) {
		t.Fatal("restricted delivery did not hand off once")
	}
	var remaining []byte
	if err := s.h.Admin.QueryRow(ctx, `SELECT delivery_cipher FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&remaining); err != nil || len(remaining) != 0 {
		t.Fatal("delivery envelope not purged after handoff")
	}
	otherActor, err := s.svc.CreateAccount(ctx, "recipient-existing", "Recipient Existing", "recipient-existing", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantB, ActorID: otherActor, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"recipient-existing","password":"` + testPassword + `"}`})
	if login.Code != http.StatusOK {
		t.Fatalf("recipient login: %d", login.Code)
	}
	recipientCookie := sessionCookie(t, login, s.cookies.Name())
	recipientCSRF, _ := decodeBody(t, login)["csrfToken"].(string)
	if denied, _ := invitationPost(s, recipientCookie, "", "/api/v1/invitations/inspect", `{"code":"`+code+`"}`, ""); denied != http.StatusForbidden {
		t.Fatalf("inspect without CSRF: %d", denied)
	}
	if unavailable, result := invitationPost(s, recipientCookie, recipientCSRF, "/api/v1/invitations/inspect", `{"code":"v1.`+s.tenantB.String()+`.`+id.String()+`.`+code[len(code)-43:]+`"}`, ""); unavailable != http.StatusNotFound || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("forged tenant selector: %d %v", unavailable, result["code"])
	}
	if inspected, result := invitationPost(s, recipientCookie, recipientCSRF, "/api/v1/invitations/inspect", `{"code":"`+code+`"}`, ""); inspected != http.StatusOK || result["tenantDisplayName"] != "HTTP A" || result["invitationStatus"] != "PENDING" {
		t.Fatalf("inspect: %d %v", inspected, result["code"])
	}
	if denied, result := invitationPost(s, recipientCookie, recipientCSRF, "/api/v1/invitations/accept-existing", `{"code":"`+code+`","confirmed":false}`, "invitation-accept-case-0001"); denied != http.StatusBadRequest || result["code"] != "INVITATION_CONFIRMATION_REQUIRED" {
		t.Fatalf("consent required: %d %v", denied, result["code"])
	}
	acceptBody := `{"code":"` + code + `","confirmed":true}`
	joined, result := invitationPost(s, recipientCookie, recipientCSRF, "/api/v1/invitations/accept-existing", acceptBody, "invitation-accept-case-0001")
	if joined != http.StatusOK || result["tenantId"] != s.tenantA.String() || result["membershipStatus"] != "ACTIVE" || result["accessPending"] != true {
		t.Fatalf("accept: %d %v", joined, result["code"])
	}
	if replay, result := invitationPost(s, recipientCookie, recipientCSRF, "/api/v1/invitations/accept-existing", acceptBody, "invitation-accept-case-0001"); replay != http.StatusOK || result["membershipId"] == nil {
		t.Fatalf("accept replay: %d %v", replay, result["code"])
	}
	if reused, result := invitationPost(s, recipientCookie, recipientCSRF, "/api/v1/invitations/accept-existing", acceptBody, "invitation-accept-case-0002"); reused != http.StatusConflict || result["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("altered accept key: %d %v", reused, result["code"])
	}
	if inspected, result := invitationPost(s, recipientCookie, recipientCSRF, "/api/v1/invitations/inspect", `{"code":"`+code+`"}`, ""); inspected != http.StatusOK || result["invitationStatus"] != "ACCEPTED" {
		t.Fatalf("same actor accepted inspect: %d %v", inspected, result["code"])
	}
	var grants, audits int
	memberID, err := uuid.Parse(result["membershipId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, memberID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("new membership grants %d err %v", grants, err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND action_code='tenant_invitation.accept_existing' AND resource_id=$2`, s.tenantA, id).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("accept audit count %d err %v", audits, err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2 AND membership_status='ACTIVE'`, s.tenantB, otherActor).Scan(&rows); err != nil || rows != 1 {
		t.Fatal("other tenant membership changed")
	}
	if tagBody := body["status"]; tagBody != "PENDING" {
		t.Fatal("test setup response modified")
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period=tstzrange(clock_timestamp()-interval '2 days',clock_timestamp()-interval '1 day','[)')
	 WHERE tenant_id=$1 AND tenant_membership_id=(SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2)
	 AND scope_type='TENANT' AND role_id=(SELECT id FROM iam.role WHERE tenant_id=$1 AND code='TENANT_ADMIN')`, s.tenantA, s.actor); err != nil {
		t.Fatal(err)
	}
	if denied, result, _ := invitationCreateCall(s, managerCookie, managerCSRF, s.tenantA, createKey, "Recipient+Team@example.test"); denied != 403 || result["code"] != "PERMISSION_DENIED" {
		t.Fatalf("revoked manager replay: %d %v", denied, result["code"])
	}
}

func TestInvitationCancelExpiryInactiveTenantAndCompetingCreates(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := range statuses {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			statuses[n], _, _ = invitationCreateCall(s, cookie, csrf, s.tenantA, "parallel-invite-key-000"+string(rune('1'+n)), "parallel@example.test")
		}(i)
	}
	wg.Wait()
	validOutcome := (statuses[0] == 200 && statuses[1] == 409) || (statuses[1] == 200 && statuses[0] == 409)
	if !validOutcome {
		t.Fatalf("parallel create outcomes %v", statuses)
	}
	status, body, header := invitationCreateCall(s, cookie, csrf, s.tenantA, "cancel-invitation-0001", "cancel@example.test")
	if status != 200 {
		t.Fatalf("create for cancel: %d", status)
	}
	id, _ := uuid.Parse(body["invitationId"].(string))
	code := invitationProof(t, s, s.tenantA, id)
	request := call{method: http.MethodPost, path: "/api/v1/admin/invitations/" + id.String() + "/cancel", cookie: cookie, csrf: csrf,
		headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "Idempotency-Key": "cancel-invitation-req-0001", "If-Match": header.Get("ETag")}}
	if rec := s.do(request); rec.Code != 200 {
		t.Fatalf("cancel: %d", rec.Code)
	}
	if rec := s.do(request); rec.Code != 200 || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("cancel replay: %d", rec.Code)
	}
	actor, err := s.svc.CreateAccount(ctx, "invitation-inspector", "Invitation Inspector", "invitation-inspector", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = actor
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"invitation-inspector","password":"` + testPassword + `"}`})
	guest := sessionCookie(t, login, s.cookies.Name())
	guestCSRF, _ := decodeBody(t, login)["csrfToken"].(string)
	if unavailable, result := invitationPost(s, guest, guestCSRF, "/api/v1/invitations/inspect", `{"code":"`+code+`"}`, ""); unavailable != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("cancelled proof: %d %v", unavailable, result["code"])
	}
	status, body, _ = invitationCreateCall(s, cookie, csrf, s.tenantA, "expired-invitation-0001", "expired@example.test")
	if status != 200 {
		t.Fatalf("create for expiry: %d", status)
	}
	expiredID, _ := uuid.Parse(body["invitationId"].(string))
	expiredCode := invitationProof(t, s, s.tenantA, expiredID)
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_invitation SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, expiredID); err != nil {
		t.Fatal(err)
	}
	if unavailable, _ := invitationPost(s, guest, guestCSRF, "/api/v1/invitations/inspect", `{"code":"`+expiredCode+`"}`, ""); unavailable != 404 {
		t.Fatalf("expired proof: %d", unavailable)
	}
	status, _, _ = invitationCreateCall(s, cookie, csrf, s.tenantA, "expired-invitation-0002", "expired@example.test")
	if status != 200 {
		t.Fatalf("fresh invitation after elapsed pending: %d", status)
	}
	var oldStatus string
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.tenant_invitation WHERE id=$1`, expiredID).Scan(&oldStatus); err != nil || oldStatus != "EXPIRED" {
		t.Fatal("elapsed invitation not expired transactionally")
	}
	status, body, _ = invitationCreateCall(s, cookie, csrf, s.tenantA, "inactive-tenant-invite-0001", "inactive@example.test")
	if status != 200 {
		t.Fatalf("create for inactive tenant: %d", status)
	}
	inactiveID, _ := uuid.Parse(body["invitationId"].(string))
	inactiveCode := invitationProof(t, s, s.tenantA, inactiveID)
	if _, err := s.h.Admin.Exec(ctx, `UPDATE platform.tenant SET status='SUSPENDED' WHERE id=$1`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if unavailable, result := invitationPost(s, guest, guestCSRF, "/api/v1/invitations/inspect", `{"code":"`+inactiveCode+`"}`, ""); unavailable != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("inactive inspect: %d %v", unavailable, result["code"])
	}
	if unavailable, result := invitationPost(s, guest, guestCSRF, "/api/v1/invitations/accept-existing", `{"code":"`+inactiveCode+`","confirmed":true}`, "inactive-accept-key-0001"); unavailable != 404 || result["code"] != "INVITATION_UNAVAILABLE" {
		t.Fatalf("inactive accept: %d %v", unavailable, result["code"])
	}
}

func TestInvitationDeliveryFailuresAndTerminalNoSend(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	manager, csrf := directorySession(t, s)
	stepUpDirectory(t, s, manager, csrf)
	create := func(key, email string) (uuid.UUID, outbox.Delivery) {
		t.Helper()
		status, body, _ := invitationCreateCall(s, manager, csrf, s.tenantA, key, email)
		if status != 200 {
			t.Fatalf("delivery setup create: %d %v", status, body["code"])
		}
		id, err := uuid.Parse(body["invitationId"].(string))
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{"invitationId": id, "generation": 1})
		return id, outbox.Delivery{TenantID: uuid.NullUUID{UUID: s.tenantA, Valid: true}, AggregateID: id, Payload: payload}
	}
	id, event := create("delivery-transient-0001", "transient@example.test")
	code := invitationProof(t, s, s.tenantA, id)
	transient := &invitationSender{err: errors.New("SMTP echoed private code " + code)}
	if err := s.invitationRepo.DeliverInvitation(ctx, event, transient, "http://127.0.0.1:5181", true); err == nil || strings.Contains(err.Error(), code) {
		t.Fatal("transient delivery error exposed the invitation proof")
	}
	var state string
	var envelope []byte
	if err := s.h.Admin.QueryRow(ctx, `SELECT delivery_status,delivery_cipher FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&state, &envelope); err != nil || state != "QUEUED" || len(envelope) == 0 {
		t.Fatal("transient failure did not preserve bounded retry envelope")
	}
	transient.err = nil
	if err := s.invitationRepo.DeliverInvitation(ctx, event, transient, "http://127.0.0.1:5181", true); err != nil || len(transient.messages) != 2 {
		t.Fatal("transient delivery did not retry")
	}
	id, event = create("delivery-rejected-0001", "rejected@example.test")
	permanent := &invitationSender{err: errors.Join(mail.ErrRejected, errors.New("SMTP echoed private proof"))}
	if err := s.invitationRepo.DeliverInvitation(ctx, event, permanent, "http://127.0.0.1:5181", true); err != nil {
		t.Fatalf("permanent rejection metadata: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT delivery_status,delivery_cipher FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&state, &envelope); err != nil || state != "FAILED" || len(envelope) != 0 {
		t.Fatal("permanent rejection retained private envelope")
	}
	if err := s.invitationRepo.DeliverInvitation(ctx, event, permanent, "http://127.0.0.1:5181", true); err != nil || len(permanent.messages) != 1 {
		t.Fatal("failed invitation sent again")
	}
	id, event = create("delivery-cancelled-0001", "cancelled-delivery@example.test")
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_invitation SET status='CANCELLED',terminal_at=clock_timestamp(),contact_cipher=NULL,contact_hash=NULL,proof_digest=NULL,delivery_cipher=NULL,delivery_status='CANCELLED' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	noSend := &invitationSender{}
	if err := s.invitationRepo.DeliverInvitation(ctx, event, noSend, "http://127.0.0.1:5181", true); err != nil || len(noSend.messages) != 0 {
		t.Fatal("cancelled invitation was sent")
	}
	id, event = create("delivery-expired-0001", "expired-delivery@example.test")
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_invitation SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.invitationRepo.DeliverInvitation(ctx, event, noSend, "http://127.0.0.1:5181", true); err != nil || len(noSend.messages) != 0 {
		t.Fatal("expired invitation was sent")
	}
}

func TestInvitationCodeAndEmailNormalization(t *testing.T) {
	a, err := application.NormalizeInvitationEmail("  Ali+Test@Example.TEST ")
	if err != nil || a != "Ali+Test@example.test" {
		t.Fatal("email normalization")
	}
	if _, err := application.NormalizeInvitationEmail("bad\r\n@example.test"); !errors.Is(err, application.ErrInvitationInvalidEmail) {
		t.Fatal("header injection accepted")
	}
	code, digest, err := application.GenerateInvitationCode(uuid.New(), uuid.New())
	if err != nil || len(code) != 120 || !application.VerifyInvitationProof(code, digest) {
		t.Fatal("code generation")
	}
	if _, err := application.ParseInvitationCode(code); err != nil {
		t.Fatal(err)
	}
	if application.VerifyInvitationProof(code+"x", digest) {
		t.Fatal("altered proof accepted")
	}
}

func TestInvitationSuspendedMembershipAndAuditRollback(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	manager, managerCSRF := directorySession(t, s)
	stepUpDirectory(t, s, manager, managerCSRF)
	status, body, _ := invitationCreateCall(s, manager, managerCSRF, s.tenantA, "suspended-member-invite-0001", "suspended@example.test")
	if status != 200 {
		t.Fatalf("create: %d", status)
	}
	id, _ := uuid.Parse(body["invitationId"].(string))
	code := invitationProof(t, s, s.tenantA, id)
	actor, err := s.svc.CreateAccount(ctx, "suspended-recipient", "Suspended Recipient", "suspended-recipient", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: actor, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership SET membership_status='SUSPENDED' WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, actor); err != nil {
		t.Fatal(err)
	}
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"suspended-recipient","password":"` + testPassword + `"}`})
	if login.Code != 200 {
		t.Fatalf("login: %d", login.Code)
	}
	guest := sessionCookie(t, login, s.cookies.Name())
	csrf, _ := decodeBody(t, login)["csrfToken"].(string)
	if refused, result := invitationPost(s, guest, csrf, "/api/v1/invitations/accept-existing", `{"code":"`+code+`","confirmed":true}`, "suspended-accept-0001"); refused != 409 || result["code"] != "INVITATION_MEMBERSHIP_CONFLICT" {
		t.Fatalf("suspended membership reactivation: %d %v", refused, result["code"])
	}
	var memberStatus string
	if err := s.h.Admin.QueryRow(ctx, `SELECT membership_status FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, actor).Scan(&memberStatus); err != nil || memberStatus != "SUSPENDED" {
		t.Fatal("existing membership changed")
	}
	failing := identitypg.NewInvitationRepository(s.h.App, s.invitationKeys, s.invitationKeys, failingDirectoryAudit{}).WithDeliveryEnabled(true)
	proof, err := application.ParseInvitationCode(code)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.svc.CreateAccount(ctx, "audit-rollback-recipient", "Audit Rollback Recipient", "audit-rollback-recipient", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.AcceptExistingInvitation(ctx, fresh, proof, "audit-fail-accept-0001"); err == nil {
		t.Fatal("audit failure accepted invitation")
	}
	var invitationStatus string
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&invitationStatus); err != nil || invitationStatus != "PENDING" {
		t.Fatal("audit failure committed acceptance")
	}
	var rollbackMembers int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, fresh).Scan(&rollbackMembers); err != nil || rollbackMembers != 0 {
		t.Fatal("audit failure committed new zero-grant membership")
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership SET membership_status='ACTIVE' WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, actor); err != nil {
		t.Fatal(err)
	}
	if accepted, result := invitationPost(s, guest, csrf, "/api/v1/invitations/accept-existing", `{"code":"`+code+`","confirmed":true}`, "audit-pass-accept-0001"); accepted != 200 || result["accessPending"] != false {
		t.Fatalf("existing active membership not preserved: %d %v", accepted, result["code"])
	}
	var members int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, actor).Scan(&members); err != nil || members != 1 {
		t.Fatal("existing active membership was duplicated")
	}
}

func TestInvitationReadRequiresActiveActorAndCleanupPurgesSecrets(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	manager, managerCSRF := directorySession(t, s)
	stepUpDirectory(t, s, manager, managerCSRF)
	status, body, _ := invitationCreateCall(s, manager, managerCSRF, s.tenantA, "cleanup-invitation-0001", "cleanup@example.test")
	if status != 200 {
		t.Fatalf("create: %d", status)
	}
	id, _ := uuid.Parse(body["invitationId"].(string))
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_invitation SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := s.invitationRepo.CleanupInvitations(ctx); err != nil || cleaned < 1 {
		t.Fatalf("expire cleanup: %d %v", cleaned, err)
	}
	var contact, proof, envelope []byte
	var invitationStatus string
	if err := s.h.Admin.QueryRow(ctx, `SELECT status,contact_cipher,proof_digest,delivery_cipher FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&invitationStatus, &contact, &proof, &envelope); err != nil || invitationStatus != "EXPIRED" || len(contact) != 0 || len(proof) != 0 || len(envelope) != 0 {
		t.Fatal("terminal invitation retained secrets")
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_invitation SET terminal_at=clock_timestamp()-interval '31 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.invitationRepo.CleanupInvitations(ctx); err != nil {
		t.Fatal(err)
	}
	var masked *string
	if err := s.h.Admin.QueryRow(ctx, `SELECT masked_recipient FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&masked); err != nil || masked != nil {
		t.Fatal("masked recipient persisted beyond retention")
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.actor SET status='SUSPENDED' WHERE id=$1`, s.actor); err != nil {
		t.Fatal(err)
	}
	if code, result := directoryCall(s, manager, s.tenantA, "/api/v1/admin/invitations", "backoffice"); code != 403 || result["code"] != "PERMISSION_DENIED" {
		t.Fatalf("suspended global actor read invitation list: %d %v", code, result["code"])
	}
}

func TestInvitationCancelAcceptRaceHasOneWinner(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	manager, managerCSRF := directorySession(t, s)
	stepUpDirectory(t, s, manager, managerCSRF)
	created, body, header := invitationCreateCall(s, manager, managerCSRF, s.tenantA, "race-create-invitation-0001", "race@example.test")
	if created != 200 {
		t.Fatalf("create: %d", created)
	}
	id, _ := uuid.Parse(body["invitationId"].(string))
	proof := invitationProof(t, s, s.tenantA, id)
	actor, err := s.svc.CreateAccount(ctx, "race-invite-recipient", "Race Recipient", "race-invite-recipient", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"race-invite-recipient","password":"` + testPassword + `"}`})
	if login.Code != 200 {
		t.Fatalf("login: %d", login.Code)
	}
	guest := sessionCookie(t, login, s.cookies.Name())
	guestCSRF, _ := decodeBody(t, login)["csrfToken"].(string)
	var statuses [2]int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		statuses[0] = s.do(call{method: http.MethodPost, path: "/api/v1/admin/invitations/" + id.String() + "/cancel", cookie: manager, csrf: managerCSRF,
			headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "If-Match": header.Get("ETag"), "Idempotency-Key": "race-cancel-invitation-0001"}}).Code
	}()
	go func() {
		defer wg.Done()
		statuses[1], _ = invitationPost(s, guest, guestCSRF, "/api/v1/invitations/accept-existing", `{"code":"`+proof+`","confirmed":true}`, "race-accept-invitation-0001")
	}()
	wg.Wait()
	validOutcome := (statuses[0] == 200 && statuses[1] == 404) || (statuses[1] == 200 && (statuses[0] == 409 || statuses[0] == 412))
	if !validOutcome {
		t.Fatalf("race outcomes: %v", statuses)
	}
	var state string
	var members, audits int
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.tenant_invitation WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, actor).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code IN ('tenant_invitation.cancel','tenant_invitation.accept_existing')`, s.tenantA, id).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || (state == "ACCEPTED" && members != 1) || (state == "CANCELLED" && members != 0) {
		t.Fatalf("race persistence state=%s members=%d audits=%d", state, members, audits)
	}
}
