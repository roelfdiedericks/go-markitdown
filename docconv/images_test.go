package docconv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingDescriber captures every DescribeImage invocation so tests can
// assert on call count, prompt content, and byte payload. reply is
// returned for every call (unless err is set) — letting tests simulate
// decorative markers, empty responses, and rich captions just by changing
// one field.
type recordingDescriber struct {
	calls []recordedCall
	reply string
	err   error
}

type recordedCall struct {
	mime   string
	prompt string
	data   []byte
}

func (r *recordingDescriber) DescribeImage(_ context.Context, data []byte, mime, prompt string) (string, error) {
	r.calls = append(r.calls, recordedCall{mime: mime, prompt: prompt, data: append([]byte(nil), data...)})
	if r.err != nil {
		return "", r.err
	}
	return r.reply, nil
}

func TestReplaceImagePlaceholders_DecorativeStripsPlaceholder(t *testing.T) {
	describer := &recordingDescriber{reply: DecorativeMarker}
	md := "Before ![](rid:abc) after."
	images := map[string]extractedImage{
		"abc": {
			ID:        "abc",
			Index:     0,
			Data:      []byte("fake-bytes"),
			MimeType:  "image/png",
			Extension: ".png",
		},
	}
	out, err := replaceImagePlaceholders(context.Background(), md, images, Options{
		IncludeImages: true,
		LLMClient:     describer,
	})
	if err != nil {
		t.Fatalf("replaceImagePlaceholders: %v", err)
	}
	if strings.Contains(out, "rid:abc") || strings.Contains(out, "![") {
		t.Fatalf("expected decorative image stripped, got %q", out)
	}
	if len(describer.calls) != 1 {
		t.Fatalf("expected exactly 1 describer call, got %d", len(describer.calls))
	}
}

func TestReplaceImagePlaceholders_DedupSingleDescriberCall(t *testing.T) {
	describer := &recordingDescriber{reply: "a chart of quarterly revenue"}
	md := "![](rid:x) then ![](rid:x) and again ![](rid:x)."
	images := map[string]extractedImage{
		"x": {
			ID: "x", Index: 0, Data: []byte("payload"),
			MimeType: "image/png", Extension: ".png",
		},
	}
	out, err := replaceImagePlaceholders(context.Background(), md, images, Options{
		IncludeImages: true,
		LLMClient:     describer,
	})
	if err != nil {
		t.Fatalf("replaceImagePlaceholders: %v", err)
	}
	if strings.Count(out, "a chart of quarterly revenue") != 3 {
		t.Fatalf("expected 3 rendered refs, got %q", out)
	}
	if len(describer.calls) != 1 {
		t.Fatalf("expected 1 describer call across duplicates, got %d", len(describer.calls))
	}
}

func TestReplaceImagePlaceholders_VerbatimPromptWhenUserSupplied(t *testing.T) {
	describer := &recordingDescriber{reply: "caption"}
	md := "![](rid:a)"
	images := map[string]extractedImage{
		"a": {
			ID: "a", Index: 0, Data: []byte("p"),
			MimeType:      "image/png",
			Extension:     ".png",
			ContextBefore: "ignored before",
			ContextAfter:  "ignored after",
			AuthorAltText: "ignored author",
		},
	}
	custom := "Transcribe every word of text in this image."
	if _, err := replaceImagePlaceholders(context.Background(), md, images, Options{
		IncludeImages:   true,
		LLMClient:       describer,
		DescribePrompt:  custom,
	}); err != nil {
		t.Fatalf("replaceImagePlaceholders: %v", err)
	}
	if len(describer.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(describer.calls))
	}
	if describer.calls[0].prompt != custom {
		t.Fatalf("expected verbatim user prompt, got %q", describer.calls[0].prompt)
	}
}

func TestReplaceImagePlaceholders_DefaultPromptContainsContextAndAlt(t *testing.T) {
	describer := &recordingDescriber{reply: "caption"}
	md := "![](rid:a)"
	images := map[string]extractedImage{
		"a": {
			ID: "a", Index: 0, Data: []byte("p"),
			MimeType:      "image/png",
			Extension:     ".png",
			ContextBefore: "PREFIX-SENTINEL before the image",
			ContextAfter:  "SUFFIX-SENTINEL after the image",
			AuthorAltText: "AUTHOR-ALT-SENTINEL",
		},
	}
	if _, err := replaceImagePlaceholders(context.Background(), md, images, Options{
		IncludeImages: true,
		LLMClient:     describer,
	}); err != nil {
		t.Fatalf("replaceImagePlaceholders: %v", err)
	}
	prompt := describer.calls[0].prompt
	for _, want := range []string{"PREFIX-SENTINEL", "SUFFIX-SENTINEL", "AUTHOR-ALT-SENTINEL"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", want, prompt)
		}
	}
}

