//go:build !nofitz

package docconv

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/gen2brain/go-fitz"

	"github.com/roelfdiedericks/go-markitdown/docconv/internal/mdconv"
	"github.com/roelfdiedericks/go-markitdown/docconv/internal/ooxml"
)

// fitzDataURIPattern matches <img ... src="data:..." ...> tags that go-fitz
// emits in HTML output for PDFs and similar. The base64 payloads are huge
// and useless for LLM context, so we strip them before html-to-markdown
// conversion.
var fitzDataURIPattern = regexp.MustCompile(`(?is)<img[^>]*src=["']data:[^"']*["'][^>]*/?>`)

// extractFitz handles PDF/EPUB/MOBI using go-fitz for structured text. It
// is also used as the fallback backend for DOCX/PPTX when the native
// structure-preserving parsers fail — see extractFitzFallback.
//
// Strategy:
//
//  1. Open the document via go-fitz.NewFromMemory.
//  2. For each page call HTML(page, false) → html-to-markdown.
//  3. Join pages with "---\n".
//  4. If empty and OCRFallback is on, render each page as PNG and feed it
//     through LLMClient.
//  5. If IncludeImages, pull embedded images via the OOXML zip walker and
//     append them at the end (document-order placement for OOXML requires
//     more work than v0.1 scopes for; we keep a stable appendix).
func extractFitz(ctx context.Context, data []byte, format Format, opts Options) (string, Metadata, error) {
	doc, err := fitz.NewFromMemory(data)
	if err != nil {
		return "", Metadata{Format: format.String()}, wrapBackendError(format, err)
	}
	defer doc.Close()

	meta := buildFitzMetadata(doc, format)

	pages := doc.NumPage()
	if pages <= 0 {
		if opts.OCRFallback && opts.LLMClient != nil {
			return "", meta, wrapBackendError(format, fmt.Errorf("no pages to OCR"))
		}
		return "", meta, ErrNoText
	}

	bodies := make([]string, 0, pages)
	for i := 0; i < pages; i++ {
		html, herr := doc.HTML(i, false)
		if herr != nil {
			return "", meta, wrapBackendError(format, fmt.Errorf("page %d html: %w", i+1, herr))
		}
		html = fitzDataURIPattern.ReplaceAllString(html, "")
		md, merr := mdconv.Convert(html)
		if merr != nil {
			return "", meta, wrapBackendError(format, fmt.Errorf("page %d markdown: %w", i+1, merr))
		}
		bodies = append(bodies, md)
	}

	joined := strings.TrimSpace(strings.Join(bodies, "\n\n---\n\n"))

	// If the whole document is empty, optionally run OCR fallback.
	if joined == "" {
		if opts.OCRFallback && opts.LLMClient != nil {
			ocrMD, oerr := runOCRFallback(ctx, doc, opts)
			if oerr != nil {
				return "", meta, wrapBackendError(format, oerr)
			}
			joined = ocrMD
		}
		if strings.TrimSpace(joined) == "" {
			return "", meta, ErrNoText
		}
	}

	// Append OOXML images when requested. DOCX/PPTX only (XLSX has its
	// own backend). PDF/EPUB/MOBI embedded-image extraction is out of
	// scope for v0.1 — go-fitz page rasterisation isn't the same as
	// embedded-image extraction, and we deliberately avoid fabricating
	// per-page screenshots here.
	if opts.IncludeImages && isOOXMLFitzFormat(format) {
		imgs, ierr := ooxml.ExtractImages(data, format.String())
		if ierr == nil && len(imgs) > 0 {
			appendix := buildImageAppendix(ctx, imgs, opts)
			if appendix != "" {
				joined = joined + "\n\n" + appendix
			}
		}
	}

	return joined, meta, nil
}

func isOOXMLFitzFormat(f Format) bool {
	return f == FormatDOCX || f == FormatPPTX
}

// extractFitzFallback is the DOCX/PPTX fallback entry. It exists so the
// native structure-preserving DOCX and PPTX backends can retry through
// go-fitz when parsing the OOXML XML directly fails. Under the nofitz
// build tag this returns ErrFitzRequired.
//
// Behaviour is identical to extractFitz; the separate function name makes
// the call graph clear and gives the nofitz stub a single surface to trip.
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

// buildImageAppendix turns OOXML-extracted images into markdown. When the
// LLMClient is available, each image gets a DescribeImage call.
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
		b.WriteString(imagePlaceholder(id))
		b.WriteString("\n\n")
	}

	replaced, err := replaceImagePlaceholders(ctx, b.String(), imgs, opts)
	if err != nil {
		// On failure, return the header with a short note rather than
		// aborting the whole document.
		return "## Images\n\n(image extraction failed: " + err.Error() + ")\n"
	}
	return strings.TrimSpace(replaced) + "\n"
}
