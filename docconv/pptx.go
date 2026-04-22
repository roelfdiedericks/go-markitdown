package docconv

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/roelfdiedericks/go-markitdown/docconv/internal/mdconv"
	"github.com/roelfdiedericks/go-markitdown/docconv/internal/ooxml"
	"github.com/roelfdiedericks/go-markitdown/docconv/internal/pptx"
)

// extractPPTX parses a PPTX by walking ppt/slides/slide*.xml in numeric
// order with a stdlib encoding/xml decoder (see internal/pptx). Slides are
// rendered as per-slide <section> blocks in HTML, joined with "---\n" as
// slide separators after markdown conversion.
//
// On any parse error the function falls back to extractFitzFallback so we
// never regress against the pre-native-backend behaviour. The fitz path
// also handles the OCR fallback when the native walker produces empty
// output.
func extractPPTX(ctx context.Context, data []byte, opts Options) (string, Metadata, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return extractFitzOrWrap(ctx, data, opts, err)
	}

	slides := collectSlidePaths(zr)
	if len(slides) == 0 {
		return extractFitzOrWrap(ctx, data, opts, fmt.Errorf("pptx: no slides found"))
	}

	var (
		htmlSections []string
		allImages    = map[string]extractedImage{}
		imageIdx     int
	)

	for _, slidePath := range slides {
		section, imgs, err := walkSlide(zr, slidePath)
		if err != nil {
			// One bad slide should not sink the whole file; log
			// shape via a placeholder section and keep going.
			htmlSections = append(htmlSections, fmt.Sprintf("<section><p><em>slide parse error: %s</em></p></section>", escapeForHTML(err.Error())))
			continue
		}
		htmlSections = append(htmlSections, section)
		for id, ref := range imgs {
			if _, seen := allImages[id]; seen {
				continue
			}
			allImages[id] = extractedImage{
				ID:            id,
				Index:         imageIdx,
				Data:          ref.Data,
				MimeType:      ref.MimeType,
				Extension:     ref.Extension,
				ContextBefore: ref.ContextBefore,
				AuthorAltText: ref.AuthorAltText,
			}
			imageIdx++
		}
	}

	// Convert each slide section separately and join with a horizontal
	// rule. Running mdconv once per slide keeps the markdown stable even
	// when html-to-markdown re-numbers or re-groups adjacent blocks.
	// Each slide is prefixed with an HTML comment carrying the 1-based
	// slide number so downstream readers (humans, LLMs, and the parity
	// comparison against Microsoft markitdown) can locate slide
	// boundaries without relying on the horizontal rule alone.
	var mdSlides []string
	for i, section := range htmlSections {
		md, err := mdconv.Convert(section)
		if err != nil {
			return extractFitzOrWrap(ctx, data, opts, fmt.Errorf("pptx: markdown convert: %w", err))
		}
		if strings.TrimSpace(md) == "" {
			continue
		}
		mdSlides = append(mdSlides, fmt.Sprintf("<!-- Slide number: %d -->\n%s", i+1, md))
	}

	md := strings.Join(mdSlides, "\n\n---\n\n")
	md = strings.TrimSpace(md)

	if md == "" {
		// Empty native output — route to the OCR fallback which
		// renders pages and feeds them to the describer. If OCR is
		// off or the client is missing, surface ErrNoText.
		if opts.OCRFallback && opts.LLMClient != nil {
			return extractFitzFallback(ctx, data, FormatPPTX, opts)
		}
		return "", Metadata{Format: FormatPPTX.String()}, ErrNoText
	}

	md, err = replaceImagePlaceholders(ctx, md, allImages, opts)
	if err != nil {
		return "", Metadata{Format: FormatPPTX.String()}, wrapBackendError(FormatPPTX, err)
	}

	return md, Metadata{Format: FormatPPTX.String()}, nil
}

