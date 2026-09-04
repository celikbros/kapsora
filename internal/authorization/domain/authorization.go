// Package domain holds what an authorization, a fulfilment and a voucher may look like
// and how they may move: the closed lists the schema repeats as CHECK constraints, the
// transition tables that are the only way through each lifecycle, and the validation of
// everything a caller may send. It depends on nothing outside the standard library and
// the exact-decimal type of the benefit module, so every rule here is unit-testable
// without a database.
//
// Two things this package deliberately does not have. It has no way to write a status:
// an authorization moves when entitlement moves, and a field a caller could set to
// "USED" would make the ledger optional. And it holds no balance arithmetic beyond
// comparing what was approved with what was delivered — the balances themselves belong to
// benefit.entitlement_account and are only ever changed through the ledger.
package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	benefit "github.com/celikbros/kapsora/internal/benefit/domain"
)

// FieldError names one invalid request field; Field uses the JSON path of the contract.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError aggregates field errors for a 422 response.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("authorization: %d validation error(s)", len(e.Fields))
}

// Add appends one field error.
func (e *ValidationError) Add(field, code, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Code: code, Message: message})
}

// Len reports how many field errors were collected.
func (e *ValidationError) Len() int { return len(e.Fields) }

// OrNil returns nil when nothing failed, so callers can `return ve.OrNil()`.
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// ErrValidation lets callers detect a ValidationError with errors.Is.
var ErrValidation = errors.New("authorization: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Authorization statuses (migration 000026).
const (
	StatusActive        = "ACTIVE"
	StatusPartiallyUsed = "PARTIALLY_USED"
	StatusUsed          = "USED"
	StatusExpired       = "EXPIRED"
	StatusCancelled     = "CANCELLED"
)

// Fulfilment statuses (migration 000026).
const (
	FulfilmentRecorded  = "RECORDED"
	FulfilmentCompleted = "COMPLETED"
	FulfilmentCancelled = "CANCELLED"
)

// Voucher statuses (migration 000026).
const (
	VoucherIssued   = "ISSUED"
	VoucherRedeemed = "REDEEMED"
	VoucherExpired  = "EXPIRED"
	VoucherRevoked  = "REVOKED"
)

// The request statuses an authorization may be granted against. Nothing else is a
// promise anybody made: a request still under review has been decided by nobody, and a
// rejected one has been decided against.
const (
	RequestApproved          = "APPROVED"
	RequestPartiallyApproved = "PARTIALLY_APPROVED"
)

// The request line statuses that carry an approved quantity.
const (
	ItemApproved          = "APPROVED"
	ItemPartiallyApproved = "PARTIALLY_APPROVED"
)

// Closed lists the database repeats as CHECK constraints.
var (
	Statuses = []string{
		StatusActive, StatusPartiallyUsed, StatusUsed, StatusExpired, StatusCancelled,
	}
	FulfilmentStatuses = []string{FulfilmentRecorded, FulfilmentCompleted, FulfilmentCancelled}
	VoucherStatuses    = []string{VoucherIssued, VoucherRedeemed, VoucherExpired, VoucherRevoked}
	// AuthorizableRequestStatuses are the two decisions that may be turned into a hold.
	AuthorizableRequestStatuses = []string{RequestApproved, RequestPartiallyApproved}
	// ApprovedItemStatuses are the line outcomes that carry a quantity worth reserving.
	ApprovedItemStatuses = []string{ItemApproved, ItemPartiallyApproved}
	// OpenStatuses are the states in which an authorization still holds entitlement, so
	// they are the states cancellation and expiry have anything to do.
	OpenStatuses = []string{StatusActive, StatusPartiallyUsed}
)

// MaxItems bounds one line set; the OpenAPI schema repeats it.
const MaxItems = 100

// MaxReasonText bounds the free-text half of a reason.
const MaxReasonText = 1000

var reasonCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{1,79}$`)

// Contains reports whether a closed list holds a value.
func Contains(set []string, value string) bool {
	for _, s := range set {
		if s == value {
			return true
		}
	}
	return false
}

// Open reports whether an authorization still holds entitlement.
func Open(status string) bool { return Contains(OpenStatuses, status) }

// NextStatus is where an authorization lands once `consumed` of `reserved` has been
// delivered. There is no entry that leads out of USED, EXPIRED or CANCELLED: a promise
// that ended stays ended, and delivering against it is refused rather than reviving it.
func NextStatus(reserved, consumed benefit.Quantity) string {
	if consumed.Cmp(reserved) >= 0 {
		return StatusUsed
	}
	return StatusPartiallyUsed
}

// NewAuthorization is the create command as a caller sent it.
type NewAuthorization struct {
	ValidFrom time.Time
	ValidTo   time.Time
	// MemberAmounts is the member's share per request line number; a line nobody named
	// carries zero, which is what "the plan pays all of it" means.
	MemberAmounts map[int]string
}

// ValidateNewAuthorization checks a create command. Whether the request it names is
// approved, and whether the lines have anything left to reserve, is the application
// layer's business: only it can read them.
func ValidateNewAuthorization(in NewAuthorization) error {
	ve := &ValidationError{}
	validateWindow(ve, in.ValidFrom, in.ValidTo)
	for lineNo, amount := range in.MemberAmounts {
		path := fmt.Sprintf("items[%d]", lineNo)
		if lineNo < 1 {
			ve.Add(path+".lineNo", "RANGE", "satır numarası birden başlar")
		}
		validateAmount(ve, path+".memberAmount", amount)
	}
	return ve.OrNil()
}

// ValidateExtension checks an extension. `current` is where the authorization ends now:
// an extension only ever moves the end forward, because shortening a promise somebody is
// already relying on is a cancellation with a friendlier name.
func ValidateExtension(current, validTo time.Time, reasonCode string, reasonText *string) error {
	ve := &ValidationError{}
	if validTo.IsZero() {
		ve.Add("validTo", "REQUIRED", "yeni bitiş zamanı zorunlu")
	} else if !validTo.After(current) {
		ve.Add("validTo", "RANGE", "yeni bitiş zamanı mevcut bitişten sonra olmalı")
	}
	if reasonCode != "" && !reasonCodePattern.MatchString(reasonCode) {
		ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-80 karakter")
	}
	if reasonText != nil && utf8.RuneCountInString(*reasonText) > MaxReasonText {
		ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter")
	}
	return ve.OrNil()
}

// ValidateReason checks the reason a command carries. Every withdrawal owes one: an
// authorization that disappeared without a reason leaves a member with nothing to appeal
// against.
func ValidateReason(reasonCode string, reasonText *string) error {
	ve := &ValidationError{}
	if !reasonCodePattern.MatchString(reasonCode) {
		ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-80 karakter")
	}
	if reasonText != nil && utf8.RuneCountInString(*reasonText) > MaxReasonText {
		ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter")
	}
	return ve.OrNil()
}

// FulfilmentItemInput is one delivered line as a caller sent it. The decimals arrive as
// text and stay exact all the way to the numeric column.
type FulfilmentItemInput struct {
	AuthorizationItemID string
	ActualQuantity      string
	ActualAmount        string
}

// NewFulfilment is the record command.
type NewFulfilment struct {
	PerformedAt time.Time
	Items       []FulfilmentItemInput
}

// ValidateNewFulfilment checks a record command. Whether the lines belong to the
// authorization named, and whether there is enough left on them, needs the stored rows
// and is checked in the application layer.
func ValidateNewFulfilment(in NewFulfilment) error {
	ve := &ValidationError{}
	if in.PerformedAt.IsZero() {
		ve.Add("performedAt", "REQUIRED", "hizmetin verildiği zaman zorunlu")
	}
	switch {
	case len(in.Items) == 0:
		ve.Add("items", "REQUIRED", "en az bir kalem gerekli")
		return ve.OrNil()
	case len(in.Items) > MaxItems:
		ve.Add("items", "RANGE", "en fazla 100 kalem gönderilebilir")
		return ve.OrNil()
	}
	seen := make(map[string]int, len(in.Items))
	for i, item := range in.Items {
		path := fmt.Sprintf("items[%d]", i)
		id := strings.TrimSpace(item.AuthorizationItemID)
		switch first, dup := seen[id]; {
		case id == "":
			ve.Add(path+".authorizationItemId", "REQUIRED", "ön onay kalemi zorunlu")
		case dup:
			ve.Add(path+".authorizationItemId", "DUPLICATE",
				fmt.Sprintf("bu kalem items[%d] içinde de var", first))
		default:
			seen[id] = i
		}
		quantity, err := benefit.ParseQuantity(item.ActualQuantity)
		switch {
		case err != nil:
			ve.Add(path+".actualQuantity", "FORMAT", "kesin ondalık bir sayı olmalı")
		case !quantity.IsPositive():
			ve.Add(path+".actualQuantity", "RANGE", "miktar sıfırdan büyük olmalı")
		}
		validateAmount(ve, path+".actualAmount", item.ActualAmount)
	}
	return ve.OrNil()
}

// ValidateVoucherWindow checks the window an issue command asks for against the
// authorization's own. A voucher may not outlive the hold behind it: a token that still
// works after the entitlement has been released is a promise with nothing under it.
func ValidateVoucherWindow(validFrom, validTo, authFrom, authTo time.Time) error {
	ve := &ValidationError{}
	validateWindow(ve, validFrom, validTo)
	if validFrom.Before(authFrom) {
		ve.Add("validFrom", "RANGE", "kupon ön onaydan önce başlayamaz")
	}
	if validTo.After(authTo) {
		ve.Add("validTo", "RANGE", "kupon ön onaydan sonra bitemez")
	}
	return ve.OrNil()
}

// ValidateStatusFilter checks the list filter's status against the closed list, so an
// unknown value is a field error rather than a silently empty page.
func ValidateStatusFilter(status string, allowed []string) error {
	if status == "" {
		return nil
	}
	ve := &ValidationError{}
	if !Contains(allowed, status) {
		ve.Add("status", "ENUM", "geçerli değerler: "+strings.Join(allowed, ", "))
	}
	return ve.OrNil()
}

func validateWindow(ve *ValidationError, from, to time.Time) {
	if to.IsZero() {
		ve.Add("validTo", "REQUIRED", "bitiş zamanı zorunlu")
		return
	}
	if !from.IsZero() && !to.After(from) {
		ve.Add("validTo", "RANGE", "bitiş zamanı başlangıçtan sonra olmalı")
	}
}

func validateAmount(ve *ValidationError, field, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	value, err := benefit.ParseQuantity(raw)
	switch {
	case err != nil:
		ve.Add(field, "FORMAT", "kesin ondalık bir sayı olmalı")
	case value.IsNegative():
		ve.Add(field, "RANGE", "tutar negatif olamaz")
	}
}

// TokenBytes is the entropy of a voucher token. It is the session's own size (v1.2
// section 19.2: unguessable identifiers), because a voucher is a bearer credential too:
// whoever holds it can spend somebody else's entitlement.
const TokenBytes = 32

// NewToken returns base64url-encoded random bytes with no padding. The plaintext lives in
// the issue response and in the member's hand, never in a column, a log or an audit row.
func NewToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("authorization: generate voucher token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// TokenHash is what the database stores.
func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// maskedTail is how much of a token the masked form keeps.
const maskedTail = 4

// MaskToken is what an operator reads back to confirm which voucher is in front of them.
// It keeps the last four characters of a token that has 256 bits of entropy, so it
// identifies the voucher without narrowing a guess to anything a lifetime would exhaust.
func MaskToken(token string) string {
	runes := []rune(token)
	if len(runes) <= maskedTail {
		return strings.Repeat("*", len(runes))
	}
	return "****" + string(runes[len(runes)-maskedTail:])
}
