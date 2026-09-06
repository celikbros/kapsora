package domain

import (
	"fmt"
	"strings"
)

// The lodging terms of a contract version (WP-I6-04, migration 000039): what a
// cancellation costs, what a no-show costs, and the shape of a stay the agreement allows.
//
// The vocabulary is here rather than in the accommodation module for the same reason the
// payment term's is: this is a term of the contract, written by the contract desk on a
// draft version, and the booking module only ever reads the snapshot of it.
const (
	// PenaltyNightsKind charges a number of nights of the stay.
	PenaltyNightsKind = "NIGHTS"
	// PenaltyPercentKind charges a percentage of the member's own share.
	PenaltyPercentKind = "PERCENT"
)

// PenaltyKinds is the closed list the column CHECK repeats.
var PenaltyKinds = []string{PenaltyNightsKind, PenaltyPercentKind}

// The bounds migration 000039 states as CHECK constraints. They are named here so the
// refusal a caller reads and the refusal the database would raise are the same number.
const (
	// MaxFreeCancellationHours is a year. A free-cancellation window longer than the
	// longest booking horizon anybody sells is a typo rather than a policy.
	MaxFreeCancellationHours = 8760
	// MaxPenaltyNights bounds the nights a cancellation may cost.
	MaxPenaltyNights = 365
	// MaxHoldMinutes is a day: a hold is a countdown a member watches, not a reservation.
	MaxHoldMinutes = 1440
	// MaxStayNights bounds both ends of the allowed stay length.
	MaxStayNights = 365
	// MaxChildFreeAge is the oldest age a contract may still call a child.
	MaxChildFreeAge = 18
	// LodgingPercentScale is the numeric(7,4) scale of the two percentages.
	LodgingPercentScale = 4
)

// DefaultTimeZone is the zone a lodging policy snapshot is stamped with when the caller's
// context carries none. It is the same zone tenant provisioning defaults to, and it exists
// so a snapshot can never be written without one: a free-cancellation window counted in an
// unstated zone is a fee that depends on who is reading it, which is the single thing this
// snapshot exists to prevent.
const DefaultTimeZone = "Europe/Istanbul"

// LodgingTermsInput is the whole of a version's lodging terms as a caller submits them.
//
// The two percentages are exact decimal strings and never floats, for the reason every
// money value in this package is one: a no-show fee of 42.5% of a member share has one
// exact answer, and a fee two systems disagree about by a kuruş is a fee nobody can
// invoice. The optional integers are pointers because "not stated" and "zero" are
// different contracts: a NULL `childFreeUnderAge` is an agreement that says nothing about
// children, and a zero would be an agreement that says no child is free.
type LodgingTermsInput struct {
	FreeCancellationHoursBefore int
	PenaltyKind                 string
	PenaltyNights               *int
	PenaltyPercent              string
	NoShowPercent               string
	HoldMinutes                 *int
	MinNights                   int
	MaxNights                   *int
	ChildFreeUnderAge           *int
}

// ValidateLodgingTerms checks the terms against the same rules migration 000039 states as
// CHECK constraints, so a caller gets a field error naming what is wrong instead of a raw
// constraint violation.
//
// The exactly-one-penalty rule is the one worth being explicit about. "3" meaning three
// nights and "3" meaning three percent are two different numbers, and a submission that
// carried both would leave the reader of the contract to guess which one the member
// agreed to. The kind names one of them and the other must be absent.
func ValidateLodgingTerms(in LodgingTermsInput) error {
	ve := &ValidationError{}

	if in.FreeCancellationHoursBefore < 0 || in.FreeCancellationHoursBefore > MaxFreeCancellationHours {
		ve.Add("freeCancellationHoursBefore", "RANGE",
			fmt.Sprintf("0 ile %d arasında olmalı", MaxFreeCancellationHours))
	}

	switch in.PenaltyKind {
	case PenaltyNightsKind:
		if in.PenaltyNights == nil {
			ve.Add("penaltyNights", "REQUIRED", "NIGHTS cezasında gece sayısı zorunlu")
		} else if *in.PenaltyNights < 0 || *in.PenaltyNights > MaxPenaltyNights {
			ve.Add("penaltyNights", "RANGE", fmt.Sprintf("0 ile %d arasında olmalı", MaxPenaltyNights))
		}
		forbidField(ve, "penaltyPercent", in.PenaltyPercent)
	case PenaltyPercentKind:
		requireField(ve, "penaltyPercent", in.PenaltyPercent)
		validateLodgingPercent(ve, "penaltyPercent", in.PenaltyPercent)
		if in.PenaltyNights != nil {
			ve.Add("penaltyNights", "FORBIDDEN", "bu yöntemle birlikte verilemez")
		}
	default:
		ve.Add("penaltyKind", "ENUM", "NIGHTS veya PERCENT olmalı")
	}

	if strings.TrimSpace(in.NoShowPercent) == "" {
		ve.Add("noShowPercent", "REQUIRED", "gelmeme durumunda alınacak oran zorunlu")
	} else {
		validateLodgingPercent(ve, "noShowPercent", in.NoShowPercent)
	}

	if in.HoldMinutes != nil && (*in.HoldMinutes < 1 || *in.HoldMinutes > MaxHoldMinutes) {
		ve.Add("holdMinutes", "RANGE", fmt.Sprintf("1 ile %d arasında olmalı", MaxHoldMinutes))
	}

	if in.MinNights < 1 || in.MinNights > MaxStayNights {
		ve.Add("minNights", "RANGE", fmt.Sprintf("1 ile %d arasında olmalı", MaxStayNights))
	}
	if in.MaxNights != nil {
		switch {
		case *in.MaxNights < 1 || *in.MaxNights > MaxStayNights:
			ve.Add("maxNights", "RANGE", fmt.Sprintf("1 ile %d arasında olmalı", MaxStayNights))
		case *in.MaxNights < in.MinNights:
			ve.Add("maxNights", "RANGE", "en az gece sayısından küçük olamaz")
		}
	}
	if in.ChildFreeUnderAge != nil && (*in.ChildFreeUnderAge < 0 || *in.ChildFreeUnderAge > MaxChildFreeAge) {
		ve.Add("childFreeUnderAge", "RANGE", fmt.Sprintf("0 ile %d arasında olmalı", MaxChildFreeAge))
	}

	return ve.OrNil()
}

// validateLodgingPercent is validatePercent with the numeric(7,4) scale of the two
// columns. A submission with more decimals than the column keeps would be silently
// rounded on the way in, and a fee the caller never typed is a fee nobody agreed to.
func validateLodgingPercent(ve *ValidationError, field, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	before := ve.Len()
	q := validatePercent(ve, field, raw)
	if q == nil || ve.Len() > before {
		return
	}
	if _, frac, found := strings.Cut(q.String(), "."); found && len(frac) > LodgingPercentScale {
		ve.Add(field, "SCALE", fmt.Sprintf("en fazla %d ondalık basamak olabilir", LodgingPercentScale))
	}
}
