// Package outbox implements the transactional outbox (v1.2 section 23, ADR-009).
// Events are inserted in the business transaction and dispatched at least once by
// kapsora-worker; handlers must be idempotent.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

var eventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$`)

// Event is what a module publishes. Payload must be JSON-serialisable and free of
// personal data beyond identifiers; consumers load details through their own ports.
type Event struct {
	TenantID         uuid.NullUUID
	AggregateType    string
	AggregateID      uuid.UUID
	Type             string // module.aggregate.action, e.g. "organization.relationship.created"
	SchemaVersion    int    // defaults to 1
	Payload          any
	Headers          map[string]string
	DeduplicationKey string    // optional; duplicates within (tenant, type) are ignored
	AvailableAt      time.Time // optional delay; zero means now
}

// Publish inserts the event into system.outbox_event inside tx. It returns
// published=false when a deduplication key matched an existing event.
func Publish(ctx context.Context, tx pgx.Tx, ev Event) (id uuid.UUID, published bool, err error) {
	if !eventTypePattern.MatchString(ev.Type) {
		return uuid.Nil, false, fmt.Errorf("outbox: invalid event type %q", ev.Type)
	}
	if ev.AggregateType == "" || ev.AggregateID == uuid.Nil {
		return uuid.Nil, false, errors.New("outbox: aggregate type and id are required")
	}
	payload := []byte("{}")
	if ev.Payload != nil {
		if payload, err = json.Marshal(ev.Payload); err != nil {
			return uuid.Nil, false, fmt.Errorf("outbox: encode payload: %w", err)
		}
	}
	headers := []byte("{}")
	if len(ev.Headers) > 0 {
		if headers, err = json.Marshal(ev.Headers); err != nil {
			return uuid.Nil, false, fmt.Errorf("outbox: encode headers: %w", err)
		}
	}
	const maxSchemaVersion = 10_000
	if ev.SchemaVersion < 0 || ev.SchemaVersion > maxSchemaVersion {
		return uuid.Nil, false, fmt.Errorf("outbox: schema version %d out of range", ev.SchemaVersion)
	}
	version := int32(1)
	if ev.SchemaVersion > 0 {
		version = int32(ev.SchemaVersion)
	}
	availableAt := ev.AvailableAt
	if availableAt.IsZero() {
		availableAt = time.Now()
	}
	var dedupe *string
	if ev.DeduplicationKey != "" {
		dedupe = &ev.DeduplicationKey
	}

	id, err = sqlcgen.New(tx).InsertOutboxEvent(ctx, sqlcgen.InsertOutboxEventParams{
		TenantID:           ev.TenantID,
		AggregateType:      ev.AggregateType,
		AggregateID:        ev.AggregateID,
		EventType:          ev.Type,
		EventSchemaVersion: version,
		PayloadJson:        payload,
		HeadersJson:        headers,
		AvailableAt:        availableAt,
		DeduplicationKey:   dedupe,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("outbox: publish %s: %w", ev.Type, err)
	}
	return id, true, nil
}
