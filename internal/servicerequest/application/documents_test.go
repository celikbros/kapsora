package application_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/servicerequest/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// These are database fixtures, not proof of the upload/scanner pipeline. That
// pipeline owns the CLEAN verdict; this suite verifies what the request gate trusts.
type gateDocument struct {
	tenant    uuid.UUID
	provider  uuid.UUID
	request   uuid.UUID
	code      string
	aggregate string
	scan      string
	bucket    string
	purged    bool
	canonical *uuid.UUID
}

func (f *fixture) attachGateDocument(t *testing.T, in gateDocument) uuid.UUID {
	t.Helper()
	if in.tenant == uuid.Nil {
		in.tenant = f.tenant
	}
	if in.scan == "" {
		in.scan = "CLEAN"
	}
	if in.bucket == "" {
		in.bucket = "secure"
	}
	if in.aggregate == "" {
		in.aggregate = "SERVICE_REQUEST"
	}
	var owner *uuid.UUID
	if in.provider != uuid.Nil {
		owner = &in.provider
	}
	id := uuid.New()
	digest := make([]byte, 32)
	copy(digest, id[:])
	f.h.AdminExec(`INSERT INTO document.object
        (id, tenant_id, object_key, bucket, original_filename, content_type,
         scan_status, byte_size, sha256, owner_tenant_organization_id, purged_at, duplicate_of_object_id)
        VALUES ($1,$2,$3,$4,'request-test.pdf','application/pdf',$5,10,$6,$7,
                CASE WHEN $8::boolean THEN clock_timestamp() ELSE NULL END,$9)`,
		id, in.tenant, id.String(), in.bucket, in.scan, digest, owner, in.purged, in.canonical)
	f.h.AdminExec(`INSERT INTO document.link
        (tenant_id, object_id, aggregate_type, aggregate_id, document_type_code)
        VALUES ($1,$2,$3,$4,$5)`, in.tenant, id, in.aggregate, in.request, in.code)
	return id
}

func TestDocumentGateCountsOnlyCleanRetainedAttachmentsOfThisRequest(t *testing.T) {
	f := newFixture(t)
	f.publishRule(t, "DOCUMENT", "DOCUMENTS_REQUIRED", "itemCount > 0",
		`[{"type":"REQUIRE_DOCUMENT","payload":{"documentTypeCodes":["INVOICE","REPORT"]}}]`)
	before := f.ledgerState(t)
	otherTenant := f.h.CreateTenant("OTHER_DOCUMENT_GATE")
	for _, tc := range []struct {
		name            string
		document        gateDocument
		otherRequest    bool
		duplicate       bool
		canonicalPurged bool
		satisfied       bool
	}{
		{name: "clean", satisfied: true},
		{name: "pending", document: gateDocument{scan: "PENDING", bucket: "quarantine"}},
		{name: "scanning", document: gateDocument{scan: "SCANNING", bucket: "quarantine"}},
		{name: "failed", document: gateDocument{scan: "FAILED", bucket: "quarantine"}},
		{name: "infected", document: gateDocument{scan: "INFECTED", bucket: "quarantine"}},
		{name: "clean but not promoted", document: gateDocument{bucket: "quarantine"}},
		{name: "purged", document: gateDocument{purged: true}},
		{name: "different request", otherRequest: true},
		{name: "different aggregate", document: gateDocument{aggregate: "CLAIM"}},
		{name: "wrong document type", document: gateDocument{code: "OTHER"}},
		{name: "different provider", document: gateDocument{provider: f.otherOr}},
		{name: "different tenant", document: gateDocument{tenant: otherTenant}},
		{name: "retained duplicate", duplicate: true, satisfied: true},
		{name: "purged canonical bytes", duplicate: true, canonicalPurged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft := f.create(t)
			doc := tc.document
			doc.request = draft.Request.ID
			if doc.code == "" {
				doc.code = "INVOICE"
			}
			if doc.tenant == uuid.Nil && doc.provider == uuid.Nil {
				doc.provider = f.providerOr
			}
			if tc.otherRequest {
				doc.request = f.create(t).Request.ID
			}
			if tc.duplicate {
				canonical := f.attachGateDocument(t, gateDocument{
					request: uuid.New(), code: "OTHER", purged: tc.canonicalPurged,
				})
				doc.canonical = &canonical
			}
			invoice := f.attachGateDocument(t, doc)
			report := f.attachGateDocument(t, gateDocument{
				request: draft.Request.ID, code: "REPORT", provider: f.providerOr,
			})
			submitted := f.submit(t, draft)
			want := domain.StatusPendingDocument
			if tc.satisfied {
				want = domain.StatusPendingReview
			}
			if submitted.Request.Status != want {
				t.Fatalf("status = %s, want %s", submitted.Request.Status, want)
			}
			if len(submitted.Request.RequiredDocumentTypes) != 2 {
				t.Fatalf("requirements lost: %v", submitted.Request.RequiredDocumentTypes)
			}
			version, err := f.svc.GetVersion(context.Background(), f.rc(), draft.Request.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			var frozen struct {
				Gate struct {
					DocumentEvidence []application.DocumentEvidence `json:"documentEvidence"`
				} `json:"gate"`
			}
			if err := json.Unmarshal(version.Version.Snapshot, &frozen); err != nil {
				t.Fatal(err)
			}
			evidence := map[uuid.UUID]bool{}
			for _, item := range frozen.Gate.DocumentEvidence {
				evidence[item.DocumentID] = true
			}
			wantCount := 1
			if tc.satisfied {
				wantCount = 2
			}
			if len(evidence) != wantCount || !evidence[report] || evidence[invoice] != tc.satisfied {
				t.Fatalf("unexpected frozen evidence: %+v", frozen.Gate.DocumentEvidence)
			}
		})
	}
	if !equalLedger(before, f.ledgerState(t)) {
		t.Fatal("document gate changed entitlement ledger")
	}
}

