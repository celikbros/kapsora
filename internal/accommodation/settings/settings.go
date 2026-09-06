// Package settings holds the tenant settings the accommodation vertical reads, and the
// defaults a tenant that has never configured one gets (WP-I6-04 section 2.3).
//
// They are keys of platform.tenant_setting rather than columns of a table of their own,
// for the reason migration 000033 gives for the inpatient window: a tenant that has never
// thought about how long a hold should stand ought to need no row at all to be admitted,
// and a second place to state the same fact is a second place for it to be wrong.
//
// The defaults live here rather than in SQL for the matching reason: a default written
// into the schema is a default that exists once per tenant and can silently disagree with
// what the code assumes when a new tenant is provisioned. Here there is one answer, and a
// tenant's own value overrides it.
package settings

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefit "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The keys, exactly as they appear in platform.tenant_setting.
const (
	// KeyHoldMinutes is how long a held room stands before the sweeper releases it. A
	// provider may override it for itself through contract.lodging_terms.hold_minutes;
	// this is the tenant's answer for every provider that has not.
	KeyHoldMinutes = "accommodation.hold_minutes"
	// KeyQuoteTTLMinutes is how long a priced availability answer may be acted on.
	KeyQuoteTTLMinutes = "accommodation.quote_ttl_minutes"
	// KeyCheckInEarlyHours and KeyCheckInLateHours bound when a check-in may be recorded
	// against the booked day: a guest arriving in the morning and one arriving near
	// midnight are both ordinary, and where the line falls is the tenant's own answer.
	KeyCheckInEarlyHours = "accommodation.checkin_early_hours"
	KeyCheckInLateHours  = "accommodation.checkin_late_hours"
	// KeyMaxNights caps a stay when the contract names no maximum of its own.
	KeyMaxNights = "accommodation.max_nights"
	// KeyStepUpMemberAmount is the member share above which confirming a booking takes a
	// re-entered password. It is a money value and therefore an exact decimal string, like
	// every other amount in this system.
	KeyStepUpMemberAmount = "accommodation.stepup_member_amount"
)

// Keys is every key this package reads, in a stable order. The seed walks it and the
// loader asks for exactly these.
var Keys = []string{
	KeyHoldMinutes, KeyQuoteTTLMinutes, KeyCheckInEarlyHours,
	KeyCheckInLateHours, KeyMaxNights, KeyStepUpMemberAmount,
}

// The documented defaults (WP-I6-04 section 2.3).
const (
	DefaultHoldMinutes        = 15
	DefaultQuoteTTLMinutes    = 60
	DefaultCheckInEarlyHours  = 6
	DefaultCheckInLateHours   = 24
	DefaultMaxNights          = 30
	DefaultStepUpMemberAmount = "500"
)

// The bounds a configured value has to be inside to be believed. A tenant that has typed a
// hold of a hundred thousand minutes has made a typo rather than a policy, and honouring it
// would mean rooms nobody can book and nobody can release; the default is the safer answer
// and the one this package returns.
const (
	maxHoldMinutes     = 1440
	maxQuoteTTLMinutes = 1440
	maxCheckInHours    = 48
	maxNightsCeiling   = 365
)

// Values is the whole set as the vertical reads it.
type Values struct {
	HoldMinutes       int
	QuoteTTLMinutes   int
	CheckInEarlyHours int
	CheckInLateHours  int
	MaxNights         int
	// StepUpMemberAmount is an exact decimal string and never a float: it is compared
	// against a member share that decides whether somebody is asked for their password
	// again, and a threshold that rounded would be a step-up that fired on some amounts
	// and not on others for no reason anybody could explain.
	StepUpMemberAmount string
}

// Defaults returns the documented answer for a tenant that has configured nothing.
func Defaults() Values {
	return Values{
		HoldMinutes:        DefaultHoldMinutes,
		QuoteTTLMinutes:    DefaultQuoteTTLMinutes,
		CheckInEarlyHours:  DefaultCheckInEarlyHours,
		CheckInLateHours:   DefaultCheckInLateHours,
		MaxNights:          DefaultMaxNights,
		StepUpMemberAmount: DefaultStepUpMemberAmount,
	}
}

// Load reads the tenant's own values inside the caller's transaction, falling back to the
// default for every key the tenant has not set or has set to something outside its bounds.
//
// One round trip for all six: they are read together on every availability search, and six
// queries where one will do is six chances for the answer to be built from a half-changed
// configuration.
func Load(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (Values, error) {
	rows, err := sqlcgen.New(tx).ListTenantSettings(ctx, sqlcgen.ListTenantSettingsParams{
		TenantID: tenantID, SettingKeys: Keys,
	})
	if err != nil {
		return Values{}, fmt.Errorf("accommodation: read tenant settings: %w", err)
	}
	raw := make(map[string]string, len(rows))
	for _, r := range rows {
		raw[r.SettingKey] = r.Value
	}
	return FromMap(raw), nil
}

// FromMap turns raw setting values into the set, applying the same fallbacks Load does. It
// is exported so the rules can be tested without a database, and so the one place a value
// is interpreted is the one place a test can point at.
func FromMap(raw map[string]string) Values {
	out := Defaults()
	out.HoldMinutes = boundedInt(raw[KeyHoldMinutes], 1, maxHoldMinutes, DefaultHoldMinutes)
	out.QuoteTTLMinutes = boundedInt(raw[KeyQuoteTTLMinutes], 1, maxQuoteTTLMinutes, DefaultQuoteTTLMinutes)
	out.CheckInEarlyHours = boundedInt(raw[KeyCheckInEarlyHours], 0, maxCheckInHours, DefaultCheckInEarlyHours)
	out.CheckInLateHours = boundedInt(raw[KeyCheckInLateHours], 0, maxCheckInHours, DefaultCheckInLateHours)
	out.MaxNights = boundedInt(raw[KeyMaxNights], 1, maxNightsCeiling, DefaultMaxNights)
	out.StepUpMemberAmount = amount(raw[KeyStepUpMemberAmount], DefaultStepUpMemberAmount)
	return out
}

// boundedInt reads a whole number a tenant configured, or the default. A value that is not
// a number, is negative, or is outside the bound is the default: a setting nobody can
// interpret is a setting nobody set.
func boundedInt(rawValue string, low, high, fallback int) int {
	n, err := strconv.Atoi(rawValue)
	if err != nil || n < low || n > high {
		return fallback
	}
	return n
}

// amount reads a money threshold as an exact decimal, canonicalising it so two tenants who
// typed "500" and "500.00" get the same string back and a comparison against a member share
// cannot depend on how the value was typed. Nothing here goes through a float.
func amount(rawValue, fallback string) string {
	q, err := benefit.ParseQuantity(rawValue)
	if err != nil || q.IsNegative() {
		return fallback
	}
	return q.String()
}
