package domain

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

// The settlement's own rules, the payment record's, and the member's reimbursement
// (WP-I7-04).
//
// Nothing here reads a database or a clock it was not given, and nothing here is a float. The
// one thing worth stating twice: `MaskAccount` is the only function in this repository that
// turns a bank account number into something that may be stored, logged or sent, and it
// returns four characters.

// The aggregate types the audit rows, the outbox events and the notifications carry.
const (
	AggregateSettlement    = "SETTLEMENT"
	AggregatePaymentRecord = "PAYMENT_RECORD"
	AggregateReimbursement = "REIMBURSEMENT"
)

// The settlement lifecycle (v1.2 10.9 step 9, 16.8). POSTED and RECONCILED are M9's and
// WP-I7-05's respectively; this package writes neither, and both are in the list because a
// filter that could not name them would be a filter that hid rows.
const (
	SettlementDraft           = "DRAFT"
	SettlementPendingApproval = "PENDING_APPROVAL"
	SettlementApproved        = "APPROVED"
	SettlementPosted          = "POSTED"
	SettlementPaid            = "PAID"
	SettlementPartiallyPaid   = "PARTIALLY_PAID"
	SettlementReconciled      = "RECONCILED"
	SettlementCancelled       = "CANCELLED"
)

// SettlementStatuses is the whole list, in lifecycle order, for a filter's validation.
var SettlementStatuses = []string{
	SettlementDraft, SettlementPendingApproval, SettlementApproved, SettlementPosted,
	SettlementPaid, SettlementPartiallyPaid, SettlementReconciled, SettlementCancelled,
}

// The settlement methods `contract.payment_term` carries.
const (
	MethodBankTransfer = "BANK_TRANSFER"
	MethodOffset       = "OFFSET"
	MethodOther        = "OTHER"
)

// SettlementMethods is the closed list the column CHECKs.
var SettlementMethods = []string{MethodBankTransfer, MethodOffset, MethodOther}

// The payment record's statuses. RECONCILED is WP-I7-05's word and this package only ever
// writes RECORDED; DISPUTED is what a wrong record becomes, because nothing here is deleted.
const (
	PaymentRecorded   = "RECORDED"
	PaymentReconciled = "RECONCILED"
	PaymentDisputed   = "DISPUTED"
)

// PaymentStatuses is the closed list.
var PaymentStatuses = []string{PaymentRecorded, PaymentReconciled, PaymentDisputed}

// The sources a payment record may come from. ERP is M9's `PaymentConfirmation`; a person
// entering what the bank statement says is MANUAL.
const (
	PaymentSourceManual = "MANUAL"
	PaymentSourceERP    = "ERP"
)

// PaymentSources is the closed list.
var PaymentSources = []string{PaymentSourceManual, PaymentSourceERP}

// The reimbursement lifecycle (v1.2 10.10).
const (
	ReimbursementDraft             = "DRAFT"
	ReimbursementSubmitted         = "SUBMITTED"
	ReimbursementUnderReview       = "UNDER_REVIEW"
	ReimbursementApproved          = "APPROVED"
	ReimbursementPartiallyApproved = "PARTIALLY_APPROVED"
	ReimbursementRejected          = "REJECTED"
	ReimbursementPaymentOrdered    = "PAYMENT_ORDERED"
	ReimbursementPaid              = "PAID"
	ReimbursementCancelled         = "CANCELLED"
)

// ReimbursementStatuses is the whole list, in lifecycle order.
var ReimbursementStatuses = []string{
	ReimbursementDraft, ReimbursementSubmitted, ReimbursementUnderReview,
	ReimbursementApproved, ReimbursementPartiallyApproved, ReimbursementRejected,
	ReimbursementPaymentOrdered, ReimbursementPaid, ReimbursementCancelled,
}

// The three answers `decideReimbursement` accepts. They are the caller's words rather than
// the statuses, because "approve for this much" and "approve" are one decision with a figure
// and the status is what the figure implies.
const (
	DecisionApproveReimbursement = "APPROVE"
	DecisionRejectReimbursement  = "REJECT"
)

// ReimbursementDecisions is the closed list a request body may name.
var ReimbursementDecisions = []string{DecisionApproveReimbursement, DecisionRejectReimbursement}

// MaskedAccountLength is how many characters of an account number this platform may hold. It
// is four, it is not a parameter, and `ck_billing_reimbursement_bank_mask` repeats it: a mask
// whose length two callers disagreed about is a mask one of them made longer.
const MaskedAccountLength = 4

