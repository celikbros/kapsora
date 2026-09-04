package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// pricingSeed is a contract seed plus the person a quote is made for.
type pricingSeed struct {
	contractSeed
	person uuid.UUID
}

func seedPricing(h *dbtest.Harness, code string) pricingSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := pricingSeed{contractSeed: seedContract(h, code)}
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Teklif', 'Sahibi', 'teklif sahibi') RETURNING id`, s.tenant).Scan(&s.person); err != nil {
		h.T.Fatalf("seed person: %v", err)
	}
	return s
}

// insertQuote writes one quote header as the schema owner and returns its id. outcome and
// the payer and member figures are parameters because most of the tests below are about
// the CHECKs that relate them.
func insertQuote(h *dbtest.Harness, s pricingSeed, outcome, payer, member string,
	key any, expiry string,
) (uuid.UUID, error) {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx, `
		INSERT INTO contract.price_quote (
		    tenant_id, person_id, provider_profile_id, service_date, currency_code, outcome,
		    requested_amount, contract_amount, covered_amount, payer_amount, member_amount,
		    request_hash, request_snapshot, result_snapshot, expires_at, idempotency_key)
		VALUES ($1, $2, $3, '2026-06-15', 'TRY', $4,
		        500, 500, 400, $5::text::numeric, $6::text::numeric,
		        sha256('quote'::bytea), '{}'::jsonb, '{}'::jsonb,
		        clock_timestamp() + $7::interval, $8)
		RETURNING id`,
		s.tenant, s.person, s.provider, outcome, payer, member, expiry, key).Scan(&id)
	return id, err
}

// TestPriceQuoteIsAppendOnly is the whole point of storing a quote: what somebody was told
// a service would cost cannot be rewritten afterwards, or the stored number stops being
// evidence of anything.
func TestPriceQuoteIsAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	s := seedPricing(h, "QUOTE_APPEND")

	id, err := insertQuote(h, s, "QUOTED", "400", "100", nil, "72 hours")
	if err != nil {
		t.Fatalf("insert quote: %v", err)
	}
	err = h.AdminExecErr(`UPDATE contract.price_quote SET payer_amount = 0 WHERE tenant_id = $1 AND id = $2`,
		s.tenant, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "update of a stored quote")

	err = h.AdminExecErr(`DELETE FROM contract.price_quote WHERE tenant_id = $1 AND id = $2`, s.tenant, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "delete of a stored quote")

	if err := h.AdminExecErr(`
		INSERT INTO contract.price_quote_item (
		    tenant_id, price_quote_id, line_no, service_definition_id, quantity,
		    requested_amount, contract_amount, covered_amount, payer_amount, member_amount, outcome)
		VALUES ($1, $2, 1, $3, 1, 500, 500, 400, 400, 100, 'QUOTED')`,
		s.tenant, id, s.definition); err != nil {
		t.Fatalf("insert quote item: %v", err)
	}
	err = h.AdminExecErr(`UPDATE contract.price_quote_item SET payer_amount = 0 WHERE tenant_id = $1`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "update of a stored quote line")
}

// TestPriceQuoteInReviewCarriesNoMemberFigure: an operator who is shown a number reads it
// as the answer, so a quote nobody can act on must carry none at all.
func TestPriceQuoteInReviewCarriesNoMemberFigure(t *testing.T) {
	h := dbtest.New(t)
	s := seedPricing(h, "QUOTE_REVIEW")

	_, err := insertQuote(h, s, "REVIEW_REQUIRED", "400", "100", nil, "72 hours")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "review quote carrying figures")

	if _, err := insertQuote(h, s, "REVIEW_REQUIRED", "0", "0", nil, "72 hours"); err != nil {
		t.Fatalf("review quote with zero figures refused: %v", err)
	}
}

// TestPriceQuoteMustExpireAfterItWasGiven guards the one thing an expiry is for: a quote
// whose expiry is not in its own future has no lifetime at all.
func TestPriceQuoteMustExpireAfterItWasGiven(t *testing.T) {
	h := dbtest.New(t)
	s := seedPricing(h, "QUOTE_EXPIRY")

	_, err := insertQuote(h, s, "QUOTED", "400", "100", nil, "-1 hours")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "expiry before the quote")
}

