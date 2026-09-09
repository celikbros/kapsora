// Package domain holds the invoice's own rules: the lifecycle, what a header has to look
// like, and the two pieces of arithmetic the whole package rests on.
//
// Nothing here reads a database or a clock it was not given, and nothing here is a float.
// Two decisions in particular live here rather than in the service:
//
//   - **the header adds up.** `line_extension + tax = payable` is checked in exact decimals
//     before anything is written. The database CHECKs it too; this copy exists so a caller
//     is told which field rather than being handed a constraint name.
//   - **the freeze.** `CanEdit` and `CanAllocate` answer "is this invoice still a draft" in
//     one place, so "a submitted invoice never changes" is a transition table rather than an
//     `if` somebody has to remember to write in five commands.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

// AggregateInvoice is the resource type the audit rows, the outbox events and the WP-I4-04
// document link of an invoice carry.
const AggregateInvoice = "INVOICE"

// DocumentTypeInvoice is the `document.link.document_type_code` of the scanned image.
const DocumentTypeInvoice = "INVOICE"

// The invoice lifecycle (v1.2 10.9). WP-I7-02 owns DRAFT, SUBMITTED and CANCELLED. The rest
// are declared because the lifecycle is one list and a list with a hole in it is a list
// nobody can read; WP-I7-03 owns the reviewer's four, the icmal owns IN_BATCH and the
// settlement owns SETTLED, and nothing in this package puts an invoice into one of them.
const (
	StatusDraft             = "DRAFT"
	StatusSubmitted         = "SUBMITTED"
	StatusInBatch           = "IN_BATCH"
	StatusReturned          = "RETURNED"
	StatusApproved          = "APPROVED"
	StatusPartiallyApproved = "PARTIALLY_APPROVED"
	StatusRejected          = "REJECTED"
	StatusSettled           = "SETTLED"
	StatusCancelled         = "CANCELLED"
)

// Statuses is the whole list, in lifecycle order, for a filter's validation.
var Statuses = []string{
	StatusDraft, StatusSubmitted, StatusInBatch, StatusReturned, StatusApproved,
	StatusPartiallyApproved, StatusRejected, StatusSettled, StatusCancelled,
}

// Where the document came from. Only MANUAL is reachable now: KAPSORA issues no fiscal
// document, and the e-document arriving from GİB through the integrator is M8's.
const (
	SourceManual    = "MANUAL"
	SourceEDocument = "EDOCUMENT"
)

// The default currency and domain of an invoice whose caller named neither.
const (
	DefaultCurrency   = "TRY"
	DefaultDomainCode = "GENERIC"
)

// The domain codes an invoice may carry, the same closed list `claim.claim` uses. One
// vocabulary for "which vertical is this", so a settlement never has to reconcile two.
var domainCodes = map[string]bool{
	"GENERIC": true, "HEALTH": true, "ACCOMMODATION": true, "ASSISTANCE": true,
	"EDUCATION": true, "SPORT": true, "TRANSPORT": true, "CARE": true, "OTHER": true,
}

// The claim statuses an allocation may name. They are the two the payer has answered: an
// allocation against anything else is money being collected for something nobody agreed to.
const (
	ClaimApproved          = "APPROVED"
	ClaimPartiallyApproved = "PARTIALLY_APPROVED"
	ClaimInvoiced          = "INVOICED"
)

// FieldError is one rejected field, as the transport renders it.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError collects field errors so a caller is told everything that is wrong at once
// rather than one thing per round trip.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("billing: %d validation error(s)", len(e.Fields))
}

// Add appends one field error.
func (e *ValidationError) Add(field, code, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Code: code, Message: message})
}

// Len reports how many field errors were collected.
func (e *ValidationError) Len() int { return len(e.Fields) }

// OrNil returns nil when nothing failed.
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// ErrValidation lets callers detect a ValidationError with errors.Is.
var ErrValidation = errors.New("billing: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

var (
	// invoiceNumberPattern is what a Turkish e-invoice serial actually looks like plus the
	// separators a paper document uses. No control characters: this string is rendered on a
	// screen and quoted on a telephone.
	invoiceNumberPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)
	currencyPattern      = regexp.MustCompile(`^[A-Z]{3}$`)
)

