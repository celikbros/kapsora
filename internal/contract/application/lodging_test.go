package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func nightsInput() domain.LodgingTermsInput {
	nights := 1
	return domain.LodgingTermsInput{
		FreeCancellationHoursBefore: 48,
		PenaltyKind:                 domain.PenaltyNightsKind,
		PenaltyNights:               &nights,
		NoShowPercent:               "100",
		MinNights:                   1,
	}
}

func ptrInt(n int) *int { return &n }

// TestLodgingTermsAreWrittenOnADraftAndReadBackExactly is the ordinary path, and the
// assertion that matters in it is the last one: the two percentages come back as the exact
// decimals that went in. A numeric parsed through a float on the way out would come back as
// 42.499999999999996 and nothing else in the system would notice until somebody was
// invoiced for it.
func TestLodgingTermsAreWrittenOnADraftAndReadBackExactly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	contract := f.contract(t, "LODGE_OK")
	view := f.version(t, contract.ID, "2026-01-01", nil)

	// A version with no terms is a 404 case rather than an empty one: "not agreed yet" and
	// "agreed as free" are different answers, and a booking may be confirmed under only one
	// of them.
	if _, err := f.svc.GetLodgingTerms(ctx, f.makerRC(), view.Version.ID); !errors.Is(err, application.ErrLodgingTermsNotFound) {
		t.Fatalf("a version with no terms: err = %v, want ErrLodgingTermsNotFound", err)
	}

	in := nightsInput()
	in.PenaltyKind = domain.PenaltyPercentKind
	in.PenaltyNights = nil
	in.PenaltyPercent = "42.5000"
	in.NoShowPercent = "87.2500"
	in.HoldMinutes = ptrInt(30)
	in.MinNights = 2
	in.MaxNights = ptrInt(14)
	in.ChildFreeUnderAge = ptrInt(6)

	written, err := f.svc.PutLodgingTerms(ctx, f.makerRC(), view.Version.ID, in, view.Version.RowVersion)
	if err != nil {
		t.Fatalf("write lodging terms on a draft: %v", err)
	}
	// Writing a child of the version moves the version's ETag, so a screen holding the old
	// one has to re-read before it writes anything else.
	if written.RowVersion == view.Version.RowVersion {
		t.Fatalf("the version ETag did not move: still %d", written.RowVersion)
	}

	read, err := f.svc.GetLodgingTerms(ctx, f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("read the terms back: %v", err)
	}
	got := read.Terms
	if got.PenaltyKind != domain.PenaltyPercentKind || got.PenaltyNights != nil {
		t.Fatalf("penalty = %s/%v, want PERCENT with no nights", got.PenaltyKind, got.PenaltyNights)
	}
	if got.PenaltyPercent != "42.5" || got.NoShowPercent != "87.25" {
		t.Fatalf("percentages = %q/%q, want the exact decimals 42.5 and 87.25",
			got.PenaltyPercent, got.NoShowPercent)
	}
	if got.FreeCancellationHoursBefore != 48 || got.MinNights != 2 {
		t.Fatalf("free window = %d, min nights = %d", got.FreeCancellationHoursBefore, got.MinNights)
	}
	for name, pair := range map[string]struct{ got, want *int }{
		"holdMinutes":       {got.HoldMinutes, ptrInt(30)},
		"maxNights":         {got.MaxNights, ptrInt(14)},
		"childFreeUnderAge": {got.ChildFreeUnderAge, ptrInt(6)},
	} {
		if pair.got == nil || *pair.got != *pair.want {
			t.Fatalf("%s = %v, want %d", name, pair.got, *pair.want)
		}
	}

	// A replace is a replace: the row keeps its id and the fields that were not sent again
	// are gone rather than kept.
	second := nightsInput()
	second.FreeCancellationHoursBefore = 0
	replaced, err := f.svc.PutLodgingTerms(ctx, f.makerRC(), view.Version.ID, second, written.RowVersion)
	if err != nil {
		t.Fatalf("replace the terms: %v", err)
	}
	if replaced.Terms.ID != got.ID {
		t.Fatalf("the replace made a new row: %s then %s", got.ID, replaced.Terms.ID)
	}
	if replaced.Terms.PenaltyPercent != "" || replaced.Terms.PenaltyNights == nil {
		t.Fatalf("after replacing with NIGHTS: percent=%q nights=%v",
			replaced.Terms.PenaltyPercent, replaced.Terms.PenaltyNights)
	}
	if replaced.Terms.HoldMinutes != nil || replaced.Terms.MaxNights != nil || replaced.Terms.ChildFreeUnderAge != nil {
		t.Fatal("a replace kept fields the new set did not name")
	}
}

