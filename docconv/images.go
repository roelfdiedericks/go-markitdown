package docconv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// shortHash returns the first 12 hex characters of a SHA-256 digest over
// the given bytes. Used by backends to derive deterministic image ids
// (e.g. "xlsx-<hash>", "pdf-<hash>", "html-<hash>") so identical images
// naturally collapse in the shared dedup map.
func shortHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:6])
}

// extensionForMime maps a MIME type to the preferred file extension
// (with leading dot). Unknown types fall through to ".bin". Kept here —
// rather than on any single backend — so the HTML backend (no build
// tags) and the fitz backend (!nofitz) can share the same table.
func extensionForMime(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/bmp":
		return ".bmp"
	case "image/webp":
		return ".webp"
	case "image/tiff":
		return ".tiff"
	case "image/svg+xml":
		return ".svg"
	}
	return ".bin"
}

// extractedImage is the internal representation of an image pulled out of a
// document. Backends populate these while parsing; the post-pass replaces
// placeholder tokens in the markdown body with real references or describer
// captions.
type extractedImage struct {
	// ID is the placeholder key. Backends choose the scheme — fumiama
	// rIds for DOCX, blip rIds for PPTX, "pdf-<shorthash>" for fitz,
	// "xlsx-<shorthash>" for XLSX, "html-<shorthash>" for HTML. The only
	// requirement is uniqueness within a single document.
	ID string

	// Index is the library-assigned zero-based sequence number across
	// the whole document. Drives the output filename when ImageDir is
	// set (image_000.png, image_001.png, ...).
	Index int

	// Data is the raw encoded image bytes.
	Data []byte

	// MimeType is the image MIME type (image/png, image/jpeg, ...).
	MimeType string

	// Extension is the file extension the image is stored as (".png",
	// ".jpg", ...). Includes the leading dot.
	Extension string

	// ContextBefore / ContextAfter carry surrounding document text for
	// use in the default describer prompt. Populated by backends that
	// can cheaply infer the context of an image (PDF/EPUB/MOBI: HTML
	// windows; DOCX: paragraph neighbours; PPTX: slide text; XLSX:
	// sheet + anchor neighbours; HTML: source-order windows).
	//
	// Ignored when Options.DescribePrompt is non-empty (caller owns the
	// prompt verbatim).
	ContextBefore string
	ContextAfter  string

	// AuthorAltText is any alt-text / description the document author
	// supplied for the image (OOXML w:docPr/@descr, HTML alt=""). When
	// present it is surfaced to the describer alongside ContextBefore/
	// After, and the default prompt asks the LLM to refine it rather
	// than invent a fresh caption. Empty when the source has none or
	// the backend cannot extract it (PDF/EPUB/MOBI/XLSX today).
	//
	// Ignored when Options.DescribePrompt is non-empty.
	AuthorAltText string
}

// placeholderPattern matches the placeholder tokens that backends emit for
// each image while building markdown. A post-pass rewrites these once we
// know the final alt-text and output path.
//
// Backends emit placeholders as <img src="rid:<id>" alt=""/>; after
// html-to-markdown conversion that becomes "![](rid:<id>)". Matching on
// the "rid:" src scheme keeps legitimate images with real URLs untouched.
var placeholderPattern = regexp.MustCompile(`!\[[^\]]*\]\(rid:([^)]+)\)`)

// imagePlaceholder renders the HTML placeholder tag for an image with the
// given id. Backends call this when they encounter an image reference in
// the source document and want it to flow through mdconv and out the other
// side as a markdown image link the shared post-pass can rewrite.
func imagePlaceholder(id string) string {
	return fmt.Sprintf(`<img src="rid:%s" alt=""/>`, id)
}

