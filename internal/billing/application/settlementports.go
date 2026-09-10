package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// The vocabulary of WP-I7-04: what a settlement is, what a payment record is, what a
// reimbursement is, and the four ports the three of them reach other modules through.

// Permissions guarding this package's work.
//
// `settlement.read` and `settlement.approve` are in the catalogue from migration 000008.
// `settlement.record_payment` is migration 000046's, granted to the payer's financial
// reviewer and the payer's approver: entering the bank's reference is an ordinary
// finance-clerk task and not the second pair of eyes, and a tenant that wants the approver
// never to touch the payment file can only arrange that while the two are separate grants.
//
// The reimbursement adds none. The member reaches their own through WP-I4-01's
// `service_request.create` / `service_request.read`, which is the gate on the request this
// wraps, and the review is `claim.financial.review` — approving a reimbursement *is* deciding
// a claim, and a permission invented for it would be one nobody's role template grants.
const (
	PermissionSettlementRead          = "settlement.read"
	PermissionSettlementApprove       = "settlement.approve"
	PermissionSettlementRecordPayment = "settlement.record_payment"
	PermissionClaimFinancialReview    = "claim.financial.review"
	PermissionServiceRequestRead      = "service_request.read"
	PermissionServiceRequestCreate    = "service_request.create"
)

// The outbox types this package publishes. `settlement.approved` is what M9's posting listens
// for and `reimbursement.approved` is what the payment adapter listens for; both are published
// now and consumed later, because a consumer added afterwards can be replayed and a fact never
// recorded cannot be recovered.
const (
	SettlementApprovedEvent    = "settlement.approved"
	SettlementOpenedEvent      = "settlement.opened"
	PaymentRecordedEvent       = "payment.recorded"
	ReimbursementApprovedEvent = "reimbursement.approved"
	ReimbursementPaidEvent     = "reimbursement.paid"
)

// Errors the transport layer maps to problem codes. Every one of them has a Turkish title in
// internal/billing/transport/http.
var (
	ErrSettlementNotFound = errors.New("billing: settlement not found")
	// ErrSettlementReferenceTaken and ErrReimbursementReferenceTaken are the two reference
	// uniqueness constraints answering. Neither ever reaches a caller: the create retries with
	// a new random tail, and the tail is forty random bits. They are declared here rather than
	// in the repository so the retry loop can name them without the application package
	// importing its own infrastructure.
	ErrSettlementReferenceTaken    = errors.New("billing: the settlement reference is already used")
	ErrReimbursementReferenceTaken = errors.New("billing: the reimbursement reference is already used")
	// ErrSettlementTransitionInvalid is a command run from a status it cannot run from.
	ErrSettlementTransitionInvalid = errors.New("billing: the settlement is not in a state this command can run from")
	// ErrSettlementDeciderCannotApprove is WP-I4-03's maker-checker applied to the money: the
	// person who decided the batch may not be the one who releases what it owes. It bites
	// only above the tenant's threshold, which is the whole point of having a threshold.
	ErrSettlementDeciderCannotApprove = errors.New("billing: the decider of the batch may not approve its settlement above the threshold")
	// ErrPaymentTermMissing is a contract that carries no payment term on the day the batch
	// was decided. It is refused rather than defaulted: a due date this platform invented
	// would be a due date nobody agreed to and everybody would be measured against.
	ErrPaymentTermMissing = errors.New("billing: the contract carries no payment term")
	// ErrSettlementHasPayments refuses cancelling a settlement money has already moved
	// against. The answer there is a dispute on the record, not a rewrite of the header.
	ErrSettlementHasPayments = errors.New("billing: the settlement already carries payment records")
	// ErrPaymentReferenceTaken is `uq_billing_payment_record_reference` answering: this
	// provider's bank reference has been entered before, and the same transfer entered twice
	// is how a settlement comes to look paid when half of it was not.
	ErrPaymentReferenceTaken = errors.New("billing: this external reference is already recorded for the provider")
	// ErrPaymentNotAllowed is a record entered against a settlement that is not payable yet
	// or is not payable any more.
	ErrPaymentNotAllowed = errors.New("billing: the settlement is not in a state a payment may be recorded against")

	ErrReimbursementNotFound = errors.New("billing: reimbursement not found")
	// ErrReimbursementTransitionInvalid is a command run from a status it cannot run from.
	ErrReimbursementTransitionInvalid = errors.New("billing: the reimbursement is not in a state this command can run from")
	// ErrRequestUnusable is a service request that is not a live REIMBURSEMENT of the person
	// asking. It is one error rather than three because the three are one mistake — this
	// request is not one you can be reimbursed for — and separating them would answer
	// questions about somebody else's data.
	ErrRequestUnusable = errors.New("billing: the service request cannot carry a reimbursement")
	// ErrReceiptUnusable is a receipt that is not this tenant's, has not been scanned clean,
	// or whose bytes retention has already purged.
	ErrReceiptUnusable = errors.New("billing: the receipt document is not usable")
	// ErrNotEligibleOnDate is the check of v1.2 10.10: the enrollment did not cover the
	// member on the day they spent the money.
	ErrNotEligibleOnDate = errors.New("billing: the member was not covered on the service date")
	// ErrEntitlementAccountNotFound is a plan with no money entitlement the approved amount
	// could be taken from. It is refused rather than approved without a consumption, because
	// a reimbursement that drew down nothing would be money paid out of a wallet nobody debited.
	ErrEntitlementAccountNotFound = errors.New("billing: the member has no money entitlement account for this service")
	// ErrEntitlementInsufficient is the wallet answering: the member does not have the
	// approved amount left.
	ErrEntitlementInsufficient = errors.New("billing: the member's entitlement balance is not enough")
)

