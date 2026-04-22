package ooxml

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
)

// buildFakeDocx constructs an in-memory zip that mimics a DOCX with a single
// embedded image and a non-image file. Useful for unit-level tests that
// don't depend on real OOXML fixtures.
func buildFakeDocx(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	add := func(name string, body []byte) {
		fh := &zip.FileHeader{Name: name, Method: zip.Store}
		wf, err := w.CreateHeader(fh)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := io.Copy(wf, bytes.NewReader(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	add("[Content_Types].xml", []byte(`<?xml version="1.0"?><Types/>`))
	add("word/document.xml", []byte(`<?xml version="1.0"?><doc/>`))
	add("word/media/image1.png", []byte("\x89PNG\r\n\x1a\nfake-png"))
	add("word/media/notes.txt", []byte("not-an-image"))

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractImages(t *testing.T) {
	data := buildFakeDocx(t)
	imgs, err := ExtractImages(data, "docx")
	if err != nil {
		t.Fatalf("ExtractImages: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %d", len(imgs))
	}
	img := imgs[0]
	if img.Extension != ".png" {
		t.Errorf("ext = %s, want .png", img.Extension)
	}
	if img.MimeType != "image/png" {
		t.Errorf("mime = %s, want image/png", img.MimeType)
	}
	if !bytes.Contains(img.Data, []byte("fake-png")) {
		t.Errorf("image data missing expected marker")
	}
}

func TestExtractImagesEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	_ = w.Close()
	imgs, err := ExtractImages(buf.Bytes(), "docx")
	if err != nil {
		t.Fatalf("ExtractImages: %v", err)
	}
	if len(imgs) != 0 {
		t.Fatalf("expected 0 images, got %d", len(imgs))
	}
}
