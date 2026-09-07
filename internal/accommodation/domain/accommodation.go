// Package domain holds the vocabulary and the arithmetic of the accommodation vertical:
// what a property, a room type and a night's allotment are allowed to say, and how many
// nights lie between two dates.
//
// It is pure. Nothing here reads a clock it was not given, opens a transaction or knows
// what a request looks like; every rule in this file can be table-tested without a
// database, which is the point — the two rules the vertical stands on (a night is a civil
// day in the property's own zone, and a missing allotment is not an allotment) are exactly
// the rules that are easiest to get subtly wrong and hardest to notice.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// FieldError is one rejected field, as the transport renders it.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError collects field errors so a caller is told everything that is wrong at
// once rather than one thing per round trip.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("accommodation: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("accommodation: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Property types (migration 000040).
const (
	PropertyHotel          = "HOTEL"
	PropertyResort         = "RESORT"
	PropertyGuesthouse     = "GUESTHOUSE"
	PropertySocialFacility = "SOCIAL_FACILITY"
	PropertyOther          = "OTHER"
)

// PropertyTypes is the closed list, in the order the CHECK states it.
var PropertyTypes = []string{
	PropertyHotel, PropertyResort, PropertyGuesthouse, PropertySocialFacility, PropertyOther,
}

// Statuses of a property and of a room type.
const (
	StatusActive   = "ACTIVE"
	StatusInactive = "INACTIVE"
)

// Statuses is the closed list.
var Statuses = []string{StatusActive, StatusInactive}

// UnitNight is the entitlement unit a room type's service is measured in
// (benefit.entitlement_definition.unit_type, migration 000004). It is spelled here because
// this vertical compares against it in two places -- the room type write, which refuses a
// service measured in anything else, and the search, which reads a remaining balance as a
// count of nights rather than a sum of money.
const UnitNight = "NIGHT"

// The amenity keys a property may declare. It is a closed list and it lives here rather
// than in a CHECK for two reasons: it will grow, and an amenity nobody has heard of should
// be a 422 naming the field rather than a constraint violation with a SQLSTATE in it.
//
// Every key is a fact about a building. None of them is, or may ever become, a fact about
// a guest: there is no ACCESSIBLE_GUEST, no DIETARY_ and no MEDICAL_ key, because an
// amenities array is written by a provider clerk and read by everybody, and a slot a
// health need could be typed into is a slot it eventually would be.
const (
	AmenityWifi            = "WIFI"
	AmenityParking         = "PARKING"
	AmenityBreakfast       = "BREAKFAST"
	AmenityHalfBoard       = "HALF_BOARD"
	AmenityFullBoard       = "FULL_BOARD"
	AmenityAllInclusive    = "ALL_INCLUSIVE"
	AmenityPool            = "POOL"
	AmenitySpa             = "SPA"
	AmenityGym             = "GYM"
	AmenityAirConditioning = "AIR_CONDITIONING"
	AmenityRestaurant      = "RESTAURANT"
	AmenityBeach           = "BEACH"
	AmenityStepFreeAccess  = "STEP_FREE_ACCESS"
	AmenityPetFriendly     = "PET_FRIENDLY"
	AmenityFamilyRoom      = "FAMILY_ROOM"
	AmenityShuttle         = "SHUTTLE"
	AmenityLaundry         = "LAUNDRY"
	AmenityMeetingRoom     = "MEETING_ROOM"
	AmenityKitchenette     = "KITCHENETTE"
	AmenityThermal         = "THERMAL"
)

// AmenityKeys is the closed list in a stable order; the contract's enum is generated from
// the same set and a test keeps the two in step.
var AmenityKeys = []string{
	AmenityWifi, AmenityParking, AmenityBreakfast, AmenityHalfBoard, AmenityFullBoard,
	AmenityAllInclusive, AmenityPool, AmenitySpa, AmenityGym, AmenityAirConditioning,
	AmenityRestaurant, AmenityBeach, AmenityStepFreeAccess, AmenityPetFriendly,
	AmenityFamilyRoom, AmenityShuttle, AmenityLaundry, AmenityMeetingRoom,
	AmenityKitchenette, AmenityThermal,
}

var amenitySet = func() map[string]bool {
	out := make(map[string]bool, len(AmenityKeys))
	for _, key := range AmenityKeys {
		out[key] = true
	}
	return out
}()

// KnownAmenity reports whether the key is in the catalogue.
func KnownAmenity(key string) bool { return amenitySet[key] }