// PaymentExceedsError is the ceiling refusing a record, carrying the remainder so that a
// finance clerk is told what they may still enter rather than only that they may not enter
// this. Every figure on it is an exact decimal string.
type PaymentExceedsError struct {
	SettlementID  uuid.UUID
	Amount        string
	PaidAmount    string
	PayableAmount string
	// Remainder is `payable - paid`: what a record may still be for.
	Remainder    string
	CurrencyCode string
}

func (e *PaymentExceedsError) Error() string {
	return "billing: the payment record would take the settlement above its payable amount"
}

// ErrPaymentExceeds lets callers detect a PaymentExceedsError with errors.Is.
var ErrPaymentExceeds = errors.New("billing: payment exceeds the settlement")

// Is lets errors.Is(err, ErrPaymentExceeds) match a *PaymentExceedsError.
func (e *PaymentExceedsError) Is(target error) bool { return target == ErrPaymentExceeds }

// DuplicateReimbursementError names the earlier request rather than only saying "duplicate".
// A member told that something is a duplicate and not of what has been told nothing they can
// act on, and "you were already paid for this receipt on the 3rd of March under RB-202603-…"
// is something they can either accept or dispute.
type DuplicateReimbursementError struct {
	ExistingID        uuid.UUID
	ExistingReference string
	ExistingDate      time.Time
	// ByReceipt distinguishes the two halves of the rule: the same receipt digest, or the
	// same provider, day and amount. They are different conversations.
	ByReceipt bool
}

func (e *DuplicateReimbursementError) Error() string {
	return "billing: this reimbursement repeats an earlier one"
}

// ErrReimbursementDuplicate lets callers detect a DuplicateReimbursementError with errors.Is.
var ErrReimbursementDuplicate = errors.New("billing: duplicate reimbursement")

// Is lets errors.Is(err, ErrReimbursementDuplicate) match a *DuplicateReimbursementError.
func (e *DuplicateReimbursementError) Is(target error) bool {
	return target == ErrReimbursementDuplicate
}

// CeilingExceededError is the contract's reimbursement ceiling refusing an amount, carrying
// both figures. Both are exact decimal strings.
type CeilingExceededError struct {
	Requested    string
	Ceiling      string
	CurrencyCode string
}

func (e *CeilingExceededError) Error() string {
	return "billing: the requested amount is above the contract's ceiling for this service"
}

// ErrCeilingExceeded lets callers detect a CeilingExceededError with errors.Is.
var ErrCeilingExceeded = errors.New("billing: reimbursement ceiling exceeded")

// Is lets errors.Is(err, ErrCeilingExceeded) match a *CeilingExceededError.
func (e *CeilingExceededError) Is(target error) bool { return target == ErrCeilingExceeded }

