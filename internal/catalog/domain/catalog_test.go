package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/catalog/domain"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func dayPtr(s string) *time.Time {
	t := day(s)
	return &t
}

// fieldCodes collects the field/code pairs of a validation error for assertions.
func fieldCodes(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	out := make(map[string]string, len(ve.Fields))
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func TestValidateCategoryPlacementRefusesCycles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		subject   string
		ancestors []string
	}{
		{"direct parent is the subject", "a", []string{"a"}},
		{"grandparent is the subject", "a", []string{"b", "a"}},
		{"three levels up is the subject", "a", []string{"d", "c", "b", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := domain.ValidateCategoryPlacement(tc.subject, tc.ancestors, 1)
			if !errors.Is(err, domain.ErrCategoryCycle) {
				t.Fatalf("expected ErrCategoryCycle, got %v", err)
			}
		})
	}
}

func TestValidateCategoryPlacementAllowsSiblingBranch(t *testing.T) {
	t.Parallel()
	// The subject is not on the chain from the proposed parent to the root, so the move
	// is a re-parent inside the tree rather than a loop.
	if err := domain.ValidateCategoryPlacement("a", []string{"b", "c"}, 1); err != nil {
		t.Fatalf("expected the placement to be allowed, got %v", err)
	}
}

func TestValidateCategoryPlacementDepthCap(t *testing.T) {
	t.Parallel()
	// MaxCategoryDepth is 6, the root counting as level one.
	deepest := []string{"l5", "l4", "l3", "l2", "l1"} // parent sits at level five
	if err := domain.ValidateCategoryPlacement("", deepest, 1); err != nil {
		t.Fatalf("a leaf at level six must be allowed, got %v", err)
	}
	if err := domain.ValidateCategoryPlacement("", append([]string{"l6"}, deepest...), 1); err == nil {
		t.Fatal("a leaf at level seven must be refused")
	} else if got := fieldCodes(t, err)["parentId"]; got != "DEPTH_EXCEEDED" {
		t.Fatalf("parentId code = %q, want DEPTH_EXCEEDED", got)
	}

	// Re-parenting carries the whole subtree, so its height counts towards the cap.
	if err := domain.ValidateCategoryPlacement("a", []string{"l4", "l3", "l2", "l1"}, 2); err != nil {
		t.Fatalf("a two-level subtree under level four must be allowed, got %v", err)
	}
	if err := domain.ValidateCategoryPlacement("a", []string{"l4", "l3", "l2", "l1"}, 3); err == nil {
		t.Fatal("a three-level subtree under level four must be refused")
	} else if got := fieldCodes(t, err)["parentId"]; got != "DEPTH_EXCEEDED" {
		t.Fatalf("parentId code = %q, want DEPTH_EXCEEDED", got)
	}
}

func TestPeriodsOverlap(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		aFrom, bFrom string
		aTo, bTo     *time.Time
		want         bool
	}{
		{name: "identical open periods", aFrom: "2026-01-01", bFrom: "2026-01-01", want: true},
		{name: "adjacent half-open periods do not touch", aFrom: "2026-01-01", aTo: dayPtr("2026-06-01"),
			bFrom: "2026-06-01", want: false},
		{name: "one day of overlap", aFrom: "2026-01-01", aTo: dayPtr("2026-06-02"),
			bFrom: "2026-06-01", bTo: dayPtr("2026-12-01"), want: true},
		{name: "disjoint closed periods", aFrom: "2024-01-01", aTo: dayPtr("2025-01-01"),
			bFrom: "2026-01-01", bTo: dayPtr("2027-01-01"), want: false},
		{name: "open period swallows a later closed one", aFrom: "2020-01-01",
			bFrom: "2026-01-01", bTo: dayPtr("2026-02-01"), want: true},
		{name: "closed period ends before an open one starts", aFrom: "2020-01-01", aTo: dayPtr("2021-01-01"),
			bFrom: "2026-01-01", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := domain.PeriodsOverlap(day(tc.aFrom), tc.aTo, day(tc.bFrom), tc.bTo)
			if got != tc.want {
				t.Fatalf("PeriodsOverlap = %t, want %t", got, tc.want)
			}
			// The relation is symmetric, exactly like the && operator of daterange.
			if back := domain.PeriodsOverlap(day(tc.bFrom), tc.bTo, day(tc.aFrom), tc.aTo); back != tc.want {
				t.Fatalf("PeriodsOverlap reversed = %t, want %t", back, tc.want)
			}
		})
	}
}

