// Package memberimport turns a sponsor's member file into staged rows, validates and
// matches them against the live registry, parks the ambiguous ones for a human and
// applies the accepted ones in idempotent, transactional chunks (v1.2 section 10.1,
// WP-I2-05).
//
// Nothing in this package writes a plaintext identifier anywhere it can be read back: a
// staged row keeps only the tenant-salted blind index, the mask and one tenant-encrypted
// envelope, and the uploaded file itself is never persisted.
package memberimport

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Format is the only file format of this increment.
const Format = "CSV_V1"

// Limits of one upload (WP-I2-05 section 2.2).
const (
	MaxFileBytes = 20 << 20
	MaxRows      = 50_000
	// MaxParseErrors bounds the report of a malformed file; a file with more problems
	// than this is rejected on the first errors rather than echoed back in full.
	MaxParseErrors = 20
)

// Column names of CSV_V1. The header must carry exactly this set, in any order.
const (
	ColSourceRecordID    = "source_record_id"
	ColFirstName         = "first_name"
	ColMiddleName        = "middle_name"
	ColLastName          = "last_name"
	ColBirthDate         = "birth_date"
	ColSexAtBirth        = "sex_at_birth"
	ColTCKN              = "tckn"
	ColMemberNo          = "member_no"
	ColEmployeeNo        = "employee_no"
	ColMembershipType    = "membership_type"
	ColPrincipalMemberNo = "principal_member_no"
	ColRelationship      = "relationship"
	ColValidFrom         = "valid_from"
	ColValidTo           = "valid_to"
	ColPlanCode          = "plan_code"
)

// Columns is the exact column set of CSV_V1 in canonical order.
var Columns = []string{
	ColSourceRecordID, ColFirstName, ColMiddleName, ColLastName, ColBirthDate, ColSexAtBirth,
	ColTCKN, ColMemberNo, ColEmployeeNo, ColMembershipType, ColPrincipalMemberNo,
	ColRelationship, ColValidFrom, ColValidTo, ColPlanCode,
}