// SettlementRecord is one settlement as the repository reads it. Every money field is exact
// decimal text and nothing here is ever a float.
type SettlementRecord struct {
	ID             uuid.UUID
	Reference      string
	BatchID        uuid.UUID
	BatchReference string
	// BatchDecidedBy is who closed the batch. It is on the record because the maker-checker
	// rule above the threshold is about that person, and reading it from the batch a second
	// time inside the approval would be a second read that could disagree.
	BatchDecidedBy         *uuid.UUID
	VersionNo              int
	ProviderOrganizationID uuid.UUID
	ProviderName           string
	PayerOrganizationID    *uuid.UUID
	CurrencyCode           string
	ApprovedAmount         string
	WithheldAmount         string
	PayableAmount          string
	PaidAmount             string
	DueDate                time.Time
	SettlementMethod       string
	Status                 string
	ApprovedBy             *uuid.UUID
	ApprovedAt             *time.Time
	CheckedBy              *uuid.UUID
	PostingID              *uuid.UUID
	CancelReasonCode       string
	CreatedAt              time.Time
	RowVersion             int64
}

// SettlementView is one settlement with the recoveries it netted and the payments recorded
// against it: the whole of what a finance screen shows, in one answer.
type SettlementView struct {
	Settlement SettlementRecord
	Recoveries []SettlementRecoveryRecord
	Payments   []PaymentRecordRecord
}

// SettlementPage is one page of settlements.
type SettlementPage struct {
	Items      []SettlementRecord
	NextCursor string
}

// SettlementRecoveryRecord is one recovery netted into a settlement.
type SettlementRecoveryRecord struct {
	ID           uuid.UUID
	ClaimID      uuid.UUID
	AdjustmentID uuid.UUID
	Amount       string
	CreatedAt    time.Time
}

// PaymentRecordRecord is one payment record as the repository reads it.
type PaymentRecordRecord struct {
	ID                     uuid.UUID
	SettlementID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	ExternalReference      string
	Amount                 string
	CurrencyCode           string
	PaidAt                 time.Time
	Source                 string
	Status                 string
	RecordedBy             *uuid.UUID
	Notes                  *string
	CreatedAt              time.Time
	RowVersion             int64
}

// ReimbursementRecord is one reimbursement as the repository reads it.
//
// There is no ciphertext on it and there never will be. `bank_account_ref_enc` is written by
// the create and selected back by nothing: no screen, no list and no export has a reason to
// hold it, and a column that is only ever written is a column no mapper can leak.
type ReimbursementRecord struct {
	ID                     uuid.UUID
	Reference              string
	PersonID               uuid.UUID
	EnrollmentID           uuid.UUID
	ServiceRequestID       uuid.UUID
	ClaimID                *uuid.UUID
	ReceiptDocumentID      uuid.UUID
	ServiceDefinitionID    uuid.UUID
	ServiceDate            time.Time
	ProviderOrganizationID uuid.UUID
	RequestedAmount        string
	// ApprovedAmount is empty until somebody decides.
	ApprovedAmount     string
	CurrencyCode       string
	BankAccountMasked  string
	Status             string
	DuplicateOfID      *uuid.UUID
	DuplicateReference string
	DecisionReasonCode string
	DecidedBy          *uuid.UUID
	DecidedAt          *time.Time
	SubmittedAt        *time.Time
	PaymentReference   string
	PaidAt             *time.Time
	CreatedAt          time.Time
	RowVersion         int64
}

// ReimbursementPage is one page of reimbursements.
type ReimbursementPage struct {
	Items      []ReimbursementRecord
	NextCursor string
}

// NewSettlementRow is the header as it is written.
type NewSettlementRow struct {
	Reference              string
	BatchID                uuid.UUID
	VersionNo              int
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    *uuid.UUID
	CurrencyCode           string
	ApprovedAmount         string
	WithheldAmount         string
	PayableAmount          string
	DueDate                time.Time
	SettlementMethod       string
	Status                 string
	ActorID                *uuid.UUID
}

