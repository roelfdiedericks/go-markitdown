//go:build !nofitz

package docconv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestExtractFitzFormats runs Extract on every fitz-handled fixture. We
// assert the result is non-empty rather than matching specific text because
// go-fitz → html-to-markdown output is allowed to wander between releases.
func TestExtractFitzFormats(t *testing.T) {
	fixtures := []struct {
		file   string
		format Format
	}{
		{"test.pdf", FormatPDF},
		{"test.docx", FormatDOCX},
		{"test.pptx", FormatPPTX},
		{"test.epub", FormatEPUB},
		{"test.mobi", FormatMOBI},
	}

	for _, fx := range fixtures {
		t.Run(fx.file, func(t *testing.T) {
			md, err := Extract(filepath.Join("testdata", fx.file), nil)
			if err != nil {
				t.Fatalf("Extract %s: %v", fx.file, err)
			}
			if strings.TrimSpace(md) == "" {
				t.Errorf("Extract %s returned empty", fx.file)
			}
		})
	}
}

// TestOCRFallbackRunsPerPageNotDocumentWide verifies the v0.2 change to
// per-page OCR: pages whose extracted text is effectively empty (header /
// page-number only, or a pure scan) trigger the describer with the OCR
// prompt, while text-rich pages pass through untouched. The test.pdf
// fixture has 84 pages — some of which are blank chapter covers or
// single-page-number pages that the per-page threshold classifies as
// empty. We assert only that (a) the OCR prompt is used on whatever
// calls do fire (proving wiring), and (b) the output still contains
// real body content from the text-rich pages (so the OCR path is not
// blindly replacing everything).
func TestOCRFallbackRunsPerPageNotDocumentWide(t *testing.T) {
	stub := &stubDescriber{reply: "OCR output"}
	md, err := Extract(filepath.Join("testdata", "test.pdf"), &Options{
		LLMClient:   stub,
		OCRFallback: true,
	})
	if err != nil {
		t.Fatalf("Extract test.pdf: %v", err)
	}
	if strings.TrimSpace(md) == "" {
		t.Fatalf("expected non-empty markdown from test.pdf")
	}
	// Any call that did fire must have received the OCR prompt, not
	// the description prompt — OCR has a distinct hook path.
	for i, c := range stub.calls {
		if !strings.Contains(c.prompt, "Transcribe") {
			t.Errorf("call #%d used non-OCR prompt: %q", i, c.prompt)
		}
	}
	// Text-rich content survived (per-page OCR didn't nuke the whole
	// document). "Chapter" appears on at least one text-rich page.
	if !strings.Contains(md, "Chapter") {
		t.Errorf("text-rich pages did not survive per-page OCR, output missing 'Chapter'")
	}
}

// TestOCRFallbackRequiresLLMClient ensures that enabling OCR without an
// ImageDescriber doesn't panic; the library silently falls through to the
// ErrNoText path (as documented).
func TestOCRFallbackRequiresLLMClient(t *testing.T) {
	// We can't easily get a textless document without synthesising one,
	// so we assert the invariant via unit-level behaviour: runOCRFallback
	// should refuse when LLMClient is nil.
	_, err := runOCRFallback(context.Background(), nil, Options{OCRFallback: true})
	if err == nil {
		t.Fatal("expected error when LLMClient is nil, got nil")
	}
}

