//go:build !nofitz

package docconv

import (
	"context"
	"fmt"
	"strings"

	"github.com/gen2brain/go-fitz"
)

// runOCRForPage renders a single page to PNG and feeds it through the
// configured ImageDescriber with opts.OCRPrompt. Returns the trimmed
// transcription, or an error on render / describer / context failure.
//
// This is the per-page fallback primitive: callers (extractFitz) invoke it
// on each page whose extracted text is effectively empty. The
// whole-document variant (runOCRFallback) is a thin wrapper that invokes
// this helper for every page — used only as the DOCX/PPTX "empty native
// walker" last-ditch path.
func runOCRForPage(ctx context.Context, doc *fitz.Document, pageIdx int, opts Options) (string, error) {
	if opts.LLMClient == nil {
		return "", fmt.Errorf("OCRFallback requested but LLMClient is nil")
	}
	if doc == nil {
		return "", fmt.Errorf("OCRFallback: nil document")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	dpi := opts.OCRDPI
	if dpi <= 0 {
		dpi = DefaultOCRDPI
	}
	prompt := opts.OCRPrompt
	if prompt == "" {
		prompt = DefaultOCRPrompt
	}

	png, err := doc.ImagePNG(pageIdx, dpi)
	if err != nil {
		return "", fmt.Errorf("render page %d: %w", pageIdx+1, err)
	}
	desc, derr := opts.LLMClient.DescribeImage(ctx, png, "image/png", prompt)
	if derr != nil {
		return "", fmt.Errorf("OCR page %d: %w", pageIdx+1, derr)
	}
	return strings.TrimSpace(desc), nil
}

// runOCRFallback renders every page of doc via runOCRForPage and joins the
// transcriptions with "---" separators. Retained for the DOCX/PPTX OCR
// fallback paths that need a whole-document transcription when their
// native walkers produce empty output.
func runOCRFallback(ctx context.Context, doc *fitz.Document, opts Options) (string, error) {
	if opts.LLMClient == nil {
		return "", fmt.Errorf("OCRFallback requested but LLMClient is nil")
	}
	if doc == nil {
		return "", fmt.Errorf("OCRFallback: nil document")
	}
	pages := doc.NumPage()
	if pages <= 0 {
		return "", ErrNoText
	}

	bodies := make([]string, 0, pages)
	for i := 0; i < pages; i++ {
		body, err := runOCRForPage(ctx, doc, i, opts)
		if err != nil {
			return "", err
		}
		bodies = append(bodies, body)
	}
	return strings.TrimSpace(strings.Join(bodies, "\n\n---\n\n")), nil
}
