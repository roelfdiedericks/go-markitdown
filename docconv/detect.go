package docconv

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Format represents a document type.
type Format int

const (
	// FormatAuto asks the library to detect the format from extension or
	// magic bytes. Never returned by Detect.
	FormatAuto Format = iota
	// FormatPDF is an Adobe PDF document.
	FormatPDF
	// FormatDOCX is a Microsoft Word OOXML document.
	FormatDOCX
	// FormatXLSX is a Microsoft Excel OOXML spreadsheet.
	FormatXLSX
	// FormatPPTX is a Microsoft PowerPoint OOXML presentation.
	FormatPPTX
	// FormatEPUB is an EPUB ebook.
	FormatEPUB
	// FormatMOBI is a Mobipocket ebook.
	FormatMOBI
	// FormatHTML is an HTML document.
	FormatHTML
	// FormatText is plain text.
	FormatText
	// FormatImage is a standalone image. Extract returns
	// ErrUnsupportedFormat; callers should feed images to a vision
	// model directly rather than through this package.
	FormatImage
)

// String returns a short human-readable name for the format.
func (f Format) String() string {
	switch f {
	case FormatAuto:
		return "auto"
	case FormatPDF:
		return "pdf"
	case FormatDOCX:
		return "docx"
	case FormatXLSX:
		return "xlsx"
	case FormatPPTX:
		return "pptx"
	case FormatEPUB:
		return "epub"
	case FormatMOBI:
		return "mobi"
	case FormatHTML:
		return "html"
	case FormatText:
		return "text"
	case FormatImage:
		return "image"
	default:
		return "unknown"
	}
}

// Detect returns the format of a file based on extension and magic bytes.
// Magic-byte sniffing takes precedence over the extension when both are
// available. Returns ErrUnsupportedFormat when neither identifies a known
// format.
func Detect(path string) (Format, error) {
	f, err := os.Open(path)
	if err != nil {
		return FormatAuto, err
	}
	defer f.Close()

	// Try magic bytes first.
	buf := make([]byte, 4096)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return FormatAuto, err
	}
	buf = buf[:n]

	if fmt := detectFromMagic(buf); fmt != FormatAuto {
		return fmt, nil
	}

	// Fall back to extension.
	if fmt := detectFromExtension(path); fmt != FormatAuto {
		return fmt, nil
	}

	// Last resort: if the bytes look like valid UTF-8 text, call it text.
	if looksLikeText(buf) {
		return FormatText, nil
	}

	return FormatAuto, ErrUnsupportedFormat
}

// DetectReader returns the format from the first few KiB of a reader. The
// reader is wrapped with bufio.Reader and the peek is returned via the new
// reader, so callers should use the returned reader for subsequent work.
func DetectReader(r io.Reader) (Format, io.Reader, error) {
	br := bufio.NewReaderSize(r, 4096)
	buf, err := br.Peek(4096)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return FormatAuto, br, err
	}

	if fmt := detectFromMagic(buf); fmt != FormatAuto {
		return fmt, br, nil
	}
	if looksLikeText(buf) {
		return FormatText, br, nil
	}
	return FormatAuto, br, ErrUnsupportedFormat
}

// FromMIME returns the Format matching a MIME type, or FormatAuto if the MIME
// is not one docconv can extract. Parameters (anything after ';') are ignored
// so "text/plain; charset=utf-8" resolves the same as "text/plain". Matching
// is case-insensitive. Never panics on malformed input.
//
// Images (image/*) are intentionally unmapped: callers should send images
// directly to a vision model rather than through Extract, which returns
// ErrUnsupportedFormat for image inputs.
func FromMIME(mime string) Format {
	mime = strings.TrimSpace(mime)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch strings.ToLower(mime) {
	case "application/pdf":
		return FormatPDF
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return FormatDOCX
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return FormatXLSX
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return FormatPPTX
	case "application/epub+zip":
		return FormatEPUB
	case "application/x-mobipocket-ebook":
		return FormatMOBI
	case "text/html", "application/xhtml+xml":
		return FormatHTML
	case "text/plain", "text/markdown":
		return FormatText
	}
	return FormatAuto
}

// Supports reports whether docconv can extract content from the given MIME
// type. Equivalent to FromMIME(mime) != FormatAuto. Intended for callers
// that only need a yes/no answer (e.g. deciding whether to offer a "read
// this document" action in a UI).
func Supports(mime string) bool { return FromMIME(mime) != FormatAuto }

func detectFromExtension(path string) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return FormatPDF
	case ".docx":
		return FormatDOCX
	case ".xlsx":
		return FormatXLSX
	case ".pptx":
		return FormatPPTX
	case ".epub":
		return FormatEPUB
	case ".mobi", ".azw", ".azw3":
		return FormatMOBI
	case ".html", ".htm", ".xhtml":
		return FormatHTML
	case ".txt", ".text", ".md", ".markdown":
		return FormatText
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return FormatImage
	}
	return FormatAuto
}

