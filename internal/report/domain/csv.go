package domain

import (
	"bytes"
	"encoding/csv"
	"strings"
)

// The byte order mark. It is written because the people who open these files open them in
// Excel, and Excel reads a UTF-8 CSV without one as Latin-1 — which turns every Turkish name in
// the file into mojibake. Three bytes are cheaper than a support call.
const utf8BOM = "\ufeff"

// WatermarkColumn is the header of the column every row carries. It is first rather than last
// so that a row copied out of a spreadsheet takes the stamp with it.
const WatermarkColumn = "Filigran"

// RenderCSV writes one export file: the byte order mark, the watermark on a header line of its
// own, the column names, and then every row with the watermark in front of it.
//
// **The watermark is on every row.** Not once at the top, where a person who copies the middle
// of the file loses it, and not in a footer nobody scrolls to. That is what the requirement in
// Faz 8 means, and it is why this function takes the watermark rather than reading it from a
// row: there is one stamp per file and it is on all of it.
//
// Values that begin with `=`, `+`, `-` or `@` are prefixed with an apostrophe. A CSV cell
// starting with one of those is a formula in every spreadsheet program there is, and an export
// that carried a provider's name into somebody's Excel as a formula would be this platform
// handing out code execution in a file the recipient trusts.
func RenderCSV(watermark string, columns []string, rows [][]string) []byte {
	var buf bytes.Buffer
	buf.WriteString(utf8BOM)
	w := csv.NewWriter(&buf)
	// The sheet header: the stamp on its own line, so a printed page carries it whatever the
	// column widths do.
	_ = w.Write([]string{escapeCell(watermark)})
	header := make([]string, 0, len(columns)+1)
	header = append(header, WatermarkColumn)
	for _, c := range columns {
		header = append(header, escapeCell(c))
	}
	_ = w.Write(header)
	for _, row := range rows {
		line := make([]string, 0, len(row)+1)
		line = append(line, escapeCell(watermark))
		for _, cell := range row {
			line = append(line, escapeCell(cell))
		}
		_ = w.Write(line)
	}
	w.Flush()
	return buf.Bytes()
}

// escapeCell defuses a cell a spreadsheet would otherwise execute.
func escapeCell(value string) string {
	if value == "" {
		return value
	}
	if strings.ContainsAny(value[:1], "=+-@\t\r") {
		return "'" + value
	}
	return value
}