func TestReturnedDocumentRequestCanProceedWithoutChangingFrozenHistory(t *testing.T) {
	f := newFixture(t)
	f.publishRule(t, "DOCUMENT", "INVOICE_REQUIRED", "itemCount > 0",
		`[{"type":"REQUIRE_DOCUMENT","payload":{"documentTypeCode":"INVOICE"}}]`)
	before := f.ledgerState(t)
	first := f.submit(t, f.create(t))
	if first.Request.Status != domain.StatusPendingDocument {
		t.Fatal(first.Request.Status)
	}
	original, err := f.svc.GetVersion(context.Background(), f.rc(), first.Request.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	attached := f.attachGateDocument(t, gateDocument{request: first.Request.ID, code: "INVOICE", provider: f.providerOr})
	// Uploading alone does not decide a frozen request. The existing explicit return
	// opens a new version, and submission evaluates the evidence in that version.
	returned, err := f.svc.Return(context.Background(), f.rc(), first.Request.ID, application.ReasonInput{
		ReasonCode: "DOCUMENT_COMPLETED", ExpectedVersion: first.Request.RowVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	second := f.submit(t, returned)
	if second.Request.Status != domain.StatusPendingReview || second.Request.CurrentVersionNo != 2 ||
		second.Request.Reference != first.Request.Reference {
		t.Fatalf("resubmit: %+v", second.Request)
	}
	frozen, err := f.svc.GetVersion(context.Background(), f.rc(), first.Request.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(frozen.Version.Snapshot) != string(original.Version.Snapshot) {
		t.Fatal("version 1 snapshot changed")
	}
	// A removed link must not erase the reason version 2 was accepted.
	f.h.AdminExec(`DELETE FROM document.link WHERE tenant_id=$1 AND object_id=$2`, f.tenant, attached)
	secondVersion, err := f.svc.GetVersion(context.Background(), f.rc(), first.Request.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Gate struct {
			DocumentEvidence []application.DocumentEvidence `json:"documentEvidence"`
		} `json:"gate"`
	}
	if err := json.Unmarshal(secondVersion.Version.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Gate.DocumentEvidence) != 1 || snapshot.Gate.DocumentEvidence[0].DocumentID != attached {
		t.Fatal("version 2 lost its evidence")
	}
	returned, err = f.svc.Return(context.Background(), f.rc(), first.Request.ID, application.ReasonInput{
		ReasonCode: "RECHECK_DOCUMENT", ExpectedVersion: second.Request.RowVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if third := f.submit(t, returned); third.Request.Status != domain.StatusPendingDocument {
		t.Fatal("unlinked document still satisfied requirement")
	}

	f.autoApprove(t)
	draft := f.create(t)
	f.attachGateDocument(t, gateDocument{request: draft.Request.ID, code: "INVOICE"})
	approved := f.submit(t, draft)
	if approved.Request.Status != domain.StatusApproved || approved.Items[0].Status != domain.ItemApproved ||
		approved.Items[0].ApprovedQuantity == nil {
		t.Fatal("complete documents did not reach automatic approval")
	}
	// A clean attachment satisfies DOCUMENT only; it cannot override a PREAUTH rule.
	f.publishRule(t, "PREAUTH", "PREAUTH_REQUIRED", "itemCount > 0",
		`[{"type":"REQUIRE_PREAUTH","payload":{}}]`)
	draft = f.create(t)
	f.attachGateDocument(t, gateDocument{request: draft.Request.ID, code: "INVOICE"})
	if reviewed := f.submit(t, draft); reviewed.Request.Status != domain.StatusPendingReview {
		t.Fatal("document bypassed required medical review")
	}
	if !equalLedger(before, f.ledgerState(t)) {
		t.Fatal("document correction/approval changed entitlement ledger")
	}
}
