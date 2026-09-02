package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

const testEventType = "test.aggregate.created"

func quietOptions(o Options) Options {
	o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return o
}

func publishN(t *testing.T, h *dbtest.Harness, tenant uuid.UUID, n int, eventType string) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, n)
	err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			id, ok, err := Publish(ctx, tx, Event{
				TenantID:      uuid.NullUUID{UUID: tenant, Valid: true},
				AggregateType: "test_aggregate",
				AggregateID:   uuid.New(),
				Type:          eventType,
				Payload:       map[string]any{"n": i},
			})
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("unexpected dedupe")
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return ids
}

func TestPublishRollsBackWithTransactionAndDeduplicates(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("OB_PUB")
	ctx, cancel := h.Ctx()
	defer cancel()

	tx, err := h.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := Publish(ctx, tx, Event{AggregateType: "a", AggregateID: uuid.New(), Type: testEventType})
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(ctx)
	if _, err := sqlcgen.New(h.Admin).GetOutboxEvent(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("event survived rollback: %v", err)
	}

	// Deduplication key within (tenant, type).
	err = h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
		ev := Event{TenantID: uuid.NullUUID{UUID: tenant, Valid: true}, AggregateType: "a", AggregateID: uuid.New(), Type: testEventType, DeduplicationKey: "k1"}
		if _, ok, err := Publish(ctx, tx, ev); err != nil || !ok {
			return errors.New("first publish should succeed")
		}
		if _, ok, err := Publish(ctx, tx, ev); err != nil || ok {
			return errors.New("second publish with same key should be deduplicated")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := Publish(ctx, nil, Event{Type: "Bad Type"}); err == nil {
		t.Fatal("invalid type accepted")
	}
}

func TestDispatchProcessesEachEventExactlyOnceWithConcurrentDispatchers(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("OB_ONCE")
	ids := publishN(t, h, tenant, 300, testEventType)

	var seen sync.Map
	var total atomic.Int64
	handler := func(ctx context.Context, d Delivery) error {
		if _, dup := seen.LoadOrStore(d.ID, true); dup {
			t.Errorf("event %s delivered twice", d.ID)
		}
		total.Add(1)
		return nil
	}

	newDispatcher := func(name string) *Dispatcher {
		d := New(h.App, quietOptions(Options{InstanceID: name, BatchSize: 25}))
		d.Handle(testEventType, handler)
		return d
	}
	d1, d2 := newDispatcher("one"), newDispatcher("two")

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	for _, d := range []*Dispatcher{d1, d2} {
		wg.Add(1)
		go func(d *Dispatcher) {
			defer wg.Done()
			for total.Load() < int64(len(ids)) && ctx.Err() == nil {
				if _, err := d.RunOnce(ctx); err != nil {
					t.Errorf("run once: %v", err)
					return
				}
			}
		}(d)
	}
	wg.Wait()

	if total.Load() != int64(len(ids)) {
		t.Fatalf("processed %d events, want %d", total.Load(), len(ids))
	}
	pending, _, err := d1.Backlog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("backlog = %d, want 0", pending)
	}
	row, err := sqlcgen.New(h.Admin).GetOutboxEvent(ctx, ids[0])
	if err != nil || row.Status != "SUCCEEDED" || row.ProcessedAt == nil {
		t.Fatalf("event not marked succeeded: %+v err=%v", row, err)
	}
}

func TestTransientFailuresRetryThenDeadLetter(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("OB_RETRY")
	ids := publishN(t, h, tenant, 1, testEventType)

	var calls atomic.Int32
	d := New(h.App, quietOptions(Options{MaxAttempts: 3, BaseBackoff: time.Nanosecond, MaxBackoff: time.Millisecond}))
	d.Handle(testEventType, func(ctx context.Context, del Delivery) error {
		calls.Add(1)
		return Transient(errors.New("downstream unavailable"))
	})

	ctx, cancel := h.Ctx()
	defer cancel()
	q := sqlcgen.New(h.Admin)
	for i := 1; i <= 3; i++ {
		time.Sleep(2 * time.Millisecond) // let the tiny backoff elapse
		if _, err := d.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		row, err := q.GetOutboxEvent(ctx, ids[0])
		if err != nil {
			t.Fatal(err)
		}
		want := "FAILED"
		if i == 3 {
			want = "DEAD_LETTER"
		}
		if row.Status != want || int(row.AttemptCount) != i {
			t.Fatalf("after attempt %d: status=%s attempts=%d, want %s/%d", i, row.Status, row.AttemptCount, want, i)
		}
		if row.LastErrorCode == nil || *row.LastErrorCode != "TRANSIENT" {
			t.Fatalf("error code = %v", row.LastErrorCode)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("handler called %d times, want 3", calls.Load())
	}
	// Dead-lettered events are never claimed again.
	if n, _ := d.RunOnce(ctx); n != 0 {
		t.Fatalf("dead-lettered event was claimed again")
	}
}

func TestPermanentAndPanicDeadLetterImmediately(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("OB_PERM")
	permanentIDs := publishN(t, h, tenant, 1, "test.aggregate.permanent")
	panicIDs := publishN(t, h, tenant, 1, "test.aggregate.panics")

	d := New(h.App, quietOptions(Options{}))
	d.Handle("test.aggregate.permanent", func(context.Context, Delivery) error { return Permanent(errors.New("bad payload")) })
	d.Handle("test.aggregate.panics", func(context.Context, Delivery) error { panic("boom") })

	ctx, cancel := h.Ctx()
	defer cancel()
	if _, err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	q := sqlcgen.New(h.Admin)
	for _, id := range []uuid.UUID{permanentIDs[0], panicIDs[0]} {
		row, err := q.GetOutboxEvent(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != "DEAD_LETTER" || row.AttemptCount != 1 || row.LastErrorCode == nil || *row.LastErrorCode != "PERMANENT" {
			t.Fatalf("event %s: %+v", id, row)
		}
	}
}

func TestUnknownTypeIsDeferredNotFailed(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("OB_UNK")
	ids := publishN(t, h, tenant, 1, "test.aggregate.orphan")

	d := New(h.App, quietOptions(Options{}))
	ctx, cancel := h.Ctx()
	defer cancel()
	if _, err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := sqlcgen.New(h.Admin).GetOutboxEvent(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "PENDING" || row.AttemptCount != 0 || !row.AvailableAt.After(time.Now().Add(50*time.Minute)) {
		t.Fatalf("unknown event not deferred: %+v", row)
	}
}

func TestRecoverStaleProcessingEvents(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("OB_STALE")
	ids := publishN(t, h, tenant, 1, testEventType)
	h.AdminExec(`UPDATE system.outbox_event SET status = 'PROCESSING', locked_at = clock_timestamp() - interval '30 minutes', locked_by = 'dead-worker' WHERE id = $1`, ids[0])

	d := New(h.App, quietOptions(Options{StaleAfter: 10 * time.Minute}))
	ctx, cancel := h.Ctx()
	defer cancel()
	n, err := d.RecoverStale(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recovered %d (%v), want 1", n, err)
	}
	row, err := sqlcgen.New(h.Admin).GetOutboxEvent(ctx, ids[0])
	if err != nil || row.Status != "PENDING" {
		t.Fatalf("stale event not recovered: %+v err=%v", row, err)
	}
}
