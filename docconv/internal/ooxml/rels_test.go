package ooxml

import (
	"archive/zip"
	"bytes"
	"io"
	"testing"
)

// buildFakeSlideZip constructs a minimal PPTX-shaped zip with one slide, a
// rels part that references a media file, and the media bytes themselves.
func buildFakeSlideZip(t *testing.T) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	add := func(name string, body []byte) {
		wf, err := w.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := io.Copy(wf, bytes.NewReader(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	add("ppt/slides/slide1.xml", []byte(`<?xml version="1.0"?><slide/>`))
	add("ppt/slides/_rels/slide1.xml.rels", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com" TargetMode="External"/>
</Relationships>`))
	add("ppt/media/image1.png", []byte("\x89PNG\r\n\x1a\nfake"))

	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	return zr
}

func TestParseRelsResolvesRelativeTarget(t *testing.T) {
	zr := buildFakeSlideZip(t)
	rels, err := ParseRels(zr, "ppt/slides/slide1.xml")
	if err != nil {
		t.Fatalf("ParseRels: %v", err)
	}
	got, ok := rels["rId1"]
	if !ok {
		t.Fatalf("rId1 missing from rels map: %#v", rels)
	}
	if got != "ppt/media/image1.png" {
		t.Errorf("rId1 target = %q, want ppt/media/image1.png", got)
	}
}

func TestParseRelsKeepsExternalTargetUnchanged(t *testing.T) {
	zr := buildFakeSlideZip(t)
	rels, err := ParseRels(zr, "ppt/slides/slide1.xml")
	if err != nil {
		t.Fatalf("ParseRels: %v", err)
	}
	got, ok := rels["rId2"]
	if !ok {
		t.Fatalf("rId2 missing from rels map: %#v", rels)
	}
	if got != "https://example.com" {
		t.Errorf("rId2 target = %q, want https://example.com", got)
	}
}

func TestParseRelsMissingReturnsEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	_ = w.Close()
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}
	rels, err := ParseRels(zr, "ppt/slides/slide1.xml")
	if err != nil {
		t.Fatalf("ParseRels: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("expected empty rels, got %#v", rels)
	}
}

func TestReadMediaBytes(t *testing.T) {
	zr := buildFakeSlideZip(t)
	data, err := ReadMediaBytes(zr, "ppt/media/image1.png")
	if err != nil {
		t.Fatalf("ReadMediaBytes: %v", err)
	}
	if !bytes.Contains(data, []byte("fake")) {
		t.Errorf("media bytes missing expected marker: %q", data)
	}
}

func TestReadMediaBytesMissing(t *testing.T) {
	zr := buildFakeSlideZip(t)
	if _, err := ReadMediaBytes(zr, "ppt/media/nope.png"); err == nil {
		t.Fatal("expected error for missing media, got nil")
	}
}

func TestSiblingRelsPath(t *testing.T) {
	cases := map[string]string{
		"word/document.xml":     "word/_rels/document.xml.rels",
		"ppt/slides/slide1.xml": "ppt/slides/_rels/slide1.xml.rels",
	}
	for in, want := range cases {
		got := siblingRelsPath(in)
		if got != want {
			t.Errorf("siblingRelsPath(%q) = %q, want %q", in, got, want)
		}
	}
}