// Delimiters CSV_V1 accepts; the parser detects which one the header uses.
const (
	delimiterSemicolon = ';'
	delimiterComma     = ','
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// ErrFileTooLarge is returned before any parsing when the upload exceeds MaxFileBytes.
var ErrFileTooLarge = errors.New("memberimport: file exceeds the size limit")

// ParseError names one problem of the file. Line is the 1-based physical line the
// problem was found on, so an operator can open the file and go straight to it. Column is
// empty for problems that belong to the whole line.
type ParseError struct {
	Line    int
	Column  string
	Code    string
	Message string
}

// Field is the problem+json field path of one parse error.
func (e ParseError) Field() string {
	if e.Column == "" {
		return fmt.Sprintf("file.line[%d]", e.Line)
	}
	return fmt.Sprintf("file.line[%d].%s", e.Line, e.Column)
}

// ParseErrors is the aggregate returned when a file cannot be parsed at all. Rows that
// parse but fail business rules are not reported here: they become INVALID staging rows.
type ParseErrors struct {
	Errors []ParseError
}

func (e *ParseErrors) Error() string {
	if len(e.Errors) == 0 {
		return "memberimport: file cannot be parsed"
	}
	return fmt.Sprintf("memberimport: %d parse error(s), first at line %d: %s",
		len(e.Errors), e.Errors[0].Line, e.Errors[0].Code)
}

// ErrParse lets callers detect a *ParseErrors with errors.Is.
var ErrParse = errors.New("memberimport: file cannot be parsed")

// Is makes errors.Is(err, ErrParse) match.
func (e *ParseErrors) Is(target error) bool { return target == ErrParse }

// add appends one error and reports whether there is room for another.
func (e *ParseErrors) add(line int, column, code, message string) bool {
	e.Errors = append(e.Errors, ParseError{Line: line, Column: column, Code: code, Message: message})
	return len(e.Errors) < MaxParseErrors
}

// Parse-error codes, stable across releases.
const (
	CodeEncoding       = "FILE_NOT_UTF8"
	CodeEmpty          = "FILE_EMPTY"
	CodeHeader         = "HEADER_INVALID"
	CodeUnknownColumn  = "COLUMN_UNKNOWN"
	CodeMissingColumn  = "COLUMN_MISSING"
	CodeDuplicateCol   = "COLUMN_DUPLICATED"
	CodeFieldCount     = "FIELD_COUNT"
	CodeMalformed      = "LINE_MALFORMED"
	CodeTooManyRows    = "ROW_LIMIT_EXCEEDED"
	CodeDuplicateRowID = "SOURCE_RECORD_ID_DUPLICATED"
)

// Record is one data row of the file, still as text: parsing only proves the file's
// shape, the meaning of each value is checked by validation.
type Record struct {
	// RowNo is the 1-based position among data rows; it is the staging row_no.
	RowNo int
	// Line is the 1-based physical line, which differs from RowNo as soon as a quoted
	// field spans lines.
	Line int

	SourceRecordID    string
	FirstName         string
	MiddleName        string
	LastName          string
	BirthDate         string
	SexAtBirth        string
	TCKN              string
	MemberNo          string
	EmployeeNo        string
	MembershipType    string
	PrincipalMemberNo string
	Relationship      string
	ValidFrom         string
	ValidTo           string
	PlanCode          string
}

// ParseCSV reads a CSV_V1 file: UTF-8 with an optional byte order mark, one header row,
// semicolon or comma detected from the header, RFC 4180 quoting. The column set must be
// exactly Columns; unknown, missing and repeated columns are rejected. Every error
// carries the line it was found on.
func ParseCSV(data []byte) ([]Record, error) {
	if len(data) > MaxFileBytes {
		return nil, ErrFileTooLarge
	}
	data = bytes.TrimPrefix(data, bom)
	pe := &ParseErrors{}
	if !utf8.Valid(data) {
		pe.add(1, "", CodeEncoding, "dosya UTF-8 olmalı")
		return nil, pe
	}
	if len(bytes.TrimSpace(data)) == 0 {
		pe.add(1, "", CodeEmpty, "dosya boş")
		return nil, pe
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = detectDelimiter(data)
	reader.FieldsPerRecord = -1 // checked against the header below, with our own message
	reader.LazyQuotes = false
	reader.ReuseRecord = true

	header, err := reader.Read()
	if err != nil {
		pe.add(lineOf(err, 1), "", CodeHeader, "başlık satırı okunamadı")
		return nil, pe
	}
	index, err := headerIndex(header, pe)
	if err != nil {
		return nil, err
	}

	records := make([]Record, 0, 256)
	seen := make(map[string]int, 256)
	for {
		fields, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			pe.add(lineOf(err, len(records)+2), "", CodeMalformed, "satır ayrıştırılamadı")
			// A quoting error leaves the reader in the middle of the file; there is no
			// safe position to resume from, so the whole upload is rejected.
			return nil, pe
		}
		line, _ := reader.FieldPos(0)
		if len(fields) != len(header) {
			if !pe.add(line, "", CodeFieldCount,
				fmt.Sprintf("%d sütun bekleniyor, %d bulundu", len(header), len(fields))) {
				return nil, pe
			}
			continue
		}
		if len(records) >= MaxRows {
			pe.add(line, "", CodeTooManyRows, fmt.Sprintf("en fazla %d satır yüklenebilir", MaxRows))
			return nil, pe
		}
		rec := recordFrom(fields, index)
		rec.RowNo = len(records) + 1
		rec.Line = line
		if rec.SourceRecordID != "" {
			if first, dup := seen[rec.SourceRecordID]; dup {
				if !pe.add(line, ColSourceRecordID, CodeDuplicateRowID,
					fmt.Sprintf("bu kayıt numarası %d. satırda da var", first)) {
					return nil, pe
				}
				continue
			}
			seen[rec.SourceRecordID] = line
		}
		records = append(records, rec)
	}
	if len(pe.Errors) > 0 {
		return nil, pe
	}
	if len(records) == 0 {
		pe.add(1, "", CodeEmpty, "dosyada veri satırı yok")
		return nil, pe
	}
	return records, nil
}

// headerIndex maps every canonical column to its position in the header.
func headerIndex(header []string, pe *ParseErrors) (map[string]int, error) {
	known := make(map[string]bool, len(Columns))
	for _, c := range Columns {
		known[c] = true
	}
	index := make(map[string]int, len(Columns))
	for i, raw := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, string(bom))))
		_, repeated := index[name]
		switch {
		case name == "":
			pe.add(1, "", CodeHeader, fmt.Sprintf("%d. sütun başlığı boş", i+1))
		case !known[name]:
			pe.add(1, name, CodeUnknownColumn, "tanınmayan sütun")
		case repeated:
			pe.add(1, name, CodeDuplicateCol, "sütun birden fazla verilmiş")
		default:
			index[name] = i
		}
	}
	for _, c := range Columns {
		if _, ok := index[c]; !ok {
			pe.add(1, c, CodeMissingColumn, "zorunlu sütun eksik")
		}
	}
	if len(pe.Errors) > 0 {
		return nil, pe
	}
	return index, nil
}

func recordFrom(fields []string, index map[string]int) Record {
	at := func(col string) string { return strings.TrimSpace(fields[index[col]]) }
	return Record{
		SourceRecordID:    at(ColSourceRecordID),
		FirstName:         at(ColFirstName),
		MiddleName:        at(ColMiddleName),
		LastName:          at(ColLastName),
		BirthDate:         at(ColBirthDate),
		SexAtBirth:        strings.ToUpper(at(ColSexAtBirth)),
		TCKN:              at(ColTCKN),
		MemberNo:          at(ColMemberNo),
		EmployeeNo:        at(ColEmployeeNo),
		MembershipType:    strings.ToUpper(at(ColMembershipType)),
		PrincipalMemberNo: at(ColPrincipalMemberNo),
		Relationship:      strings.ToUpper(at(ColRelationship)),
		ValidFrom:         at(ColValidFrom),
		ValidTo:           at(ColValidTo),
		PlanCode:          at(ColPlanCode),
	}
}

// detectDelimiter counts unquoted separators on the header line. A file that uses
// neither is read as comma separated and fails on the column set instead.
func detectDelimiter(data []byte) rune {
	line := data
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		line = data[:i]
	}
	semicolons, commas, inQuotes := 0, 0, false
	for _, b := range line {
		switch {
		case b == '"':
			inQuotes = !inQuotes
		case inQuotes:
		case b == delimiterSemicolon:
			semicolons++
		case b == delimiterComma:
			commas++
		}
	}
	if semicolons > commas {
		return delimiterSemicolon
	}
	return delimiterComma
}

// lineOf extracts the line number csv reports, falling back to a caller estimate.
func lineOf(err error, fallback int) int {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) && parseErr.Line > 0 {
		return parseErr.Line
	}
	return fallback
}
