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

// xlsxImageContextBudget bounds the number of characters of sheet text we
// hand to the describer as ContextBefore. We want enough of the sheet
// header/tail to orient the model without spamming it with a 10k-row
// report.
const xlsxImageContextBudget = 600

// extractXLSX renders each sheet as a section with a markdown H2 heading,
// a markdown table (or CSV code block), followed by any embedded images
// that are anchored on that sheet. Images are surfaced as placeholder
// tags so the shared post-pass can deduplicate, describe, and — when
// ImageDir is set — persist them to disk.
//
// Empty trailing rows and columns are trimmed so we don't emit megabytes
// of blanks for sheets with stray formatting.
func extractXLSX(ctx context.Context, data []byte, opts Options) (string, Metadata, error) {
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

	var (
		b         strings.Builder
		nonEmpty  bool
		allImages = map[string]extractedImage{}
		imageIdx  int
	)

	for _, sheet := range sheets {
		rows, rerr := f.GetRows(sheet)
		if rerr != nil {
			return "", meta, wrapBackendError(FormatXLSX, fmt.Errorf("read sheet %q: %w", sheet, rerr))
		}
		rows = trimEmptyRowsAndCols(rows)

		// A sheet with no rows but with embedded pictures is still
		// worth emitting (e.g. a dashboard sheet that is entirely
		// chart imagery). We only skip the sheet entirely when we
		// have neither text nor images.
		cells, cellsErr := f.GetPictureCells(sheet)
		if cellsErr != nil {
			// Picture probe failures are not fatal. Fall through
			// with an empty list so at least the text renders.
			cells = nil
		}
		if len(rows) == 0 && len(cells) == 0 {
			continue
		}
		nonEmpty = true

		b.WriteString("## ")
		b.WriteString(escapeHeading(sheet))
		b.WriteString("\n\n")

		if len(rows) > 0 {
			if tableFits(rows) {
				b.WriteString(renderMarkdownTable(rows))
			} else {
				b.WriteString(renderCSVBlock(rows))
			}
			b.WriteString("\n")
		}

		// Sheet text forms the image ContextBefore blob. We do not
		// inline the full CSV (that would blow the token budget on
		// large sheets); instead we take a trimmed flatten of the
		// first rows so the describer knows the sheet subject.
		sheetText := flattenSheetText(rows, xlsxImageContextBudget)

		for _, cell := range cells {
			pics, perr := f.GetPictures(sheet, cell)
			if perr != nil || len(pics) == 0 {
				continue
			}
			for _, pic := range pics {
				id, registered := registerXLSXImage(pic, sheet, cell, sheetText, allImages, &imageIdx)
				if !registered {
					continue
				}
				b.WriteString("\n")
				b.WriteString(imagePlaceholderMarkdown(id))
				b.WriteString("\n")
			}
		}
	}

	if !nonEmpty {
		return "", meta, ErrNoText
	}

	md := strings.TrimRight(b.String(), "\n")

	md, err = replaceImagePlaceholders(ctx, md, allImages, opts)
	if err != nil {
		return "", meta, wrapBackendError(FormatXLSX, err)
	}

	return md, meta, nil
}

// imagePlaceholderMarkdown emits the markdown form of the rid: image
// placeholder directly — used by backends (xlsx, fitz appendix) that
// already hand the shared post-pass fully-rendered markdown rather than
// HTML that first travels through mdconv.
func imagePlaceholderMarkdown(id string) string {
	return fmt.Sprintf("![](rid:%s)", id)
}

// registerXLSXImage deduplicates an excelize Picture by content hash and
// registers a new extractedImage keyed by "xlsx-<shorthash>" on first
// sight. Returns the chosen id plus a boolean flag — false when the image
// bytes could not be hashed or the entry already existed (the caller
// should not emit a new placeholder in that case; excelize returns the
// same picture on each anchor, so deduping here is mandatory).
func registerXLSXImage(pic excelize.Picture, sheet, anchor, sheetText string, images map[string]extractedImage, next *int) (string, bool) {
	if len(pic.File) == 0 {
		return "", false
	}
	id := "xlsx-" + shortHash(pic.File)
	if _, exists := images[id]; exists {
		return id, false
	}
	ext := strings.ToLower(strings.TrimSpace(pic.Extension))
	if ext == "" {
		ext = ".bin"
	}
	mime := xlsxMimeForExt(ext)
	alt := ""
	if pic.Format != nil {
		alt = strings.TrimSpace(pic.Format.AltText)
	}
	context := strings.TrimSpace(fmt.Sprintf("Sheet %q, anchor %s. %s", sheet, anchor, sheetText))
	images[id] = extractedImage{
		ID:            id,
		Index:         *next,
		Data:          pic.File,
		MimeType:      mime,
		Extension:     ext,
		ContextBefore: context,
		AuthorAltText: alt,
	}
	*next++
	return id, true
}

// xlsxMimeForExt maps a (dotted, lower-case) extension to a MIME type.
// The list intentionally mirrors the other OOXML backends so the
// describer sees consistent content types.
func xlsxMimeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".webp":
		return "image/webp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	case ".emf":
		return "image/x-emf"
	case ".wmf":
		return "image/x-wmf"
	}
	return "application/octet-stream"
}

// flattenSheetText produces a compact, whitespace-collapsed preview of
// the sheet contents, capped at budget runes. Rows are walked in order
// and cells joined with " | " so the result still reads as tabular data
// to the LLM.
func flattenSheetText(rows [][]string, budget int) string {
	var b strings.Builder
	for _, row := range rows {
		cells := make([]string, 0, len(row))
		for _, c := range row {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			cells = append(cells, c)
		}
		if len(cells) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(strings.Join(cells, " | "))
		if b.Len() >= budget {
			break
		}
	}
	s := collapseWhitespaceRunes(b.String())
	if len([]rune(s)) > budget {
		runes := []rune(s)
		s = string(runes[:budget]) + "…"
	}
	return s
}

// collapseWhitespaceRunes collapses runs of whitespace to a single space
// so flattened sheet text reads cleanly in the describer prompt.
func collapseWhitespaceRunes(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
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