func TestValidateMappingSetOverlap(t *testing.T) {
	t.Parallel()
	const sut = "0f1b2c3d-0000-0000-0000-000000000001"
	const icd = "0f1b2c3d-0000-0000-0000-000000000002"

	t.Run("same system and code over overlapping periods", func(t *testing.T) {
		t.Parallel()
		err := domain.ValidateMappingSet([]domain.MappingInput{
			{CodeSystemID: sut, Code: "P1", ValidFrom: day("2026-01-01")},
			{CodeSystemID: sut, Code: "P1", ValidFrom: day("2026-06-01")},
		})
		if !errors.Is(err, domain.ErrMappingOverlap) {
			t.Fatalf("expected ErrMappingOverlap, got %v", err)
		}
	})

	t.Run("two primaries in one system over overlapping periods", func(t *testing.T) {
		t.Parallel()
		err := domain.ValidateMappingSet([]domain.MappingInput{
			{CodeSystemID: sut, Code: "P1", ValidFrom: day("2026-01-01"), Primary: true},
			{CodeSystemID: sut, Code: "P2", ValidFrom: day("2026-01-01"), Primary: true},
		})
		if !errors.Is(err, domain.ErrMappingOverlap) {
			t.Fatalf("expected ErrMappingOverlap, got %v", err)
		}
	})

	t.Run("successive periods and a second system are fine", func(t *testing.T) {
		t.Parallel()
		err := domain.ValidateMappingSet([]domain.MappingInput{
			{CodeSystemID: sut, Code: "P1", ValidFrom: day("2025-01-01"), ValidTo: dayPtr("2026-01-01"), Primary: true},
			{CodeSystemID: sut, Code: "P1", ValidFrom: day("2026-01-01"), Primary: true},
			{CodeSystemID: icd, Code: "M54.5", ValidFrom: day("2026-01-01"), Primary: true},
		})
		if err != nil {
			t.Fatalf("expected the set to be accepted, got %v", err)
		}
	})

	t.Run("a period that ends before it starts is a field error", func(t *testing.T) {
		t.Parallel()
		err := domain.ValidateMappingSet([]domain.MappingInput{
			{CodeSystemID: sut, Code: "P1", ValidFrom: day("2026-06-01"), ValidTo: dayPtr("2026-01-01")},
		})
		if got := fieldCodes(t, err)["items[0].validTo"]; got != "PERIOD" {
			t.Fatalf("items[0].validTo code = %q, want PERIOD", got)
		}
	})
}