// TestLodgingTermsAreFrozenOnAPublishedVersion asserts the rule twice on purpose, because
// it is stated twice: once by the service, so a caller reads CONTRACT_VERSION_IMMUTABLE
// and a status word, and once by the trigger of migration 000039, which is the statement
// that still holds when the service is bypassed. The second half writes raw SQL as the
// application role precisely to bypass it.
func TestLodgingTermsAreFrozenOnAPublishedVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	contract := f.contract(t, "LODGE_FROZEN")
	view := f.version(t, contract.ID, "2026-02-01", nil)

	written, err := f.svc.PutLodgingTerms(ctx, f.makerRC(), view.Version.ID, nightsInput(), view.Version.RowVersion)
	if err != nil {
		t.Fatalf("write on the draft: %v", err)
	}
	// A version needs a priced list before it may be submitted, so the freeze this test is
	// about is reached the way a real version reaches it.
	list := f.priceList(t, application.VersionView{Version: application.VersionRecord{
		ID: view.Version.ID, RowVersion: written.RowVersion,
	}}, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})
	item := fixedItem(t, "250.500000")
	item.ServiceDefinitionID = f.physio.String()
	f.items(t, list, item)

	current, err := f.svc.GetVersion(ctx, f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload the version: %v", err)
	}
	published := f.publish(t, view.Version.ID, current.Version.RowVersion)

	// Half one: the service refuses and says why.
	if _, err := f.svc.PutLodgingTerms(ctx, f.makerRC(), view.Version.ID, nightsInput(),
		published.Version.RowVersion); !errors.Is(err, application.ErrVersionImmutable) {
		t.Fatalf("write on a published version: err = %v, want ErrVersionImmutable", err)
	}

	// Half two: the database refuses, with the service out of the way entirely.
	dbCtx, cancel := f.h.Ctx()
	defer cancel()
	tx, err := f.h.App.Begin(dbCtx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(dbCtx) }()
	if _, err := tx.Exec(dbCtx,
		`SELECT set_config('app.tenant_id', $1::text, true)`, f.tenantA.String()); err != nil {
		t.Fatalf("bind tenant: %v", err)
	}
	for name, stmt := range map[string]string{
		"update": `UPDATE contract.lodging_terms SET no_show_percent = 50
		            WHERE tenant_id = $1 AND contract_version_id = $2`,
		"delete": `DELETE FROM contract.lodging_terms
		            WHERE tenant_id = $1 AND contract_version_id = $2`,
	} {
		if _, err := tx.Exec(dbCtx, stmt, f.tenantA, view.Version.ID); err == nil {
			t.Fatalf("a raw %s on a published version's terms was accepted", name)
		}
	}
	// A fresh insert against the published version is refused too, so "delete the row and
	// write a new one" is not a way around the freeze.
	if _, err := tx.Exec(dbCtx, `
		INSERT INTO contract.lodging_terms
		    (tenant_id, contract_version_id, penalty_kind, penalty_nights, no_show_percent)
		VALUES ($1, $2, 'NIGHTS', 1, 100)`, f.tenantA, view.Version.ID); err == nil {
		t.Fatal("a raw insert against a published version was accepted")
	}
}