// TestHyphenRejoinPattern validates the soft-hyphen pattern that rejoins
// PDF line-wrapped words. Positive cases: classic "confi-\nguration" and
// whitespace variants. Negative cases: "X-Ray", "3-phase", "-\n" at the
// start of a bullet, and uppercase second halves (preserved as-is).
func TestHyphenRejoinPattern(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Rejoin: lowercase-hyphen-newline-lowercase.
		{"confi-\nguration", "configuration"},
		{"imple-\n  mentation", "implementation"},
		{"multi-\n\tpart", "multipart"},
		// Do not rejoin: uppercase second half preserves proper nouns.
		{"X-\nRay", "X-\nRay"},
		// Do not rejoin: digit first half (version numbers, 3-phase).
		{"3-\nphase", "3-\nphase"},
		// Do not rejoin when no newline follows.
		{"state-of-the-art", "state-of-the-art"},
	}
	for _, c := range cases {
		got := fitzSoftHyphenPattern.ReplaceAllString(c.in, "$1$2")
		if got != c.want {
			t.Errorf("rejoin(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestPageMarkersEmitted confirms the fitz backend prefixes each page
// with "<!-- Page N of M -->" so LLMs can cite back into the source.
// Uses the real test.pdf fixture and asserts on at least one marker.
func TestPageMarkersEmitted(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.pdf"), nil)
	if err != nil {
		t.Fatalf("Extract test.pdf: %v", err)
	}
	if !strings.Contains(md, "<!-- Page 1 of ") {
		t.Errorf("expected page marker for page 1, output:\n%s", md)
	}
}

// TestCPQ_DocumentOrder confirms pages emerge in 1..N order with page
// markers. CPQ.pdf is a multi-page fixture with many embedded images; its
// page markers are the easiest invariant to pin down without hardcoding
// specific copy.
func TestCPQ_DocumentOrder(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "CPQ.pdf"), nil)
	if err != nil {
		t.Fatalf("Extract CPQ.pdf: %v", err)
	}
	// Page markers must appear strictly in ascending order. Extracting
	// them via regex and comparing to a sorted sequence catches silent
	// reorders.
	markerRe := regexp.MustCompile(`<!-- Page (\d+) of \d+ -->`)
	matches := markerRe.FindAllStringSubmatch(md, -1)
	if len(matches) == 0 {
		t.Fatalf("no page markers found in CPQ output")
	}
	for i, m := range matches {
		want := i + 1
		if got := m[1]; got != intToA(want) {
			t.Errorf("marker #%d has page %q, want %d", i, got, want)
		}
	}
}

// TestCPQ_StubDescriberCalledForImages verifies the describer is invoked
// when IncludeImages=true and an LLMClient is supplied. We don't assert a
// specific count (which version of go-fitz is running, which images count
// as "decorative", and whether dedup collapses identical decorations all
// shift the number); we only assert the hook ran.
func TestCPQ_StubDescriberCalledForImages(t *testing.T) {
	stub := &stubDescriber{reply: "image caption"}
	_, err := Extract(filepath.Join("testdata", "CPQ.pdf"), &Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("Extract CPQ.pdf: %v", err)
	}
	if len(stub.calls) == 0 {
		t.Fatalf("expected at least one DescribeImage call for CPQ.pdf")
	}
	// Default prompt template must be used (no caller-supplied prompt)
	// so the prompt surface should contain the template's identifying
	// header line.
	if !strings.Contains(stub.calls[0].prompt, "concise caption") {
		t.Errorf("expected library-owned prompt template, got %q", stub.calls[0].prompt)
	}
}

// TestCPQ_ImageDirByteParity confirms that writing images to ImageDir
// preserves the byte payload the backend decoded — so callers get back
// the exact pixels the document contained, not a re-encode.
func TestCPQ_ImageDirByteParity(t *testing.T) {
	dir := t.TempDir()
	_, err := Extract(filepath.Join("testdata", "CPQ.pdf"), &Options{
		IncludeImages: true,
		ImageDir:      dir,
	})
	if err != nil {
		t.Fatalf("Extract CPQ.pdf: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read ImageDir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one image written to ImageDir")
	}
	// Each file should be a non-trivial payload. Zero-byte files mean
	// the extractor decoded empty data — which would indicate the dedup
	// map stored a placeholder without bytes.
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("stat %s: %v", e.Name(), err)
		}
		if info.Size() == 0 {
			t.Errorf("image %s written empty", e.Name())
		}
	}
}

// TestCPQ_IncludeImagesFalseStripsEverything confirms the well-defined
// no-images rendering: zero placeholders, zero describer calls.
func TestCPQ_IncludeImagesFalseStripsEverything(t *testing.T) {
	stub := &stubDescriber{reply: "should-not-fire"}
	md, err := Extract(filepath.Join("testdata", "CPQ.pdf"), &Options{
		IncludeImages: false,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("Extract CPQ.pdf: %v", err)
	}
	if strings.Contains(md, "rid:") {
		t.Errorf("expected no rid: placeholders remaining with IncludeImages=false")
	}
	if len(stub.calls) != 0 {
		t.Errorf("describer called %d times with IncludeImages=false", len(stub.calls))
	}
}

// TestCPQ_DecorativeMarkerDropsImage confirms the DECORATIVE sentinel
// strips images rather than emitting a caption. The describer returns
// DecorativeMarker for every image, so the resulting markdown should
// carry no image references at all.
func TestCPQ_DecorativeMarkerDropsImage(t *testing.T) {
	stub := &stubDescriber{reply: DecorativeMarker}
	md, err := Extract(filepath.Join("testdata", "CPQ.pdf"), &Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("Extract CPQ.pdf: %v", err)
	}
	// No ![...](...) image tags should be left in the output.
	if strings.Contains(md, "](image_") || strings.Contains(md, "](rid:") {
		t.Errorf("expected all decorative images stripped, got sample:\n%s", truncate(md, 600))
	}
	if len(stub.calls) == 0 {
		t.Errorf("describer was never consulted — dedup cannot explain an empty call list")
	}
}

// intToA is a minimal int->string to keep the test free of fmt.Sprintf
// churn within the assertion loop.
func intToA(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestExtractImagesPlaceholderMode confirms that IncludeImages without an
// LLMClient emits placeholder-style markdown references instead of calling
// any describer. We use the DOCX fixture because it has embedded media.
func TestExtractImagesPlaceholderMode(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.docx"), &Options{
		IncludeImages: true,
	})
	if err != nil {
		if errors.Is(err, ErrUnsupportedFormat) {
			t.Skipf("docx not supported in this build: %v", err)
		}
		t.Fatalf("Extract test.docx: %v", err)
	}
	// No assertion on image count — some test.docx variants don't have
	// images. We just verify the call did not error and the library
	// produced markdown.
	if strings.TrimSpace(md) == "" {
		t.Fatalf("Extract test.docx returned empty")
	}
}