// detectFromMagic returns a Format based on common magic-byte signatures.
// Returns FormatAuto when nothing matches.
func detectFromMagic(b []byte) Format {
	if len(b) < 4 {
		return FormatAuto
	}

	// PDF: "%PDF"
	if b[0] == 0x25 && b[1] == 0x50 && b[2] == 0x44 && b[3] == 0x46 {
		return FormatPDF
	}

	// MOBI: "BOOKMOBI" or "TEXtREAd" at offset 60.
	if len(b) >= 68 {
		if bytes.Equal(b[60:68], []byte("BOOKMOBI")) || bytes.Equal(b[60:68], []byte("TEXtREAd")) {
			return FormatMOBI
		}
	}

	// ZIP-based formats (DOCX/XLSX/PPTX/EPUB).
	// Local file header signature "PK\x03\x04".
	if b[0] == 0x50 && b[1] == 0x4B && b[2] == 0x03 && b[3] == 0x04 {
		if fmt := detectZipFormat(b); fmt != FormatAuto {
			return fmt
		}
	}

	// HTML: permissive sniff.
	if looksLikeHTML(b) {
		return FormatHTML
	}

	// Common image magic numbers.
	if len(b) >= 8 {
		// PNG
		if b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' {
			return FormatImage
		}
		// JPEG
		if b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
			return FormatImage
		}
		// GIF
		if b[0] == 'G' && b[1] == 'I' && b[2] == 'F' && b[3] == '8' {
			return FormatImage
		}
		// BMP
		if b[0] == 'B' && b[1] == 'M' {
			return FormatImage
		}
	}

	return FormatAuto
}

// detectZipFormat inspects enough of a zip archive to distinguish
// EPUB/DOCX/XLSX/PPTX from a generic ZIP. Mirrors the approach used by
// go-fitz's content-type sniffer.
func detectZipFormat(b []byte) Format {
	const localHeaderMin = 30
	if len(b) < localHeaderMin {
		return FormatAuto
	}

	// EPUB: first entry is uncompressed "mimetype" containing
	// "application/epub+zip". Inline check at offset 30 against known
	// string.
	if len(b) >= 58 && bytes.Equal(b[30:58], []byte("mimetypeapplication/epub+zip")) {
		return FormatEPUB
	}

	// OOXML: first entry is typically "[Content_Types].xml" or "_rels/.rels",
	// then "docProps/", and eventually a directory header that starts with
	// "word/", "xl/", or "ppt/".
	firstName := readZipFirstFilename(b)
	if firstName == "" {
		return FormatAuto
	}
	if !isOOXMLFirstName(firstName) {
		return FormatAuto
	}

	// Walk subsequent local file headers looking for word/, xl/, or ppt/
	// prefix.
	off := 0
	for i := 0; i < 8; i++ {
		off = findNextLocalHeader(b, off)
		if off < 0 || off+30 > len(b) {
			break
		}
		nameLen := int(binary.LittleEndian.Uint16(b[off+26 : off+28]))
		if off+30+nameLen > len(b) {
			break
		}
		name := string(b[off+30 : off+30+nameLen])
		switch {
		case strings.HasPrefix(name, "word/"):
			return FormatDOCX
		case strings.HasPrefix(name, "xl/"):
			return FormatXLSX
		case strings.HasPrefix(name, "ppt/"):
			return FormatPPTX
		}
		off++
	}

	return FormatAuto
}

func readZipFirstFilename(b []byte) string {
	if len(b) < 30 {
		return ""
	}
	nameLen := int(binary.LittleEndian.Uint16(b[26:28]))
	if 30+nameLen > len(b) {
		return ""
	}
	return string(b[30 : 30+nameLen])
}

func isOOXMLFirstName(name string) bool {
	switch {
	case name == "[Content_Types].xml",
		name == "_rels/.rels",
		strings.HasPrefix(name, "docProps/"),
		strings.HasPrefix(name, "_rels/"),
		strings.HasPrefix(name, "word/"),
		strings.HasPrefix(name, "xl/"),
		strings.HasPrefix(name, "ppt/"):
		return true
	}
	return false
}

// findNextLocalHeader returns the byte offset of the next ZIP local file
// header signature "PK\x03\x04" at or after start, or -1 if none is found.
func findNextLocalHeader(b []byte, start int) int {
	sig := []byte{0x50, 0x4B, 0x03, 0x04}
	if start >= len(b) {
		return -1
	}
	i := bytes.Index(b[start:], sig)
	if i < 0 {
		return -1
	}
	return start + i
}

// looksLikeHTML returns true when the first non-whitespace token in b looks
// like an HTML tag, doctype, or comment.
func looksLikeHTML(b []byte) bool {
	i := 0
	for i < len(b) {
		c := b[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			i++
			continue
		}
		break
	}
	if i >= len(b) || b[i] != '<' {
		return false
	}
	rest := b[i:]
	lower := bytes.ToLower(rest[:min(len(rest), 64)])
	prefixes := [][]byte{
		[]byte("<!doctype html"),
		[]byte("<html"),
		[]byte("<head"),
		[]byte("<body"),
		[]byte("<meta"),
		[]byte("<title"),
		[]byte("<div"),
		[]byte("<p "),
		[]byte("<p>"),
		[]byte("<!--"),
	}
	for _, p := range prefixes {
		if bytes.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// looksLikeText returns true when b contains only printable UTF-8 plus common
// whitespace, and is not empty.
func looksLikeText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if !utf8.Valid(b) {
		return false
	}
	for _, c := range b {
		if c == '\t' || c == '\n' || c == '\r' || (c >= 0x20 && c < 0x7F) || c >= 0x80 {
			continue
		}
		return false
	}
	return true
}