// TestLodgingTermsPenaltyCheckIsTheDatabases proves the exactly-one-penalty rule is a
// column CHECK and not only a Go validator: every one of these goes straight to the table.
func TestLodgingTermsPenaltyCheckIsTheDatabases(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "LODGE_CHECK")
	view := f.version(t, contract.ID, "2026-03-01", nil)

	ctx, cancel := f.h.Ctx()
	defer cancel()

	insert := func(t *testing.T, kind string, nights, percent any) error {
		t.Helper()
		_, err := f.h.Admin.Exec(ctx, `
			INSERT INTO contract.lodging_terms
			    (tenant_id, contract_version_id, penalty_kind, penalty_nights, penalty_percent, no_show_percent)
			VALUES ($1, $2, $3, $4, $5, 100)`,
			f.tenantA, view.Version.ID, kind, nights, percent)
		return err
	}
	clear := func(t *testing.T) {
		t.Helper()
		if _, err := f.h.Admin.Exec(ctx,
			`DELETE FROM contract.lodging_terms WHERE tenant_id = $1`, f.tenantA); err != nil {
			t.Fatalf("clear: %v", err)
		}
	}

	for name, tc := range map[string]struct {
		kind            string
		nights, percent any
	}{
		"NIGHTS carrying a percentage too": {"NIGHTS", 1, "10"},
		"NIGHTS carrying nothing":          {"NIGHTS", nil, nil},
		"PERCENT carrying nights too":      {"PERCENT", 1, "10"},
		"PERCENT carrying nothing":         {"PERCENT", nil, nil},
		"NIGHTS with only a percentage":    {"NIGHTS", nil, "10"},
		"PERCENT with only nights":         {"PERCENT", 2, nil},
		"a penalty kind nobody defined":    {"FIXED", 1, nil},
	} {
		t.Run(name, func(t *testing.T) {
			clear(t)
			if err := insert(t, tc.kind, tc.nights, tc.percent); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}

	// And both legitimate shapes are accepted, so the CHECK is not simply refusing
	// everything.
	clear(t)
	if err := insert(t, "NIGHTS", 2, nil); err != nil {
		t.Fatalf("a NIGHTS penalty was refused: %v", err)
	}
	clear(t)
	if err := insert(t, "PERCENT", nil, "42.5"); err != nil {
		t.Fatalf("a PERCENT penalty was refused: %v", err)
	}

	// One row per version: a second is refused by the unique constraint, which is what
	// makes "what does cancelling cost" a question with one answer.
	if err := insert(t, "PERCENT", nil, "10"); err == nil {
		t.Fatal("a second lodging terms row was accepted for one version")
	} else if !isUniqueViolation(err) {
		t.Fatalf("the second row was refused by something else: %v", err)
	}
}

// isUniqueViolation reads the SQLSTATE off a raw driver error, so the test can say the
// second row was refused by uq_lodging_terms_version rather than by the CHECK or the
// trigger. "It failed" would pass even if the constraint had been replaced by an unrelated
// one.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == dbtest.SQLStateUniqueViolation
}

