package docconv

import (
	"bytes"
	"path/filepath"
	"testing"
)

// TestDetectFixtures verifies that every testdata file is identified by
// Detect as the expected format. This is the primary smoke test for the
// magic-byte and extension logic.
func TestDetectFixtures(t *testing.T) {
	cases := []struct {
		file   string
		expect Format
	}{
		{"test.pdf", FormatPDF},
		{"test.docx", FormatDOCX},
		{"test.xlsx", FormatXLSX},
		{"test.pptx", FormatPPTX},
		{"test.epub", FormatEPUB},
		{"test.mobi", FormatMOBI},
		{"test.html", FormatHTML},
		{"test.txt", FormatText},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got, err := Detect(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatalf("Detect(%q) error: %v", tc.file, err)
			}
			if got != tc.expect {
				t.Errorf("Detect(%q) = %s, want %s", tc.file, got, tc.expect)
			}
		})
	}
}

// TestDetectReader covers the streaming Detect path on the same fixtures.
func TestDetectReader(t *testing.T) {
	cases := []struct {
		file   string
		expect Format
	}{
		{"testdata/test.pdf", FormatPDF},
		{"testdata/test.docx", FormatDOCX},
		{"testdata/test.xlsx", FormatXLSX},
		{"testdata/test.pptx", FormatPPTX},
		{"testdata/test.epub", FormatEPUB},
		{"testdata/test.mobi", FormatMOBI},
		{"testdata/test.html", FormatHTML},
		{"testdata/test.txt", FormatText},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data := mustRead(t, tc.file)
			got, _, err := DetectReader(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("DetectReader(%q) error: %v", tc.file, err)
			}
			if got != tc.expect {
				t.Errorf("DetectReader(%q) = %s, want %s", tc.file, got, tc.expect)
			}
		})
	}
}

// TestDetectUnknown confirms that random bytes return ErrUnsupportedFormat.
func TestDetectUnknown(t *testing.T) {
	// All-zero bytes are neither a known format nor valid printable text.
	junk := make([]byte, 256)
	_, _, err := DetectReader(bytes.NewReader(junk))
	if err == nil {
		t.Fatalf("DetectReader(junk) expected error, got nil")
	}
}
