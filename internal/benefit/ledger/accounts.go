package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// EnsureResult reports what EnsureAccounts did.
type EnsureResult struct {
	PlanVersionID uuid.UUID
	// Accounts are every account the enrollment can reach on the date, opened or not.
	Accounts []uuid.UUID
	// Opened counts the accounts this call created.
	Opened int
}

// EnsureAccounts opens the entitlement accounts of an enrollment for the plan version
// published on asOf: one account per entitlement definition, with the benefit period
// derived from the definition's period type and an initial GRANT movement.
//
// It is idempotent through uq_entitlement_account_period, so the outbox consumer of
// benefit.enrollment.created and a lazy call from a reserve path can both run it and the
// second one simply finds the accounts already open. Definitions marked family_shared
// open their account on the principal's enrollment instead, so a household shares one
// balance; dependants reach it through ResolveAccounts.
func (l *Ledger) EnsureAccounts(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID, asOf time.Time) (EnsureResult, error) {
	asOf = domain.DateOnly(asOf)
	q := sqlcgen.New(tx)

	enrollment, err := q.GetEnrollmentForEntitlement(ctx, sqlcgen.GetEnrollmentForEntitlementParams{
		TenantID: tenantID, ID: enrollmentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EnsureResult{}, ErrEnrollmentNotFound
	}
	if err != nil {
		return EnsureResult{}, fmt.Errorf("benefit: read enrollment: %w", err)
	}

	version, err := application.ResolvePlanVersion(ctx, tx, tenantID, enrollment.PlanID, asOf)
	if err != nil {
		return EnsureResult{}, err
	}
	definitions, err := q.ListEntitlementDefinitions(ctx, sqlcgen.ListEntitlementDefinitionsParams{
		TenantID: tenantID, PlanVersionID: version.ID,
	})
	if err != nil {
		return EnsureResult{}, fmt.Errorf("benefit: list entitlement definitions: %w", err)
	}

	out := EnsureResult{PlanVersionID: version.ID}
	for _, def := range definitions {
		if def.Status != "ACTIVE" {
			continue
		}
		holder, err := l.accountHolder(ctx, tx, tenantID, enrollment, def.FamilyShared, asOf)
		if err != nil {
			return out, err
		}
		from, to, err := benefitPeriod(def.PeriodType, def.PeriodLength, version.ValidFrom, holder.start, asOf)
		if err != nil {
			return out, fmt.Errorf("%w: definition %s", err, def.Code)
		}

		accountID, opened, err := l.openAccount(ctx, tx, tenantID, holder.enrollmentID, def.ID, from, to)
		if err != nil {
			return out, err
		}
		out.Accounts = append(out.Accounts, accountID)
		if !opened {
			continue
		}
		out.Opened++

		initial, err := domain.ParseQuantity(def.InitialQuantity)
		if err != nil {
			return out, fmt.Errorf("benefit: definition %s initial quantity: %w", def.Code, err)
		}
		if !initial.IsPositive() {
			continue
		}
		if _, err := l.post(ctx, tx, postInput{
			TenantID: tenantID, AccountID: accountID, MovementType: MovementGrant,
			Deltas:        Balances{Total: initial, Available: initial},
			ReferenceType: ReferenceEnrollment, ReferenceID: holder.enrollmentID,
			Key: "grant:" + accountID.String(),
		}); err != nil {
			return out, err
		}
	}
	return out, nil
}

// holder is the enrollment an account is opened on and the day that enrollment started.
type holder struct {
	enrollmentID uuid.UUID
	start        time.Time
}

// accountHolder resolves the family-shared rule: a shared definition opens its account on
// the principal's enrollment in the same plan. A dependant whose principal is not
// enrolled keeps its own account, so the benefit is never silently lost.
func (l *Ledger) accountHolder(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	enrollment sqlcgen.GetEnrollmentForEntitlementRow, familyShared bool, asOf time.Time) (holder, error) {
	own := holder{enrollmentID: enrollment.ID, start: dateValue(enrollment.ValidFrom)}
	if !familyShared || !enrollment.PrincipalMembershipID.Valid {
		return own, nil
	}
	principal, err := sqlcgen.New(tx).FindPrincipalEnrollment(ctx, sqlcgen.FindPrincipalEnrollmentParams{
		TenantID: tenantID, SponsorMembershipID: enrollment.PrincipalMembershipID.UUID,
		PlanID: enrollment.PlanID, AsOf: dateOf(asOf),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return own, nil
	}
	if err != nil {
		return holder{}, fmt.Errorf("benefit: resolve principal enrollment: %w", err)
	}
	return holder{enrollmentID: principal.ID, start: dateValue(principal.ValidFrom)}, nil
}

// openAccount creates the account or reports the existing one for the same period.
func (l *Ledger) openAccount(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID, definitionID uuid.UUID,
	from time.Time, to *time.Time) (uuid.UUID, bool, error) {
	q := sqlcgen.New(tx)
	id, err := q.CreateEntitlementAccount(ctx, sqlcgen.CreateEntitlementAccountParams{
		TenantID: tenantID, EnrollmentID: enrollmentID, DefinitionID: definitionID,
		PeriodFrom: dateOf(from), PeriodTo: date(to),
	})
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, fmt.Errorf("benefit: open entitlement account: %w", err)
	}
	// ON CONFLICT DO NOTHING returned nothing: the account is already open.
	existing, err := q.FindEntitlementAccountForPeriod(ctx, sqlcgen.FindEntitlementAccountForPeriodParams{
		TenantID: tenantID, EnrollmentID: enrollmentID, DefinitionID: definitionID,
		PeriodFrom: dateOf(from), PeriodTo: date(to),
	})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("benefit: find entitlement account: %w", err)
	}
	return existing, false, nil
}

