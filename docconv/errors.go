package docconv

import "errors"

// Typed errors returned by the package. Callers can route behaviour by
// comparing with errors.Is.
var (
	// ErrUnsupportedFormat is returned when the input format cannot be
	// handled by this build (for example, an image passed to Extract).
	ErrUnsupportedFormat = errors.New("unsupported document format")

	// ErrNoText is returned when a document has no extractable text and
	// OCRFallback is disabled. The document may be a scan or a document
	// with only embedded images.
	ErrNoText = errors.New("no extractable text (may be scanned/image)")

	// ErrCorruptDocument is returned when the document cannot be parsed.
	ErrCorruptDocument = errors.New("document is corrupt or unreadable")

	// ErrPasswordProtected is returned when the document requires a
	// password that has not been supplied.
	ErrPasswordProtected = errors.New("document is password protected")

	// ErrFitzRequired is returned by the "nofitz" build when a caller
	// requests a format that requires the go-fitz / MuPDF backend.
	ErrFitzRequired = errors.New("go-fitz required for this format (rebuild without -tags nofitz)")
)