// TestLodgingPolicySnapshotStampsTheVersionAndTheZone covers the shape WP-I6-02 freezes on
// a booking. The zone is on it because a free-cancellation window counted in the reader's
// own zone would be a fee that depended on who was looking.
func TestLodgingPolicySnapshotStampsTheVersionAndTheZone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	contract := f.contract(t, "LODGE_SNAP")
	view := f.version(t, contract.ID, "2026-04-01", nil)

	if _, err := f.svc.SnapshotLodgingPolicy(ctx, f.makerRC(), view.Version.ID,
		"Europe/Istanbul"); !errors.Is(err, application.ErrLodgingTermsNotFound) {
		t.Fatalf("a snapshot of a version with no terms: err = %v, want ErrLodgingTermsNotFound", err)
	}

	in := nightsInput()
	in.PenaltyNights = ptrInt(2)
	if _, err := f.svc.PutLodgingTerms(ctx, f.makerRC(), view.Version.ID, in, view.Version.RowVersion); err != nil {
		t.Fatalf("write the terms: %v", err)
	}

	snapshot, err := f.svc.SnapshotLodgingPolicy(ctx, f.makerRC(), view.Version.ID, "Europe/Istanbul")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.ContractVersionID != view.Version.ID {
		t.Fatalf("snapshot names version %s, want %s", snapshot.ContractVersionID, view.Version.ID)
	}
	if snapshot.TimeZone != "Europe/Istanbul" {
		t.Fatalf("snapshot zone = %q", snapshot.TimeZone)
	}
	if !snapshot.SnapshotAt.Equal(serviceDate.UTC()) {
		t.Fatalf("snapshot taken at %s, want the pinned clock %s", snapshot.SnapshotAt, serviceDate.UTC())
	}
	if snapshot.Terms.PenaltyNights == nil || *snapshot.Terms.PenaltyNights != 2 {
		t.Fatalf("snapshot penalty nights = %v", snapshot.Terms.PenaltyNights)
	}
	if snapshot.Terms.NoShowPercent != "100" {
		t.Fatalf("snapshot no-show percent = %q, want the exact decimal 100", snapshot.Terms.NoShowPercent)
	}
}

// TestLodgingTermsStayInsideTheirTenant is the RLS assertion every module's tests make:
// the terms of one tenant's version are invisible to another tenant, and unwritable by it.
func TestLodgingTermsStayInsideTheirTenant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	contract := f.contract(t, "LODGE_RLS")
	view := f.version(t, contract.ID, "2026-05-01", nil)
	if _, err := f.svc.PutLodgingTerms(ctx, f.makerRC(), view.Version.ID, nightsInput(),
		view.Version.RowVersion); err != nil {
		t.Fatalf("write the terms: %v", err)
	}

	other := f.rc(f.tenantB, f.maker)
	if _, err := f.svc.GetLodgingTerms(ctx, other, view.Version.ID); !errors.Is(err, application.ErrVersionNotFound) {
		t.Fatalf("another tenant read the terms: err = %v, want ErrVersionNotFound", err)
	}
	if _, err := f.svc.PutLodgingTerms(ctx, other, view.Version.ID, nightsInput(), 1); err == nil {
		t.Fatal("another tenant wrote the terms")
	}
}

// TestLodgingTermsRefuseAnInvalidSubmission keeps the validator wired: the service must not
// reach the database with a set the domain would have refused.
func TestLodgingTermsRefuseAnInvalidSubmission(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "LODGE_INVALID")
	view := f.version(t, contract.ID, "2026-06-01", nil)

	bad := nightsInput()
	bad.PenaltyPercent = "10" // NIGHTS may not carry one
	_, err := f.svc.PutLodgingTerms(context.Background(), f.makerRC(), view.Version.ID, bad,
		view.Version.RowVersion)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("an invalid set: err = %v, want a validation error", err)
	}

	// And nothing was written on the way to the refusal.
	if _, err := f.svc.GetLodgingTerms(context.Background(), f.makerRC(), view.Version.ID); !errors.Is(err, application.ErrLodgingTermsNotFound) {
		t.Fatalf("a refused write left a row: %v", err)
	}

	// A stale ETag is refused before anything is written, too.
	if _, err := f.svc.PutLodgingTerms(context.Background(), f.makerRC(), view.Version.ID,
		nightsInput(), view.Version.RowVersion+99); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("a stale ETag: err = %v, want ErrVersionMismatch", err)
	}

	if _, err := f.svc.PutLodgingTerms(context.Background(), f.makerRC(), uuid.New(),
		nightsInput(), 1); !errors.Is(err, application.ErrVersionNotFound) {
		t.Fatalf("an unknown version: err = %v, want ErrVersionNotFound", err)
	}
}