// The bounds a header has to be inside. The date bound is generous on both sides because a
// provider entering last year's invoice is ordinary and a provider entering next century's
// has made a typo.
const (
	maxNotesLength = 2000
	minFiscalYear  = 2000
	maxFiscalYear  = 2100
)

// Header is one invoice as the provider entered it, before anything has been written.
type Header struct {
	InvoiceNumber       string
	InvoiceDate         time.Time
	CurrencyCode        string
	LineExtensionAmount string
	TaxAmount           string
	PayableAmount       string
	VatRate             *string
	DomainCode          string
	Notes               *string
}

// ValidateHeader checks everything about a header that can be checked without a database.
//
// The arithmetic is the point: `lineExtension + tax = payable`, in exact decimals, on the
// values as they will be stored. A service that rounded the tax independently is refused here
// by a kuruş rather than publishing a document nobody can reconcile.
func ValidateHeader(h Header) (Header, error) {
	ve := &ValidationError{}
	out := h

	out.InvoiceNumber = strings.TrimSpace(h.InvoiceNumber)
	if !invoiceNumberPattern.MatchString(out.InvoiceNumber) {
		ve.Add("invoiceNumber", "FORMAT",
			"harf, rakam ve . _ / - dışında karakter içeremez; en fazla 64 karakter")
	}
	if h.InvoiceDate.IsZero() {
		ve.Add("invoiceDate", "REQUIRED", "fatura tarihi zorunlu")
	} else if year := h.InvoiceDate.Year(); year < minFiscalYear || year > maxFiscalYear {
		ve.Add("invoiceDate", "RANGE", "fatura tarihi makul bir yılda olmalı")
	}

	out.CurrencyCode = strings.ToUpper(strings.TrimSpace(h.CurrencyCode))
	if out.CurrencyCode == "" {
		out.CurrencyCode = DefaultCurrency
	}
	if !currencyPattern.MatchString(out.CurrencyCode) {
		ve.Add("currencyCode", "FORMAT", "üç harfli para birimi kodu olmalı")
	}

	out.DomainCode = strings.ToUpper(strings.TrimSpace(h.DomainCode))
	if out.DomainCode == "" {
		out.DomainCode = DefaultDomainCode
	}
	if !domainCodes[out.DomainCode] {
		ve.Add("domainCode", "ENUM", "tanımlı bir alan kodu olmalı")
	}

	lines, linesOK := amountField(ve, "lineExtensionAmount", h.LineExtensionAmount)
	tax, taxOK := amountField(ve, "taxAmount", h.TaxAmount)
	payable, payableOK := amountField(ve, "payableAmount", h.PayableAmount)
	if linesOK && taxOK && payableOK {
		if lines.Add(tax).Cmp(payable) != 0 {
			// The field named is `payableAmount` because that is the one a person retypes:
			// the two halves are read off the provider's document and the total is what they
			// got wrong.
			ve.Add("payableAmount", "SUM",
				"mal/hizmet toplamı ile vergi toplamının tam olarak eşiti olmalı")
		}
		out.LineExtensionAmount = lines.String()
		out.TaxAmount = tax.String()
		out.PayableAmount = payable.String()
	}

	if h.VatRate != nil {
		rate, err := benefitdomain.ParseQuantity(strings.TrimSpace(*h.VatRate))
		switch {
		case err != nil:
			ve.Add("vatRate", "FORMAT", "kesin ondalık bir sayı olmalı")
		case rate.IsNegative() || rate.Cmp(benefitdomain.MustQuantity("100")) > 0:
			ve.Add("vatRate", "RANGE", "0 ile 100 arasında olmalı")
		default:
			value := rate.String()
			out.VatRate = &value
		}
	}

	if h.Notes != nil {
		notes := strings.TrimSpace(*h.Notes)
		if len([]rune(notes)) > maxNotesLength {
			ve.Add("notes", "LENGTH", "en fazla 2000 karakter olabilir")
		}
		if notes == "" {
			out.Notes = nil
		} else {
			out.Notes = &notes
		}
	}

	return out, ve.OrNil()
}

