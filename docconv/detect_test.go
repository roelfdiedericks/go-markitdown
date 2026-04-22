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

// TestFromMIME covers the MIME → Format mapping (happy paths), case and
// parameter robustness, and a set of negatives that must resolve to
// FormatAuto (and therefore Supports == false).
func TestFromMIME(t *testing.T) {
	cases := []struct {
		mime   string
		expect Format
		supp   bool
	}{
		{"application/pdf", FormatPDF, true},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", FormatDOCX, true},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", FormatXLSX, true},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", FormatPPTX, true},
		{"application/epub+zip", FormatEPUB, true},
		{"application/x-mobipocket-ebook", FormatMOBI, true},
		{"text/html", FormatHTML, true},
		{"application/xhtml+xml", FormatHTML, true},
		{"text/plain", FormatText, true},
		{"text/markdown", FormatText, true},

		{"APPLICATION/PDF", FormatPDF, true},
		{"text/plain; charset=utf-8", FormatText, true},
		{"  text/html  ", FormatHTML, true},

		{"image/png", FormatAuto, false},
		{"image/jpeg", FormatAuto, false},
		{"application/zip", FormatAuto, false},
		{"application/octet-stream", FormatAuto, false},
		{"", FormatAuto, false},
		{"garbage", FormatAuto, false},
		{"application/", FormatAuto, false},
	}

	for _, tc := range cases {
		t.Run(tc.mime, func(t *testing.T) {
			if got := FromMIME(tc.mime); got != tc.expect {
				t.Errorf("FromMIME(%q) = %s, want %s", tc.mime, got, tc.expect)
			}
			if got := Supports(tc.mime); got != tc.supp {
				t.Errorf("Supports(%q) = %v, want %v", tc.mime, got, tc.supp)
			}
		})
	}
}
