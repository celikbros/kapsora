package domain

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

// The batch's own rules: the lifecycle, what a decision has to look like, and the one piece
// of arithmetic the whole package rests on — how a cut is spread across the claims the
// invoice was collecting.
//
// Nothing here reads a database or a clock it was not given, and nothing here is a float.

// AggregateBatch is the resource type the audit rows, the outbox events and the work item of
// a batch carry.
const AggregateBatch = "BATCH"

// QueueBatchReview is the WP-I4-03 work queue a submitted batch is raised into. It is a code
// rather than an id because a module raising work cannot know the id of a row an operator
// created, and the code is what the queue is configured under.
const QueueBatchReview = "BATCH_REVIEW"

// The batch lifecycle (v1.2 10.9). WP-I7-03 owns everything up to DECIDED; SETTLING and
// CLOSED are the settlement's, and nothing in this package puts a batch into one of them.
const (
	BatchDraft       = "DRAFT"
	BatchSubmitted   = "SUBMITTED"
	BatchUnderReview = "UNDER_REVIEW"
	BatchDecided     = "DECIDED"
	BatchSettling    = "SETTLING"
	BatchClosed      = "CLOSED"
	BatchCancelled   = "CANCELLED"
)

// BatchStatuses is the whole list, in lifecycle order, for a filter's validation.
var BatchStatuses = []string{
	BatchDraft, BatchSubmitted, BatchUnderReview, BatchDecided, BatchSettling, BatchClosed,
	BatchCancelled,
}

// The four things a payer may say about one invoice in a batch.
//
// They are four words rather than two because a provider acts on each of them differently: an
// approval is money coming, a cut is money to dispute, a return is a document to correct and
// resend, and a rejection is the end of that document. The database ties each of them to an
// amount, so "approved" can never sit beside a figure that was not what was billed.
const (
	DecisionApprove = "APPROVE"
	DecisionCut     = "CUT"
	DecisionReturn  = "RETURN"
	DecisionReject  = "REJECT"
)

// BatchDecisions is the closed list.
var BatchDecisions = []string{DecisionApprove, DecisionCut, DecisionReturn, DecisionReject}

// ValidBatchDecision reports whether a caller named one of the four.
func ValidBatchDecision(decision string) bool {
	for _, d := range BatchDecisions {
		if d == decision {
			return true
		}
	}
	return false
}

