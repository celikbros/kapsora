package domain

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The resource types the audit rows of this package carry.
const (
	AggregateStay          = "inpatient_stay"
	AggregateStayExtension = "stay_extension"
)

// Stay statuses (migration 000033).
const (
	StayRequested  = "REQUESTED"
	StayAuthorized = "AUTHORIZED"
	StayAdmitted   = "ADMITTED"
	StayDischarged = "DISCHARGED"
	StayCancelled  = "CANCELLED"
	StayRejected   = "REJECTED"
)

// Extension statuses (migration 000033).
const (
	ExtensionRequested = "REQUESTED"
	ExtensionApproved  = "APPROVED"
	ExtensionRejected  = "REJECTED"
	ExtensionCancelled = "CANCELLED"
)

// Segment types (migration 000033). COMPANION is the one outside the exclusion constraint:
// the relative sleeping in the room overlaps the patient's own segment by definition.
const (
	SegmentWard        = "WARD"
	SegmentICU         = "ICU"
	SegmentSurgery     = "SURGERY"
	SegmentObservation = "OBSERVATION"
	SegmentCompanion   = "COMPANION"
)

// Commands. Each is a transition with its own precondition; the command is what the
// application service asks StayTarget about before it writes anything.
const (
	StayCommandExtend    = "EXTEND"
	StayCommandSegments  = "PUT_SEGMENTS"
	StayCommandDischarge = "DISCHARGE"
	StayCommandCancel    = "CANCEL"
	// StayCommandAuthorize and StayCommandReject are the two the outbox subscriber gives.
	// They have no endpoint: the reviewer decides the request, and the stay follows.
	StayCommandAuthorize = "AUTHORIZE"
	StayCommandReject    = "REJECT"
)

// Closed lists the database repeats as CHECK constraints.
var (
	StayStatuses = []string{
		StayRequested, StayAuthorized, StayAdmitted, StayDischarged, StayCancelled, StayRejected,
	}
	ExtensionStatuses = []string{
		ExtensionRequested, ExtensionApproved, ExtensionRejected, ExtensionCancelled,
	}
	SegmentTypes = []string{
		SegmentWard, SegmentICU, SegmentSurgery, SegmentObservation, SegmentCompanion,
	}
	// OpenStayStatuses are the three the partial unique index calls open. They are named
	// once so "one open stay per case and provider", "the case cannot be closed" and "the
	// list filter" cannot drift into three different definitions of open.
	OpenStayStatuses = []string{StayRequested, StayAuthorized, StayAdmitted}
)

// stayTransitions is the whole lifecycle: which command may be given in which status and
// where it lands. A command missing from a status is a command that status does not have,
// which is what makes "you cannot discharge a cancelled stay" one lookup rather than a
// condition repeated in six places.
var stayTransitions = map[string]map[string]string{
	StayCommandAuthorize: {StayRequested: StayAuthorized},
	StayCommandReject:    {StayRequested: StayRejected},
	// An extension is asked for on a decided, live admission. REQUESTED is deliberately
	// absent: extending something nobody has approved yet is asking for more of nothing.
	StayCommandExtend: {StayAuthorized: StayAuthorized, StayAdmitted: StayAdmitted},
	// Recording where the patient actually is *is* the admission, so the first segment set
	// on an authorized stay moves it to ADMITTED. There is no separate admit command: a
	// second way to say the same thing is a second thing to keep in step.
	StayCommandSegments:  {StayAuthorized: StayAdmitted, StayAdmitted: StayAdmitted},
	StayCommandDischarge: {StayAuthorized: StayDischarged, StayAdmitted: StayDischarged},
	StayCommandCancel: {
		StayRequested: StayCancelled, StayAuthorized: StayCancelled, StayAdmitted: StayCancelled,
	},
}

// StayTarget answers where a command leads from a status, and whether it is allowed at all.
func StayTarget(command, status string) (string, bool) {
	to, ok := stayTransitions[command][status]
	return to, ok
}

// StayFinished reports whether a stay has stopped moving. A discharged, cancelled or
// refused stay is a record rather than a thing anybody may still change.
func StayFinished(status string) bool {
	return status == StayDischarged || status == StayCancelled || status == StayRejected
}

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxEstimatedDays bounds an admission estimate and an extension. It is a year, which
	// is longer than any admission anybody should be preauthorizing in one go and short
	// enough that a fat finger on the keypad is refused rather than reserving a decade.
	MaxStayDays = 365
	// MaxSegments bounds one segment set replacement; the OpenAPI schema repeats it.
	MaxSegments = 200
	// MaxSegmentCode bounds a room or bed label.
	MaxSegmentCode = 32
	// MaxExtensionReasonText bounds the free-text half of an extension reason. It is
	// clinical text, so it never leaves the clinical projection.
	MaxExtensionReasonText = 1000
	// DefaultBackdateDays and DefaultFutureDays are the admission window a tenant that has
	// configured neither gets. Three days back covers a weekend admission entered on the
	// Monday; thirty days forward covers a planned operation.
	DefaultBackdateDays = 3
	DefaultFutureDays   = 30
	// MaxWindowDays caps what a tenant may configure. A setting of a hundred thousand is a
	// typo rather than a policy, and the window it would open is "any date at all".
	MaxWindowDays = 3650
)

