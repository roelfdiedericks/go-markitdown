package docconv

import (
	"bytes"
	"context"
	"encoding/base64"
	"regexp"
	"strings"

	readability "codeberg.org/readeck/go-readability/v2"

	"github.com/roelfdiedericks/go-markitdown/docconv/internal/mdconv"
)

// extractHTML runs the HTML payload through go-readability to strip chrome
// (nav, ads, footers), then html-to-markdown to produce clean markdown.
// Before markdown conversion it scans the HTML for inline data: URI <img>
// tags, decodes them, and rewrites each to an rid: placeholder the shared
// post-pass can describe, deduplicate, and optionally persist to disk.
//
// Readability may reject very short or unusual documents. In that case we
// fall back to converting the raw HTML directly, still with data URI
// capture so LLM-facing output is consistent.
func extractHTML(ctx context.Context, data []byte, opts Options) (string, Metadata, error) {
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
			html := buf.String()
			images := map[string]extractedImage{}
			htmlCaptured := captureHTMLDataImages(html, images)
			md, cerr := mdconv.Convert(htmlCaptured)
			if cerr == nil && strings.TrimSpace(md) != "" {
				if err := ensureNotEmpty(md); err != nil {
					return "", meta, err
				}
				md, err = replaceImagePlaceholders(ctx, md, images, opts)
				if err != nil {
					return "", meta, wrapBackendError(FormatHTML, err)
				}
				return md, meta, nil
			}
		}
	}

	// Fallback: raw HTML directly through the converter.
	rawImages := map[string]extractedImage{}
	rawHTML := captureHTMLDataImages(string(data), rawImages)
	md, err := mdconv.Convert(rawHTML)
	if err != nil {
		return "", meta, wrapBackendError(FormatHTML, err)
	}
	if e := ensureNotEmpty(md); e != nil {
		return "", meta, e
	}
	md, err = replaceImagePlaceholders(ctx, md, rawImages, opts)
	if err != nil {
		return "", meta, wrapBackendError(FormatHTML, err)
	}
	return md, meta, nil
}

// htmlImageTagPattern matches any <img> tag so the callback can inspect
// the tag's attributes in full — only data: URIs are rewritten; real
// http(s) URLs are left intact so remote images still render as regular
// markdown image links (the library never fetches them).
var htmlImageTagPattern = regexp.MustCompile(`(?is)<img[^>]*>`)

// htmlSrcPattern extracts the src="..." or src='...' value from an <img>
// tag when we've already decided it is an image.
var htmlSrcPattern = regexp.MustCompile(`(?is)\bsrc\s*=\s*["']([^"']*)["']`)

// htmlAltPattern extracts the alt="..." value, mirroring htmlSrcPattern
// semantics. Missing alt yields an empty string.
var htmlAltPattern = regexp.MustCompile(`(?is)\balt\s*=\s*["']([^"']*)["']`)

// htmlDataURIPattern matches a data: URI with required MIME and base64
// payload. Whitespace in the payload is stripped before decoding because
// some HTML sources hard-wrap long base64 strings.
var htmlDataURIPattern = regexp.MustCompile(`(?is)^data:([^;,]+);base64,(.*)$`)

// captureHTMLDataImages scans html for <img src="data:..."> tags, decodes
// each, registers a corresponding extractedImage keyed by "html-<hash>",
// and rewrites the src to "rid:html-<hash>" so mdconv produces a
// placeholder the shared post-pass can turn into a final markdown image
// reference. Real URLs (http/https/relative paths) are left untouched —
// remote fetching is explicitly out of scope for this library.
func captureHTMLDataImages(html string, images map[string]extractedImage) string {
	nextIndex := len(images)
	return htmlImageTagPattern.ReplaceAllStringFunc(html, func(tag string) string {
		srcMatch := htmlSrcPattern.FindStringSubmatch(tag)
		if srcMatch == nil {
			return tag
		}
		src := strings.TrimSpace(srcMatch[1])
		du := htmlDataURIPattern.FindStringSubmatch(src)
		if du == nil {
			// Not a data: URI — leave the tag alone so the
			// downstream converter emits a normal markdown link.
			return tag
		}
		mime := strings.TrimSpace(du[1])
		payload := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				return -1
			}
			return r
		}, du[2])
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			// Malformed payload — drop the whole tag rather than
			// leaving base64 garbage in the output.
			return ""
		}
		id := "html-" + shortHash(data)
		if _, seen := images[id]; !seen {
			alt := ""
			if m := htmlAltPattern.FindStringSubmatch(tag); m != nil {
				alt = strings.TrimSpace(m[1])
			}
			images[id] = extractedImage{
				ID:            id,
				Index:         nextIndex,
				Data:          data,
				MimeType:      mime,
				Extension:     extensionForMime(mime),
				AuthorAltText: alt,
			}
			nextIndex++
		}
		return imagePlaceholder(id)
	})
}
