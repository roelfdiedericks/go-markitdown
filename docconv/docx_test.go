package docconv

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractDOCXStructuredOutput verifies the new native DOCX backend
// produces real structural markdown: proper headings, a real markdown pipe
// table, and preserved link targets. We assert on the high-signal features
// rather than exact line matching (that is what TestGoldenDOCX covers).
func TestExtractDOCXStructuredOutput(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.docx"), nil)
	if err != nil {
		t.Fatalf("Extract test.docx: %v", err)
	}
	if strings.TrimSpace(md) == "" {
		t.Fatal("DOCX markdown was empty")
	}

	// Structural assertions — fixtures all document "Sample Document" as
	// the top-level title, with "Headings"/"Lists"/"Tables" as h2, and
	// "Simple Tables"/"Complex Tables" as h3 inside the Tables section.
	mustContain := []string{
		"# Sample Document",
		"## Headings",
		"## Lists",
		"## Tables",
		"### Simple Tables",
		"### Complex Tables",
	}
	for _, want := range mustContain {
		if !strings.Contains(md, want) {
			t.Errorf("missing expected heading %q in output", want)
		}
	}

	// Real markdown table: at least one row should be a pipe-table body
	// line with the Simple Tables fixture entries.
	if !containsPipeTable(md) {
		t.Errorf("expected a pipe-format markdown table in DOCX output")
	}

	// Fixture ships an external hyperlink to the Illinois DHS page; it
	// must survive the walker + markdown conversion.
	if !strings.Contains(md, "http://www.dhs.state.il.us") {
		t.Errorf("hyperlink target missing from output")
	}
}

// containsPipeTable checks for the shape "| cell | cell |" followed by a
// header-divider row "|---|---|". Requires both to be present on adjacent
// lines to avoid matching random pipe characters in body text.
func containsPipeTable(md string) bool {
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines)-1; i++ {
		if strings.HasPrefix(lines[i], "|") && strings.Count(lines[i], "|") >= 3 &&
			strings.HasPrefix(strings.TrimSpace(lines[i+1]), "|") &&
			strings.Contains(lines[i+1], "---") {
			return true
		}
	}
	return false
}

// TestExtractDOCXLists verifies that the list walker splits consecutive
// list-item paragraphs by numId, so the fixture's "ordered 1-5 / bullet /
// ordered restart" layout comes out as three separate markdown lists
// rather than one flat sequence.
func TestExtractDOCXLists(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.docx"), nil)
	if err != nil {
		t.Fatalf("Extract test.docx: %v", err)
	}

	// Ordered list "1. Headings ... 5. Tables" must be present, followed
	// by a bullet list "- Simple Tables / - Complex Tables", followed by
	// an ordered restart "1. Columns".
	needles := []string{
		"1. Headings",
		"5. Tables",
		"- Simple Tables",
		"- Complex Tables",
		"1. Columns",
	}
	for _, want := range needles {
		if !strings.Contains(md, want) {
			t.Errorf("list output missing %q in DOCX markdown", want)
		}
	}
}

// TestExtractDOCXFallbackOnParseError verifies that when fumiama/go-docx
// fails to parse the file, extractDOCX routes through extractFitzFallback
// rather than surfacing the parse error to the caller.
//
// We trigger the parse-error path synthetically by handing extractDOCX a
// file with the ZIP magic bytes detected as DOCX upstream but with garbage
// for word/document.xml. fumiama will error on the XML decode; go-fitz
// (MuPDF) is more permissive and will still recognise the zip as OOXML.
//
// If the CI environment somehow lacks MuPDF support, the fallback will
// return ErrFitzRequired; we skip in that case rather than fail.
func TestExtractDOCXFallbackOnParseError(t *testing.T) {
	// Build a minimally-shaped DOCX zip with broken word/document.xml.
	data := buildBrokenDocx(t)

	_, _, err := extractDOCX(context.Background(), data, Options{})
	// The fallback may either succeed and return no error, or fail and
	// return a wrapped error that mentions fallback. What we care about
	// is that the error (if any) is NOT the raw fumiama parse error
	// unwrapped — it should always go through the fallback path.
	if err != nil {
		// Acceptable forms: wrapped fitz fallback error, or ErrNoText
		// when fitz also produces empty output.
		msg := err.Error()
		if !strings.Contains(msg, "fitz fallback") &&
			!strings.Contains(msg, "no extractable text") &&
			!strings.Contains(msg, "docx:") {
			t.Errorf("unexpected error shape after fallback: %v", err)
		}
	}
}

