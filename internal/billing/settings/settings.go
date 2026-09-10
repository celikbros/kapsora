// Package settings holds the tenant settings the invoice and the icmal read, and the defaults a
// tenant that has never configured one gets (WP-I7-02 section 2.1, WP-I7-03 section 2.1).
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
	"strings"

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
	// KeyBatchMinInvoices and KeyBatchMaxInvoices bound how many invoices one icmal may
	// carry at the moment it is submitted (WP-I7-03 section 2.1). They are the tenant's
	// because "how big is a batch we are willing to review in one sitting" is a policy of
	// the payer's finance department and not a fact about the software.
	KeyBatchMinInvoices = "billing.batch_min_invoices"
	KeyBatchMaxInvoices = "billing.batch_max_invoices"
	// KeyBatchDecisionThreshold is the approved total above which `decideBatch` needs a
	// second person: the submitter may never decide, and above this figure the person who
	// took the last decision may not be the one who closes the batch either. It is a money
	// value and therefore an exact decimal string, for the reason the tolerance is: a
	// threshold that rounded would be a second pair of eyes that was asked for on some
	// batches and not on others for no reason anybody could explain.
	KeyBatchDecisionThreshold = "billing.batch_decision_threshold"
)

// Keys is every key this package reads, in a stable order. The loader asks for exactly these.
var Keys = []string{
	KeyAllocationTolerance, KeyInvoiceRequiresImage,
	KeyBatchMinInvoices, KeyBatchMaxInvoices, KeyBatchDecisionThreshold,
}

// The documented defaults (WP-I7-02 section 2.1).
const (
	// DefaultAllocationTolerance is one kuruş. It is not zero because a provider's own
	// accounting package rounds its VAT to two decimals and KAPSORA's claim decisions do not,
	// and refusing every invoice that lands a kuruş out would refuse most of them.
	DefaultAllocationTolerance = "0.01"
	// DefaultInvoiceRequiresImage is true.
	DefaultInvoiceRequiresImage = true
	// DefaultBatchMinInvoices is one. A batch of one invoice is ordinary — a single large
	// document a provider wants answered on its own — and refusing it would make the icmal a
	// thing providers work around rather than through.
	DefaultBatchMinInvoices = 1
	// DefaultBatchMaxInvoices is five hundred, which is the documented bound of section 2.1
	// and also roughly what a reviewer can hold in one screen's paging.
	DefaultBatchMaxInvoices = 500
	// DefaultBatchDecisionThreshold is one hundred thousand, in the batch's own currency. It
	// is deliberately a figure a tenant is expected to change: what counts as large is what
	// their finance department says it is.
	DefaultBatchDecisionThreshold = "100000"
)

// The bounds outside which a configured batch size is a typo rather than a policy. A tenant
// that has typed a maximum of two million has said "do not check the count", and honouring
// that silently would make an acceptance criterion of this package untrue for them without
// anybody noticing.
const (
	minBatchInvoices = 1
	maxBatchInvoices = 5000
	// maxBatchDecisionThreshold is the largest threshold that is a policy rather than a
	// mistyped zero. Above it, nothing would ever need a second pair of eyes.
	maxBatchDecisionThreshold = "100000000000"
)

// maxAllocationTolerance is the largest tolerance that is a policy rather than a typo. A
// tenant that has typed a tolerance of ten thousand has said "do not check the total", and
// honouring that silently would make the acceptance criterion of this package untrue for
// them without anybody noticing. The default is the safer answer and the one this package
// returns.
const maxAllocationTolerance = "1000"

// Values is the set as the invoice and the icmal read it.
type Values struct {
	// AllocationTolerance is an exact decimal string and never a float.
	AllocationTolerance  string
	InvoiceRequiresImage bool
	BatchMinInvoices     int
	BatchMaxInvoices     int
	// BatchDecisionThreshold is an exact decimal string and never a float.
	BatchDecisionThreshold string
}

// Defaults returns the documented answer for a tenant that has configured nothing.
func Defaults() Values {
	return Values{
		AllocationTolerance:    DefaultAllocationTolerance,
		InvoiceRequiresImage:   DefaultInvoiceRequiresImage,
		BatchMinInvoices:       DefaultBatchMinInvoices,
		BatchMaxInvoices:       DefaultBatchMaxInvoices,
		BatchDecisionThreshold: DefaultBatchDecisionThreshold,
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
	out.BatchMinInvoices = count(raw[KeyBatchMinInvoices], DefaultBatchMinInvoices)
	out.BatchMaxInvoices = count(raw[KeyBatchMaxInvoices], DefaultBatchMaxInvoices)
	// A tenant that has configured a minimum above its own maximum has configured a batch
	// that can never be submitted. Both are read back as the defaults rather than one of them
	// silently winning, because which one won would be an answer nobody could predict.
	if out.BatchMinInvoices > out.BatchMaxInvoices {
		out.BatchMinInvoices = DefaultBatchMinInvoices
		out.BatchMaxInvoices = DefaultBatchMaxInvoices
	}
	out.BatchDecisionThreshold = threshold(raw[KeyBatchDecisionThreshold])
	return out
}

// count reads a configured whole number, falling back to the default for anything that is not
// one or is outside the bound. A setting nobody can interpret is a setting nobody set.
func count(rawValue string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(rawValue))
	if err != nil || n < minBatchInvoices || n > maxBatchInvoices {
		return fallback
	}
	return n
}

// threshold reads the configured second-pair-of-eyes threshold as an exact decimal,
// canonicalising it so two tenants who typed "100000" and "100000.00" get the same string
// back. A negative threshold would ask for a second person on every batch including an empty
// one, which is not a policy anybody typed on purpose.
func threshold(rawValue string) string {
	q, err := benefit.ParseQuantity(strings.TrimSpace(rawValue))
	if err != nil || q.IsNegative() ||
		q.Cmp(benefit.MustQuantity(maxBatchDecisionThreshold)) > 0 {
		return DefaultBatchDecisionThreshold
	}
	return q.String()
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