// ApproveSettlementRow is who approved and who checked.
type ApproveSettlementRow struct {
	ApprovedBy uuid.UUID
	ApprovedAt time.Time
	// CheckedBy is written only above the threshold; below it there is one person and
	// recording them twice would say a check happened that did not.
	CheckedBy *uuid.UUID
	ActorID   *uuid.UUID
}

// NewPaymentRecordRow is one payment record as it is written.
type NewPaymentRecordRow struct {
	SettlementID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	ExternalReference      string
	Amount                 string
	CurrencyCode           string
	PaidAt                 time.Time
	Source                 string
	RecordedBy             *uuid.UUID
	Notes                  *string
	ActorID                *uuid.UUID
}

// NewReimbursementRow is the header as it is written. `BankAccountRefEnc` is the platform
// cipher's envelope and is the only form of the account number that reaches the database.
type NewReimbursementRow struct {
	Reference              string
	PersonID               uuid.UUID
	EnrollmentID           uuid.UUID
	ServiceRequestID       uuid.UUID
	ReceiptDocumentID      uuid.UUID
	ReceiptSHA256          []byte
	ServiceDefinitionID    uuid.UUID
	ServiceDate            time.Time
	ProviderOrganizationID uuid.UUID
	RequestedAmount        string
	CurrencyCode           string
	BankAccountRefEnc      []byte
	BankAccountMasked      string
	ActorID                *uuid.UUID
}

// DecideReimbursementRow is the decision as it is written: the status the figure implies, the
// figure, the reason and the claim approval created.
type DecideReimbursementRow struct {
	Status         string
	ApprovedAmount string
	ReasonCode     *string
	ReasonText     *string
	DuplicateOfID  *uuid.UUID
	ClaimID        *uuid.UUID
	DecidedBy      uuid.UUID
	DecidedAt      time.Time
	ActorID        *uuid.UUID
}