var (
	settlementReferencePattern    = regexp.MustCompile(`^ST-[0-9]{6}-[A-Z2-7]{8}$`)
	reimbursementReferencePattern = regexp.MustCompile(`^RB-[0-9]{6}-[A-Z2-7]{8}$`)
	// externalReferencePattern mirrors `ck_billing_payment_record_reference`. It is what a
	// bank or an ERP prints on a statement: letters, digits and the four separators.
	externalReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)
	// ibanPattern is deliberately loose: two letters, two digits and up to thirty more
	// alphanumerics is every IBAN in use, and this platform is not the authority on which
	// country issues which length. What matters here is that the value is an account number
	// and not a sentence somebody pasted.
	ibanPattern = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{10,30}$`)
	// maskedAccountPattern mirrors `ck_billing_reimbursement_bank_mask`.
	maskedAccountPattern = regexp.MustCompile(`^[0-9A-Z]{4}$`)
)

// maxSettlementCancelReason mirrors `ck_billing_settlement_cancel_text`.
const maxSettlementCancelReason = 1000

// NewSettlementReference builds `ST-YYYYMM-XXXXXXXX`: the month the settlement was opened in,
// and forty random bits. The tail is random rather than sequential for the reason a batch
// reference's is — a sequential reference tells a competitor how much a tenant settled last
// month.
func NewSettlementReference(now time.Time) (string, error) {
	return prefixedReference("ST", now)
}

// NewReimbursementReference builds `RB-YYYYMM-XXXXXXXX`, on the same shape.
func NewReimbursementReference(now time.Time) (string, error) {
	return prefixedReference("RB", now)
}

func prefixedReference(prefix string, now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("billing: generate %s reference: %w", prefix, err)
	}
	return fmt.Sprintf("%s-%s-%s", prefix, now.UTC().Format("200601"),
		batchReferenceEncoding.EncodeToString(buf)), nil
}

// ValidSettlementReference and ValidReimbursementReference exist so a test can assert the
// generator and the column CHECK agree without reaching for a database.
func ValidSettlementReference(v string) bool { return settlementReferencePattern.MatchString(v) }

// ValidReimbursementReference reports whether v has the shape the column CHECKs.
func ValidReimbursementReference(v string) bool {
	return reimbursementReferencePattern.MatchString(v)
}

// ValidSettlementStatus reports whether a filter names a status the lifecycle has.
func ValidSettlementStatus(s string) bool { return contains(SettlementStatuses, s) }

// ValidReimbursementStatus reports whether a filter names a status the lifecycle has.
func ValidReimbursementStatus(s string) bool { return contains(ReimbursementStatuses, s) }

// ValidSettlementMethod reports whether the contract's term names one of the three.
func ValidSettlementMethod(m string) bool { return contains(SettlementMethods, m) }

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// CanApproveSettlement reports whether the settlement may be approved.
func CanApproveSettlement(status string) bool { return status == SettlementPendingApproval }

// CanRecordPayment reports whether a payment record may be entered against the settlement.
//
// APPROVED and POSTED are the ordinary cases and PARTIALLY_PAID is the second instalment.
// PAID is not: the whole payable amount has arrived, and a further record would be refused by
// the ceiling anyway — refusing it by status says why. A settlement waiting for approval is
// not payable yet, and paying one that was cancelled is paying a document that was withdrawn.
func CanRecordPayment(status string) bool {
	switch status {
	case SettlementApproved, SettlementPosted, SettlementPartiallyPaid:
		return true
	default:
		return false
	}
}

// PaidStatusFor is the word that follows the sum: PAID when the whole payable amount has
// arrived, PARTIALLY_PAID while some of it has, and the status the settlement already carried
// when none has.
//
// A settlement of nought is PAID as soon as anybody asks, which is the honest answer: there is
// nothing to wait for. It is also what `ck_billing_settlement_paid_status` requires, since
// PARTIALLY_PAID needs a positive payable amount.
func PaidStatusFor(paid, payable benefitdomain.Quantity, current string) string {
	switch {
	case paid.Cmp(payable) >= 0:
		return SettlementPaid
	case paid.IsPositive():
		return SettlementPartiallyPaid
	default:
		return current
	}
}

// ValidExternalReference reports whether a bank or ERP reference has the shape the column
// CHECKs.
func ValidExternalReference(v string) bool { return externalReferencePattern.MatchString(v) }

// ValidPaymentSource reports whether the caller named one of the two.
func ValidPaymentSource(v string) bool { return contains(PaymentSources, v) }

// ValidReasonCode reports whether a reason code has the shape every reason column in this
// module CHECKs. It is the batch's pattern, exported here because a settlement's cancellation
// and a reimbursement's decision carry the same kind of value and a second pattern would be a
// second answer to one question.
func ValidReasonCode(code string) bool { return batchReasonPattern.MatchString(code) }

// ValidCancelReasonText reports whether a settlement's cancellation prose fits the column.
func ValidCancelReasonText(v string) bool { return len([]rune(v)) <= maxSettlementCancelReason }

// NormalizeAccount strips the spaces a person types into an IBAN and upper-cases it.
//
// It is the only normalisation the value receives and it happens once, at the edge, so that
// the string that reaches the cipher and the string the mask is taken from are the same
// string. A mask taken before normalisation and a ciphertext taken after would be a mask that
// did not belong to the account it claimed to describe.
func NormalizeAccount(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if unicode.IsSpace(r) || r == '-' {
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// ValidAccount reports whether a normalised value looks like an account number at all.
func ValidAccount(normalized string) bool { return ibanPattern.MatchString(normalized) }

// MaskAccount returns the last four characters of a normalised account number, and nothing
// else. It is the single place in this repository where an account number becomes something
// that may be stored, logged or sent.
//
// A value shorter than four characters is not an account number and `ValidAccount` has
// already refused it; the guard is here anyway because a masking function that could panic is
// a masking function somebody would eventually wrap in a recover instead of calling correctly.
func MaskAccount(normalized string) string {
	runes := []rune(normalized)
	if len(runes) < MaskedAccountLength {
		return ""
	}
	return string(runes[len(runes)-MaskedAccountLength:])
}

// ValidMaskedAccount reports whether a mask has the shape the column CHECKs. It exists so a
// test can assert that `MaskAccount` and the constraint agree.
func ValidMaskedAccount(v string) bool { return maskedAccountPattern.MatchString(v) }

// DueDate applies a payment term's days to the day the batch was decided. Both are dates:
// a due date computed from an instant would move with the reader's time zone, and "when is
// this due" is a question with one answer.
func DueDate(decidedAt time.Time, dueDays int) time.Time {
	return DateOnly(decidedAt.UTC()).AddDate(0, 0, dueDays)
}