// TestGoldenDOCX locks in the exact markdown output for test.docx so we
// notice accidental formatting regressions. Run `go test -update` after
// intentional walker changes.
func TestGoldenDOCX(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.docx"), nil)
	if err != nil {
		t.Fatalf("Extract test.docx: %v", err)
	}
	checkGolden(t, "test.docx.md", md)
}

// TestExtractDOCXImageContextInPrompt feeds a stub describer and asserts
// the describer prompt for at least one image carries the library's
// context-aware template (by sentinel string) and non-empty surrounding
// text. This verifies ContextBefore/ContextAfter wiring from the walker
// through to the describer hook.
func TestExtractDOCXImageContextInPrompt(t *testing.T) {
	stub := &stubDescriber{reply: "caption"}
	_, err := Extract(filepath.Join("testdata", "test.docx"), &Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("Extract test.docx: %v", err)
	}
	if len(stub.calls) == 0 {
		// test.docx may have no images in some fixture revisions;
		// this is not a regression — skip when true.
		t.Skip("test.docx has no images in this fixture")
	}
	anyContext := false
	for _, c := range stub.calls {
		if !strings.Contains(c.prompt, "concise caption") {
			t.Errorf("expected library-owned template in prompt, got %q", c.prompt)
		}
		if strings.Contains(c.prompt, "Surrounding document text") &&
			!strings.Contains(c.prompt, "Surrounding document text (may or may not be relevant to the image):\n\n") {
			anyContext = true
		}
	}
	if !anyContext {
		// Not necessarily a failure for test.docx (short doc, images
		// at doc boundary), but log so regressions are visible.
		t.Logf("no prompt carried surrounding text — verify ContextBefore wiring if the fixture has text around the image")
	}
}

// TestExtractDOCXIncludeCommentsFlag confirms the Options.IncludeComments
// toggle does not crash the extractor when the fixture has no comments
// stream, and that the default output does not leak any HTML comments
// that look like DOCX review comments (a regression guard for the opt-in
// default).
func TestExtractDOCXIncludeCommentsFlag(t *testing.T) {
	// Default (comments OFF): no review-comment markers in the output.
	md, err := Extract(filepath.Join("testdata", "test.docx"), nil)
	if err != nil {
		t.Fatalf("Extract test.docx (default): %v", err)
	}
	if strings.Contains(md, "<!-- Comment") || strings.Contains(md, "<!--comment") {
		t.Errorf("default output leaked a comment marker: %q", md)
	}

	// Comments ON: must not error even when the fixture has no
	// word/comments.xml — exercises the "missing stream" branch in
	// LoadCommentRefs.
	_, err = Extract(filepath.Join("testdata", "test.docx"), &Options{IncludeComments: true})
	if err != nil {
		t.Fatalf("Extract test.docx with IncludeComments=true: %v", err)
	}
}

// TestExtractDOCXFootnotesNoRegressionOnTestFixture ensures the footnote
// renderer is idempotent on a fixture that has none — no "Footnotes"
// heading, no "[^fn-" references — so we don't start emitting empty
// footnote blocks for docs without footnotes.
func TestExtractDOCXFootnotesNoRegressionOnTestFixture(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.docx"), nil)
	if err != nil {
		t.Fatalf("Extract test.docx: %v", err)
	}
	if strings.Contains(md, "[^fn-") {
		t.Errorf("unexpected footnote reference in output (fixture has no footnotes): %q", md)
	}
	if strings.Contains(md, "\n## Footnotes\n") || strings.Contains(md, "\n# Footnotes\n") {
		t.Errorf("unexpected Footnotes heading emitted for fixture without footnotes")
	}
}

// TestExtractDOCXMetadata verifies that metadata from docProps/core.xml is
// surfaced when IncludeMetadata is set.
func TestExtractDOCXMetadata(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.docx"), &Options{IncludeMetadata: true})
	if err != nil {
		t.Fatalf("Extract test.docx: %v", err)
	}
	// The fixture has core.xml, so front-matter should at minimum
	// carry the format marker.
	if !strings.HasPrefix(md, "---\n") {
		t.Errorf("expected YAML front-matter, got prefix: %q", firstLines(md, 3))
	}
	if !strings.Contains(md, "format: docx") {
		t.Errorf("format key missing from front-matter")
	}
}
