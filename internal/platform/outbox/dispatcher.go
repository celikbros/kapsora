package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Delivery is one attempt to hand an event to a handler.
type Delivery struct {
	ID            uuid.UUID
	TenantID      uuid.NullUUID
	AggregateType string
	AggregateID   uuid.UUID
	Type          string
	SchemaVersion int
	Payload       json.RawMessage
	Headers       map[string]string
	OccurredAt    time.Time
	Attempt       int
}

// HandlerFunc processes a delivery. Return nil on success; wrap errors with Permanent,
// Transient, RateLimited or Security to steer retries. Handlers must be idempotent:
// the same event can be delivered more than once.
type HandlerFunc func(ctx context.Context, d Delivery) error

// Options tunes the dispatcher; zero values take the defaults documented on each field.
type Options struct {
	InstanceID      string        // locked_by value; default hostname+pid
	BatchSize       int32         // default 100
	PollInterval    time.Duration // idle sleep when nothing was claimed; default 1s
	MaxPollInterval time.Duration // idle sleep cap after repeated empty polls; default 5s
	HandlerTimeout  time.Duration // per delivery; default 60s
	MaxAttempts     int           // dead-letter after this many attempts; default 10
	BaseBackoff     time.Duration // first retry delay; default 5s
	MaxBackoff      time.Duration // retry delay cap; default 1h
	StaleAfter      time.Duration // PROCESSING older than this is recovered; default 10m
	Logger          *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.InstanceID == "" {
		host, _ := os.Hostname()
		o.InstanceID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 100
	}
	if o.PollInterval <= 0 {
		o.PollInterval = time.Second
	}
	if o.MaxPollInterval < o.PollInterval {
		o.MaxPollInterval = 5 * time.Second
	}
	if o.HandlerTimeout <= 0 {
		o.HandlerTimeout = 60 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 10
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 5 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = time.Hour
	}
	if o.StaleAfter <= 0 {
		o.StaleAfter = 10 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Dispatcher claims batches with FOR UPDATE SKIP LOCKED and runs handlers. Several
// dispatchers (processes) can run concurrently against the same table.
type Dispatcher struct {
	pool *pgxpool.Pool
	opts Options

	mu            sync.RWMutex
	handlers      map[string]HandlerFunc
	unknownLogged map[string]time.Time
}

// New creates a dispatcher; register handlers with Handle before Run.
func New(pool *pgxpool.Pool, opts Options) *Dispatcher {
	return &Dispatcher{
		pool:          pool,
		opts:          opts.withDefaults(),
		handlers:      map[string]HandlerFunc{},
		unknownLogged: map[string]time.Time{},
	}
}

// Handle registers the handler for an event type; registering twice panics.
func (d *Dispatcher) Handle(eventType string, h HandlerFunc) {
	if !eventTypePattern.MatchString(eventType) {
		panic(fmt.Sprintf("outbox: invalid event type %q", eventType))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, dup := d.handlers[eventType]; dup {
		panic(fmt.Sprintf("outbox: handler for %q registered twice", eventType))
	}
	d.handlers[eventType] = h
}

// Run polls until ctx is cancelled. Empty polls back off up to MaxPollInterval.
func (d *Dispatcher) Run(ctx context.Context) error {
	sleep := d.opts.PollInterval
	for {
		n, err := d.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.opts.Logger.Warn("outbox poll failed", "error", err)
		}
		if n > 0 {
			sleep = d.opts.PollInterval
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(sleep):
		}
		sleep = min(sleep*2, d.opts.MaxPollInterval)
	}
}

// RunOnce claims one batch and processes it sequentially; it returns the number of
// events claimed.
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	rows, err := sqlcgen.New(d.pool).ClaimOutboxEvents(ctx, sqlcgen.ClaimOutboxEventsParams{
		Limit:    d.opts.BatchSize,
		LockedBy: &d.opts.InstanceID,
	})
	if err != nil {
		return 0, fmt.Errorf("outbox: claim batch: %w", err)
	}
	for _, row := range rows {
		d.process(ctx, row)
	}
	return len(rows), nil
}

// RecoverStale returns PROCESSING rows abandoned by a crashed worker to PENDING.
func (d *Dispatcher) RecoverStale(ctx context.Context) (int64, error) {
	before := time.Now().Add(-d.opts.StaleAfter)
	return sqlcgen.New(d.pool).RecoverStaleOutboxEvents(ctx, &before)
}

// Backlog reports pending/failed events and the age of the oldest one (zero when empty).
func (d *Dispatcher) Backlog(ctx context.Context) (pending int64, oldestAge time.Duration, err error) {
	row, err := sqlcgen.New(d.pool).OutboxBacklog(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("outbox: backlog: %w", err)
	}
	if row.Pending > 0 {
		oldestAge = max(time.Since(row.OldestAvailableAt), 0)
	}
	return row.Pending, oldestAge, nil
}

func (d *Dispatcher) handler(eventType string) (HandlerFunc, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	h, ok := d.handlers[eventType]
	return h, ok
}

func (d *Dispatcher) process(ctx context.Context, row sqlcgen.ClaimOutboxEventsRow) {
	q := sqlcgen.New(d.pool)
	logger := d.opts.Logger.With("event_id", row.ID, "event_type", row.EventType, "attempt", row.AttemptCount)

	h, ok := d.handler(row.EventType)
	if !ok {
		d.deferUnknown(ctx, q, row, logger)
		return
	}

	delivery := Delivery{
		ID:            row.ID,
		TenantID:      row.TenantID,
		AggregateType: row.AggregateType,
		AggregateID:   row.AggregateID,
		Type:          row.EventType,
		SchemaVersion: int(row.EventSchemaVersion),
		Payload:       json.RawMessage(row.PayloadJson),
		OccurredAt:    row.OccurredAt,
		Attempt:       int(row.AttemptCount),
	}
	if len(row.HeadersJson) > 0 {
		_ = json.Unmarshal(row.HeadersJson, &delivery.Headers)
	}

	start := time.Now()
	err := d.callHandler(ctx, h, delivery)
	if err == nil {
		if err := q.MarkOutboxSucceeded(ctx, row.ID); err != nil {
			logger.Error("outbox: mark succeeded failed", "error", err)
		}
		logger.Debug("outbox event processed", "duration_ms", time.Since(start).Milliseconds())
		return
	}

	kind := KindOf(err)
	attempt := int(row.AttemptCount)
	status := "FAILED"
	availableAt := time.Now().Add(Backoff(attempt, kind, d.opts.BaseBackoff, d.opts.MaxBackoff, rand.Float64))
	if kind == KindPermanent || kind == KindSecurity || attempt >= d.opts.MaxAttempts {
		status = "DEAD_LETTER"
		availableAt = time.Now()
	}
	code := kind.String()
	message := truncate(err.Error(), 500)
	if mErr := q.MarkOutboxFailed(ctx, sqlcgen.MarkOutboxFailedParams{
		ID:               row.ID,
		Status:           status,
		AvailableAt:      availableAt,
		LastErrorCode:    &code,
		LastErrorMessage: &message,
	}); mErr != nil {
		logger.Error("outbox: mark failed failed", "error", mErr)
	}
	level := slog.LevelWarn
	if kind == KindSecurity || status == "DEAD_LETTER" {
		level = slog.LevelError
	}
	logger.Log(ctx, level, "outbox event failed", "kind", code, "status", status, "error_code", code)
}

func (d *Dispatcher) callHandler(ctx context.Context, h HandlerFunc, delivery Delivery) (err error) {
	hctx, cancel := context.WithTimeout(ctx, d.opts.HandlerTimeout)
	defer cancel()
	defer func() {
		if rec := recover(); rec != nil {
			d.opts.Logger.Error("outbox handler panicked", "event_id", delivery.ID, "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			err = Permanent(fmt.Errorf("handler panic: %v", rec))
		}
	}()
	return h(hctx, delivery)
}

// deferUnknown leaves the event pending for an hour and logs once per type per hour.
func (d *Dispatcher) deferUnknown(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.ClaimOutboxEventsRow, logger *slog.Logger) {
	code := "NO_HANDLER"
	if err := q.DeferOutboxEvent(ctx, sqlcgen.DeferOutboxEventParams{
		ID:            row.ID,
		AvailableAt:   time.Now().Add(time.Hour),
		LastErrorCode: &code,
	}); err != nil {
		logger.Error("outbox: defer unknown event failed", "error", err)
	}
	d.mu.Lock()
	last, seen := d.unknownLogged[row.EventType]
	if !seen || time.Since(last) > time.Hour {
		d.unknownLogged[row.EventType] = time.Now()
		d.mu.Unlock()
		logger.Warn("outbox: no handler registered for event type; deferred one hour")
		return
	}
	d.mu.Unlock()
}

// Backoff returns the delay before retry number attempt+1: base * 2^(attempt-1) with
// +/-20% jitter, capped at max. Rate-limited failures start from four times the base.
func Backoff(attempt int, kind Kind, base, maxDelay time.Duration, random func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	if kind == KindRateLimited {
		delay = 4 * base
	}
	for i := 1; i < attempt && delay < maxDelay; i++ {
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	jitter := 1 + (random()*0.4 - 0.2)
	return time.Duration(float64(delay) * jitter)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ErrNoHandler is returned by helpers when a type has no handler; exported for tests.
var ErrNoHandler = errors.New("outbox: no handler")
