package memberimport_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/celikbros/kapsora/internal/party/memberimport"
)

// header returns the canonical header line with the given delimiter.
func header(sep string) string {
	return strings.Join(memberimport.Columns, sep)
}

func TestParseCSVAcceptsBothDelimitersAndABOM(t *testing.T) {
	for _, sep := range []string{";", ","} {
		t.Run("delimiter "+sep, func(t *testing.T) {
			row := strings.Join([]string{
				"R1", "Ayşe", "", "Yılmaz", "1980-05-04", "FEMALE", "12345678901", "M-1", "E-1",
				"EMPLOYEE", "", "", "2026-01-01", "", "PLAN1",
			}, sep)
			bom := string([]byte{0xEF, 0xBB, 0xBF})
			file := bom + header(sep) + "\n" + row + "\n"
			records, err := memberimport.ParseCSV([]byte(file))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("records = %d, want 1", len(records))
			}
			got := records[0]
			if got.RowNo != 1 || got.Line != 2 {
				t.Fatalf("row %d line %d, want 1 and 2", got.RowNo, got.Line)
			}
			if got.SourceRecordID != "R1" || got.FirstName != "Ayşe" || got.LastName != "Yılmaz" {
				t.Fatalf("names = %+v", got)
			}
			if got.TCKN != "12345678901" || got.MemberNo != "M-1" || got.PlanCode != "PLAN1" {
				t.Fatalf("values = %+v", got)
			}
		})
	}
}

func TestParseCSVKeepsQuotedFieldsAndCountsPhysicalLines(t *testing.T) {
	// A quoted field spanning two lines: the row number stays 1 while the line moves on,
	// so an error in the next row points at the right place in the operator's editor.
	file := header(";") + "\n" +
		`R1;"Ayşe` + "\n" + `Nur";;Yılmaz;;;;;;;;;;;` + "\n" +
		"R2;Mehmet;;Demir;;;;;;;;;;;\n"
	records, err := memberimport.ParseCSV([]byte(file))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].FirstName != "Ayşe\nNur" {
		t.Fatalf("quoted field = %q", records[0].FirstName)
	}
	if records[1].RowNo != 2 || records[1].Line != 4 {
		t.Fatalf("second row = %d line %d, want 2 and 4", records[1].RowNo, records[1].Line)
	}
}

func TestParseCSVRejectsBadHeaders(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{"empty file", ""},
		{"unknown column", header(";") + ";extra\nR1;;;;;;;;;;;;;;;\n"},
		{"missing column", strings.Join(memberimport.Columns[:len(memberimport.Columns)-1], ";") + "\nR1;;;;;;;;;;;;;;\n"},
		{"repeated column", header(";") + ";tckn\nR1;;;;;;;;;;;;;;;\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := memberimport.ParseCSV([]byte(tc.file))
			if !errors.Is(err, memberimport.ErrParse) {
				t.Fatalf("error = %v, want a parse error", err)
			}
			var pe *memberimport.ParseErrors
			if !errors.As(err, &pe) || len(pe.Errors) == 0 {
				t.Fatalf("no field errors in %v", err)
			}
			if pe.Errors[0].Field() == "" {
				t.Fatal("first error has no field path")
			}
		})
	}
}

func TestParseCSVReportsTheLineOfABadRow(t *testing.T) {
	// A row with too few fields is a structural problem, so it is reported with its line.
	file := header(";") + "\n" +
		"R1;Ayşe;;Yılmaz;;;;;;;;;;;\n" +
		"R2;Mehmet\n"
	_, err := memberimport.ParseCSV([]byte(file))
	var pe *memberimport.ParseErrors
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *ParseErrors", err)
	}
	found := false
	for _, e := range pe.Errors {
		if e.Line == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no error on line 3: %+v", pe.Errors)
	}
}

func TestParseCSVRefusesOversizedInput(t *testing.T) {
	if _, err := memberimport.ParseCSV(make([]byte, memberimport.MaxFileBytes+1)); !errors.Is(err, memberimport.ErrFileTooLarge) {
		t.Fatalf("error = %v, want ErrFileTooLarge", err)
	}

	var b strings.Builder
	b.WriteString(header(";"))
	b.WriteString("\n")
	for i := 0; i <= memberimport.MaxRows; i++ {
		fmt.Fprintf(&b, "R%d;Ad;;Soyad;;;;;;;;;;;\n", i)
	}
	_, err := memberimport.ParseCSV([]byte(b.String()))
	if !errors.Is(err, memberimport.ErrParse) {
		t.Fatalf("error = %v, want a parse error for too many rows", err)
	}
}

func TestParseCSVRejectsInvalidUTF8(t *testing.T) {
	file := append([]byte(header(";")+"\n"), []byte{0xFF, 0xFE, '\n'}...)
	if _, err := memberimport.ParseCSV(file); !errors.Is(err, memberimport.ErrParse) {
		t.Fatalf("error = %v, want a parse error", err)
	}
}