// TestPriceQuoteIdempotencyKeyIsUniquePerTenant: the key is how a retried browser submit
// replays one quote instead of writing a second, so a second row under it must be
// impossible. A quote made without a key is not constrained by the others.
func TestPriceQuoteIdempotencyKeyIsUniquePerTenant(t *testing.T) {
	h := dbtest.New(t)
	s := seedPricing(h, "QUOTE_KEY")

	if _, err := insertQuote(h, s, "QUOTED", "400", "100", "quote-key-0001", "72 hours"); err != nil {
		t.Fatalf("first keyed quote: %v", err)
	}
	_, err := insertQuote(h, s, "QUOTED", "400", "100", "quote-key-0001", "72 hours")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "second quote under one key")

	for range 2 {
		if _, err := insertQuote(h, s, "QUOTED", "400", "100", nil, "72 hours"); err != nil {
			t.Fatalf("unkeyed quote refused: %v", err)
		}
	}

	// The same key in another tenant is a different question by a different customer.
	other := seedPricing(h, "QUOTE_KEY_OTHER")
	if _, err := insertQuote(h, other, "QUOTED", "400", "100", "quote-key-0001", "72 hours"); err != nil {
		t.Fatalf("same key in another tenant refused: %v", err)
	}
}

// TestPriceQuoteItemNamesExactlyOneTarget: a line prices a service or a bundle, never both
// and never neither, because the selection has to know what it is being asked about.
func TestPriceQuoteItemNamesExactlyOneTarget(t *testing.T) {
	h := dbtest.New(t)
	s := seedPricing(h, "QUOTE_TARGET")

	id, err := insertQuote(h, s, "QUOTED", "400", "100", nil, "72 hours")
	if err != nil {
		t.Fatalf("insert quote: %v", err)
	}
	err = h.AdminExecErr(`
		INSERT INTO contract.price_quote_item (
		    tenant_id, price_quote_id, line_no, quantity,
		    requested_amount, contract_amount, covered_amount, payer_amount, member_amount, outcome)
		VALUES ($1, $2, 1, 1, 500, 500, 400, 400, 100, 'QUOTED')`, s.tenant, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "quote line naming nothing")

	err = h.AdminExecErr(`
		INSERT INTO contract.price_quote_item (
		    tenant_id, price_quote_id, line_no, quantity, explanations,
		    requested_amount, contract_amount, covered_amount, payer_amount, member_amount,
		    outcome, service_definition_id)
		VALUES ($1, $2, 1, 1, '{}'::jsonb, 500, 500, 400, 400, 100, 'QUOTED', $3)`,
		s.tenant, id, s.definition)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "quote line explanations that are not an array")
}

// TestPriceQuoteIsTenantIsolated: RLS hides another tenant's quotes from the application
// role entirely, so an id from one customer answers "not found" in another rather than
// leaking that it exists. The same test is what proves migration 000023 granted the
// application role its privileges at all: without the grant every statement below would
// fail with insufficient_privilege instead.
func TestPriceQuoteIsTenantIsolated(t *testing.T) {
	h := dbtest.New(t)
	mine := seedPricing(h, "QUOTE_RLS_A")
	theirs := seedPricing(h, "QUOTE_RLS_B")

	id, err := insertQuote(h, mine, "QUOTED", "400", "100", nil, "72 hours")
	if err != nil {
		t.Fatalf("insert quote: %v", err)
	}

	count := func(tenant uuid.UUID) int {
		t.Helper()
		var n int
		if err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM contract.price_quote WHERE id = $1`, id).Scan(&n)
		}); err != nil {
			t.Fatalf("read as tenant %s: %v", tenant, err)
		}
		return n
	}
	if got := count(mine.tenant); got != 1 {
		t.Fatalf("own tenant sees %d quotes, want 1", got)
	}
	if got := count(theirs.tenant); got != 0 {
		t.Fatalf("other tenant sees %d quotes, want 0", got)
	}

	// The application role writes too, which is the half of the grant a read alone would
	// not prove: the API inserts every quote as kapsora_app, never as the schema owner.
	if err := h.AppTx(mine.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO contract.price_quote (
			    tenant_id, person_id, provider_profile_id, service_date, currency_code, outcome,
			    request_hash, request_snapshot, result_snapshot, expires_at)
			VALUES ($1, $2, $3, '2026-06-15', 'TRY', 'QUOTED',
			        sha256('app'::bytea), '{}'::jsonb, '{}'::jsonb, clock_timestamp() + interval '72 hours')`,
			mine.tenant, mine.person, mine.provider)
		return err
	}); err != nil {
		t.Fatalf("application role could not write a quote: %v", err)
	}
}
