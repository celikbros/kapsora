package outbox

import (
	"context"
	"errors"
	"fmt"
)

// All is the handler for an event more than one module subscribes to. The dispatcher keeps
// one handler per event type, and it is right to: two registrations under one name are
// almost always a mistake, and it panics at start-up rather than letting the second one
// quietly win. When two modules really do follow the same event, cmd/worker registers All of
// them under that one name.
//
// Every handler runs, in the order given, whatever the ones before it returned. A bug in
// one subscriber must not stop the others hearing about the event; a decided request whose
// booking side failed still has an admission side that needs to move.
//
// The outcome is the one the dispatcher needs:
//   - nil when every handler returned nil;
//   - retryable when any failure is retryable, because the event must come back. The whole
//     set runs again on the next delivery, which is why **every handler given to All must be
//     idempotent** -- the ones that succeeded the first time run a second time;
//   - SECURITY when every failure is permanent and one of them is a security failure;
//   - PERMANENT when every failure is permanent.
//
// A panic in one handler is caught here and counts as that handler's permanent failure, so
// the handlers after it still run.
func All(handlers ...HandlerFunc) HandlerFunc {
	if len(handlers) == 0 {
		panic("outbox: All needs at least one handler")
	}
	for i, h := range handlers {
		if h == nil {
			panic(fmt.Sprintf("outbox: All handler %d is nil", i))
		}
	}
	return func(ctx context.Context, d Delivery) error {
		var (
			errs      []error
			retryable bool
			rate      bool
			security  bool
		)
		for i, h := range handlers {
			err := runOne(ctx, h, d)
			if err == nil {
				continue
			}
			errs = append(errs, fmt.Errorf("handler %d: %w", i, err))
			switch KindOf(err) {
			case KindPermanent:
			case KindSecurity:
				security = true
			case KindRateLimited:
				retryable, rate = true, true
			default:
				retryable = true
			}
		}
		if len(errs) == 0 {
			return nil
		}
		// The joined error may hold kinds that disagree; the outer wrapper is the one
		// KindOf reads, because errors.As looks at the error itself before its children.
		joined := errors.Join(errs...)
		switch {
		case rate:
			return RateLimited(joined)
		case retryable:
			return Transient(joined)
		case security:
			return Security(joined)
		default:
			return Permanent(joined)
		}
	}
}

func runOne(ctx context.Context, h HandlerFunc, d Delivery) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = Permanent(fmt.Errorf("handler panic: %v", rec))
		}
	}()
	return h(ctx, d)
}
