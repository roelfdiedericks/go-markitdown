package docconv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// extractedImage is the internal representation of an image pulled out of a
// document. Backends populate these while parsing and the post-pass replaces
// placeholder tokens in the markdown body with real references or
// descriptions.
type extractedImage struct {
	// ID is the stable relationship ID from the source document (e.g.
	// "rId3" for OOXML, "img-NNN" for fitz-rendered pages).
	ID string

	// Index is the library-assigned zero-based sequence number across
	// the whole document. Drives the output filename when ImageDir is set.
	Index int

	// Data is the raw encoded image bytes.
	Data []byte

	// MimeType is the image MIME type (image/png, image/jpeg, ...).
	MimeType string

	// Extension is the file extension the image is stored as (".png",
	// ".jpg", ...). Includes the leading dot.
	Extension string
}

// placeholderPattern matches the placeholder tokens that backends emit for
// each image while building markdown. A post-pass rewrites these once we
// know the final alt-text and output path.
//
// Backends emit placeholders as <img src="rid:<id>" alt=""/>; after
// html-to-markdown conversion that becomes "![](rid:<id>)". We match on
// the src scheme so legitimate images with real URLs are left alone.
var placeholderPattern = regexp.MustCompile(`!\[[^\]]*\]\(rid:([^)]+)\)`)

// imagePlaceholder renders the placeholder token for an image with the given
// relationship ID. Backends call this when they encounter an image reference
// in the source document.
func imagePlaceholder(id string) string {
	return fmt.Sprintf("![__rid_%s__]()", id)
}

// replaceImagePlaceholders walks the markdown body, replaces each image
// placeholder with a final markdown reference, and writes image bytes to
// ImageDir when requested. When LLMClient is nil, placeholder alt-text is
// used.
func replaceImagePlaceholders(ctx context.Context, md string, images map[string]extractedImage, opts Options) (string, error) {
	if len(images) == 0 {
		// Nothing to replace but still strip any unknown placeholders so
		// the output is clean.
		return placeholderPattern.ReplaceAllString(md, ""), nil
	}

	if !opts.IncludeImages {
		// Strip all placeholders cleanly.
		return placeholderPattern.ReplaceAllString(md, ""), nil
	}

	imageDir := opts.ImageDir
	if imageDir != "" {
		if err := os.MkdirAll(imageDir, 0o755); err != nil {
			return "", fmt.Errorf("create image dir: %w", err)
		}
	}

	// We use a serial replacement rather than ReplaceAllStringFunc so we
	// can return errors from describer calls or file writes.
	var (
		result []byte
		pos    int
	)
	matches := placeholderPattern.FindAllStringSubmatchIndex(md, -1)
	for _, m := range matches {
		result = append(result, md[pos:m[0]]...)
		id := md[m[2]:m[3]]
		img, ok := images[id]
		if !ok {
			// Unknown ID; drop the placeholder.
			pos = m[1]
			continue
		}

		ref, err := renderImageRef(ctx, img, imageDir, opts)
		if err != nil {
			return "", err
		}
		result = append(result, ref...)
		pos = m[1]
	}
	result = append(result, md[pos:]...)
	return string(result), nil
}

// renderImageRef writes img to ImageDir (if set) and returns the markdown
// reference for it, optionally with a DescribeImage-produced alt-text.
func renderImageRef(ctx context.Context, img extractedImage, imageDir string, opts Options) (string, error) {
	filename := fmt.Sprintf("image_%s%s", paddedIndex(img.Index), img.Extension)
	relPath := filename
	if imageDir != "" {
		full := filepath.Join(imageDir, filename)
		if err := os.WriteFile(full, img.Data, 0o644); err != nil {
			return "", fmt.Errorf("write image: %w", err)
		}
		relPath = full
	}

	alt := fmt.Sprintf("Image: %s", filename)
	if opts.LLMClient != nil {
		desc, err := opts.LLMClient.DescribeImage(ctx, img.Data, img.MimeType, opts.DescribePrompt)
		if err == nil && desc != "" {
			alt = sanitizeAlt(desc)
		}
		// Description errors fall back to the placeholder; we don't fail
		// the whole document over a single image.
	}

	return fmt.Sprintf("![%s](%s)", alt, relPath), nil
}

// sanitizeAlt collapses newlines and strips ] characters so the markdown
// ![...](...) syntax doesn't break on multi-line descriptions.
func sanitizeAlt(s string) string {
	var b []byte
	lastSpace := false
	for _, c := range s {
		if c == '\n' || c == '\r' || c == '\t' {
			if !lastSpace {
				b = append(b, ' ')
				lastSpace = true
			}
			continue
		}
		if c == ']' || c == '[' {
			continue
		}
		b = append(b, byte(c))
		lastSpace = c == ' '
	}
	out := string(b)
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return out
}

func paddedIndex(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
