//go:build !nofitz

package docconv

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"

	"github.com/gen2brain/go-fitz"

	"github.com/roelfdiedericks/go-markitdown/docconv/internal/mdconv"
	"github.com/roelfdiedericks/go-markitdown/docconv/internal/ooxml"
)

// fitzImageTagPattern matches <img ... src="data:<mime>;base64,<payload>" ...>
// tags as emitted by go-fitz's HTML export. Capture groups: (1) MIME type,
// (2) raw base64 payload (may contain whitespace which we strip before
// decoding — go-fitz wraps long payloads across lines).
var fitzImageTagPattern = regexp.MustCompile(`(?is)<img[^>]*src=["']data:([^;"']+);base64,([^"']*)["'][^>]*/?>`)

// fitzTagStripPattern strips any remaining HTML tags when we want the
// page's plain text (for per-page context windows around images and for
// the per-page "is this empty?" check used by the per-page OCR path).
var fitzTagStripPattern = regexp.MustCompile(`<[^>]+>`)

// fitzSoftHyphenPattern rejoins PDF line-wrap hyphenation. Matches only
// lowercase-letter + "-" + newline + optional whitespace + lowercase-letter
// so it leaves "X-Ray", "3-phase", "state-of-the-art", and bullet dashes
// alone. Rare legitimate lowercase-to-lowercase compounds that straddle a
// line break collapse to their joined form; in practice the joined form is
// closer to what the LLM reader expects than the broken form.
var fitzSoftHyphenPattern = regexp.MustCompile(`(\p{Ll})-\n\s*(\p{Ll})`)

// fitzWhitespacePattern collapses any run of whitespace (including
// newlines) to a single space. Used when building the context window
// strings for image captions — we need compact, readable surroundings, not
// the raw page layout.
var fitzWhitespacePattern = regexp.MustCompile(`\s+`)

const (
	// fitzContextWindow is the character budget for each side of an
	// image's context. 400 chars is roughly a paragraph — long enough to
	// capture a Figure caption, short enough to fit comfortably into a
	// describer prompt.
	fitzContextWindow = 400

	// fitzPageTextThreshold is the number of non-whitespace characters
	// below which a page is considered "effectively empty" and eligible
	// for per-page OCR fallback. Captures header/footer-only pages
	// without firing on genuinely short ones.
	fitzPageTextThreshold = 20
)

// fitzIDPrefix returns the placeholder-ID prefix for a fitz-backed format.
// Keeping the prefix format-specific means a document that contains both
// PDF-embedded images (via the appendix path) and fitz-extracted images
// won't accidentally collide IDs.
func fitzIDPrefix(format Format) string {
	switch format {
	case FormatEPUB:
		return "epub"
	case FormatMOBI:
		return "mobi"
	}
	return "pdf"
}

