package application

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// DecidedEvent is the outbox event a decided request publishes. It is the seam WP-I5-03
// hangs an inpatient stay off: a medical reviewer decides an admission on the request page,
// exactly where they decide every other request, and the stay follows without the reviewer
// ever learning that a stay exists.
//
// It is an outbox event rather than a call into whatever cares, and that is the whole point.
// A call would mean this package knowing about admissions, then about claims, then about
// whatever comes next; an event means it knows about none of them, and a consumer that is
// down or slow cannot make a reviewer's decision fail.
const DecidedEvent = "service_request.decided"

// decidedAggregate is the aggregate type the event names.
const decidedAggregate = "service_request"

// publishDecided writes the outbox row a decision owes, inside the decision's own
// transaction. A decision that rolls back has published nothing, and a published event is
// therefore always a decision that actually happened.
//
// The payload is identifiers and a status word. There is no reason text, no review comment
// and no line of the request in it: an outbox payload is read by every consumer and by
// anybody who can see the table, and v1.2 section 23 says the same thing — consumers load
// what they need through their own ports.
//
// The deduplication key is the request and the status it landed on, so a redelivered command
// publishes once and two different decisions on one request are two events.
func (s *Service) publishDecided(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	request RequestRecord, status string,
) error {
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: decidedAggregate,
		AggregateID:   request.ID,
		Type:          DecidedEvent,
		Payload: map[string]any{
			"serviceRequestId": request.ID,
			"requestType":      request.RequestType,
			"status":           status,
			"personId":         request.PersonID,
			"versionNo":        request.CurrentVersionNo,
		},
		DeduplicationKey: request.ID.String() + ":" + status,
	})
	return err
}
