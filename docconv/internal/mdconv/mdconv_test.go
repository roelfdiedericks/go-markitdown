package mdconv

import (
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// TestOutputIsNFC feeds Convert HTML containing a decomposed "café" and
// asserts the output is in Unicode Normalization Form C. Without NFC the
// string would still render identically but would hash differently from a
// composed "café" on the LLM side, silently breaking deduplication and
// exact-match lookups in downstream pipelines.
func TestOutputIsNFC(t *testing.T) {
	// "cafe\u0301" — decomposed form.
	decomposed := "cafe\u0301"
	composed := "café"
	if decomposed == composed {
		t.Fatalf("test setup bug: decomposed and composed should differ by bytes")
	}
	if !norm.NFD.IsNormalString(decomposed) {
		t.Fatalf("test setup bug: input is not in NFD")
	}

	html := "<p>Let's go to the " + decomposed + "</p>"
	md, err := Convert(html)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if !strings.Contains(md, composed) {
		t.Fatalf("expected composed café in output, got %q", md)
	}
	if strings.Contains(md, decomposed) {
		t.Fatalf("found decomposed café in output — NFC normalization regressed: %q", md)
	}
	if !norm.NFC.IsNormalString(md) {
		t.Fatalf("output is not NFC-normalised: %q", md)
	}
}

// TestTidyIdempotent confirms running tidy twice produces the same bytes
// (a light guard against future refactors that accidentally re-introduce
// mutating passes).
func TestTidyIdempotent(t *testing.T) {
	in := "# Title\n\n\n\nSome body\n\n\n\n<!--THE END-->\n\nMore"
	once := tidy(in)
	twice := tidy(once)
	if once != twice {
		t.Fatalf("tidy not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}
