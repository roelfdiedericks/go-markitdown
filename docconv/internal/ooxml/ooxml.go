// Package ooxml extracts embedded media from OOXML files (DOCX, XLSX, PPTX).
//
// go-fitz and excelize both handle text extraction beautifully, but neither
// surfaces embedded images as raw bytes — go-fitz because MuPDF's office
// readers emit rendered pages without asset streams, and excelize only
// exposes pictures that are explicitly anchored to cells. This package walks
// the zip directly to recover everything in word/media, ppt/media, and
// xl/media.
package ooxml

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// Image is a single extracted embedded image.
type Image struct {
	// Path is the full path within the OOXML zip (e.g. "word/media/image1.png").
	Path string

	// Data is the raw encoded image bytes.
	Data []byte

	// MimeType is the MIME type derived from the file extension.
	MimeType string

	// Extension is the file extension including the leading dot
	// (".png", ".jpeg", ".gif", ...).
	Extension string
}

// ExtractImages opens data as an OOXML zip and returns every image found in
// word/media/, ppt/media/, or xl/media/. Images are returned in lexical path
// order which keeps downstream numbering deterministic.
//
// format is a short identifier ("docx", "pptx", "xlsx") used purely for
// wrapping error messages; the extraction logic scans every known media
// directory regardless of the hint.
func ExtractImages(data []byte, format string) ([]Image, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%s: open zip: %w", format, err)
	}

	var out []Image
	for _, f := range zr.File {
		if !isMediaPath(f.Name) {
			continue
		}
		ext := strings.ToLower(path.Ext(f.Name))
		if !isSupportedImageExt(ext) {
			continue
		}
		body, rerr := readZipFile(f)
		if rerr != nil {
			return nil, fmt.Errorf("%s: read %s: %w", format, f.Name, rerr)
		}
		out = append(out, Image{
			Path:      f.Name,
			Data:      body,
			MimeType:  mimeForExt(ext),
			Extension: ext,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func isMediaPath(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "word/media/") ||
		strings.HasPrefix(n, "ppt/media/") ||
		strings.HasPrefix(n, "xl/media/")
}

func isSupportedImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".tif", ".tiff":
		return true
	}
	return false
}

func mimeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".webp":
		return "image/webp"
	case ".tif", ".tiff":
		return "image/tiff"
	}
	return "application/octet-stream"
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
