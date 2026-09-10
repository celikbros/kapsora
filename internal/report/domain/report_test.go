package domain_test

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/report/domain"
)

var testMoment = time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)

// The watermark is what makes a leaked file traceable, so it has to name all four things and it
// has to fit the column. A stamp that named only the tenant would be a stamp that says which
// company the file came from and not who took it.
func TestWatermarkNamesTenantPersonMomentAndExport(t *testing.T) {
	mark := domain.Watermark("DEMO-A", "Ada Kaya", testMoment,
		"0195f2a0-0000-7000-8000-000000000001")
	for _, want := range []string{
		"DEMO-A", "Ada Kaya", "2026-03-20T09:00:00Z", "0195f2a0-0000-7000-8000-000000000001",
	} {
		if !strings.Contains(mark, want) {
			t.Errorf("watermark %q does not name %q", mark, want)
		}
	}
	if len(mark) < domain.MinWatermark || len(mark) > domain.MaxWatermark {
		t.Errorf("watermark length %d is outside the column's CHECK", len(mark))
	}
}

// A person with no display name still gets a traceable stamp: the export id is what a leak is
// traced by, and an empty name must not be allowed to shorten the mark below the CHECK.
func TestWatermarkSurvivesAMissingName(t *testing.T) {
	mark := domain.Watermark("DEMO-A", "   ", testMoment, "0195f2a0-0000-7000-8000-000000000001")
	if len(mark) < domain.MinWatermark {
		t.Fatalf("watermark %q is shorter than the column allows", mark)
	}
	if !strings.Contains(mark, "0195f2a0-0000-7000-8000-000000000001") {
		t.Errorf("watermark %q does not name the export", mark)
	}
}

