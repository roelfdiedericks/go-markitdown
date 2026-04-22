package docconv

import (
	"bytes"
	"context"
	"strings"

	readability "codeberg.org/readeck/go-readability/v2"

	"github.com/roelfdiedericks/go-markitdown/docconv/internal/mdconv"
)

// extractHTML runs the HTML payload through go-readability to strip chrome
// (nav, ads, footers), then html-to-markdown to produce clean markdown.
//
// Readability may reject very short or unusual documents. In that case we
// fall back to converting the raw HTML directly.
func extractHTML(_ context.Context, data []byte, _ Options) (string, Metadata, error) {
	meta := Metadata{Format: "html"}

	article, err := readability.FromReader(bytes.NewReader(data), nil)
	if err == nil && article.Node != nil {
		if t := strings.TrimSpace(article.Title()); t != "" {
			meta.Title = t
		}
		if b := strings.TrimSpace(article.Byline()); b != "" {
			meta.Author = b
		}
		if p, perr := article.PublishedTime(); perr == nil {
			meta.Created = p.UTC().Format("2006-01-02")
		}
		// Any other PublishedTime error (including ErrTimestampMissing)
		// is treated as "no published date available" and skipped.

		var buf bytes.Buffer
		if rerr := article.RenderHTML(&buf); rerr == nil {
			md, cerr := mdconv.Convert(buf.String())
			if cerr == nil && strings.TrimSpace(md) != "" {
				if err := ensureNotEmpty(md); err != nil {
					return "", meta, err
				}
				return md, meta, nil
			}
		}
	}

	// Fallback: raw HTML directly through the converter.
	md, err := mdconv.Convert(string(data))
	if err != nil {
		return "", meta, wrapBackendError(FormatHTML, err)
	}
	if e := ensureNotEmpty(md); e != nil {
		return "", meta, e
	}
	return md, meta, nil
}
