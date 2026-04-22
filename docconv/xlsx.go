package docconv

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

// wideRowThreshold is the column count past which the row is rendered as a
// CSV code block instead of a markdown table. Wide markdown tables become
// unreadable for humans and eat a lot of tokens.
const wideRowThreshold = 12

// extractXLSX renders each sheet as a section with a markdown H2 heading and
// either a markdown table or a CSV code block. Empty trailing rows and
// columns are trimmed so we don't emit megabytes of blanks for sheets with
// stray formatting.
func extractXLSX(_ context.Context, data []byte, _ Options) (string, Metadata, error) {
	meta := Metadata{Format: "xlsx"}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", meta, wrapBackendError(FormatXLSX, err)
	}
	defer f.Close()

	// Pull common metadata if present.
	if props, perr := f.GetDocProps(); perr == nil && props != nil {
		meta.Title = strings.TrimSpace(props.Title)
		meta.Author = strings.TrimSpace(props.Creator)
		meta.Subject = strings.TrimSpace(props.Subject)
		meta.Keywords = strings.TrimSpace(props.Keywords)
		if props.Created != "" {
			meta.Created = props.Created
		}
		if props.Modified != "" {
			meta.Modified = props.Modified
		}
	}

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return "", meta, ErrNoText
	}

	var b strings.Builder
	nonEmpty := false
	for _, sheet := range sheets {
		rows, rerr := f.GetRows(sheet)
		if rerr != nil {
			return "", meta, wrapBackendError(FormatXLSX, fmt.Errorf("read sheet %q: %w", sheet, rerr))
		}
		rows = trimEmptyRowsAndCols(rows)
		if len(rows) == 0 {
			continue
		}
		nonEmpty = true

		b.WriteString("## ")
		b.WriteString(escapeHeading(sheet))
		b.WriteString("\n\n")

		if tableFits(rows) {
			b.WriteString(renderMarkdownTable(rows))
		} else {
			b.WriteString(renderCSVBlock(rows))
		}
		b.WriteString("\n")
	}

	if !nonEmpty {
		return "", meta, ErrNoText
	}

	return strings.TrimRight(b.String(), "\n"), meta, nil
}

// trimEmptyRowsAndCols removes trailing all-empty rows and columns. Excelize
// sometimes reports rows that extend past the last real data when formatting
// touched them. We do not trim leading blank rows in case they are part of
// the layout.
func trimEmptyRowsAndCols(rows [][]string) [][]string {
	for len(rows) > 0 && rowIsEmpty(rows[len(rows)-1]) {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 {
		return rows
	}
	maxCol := 0
	for _, row := range rows {
		for i := len(row) - 1; i >= 0; i-- {
			if strings.TrimSpace(row[i]) != "" {
				if i+1 > maxCol {
					maxCol = i + 1
				}
				break
			}
		}
	}
	if maxCol == 0 {
		return nil
	}
	for i, row := range rows {
		if len(row) > maxCol {
			rows[i] = row[:maxCol]
		} else if len(row) < maxCol {
			padded := make([]string, maxCol)
			copy(padded, row)
			rows[i] = padded
		}
	}
	return rows
}

func rowIsEmpty(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// tableFits returns true when rows are narrow enough, and none contains
// newlines, to render cleanly as a markdown table.
func tableFits(rows [][]string) bool {
	if len(rows) == 0 {
		return false
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
		for _, c := range r {
			if strings.ContainsAny(c, "\n\r") {
				return false
			}
		}
	}
	return cols > 0 && cols <= wideRowThreshold
}

// renderMarkdownTable writes a standard pipe-delimited table. The first row
// is used as the header.
func renderMarkdownTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}

	var b strings.Builder
	header := rows[0]
	writeRow(&b, header, cols)
	// Divider.
	b.WriteByte('|')
	for i := 0; i < cols; i++ {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	for _, row := range rows[1:] {
		writeRow(&b, row, cols)
	}
	return b.String()
}

func writeRow(b *strings.Builder, row []string, cols int) {
	b.WriteByte('|')
	for i := 0; i < cols; i++ {
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		b.WriteByte(' ')
		b.WriteString(escapeCell(cell))
		b.WriteString(" |")
	}
	b.WriteByte('\n')
}

// escapeCell escapes pipe and newline characters for markdown table safety.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// renderCSVBlock wraps rows in a fenced csv code block — chosen for wide
// sheets or sheets with multi-line cells because it survives those cleanly.
func renderCSVBlock(rows [][]string) string {
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	for _, r := range rows {
		_ = w.Write(r)
	}
	w.Flush()

	var b strings.Builder
	b.WriteString("```csv\n")
	b.Write(bytes.TrimRight(out.Bytes(), "\n"))
	b.WriteString("\n```\n")
	return b.String()
}

// escapeHeading keeps sheet names from breaking markdown headings. Newlines
// and leading/trailing whitespace are collapsed.
func escapeHeading(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