// amountField parses one money field, or records why it could not be read. It refuses a
// negative: an invoice for a negative amount is a credit note, which is a different document
// with a different number, and accepting one here would let a provider bill a negative total
// that happened to satisfy the sum.
func amountField(ve *ValidationError, field, raw string) (benefitdomain.Quantity, bool) {
	value, err := benefitdomain.ParseQuantity(strings.TrimSpace(raw))
	if err != nil {
		ve.Add(field, "FORMAT", "kesin ondalık bir sayı olmalı")
		return benefitdomain.Quantity{}, false
	}
	if value.IsNegative() {
		ve.Add(field, "RANGE", "negatif olamaz")
		return benefitdomain.Quantity{}, false
	}
	return value, true
}

// FiscalYear is the year an invoice's number is unique inside. It is derived from the date
// and stored, and the database CHECKs that the stored value is this one: without that, the
// uniqueness rule of 11.12 could be evaded by typing a different year beside the date.
func FiscalYear(date time.Time) int { return date.Year() }

// DateOnly strips the time from a date the transport parsed, so an invoice dated "on the
// 3rd" is the 3rd whatever timezone the caller's clock was in.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// CanEdit reports whether the header may still be changed. Only a draft: a submitted
// invoice's figures never change, and a correction is a new invoice in a chain.
func CanEdit(status string) bool { return status == StatusDraft }

// CanAllocate reports whether the claim links may still be changed. It is the same answer as
// CanEdit and is spelled separately because they are two different promises to two different
// readers, and one of them could one day stop being the other.
func CanAllocate(status string) bool { return status == StatusDraft }

// CanSubmit reports whether the invoice may be sent to the payer.
func CanSubmit(status string) bool { return status == StatusDraft }

// CanCancel reports whether the invoice may be withdrawn. A draft the provider is still
// building and a returned invoice the payer sent back: everything else is either already
// being collected or already finished, and withdrawing it would be rewriting the payer's
// own record.
func CanCancel(status string) bool {
	return status == StatusDraft || status == StatusReturned
}

// CanSupersede reports whether an invoice may be corrected by a new one. A returned invoice
// is the ordinary case; a rejected one is the same situation with a harder word on it.
//
// A SUBMITTED invoice is deliberately not correctable: it is in front of a reviewer, and the
// answer to "I sent the wrong figures" while somebody is looking at them is to have it
// returned, not to slide a second document underneath.
func CanSupersede(status string) bool {
	return status == StatusReturned || status == StatusRejected
}

// ClaimIsInvoiceable reports whether a claim in this status may be allocated. It is the same
// pair the deferred trigger enforces; this copy exists so the caller is told which claim.
func ClaimIsInvoiceable(status string) bool {
	return status == ClaimApproved || status == ClaimPartiallyApproved
}

// ValidStatus reports whether a filter names a status the lifecycle has.
func ValidStatus(status string) bool {
	for _, s := range Statuses {
		if s == status {
			return true
		}
	}
	return false
}

// ValidCurrency reports whether a filter names a three-letter currency code.
func ValidCurrency(code string) bool { return currencyPattern.MatchString(code) }

// WithinTolerance reports whether two amounts agree closely enough for the invoice to be
// submitted, and by how much they differ.
//
// The difference is signed from the invoice's point of view — payable minus allocated — so a
// screen can say "1.50 short" rather than "off by 1.50", and it is compared against the
// tolerance as an absolute value, because an invoice that allocates a lira too much is
// exactly as wrong as one that allocates a lira too little.
func WithinTolerance(payable, allocated, tolerance benefitdomain.Quantity) (ok bool,
	difference benefitdomain.Quantity,
) {
	difference = payable.Sub(allocated)
	magnitude := difference
	if magnitude.IsNegative() {
		magnitude = magnitude.Neg()
	}
	return magnitude.Cmp(tolerance) <= 0, difference
}
