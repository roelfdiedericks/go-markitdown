package docconv

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"testing"
)

// stubDescriber captures every DescribeImage call for assertion. Shared
// across backend tests because every backend exercises the same
// ImageDescriber hook; duplicating this per-file previously forced
// fitz_doc_test.go to keep its !nofitz build tag on the helper and
// broke -tags nofitz test builds for xlsx/html/pptx tests.
type stubDescriber struct {
	calls []struct {
		mime   string
		prompt string
		bytes  int
	}
	reply string
	err   error
}

func (s *stubDescriber) DescribeImage(_ context.Context, img []byte, mime, prompt string) (string, error) {
	s.calls = append(s.calls, struct {
		mime   string
		prompt string
		bytes  int
	}{mime: mime, prompt: prompt, bytes: len(img)})
	if s.err != nil {
		return "", s.err
	}
	return s.reply, nil
}

// mustRead reads a testdata file and fails the test on error.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// buildBrokenDocx constructs a zip with DOCX-shaped entries but garbage
// inside word/document.xml, forcing fumiama/go-docx's parser to fail. Used
// to exercise the fumiama -> fitz fallback path.
func buildBrokenDocx(t *testing.T) []byte {
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
	add("word/document.xml", []byte(`<not-a-real-document>this will not unmarshal as w:document`))
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
