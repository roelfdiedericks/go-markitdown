package docconv

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden regenerates golden-file fixtures when set.
var updateGolden = flag.Bool("update", false, "regenerate golden files under testdata/golden")

// TestExtractHTML covers the non-fitz HTML path.
func TestExtractHTML(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.html"), nil)
	if err != nil {
		t.Fatalf("Extract test.html: %v", err)
	}
	if strings.TrimSpace(md) == "" {
		t.Fatalf("Extract test.html returned empty")
	}
	if !strings.Contains(md, "Hello, markdown") {
		t.Errorf("expected title text in markdown, got: %q", firstLines(md, 5))
	}
}

// TestExtractText covers the FormatText path.
func TestExtractText(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.txt"), nil)
	if err != nil {
		t.Fatalf("Extract test.txt: %v", err)
	}
	if !strings.Contains(md, "plain-text fixture") {
		t.Errorf("expected fixture marker in text output, got: %q", firstLines(md, 3))
	}
	if !strings.Contains(md, "café") {
		t.Errorf("UTF-8 content was stripped: %q", firstLines(md, 3))
	}
}

// TestExtractXLSX covers the excelize path.
func TestExtractXLSX(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.xlsx"), nil)
	if err != nil {
		t.Fatalf("Extract test.xlsx: %v", err)
	}
	if strings.TrimSpace(md) == "" {
		t.Fatalf("Extract test.xlsx returned empty")
	}
}

// TestExtractUnsupported confirms images return ErrUnsupportedFormat via the
// Extract path (we exercise FormatImage explicitly since we don't ship an
// image fixture).
func TestExtractImageFormatUnsupported(t *testing.T) {
	_, err := extract(t.Context(), []byte{1, 2, 3}, FormatImage, Options{})
	if err != ErrUnsupportedFormat {
		t.Fatalf("expected ErrUnsupportedFormat, got %v", err)
	}
}

// TestExtractMetadata verifies that IncludeMetadata renders YAML
// front-matter. We use the text fixture for speed; the front-matter path is
// format-agnostic.
func TestExtractMetadata(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.txt"), &Options{IncludeMetadata: true})
	if err != nil {
		t.Fatalf("Extract test.txt: %v", err)
	}
	if !strings.HasPrefix(md, "---\n") {
		t.Errorf("expected YAML front-matter prefix, got: %q", firstLines(md, 3))
	}
	if !strings.Contains(md, "format: text") {
		t.Errorf("expected 'format: text' key in front-matter, got: %q", firstLines(md, 6))
	}
}

// TestGoldenHTML writes/compares a deterministic golden file for the HTML
// fixture. Run `go test -update` to regenerate after intentional changes.
func TestGoldenHTML(t *testing.T) {
	md, err := Extract(filepath.Join("testdata", "test.html"), nil)
	if err != nil {
		t.Fatalf("Extract test.html: %v", err)
	}
	checkGolden(t, "test.html.md", md)
}

// checkGolden compares output against testdata/golden/<name>. When -update
// is passed, the file is written (or created) instead of compared.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("golden %s does not exist; run `go test -update` to create", path)
			return
		}
		t.Fatalf("read golden %s: %v", path, err)
	}
	if string(want) != got {
		t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, string(want))
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
