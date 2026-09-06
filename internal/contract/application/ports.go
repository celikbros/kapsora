// Package application implements the contract use cases: the contract itself, the
// maker-checker lifecycle of its versions, the price lists, items, packages, quotas and
// payment term that hang under a version, and the price resolution the rest of the system
// asks "what does this cost here, on this day". Transactions are opened here with
// db.WithTenantTx, so a write and its audit row commit together and RLS is bound for every
// statement.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding contract work (migration 000008). They live here rather than in
// the transport because whether a caller may see an unagreed draft is a business rule,
// not a routing detail.
const (
	PermissionRead    = "contract.read"
	PermissionManage  = "contract.manage"
	PermissionPublish = "contract.publish"
)

// Errors mapped by the transport layer to problem codes.
var (
	ErrContractNotFound    = errors.New("contract: contract not found")
	ErrVersionNotFound     = errors.New("contract: contract version not found")
	ErrPriceListNotFound   = errors.New("contract: price list not found")
	ErrPaymentTermNotFound = errors.New("contract: payment term not found")
	// ErrLodgingTermsNotFound is a version that has not said what a cancellation or a
	// no-show costs. It is the refusal WP-I6-02 turns into 409 LODGING_TERMS_MISSING
	// rather than confirming a booking under a default nobody agreed to.
	ErrLodgingTermsNotFound = errors.New("contract: lodging terms not found")
	ErrContractCodeTaken    = errors.New("contract: contract code already used in this tenant")
	ErrPriceListCodeTaken   = errors.New("contract: price list code already used in this version")
	ErrPackageCodeTaken     = errors.New("contract: package code already used in this version")
	ErrQuotaScopeDuplicate  = errors.New("contract: a quota with this scope and period already exists")
	ErrVersionMismatch      = errors.New("contract: row version does not match If-Match")
	ErrVersionImmutable     = errors.New("contract: a version that is not a draft cannot be changed")
	ErrVersionTransition    = errors.New("contract: this version status transition is not allowed")
	ErrVersionOverlap       = errors.New("contract: two published versions may not cover the same date")
	ErrMakerCheckerSame     = errors.New("contract: the publisher must differ from the submitter")
	ErrPartyNotFound        = errors.New("contract: payer, sponsor or provider not found")
	ErrCatalogTargetMissing = errors.New("contract: service definition, category or location not found")
)

// ContractRecord is one contract.contract row joined with the display names of its
// parties; a contract screen always shows "who with whom".
type ContractRecord struct {
	ID                    uuid.UUID
	Code                  string
	Name                  string
	PayerOrganizationID   uuid.UUID
	PayerName             string
	ProviderProfileID     uuid.UUID
	ProviderName          string
	SponsorOrganizationID *uuid.UUID
	SponsorName           *string
	DomainCode            string
	Status                string
	CreatedAt             time.Time
	RowVersion            int64
}

// NewContractRow is the insert payload for a contract.
type NewContractRow struct {
	Code                  string
	Name                  string
	PayerOrganizationID   uuid.UUID
	ProviderProfileID     uuid.UUID
	SponsorOrganizationID *uuid.UUID
	DomainCode            string
}

// ContractUpdateRow is the update payload for a contract; the code, the parties and the
// domain are absent because every published version was agreed under them.
type ContractUpdateRow struct {
	Name                  string
	SponsorOrganizationID *uuid.UUID
	Status                string
}

// ContractQuery is the repository-level contract filter.
type ContractQuery struct {
	ProviderProfileID   *uuid.UUID
	PayerOrganizationID *uuid.UUID
	DomainCode          string
	Status              string
	Query               string
	After               *httpx.Cursor
	PageSize            int
}

// VersionRecord is one contract.contract_version row.
type VersionRecord struct {
	ID                uuid.UUID
	ContractID        uuid.UUID
	VersionNo         int
	Status            string
	ValidFrom         *time.Time
	ValidTo           *time.Time
	CurrencyCode      string
	Notes             *string
	ConfigurationHash *string
	SubmittedAt       *time.Time
	SubmittedBy       *uuid.UUID
	PublishedAt       *time.Time
	PublishedBy       *uuid.UUID
	RetireReasonCode  *string
	ReviewComment     *string
	CreatedAt         time.Time
	RowVersion        int64
}

// NewVersionRow is the insert payload for a contract version.
type NewVersionRow struct {
	ContractID   uuid.UUID
	VersionNo    int
	ValidFrom    *time.Time
	ValidTo      *time.Time
	CurrencyCode string
	Notes        *string
}

// VersionDraftRow is the merge-patch result written back to a draft version.
type VersionDraftRow struct {
	ValidFrom    *time.Time
	ValidTo      *time.Time
	CurrencyCode string
	Notes        *string
}

// SubmitRow, PublishRow and RetireRow are the three maker-checker writes.
type SubmitRow struct {
	ActorID uuid.UUID
	Comment *string
}

// PublishRow carries the checker and the hash the publish freezes.
type PublishRow struct {
	ActorID           uuid.UUID
	ConfigurationHash string
	Comment           *string
}

