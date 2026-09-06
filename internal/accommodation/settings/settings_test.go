package settings_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/settings"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// TestDefaultsAreTheDocumentedNumbers pins the six values WP-I6-04 section 2.3 states. It
// is a literal restatement on purpose: these are the numbers a tenant gets without saying
// anything, and changing one silently would change how long every hold in every unconfigured
// tenant stands.
func TestDefaultsAreTheDocumentedNumbers(t *testing.T) {
	got := settings.Defaults()
	if got.HoldMinutes != 15 || got.QuoteTTLMinutes != 60 {
		t.Errorf("hold=%d quoteTTL=%d, want 15 and 60", got.HoldMinutes, got.QuoteTTLMinutes)
	}
	if got.CheckInEarlyHours != 6 || got.CheckInLateHours != 24 {
		t.Errorf("early=%d late=%d, want 6 and 24", got.CheckInEarlyHours, got.CheckInLateHours)
	}
	if got.MaxNights != 30 {
		t.Errorf("maxNights=%d, want 30", got.MaxNights)
	}
	if got.StepUpMemberAmount != "500" {
		t.Errorf("stepUpMemberAmount=%q, want the exact decimal 500", got.StepUpMemberAmount)
	}
	if len(settings.Keys) != 6 {
		t.Errorf("the key list has %d entries, want 6", len(settings.Keys))
	}
}

// TestFromMapTakesTheTenantsValueAndRefusesNonsense is the whole interpretation rule. Each
// nonsense case has to fall back rather than be honoured: a hold of a hundred thousand
// minutes is a typo, and honouring it would leave rooms held for two months by a member who
// closed the tab.
func TestFromMapTakesTheTenantsValueAndRefusesNonsense(t *testing.T) {
	configured := settings.FromMap(map[string]string{
		settings.KeyHoldMinutes:        "20",
		settings.KeyQuoteTTLMinutes:    "90",
		settings.KeyCheckInEarlyHours:  "0",
		settings.KeyCheckInLateHours:   "12",
		settings.KeyMaxNights:          "45",
		settings.KeyStepUpMemberAmount: "1500.50",
	})
	want := settings.Values{
		HoldMinutes: 20, QuoteTTLMinutes: 90, CheckInEarlyHours: 0,
		CheckInLateHours: 12, MaxNights: 45, StepUpMemberAmount: "1500.5",
	}
	if configured != want {
		t.Fatalf("configured = %+v, want %+v", configured, want)
	}

	defaults := settings.Defaults()
	for name, raw := range map[string]map[string]string{
		"a hold of no minutes":                   {settings.KeyHoldMinutes: "0"},
		"a hold longer than a day":               {settings.KeyHoldMinutes: "100000"},
		"a negative hold":                        {settings.KeyHoldMinutes: "-5"},
		"a hold that is not a number":            {settings.KeyHoldMinutes: "onbeş"},
		"a stay of a thousand nights":            {settings.KeyMaxNights: "1000"},
		"a check-in window of a week":            {settings.KeyCheckInLateHours: "168"},
		"a threshold that is a word":             {settings.KeyStepUpMemberAmount: "beşyüz"},
		"a negative threshold":                   {settings.KeyStepUpMemberAmount: "-1"},
		"a threshold with a thousands separator": {settings.KeyStepUpMemberAmount: "1,500"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := settings.FromMap(raw); got != defaults {
				t.Fatalf("%s was honoured: %+v", name, got)
			}
		})
	}

	// An empty map is the tenant that has configured nothing at all.
	if got := settings.FromMap(nil); got != defaults {
		t.Fatalf("an unconfigured tenant got %+v", got)
	}
}

// TestFromMapKeepsTheThresholdExact is the money rule at this boundary: the threshold
// decides whether somebody is asked for their password again, and two tenants who typed
// "500" and "500.000" must be compared against the same number.
func TestFromMapKeepsTheThresholdExact(t *testing.T) {
	for _, raw := range []string{"500", "500.0", "500.000000"} {
		got := settings.FromMap(map[string]string{settings.KeyStepUpMemberAmount: raw}).StepUpMemberAmount
		if got != "500" {
			t.Errorf("%q canonicalised to %q, want 500", raw, got)
		}
	}
	got := settings.FromMap(map[string]string{settings.KeyStepUpMemberAmount: "0.1"}).StepUpMemberAmount
	if got != "0.1" {
		t.Errorf("0.1 became %q; a threshold must not be rounded", got)
	}
}

// TestLoadReadsTheTenantsOwnValuesAndNobodyElses is the database half: the two acceptance
// cases of section 2.3 (a tenant with none gets the defaults; a tenant's own values are
// read) plus the isolation that makes them meaningful.
func TestLoadReadsTheTenantsOwnValuesAndNobodyElses(t *testing.T) {
	h := dbtest.New(t)
	configured := h.CreateTenant("ACC_SET_A")
	bare := h.CreateTenant("ACC_SET_B")

	write := func(tenantID uuid.UUID, key string, value any) {
		t.Helper()
		ctx, cancel := h.Ctx()
		defer cancel()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode %s: %v", key, err)
		}
		if _, err := h.Admin.Exec(ctx, `
			INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
			VALUES ($1, $2, $3)`, tenantID, key, encoded); err != nil {
			t.Fatalf("write %s: %v", key, err)
		}
	}
	write(configured, settings.KeyHoldMinutes, 20)
	write(configured, settings.KeyStepUpMemberAmount, "1500.50")
	// A key left unset stays at the default even when its neighbours are configured: a
	// tenant configures one thing at a time.
	write(bare, settings.KeyHoldMinutes, 45)

	load := func(tenantID uuid.UUID) settings.Values {
		t.Helper()
		ctx, cancel := h.Ctx()
		defer cancel()
		var out settings.Values
		if err := db.WithTenantTx(ctx, h.App, db.TenantContext{TenantID: tenantID},
			func(ctx context.Context, tx pgx.Tx) error {
				var err error
				out, err = settings.Load(ctx, tx, tenantID)
				return err
			}); err != nil {
			t.Fatalf("load settings: %v", err)
		}
		return out
	}

	got := load(configured)
	if got.HoldMinutes != 20 {
		t.Errorf("hold minutes = %d, want the tenant's 20", got.HoldMinutes)
	}
	if got.StepUpMemberAmount != "1500.5" {
		t.Errorf("threshold = %q, want the tenant's 1500.5", got.StepUpMemberAmount)
	}
	if got.QuoteTTLMinutes != settings.DefaultQuoteTTLMinutes || got.MaxNights != settings.DefaultMaxNights {
		t.Errorf("unset keys did not fall back: %+v", got)
	}

	// The other tenant's rows are not this tenant's. This is what would go red if the
	// query dropped its tenant_id predicate and leaned on RLS in a context that had none.
	other := load(bare)
	if other.HoldMinutes != 45 {
		t.Errorf("the second tenant's hold minutes = %d, want 45", other.HoldMinutes)
	}
	if other.StepUpMemberAmount != settings.DefaultStepUpMemberAmount {
		t.Errorf("the second tenant read the first tenant's threshold: %q", other.StepUpMemberAmount)
	}

	// And a tenant that has configured nothing at all gets exactly the defaults.
	empty := h.CreateTenant("ACC_SET_C")
	if got := load(empty); got != settings.Defaults() {
		t.Fatalf("an unconfigured tenant got %+v, want the defaults", got)
	}
}