var (
	segmentCodePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._/-]{0,31}$`)
	stayReasonCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{1,79}$`)
)

// NewStay is the create command.
type NewStay struct {
	CaseType    string
	AdmissionAt time.Time
	// EstimatedDays is what the provider believes the admission will take. It becomes the
	// requested quantity of the preauthorization and the length of the authorization's
	// validity, so a zero here would be a promise of nothing.
	EstimatedDays int
	// Now, BackdateDays and FutureDays are the window the admission date is checked
	// against. They come from tenant configuration rather than from a constant, because
	// what counts as "too long ago to still be entering" is a tenant's own answer.
	Now           time.Time
	BackdateDays  int
	FutureDays    int
	SegmentsCount int
}

// ValidateNewStay checks a create command apart from the window, which needs its own error:
// a date outside the window is a 422 with a code of its own, because the caller has to be
// told which end of the window it fell off rather than being told the field is malformed.
func ValidateNewStay(in NewStay) error {
	ve := &ValidationError{}
	if in.AdmissionAt.IsZero() {
		ve.Add("admissionAt", "REQUIRED", "yatış zamanı zorunlu")
	}
	if in.EstimatedDays < 1 || in.EstimatedDays > MaxStayDays {
		ve.Add("estimatedDays", "RANGE",
			fmt.Sprintf("tahmini yatış süresi 1 ile %d gün arasında olmalı", MaxStayDays))
	}
	return ve.OrNil()
}

// AdmissionInWindow reports whether an admission date falls inside the tenant's backdating
// and future-dating window. The comparison is on whole days rather than on the instant: a
// stay admitted at 23:00 three days ago and one admitted at 01:00 three days ago are both
// "three days ago" to the person entering them.
func AdmissionInWindow(admissionAt, now time.Time, backdateDays, futureDays int) bool {
	day := 24 * time.Hour
	earliest := now.UTC().Truncate(day).Add(-time.Duration(backdateDays) * day)
	// The window is inclusive at both ends and the future end is the end of that day, so
	// "thirty days forward" means the whole of the thirtieth day.
	latest := now.UTC().Truncate(day).Add(time.Duration(futureDays+1) * day)
	return !admissionAt.UTC().Before(earliest) && admissionAt.UTC().Before(latest)
}

// NewStayExtension is the extend command.
type NewStayExtension struct {
	AdditionalDays int
	ReasonCode     string
	ReasonText     *string
}

// ValidateStayExtension checks an extension request.
func ValidateStayExtension(in NewStayExtension) error {
	ve := &ValidationError{}
	if in.AdditionalDays < 1 || in.AdditionalDays > MaxStayDays {
		ve.Add("additionalDays", "RANGE",
			fmt.Sprintf("ek gün sayısı 1 ile %d arasında olmalı", MaxStayDays))
	}
	if !stayReasonCodePattern.MatchString(strings.TrimSpace(in.ReasonCode)) {
		ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve _.:- ; 2-80 karakter")
	}
	if in.ReasonText != nil && utf8.RuneCountInString(*in.ReasonText) > MaxExtensionReasonText {
		ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter")
	}
	return ve.OrNil()
}

// ValidateStayReason checks the reason a cancellation carries.
func ValidateStayReason(reasonCode string, reasonText *string) error {
	ve := &ValidationError{}
	if !stayReasonCodePattern.MatchString(strings.TrimSpace(reasonCode)) {
		ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve _.:- ; 2-80 karakter")
	}
	if reasonText != nil && utf8.RuneCountInString(*reasonText) > MaxExtensionReasonText {
		ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter")
	}
	return ve.OrNil()
}

// SegmentInput is one line of a segment set replacement.
type SegmentInput struct {
	SegmentType string
	StartsAt    time.Time
	EndsAt      *time.Time
	RoomCode    *string
	BedCode     *string
}