func TestValidateImportBatch(t *testing.T) {
	t.Parallel()
	row := func(code, from string) domain.CodeValueInput {
		return domain.CodeValueInput{Code: code, Display: code + " display", ValidFrom: day(from), Active: true}
	}

	t.Run("empty batch", func(t *testing.T) {
		t.Parallel()
		if got := fieldCodes(t, domain.ValidateImportBatch(nil))["items"]; got != "REQUIRED" {
			t.Fatalf("items code = %q, want REQUIRED", got)
		}
	})

	t.Run("batch at the cap is accepted", func(t *testing.T) {
		t.Parallel()
		items := make([]domain.CodeValueInput, 0, domain.MaxImportRows)
		for i := range domain.MaxImportRows {
			items = append(items, row("C"+itoa(i), "2026-01-01"))
		}
		if err := domain.ValidateImportBatch(items); err != nil {
			t.Fatalf("a batch of %d rows must be accepted, got %v", domain.MaxImportRows, err)
		}
	})

	t.Run("one row over the cap is refused", func(t *testing.T) {
		t.Parallel()
		items := make([]domain.CodeValueInput, 0, domain.MaxImportRows+1)
		for i := range domain.MaxImportRows + 1 {
			items = append(items, row("C"+itoa(i), "2026-01-01"))
		}
		if got := fieldCodes(t, domain.ValidateImportBatch(items))["items"]; got != "MAX_ITEMS" {
			t.Fatalf("items code = %q, want MAX_ITEMS", got)
		}
	})

	t.Run("a duplicate key inside the batch names the first row", func(t *testing.T) {
		t.Parallel()
		err := domain.ValidateImportBatch([]domain.CodeValueInput{
			row("P1", "2026-01-01"), row("P2", "2026-01-01"), row("P1", "2026-01-01"),
		})
		if got := fieldCodes(t, err)["items[2].code"]; got != "DUPLICATE" {
			t.Fatalf("items[2].code = %q, want DUPLICATE", got)
		}
	})

	t.Run("the same code with a different start is a new edition, not a duplicate", func(t *testing.T) {
		t.Parallel()
		err := domain.ValidateImportBatch([]domain.CodeValueInput{
			row("P1", "2025-01-01"), row("P1", "2026-01-01"),
		})
		if err != nil {
			t.Fatalf("expected the batch to be accepted, got %v", err)
		}
	})

	t.Run("one bad row rejects the whole batch", func(t *testing.T) {
		t.Parallel()
		bad := row("P2", "2026-01-01")
		bad.Display = ""
		err := domain.ValidateImportBatch([]domain.CodeValueInput{row("P1", "2026-01-01"), bad})
		if got := fieldCodes(t, err)["items[1].display"]; got != "LENGTH" {
			t.Fatalf("items[1].display = %q, want LENGTH", got)
		}
	})
}

func TestValidateNewCategory(t *testing.T) {
	t.Parallel()
	ok := domain.NewCategory{Code: "PHYSIO", Name: "Fizyoterapi", Domain: "HEALTH", Active: true}
	if err := domain.ValidateNewCategory(ok); err != nil {
		t.Fatalf("expected the command to be accepted, got %v", err)
	}

	bad := domain.NewCategory{Code: "physio", Name: "F", Domain: "MEDICAL"}
	codes := fieldCodes(t, domain.ValidateNewCategory(bad))
	for field, want := range map[string]string{"code": "FORMAT", "name": "LENGTH", "domain": "ENUM"} {
		if codes[field] != want {
			t.Errorf("%s code = %q, want %q", field, codes[field], want)
		}
	}
}

func TestValidateNewCodeSystem(t *testing.T) {
	t.Parallel()
	ok := domain.NewCodeSystem{
		Code: "SUT", Name: "Sağlık Uygulama Tebliği", Version: "2026", Authority: "SGK",
		ValidFrom: day("2026-01-01"),
	}
	if err := domain.ValidateNewCodeSystem(ok); err != nil {
		t.Fatalf("expected the command to be accepted, got %v", err)
	}

	bad := ok
	bad.Code = "sut"
	bad.Version = ""
	bad.Authority = "TSE"
	bad.ValidTo = dayPtr("2025-01-01")
	codes := fieldCodes(t, domain.ValidateNewCodeSystem(bad))
	for field, want := range map[string]string{
		"code": "FORMAT", "version": "LENGTH", "authority": "ENUM", "validTo": "PERIOD",
	} {
		if codes[field] != want {
			t.Errorf("%s code = %q, want %q", field, codes[field], want)
		}
	}
}

func TestLikePatternEscapesWildcards(t *testing.T) {
	t.Parallel()
	if got := domain.LikePattern(""); got != "" {
		t.Fatalf("an empty term must mean no filter, got %q", got)
	}
	if got := domain.LikePattern(" 50%_off "); got != `%50\%\_off%` {
		t.Fatalf("LikePattern = %q", got)
	}
}

// itoa keeps the table-driven tests free of a strconv import.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
