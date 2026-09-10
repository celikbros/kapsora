// Package settings holds the tenant settings an export reads, and the defaults a tenant that has
// never configured one gets (WP-I7-05 section 2.1).
//
// They are keys of platform.tenant_setting rather than columns of a table of their own, for the
// reason WP-I7-02's settings give: a tenant that has never thought about how long an export link
// should live ought to need no row at all to ask for one, and a second place to state the same
// fact is a second place for it to be wrong.
//
// The defaults live here rather than in SQL for the matching reason: a default written into the
// schema exists once per tenant and can silently disagree with what the code assumes when a new
// tenant is provisioned.
package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// KeyExportTTLHours is how long a finished export stays downloadable. It is the tenant's because
// "how long may a spreadsheet of our settlements sit on a link" is a policy of the payer's
// security officer rather than a fact about the software.
const KeyExportTTLHours = "report.export_ttl_hours"

// Keys is every key this package reads, in a stable order. The loader asks for exactly these.
var Keys = []string{KeyExportTTLHours}

// DefaultExportTTLHours is twenty-four hours (WP-I7-05 section 2.1). Long enough that somebody
// who asked for a report at five in the afternoon can still open it the next morning, short
// enough that a link forwarded in an e-mail stops working before it is forwarded again.
const DefaultExportTTLHours = 24

// The bounds outside which a configured TTL is a typo rather than a policy. One hour is the
// shortest that leaves the worker time to render; ninety days is longer than anybody types on
// purpose, and honouring it silently would make the TTL an acceptance criterion this package
// fails without anybody noticing.
const (
	minExportTTLHours = 1
	maxExportTTLHours = 24 * 90
)

// Values is the set as the export reads it.
type Values struct {
	ExportTTL time.Duration
}

// Defaults returns the documented answer for a tenant that has configured nothing.
func Defaults() Values {
	return Values{ExportTTL: time.Duration(DefaultExportTTLHours) * time.Hour}
}

// Load reads the tenant's own values inside the caller's transaction, falling back to the default
// for every key the tenant has not set or has set to something outside its bounds.
func Load(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (Values, error) {
	rows, err := sqlcgen.New(tx).ListTenantSettings(ctx, sqlcgen.ListTenantSettingsParams{
		TenantID: tenantID, SettingKeys: Keys,
	})
	if err != nil {
		return Values{}, fmt.Errorf("report: read tenant settings: %w", err)
	}
	raw := make(map[string]string, len(rows))
	for _, r := range rows {
		raw[r.SettingKey] = r.Value
	}
	return FromMap(raw), nil
}

// FromMap turns raw setting values into the pair, applying the same fallbacks Load does. It is
// exported so the rule can be tested without a database, and so the one place a value is
// interpreted is the one place a test can point at.
func FromMap(raw map[string]string) Values {
	out := Defaults()
	if hours, ok := ttlHours(raw[KeyExportTTLHours]); ok {
		out.ExportTTL = time.Duration(hours) * time.Hour
	}
	return out
}

// ttlHours reads a configured TTL, refusing anything that is not a whole number of hours inside
// the bound. A setting nobody can interpret is a setting nobody set.
func ttlHours(rawValue string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(rawValue))
	if err != nil || n < minExportTTLHours || n > maxExportTTLHours {
		return 0, false
	}
	return n, true
}
