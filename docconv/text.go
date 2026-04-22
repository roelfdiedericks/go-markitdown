package docconv

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// extractText handles FormatText — just sanitises to UTF-8 and normalises
// line endings. The body is returned as-is (not wrapped in a code fence)
// because callers typically want the raw text for LLM context.
func extractText(data []byte) (string, Metadata, error) {
	text := sanitizeUTF8(data)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	meta := Metadata{Format: "text"}
	if text == "" {
		return "", meta, ErrNoText
	}
	return text, meta, nil
}

// sanitizeUTF8 replaces invalid UTF-8 byte sequences with the Unicode
// replacement character. Preserves valid UTF-8 bytes including multi-byte
// runes.
func sanitizeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var buf bytes.Buffer
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			buf.WriteRune('\uFFFD')
			i++
			continue
		}
		buf.WriteRune(r)
		i += size
	}
	return buf.String()
}