// extractFitz handles PDF/EPUB/MOBI via go-fitz. It also serves as the
// fallback backend for DOCX/PPTX when the native structure-preserving
// parsers fail; see extractFitzFallback.
//
// Per-page strategy:
//
//  1. Render the page to HTML via go-fitz.
//  2. Capture any <img src="data:...base64,..."> tags: decode the payload,
//     compute a content hash, register the image with surrounding-text
//     context, rewrite the tag in place to <img src="rid:<id>" alt=""/>.
//  3. Rejoin PDF soft-hyphens (confi-\nguration -> configuration).
//  4. Run the HTML through mdconv.
//  5. If the resulting markdown is effectively empty and per-page OCR is
//     configured, replace the page body with the OCR transcription.
//  6. Prefix with "<!-- Page N of M -->" so downstream LLMs can cite
//     page-grounded references.
//
// After all pages: join with "---", then run replaceImagePlaceholders so
// every captured image gets either a describer caption or a fallback label.
func extractFitz(ctx context.Context, data []byte, format Format, opts Options) (string, Metadata, error) {
	doc, err := fitz.NewFromMemory(data)
	if err != nil {
		return "", Metadata{Format: format.String()}, wrapBackendError(format, err)
	}
	defer doc.Close()

	meta := buildFitzMetadata(doc, format)
	pages := doc.NumPage()
	if pages <= 0 {
		return "", meta, ErrNoText
	}

	idPrefix := fitzIDPrefix(format)
	images := make(map[string]extractedImage)
	nextIndex := 0

	bodies := make([]string, 0, pages)
	for i := 0; i < pages; i++ {
		if cerr := ctx.Err(); cerr != nil {
			return "", meta, cerr
		}
		html, herr := doc.HTML(i, false)
		if herr != nil {
			return "", meta, wrapBackendError(format, fmt.Errorf("page %d html: %w", i+1, herr))
		}

		// Capture embedded images BEFORE hyphen rejoin / tag-strip;
		// context windows are built from the post-capture HTML so the
		// rewritten <img rid:...> placeholders are themselves stripped
		// and don't appear inside the context a neighbouring image's
		// prompt sees.
		html = captureFitzImages(html, idPrefix, images, &nextIndex)
		html = fitzSoftHyphenPattern.ReplaceAllString(html, "$1$2")

		md, merr := mdconv.Convert(html)
		if merr != nil {
			return "", meta, wrapBackendError(format, fmt.Errorf("page %d markdown: %w", i+1, merr))
		}

		// Per-page OCR fallback: if the page's plain text is under the
		// threshold and OCR is configured, swap the body for the
		// transcription. Unlike the old per-document gate, this
		// recovers content from mixed scanned/text PDFs where most
		// pages extract cleanly but a minority are scans.
		if opts.OCRFallback && opts.LLMClient != nil && isEffectivelyEmpty(md) {
			ocr, oerr := runOCRForPage(ctx, doc, i, opts)
			if oerr == nil && strings.TrimSpace(ocr) != "" {
				md = ocr
			}
			// OCR errors fall through with the thin body intact —
			// one bad page should not sink the whole document.
		}

		bodies = append(bodies, fmt.Sprintf("<!-- Page %d of %d -->\n%s", i+1, pages, md))
	}

	joined := strings.TrimSpace(strings.Join(bodies, "\n\n---\n\n"))

	// Resolve image placeholders — the per-page loop registered every
	// data-URI image it found; the post-pass describes or strips them.
	if len(images) > 0 || opts.IncludeImages {
		replaced, rerr := replaceImagePlaceholders(ctx, joined, images, opts)
		if rerr != nil {
			return "", meta, wrapBackendError(format, rerr)
		}
		joined = replaced
	}

	// Append OOXML images when extractFitz is serving as the DOCX/PPTX
	// fallback: the native walkers failed and we never saw the embedded
	// media by rIds, so surface whatever's in word/media or ppt/media
	// via the appendix. PDF/EPUB/MOBI images are captured inline above.
	if opts.IncludeImages && isOOXMLFitzFormat(format) {
		imgs, ierr := ooxml.ExtractImages(data, format.String())
		if ierr == nil && len(imgs) > 0 {
			appendix := buildImageAppendix(ctx, imgs, opts)
			if appendix != "" {
				joined = joined + "\n\n" + appendix
			}
		}
	}

	if strings.TrimSpace(joined) == "" || stripPageMarkers(joined) == "" {
		return "", meta, ErrNoText
	}

	return joined, meta, nil
}

// captureFitzImages walks every data-URI <img> in html, decodes the bytes,
// registers an extractedImage (keyed by a short content hash), and rewrites
// the tag in place to a rid: placeholder the shared post-pass can resolve.
//
// Context capture is cheap: we pull ~400 chars of HTML either side of the
// tag, strip any remaining tags, and trim-on-word-boundary to the budget.
// Good enough to catch "Figure 3: ...", nearby paragraphs, and captions
// without attempting layout reconstruction.
func captureFitzImages(html, idPrefix string, images map[string]extractedImage, nextIndex *int) string {
	matches := fitzImageTagPattern.FindAllStringSubmatchIndex(html, -1)
	if len(matches) == 0 {
		return html
	}

	var out []byte
	pos := 0
	for _, m := range matches {
		tagStart, tagEnd := m[0], m[1]
		mimeType := html[m[2]:m[3]]
		payload := html[m[4]:m[5]]

		// go-fitz wraps long base64 payloads across newlines; strip
		// whitespace before decoding.
		payload = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
				return -1
			}
			return r
		}, payload)

		data, derr := base64.StdEncoding.DecodeString(payload)
		if derr != nil || len(data) == 0 {
			// Undecodable: drop the tag entirely. Better to lose
			// one image than to leave a megabyte of base64 in the
			// output.
			out = append(out, html[pos:tagStart]...)
			pos = tagEnd
			continue
		}

		id := fmt.Sprintf("%s-%s", idPrefix, shortHash(data))

		if _, seen := images[id]; !seen {
			before := contextWindow(html[:tagStart], fitzContextWindow, true)
			after := contextWindow(html[tagEnd:], fitzContextWindow, false)
			images[id] = extractedImage{
				ID:            id,
				Index:         *nextIndex,
				Data:          data,
				MimeType:      mimeType,
				Extension:     extensionForMime(mimeType),
				ContextBefore: before,
				ContextAfter:  after,
			}
			*nextIndex++
		}

		out = append(out, html[pos:tagStart]...)
		out = append(out, []byte(fmt.Sprintf(`<img src="rid:%s" alt=""/>`, id))...)
		pos = tagEnd
	}
	out = append(out, html[pos:]...)
	return string(out)
}

