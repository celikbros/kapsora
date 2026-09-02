package auditpg

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func TestRecorderWritesSanitizedRowsInsideTenantTransaction(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("AUD_REC")
	actor := uuid.New()
	rec := New()

	meta := audit.RequestMeta{
		RequestID: uuid.NullUUID{UUID: uuid.New(), Valid: true},
		TraceID:   "abc123",
		SourceIP:  netip.MustParseAddr("198.51.100.9"),
	}

	err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
		ctx = audit.WithRequestMeta(ctx, meta)
		if err := rec.Record(ctx, tx, audit.Event{
			TenantID:     uuid.NullUUID{UUID: tenant, Valid: true},
			ActorID:      uuid.NullUUID{UUID: actor, Valid: true},
			Category:     audit.CategoryBusiness,
			ActionCode:   "organization.create",
			ResourceType: "organization",
			ResourceID:   uuid.NullUUID{UUID: uuid.New(), Valid: true},
			Outcome:      audit.OutcomeSuccess,
			Detail:       map[string]any{"line_count": 2, "tckn": "12345678901", "customer_email": "x@y.z"},
		}); err != nil {
			return err
		}
		return rec.RecordAccess(ctx, tx, audit.AccessEvent{
			TenantID:       tenant,
			ActorID:        actor,
			PersonID:       uuid.NullUUID{UUID: uuid.New(), Valid: true},
			ResourceType:   "person",
			AccessType:     audit.AccessView,
			Classification: audit.ClassPersonal,
			PurposeCode:    "CLAIM_REVIEW",
			Outcome:        audit.OutcomeSuccess,
		})
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	ctx, cancel := h.Ctx()
	defer cancel()
	var detail string
	var trace string
	var ip string
	if err := h.Admin.QueryRow(ctx, `
		SELECT detail_json::text, trace_id, host(source_ip)
		  FROM audit.event WHERE tenant_id = $1 AND action_code = 'organization.create'`, tenant).Scan(&detail, &trace, &ip); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if detail != `{"line_count": 2}` {
		t.Fatalf("detail not sanitised: %s", detail)
	}
	if trace != "abc123" || ip != "198.51.100.9" {
		t.Fatalf("meta not copied: trace=%q ip=%q", trace, ip)
	}

	var accessCount int
	if err := h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.access_event WHERE tenant_id = $1 AND access_type = 'VIEW'`, tenant).Scan(&accessCount); err != nil {
		t.Fatalf("read access event: %v", err)
	}
	if accessCount != 1 {
		t.Fatalf("access events = %d, want 1", accessCount)
	}
}

func TestRecorderRollsBackWithBusinessTransaction(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("AUD_RB")
	rec := New()
	sentinel := errors.New("business failure")

	err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
		if err := rec.Record(ctx, tx, audit.Event{
			TenantID:   uuid.NullUUID{UUID: tenant, Valid: true},
			Category:   audit.CategoryAdmin,
			ActionCode: "tenant.setting.update",
			Outcome:    audit.OutcomeSuccess,
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected business failure, got %v", err)
	}

	ctx, cancel := h.Ctx()
	defer cancel()
	var n int
	if err := h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id = $1`, tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("audit row survived a rolled back transaction")
	}
}

func TestRecorderRejectsIncompleteEvents(t *testing.T) {
	rec := New()
	if err := rec.Record(context.Background(), nil, audit.Event{ActionCode: "x"}); err == nil {
		t.Fatal("expected validation error")
	}
	if err := rec.RecordAccess(context.Background(), nil, audit.AccessEvent{}); err == nil {
		t.Fatal("expected validation error")
	}
}
