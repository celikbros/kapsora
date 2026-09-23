// Package application turns a request for a number into a stored quote: it gathers the
// eligibility answer, the selected contract price, the entitlement balance and the rule
// adjustments for every requested line, hands them to the pure calculation in
// internal/pricing, and writes the result down so the number quoted and the number later
// claimed can be compared.
//
// The calculation itself lives one package up and is never reimplemented here. This layer
// is the loading, the ordering and the persistence around it, and it has one property it
// guards above all the others: a quote reserves nothing and moves no balance. Nothing in
// this package opens an entitlement account, posts a ledger entry or takes a reservation,
// and the tests count the ledger rows before and after to keep it that way.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/rules/engine"
)

// PermissionQuote guards both operations (migration 000023). It lives here rather than in
// the transport because whether a provider-scoped actor may read somebody else's quote is
// a business rule, not a routing detail.
const PermissionQuote = "pricing.quote"

// PurposeQuote is the purpose code of the access audit rows this package writes.
const PurposeQuote = "PRICE_QUOTE"

// ResourceQuote is the audited resource type.
const ResourceQuote = "price_quote"

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles, the same
// constant the eligibility check compares against.
const ScopeOrganization = eligibility.ScopeOrganization

// Disclaimer is carried on every quote, in the response and in the stored snapshot. It is
// a sentence rather than a flag because the person who needs it is standing at a counter
// reading a screen, not writing code against the field.
const Disclaimer = "Bu bir fiyat teklifidir; onay, tahsis veya hak taahhüdü değildir. " +
	"Hiçbir bakiye ayrılmaz ve hiçbir hak kullanılmış sayılmaz."

// Errors mapped by the transport layer to problem codes.
var (
	// ErrQuoteNotFound is a quote this caller cannot see. An unknown id, a foreign
	// tenant's id and another provider's quote are deliberately indistinguishable.
	ErrQuoteNotFound = errors.New("pricing: price quote not found")
	// ErrProviderNotFound is a provider profile that does not exist in this tenant.
	ErrProviderNotFound = errors.New("pricing: provider profile not found")
	// ErrLocationNotFound is a location that is not this provider's.
	ErrLocationNotFound = errors.New("pricing: location does not belong to this provider")
	// ErrEvaluationNotFound is an eligibility evaluation that is not this person's.
	ErrEvaluationNotFound = errors.New("pricing: eligibility evaluation not found for this person")
	// ErrPackageNotFound is a package definition that does not exist in this tenant.
	ErrPackageNotFound = errors.New("pricing: package definition not found")
	// ErrIdempotencyKeyReuse is the same Idempotency-Key with a different request.
	ErrIdempotencyKeyReuse = errors.New("pricing: idempotency key reused with different arguments")
	// ErrProviderScope refuses a provider-scoped actor quoting or reading outside its own
	// organization.
	ErrProviderScope = errors.New("pricing: provider-scoped actor may only quote for its own provider")
	// ErrSerializationFailure is a REPEATABLE READ transaction that lost a race. The
	// request is safe to retry unchanged, which is what the problem detail says.
	ErrSerializationFailure = errors.New("pricing: quote transaction could not be serialized")
)

// ProviderRecord is the part of provider.provider_profile a quote needs: enough to know
// the profile exists and which organization a provider-scoped actor must be inside.
type ProviderRecord struct {
	ID                   uuid.UUID
	TenantOrganizationID uuid.UUID
	Status               string
}

// EligibilityInput is everything the pure eligibility resolver reads, loaded in one
// transaction. It is the resolver's own Input with the plan version already looked up.
type EligibilityInput struct {
	Person      eligibility.Person
	Memberships []eligibility.Membership
	Enrollments []eligibility.Enrollment
	// PlanVersion is nil when no version was published on the service date.
	PlanVersion *eligibility.PlanVersion
	// Accounts are the entitlement accounts the person can already spend from. Accounts
	// that were never opened are simply absent: a quote must not open one, because
	// opening an account posts a GRANT movement and the ledger has to stay untouched.
	Accounts []eligibility.Account
	Mappings map[uuid.UUID]eligibility.Mapping
}

// PriceRuleVersion is one published PRICE rule set version with its rules, ready to be
// compiled by the engine.
type PriceRuleVersion struct {
	ID          uuid.UUID
	RuleSetID   uuid.UUID
	RuleSetCode string
	VersionNo   int
	InputSchema map[string]string
	Rules       []engine.Rule
}

