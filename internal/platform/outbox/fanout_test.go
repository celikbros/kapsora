package outbox

import (
	"context"
	"errors"
	"testing"
)

func TestAllRunsEveryHandlerAndPicksTheKindTheDispatcherNeeds(t *testing.T) {
	ok := func(context.Context, Delivery) error { return nil }
	fail := func(wrap func(error) error) HandlerFunc {
		return func(context.Context, Delivery) error { return wrap(errors.New("boom")) }
	}
	plain := func(context.Context, Delivery) error { return errors.New("plain") }
	boom := func(context.Context, Delivery) error { panic("kaboom") }

	cases := []struct {
		name     string
		handlers []HandlerFunc
		wantNil  bool
		want     Kind
	}{
		{"all succeed", []HandlerFunc{ok, ok}, true, 0},
		{"one transient, one ok", []HandlerFunc{fail(Transient), ok}, false, KindTransient},
		{"an unclassified error is retryable", []HandlerFunc{ok, plain}, false, KindTransient},
		{"permanent and transient retries", []HandlerFunc{fail(Permanent), fail(Transient)}, false, KindTransient},
		{"rate limited wins over transient", []HandlerFunc{fail(Transient), fail(RateLimited)}, false, KindRateLimited},
		{"all permanent", []HandlerFunc{fail(Permanent), fail(Permanent)}, false, KindPermanent},
		{"security among permanents", []HandlerFunc{fail(Permanent), fail(Security)}, false, KindSecurity},
		{"a panic is permanent", []HandlerFunc{boom, ok}, false, KindPermanent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			counted := make([]HandlerFunc, len(tc.handlers))
			for i, h := range tc.handlers {
				counted[i] = func(ctx context.Context, d Delivery) error { calls++; return h(ctx, d) }
			}
			err := All(counted...)(context.Background(), Delivery{})
			if calls != len(tc.handlers) {
				t.Fatalf("ran %d of %d handlers", calls, len(tc.handlers))
			}
			if tc.wantNil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			if got := KindOf(err); got != tc.want {
				t.Fatalf("kind = %s, want %s (%v)", got, tc.want, err)
			}
		})
	}
}

func TestAllRefusesAnEmptyOrNilSet(t *testing.T) {
	for name, hs := range map[string][]HandlerFunc{"empty": nil, "nil entry": {nil}} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("All did not panic")
				}
			}()
			All(hs...)
		})
	}
}