// SettlementQuery is the listSettlements filter, already validated.
type SettlementQuery struct {
	Scope                  Scope
	ProviderOrganizationID *uuid.UUID
	PayerOrganizationID    *uuid.UUID
	BatchID                *uuid.UUID
	Status                 *string
	CurrencyCode           *string
	DueFrom                *time.Time
	DueTo                  *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// ReimbursementQuery is the listReimbursements filter, already validated. `PersonID` is the
// PERSON scope: the member's own list passes it and the back-office list does not.
type ReimbursementQuery struct {
	PersonID *uuid.UUID
	Status   *string
	DateFrom *time.Time
	DateTo   *time.Time
	After    *httpx.Cursor
	PageSize int
}

// SettlementBatch is what a settlement needs from the batch it opens on.
type SettlementBatch struct {
	ID                     uuid.UUID
	Reference              string
	ProviderOrganizationID uuid.UUID
	ProviderName           string
	ProviderProfileID      *uuid.UUID
	PayerOrganizationID    *uuid.UUID
	CurrencyCode           string
	DomainCode             string
	Status                 string
	DecidedAt              *time.Time
	DecidedBy              *uuid.UUID
	ApprovedTotal          string
}

// PaymentTerm is the contract's answer to "when is this due and how is it settled".
type PaymentTerm struct {
	Found            bool
	DueDays          int
	SettlementMethod string
	ContractID       uuid.UUID
}

// OpenRecovery is one RECOVERY adjustment no settlement has netted yet.
type OpenRecovery struct {
	AdjustmentID uuid.UUID
	ClaimID      uuid.UUID
	Amount       string
	ReasonCode   string
	CreatedAt    time.Time
}

// ReimbursementRequest is what the create reads from the service request and the receipt in
// one go. `ReceiptClean` and `ReceiptSHA256` are the document's; everything else is the
// request's.
type ReimbursementRequest struct {
	Found                  bool
	ServiceRequestID       uuid.UUID
	PersonID               uuid.UUID
	EnrollmentID           uuid.UUID
	ProgramID              uuid.UUID
	ServiceDate            time.Time
	ProviderOrganizationID *uuid.UUID
	RequestType            string
	Status                 string
	ServiceDefinitionID    uuid.UUID
	ReceiptClean           bool
	ReceiptSHA256          []byte
}

// EnrollmentCoverage is the eligibility check of v1.2 10.10, as this package can honestly
// make it: was the member covered on the day they spent the money.
type EnrollmentCoverage struct {
	Found      bool
	Status     string
	CoversDate bool
	PersonID   uuid.UUID
}

// DuplicateMatch is what the duplicate check found, or nothing.
type DuplicateMatch struct {
	Found       bool
	ID          uuid.UUID
	Reference   string
	ServiceDate time.Time
	ByReceipt   bool
}

// DuplicateProbe is the question the duplicate check asks.
type DuplicateProbe struct {
	PersonID               uuid.UUID
	ExcludeID              uuid.UUID
	ReceiptSHA256          []byte
	ProviderOrganizationID uuid.UUID
	ServiceDate            time.Time
	RequestedAmount        string
	WindowFrom             time.Time
}

// SettlementRepository is the persistence port of WP-I7-04. It is a third interface rather
// than more methods on BatchRepository so that a test double for the icmal does not have to
// grow twenty methods it never calls.
type SettlementRepository interface {
	CreateSettlement(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewSettlementRow) (SettlementRecord, error)
	GetSettlement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (SettlementRecord, error)
	LockSettlement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (SettlementRecord, error)
	ListSettlements(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q SettlementQuery) ([]SettlementRecord, error)
	// FindLiveSettlementForBatch is the first half of the outbox handler's idempotency;
	// `uq_billing_settlement_live_batch` is the half that holds under a race.
	FindLiveSettlementForBatch(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID) (uuid.UUID, bool, error)
	NextSettlementVersion(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID) (int, error)
	ApproveSettlement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in ApproveSettlementRow, expected int64) (bool, error)
	SetSettlementStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status string, from []string, actorID *uuid.UUID) (bool, error)
	CancelSettlement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, reasonCode string, reasonText *string, actorID *uuid.UUID, expected int64) (bool, error)
	SetSettlementPaidAmount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, paid, status string, actorID *uuid.UUID) error

	BatchForSettlement(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID) (SettlementBatch, error)
	PaymentTermFor(ctx context.Context, tx pgx.Tx, tenantID, providerProfileID uuid.UUID, payerOrganizationID *uuid.UUID, asOf time.Time) (PaymentTerm, error)
	ListOpenRecoveries(ctx context.Context, tx pgx.Tx, tenantID, providerOrganizationID uuid.UUID) ([]OpenRecovery, error)
	CreateSettlementRecovery(ctx context.Context, tx pgx.Tx, tenantID, settlementID, claimID, adjustmentID uuid.UUID, amount string, actorID *uuid.UUID) error
	ListSettlementRecoveries(ctx context.Context, tx pgx.Tx, tenantID, settlementID uuid.UUID) ([]SettlementRecoveryRecord, error)
	DeleteSettlementRecoveries(ctx context.Context, tx pgx.Tx, tenantID, settlementID uuid.UUID) error

	CreatePaymentRecord(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewPaymentRecordRow) (PaymentRecordRecord, error)
	ListPaymentRecords(ctx context.Context, tx pgx.Tx, tenantID, settlementID uuid.UUID, scope Scope) ([]PaymentRecordRecord, error)
	SumLivePaymentRecords(ctx context.Context, tx pgx.Tx, tenantID, settlementID uuid.UUID) (string, error)

	CreateReimbursement(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewReimbursementRow) (ReimbursementRecord, error)
	GetReimbursement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, personID *uuid.UUID) (ReimbursementRecord, error)
	LockReimbursement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, personID *uuid.UUID) (ReimbursementRecord, error)
	ListReimbursements(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ReimbursementQuery) ([]ReimbursementRecord, error)
	SubmitReimbursement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, submittedAt time.Time, actorID *uuid.UUID, expected int64) (bool, error)
	DecideReimbursement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in DecideReimbursementRow, expected int64) (bool, error)
	SetReimbursementStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status string, from []string, actorID *uuid.UUID) (bool, error)
	SetReimbursementPaid(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, reference string, paidAt time.Time, actorID *uuid.UUID) (bool, error)
	FindReimbursementDuplicate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, probe DuplicateProbe) (DuplicateMatch, error)
	ReimbursementRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID, documentID uuid.UUID) (ReimbursementRequest, error)
	ReimbursementCeiling(ctx context.Context, tx pgx.Tx, tenantID, serviceDefinitionID uuid.UUID, providerProfileID *uuid.UUID, asOf time.Time) (string, error)
	EnrollmentCoverage(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID, on time.Time) (EnrollmentCoverage, error)
	ProviderProfileOf(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (uuid.UUID, bool, error)
}