// walkSlide parses one slide and returns its HTML section plus the set of
// images referenced from that slide, keyed by rId, with media bytes
// resolved through the slide's _rels/slide<N>.xml.rels part.
func walkSlide(zr *zip.Reader, slidePath string) (string, map[string]pptx.ImageRef, error) {
	f := findZipFile(zr, slidePath)
	if f == nil {
		return "", nil, fmt.Errorf("pptx: slide %q not found in zip", slidePath)
	}
	rc, err := f.Open()
	if err != nil {
		return "", nil, fmt.Errorf("pptx: open %s: %w", slidePath, err)
	}
	defer rc.Close()

	walkCtx := &pptx.WalkCtx{Images: map[string]pptx.ImageRef{}}
	section, err := pptx.Walk(rc, walkCtx)
	if err != nil {
		return "", nil, err
	}

	// Resolve any image rIds the walker discovered against the slide's
	// rels part. Any rId we cannot resolve is dropped from the image
	// map; the <img> placeholder in the markdown will be stripped by
	// replaceImagePlaceholders as an unknown id.
	if len(walkCtx.Images) > 0 {
		rels, err := ooxml.ParseRels(zr, slidePath)
		if err != nil {
			return section, nil, fmt.Errorf("pptx: parse rels for %s: %w", slidePath, err)
		}
		for rid, ref := range walkCtx.Images {
			target, ok := rels[rid]
			if !ok || target == "" {
				delete(walkCtx.Images, rid)
				continue
			}
			bytesData, err := ooxml.ReadMediaBytes(zr, target)
			if err != nil {
				delete(walkCtx.Images, rid)
				continue
			}
			ext := strings.ToLower(path.Ext(target))
			ref.Target = target
			ref.Data = bytesData
			ref.Extension = ext
			ref.MimeType = pptxMimeForExt(ext)
			walkCtx.Images[rid] = ref
		}
	}

	return section, walkCtx.Images, nil
}

// slidePathPattern matches slide entries under ppt/slides/ with a numeric
// suffix. Slide layouts and masters are deliberately excluded.
var slidePathPattern = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

// collectSlidePaths returns the slide paths in numeric order. The default
// zip iteration order is insertion order, which does not always match the
// presentation sequence — most fixtures alphabetise as slide1, slide10,
// slide11, slide2 which is wrong for users. Sorting by the integer suffix
// fixes that.
func collectSlidePaths(zr *zip.Reader) []string {
	type indexedSlide struct {
		path string
		n    int
	}
	var slides []indexedSlide
	for _, f := range zr.File {
		m := slidePathPattern.FindStringSubmatch(f.Name)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		slides = append(slides, indexedSlide{path: f.Name, n: n})
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].n < slides[j].n })
	out := make([]string, len(slides))
	for i, s := range slides {
		out[i] = s.path
	}
	return out
}

// extractFitzOrWrap routes to the go-fitz fallback, unwrapping
// ErrFitzRequired into the original parse error so callers understand
// what actually went wrong under -tags nofitz.
func extractFitzOrWrap(ctx context.Context, data []byte, opts Options, primary error) (string, Metadata, error) {
	md, meta, ferr := extractFitzFallback(ctx, data, FormatPPTX, opts)
	if ferr == nil {
		return md, meta, nil
	}
	if errors.Is(ferr, ErrFitzRequired) {
		return "", Metadata{Format: FormatPPTX.String()}, wrapBackendError(FormatPPTX, primary)
	}
	return "", Metadata{Format: FormatPPTX.String()},
		fmt.Errorf("pptx: parse failed (%w); fitz fallback also failed: %v", primary, ferr)
}

func findZipFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// pptxMimeForExt maps a file extension (with leading dot) to the MIME
// type the describer hook should see. Mirrors the DOCX walker's table;
// kept here rather than cross-package to avoid widening internal/docx's
// public surface.
func pptxMimeForExt(ext string) string {
	switch strings.ToLower(ext) {
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

// escapeForHTML is a tiny helper for embedding error text into the
// placeholder error section we emit when a single slide fails to parse.
func escapeForHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
