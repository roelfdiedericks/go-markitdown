//go:build nofitz

package docconv

import "context"

// extractFitz is a stub when the nofitz build tag is set. Only PDF, EPUB,
// and MOBI actually require go-fitz — DOCX, PPTX, XLSX, HTML, and text
// now work without CGO. See docconv.Format dispatch in docconv.go.
func extractFitz(_ context.Context, _ []byte, format Format, _ Options) (string, Metadata, error) {
	return "", Metadata{Format: format.String()}, ErrFitzRequired
}

// extractFitzFallback is the DOCX/PPTX fallback entry. Under the nofitz
// build it always returns ErrFitzRequired; the native DOCX and PPTX
// backends detect that and surface the primary parse error instead of
// the fallback error, so callers see a meaningful message rather than
// "fitz not available" on a badly-formed document.
func extractFitzFallback(_ context.Context, _ []byte, format Format, _ Options) (string, Metadata, error) {
	return "", Metadata{Format: format.String()}, ErrFitzRequired
}