// ReimbursementClaim is the claim an approved reimbursement creates, in this package's
// vocabulary rather than the claim module's.
type ReimbursementClaim struct {
	PersonID            uuid.UUID
	ProgramID           uuid.UUID
	EnrollmentID        uuid.UUID
	ServiceRequestID    uuid.UUID
	ServiceDefinitionID uuid.UUID
	ServiceDate         time.Time
	// ApprovedAmount is what the payer agreed to, exact decimal text. The claim's single line
	// is billed at it and decided at it in the same breath: the decision has already been
	// taken, and the claim records it rather than asking for it again.
	ApprovedAmount         string
	CurrencyCode           string
	ProviderOrganizationID uuid.UUID
	ReasonCode             string
}

// EntitlementConsumption is one draw-down of the member's wallet.
type EntitlementConsumption struct {
	PersonID     uuid.UUID
	EnrollmentID uuid.UUID
	// ReferenceID is the service request the reimbursement wraps: the ledger's reference type
	// is SERVICE_REQUEST and the request is what the member actually asked for.
	ReferenceID uuid.UUID
	Amount      string
	// CurrencyCode narrows which money account is drawn down. A member with a TRY wallet and
	// a EUR wallet has two, and spending the wrong one is spending somebody else's promise.
	CurrencyCode string
	// Key is the idempotency key of the movement. It is derived from the reimbursement rather
	// than from a clock, so a redelivered command consumes once.
	Key        string
	ReasonCode string
	ActorID    uuid.UUID
	AsOf       time.Time
}

// PaymentOrder is what the payment adapter is handed. There is no account number on it and
// there never will be: the adapter reads the envelope from the row under its own key, and an
// order that carried the number would be an order that put it in a queue.
type PaymentOrder struct {
	ReimbursementID   uuid.UUID
	Reference         string
	PersonID          uuid.UUID
	Amount            string
	CurrencyCode      string
	BankAccountMasked string
}

// EntitlementPort is the member's wallet seen from the settlement module: find the money
// account this service draws on and spend exactly the approved amount from it.
//
// It is a port rather than a call into the ledger service because that service opens no
// transaction of its own but does need one: the consumption and the claim and the decision
// are one fact, and a wallet debited while the decision rolled back would be a member charged
// for a reimbursement they never received.
type EntitlementPort interface {
	Consume(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in EntitlementConsumption) error
}

// NoEntitlements is the default EntitlementPort: it refuses.
//
// Silence is the wrong answer here, unlike the work-queue port. A process wired without a
// wallet cannot approve a reimbursement at all — approving one without consuming anything is
// exactly the bug the acceptance criterion names — so it says so rather than paying out.
type NoEntitlements struct{}

// Consume implements EntitlementPort.
func (NoEntitlements) Consume(context.Context, pgx.Tx, uuid.UUID, EntitlementConsumption) error {
	return errors.New("billing: no entitlement ledger is wired")
}

// PaymentOrderPort is the payment adapter of M9, declared here so that the approval has
// somebody to hand the order to and so that the shape of that hand-off is fixed before the
// adapter exists.
type PaymentOrderPort interface {
	Place(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, order PaymentOrder) error
}

// RecordingPaymentOrders is the no-op implementation this milestone ships: it writes the
// outbox event and nothing else, which is exactly what a platform that transfers no money
// (v1.2 4.3) should do.
//
// It is a distinct type rather than a nil check inside the service because "who pays the
// member" is a real seam: M9 swaps a real adapter in behind this interface, and a nil check
// would be a seam nobody could find.
type RecordingPaymentOrders struct{}

// Place implements PaymentOrderPort. The order has already been published to the outbox by
// the caller inside the same transaction, which is what makes this a genuine no-op rather
// than a dropped instruction.
func (RecordingPaymentOrders) Place(context.Context, pgx.Tx, uuid.UUID, PaymentOrder) error {
	return nil
}
