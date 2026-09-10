package domain

import (
	"fmt"
	"strings"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

// Where a claim came from (migration 000043). `case_id` answered this for exactly one
// vertical; the pair answers it for all three, and it is what makes "every completed stay is
// a claim the provider can invoice" a sentence about the same table as a treated encounter.
const (
	SourceHealthCase    = "HEALTH_CASE"
	SourceBooking       = "BOOKING"
	SourceReimbursement = "REIMBURSEMENT"
)

// SourceTypes is the closed list.
var SourceTypes = []string{SourceHealthCase, SourceBooking, SourceReimbursement}

// The domain codes this package writes. HEALTH is WP-I5-04's; ACCOMMODATION is what a stay
// bills under, and it is the same table and the same lifecycle — a settlement that had to
// branch on the vertical would be a settlement with a vertical list in it.
const (
	DomainAccommodation = "ACCOMMODATION"
)

// The four kinds of adjustment. The first three are money that moved for a reason that is not
// a line; REVERSAL is the only way one of them is undone, and it is a row of its own because
// an edited adjustment would be an adjustment nobody can prove was ever different.
const (
	AdjustmentCut        = "CUT"
	AdjustmentRecovery   = "RECOVERY"
	AdjustmentCorrection = "CORRECTION"
	AdjustmentReversal   = "REVERSAL"
)

// AdjustmentTypes is the closed list a caller may name. REVERSAL is absent on purpose: a
// reversal is raised by naming what it reverses, not by asking for one.
var AdjustmentTypes = []string{AdjustmentCut, AdjustmentRecovery, AdjustmentCorrection}

// Where an adjustment came from, in one vocabulary for every vertical. The two system sources
// name the fee row they were written from; the three a person raises name nothing.
const (
	AdjustmentSourceCancellation = "CANCELLATION"
	AdjustmentSourceNoShow       = "NO_SHOW"
	AdjustmentSourceReview       = "REVIEW"
	AdjustmentSourceManual       = "MANUAL"
	AdjustmentSourceRecovery     = "RECOVERY"
)

// AdjustmentSources a caller may name. CANCELLATION and NO_SHOW are absent: they name a fee row
// of WP-I6-03, and a caller that could claim one could claim a fee no booking ever assessed.
//
// Nothing writes those two today — a fee claim carries its fee on its line and needs no ledger
// row to say so — and the vocabulary is kept for the day money really does move against a
// cancellation or a no-show row.
var AdjustmentSources = []string{
	AdjustmentSourceReview, AdjustmentSourceManual, AdjustmentSourceRecovery,
}

// ReasonReviewReversed is the reason a reversal carries when the reviewer who wrote the
// adjustment has changed their mind rather than found a mistake. It is named because
// WP-I7-03's changed decision reverses without a person typing anything, and a reason code
// spelled in two places is two codes a report has to add together.
const ReasonReviewReversed = "REVIEW_REVERSED"

// AdjustmentReasons is the closed list section 2.3 asks for.
//
// It is closed because an adjustment is what a provider disputes, and a dispute is answerable
// only if the reason is a code somebody can count, group and argue about rather than a
// sentence one reviewer typed. The free text beside it stays free; the code does not.
var AdjustmentReasons = []string{
	// A cut: the payer took money off a line it otherwise accepted.
	"TARIFF_EXCEEDED", "CONTRACT_TERMS", "NOT_COVERED", "DUPLICATE_SERVICE",
	"DOCUMENT_MISSING",
	// A recovery: money that was already paid comes back.
	"OVERPAYMENT", "DUPLICATE_PAYMENT", "MEMBER_LIABILITY",
	// A correction: an arithmetic or pricing mistake, either way.
	"ARITHMETIC_ERROR", "PRICE_CORRECTION", "CURRENCY_CORRECTION",
	// A reversal: the adjustment should not have been made.
	"REVIEW_REVERSED", "ENTERED_IN_ERROR",
}

// CutReasons is the subset a CUT may carry: the five reasons a payer takes money off a line
// it otherwise accepted. It is named separately because WP-I7-03's icmal decision writes cut
// adjustments from a reason the reviewer chose, and a reviewer offered "OVERPAYMENT" as a
// reason for a cut would be offered a word that means something else in this ledger.
var CutReasons = []string{
	"TARIFF_EXCEEDED", "CONTRACT_TERMS", "NOT_COVERED", "DUPLICATE_SERVICE",
	"DOCUMENT_MISSING",
}

// KnownCutReason reports whether a reason code is one a cut may carry.
func KnownCutReason(code string) bool {
	for _, known := range CutReasons {
		if known == code {
			return true
		}
	}
	return false
}

// KnownAdjustmentReason reports whether a reason code is in the closed list.
func KnownAdjustmentReason(code string) bool {
	for _, known := range AdjustmentReasons {
		if known == code {
			return true
		}
	}
	return false
}

// NewAdjustment is one adjustment as it arrives, before anything has been looked up.
type NewAdjustment struct {
	AdjustmentType string
	Amount         string
	PayerAmount    string
	MemberAmount   string
	CurrencyCode   string
	ReasonCode     string
	ReasonText     *string
	SourceType     string
}

// ValidateAdjustment checks an adjustment command against the column CHECKs and the closed
// lists, so a caller is told which field is wrong rather than handed a constraint name.
//
// **The split is the point, again.** `payerAmount + memberAmount` has to be `amount` exactly,
// in exact decimals, and it is a database CHECK as well — this is the copy that says which
// field. A CUT of 100 that the reviewer split 60/50 is refused here with a sentence, and
// would be refused underneath with `ck_claim_adjustment_split` if this function were ever
// removed.
func ValidateAdjustment(in NewAdjustment) error {
	ve := &ValidationError{}

	known := false
	for _, kind := range AdjustmentTypes {
		if kind == in.AdjustmentType {
			known = true
			break
		}
	}
	if !known {
		ve.Add("adjustmentType", "ENUM", "geçerli değerler: "+strings.Join(AdjustmentTypes, ", "))
	}
	if in.SourceType != "" {
		knownSource := false
		for _, source := range AdjustmentSources {
			if source == in.SourceType {
				knownSource = true
				break
			}
		}
		if !knownSource {
			ve.Add("sourceType", "ENUM", "geçerli değerler: "+strings.Join(AdjustmentSources, ", "))
		}
	}
	if !KnownAdjustmentReason(in.ReasonCode) {
		ve.Add("reasonCode", "ENUM", "tanımlı bir düzeltme gerekçesi olmalı")
	}
	if in.ReasonText != nil && len([]rune(*in.ReasonText)) > MaxReasonText {
		ve.Add("reasonText", "LENGTH", fmt.Sprintf("en fazla %d karakter", MaxReasonText))
	}
	if in.CurrencyCode != "" && !currencyPattern.MatchString(in.CurrencyCode) {
		ve.Add("currencyCode", "FORMAT", "üç harfli para birimi kodu olmalı")
	}

	amount, aErr := benefitdomain.ParseQuantity(in.Amount)
	if aErr != nil {
		ve.Add("amount", "FORMAT", "kesin ondalık bir sayı olmalı")
	}
	payer, pErr := benefitdomain.ParseQuantity(in.PayerAmount)
	if pErr != nil {
		ve.Add("payerAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
	}
	member, mErr := benefitdomain.ParseQuantity(in.MemberAmount)
	if mErr != nil {
		ve.Add("memberAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
	}
	if aErr != nil || pErr != nil || mErr != nil {
		return ve.OrNil()
	}
	if payer.Add(member).Cmp(amount) != 0 {
		ve.Add("memberAmount", "SPLIT",
			"ödeyici ve hak sahibi payları düzeltme tutarına eşit olmalı")
	}
	// A cut takes money away and a recovery takes money back; a correction may go either
	// way. It is the same sign rule the column carries, so a caller reads a sentence rather
	// than `ck_claim_adjustment_sign`.
	if in.AdjustmentType != AdjustmentCorrection && amount.IsNegative() {
		ve.Add("amount", "RANGE", "kesinti ve tahsilat tutarı negatif olamaz")
	}
	if amount.Cmp(benefitdomain.ZeroQuantity()) == 0 {
		ve.Add("amount", "RANGE", "sıfır tutarlı düzeltme yazılamaz")
	}
	return ve.OrNil()
}