func TestReplaceImagePlaceholders_DescriberErrorFallsBackToPlaceholderLabel(t *testing.T) {
	describer := &recordingDescriber{err: errors.New("llm boom")}
	md := "![](rid:a)"
	images := map[string]extractedImage{
		"a": {
			ID: "a", Index: 0, Data: []byte("p"),
			MimeType: "image/png", Extension: ".png",
		},
	}
	out, err := replaceImagePlaceholders(context.Background(), md, images, Options{
		IncludeImages: true,
		LLMClient:     describer,
	})
	if err != nil {
		t.Fatalf("replaceImagePlaceholders should absorb describer errors, got %v", err)
	}
	if !strings.Contains(out, "Image: image_") {
		t.Fatalf("expected fallback placeholder label, got %q", out)
	}
}

func TestReplaceImagePlaceholders_IncludeImagesFalseStripsEverything(t *testing.T) {
	describer := &recordingDescriber{reply: "should-not-be-called"}
	md := "![](rid:a) middle ![](rid:b)"
	images := map[string]extractedImage{
		"a": {ID: "a", Data: []byte("1"), MimeType: "image/png", Extension: ".png"},
		"b": {ID: "b", Data: []byte("2"), MimeType: "image/png", Extension: ".png"},
	}
	out, err := replaceImagePlaceholders(context.Background(), md, images, Options{
		IncludeImages: false,
		LLMClient:     describer,
	})
	if err != nil {
		t.Fatalf("replaceImagePlaceholders: %v", err)
	}
	if strings.Contains(out, "rid:") || strings.Contains(out, "![") {
		t.Fatalf("expected every placeholder stripped, got %q", out)
	}
	if len(describer.calls) != 0 {
		t.Fatalf("describer must not be called when IncludeImages=false, got %d calls", len(describer.calls))
	}
}

func TestReplaceImagePlaceholders_UnknownIDStripsPlaceholder(t *testing.T) {
	md := "before ![](rid:unknown) after"
	out, err := replaceImagePlaceholders(context.Background(), md, map[string]extractedImage{}, Options{
		IncludeImages: true,
	})
	if err != nil {
		t.Fatalf("replaceImagePlaceholders: %v", err)
	}
	if strings.Contains(out, "rid:unknown") {
		t.Fatalf("expected unknown id stripped, got %q", out)
	}
}

func TestReplaceImagePlaceholders_ImageDirByteParity(t *testing.T) {
	dir := t.TempDir()
	payload := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	images := map[string]extractedImage{
		"only": {
			ID: "only", Index: 0, Data: payload,
			MimeType: "image/png", Extension: ".png",
		},
	}
	_, err := replaceImagePlaceholders(context.Background(), "![](rid:only)", images, Options{
		IncludeImages: true,
		ImageDir:      dir,
	})
	if err != nil {
		t.Fatalf("replaceImagePlaceholders: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "image_000.png"))
	if err != nil {
		t.Fatalf("read written image: %v", err)
	}
	if !bytesEqual(got, payload) {
		t.Fatalf("bytes on disk differ from source payload")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSanitizeAltPreservesEmphasisDropsBrackets(t *testing.T) {
	in := "A **bold** and `code` caption with [brackets]\nand a newline"
	out := sanitizeAlt(in)
	if !strings.Contains(out, "**bold**") || !strings.Contains(out, "`code`") {
		t.Fatalf("expected emphasis preserved, got %q", out)
	}
	if strings.ContainsAny(out, "[]") {
		t.Fatalf("expected brackets stripped, got %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("expected newlines collapsed, got %q", out)
	}
}

func TestJoinContextBehaviour(t *testing.T) {
	if got := joinContext("", ""); got != "" {
		t.Fatalf("empty+empty: %q", got)
	}
	if got := joinContext("before", ""); got != "before" {
		t.Fatalf("before-only: %q", got)
	}
	if got := joinContext("", "after"); got != "after" {
		t.Fatalf("after-only: %q", got)
	}
	if got := joinContext("a", "b"); !strings.Contains(got, "[…image…]") {
		t.Fatalf("expected ellipsis marker, got %q", got)
	}
}