// **The watermark is on every row.** This is the property Faz 8 asks for, and the test reads the
// rendered bytes rather than trusting the renderer: every data line, and the column header, and a
// line of its own at the top.
func TestRenderCSVStampsEveryRow(t *testing.T) {
	mark := domain.Watermark("DEMO-A", "Ada Kaya", testMoment, "export-1234")
	body := domain.RenderCSV(mark,
		[]string{"Referans", "Tutar"},
		[][]string{{"ST-202603-AAAA1111", "1000"}, {"ST-202603-BBBB2222", "2000.50"}})

	if !bytes.HasPrefix(body, []byte("\ufeff")) {
		t.Error("the file has no UTF-8 byte order mark; Excel will read it as Latin-1")
	}
	records, err := readCSV(body)
	if err != nil {
		t.Fatalf("read back the rendered file: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("rendered %d lines, want the stamp, the header and two rows", len(records))
	}
	if records[0][0] != mark {
		t.Errorf("sheet header = %q, want the watermark", records[0][0])
	}
	if records[1][0] != domain.WatermarkColumn {
		t.Errorf("first column header = %q, want %q", records[1][0], domain.WatermarkColumn)
	}
	for i, row := range records[2:] {
		if row[0] != mark {
			t.Errorf("data row %d carries %q in the first column, want the watermark", i, row[0])
		}
		if len(row) != 3 {
			t.Errorf("data row %d has %d columns, want the stamp plus two", i, len(row))
		}
	}
}

// A cell a spreadsheet would execute is defused. An export that carried a provider's name into
// somebody's Excel as a formula would be this platform handing out code execution in a file the
// recipient trusts.
func TestRenderCSVDefusesFormulas(t *testing.T) {
	body := domain.RenderCSV("KAPSORA DEMO-A · x · y · z",
		[]string{"Ad"}, [][]string{{"=1+1"}, {"+HYPERLINK(\"x\")"}, {"-2"}, {"@SUM(A1)"}})
	records, err := readCSV(body)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, row := range records[2:] {
		if !strings.HasPrefix(row[1], "'") {
			t.Errorf("cell %q was not defused", row[1])
		}
	}
}

// **The parameters hold no identifier.** Both halves: a uuid anywhere in a value, and a key that
// names an identifier. The database says the same thing; this is the half that turns it into a
// field error instead of a 500.
func TestParametersRefuseIdentifiers(t *testing.T) {
	for name, parameters := range map[string]map[string]any{
		"a uuid value":     {"filter": "0195f2a0-0000-7000-8000-000000000001"},
		"a uuid in prose":  {"note": "see 0195f2a0-0000-7000-8000-000000000001 for detail"},
		"a camel-case key": {"providerId": "x"},
		"a snake-case key": {"claim_id": "x"},
		"a bare id key":    {"id": "x"},
		"a nested uuid":    {"any": []string{"0195f2a0-0000-7000-8000-000000000001"}},
	} {
		if domain.ParametersAreAnonymous(parameters) {
			t.Errorf("%s: parameters were accepted and carry an identifier", name)
		}
		err := domain.ValidateExportRequest(domain.KindSettlements, domain.FormatCSV, false,
			nil, nil, "", parameters)
		if err == nil {
			t.Errorf("%s: createExport accepted a filter carrying an identifier", name)
		}
	}
}

// And the ordinary filters are not refused. A rule that rejected `valid`, `paid` or `overdue`
// because they end in letters that look like an id would be a rule nobody could use.
func TestParametersAcceptOrdinaryFilters(t *testing.T) {
	parameters := map[string]any{
		"status": "APPROVED", "overdue": true, "paid": false, "valid": true,
		"from": "2026-03-01", "currency": "TRY",
	}
	if !domain.ParametersAreAnonymous(parameters) {
		t.Error("ordinary filters were refused as identifiers")
	}
	if err := domain.ValidateExportRequest(domain.KindSettlements, domain.FormatCSV, false,
		nil, nil, "TRY", parameters); err != nil {
		t.Errorf("createExport refused ordinary filters: %v", err)
	}
}

// XLSX is a value of the column and is refused by this release. A caller asking for one is told
// so rather than handed a CSV under another name.
func TestXLSXIsRefusedWithAFieldError(t *testing.T) {
	err := domain.ValidateExportRequest(domain.KindSettlements, domain.FormatXLSX, false,
		nil, nil, "", nil)
	var ve *domain.ValidationError
	if !asValidation(err, &ve) {
		t.Fatalf("XLSX was accepted or refused with the wrong error: %v", err)
	}
	if ve.Fields[0].Field != "format" || ve.Fields[0].Code != "UNSUPPORTED" {
		t.Errorf("field error = %+v, want format/UNSUPPORTED", ve.Fields[0])
	}
}

// A statement export with no provider and no period is refused: the CHECK in the schema says the
// same thing, and a caller told "validation failed" at the constraint would learn nothing.
func TestStatementExportNeedsAProviderAndAPeriod(t *testing.T) {
	if err := domain.ValidateExportRequest(domain.KindProviderStatement, domain.FormatCSV, false,
		nil, nil, "", nil); err == nil {
		t.Fatal("a statement export with no scope was accepted")
	}
	from := testMoment.AddDate(0, -1, 0)
	if err := domain.ValidateExportRequest(domain.KindProviderStatement, domain.FormatCSV, true,
		&from, &testMoment, "TRY", nil); err != nil {
		t.Errorf("a scoped statement export was refused: %v", err)
	}
}

// Exactly one kind needs the sensitive grant, and it is the one whose rows carry prose.
func TestOnlyClaimsIsSensitive(t *testing.T) {
	for _, kind := range domain.Kinds {
		want := kind == domain.KindClaims
		if got := domain.SensitiveKind(kind); got != want {
			t.Errorf("SensitiveKind(%s) = %t, want %t", kind, got, want)
		}
	}
}

// readCSV parses a rendered export. The stamp line has one cell and the rows have many, so the
// reader is told not to insist they match: the shape is the point of the file, not a mistake.
func readCSV(body []byte) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(body, []byte("\ufeff"))))
	reader.FieldsPerRecord = -1
	return reader.ReadAll()
}

func asValidation(err error, target **domain.ValidationError) bool {
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) == 0 {
		return false
	}
	*target = ve
	return true
}