// QuoteRow is the append-only header as it is written.
type QuoteRow struct {
	ID                      uuid.UUID
	PersonID                uuid.UUID
	ProgramID               *uuid.UUID
	ProviderProfileID       uuid.UUID
	LocationID              *uuid.UUID
	ServiceDate             time.Time
	CurrencyCode            string
	Outcome                 string
	Requested               benefitdomain.Quantity
	Contract                benefitdomain.Quantity
	Covered                 benefitdomain.Quantity
	Payer                   benefitdomain.Quantity
	Member                  benefitdomain.Quantity
	RequestHash             []byte
	RequestSnapshot         []byte
	ResultSnapshot          []byte
	ContractVersionID       *uuid.UUID
	PlanVersionID           *uuid.UUID
	EligibilityEvaluationID *uuid.UUID
	ExpiresAt               time.Time
	QuotedAt                time.Time
	QuotedBy                *uuid.UUID
	IdempotencyKey          *string
}

// QuoteItemRow is one priced line as it is written.
type QuoteItemRow struct {
	LineNo              int
	ServiceDefinitionID *uuid.UUID
	PackageDefinitionID *uuid.UUID
	PriceItemID         *uuid.UUID
	Quantity            benefitdomain.Quantity
	Requested           benefitdomain.Quantity
	Contract            benefitdomain.Quantity
	Covered             benefitdomain.Quantity
	Payer               benefitdomain.Quantity
	Member              benefitdomain.Quantity
	Outcome             string
	Explanations        []byte
}

// QuoteRecord is one stored contract.price_quote row read back.
type QuoteRecord struct {
	ID                      uuid.UUID
	PersonID                uuid.UUID
	ProgramID               *uuid.UUID
	ProviderProfileID       uuid.UUID
	LocationID              *uuid.UUID
	ServiceDate             time.Time
	CurrencyCode            string
	Outcome                 string
	Requested               string
	Contract                string
	Covered                 string
	Payer                   string
	Member                  string
	RequestHash             []byte
	RequestSnapshot         []byte
	ResultSnapshot          []byte
	ContractVersionID       *uuid.UUID
	PlanVersionID           *uuid.UUID
	EligibilityEvaluationID *uuid.UUID
	ExpiresAt               time.Time
	QuotedAt                time.Time
	QuotedBy                *uuid.UUID
	IdempotencyKey          *string
}

// QuoteItemRecord is one stored contract.price_quote_item row read back.
type QuoteItemRecord struct {
	LineNo              int
	ServiceDefinitionID *uuid.UUID
	PackageDefinitionID *uuid.UUID
	PriceItemID         *uuid.UUID
	Quantity            string
	Requested           string
	Contract            string
	Covered             string
	Payer               string
	Member              string
	Outcome             string
	Explanations        []byte
}

// Repository is the persistence port. Every method runs inside the caller's transaction,
// which the service has already bound to the tenant, so RLS is active for every statement
// and no method here can be called outside one.
type Repository interface {
	// QuoteTTLHours reads the tenant setting pricing.quote_ttl_hours. found is false when
	// the tenant never set one, which is not an error: the service falls back.
	QuoteTTLHours(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (hours int, found bool, err error)
	GetProvider(ctx context.Context, tx pgx.Tx, tenantID, providerProfileID uuid.UUID) (ProviderRecord, error)
	LocationBelongsToProvider(ctx context.Context, tx pgx.Tx, tenantID, locationID, providerProfileID uuid.UUID) (bool, error)
	EligibilityEvaluationExists(ctx context.Context, tx pgx.Tx, tenantID, evaluationID, personID uuid.UUID) (bool, error)
	PackagesExist(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error)

	// LoadEligibility reads the person, the memberships, the enrollments, the plan
	// version published on the service date and the already-open entitlement accounts.
	// It writes nothing.
	LoadEligibility(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
		programID *uuid.UUID, serviceDate time.Time) (EligibilityInput, error)

	// The three reads the deterministic price selection needs, in the same shape the
	// contract module resolves a single price with.
	CategoryPath(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error)
	PackagesContaining(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error)
	ListCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		q contractapp.CandidateQuery) ([]contractapp.CandidateRecord, error)
	ServiceDefinitionExists(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) (bool, error)

	// ListPriceRuleVersions returns every published PRICE rule set version covering the
	// service date, with the rules under it in evaluation order.
	ListPriceRuleVersions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		serviceDate time.Time) ([]PriceRuleVersion, error)

	CreateQuote(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, row QuoteRow, items []QuoteItemRow) error
	GetQuote(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (QuoteRecord, []QuoteItemRecord, error)
	// FindQuoteByKey is the Idempotency-Key replay lookup; found is false when the key
	// has never been used in this tenant.
	FindQuoteByKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (QuoteRecord, []QuoteItemRecord, bool, error)
}