// RetireRow carries the reason a published version was withdrawn.
type RetireRow struct {
	ReasonCode string
	ReasonText *string
}

// PriceListRecord is one contract.price_list row with the number of items under it.
type PriceListRecord struct {
	ID                uuid.UUID
	ContractVersionID uuid.UUID
	Code              string
	Name              string
	Priority          int
	SeasonFrom        *time.Time
	SeasonTo          *time.Time
	WeekdayMask       *int
	ItemCount         int
	CreatedAt         time.Time
	RowVersion        int64
}

// PriceListRow is one row of a price list set replacement.
type PriceListRow struct {
	Code        string
	Name        string
	Priority    int
	SeasonFrom  *time.Time
	SeasonTo    *time.Time
	WeekdayMask *int
}

// PriceItemRecord is one contract.price_item row with the codes of whatever it names.
// Every money field is an exact decimal string; the empty string means the column is NULL.
type PriceItemRecord struct {
	ID                    uuid.UUID
	PriceListID           uuid.UUID
	ServiceDefinitionID   *uuid.UUID
	ServiceDefinitionCode *string
	ServiceCategoryID     *uuid.UUID
	ServiceCategoryCode   *string
	PackageDefinitionID   *uuid.UUID
	PackageDefinitionCode *string
	LocationID            *uuid.UUID
	UnitType              string
	PricingMethod         string
	Amount                string
	Percent               string
	FormulaKey            *string
	MinAmount             string
	MaxAmount             string
	MemberShareMethod     string
	MemberShareAmount     string
	MemberSharePercent    string
	ValidFrom             time.Time
	ValidTo               *time.Time
	Priority              int
	CreatedAt             time.Time
}

// PriceItemRow is one row of a price item set replacement.
type PriceItemRow struct {
	ServiceDefinitionID *uuid.UUID
	ServiceCategoryID   *uuid.UUID
	PackageDefinitionID *uuid.UUID
	LocationID          *uuid.UUID
	UnitType            string
	PricingMethod       string
	Amount              *string
	Percent             *string
	FormulaKey          *string
	MinAmount           *string
	MaxAmount           *string
	MemberShareMethod   string
	MemberShareAmount   *string
	MemberSharePercent  *string
	ValidFrom           time.Time
	ValidTo             *time.Time
	Priority            int
}

// PriceItemQuery is the repository-level price item page request.
type PriceItemQuery struct {
	PriceListID uuid.UUID
	After       *httpx.Cursor
	PageSize    int
}

// PackageLineRecord is one contract.package_line row.
type PackageLineRecord struct {
	ServiceDefinitionID   uuid.UUID
	ServiceDefinitionCode *string
	IncludedQuantity      string
}

// PackageRecord is one contract.package_definition row with its lines.
type PackageRecord struct {
	ID                uuid.UUID
	ContractVersionID uuid.UUID
	Code              string
	Name              string
	InclusionRule     string
	MinLines          *int
	Lines             []PackageLineRecord
}

// PackageRow is one package of a package set replacement.
type PackageRow struct {
	Code          string
	Name          string
	InclusionRule string
	MinLines      *int
	Lines         []PackageLineRow
}

// PackageLineRow is one line of a package replacement.
type PackageLineRow struct {
	ServiceDefinitionID uuid.UUID
	IncludedQuantity    string
}

// QuotaRecord is one contract.provider_quota row.
type QuotaRecord struct {
	ID                  uuid.UUID
	ContractVersionID   uuid.UUID
	LocationID          *uuid.UUID
	ServiceDefinitionID *uuid.UUID
	PeriodType          string
	PeriodFrom          time.Time
	PeriodTo            time.Time
	Capacity            string
	Consumed            string
	AllowOverdraft      bool
}

// QuotaRow is one row of a quota set replacement; the consumed counter is absent because
// authorization owns it and a price sheet write must never reset it.
type QuotaRow struct {
	LocationID          *uuid.UUID
	ServiceDefinitionID *uuid.UUID
	PeriodType          string
	PeriodFrom          time.Time
	PeriodTo            time.Time
	Capacity            string
	AllowOverdraft      bool
}

// LodgingTermsRecord is the single contract.lodging_terms row of a version, as the
// database holds it. The two percentages are exact decimal strings all the way out: the
// column is numeric(7,4), the query casts it to text, and nothing between here and the
// wire parses it into a number.
type LodgingTermsRecord struct {
	ID                          uuid.UUID
	ContractVersionID           uuid.UUID
	FreeCancellationHoursBefore int
	PenaltyKind                 string
	PenaltyNights               *int
	PenaltyPercent              string
	NoShowPercent               string
	HoldMinutes                 *int
	MinNights                   int
	MaxNights                   *int
	ChildFreeUnderAge           *int
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
	RowVersion                  int64
}