// replaceImagePlaceholders walks the markdown body and replaces every
// placeholder token with a final markdown reference. Behaviour:
//
//   - Unknown IDs are dropped silently (the placeholder is stripped).
//   - With IncludeImages=false, every placeholder is stripped.
//   - Duplicate placeholders for the same ID are described exactly once
//     (first hit), and subsequent hits reuse the cached ref — so a logo
//     repeated on every slide costs one describer call, not N.
//   - When Options.DescribePrompt is empty, the library builds a
//     context-aware prompt from DefaultDescribePromptTemplate,
//     img.ContextBefore/After, and img.AuthorAltText; responses equal to
//     DecorativeMarker are honoured by stripping the image placeholder
//     from the output entirely.
//   - When Options.DescribePrompt is non-empty, the caller's prompt is
//     used verbatim and DecorativeMarker handling is disabled.
//   - Describer errors fall back to the placeholder label and are
//     cached, so a single failing image does not poison the whole doc.
func replaceImagePlaceholders(ctx context.Context, md string, images map[string]extractedImage, opts Options) (string, error) {
	if !opts.IncludeImages || len(images) == 0 {
		// Strip all placeholders cleanly. This path is hit when the
		// caller asked us to drop images entirely, or when no backend
		// registered an image (nothing to resolve against).
		return placeholderPattern.ReplaceAllString(md, ""), nil
	}

	imageDir := opts.ImageDir
	if imageDir != "" {
		if err := os.MkdirAll(imageDir, 0o755); err != nil {
			return "", fmt.Errorf("create image dir: %w", err)
		}
	}

	userPrompt := opts.DescribePrompt

	// Per-ID cache of the rendered markdown ref. Empty string means
	// "decorative, drop placeholder". Missing key means "not yet
	// rendered". Ensures a single describer call per unique image across
	// the whole document — dedup without extra coordination.
	rendered := make(map[string]string, len(images))

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
			// Unknown ID. Drop the placeholder.
			pos = m[1]
			continue
		}

		ref, cached := rendered[id]
		if !cached {
			r, err := renderImageRef(ctx, img, imageDir, userPrompt, opts)
			if err != nil {
				return "", err
			}
			ref = r
			rendered[id] = r
		}

		// Empty ref == decorative: drop the placeholder but leave
		// surrounding whitespace for tidy() / strings.TrimSpace to
		// clean up.
		if ref != "" {
			result = append(result, ref...)
		}
		pos = m[1]
	}
	result = append(result, md[pos:]...)
	return string(result), nil
}

// renderImageRef writes img to imageDir (if set) and returns the markdown
// reference. Empty return means "decorative, drop the placeholder" — only
// possible on the library-owned prompt path.
func renderImageRef(ctx context.Context, img extractedImage, imageDir, userPrompt string, opts Options) (string, error) {
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
		prompt := userPrompt
		libraryOwned := prompt == ""
		if libraryOwned {
			prompt = buildDefaultDescribePrompt(img.ContextBefore, img.ContextAfter, img.AuthorAltText)
		}

		desc, err := opts.LLMClient.DescribeImage(ctx, img.Data, img.MimeType, prompt)
		if err == nil {
			trimmed := strings.TrimSpace(desc)
			if libraryOwned && trimmed == DecorativeMarker {
				return "", nil
			}
			if trimmed != "" {
				alt = sanitizeAlt(trimmed)
			}
		}
		// Describer errors fall back to the placeholder label — a
		// single failing image should not block the whole document.
	}

	return fmt.Sprintf("![%s](%s)", alt, relPath), nil
}

// buildDefaultDescribePrompt renders DefaultDescribePromptTemplate with the
// two variable fields the library owns: surrounding document text (before
// plus after, joined when both are present), and author-supplied alt-text.
// Both arguments may be empty; the template handles that gracefully in its
// "both are empty" rule.
func buildDefaultDescribePrompt(before, after, authorAlt string) string {
	surrounding := joinContext(before, after)
	return fmt.Sprintf(DefaultDescribePromptTemplate, surrounding, strings.TrimSpace(authorAlt))
}

// joinContext combines the before and after context strings with an ellipsis
// separator so the describer can tell where the image sat in the flow. When
// either side is empty the ellipsis is dropped; trimming keeps the injected
// text tight.
func joinContext(before, after string) string {
	before = strings.TrimSpace(before)
	after = strings.TrimSpace(after)
	switch {
	case before == "" && after == "":
		return ""
	case before == "":
		return after
	case after == "":
		return before
	default:
		return before + " […image…] " + after
	}
}

// sanitizeAlt collapses newlines and strips bracket characters so the
// markdown ![...](...) syntax doesn't break on multi-line descriptions.
// Preserves backticks and asterisks so LLM emphasis survives intact.
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