// ValidateSegments checks a whole segment set, including the rule the exclusion constraint
// also enforces: two non-companion segments may not claim the same hours. Both halves exist
// on purpose — the caller is told which line overlaps which, and the constraint refuses it
// whatever writes it, including a statement this validation never saw.
//
// A companion is skipped by the overlap check here for the same reason it sits outside the
// constraint: the relative sleeping in the room is in the room while the patient is.
func ValidateSegments(items []SegmentInput, admissionAt time.Time, dischargeAt *time.Time) error {
	ve := &ValidationError{}
	if len(items) > MaxSegments {
		ve.Add("items", "RANGE", fmt.Sprintf("en fazla %d segment gönderilebilir", MaxSegments))
		return ve.OrNil()
	}
	for i, item := range items {
		path := fmt.Sprintf("items[%d]", i)
		requireOneOf(ve, path+".segmentType", item.SegmentType, SegmentTypes)
		if item.StartsAt.IsZero() {
			ve.Add(path+".startsAt", "REQUIRED", "başlangıç zamanı zorunlu")
			continue
		}
		if item.EndsAt != nil && !item.EndsAt.After(item.StartsAt) {
			ve.Add(path+".endsAt", "RANGE", "bitiş zamanı başlangıçtan sonra olmalı")
		}
		if item.StartsAt.Before(admissionAt) {
			ve.Add(path+".startsAt", "RANGE", "segment yatış zamanından önce başlayamaz")
		}
		if dischargeAt != nil && item.EndsAt != nil && item.EndsAt.After(*dischargeAt) {
			ve.Add(path+".endsAt", "RANGE", "segment taburcu zamanından sonra bitemez")
		}
		if item.RoomCode != nil && !segmentCodePattern.MatchString(strings.TrimSpace(*item.RoomCode)) {
			ve.Add(path+".roomCode", "FORMAT", "harf, rakam ve boşluk; en fazla 32 karakter")
		}
		if item.BedCode != nil && !segmentCodePattern.MatchString(strings.TrimSpace(*item.BedCode)) {
			ve.Add(path+".bedCode", "FORMAT", "harf, rakam ve boşluk; en fazla 32 karakter")
		}
	}
	validateSegmentOverlaps(ve, items)
	return ve.OrNil()
}

// validateSegmentOverlaps is the half-open comparison the exclusion constraint makes,
// written out so the caller learns which two lines collided rather than reading a
// constraint name off a 409.
func validateSegmentOverlaps(ve *ValidationError, items []SegmentInput) {
	for i, a := range items {
		if a.SegmentType == SegmentCompanion || a.StartsAt.IsZero() {
			continue
		}
		for j := 0; j < i; j++ {
			b := items[j]
			if b.SegmentType == SegmentCompanion || b.StartsAt.IsZero() {
				continue
			}
			if overlaps(a, b) {
				ve.Add(fmt.Sprintf("items[%d].startsAt", i), "OVERLAP",
					fmt.Sprintf("bu segment items[%d] ile çakışıyor", j))
				break
			}
		}
	}
}

// overlaps compares two half-open ranges; a nil end is an open-ended segment, so two of them
// always overlap.
func overlaps(a, b SegmentInput) bool {
	if a.EndsAt != nil && !a.EndsAt.After(b.StartsAt) {
		return false
	}
	if b.EndsAt != nil && !b.EndsAt.After(a.StartsAt) {
		return false
	}
	return true
}

// ActualDays is the day count a discharge is reconciled on: ceil((discharge − admission) /
// one day), never less than one. A person admitted at nine in the morning and sent home at
// five in the afternoon stayed a day, not a third of one, because a day is the unit the
// admission was authorized in.
func ActualDays(admissionAt, dischargeAt time.Time) int {
	elapsed := dischargeAt.Sub(admissionAt)
	if elapsed <= 0 {
		return 1
	}
	days := int(math.Ceil(elapsed.Hours() / 24))
	if days < 1 {
		return 1
	}
	return days
}

// ValidateDischarge checks the moment a discharge names.
func ValidateDischarge(dischargeAt, admissionAt, now time.Time) error {
	ve := &ValidationError{}
	switch {
	case dischargeAt.IsZero():
		ve.Add("dischargeAt", "REQUIRED", "taburcu zamanı zorunlu")
	case dischargeAt.Before(admissionAt):
		ve.Add("dischargeAt", "RANGE", "taburcu zamanı yatıştan önce olamaz")
	case dischargeAt.After(now):
		ve.Add("dischargeAt", "RANGE", "taburcu zamanı gelecekte olamaz")
	}
	return ve.OrNil()
}

// ValidateStayStatusFilter checks the list filter's status against the closed list, so an
// unknown value is a field error rather than a silently empty page.
func ValidateStayStatusFilter(status string) error {
	if status == "" {
		return nil
	}
	ve := &ValidationError{}
	requireOneOf(ve, "status", status, StayStatuses)
	return ve.OrNil()
}
