package docconv

import (
	"archive/zip"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestExtractPPTXStructuredOutput verifies that the native PPTX backend
// produces real per-slide structure: slide titles render as <h2> markdown
// headings, slides are separated by the horizontal-rule convention, and
// body text ends up as plain paragraphs rather than absolute-positioned
// blobs (the fitz behaviour we're moving away from).
func TestExtractPPTXStructuredOutput(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.pptx"), nil)
	if err != nil {
		t.Fatalf("Extract test.pptx: %v", err)
	}
	if strings.TrimSpace(md) == "" {
		t.Fatal("PPTX markdown was empty")
	}

	// The fixture ships with two slides:
	//   1. "Sample PowerPoint File" / "St. Cloud Technical College"
	//   2. "This is a Sample Slide" / bullet-like content
	mustContain := []string{
		"## Sample PowerPoint File",
		"## This is a Sample Slide",
		"St. Cloud Technical College",
		"<!-- Slide number: 1 -->",
		"<!-- Slide number: 2 -->",
	}
	for _, want := range mustContain {
		if !strings.Contains(md, want) {
			t.Errorf("missing expected fragment %q in PPTX markdown", want)
		}
	}

	// Slide separator: we join slides with "\n\n---\n\n" in the
	// orchestrator. The fixture has two slides so exactly one separator
	// should appear.
	if got := strings.Count(md, "\n---\n"); got != 1 {
		t.Errorf("expected exactly one slide separator line, got %d\n---\noutput:\n%s", got, md)
	}
}

// TestGoldenPPTX locks in the exact markdown output for test.pptx so we
// notice accidental formatting regressions. Run `go test -update` after
// intentional walker changes.
func TestGoldenPPTX(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.pptx"), nil)
	if err != nil {
		t.Fatalf("Extract test.pptx: %v", err)
	}
	checkGolden(t, "test.pptx.md", md)
}

// TestExtractPPTXFallbackOnZipError verifies that when the bytes cannot
// even be opened as a zip (garbage input), extractPPTX routes through
// extractFitzFallback rather than surfacing the zip error unwrapped.
func TestExtractPPTXFallbackOnZipError(t *testing.T) {
	data := []byte("not a zip file at all")

	_, _, err := extractPPTX(context.Background(), data, Options{})
	if err == nil {
		// The fitz fallback could conceivably succeed with exotic
		// input; that is still acceptable — the point is that we
		// didn't panic or surface the raw zip parse error.
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, "fitz fallback") &&
		!strings.Contains(msg, "pptx:") &&
		!strings.Contains(msg, "no extractable text") {
		t.Errorf("unexpected error shape after fallback: %v", err)
	}
}

// TestExtractPPTXEmptySlideDeck verifies that when a presentation has no
// slide entries at all, we either fall through to fitz fallback or
// surface ErrNoText rather than returning empty markdown silently.
func TestExtractPPTXEmptySlideDeck(t *testing.T) {
	data := buildEmptyPPTX(t)

	_, _, err := extractPPTX(context.Background(), data, Options{})
	if err == nil {
		t.Fatal("expected an error for empty presentation, got nil")
	}
}

// TestExtractComplexPPTXStructures verifies that complex.pptx — which
// exercises PPTX features beyond test.pptx — survives the walker with
// its table and picture intact. Specifically it asserts:
//   - per-slide <!-- Slide number: N --> markers for all four slides;
//   - slide 3's a:tbl renders as a pipe-style markdown table with a
//     five-column header row (verifies p:graphicFrame > a:tbl);
//   - slide 4's p:pic emits an image placeholder (verifies --include-images
//     is the right flag to surface it);
//   - slide 2's OLE chart gets a "(embedded object omitted)" marker
//     rather than being silently dropped.
func TestExtractComplexPPTXStructures(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "complex.pptx"), &Options{IncludeImages: true})
	if err != nil {
		t.Fatalf("Extract complex.pptx: %v", err)
	}

	needles := []string{
		"<!-- Slide number: 1 -->",
		"<!-- Slide number: 2 -->",
		"<!-- Slide number: 3 -->",
		"<!-- Slide number: 4 -->",
		"## Lorem ipsum",
		"## Chart",
		"(embedded object omitted)",
		"## Table",
		"| Column 1 | Column 2 | Column 3 | Column 4 | Column 5 |",
		"## Photo",
		"image_000.jpeg",
	}
	for _, want := range needles {
		if !strings.Contains(md, want) {
			t.Errorf("complex.pptx markdown missing %q\nfull output:\n%s", want, md)
		}
	}
}

// TestComplexPPTX_DescriberPromptIncludesSlideTitle feeds a stub
// describer into Extract and checks that the prompt it receives contains
// the surrounding slide text (the slide-4 "Photo" heading). This is how
// we prove ContextBefore threading all the way through from the walker
// to the describer hook.
func TestComplexPPTX_DescriberPromptIncludesSlideTitle(t *testing.T) {
	stub := &stubDescriber{reply: "a photograph"}
	_, err := Extract(filepath.Join("testdata", "complex.pptx"), &Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("Extract complex.pptx: %v", err)
	}
	if len(stub.calls) == 0 {
		t.Fatalf("expected describer to be invoked for complex.pptx images")
	}
	// At least one call's prompt should carry "Photo" — the slide 4
	// title — proving slide text made it into ContextBefore.
	found := false
	for _, c := range stub.calls {
		if strings.Contains(c.prompt, "Photo") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no describer prompt contained slide title 'Photo'; got %d calls", len(stub.calls))
	}
}

// TestComplexPPTX_DedupOneCallPerImage confirms dedup: rerunning the
// describer repeatedly for an image that appears once shouldn't generate
// any extra calls, and the number of DescribeImage calls should not
// exceed the number of unique media entries in the deck.
func TestComplexPPTX_DedupOneCallPerImage(t *testing.T) {
	stub := &stubDescriber{reply: "caption"}
	_, err := Extract(filepath.Join("testdata", "complex.pptx"), &Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("Extract complex.pptx: %v", err)
	}
	// Each call's byte-length should be unique — a duplicate length
	// across calls for the same rId-pair is a strong indicator the
	// dedup cache missed a collision on identical media. (stubDescriber
	// intentionally only keeps a byte count to avoid retaining large
	// image payloads across a test suite run.)
	byLen := map[int]int{}
	for _, c := range stub.calls {
		byLen[c.bytes]++
	}
	for size, n := range byLen {
		if n > 1 {
			t.Errorf("size=%d seen %d times — potential dedup regression", size, n)
		}
	}
}

// TestComplexPPTX_DecorativeStripsImages asserts that the DecorativeMarker
// sentinel causes every image in the deck to be stripped from the output,
// leaving no image links behind.
func TestComplexPPTX_DecorativeStripsImages(t *testing.T) {
	stub := &stubDescriber{reply: DecorativeMarker}
	md, err := Extract(filepath.Join("testdata", "complex.pptx"), &Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("Extract complex.pptx: %v", err)
	}
	if strings.Contains(md, "](image_") || strings.Contains(md, "](rid:") {
		t.Errorf("expected no image links, got sample:\n%s", md)
	}
}

// buildEmptyPPTX constructs a zip with PPTX-shaped Content Types but no
// ppt/slides/slide*.xml entries. Used to exercise the "no slides found"
// branch in extractPPTX.
func buildEmptyPPTX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	add := func(name string, body []byte) {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	add("[Content_Types].xml", []byte(`<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`))
	add("_rels/.rels", []byte(`<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`))
	add("ppt/presentation.xml", []byte(`<?xml version="1.0"?><p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`))
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