// contextWindow produces a compact plaintext snippet from raw HTML around
// an image tag. takeFromEnd=true returns the LAST budget chars of s (i.e.
// the text immediately BEFORE the tag); takeFromEnd=false returns the
// FIRST budget chars of s (immediately AFTER the tag). HTML tags and
// whitespace are collapsed.
func contextWindow(s string, budget int, takeFromEnd bool) string {
	stripped := fitzTagStripPattern.ReplaceAllString(s, " ")
	stripped = fitzWhitespacePattern.ReplaceAllString(stripped, " ")
	stripped = strings.TrimSpace(stripped)
	if stripped == "" {
		return ""
	}
	if len(stripped) <= budget {
		return stripped
	}
	if takeFromEnd {
		tail := stripped[len(stripped)-budget:]
		// Trim on word boundary: drop any leading partial word so the
		// describer doesn't see half a token at the start.
		if space := strings.IndexByte(tail, ' '); space != -1 && space < len(tail)/4 {
			tail = tail[space+1:]
		}
		return tail
	}
	head := stripped[:budget]
	if space := strings.LastIndexByte(head, ' '); space != -1 && space > len(head)*3/4 {
		head = head[:space]
	}
	return head
}

// extensionForMime maps a MIME type declared in a data: URI to the file
// extension we'd store that image under. Unknown types default to .bin so
// a missing extension does not blow up file writes.
// extensionForMime now lives in images.go so non-fitz builds and the
// HTML backend can share it.

// isEffectivelyEmpty reports whether a page's markdown body has fewer
// than fitzPageTextThreshold non-whitespace characters after stripping
// page markers and placeholder-image refs. The metric catches scan pages
// that produce nothing but headers/footers without firing on very short
// (but real) text pages.
func isEffectivelyEmpty(md string) bool {
	text := stripPageMarkers(md)
	text = placeholderPattern.ReplaceAllString(text, "")
	count := 0
	for _, r := range text {
		if r != ' ' && r != '\n' && r != '\r' && r != '\t' {
			count++
			if count >= fitzPageTextThreshold {
				return false
			}
		}
	}
	return count < fitzPageTextThreshold
}

// stripPageMarkers removes the "<!-- Page N of M -->" comments we inject
// per page, so the "is the document effectively empty?" terminal check
// doesn't count the markers themselves as content.
var fitzPageMarkerPattern = regexp.MustCompile(`(?m)^<!-- Page \d+ of \d+ -->[[:space:]]*$`)

func stripPageMarkers(s string) string {
	return strings.TrimSpace(fitzPageMarkerPattern.ReplaceAllString(s, ""))
}

func isOOXMLFitzFormat(f Format) bool {
	return f == FormatDOCX || f == FormatPPTX
}

// extractFitzFallback is the DOCX/PPTX fallback entry. It exists so the
// native structure-preserving DOCX and PPTX backends can retry through
// go-fitz when parsing the OOXML XML directly fails. Under the nofitz
// build tag this returns ErrFitzRequired.
func extractFitzFallback(ctx context.Context, data []byte, format Format, opts Options) (string, Metadata, error) {
	return extractFitz(ctx, data, format, opts)
}

func buildFitzMetadata(doc *fitz.Document, format Format) Metadata {
	m := Metadata{Format: format.String(), Pages: doc.NumPage()}
	if raw := doc.Metadata(); raw != nil {
		if v, ok := raw["title"]; ok {
			m.Title = strings.TrimSpace(v)
		}
		if v, ok := raw["author"]; ok {
			m.Author = strings.TrimSpace(v)
		}
		if v, ok := raw["subject"]; ok {
			m.Subject = strings.TrimSpace(v)
		}
		if v, ok := raw["keywords"]; ok {
			m.Keywords = strings.TrimSpace(v)
		}
		if v, ok := raw["creationDate"]; ok {
			m.Created = strings.TrimSpace(v)
		}
		if v, ok := raw["modDate"]; ok {
			m.Modified = strings.TrimSpace(v)
		}
	}
	return m
}

// buildImageAppendix turns OOXML-extracted images into markdown. Used only
// from the DOCX/PPTX fallback path — primary paths handle images inline
// via the native walkers.
//
// Runs post-mdconv, so placeholders are emitted in their final markdown
// form "![](rid:<id>)" directly rather than as HTML <img> tags (those
// only survive when emitted pre-mdconv).
func buildImageAppendix(ctx context.Context, raw []ooxml.Image, opts Options) string {
	if !opts.IncludeImages {
		return ""
	}
	imgs := make(map[string]extractedImage, len(raw))
	var b strings.Builder
	b.WriteString("## Images\n\n")

	for i, r := range raw {
		id := fmt.Sprintf("ooxml-%d", i)
		imgs[id] = extractedImage{
			ID:        id,
			Index:     i,
			Data:      r.Data,
			MimeType:  r.MimeType,
			Extension: r.Extension,
		}
		fmt.Fprintf(&b, "![](rid:%s)\n\n", id)
	}

	replaced, err := replaceImagePlaceholders(ctx, b.String(), imgs, opts)
	if err != nil {
		return "## Images\n\n(image extraction failed: " + err.Error() + ")\n"
	}
	return strings.TrimSpace(replaced) + "\n"
}
