package docconv

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// htmlTinyPNG returns bytes for a small PNG we can embed in a data: URI
// for HTML tests. Uses image/png rather than hardcoded bytes so excelize
// and HTML captures agree on what "a valid PNG" looks like.
func htmlTinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

// buildHTMLWithDataURI wraps the PNG bytes in a valid data: URI inside an
// <img> tag, surrounded by enough prose that go-readability will keep the
// article rather than rejecting it as chrome.
func buildHTMLWithDataURI(t *testing.T, alt string) string {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(htmlTinyPNG(t))
	return `<!doctype html><html><head><title>A Post</title></head><body>
<article>
<h1>A Post</h1>
<p>Here is some introductory prose to keep go-readability happy. The article continues below with an embedded image illustrating the quarterly trend for our sales team, which is important context for anyone reading this report.</p>
<p><img src="data:image/png;base64,` + encoded + `" alt="` + alt + `"/></p>
<p>Following the chart we summarise what the reader just saw in a couple of paragraphs so the article has enough density for the readability heuristic to keep it.</p>
<p>More explanatory prose: do not trim this out — the readability score depends on having several paragraphs of non-trivial text surrounding the image element under test.</p>
</article>
</body></html>`
}

// TestExtractHTMLDataURIRewrittenAndCaptioned exercises the full data URI
// capture pipeline: decode the base64 payload, register as an image, run
// the describer, and splice the caption back into the markdown body.
func TestExtractHTMLDataURIRewrittenAndCaptioned(t *testing.T) {
	stub := &stubDescriber{reply: "a colourful chart"}
	html := buildHTMLWithDataURI(t, "Author alt here")

	md, _, err := extractHTML(context.Background(), []byte(html), Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("extractHTML: %v", err)
	}
	if strings.Contains(md, "data:image") || strings.Contains(md, "base64,") {
		t.Errorf("raw data URI leaked into output: %q", md)
	}
	if !strings.Contains(md, "a colourful chart") {
		t.Errorf("describer caption missing from output: %q", md)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected one describer call, got %d", len(stub.calls))
	}
	if !strings.Contains(stub.calls[0].prompt, "Author alt here") {
		t.Errorf("author alt not surfaced to describer: %q", stub.calls[0].prompt)
	}
}

// TestExtractHTMLRealURLUntouched verifies that ordinary http(s) images
// keep their src intact — the backend must never rewrite remote URLs
// (we deliberately do not fetch them) and must never treat them as data:
// URIs.
func TestExtractHTMLRealURLUntouched(t *testing.T) {
	html := `<!doctype html><html><body>
<article>
<h1>Real URL test</h1>
<p>Some leading prose to keep readability happy. Some leading prose to keep readability happy. Some leading prose to keep readability happy.</p>
<p><img src="https://example.com/foo.png" alt="remote"/></p>
<p>Following prose with more content to ensure we keep the article above the readability cutoff and the image link survives the conversion intact.</p>
</article>
</body></html>`
	stub := &stubDescriber{reply: "should-not-fire"}

	md, _, err := extractHTML(context.Background(), []byte(html), Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("extractHTML: %v", err)
	}
	if !strings.Contains(md, "https://example.com/foo.png") {
		t.Errorf("real URL was rewritten or lost: %q", md)
	}
	if len(stub.calls) != 0 {
		t.Errorf("describer should not fire for remote images, got %d calls", len(stub.calls))
	}
}

// TestCaptureHTMLDataImagesDedup checks that multiple inline occurrences
// of the same payload collapse to one extractedImage entry — dedup
// happens at capture time, not just in the post-pass.
func TestCaptureHTMLDataImagesDedup(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(htmlTinyPNG(t))
	html := `<img src="data:image/png;base64,` + encoded + `" alt="one"/>
<p>prose</p>
<img src="data:image/png;base64,` + encoded + `" alt="two"/>`

	images := map[string]extractedImage{}
	out := captureHTMLDataImages(html, images)
	if len(images) != 1 {
		t.Fatalf("expected 1 deduped image, got %d", len(images))
	}
	if strings.Count(out, `src="rid:`) != 2 {
		t.Errorf("expected both <img> tags rewritten, got:\n%s", out)
	}
}