// benefitPeriod computes the half-open [from, to) window an account covers, following
// WP-I2-03 section 2.2:
//
//	CALENDAR_YEAR  January 1st of the year containing asOf, one year wide
//	PLAN_YEAR      anniversaries of the plan version's own validity start
//	ROLLING_DAYS   [asOf, asOf + period_length)
//	LIFETIME       [enrollment start, infinity)
//	CUSTOM         rejected: benefit.entitlement_definition carries no metadata column
//	               to read a custom window from, so accepting it would guess
func benefitPeriod(periodType string, periodLength *int32, versionFrom *time.Time,
	enrollmentStart, asOf time.Time) (time.Time, *time.Time, error) {
	asOf = domain.DateOnly(asOf)
	switch periodType {
	case "CALENDAR_YEAR":
		from := time.Date(asOf.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(1, 0, 0)
		return from, &to, nil
	case "PLAN_YEAR":
		if versionFrom == nil {
			return time.Time{}, nil, fmt.Errorf("%w: the plan version has no validity start", ErrPeriodUnsupported)
		}
		base := domain.DateOnly(*versionFrom)
		years := asOf.Year() - base.Year()
		if years < 0 {
			years = 0
		}
		from := base.AddDate(years, 0, 0)
		if from.After(asOf) && years > 0 {
			years--
			from = base.AddDate(years, 0, 0)
		}
		to := base.AddDate(years+1, 0, 0)
		return from, &to, nil
	case "ROLLING_DAYS":
		if periodLength == nil || *periodLength <= 0 {
			return time.Time{}, nil, fmt.Errorf("%w: ROLLING_DAYS needs a day count", ErrPeriodUnsupported)
		}
		to := asOf.AddDate(0, 0, int(*periodLength))
		return asOf, &to, nil
	case "LIFETIME":
		from := domain.DateOnly(enrollmentStart)
		if from.IsZero() {
			from = asOf
		}
		return from, nil, nil
	default:
		return time.Time{}, nil, fmt.Errorf("%w: period type %s", ErrPeriodUnsupported, periodType)
	}
}

// ResolveAccounts returns every account a person can spend from on asOf: the accounts of
// their own enrollments plus the family-shared accounts of their principal, each marked
// with whether it was reached through the principal.
func (l *Ledger) ResolveAccounts(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID, asOf time.Time) ([]Account, error) {
	rows, err := sqlcgen.New(tx).ListPersonEntitlementAccounts(ctx, sqlcgen.ListPersonEntitlementAccountsParams{
		TenantID: tenantID, PersonID: personID, AsOf: dateOf(asOf),
	})
	if err != nil {
		return nil, fmt.Errorf("benefit: list person entitlement accounts: %w", err)
	}
	out := make([]Account, 0, len(rows))
	for _, r := range rows {
		balances, err := parseBalances(r.TotalGranted, r.AvailableQuantity, r.ReservedQuantity,
			r.ConsumedQuantity, r.ExpiredQuantity)
		if err != nil {
			return nil, err
		}
		out = append(out, Account{
			ID: r.ID, EnrollmentID: r.EnrollmentID, PersonID: r.PersonID,
			PeriodFrom: dateValue(r.PeriodFrom), PeriodTo: datePtr(r.PeriodTo),
			Balances: balances, Status: r.Status, Shared: r.Shared,
			Definition: DefinitionSummary{
				ID: r.DefinitionID, Code: r.DefinitionCode, Name: r.DefinitionName,
				UnitType: r.UnitType, CurrencyCode: deref(r.CurrencyCode),
				FamilyShared: r.FamilyShared, AllowOverdraft: r.AllowOverdraft,
			},
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}
