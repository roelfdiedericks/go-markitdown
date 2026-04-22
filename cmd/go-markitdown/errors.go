package main

import (
	"errors"

	"github.com/roelfdiedericks/go-markitdown/docconv"
)

// badArgsSentinel wraps errors that should map to exit code 3 (invalid
// arguments) rather than the default extraction-error exit 1.
type badArgsSentinel struct{ err error }

func (b *badArgsSentinel) Error() string { return b.err.Error() }
func (b *badArgsSentinel) Unwrap() error { return b.err }

func badArgsError(err error) error {
	if err == nil {
		return nil
	}
	return &badArgsSentinel{err: err}
}

// codeForError maps a library error into the SPEC exit codes.
func codeForError(err error) int {
	if err == nil {
		return exitOK
	}
	var bad *badArgsSentinel
	if errors.As(err, &bad) {
		return exitBadArgs
	}
	if errors.Is(err, docconv.ErrUnsupportedFormat) || errors.Is(err, docconv.ErrFitzRequired) {
		return exitUnsupported
	}
	return exitExtractionErr
}
