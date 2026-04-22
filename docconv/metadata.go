package docconv

import (
	"fmt"
	"sort"
	"strings"
)

// Metadata is what backends report alongside the extracted markdown. Fields
// not populated by the backend are left blank and suppressed in the rendered
// YAML front-matter.
type Metadata struct {
	Title    string
	Author   string
	Subject  string
	Keywords string
	Created  string
	Modified string
	Pages    int
	Format   string
	Extra    map[string]string
}

// renderMetadataFrontMatter emits a YAML front-matter block for the non-zero
// fields of m. Returns an empty string when m has nothing to report. The
// output always ends with a trailing blank line so it can be concatenated
// directly with the markdown body.
func renderMetadataFrontMatter(m Metadata) string {
	fields := [][2]string{}
	appendIf := func(key, val string) {
		val = strings.TrimSpace(val)
		if val != "" {
			fields = append(fields, [2]string{key, val})
		}
	}

	appendIf("title", m.Title)
	appendIf("author", m.Author)
	appendIf("subject", m.Subject)
	appendIf("keywords", m.Keywords)
	appendIf("created", m.Created)
	appendIf("modified", m.Modified)
	if m.Pages > 0 {
		fields = append(fields, [2]string{"pages", fmt.Sprintf("%d", m.Pages)})
	}
	appendIf("format", m.Format)

	if m.Extra != nil {
		extraKeys := make([]string, 0, len(m.Extra))
		for k := range m.Extra {
			extraKeys = append(extraKeys, k)
		}
		sort.Strings(extraKeys)
		for _, k := range extraKeys {
			appendIf(k, m.Extra[k])
		}
	}

	if len(fields) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("---\n")
	for _, kv := range fields {
		b.WriteString(kv[0])
		b.WriteString(": ")
		b.WriteString(yamlScalar(kv[1]))
		b.WriteByte('\n')
	}
	b.WriteString("---\n\n")
	return b.String()
}

// yamlScalar quotes a string for safe YAML emission. Keeps plain scalars for
// simple cases and double-quotes anything with special characters.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	needsQuote := false
	for _, c := range s {
		if c == ':' || c == '#' || c == '"' || c == '\'' || c == '\n' || c == '\r' || c == '\t' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		trimmed := strings.TrimSpace(s)
		if trimmed != s || trimmed == "" {
			needsQuote = true
		}
		// Reserved YAML values.
		switch strings.ToLower(trimmed) {
		case "true", "false", "yes", "no", "null", "~":
			needsQuote = true
		}
	}
	if !needsQuote {
		return s
	}
	// Double-quote with JSON-style escaping for newlines and quotes.
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
