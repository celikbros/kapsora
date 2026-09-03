package domain

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// Closed enumerations from migration 000003.
var (
	SexAtBirthValues     = []string{"FEMALE", "MALE", "INTERSEX", "UNKNOWN"}
	PersonStatusUpdates  = []string{"ACTIVE", "INACTIVE", "DECEASED"} // MERGED is set by the merge command
	PersonStatuses       = []string{"ACTIVE", "INACTIVE", "DECEASED", "MERGED"}
	RelationshipStatuses = []string{"ACTIVE", "SUSPENDED", "ENDED"}
	MembershipCreate     = []string{"PENDING", "ACTIVE"}
	MembershipUpdates    = []string{"ACTIVE", "SUSPENDED", "ENDED"}
	SponsorRoles         = []string{"SPONSOR", "PAYER"}
)

// Person status values.
const (
	PersonActive = "ACTIVE"
	PersonMerged = "MERGED"
)

// Name length limits mirror the OpenAPI contract.
const (
	MinNameLength = 1
	MaxNameLength = 100
	MaxQueryRunes = 120
)

// EarliestBirthDate rejects obviously wrong data entry; nothing older is accepted.
var EarliestBirthDate = time.Date(1900, time.January, 1, 0, 0, 0, 0, time.UTC)

// SubmittedIdentifier is one identifier as it arrives on a create or patch, before the
// catalog is consulted. Remove is only meaningful on a patch.
type SubmittedIdentifier struct {
	Type    string
	Value   string
	Primary bool
	Remove  bool
}

// NewPerson is a create command; the fields are normalised in place by ValidateNew.
type NewPerson struct {
	FirstName   string
	MiddleName  string
	LastName    string
	BirthDate   *time.Time
	SexAtBirth  string
	Identifiers []SubmittedIdentifier
}

// PersonPatch is a merge-patch; nil pointers mean "unchanged" and the Clear flags carry
// an explicit JSON null.
type PersonPatch struct {
	FirstName       *string
	MiddleName      *string
	ClearMiddleName bool
	LastName        *string
	BirthDate       *time.Time
	ClearBirthDate  bool
	SexAtBirth      *string
	ClearSexAtBirth bool
	Status          *string
	Identifiers     []SubmittedIdentifier
	ExpectedVersion int64
}

// ValidateNew normalises and validates a create command in place, reporting every problem
// at once so a form can be corrected in one round trip.
func ValidateNew(n *NewPerson, today time.Time) error {
	ve := &ValidationError{}
	n.FirstName = CleanName(n.FirstName)
	n.MiddleName = CleanName(n.MiddleName)
	n.LastName = CleanName(n.LastName)
	checkName(ve, "firstName", n.FirstName, true)
	checkName(ve, "middleName", n.MiddleName, false)
	checkName(ve, "lastName", n.LastName, true)
	checkBirthDate(ve, n.BirthDate, today)
	if n.SexAtBirth != "" && !contains(SexAtBirthValues, n.SexAtBirth) {
		ve.Add("sexAtBirth", "ENUM", "geçersiz cinsiyet değeri")
	}
	validateIdentifiers(ve, n.Identifiers, false)
	return ve.OrNil()
}

// ValidatePatch normalises and validates a merge-patch in place.
func ValidatePatch(p *PersonPatch, today time.Time) error {
	ve := &ValidationError{}
	if p.FirstName != nil {
		*p.FirstName = CleanName(*p.FirstName)
		checkName(ve, "firstName", *p.FirstName, true)
	}
	if p.MiddleName != nil {
		*p.MiddleName = CleanName(*p.MiddleName)
		checkName(ve, "middleName", *p.MiddleName, false)
	}
	if p.LastName != nil {
		*p.LastName = CleanName(*p.LastName)
		checkName(ve, "lastName", *p.LastName, true)
	}
	checkBirthDate(ve, p.BirthDate, today)
	if p.SexAtBirth != nil && !contains(SexAtBirthValues, *p.SexAtBirth) {
		ve.Add("sexAtBirth", "ENUM", "geçersiz cinsiyet değeri")
	}
	if p.Status != nil && !contains(PersonStatusUpdates, *p.Status) {
		ve.Add("status", "ENUM", "ACTIVE, INACTIVE veya DECEASED olmalı")
	}
	validateIdentifiers(ve, p.Identifiers, true)
	return ve.OrNil()
}

// validateIdentifiers normalises every submitted value and checks its shape. Catalog
// membership, uniqueness scope and the ACTIVE flag are checked by the application, which
// can read party.identifier_type.
func validateIdentifiers(ve *ValidationError, ids []SubmittedIdentifier, allowRemove bool) {
	if len(ids) > MaxIdentifiers {
		ve.Add("identifiers", "LENGTH", fmt.Sprintf("en fazla %d tanımlayıcı verilebilir", MaxIdentifiers))
		return
	}
	seen := map[string]bool{}
	primaries := map[string]int{}
	for i := range ids {
		id := &ids[i]
		field := fmt.Sprintf("identifiers[%d]", i)
		if !ValidTypeCode(id.Type) {
			ve.Add(field+".type", CodeIdentifierTypeUnknown, "geçersiz tanımlayıcı türü")
			continue
		}
		if seen[id.Type] {
			ve.Add(field+".type", CodeIdentifierDuplicated, "aynı tür birden fazla verilemedi")
			continue
		}
		seen[id.Type] = true
		if id.Remove {
			if !allowRemove {
				ve.Add(field+".remove", "UNSUPPORTED", "oluşturmada tanımlayıcı silinemez")
			}
			continue
		}
		id.Value = NormalizeIdentifier(id.Value)
		if id.Value == "" {
			ve.Add(field+".value", CodeIdentifierInvalid, "tanımlayıcı boş olamaz")
			continue
		}
		if err := ValidateIdentifier(id.Type, id.Value); err != nil {
			ve.Add(field+".value", CodeIdentifierInvalid, "tanımlayıcı doğrulanamadı")
			continue
		}
		if id.Primary {
			primaries[id.Type]++
			if primaries[id.Type] > 1 {
				ve.Add(field+".primary", CodeIdentifierDuplicated, "her tür için tek birincil tanımlayıcı olabilir")
			}
		}
	}
}

// ValidatePeriod checks a validity period used by relationships and memberships.
func ValidatePeriod(field string, from time.Time, to *time.Time) error {
	ve := &ValidationError{}
	if from.IsZero() {
		ve.Add(field+"From", "REQUIRED", "başlangıç tarihi zorunlu")
	}
	if to != nil && !to.After(from) {
		ve.Add(field+"To", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
	}
	return ve.OrNil()
}

func checkName(ve *ValidationError, field, value string, required bool) {
	l := utf8.RuneCountInString(value)
	if l == 0 {
		if required {
			ve.Add(field, "REQUIRED", "zorunlu alan")
		}
		return
	}
	if l < MinNameLength || l > MaxNameLength {
		ve.Add(field, "LENGTH", fmt.Sprintf("%d-%d karakter olmalı", MinNameLength, MaxNameLength))
	}
}

func checkBirthDate(ve *ValidationError, d *time.Time, today time.Time) {
	if d == nil {
		return
	}
	switch {
	case d.After(today):
		ve.Add("birthDate", "RANGE", "doğum tarihi gelecekte olamaz")
	case d.Before(EarliestBirthDate):
		ve.Add("birthDate", "RANGE", "doğum tarihi 1900-01-01 sonrası olmalı")
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Contains is the exported form used by the application for catalog enums.
func Contains(list []string, v string) bool { return contains(list, v) }