// NormaliseAmenities refuses an unknown key and returns the set in the catalogue's order
// with duplicates dropped, so two providers who listed the same three amenities in two
// orders store the same array and a reader can compare them.
func NormaliseAmenities(field string, keys []string, ve *ValidationError) []string {
	seen := make(map[string]bool, len(keys))
	for i, key := range keys {
		if !amenitySet[key] {
			ve.Add(fmt.Sprintf("%s[%d]", field, i), "ENUM", "tanınmayan olanak kodu")
			continue
		}
		seen[key] = true
	}
	out := make([]string, 0, len(seen))
	for _, key := range AmenityKeys {
		if seen[key] {
			out = append(out, key)
		}
	}
	return out
}

// InList reports whether value is one of allowed.
func InList(value string, allowed []string) bool {
	for _, a := range allowed {
		if a == value {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Nights
// ---------------------------------------------------------------------------

// ErrCheckOutNotAfterCheckIn is a stay of no nights. It is its own error because
// `checkOut == checkIn` is the mistake a date picker makes, not a nonsense request, and the
// answer has to say which of the two dates to move.
var ErrCheckOutNotAfterCheckIn = errors.New("accommodation: check-out must be after check-in")

// Day is a civil date carried as midnight UTC. Every date in this package is one: a date
// is a position on a calendar and not an instant, and the moment a date becomes an instant
// somebody's stay moves by a day when the clocks change.
func Day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// DayIn is the civil date an instant falls on in a given zone, as a Day. It is how an
// instant enters this package at all: a property's night belongs to the calendar hanging
// on the wall of that building, so 2026-03-29T23:30 in Europe/Istanbul is the 29th and the
// same instant in Europe/Berlin is the 29th too, but 2026-03-29T22:30Z is the 30th in
// Istanbul and the 29th in Berlin, and only the property's own zone settles it.
func DayIn(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return Day(t.In(loc))
}

// Nights counts the nights of the half-open stay [checkIn, checkOut).
//
// It counts calendar days and never elapsed hours. That is the whole of the function and
// the whole of the reason it exists: on the night a zone leaves or enters summer time the
// wall clock advances by 23 or 25 hours, and any implementation that divided a duration by
// twenty-four would answer a stay over the last Sunday of March one night short and a stay
// over the last Sunday of October one night long. The arithmetic is done on midnight-UTC
// civil dates, where a day is always exactly a day.
func Nights(checkIn, checkOut time.Time) (int, error) {
	in, out := Day(checkIn), Day(checkOut)
	if !out.After(in) {
		return 0, ErrCheckOutNotAfterCheckIn
	}
	nights := 0
	for day := in; day.Before(out); day = day.AddDate(0, 0, 1) {
		nights++
	}
	return nights, nil
}

// StayDates lists the nights of the stay, one civil date per night, in order. The last
// element is the night before check-out: a guest checking out on the 5th slept on the 4th
// and not on the 5th, and an inventory row taken for the 5th is a room nobody was in.
func StayDates(checkIn time.Time, nights int) []time.Time {
	if nights <= 0 {
		return nil
	}
	out := make([]time.Time, 0, nights)
	day := Day(checkIn)
	for i := 0; i < nights; i++ {
		out = append(out, day)
		day = day.AddDate(0, 0, 1)
	}
	return out
}

// LastNight is the civil date of the final night of the stay, which is the day before
// check-out. Every range read in this vertical is bounded by it rather than by check-out,
// because an allotment on the check-out day belongs to the next guest.
func LastNight(checkOut time.Time) time.Time {
	return Day(checkOut).AddDate(0, 0, -1)
}

// NightStart is the instant a night begins in the property's own zone. It is not used by
// the availability arithmetic — that is deliberately civil — and exists for the hold and
// check-in clocks of WP-I6-02 and I6-03, and for the tests that prove a night is still one
// night when it is twenty-three hours long.
func NightStart(day time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := day.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// LoadLocation resolves a property's IANA zone. An unknown zone is an error rather than a
// silent fall back to UTC: a property whose zone the server cannot resolve would have its
// nights counted against the wrong calendar, and nothing on any screen would say so.
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		return nil, fmt.Errorf("accommodation: property has no timezone")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("accommodation: unknown timezone %q: %w", name, err)
	}
	return loc, nil
}

// ValidTimezone reports whether the name is a zone this server can resolve. It is what the
// write path checks before a property is stored, so a bad zone is a 422 on the field that
// carried it rather than a failure months later when somebody searches.
func ValidTimezone(name string) bool {
	_, err := LoadLocation(name)
	return err == nil
}
