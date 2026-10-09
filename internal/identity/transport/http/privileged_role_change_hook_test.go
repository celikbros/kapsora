package identityhttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
)

// A trusted isolated fixture places the committed business receipt after B's
// first preflight but before its generic claim returns. This exercises the
// post-claim StoredResultGate branch, not a production process crash.
func TestDirectoryRoleChangeReceiptAppearsDuringGenericClaimWait(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	ctx := context.Background()
	path := "/api/v1/admin/role-change-requests/" + f.requestID + "/approve"
	const originalKey = "role-change-hook-original-0001"
	const stagingKey = "role-change-hook-staging-0001"
	const waitingKey = "role-change-hook-waiting-0001"
	callApproval := func(key string) *roleChangeHookResponse {
		rec := f.s.do(call{method: http.MethodPost, path: path, cookie: f.checkerCookie, csrf: f.checkerCSRF,
			headers: map[string]string{identityhttp.TenantHeader: f.s.tenantA.String(), "If-Match": f.requestETag, "Idempotency-Key": key}, body: `{}`})
		return &roleChangeHookResponse{status: rec.Code, body: append([]byte(nil), rec.Body.Bytes()...), etag: rec.Header().Get("ETag"), replayed: rec.Header().Get("Idempotent-Replayed")}
	}
	first := callApproval(originalKey)
	if first.status != 200 || first.etag == "" {
		t.Fatalf("initial approval: status=%d body=%s ETag=%q", first.status, first.body, first.etag)
	}
	var checker uuid.UUID
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject='role-change-checker'`).Scan(&checker); err != nil {
		t.Fatal(err)
	}
	originalHash := sha256.Sum256([]byte(originalKey))
	waitingHash := sha256.Sum256([]byte(waitingKey))
	// The committed staging row lets UPDATE reserve waitingKey without acquiring
	// a foreign-key lock on platform.tenant during the request's first preflight.
	if _, err := f.s.h.Admin.Exec(ctx, `INSERT INTO system.idempotency_record(tenant_id,actor_id,command_code,idempotency_key,request_hash) SELECT tenant_id,actor_id,'role_change.approve',$4,request_hash FROM iam.role_change_command_receipt WHERE tenant_id=$1 AND actor_id=$2 AND command_code='APPROVE' AND key_hash=$3`, f.s.tenantA, checker, originalHash[:], stagingKey); err != nil {
		t.Fatal(err)
	}
	block, err := f.s.h.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = block.Rollback(ctx) }()
	if _, err := block.Exec(ctx, `UPDATE system.idempotency_record SET idempotency_key=$4 WHERE tenant_id=$1 AND actor_id=$2 AND command_code='role_change.approve' AND idempotency_key=$3`, f.s.tenantA, checker, stagingKey, waitingKey); err != nil {
		t.Fatal(err)
	}
	finished := make(chan *roleChangeHookResponse, 1)
	go func() { finished <- callApproval(waitingKey) }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var waiting bool
		if err := f.s.h.Admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%system.idempotency_record%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case got := <-finished:
			t.Fatalf("request completed before generic claim barrier: %d %s", got.status, got.body)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("request did not wait in generic idempotency claim")
		}
		time.Sleep(15 * time.Millisecond)
	}
	// This separate transaction makes a matching business receipt visible only
	// after first preflight. Bind the checker actor for migration 000058's guard.
	receiptTx, err := f.s.h.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receiptTx.Rollback(ctx) }()
	if _, err := receiptTx.Exec(ctx, `SELECT set_config('app.tenant_id',$1,true),set_config('app.actor_id',$2,true)`, f.s.tenantA.String(), checker.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := receiptTx.Exec(ctx, `INSERT INTO iam.role_change_command_receipt(tenant_id,actor_id,command_code,key_hash,request_hash,request_id,response_status,response_etag,response_body) SELECT tenant_id,actor_id,command_code,$4,request_hash,request_id,response_status,response_etag,response_body FROM iam.role_change_command_receipt WHERE tenant_id=$1 AND actor_id=$2 AND command_code='APPROVE' AND key_hash=$3`, f.s.tenantA, checker, originalHash[:], waitingHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := receiptTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case replay := <-finished:
		if replay.status != first.status || replay.etag != first.etag || replay.replayed != "true" || !bytes.Equal(replay.body, first.body) {
			t.Fatalf("post-claim receipt mismatch: status=%d/%d ETag=%q/%q replayed=%q bytesEqual=%t body=%s", replay.status, first.status, replay.etag, first.etag, replay.replayed, bytes.Equal(replay.body, first.body), replay.body)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("generic claim did not return after blocked key committed")
	}
	var grants, audits, version int
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code='role_change_request.approve' AND outcome='SUCCESS'`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, f.targetMember).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if grants != 1 || audits != 1 || version != 2 {
		t.Fatalf("post-claim replay duplicated effect: grants=%d audits=%d targetVersion=%d", grants, audits, version)
	}
}

type roleChangeHookResponse struct {
	status   int
	body     []byte
	etag     string
	replayed string
}
