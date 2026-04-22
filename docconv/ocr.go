//go:build !nofitz

package docconv

import (
	"context"
	"fmt"
	"strings"

	"github.com/gen2brain/go-fitz"
)

// runOCRFallback renders each page of doc to PNG at opts.OCRDPI and calls
// opts.LLMClient.DescribeImage with opts.OCRPrompt. Pages are joined with
// "---" separators to mirror the normal text pipeline.
func runOCRFallback(ctx context.Context, doc *fitz.Document, opts Options) (string, error) {
	if opts.LLMClient == nil {
		return "", fmt.Errorf("OCRFallback requested but LLMClient is nil")
	}
	dpi := opts.OCRDPI
	if dpi <= 0 {
		dpi = DefaultOCRDPI
	}

	pages := doc.NumPage()
	if pages <= 0 {
		return "", ErrNoText
	}

	bodies := make([]string, 0, pages)
	for i := 0; i < pages; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		png, err := doc.ImagePNG(i, dpi)
		if err != nil {
			return "", fmt.Errorf("render page %d: %w", i+1, err)
		}
		desc, derr := opts.LLMClient.DescribeImage(ctx, png, "image/png", opts.OCRPrompt)
		if derr != nil {
			return "", fmt.Errorf("OCR page %d: %w", i+1, derr)
		}
		bodies = append(bodies, strings.TrimSpace(desc))
	}

	return strings.TrimSpace(strings.Join(bodies, "\n\n---\n\n")), nil
}
