// Package mdconv wraps html-to-markdown/v2 with the option set this project
// prefers. Centralised here so every backend produces markdown in the same
// house style.
package mdconv

import (
	"regexp"
	"strings"
	"sync"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"

	"golang.org/x/text/unicode/norm"
)

// convOnce lazily builds the shared converter. html-to-markdown/v2 converters
// are safe for concurrent use once constructed.
var (
	convOnce sync.Once
	conv     *converter.Converter
)

// getConverter returns the shared converter configured with the plugin set
// we ship: base + commonmark + strikethrough + table. The table plugin is
// critical — without it html-to-markdown collapses <table> structure into
// inline text, which breaks our DOCX/PPTX table output.
func getConverter() *converter.Converter {
	convOnce.Do(func() {
		conv = converter.NewConverter(
			converter.WithPlugins(
				base.NewBasePlugin(),
				commonmark.NewCommonmarkPlugin(),
				strikethrough.NewStrikethroughPlugin(),
				table.NewTablePlugin(
					table.WithSpanCellBehavior(table.SpanBehaviorMirror),
				),
			),
		)
	})
	return conv
}

// Convert turns an HTML string into markdown. Returns the trimmed result;
// empty on pure-whitespace input.
func Convert(html string) (string, error) {
	if strings.TrimSpace(html) == "" {
		return "", nil
	}
	md, err := getConverter().ConvertString(html)
	if err != nil {
		return "", err
	}
	return tidy(md), nil
}

// tidy normalises the html-to-markdown output: trims, collapses runs of 3+
// blank lines into 2, replaces html-to-markdown's list-boundary comments
// with a single blank line, and normalises line endings.
//
// html-to-markdown/v2's commonmark plugin emits "<!--THE END-->" markers
// between adjacent lists so that markdown renderers keep them visually
// separate rather than merging into one long list. We want the separation
// but not the comment text; collapsing to a blank line preserves the
// semantic boundary without leaking implementation detail.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = listBoundaryCommentPattern.ReplaceAllString(s, "\n")
	s = multiBlankLinePattern.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	// Normalize to NFC as the final step. Without this, strings that
	// look identical on screen (e.g. "café" composed vs decomposed) hash
	// differently, break exact-match and deduplication, and split
	// tokens across embeddings. All downstream consumers of our markdown
	// can safely assume Unicode Normalization Form C.
	if !norm.NFC.IsNormalString(s) {
		s = norm.NFC.String(s)
	}
	return s
}

var (
	multiBlankLinePattern      = regexp.MustCompile(`\n{3,}`)
	listBoundaryCommentPattern = regexp.MustCompile(`(?m)^[[:space:]]*<!--THE END-->[[:space:]]*\n?`)
)
