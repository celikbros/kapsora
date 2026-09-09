// Package settings holds the two tenant settings the invoice reads, and the defaults a
// tenant that has never configured one gets (WP-I7-02 section 2.1).
//
// They are keys of platform.tenant_setting rather than columns of a table of their own, for
// the reason migration 000033 gives for the inpatient window and WP-I6-04 gives for the hold:
// a tenant that has never thought about how close an invoice has to add up ought to need no
// row at all to raise one, and a second place to state the same fact is a second place for it
// to be wrong.
//
// The defaults live here rather than in SQL for the matching reason: a default written into
// the schema exists once per tenant and can silently disagree with what the code assumes when
// a new tenant is provisioned.
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
	// KeyAllocationTolerance is how far the sum of an invoice's allocations may sit from its
	// payable amount and still be submitted. It is a money value and therefore an exact
	// decimal string, like every other amount in this system: a tolerance that rounded would
	// be a gate that opened on some invoices and not on others for no reason anybody could
	// explain.
	KeyAllocationTolerance = "billing.allocation_tolerance"
	// KeyInvoiceRequiresImage is whether a submitted invoice has to carry a scan of the
	// document the provider actually issued. True by default, because the payer's reviewer
	// is being asked to accept a figure and "show me the invoice" is the first thing they
	// will ask; a tenant whose providers integrate by API can turn it off.
	KeyInvoiceRequiresImage = "billing.invoice_requires_image"
)

// Keys is every key this package reads, in a stable order. The loader asks for exactly these.
var Keys = []string{KeyAllocationTolerance, KeyInvoiceRequiresImage}

// The documented defaults (WP-I7-02 section 2.1).
const (
	// DefaultAllocationTolerance is one kuruş. It is not zero because a provider's own
	// accounting package rounds its VAT to two decimals and KAPSORA's claim decisions do not,
	// and refusing every invoice that lands a kuruş out would refuse most of them.
	DefaultAllocationTolerance = "0.01"
	// DefaultInvoiceRequiresImage is true.
	DefaultInvoiceRequiresImage = true
)

// maxAllocationTolerance is the largest tolerance that is a policy rather than a typo. A
// tenant that has typed a tolerance of ten thousand has said "do not check the total", and
// honouring that silently would make the acceptance criterion of this package untrue for
// them without anybody noticing. The default is the safer answer and the one this package
// returns.
const maxAllocationTolerance = "1000"

// Values is the pair as the invoice reads it.
type Values struct {
	// AllocationTolerance is an exact decimal string and never a float.
	AllocationTolerance  string
	InvoiceRequiresImage bool
}

// Defaults returns the documented answer for a tenant that has configured nothing.
func Defaults() Values {
	return Values{
		AllocationTolerance:  DefaultAllocationTolerance,
		InvoiceRequiresImage: DefaultInvoiceRequiresImage,
	}
}

// Load reads the tenant's own values inside the caller's transaction, falling back to the
// default for every key the tenant has not set or has set to something outside its bounds.
//
// One round trip for both: they are read together on every submit, and two queries where one
// will do is one more chance for the gate to be built from a half-changed configuration.
func Load(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (Values, error) {
	rows, err := sqlcgen.New(tx).ListTenantSettings(ctx, sqlcgen.ListTenantSettingsParams{
		TenantID: tenantID, SettingKeys: Keys,
	})
	if err != nil {
		return Values{}, fmt.Errorf("billing: read tenant settings: %w", err)
	}
	raw := make(map[string]string, len(rows))
	for _, r := range rows {
		raw[r.SettingKey] = r.Value
	}
	return FromMap(raw), nil
}

// FromMap turns raw setting values into the pair, applying the same fallbacks Load does. It is
// exported so the rules can be tested without a database, and so the one place a value is
// interpreted is the one place a test can point at.
func FromMap(raw map[string]string) Values {
	out := Defaults()
	out.AllocationTolerance = tolerance(raw[KeyAllocationTolerance])
	out.InvoiceRequiresImage = boolean(raw[KeyInvoiceRequiresImage], DefaultInvoiceRequiresImage)
	return out
}

// tolerance reads the configured tolerance as an exact decimal, canonicalising it so two
// tenants who typed "0.01" and "0.010000" get the same string back. A value that is not a
// number, is negative, or is outside the bound is the default: a setting nobody can interpret
// is a setting nobody set.
func tolerance(rawValue string) string {
	q, err := benefit.ParseQuantity(rawValue)
	if err != nil || q.IsNegative() || q.Cmp(benefit.MustQuantity(maxAllocationTolerance)) > 0 {
		return DefaultAllocationTolerance
	}
	return q.String()
}

// boolean reads a flag a tenant configured. The setting is stored as JSON, so it arrives as
// `true` or `false`; anything else is the default.
func boolean(rawValue string, fallback bool) bool {
	value, err := strconv.ParseBool(rawValue)
	if err != nil {
		return fallback
	}
	return value
}