// LodgingTermsRow is the write payload of a version's lodging terms.
type LodgingTermsRow struct {
	FreeCancellationHoursBefore int
	PenaltyKind                 string
	PenaltyNights               *int
	PenaltyPercent              *string
	NoShowPercent               string
	HoldMinutes                 *int
	MinNights                   int
	MaxNights                   *int
	ChildFreeUnderAge           *int
}

// PaymentTermRecord is the single contract.payment_term row of a version.
type PaymentTermRecord struct {
	ID                uuid.UUID
	ContractVersionID uuid.UUID
	DueDays           int
	SettlementMethod  string
	TaxBehaviour      string
	VatRate           string
	LateFeePercent    string
	RowVersion        int64
}

// PaymentTermRow is the write payload of a payment term.
type PaymentTermRow struct {
	DueDays          int
	SettlementMethod string
	TaxBehaviour     string
	VatRate          *string
	LateFeePercent   *string
}

// PriceDetail is the money content of one price item, carried alongside a selection
// candidate so the winner can be reported without a second read.
type PriceDetail struct {
	ContractID         uuid.UUID
	ContractCode       string
	VersionNo          int
	CurrencyCode       string
	PriceListCode      string
	LocationID         *uuid.UUID
	UnitType           string
	PricingMethod      string
	Amount             string
	Percent            string
	FormulaKey         *string
	MinAmount          string
	MaxAmount          string
	MemberShareMethod  string
	MemberShareAmount  string
	MemberSharePercent string
}

// CandidateRecord is one loaded price item ready to be scored: the pure-function shape the
// selection package needs, plus the money the winner will be reported with.
type CandidateRecord struct {
	Candidate selection.Candidate
	Detail    PriceDetail
}

// CandidateQuery is the repository-level candidate load; the category chain and the
// packages containing the definition are resolved by the caller first, because the same
// two reads also describe the request to the selection function.
type CandidateQuery struct {
	ProviderProfileID   uuid.UUID
	ServiceDate         time.Time
	ServiceDefinitionID uuid.UUID
	CategoryIDs         []uuid.UUID
	PackageIDs          []uuid.UUID
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active.
type Repository interface {
	CreateContract(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewContractRow) (uuid.UUID, error)
	GetContract(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (ContractRecord, error)
	ListContracts(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ContractQuery) ([]ContractRecord, error)
	UpdateContract(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in ContractUpdateRow, expected int64) error

	NextVersionNo(ctx context.Context, tx pgx.Tx, tenantID, contractID uuid.UUID) (int, error)
	CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewVersionRow) (uuid.UUID, error)
	GetVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (VersionRecord, error)
	ListVersions(ctx context.Context, tx pgx.Tx, tenantID, contractID uuid.UUID) ([]VersionRecord, error)
	// LockVersion reads the row FOR UPDATE, so two commands on one version serialise.
	LockVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (VersionRecord, error)
	UpdateVersionDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in VersionDraftRow) error
	// TouchVersion bumps row_version without changing a business field, so writing a
	// child row invalidates the ETag the caller holds for the version.
	TouchVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	SubmitVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in SubmitRow) error
	// PublishVersion surfaces the published-overlap exclusion constraint as
	// ErrVersionOverlap.
	PublishVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in PublishRow) error
	RetireVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in RetireRow) error

	ListPriceLists(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]PriceListRecord, error)
	GetPriceList(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (PriceListRecord, error)
	// ReplacePriceLists upserts by code and deletes what the set did not name, so a list
	// that survives keeps its id and its items.
	ReplacePriceLists(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []PriceListRow) error
	TouchPriceList(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, expected int64) error

	ListPriceItems(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q PriceItemQuery) ([]PriceItemRecord, error)
	CountPriceItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int, error)
	ReplacePriceItems(ctx context.Context, tx pgx.Tx, tenantID, priceListID uuid.UUID, rows []PriceItemRow) error

	ListPackages(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]PackageRecord, error)
	ReplacePackages(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []PackageRow) error

	ListQuotas(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]QuotaRecord, error)
	ReplaceQuotas(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []QuotaRow) error

	GetPaymentTerm(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (PaymentTermRecord, error)
	UpsertPaymentTerm(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in PaymentTermRow) error

	// GetLodgingTerms answers ErrLodgingTermsNotFound for a version that has none, which
	// is an ordinary state of a draft rather than an error in the read.
	GetLodgingTerms(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (LodgingTermsRecord, error)
	// UpsertLodgingTerms replaces the version's terms in place. The DRAFT-only rule is the
	// trigger's; this method does not restate it.
	UpsertLodgingTerms(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in LodgingTermsRow) error

	// CategoryPath returns the definition's own catalog category first and then each
	// ancestor up to the root; an empty result means the definition does not exist.
	CategoryPath(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error)
	// PackagesContaining lists the packages whose lines name this service definition.
	PackagesContaining(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) ([]uuid.UUID, error)
	ListCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q CandidateQuery) ([]CandidateRecord, error)
	ServiceDefinitionExists(ctx context.Context, tx pgx.Tx, tenantID, definitionID uuid.UUID) (bool, error)
}