// ValidBatchStatus reports whether a filter names a status the lifecycle has.
func ValidBatchStatus(status string) bool {
	for _, s := range BatchStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// ValidDomainCode reports whether a code is in the closed list an invoice and a batch share.
func ValidDomainCode(code string) bool { return domainCodes[code] }

// batchReasonPattern mirrors `ck_billing_batch_invoice_reason_code`. A reason is a code
// somebody can count, group and argue about; the free text beside it stays free.
var batchReasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{1,79}$`)

// maxBatchReasonText mirrors `ck_billing_batch_invoice_reason_text`.
const maxBatchReasonText = 1000

// CanPutBatchInvoices reports whether the membership may still be replaced. Only a draft: a
// submitted batch's invoices never change, and the database refuses it from anywhere else.
func CanPutBatchInvoices(status string) bool { return status == BatchDraft }

// CanSubmitBatch reports whether the batch may be sent to the payer.
func CanSubmitBatch(status string) bool { return status == BatchDraft }

// CanReviewBatch reports whether a decision may be recorded. SUBMITTED is included because
// taking the first decision is what puts the batch in front of a reviewer; the transition
// happens in the same command, so no decision is ever written on a batch that is still only
// SUBMITTED when the command commits.
func CanReviewBatch(status string) bool {
	return status == BatchSubmitted || status == BatchUnderReview
}

// CanDecideBatch reports whether the batch may be closed off.
func CanDecideBatch(status string) bool { return status == BatchUnderReview }

// BatchPeriod is the window a batch collects over.
type BatchPeriod struct {
	From time.Time
	To   time.Time
}

// NewBatchInput is the createBatch command, before anything has been looked up.
type NewBatchInput struct {
	DomainCode   string
	CurrencyCode string
	Period       BatchPeriod
}

// ValidateNewBatch checks everything about a new batch that can be checked without a
// database, and fills in the two defaults.
func ValidateNewBatch(in NewBatchInput) (NewBatchInput, error) {
	ve := &ValidationError{}
	out := in

	out.CurrencyCode = strings.ToUpper(strings.TrimSpace(in.CurrencyCode))
	if out.CurrencyCode == "" {
		out.CurrencyCode = DefaultCurrency
	}
	if !currencyPattern.MatchString(out.CurrencyCode) {
		ve.Add("currencyCode", "FORMAT", "üç harfli para birimi kodu olmalı")
	}

	out.DomainCode = strings.ToUpper(strings.TrimSpace(in.DomainCode))
	if out.DomainCode == "" {
		out.DomainCode = DefaultDomainCode
	}
	if !domainCodes[out.DomainCode] {
		ve.Add("domainCode", "ENUM", "tanımlı bir alan kodu olmalı")
	}

	switch {
	case in.Period.From.IsZero():
		ve.Add("periodFrom", "REQUIRED", "dönem başlangıcı zorunlu")
	case in.Period.From.Year() < minFiscalYear || in.Period.From.Year() > maxFiscalYear:
		ve.Add("periodFrom", "RANGE", "makul bir yılda olmalı")
	}
	switch {
	case in.Period.To.IsZero():
		ve.Add("periodTo", "REQUIRED", "dönem bitişi zorunlu")
	case in.Period.To.Year() < minFiscalYear || in.Period.To.Year() > maxFiscalYear:
		ve.Add("periodTo", "RANGE", "makul bir yılda olmalı")
	}
	if !in.Period.From.IsZero() && !in.Period.To.IsZero() && in.Period.To.Before(in.Period.From) {
		ve.Add("periodTo", "RANGE", "dönem bitişi başlangıcından önce olamaz")
	}
	out.Period = BatchPeriod{From: DateOnly(in.Period.From), To: DateOnly(in.Period.To)}
	return out, ve.OrNil()
}

// BatchDecisionInput is one reviewBatchInvoice command.
type BatchDecisionInput struct {
	Decision string
	// ApprovedAmount is meaningful only on a CUT. The other three decisions derive it — a
	// caller that could send it would be a caller who could approve an amount nobody billed.
	ApprovedAmount string
	ReasonCode     string
	ReasonText     *string
}

// ValidateBatchDecision checks a decision against the amount the invoice was submitted at, and
// answers the approved amount the row will carry.
//
// The amount is derived here rather than trusted from the body for every decision but the cut:
// an APPROVE is the submitted amount exactly, and a RETURN and a REJECT are zero. That is the
// same rule `ck_billing_batch_invoice_decision` holds, and this copy exists so the caller is
// told which field rather than being handed a constraint name.
func ValidateBatchDecision(in BatchDecisionInput, submitted benefitdomain.Quantity) (
	decision string, approved benefitdomain.Quantity, reasonCode string,
	reasonText *string, err error,
) {
	ve := &ValidationError{}
	decision = strings.ToUpper(strings.TrimSpace(in.Decision))
	if !ValidBatchDecision(decision) {
		ve.Add("decision", "ENUM", "APPROVE, CUT, RETURN ya da REJECT olmalı")
		return "", benefitdomain.Quantity{}, "", nil, ve
	}

	reasonCode = strings.ToUpper(strings.TrimSpace(in.ReasonCode))
	if decision != DecisionApprove {
		if reasonCode == "" {
			ve.Add("reasonCode", "REQUIRED", "kesinti, iade ve ret için gerekçe kodu zorunlu")
		} else if !batchReasonPattern.MatchString(reasonCode) {
			ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve _ . : - içerebilir")
		}
	} else if reasonCode != "" && !batchReasonPattern.MatchString(reasonCode) {
		ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve _ . : - içerebilir")
	}

	if in.ReasonText != nil {
		trimmed := strings.TrimSpace(*in.ReasonText)
		if len([]rune(trimmed)) > maxBatchReasonText {
			ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter olabilir")
		}
		if trimmed != "" {
			reasonText = &trimmed
		}
	}

	switch decision {
	case DecisionApprove:
		approved = submitted
	case DecisionReturn, DecisionReject:
		approved = benefitdomain.ZeroQuantity()
	case DecisionCut:
		value, parseErr := benefitdomain.ParseQuantity(strings.TrimSpace(in.ApprovedAmount))
		switch {
		case parseErr != nil:
			ve.Add("approvedAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
		case !value.IsPositive() || value.Cmp(submitted) >= 0:
			// Zero is a rejection and the whole amount is an approval; a cut is what is
			// strictly between them, and calling either of the ends a cut would hide a
			// decision a provider disputes differently.
			ve.Add("approvedAmount", "RANGE",
				"kesinti tutarı sıfırdan büyük ve fatura tutarından küçük olmalı")
		default:
			approved = value
		}
	}
	if err := ve.OrNil(); err != nil {
		return "", benefitdomain.Quantity{}, "", nil, err
	}
	if reasonCode == "" {
		return decision, approved, "", reasonText, nil
	}
	return decision, approved, reasonCode, reasonText, nil
}

// BatchTotals is what the four decisions add up to.
type BatchTotals struct {
	Submitted benefitdomain.Quantity
	Approved  benefitdomain.Quantity
	Cut       benefitdomain.Quantity
	Returned  benefitdomain.Quantity
	Rejected  benefitdomain.Quantity
}

// Reconciles reports whether the four halves are the whole, exactly. It is the same equation
// `ck_billing_batch_totals` holds; this copy exists so a service can refuse before the
// database does and say which figure was wrong.
func (t BatchTotals) Reconciles() bool {
	sum := t.Approved.Add(t.Cut).Add(t.Returned).Add(t.Rejected)
	return sum.Cmp(t.Submitted) == 0
}

// DecidedTotals sums a set of decisions into the four buckets.
//
// **A CUT lands in two buckets and neither of them twice.** What was approved goes to the
// approved total and what was taken off goes to the cut total, and the two of them are the
// submitted amount — which is what makes the reconciliation an identity rather than a hope.
func DecidedTotals(rows []DecidedRow) BatchTotals {
	out := BatchTotals{
		Submitted: benefitdomain.ZeroQuantity(), Approved: benefitdomain.ZeroQuantity(),
		Cut: benefitdomain.ZeroQuantity(), Returned: benefitdomain.ZeroQuantity(),
		Rejected: benefitdomain.ZeroQuantity(),
	}
	for _, row := range rows {
		out.Submitted = out.Submitted.Add(row.Submitted)
		switch row.Decision {
		case DecisionApprove:
			out.Approved = out.Approved.Add(row.Submitted)
		case DecisionCut:
			out.Approved = out.Approved.Add(row.Approved)
			out.Cut = out.Cut.Add(row.Submitted.Sub(row.Approved))
		case DecisionReturn:
			out.Returned = out.Returned.Add(row.Submitted)
		case DecisionReject:
			out.Rejected = out.Rejected.Add(row.Submitted)
		}
	}
	return out
}

// DecidedRow is one decided member, as the totals read it.
type DecidedRow struct {
	Decision  string
	Submitted benefitdomain.Quantity
	Approved  benefitdomain.Quantity
}

// SplitProportional divides one amount across a set of weights, exactly.
//
// It is the arithmetic a CUT rests on: the payer took a figure off an invoice, and that figure
// has to reach the claims the invoice was collecting in proportion to what each of them was
// allocated. Two properties, and both of them are the point:
//
//   - **the shares sum to the total, exactly.** Every share is truncated towards zero and the
//     remainder — which is therefore never negative and never as large as one micro-unit per
//     weight — is added to the largest weight. Rounding each share independently would leave a
//     few kuruş belonging to nobody, and a cut whose adjustments did not add up to the cut is
//     a settlement that cannot be reconciled;
//   - **it is integer arithmetic on micro-units.** No division here goes anywhere near a
//     float, and the same inputs always produce the same shares.
//
// The remainder goes to the *largest* weight rather than the first, because that is the claim
// on which a kuruş is least visible as a proportion of what it carries. Ties are broken by
// order, so the answer is deterministic.
//
// A total of zero answers zeroes. Weights that are all zero put the whole total on the first
// share: there is no proportion to divide by, and dropping the money on the floor would be
// worse than putting it somewhere a reader can find it.
func SplitProportional(total benefitdomain.Quantity, weights []benefitdomain.Quantity) (
	[]benefitdomain.Quantity, error,
) {
	if len(weights) == 0 {
		return nil, fmt.Errorf("billing: a cut cannot be spread across no allocations")
	}
	totalUnits, err := microUnits(total)
	if err != nil {
		return nil, err
	}
	weightUnits := make([]*big.Int, len(weights))
	sum := new(big.Int)
	largest := 0
	for i, w := range weights {
		units, err := microUnits(w)
		if err != nil {
			return nil, err
		}
		if units.Sign() < 0 {
			return nil, fmt.Errorf("billing: an allocation cannot be negative")
		}
		weightUnits[i] = units
		sum.Add(sum, units)
		if units.Cmp(weightUnits[largest]) > 0 {
			largest = i
		}
	}

	shares := make([]*big.Int, len(weights))
	if sum.Sign() == 0 {
		for i := range shares {
			shares[i] = new(big.Int)
		}
		shares[0].Set(totalUnits)
	} else {
		allocated := new(big.Int)
		for i, units := range weightUnits {
			product := new(big.Int).Mul(totalUnits, units)
			shares[i] = product.Quo(product, sum)
			allocated.Add(allocated, shares[i])
		}
		shares[largest].Add(shares[largest], new(big.Int).Sub(totalUnits, allocated))
	}

	out := make([]benefitdomain.Quantity, len(shares))
	for i, share := range shares {
		value, err := quantityOfUnits(share)
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	return out, nil
}

// microUnits renders an exact decimal as an integer number of micro-units.
//
// It goes through the canonical text rather than reaching inside the Quantity, because the
// canonical text is the one representation this codebase guarantees: it is what PostgreSQL is
// handed, what the wire carries and what a test compares. A conversion that agreed with the
// column and disagreed with the string would be a conversion nobody could check.
func microUnits(q benefitdomain.Quantity) (*big.Int, error) {
	text := q.String()
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	intPart, fracPart, _ := strings.Cut(text, ".")
	if len(fracPart) > benefitdomain.QuantityScale {
		return nil, fmt.Errorf("billing: %q has more than %d decimals", q.String(),
			benefitdomain.QuantityScale)
	}
	digits := intPart + fracPart + strings.Repeat("0", benefitdomain.QuantityScale-len(fracPart))
	units, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("billing: %q is not an exact decimal", q.String())
	}
	if negative {
		units.Neg(units)
	}
	return units, nil
}

// quantityOfUnits is the way back.
func quantityOfUnits(units *big.Int) (benefitdomain.Quantity, error) {
	negative := units.Sign() < 0
	digits := new(big.Int).Abs(units).String()
	if len(digits) <= benefitdomain.QuantityScale {
		digits = strings.Repeat("0", benefitdomain.QuantityScale-len(digits)+1) + digits
	}
	text := digits[:len(digits)-benefitdomain.QuantityScale] + "." +
		digits[len(digits)-benefitdomain.QuantityScale:]
	if negative {
		text = "-" + text
	}
	return benefitdomain.ParseQuantity(text)
}

// batchReferenceEncoding is the alphabet a batch reference's random tail is rendered in: upper
// case, no padding, and no character the column CHECK would refuse.
var batchReferenceEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewBatchReference builds `IC-YYYYMM-XXXXXXXX`: the month the batch was opened in, and forty
// random bits. The tail is random rather than sequential because a sequential reference tells a
// competitor how many batches a tenant settled last month.
func NewBatchReference(now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("billing: generate batch reference: %w", err)
	}
	return fmt.Sprintf("IC-%s-%s", now.UTC().Format("200601"),
		batchReferenceEncoding.EncodeToString(buf)), nil
}
