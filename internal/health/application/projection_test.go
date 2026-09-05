package application

import (
	"errors"
	"testing"

	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

func contextWith(permissions ...string) identity.RequestContext {
	rc := identity.RequestContext{Permissions: map[string]struct{}{}}
	for _, p := range permissions {
		rc.Permissions[p] = struct{}{}
	}
	return rc
}

// TestDecideIsTheWholeVisibilityRule pins every branch of section 2.2 and 2.3 in one table.
// It is a unit test on purpose: decide is the one function the whole package's guarantee
// rests on, and it should be provable without a database, a request or a row.
func TestDecideIsTheWholeVisibilityRule(t *testing.T) {
	cases := []struct {
		name        string
		permissions []string
		sensitivity string
		purpose     string
		projection  Projection
		refused     bool
		err         error
	}{
		{
			name:        "a sponsor HR user sees the financial projection of an ordinary case",
			permissions: []string{PermissionCaseRead},
			sensitivity: domain.SensitivityStandard,
			projection:  ProjectionFinancial,
		},
		{
			name: "and of a sensitive one, with nothing to say it was sensitive",
			// No refusal is recorded: the caller holds no clinical grant at all, so it was
			// not refused a sensitive record, it was never in that conversation.
			permissions: []string{PermissionCaseRead},
			sensitivity: domain.SensitivitySensitive,
			projection:  ProjectionFinancial,
		},
		{
			name:        "a clinical reader sees everything of an ordinary case, purpose or not",
			permissions: []string{PermissionCaseRead, PermissionClinicalRead},
			sensitivity: domain.SensitivityStandard,
			projection:  ProjectionClinical,
		},
		{
			name:        "a clinical reader without the sensitive grant is narrowed, not refused",
			permissions: []string{PermissionCaseRead, PermissionClinicalRead},
			sensitivity: domain.SensitivitySensitive,
			projection:  ProjectionFinancial,
			refused:     true,
		},
		{
			name:        "a purpose does not substitute for the sensitive grant",
			permissions: []string{PermissionCaseRead, PermissionClinicalRead},
			sensitivity: domain.SensitivitySensitive,
			purpose:     "MEDICAL_REVIEW",
			projection:  ProjectionFinancial,
			refused:     true,
		},
		{
			name:        "the sensitive grant without a purpose is refused",
			permissions: []string{PermissionCaseRead, PermissionClinicalRead, PermissionSensitiveRead},
			sensitivity: domain.SensitivitySensitive,
			projection:  ProjectionFinancial,
			refused:     true,
			err:         ErrAccessPurposeRequired,
		},
		{
			name:        "the sensitive grant with a purpose sees everything",
			permissions: []string{PermissionCaseRead, PermissionClinicalRead, PermissionSensitiveRead},
			sensitivity: domain.SensitivitySensitive,
			purpose:     "MEDICAL_REVIEW",
			projection:  ProjectionClinical,
		},
		{
			name: "the sensitive grant alone grants nothing: clinical read is the floor",
			// A grant that widened a caller who cannot read clinical detail at all would be
			// a way round the first rule using the second.
			permissions: []string{PermissionCaseRead, PermissionSensitiveRead},
			sensitivity: domain.SensitivitySensitive,
			purpose:     "MEDICAL_REVIEW",
			projection:  ProjectionFinancial,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decide(contextWith(tc.permissions...), tc.sensitivity,
				AccessRequest{PurposeCode: tc.purpose})
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if tc.err == nil && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if got.projection != tc.projection {
				t.Fatalf("projection = %s, want %s", got.projection, tc.projection)
			}
			if got.refusedSensitive != tc.refused {
				t.Fatalf("refusedSensitive = %t, want %t", got.refusedSensitive, tc.refused)
			}
		})
	}
}

// TestProjectionClearsEveryClinicalField is the other half of the same guarantee: whatever
// decide answered, the financial projection has to leave nothing clinical on the record. It
// is written as "the record no longer carries it" rather than "the mapper hid it", because a
// record that no longer carries the answer cannot leak it however it is serialised later.
func TestProjectionClearsEveryClinicalField(t *testing.T) {
	branch, notes := "PSK", "Hasta uyku düzeninden şikayetçi."
	record := CaseRecord{Sensitivity: domain.SensitivitySensitive}
	encounters := []EncounterRecord{{BranchCode: &branch, NotesClinical: &notes}}

	clinicalCase := projectCase(record, ProjectionClinical)
	if clinicalCase.Sensitivity != domain.SensitivitySensitive {
		t.Fatal("the clinical projection dropped the sensitivity; a guard that refuses everybody is an outage")
	}
	if got := projectEncounters(encounters, ProjectionClinical); got[0].BranchCode == nil || got[0].NotesClinical == nil {
		t.Fatal("the clinical projection dropped a clinical field")
	}

	financialCase := projectCase(record, ProjectionFinancial)
	if financialCase.Sensitivity != "" {
		t.Fatalf("the financial projection kept sensitivity = %q", financialCase.Sensitivity)
	}
	financial := projectEncounters(encounters, ProjectionFinancial)
	if financial[0].BranchCode != nil {
		t.Fatalf("the financial projection kept branchCode = %q", *financial[0].BranchCode)
	}
	if financial[0].NotesClinical != nil {
		t.Fatal("the financial projection kept the clinical notes")
	}
	// And the caller's own rows were not mutated on the way past: the same record can be
	// projected twice, which is what a list of one case read by two people amounts to.
	if encounters[0].BranchCode == nil || encounters[0].NotesClinical == nil {
		t.Fatal("projecting mutated the record it was given")
	}
}
