package docconv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/text/unicode/norm"
)

// ImageDescriber describes images for LLM context. Consumers implement this
// with whatever vision model or chain they choose; docconv never imports any
// specific LLM SDK.
//
// The same interface serves two roles:
//
//   - Inline image description when IncludeImages is true.
//   - Page-level transcription (OCR) when OCRFallback is true and a document
//     has no extractable text.
//
// The library supplies a distinct prompt for each role and the implementation
// may branch on that prompt if it wants different behaviour, or simply pass
// the prompt through to its vision model of choice.
type ImageDescriber interface {
	// DescribeImage returns a text description of an image.
	//
	// img is raw encoded image bytes (PNG, JPEG, ...). mimeType is the image
	// MIME type, useful for APIs that require explicit typing. prompt is the
	// caller-chosen (or library-default) instruction sent to the vision
	// model.
	DescribeImage(ctx context.Context, img []byte, mimeType string, prompt string) (string, error)
}

// Options configures extraction behaviour. A nil *Options is treated as the
// zero value: no image extraction, no metadata, no OCR fallback.
type Options struct {
	// LLMClient, when non-nil, is called to describe embedded images
	// (see IncludeImages) and to transcribe pages when OCRFallback is set.
	LLMClient ImageDescriber

	// IncludeImages controls whether embedded images are represented in
	// the markdown output:
	//
	//   - true + LLMClient != nil: images are described inline via
	//     DescribePrompt.
	//   - true + LLMClient == nil: images are referenced with placeholder
	//     alt-text.
	//   - false: images are stripped entirely.
	IncludeImages bool

	// ImageDir is an optional directory where extracted images are
	// written. When empty, images live only in memory.
	ImageDir string

	// IncludeMetadata prepends a YAML front-matter block with document
	// metadata (title, author, page count, created-at) where the source
	// document exposes it.
	IncludeMetadata bool

	// OCRFallback enables LLM-based transcription of documents that have
	// no extractable text. Requires LLMClient != nil. Default false; the
	// library returns ErrNoText in that case.
	//
	// When enabled, the library renders each page via go-fitz and feeds
	// the image to LLMClient.DescribeImage with OCRPrompt. This is slow
	// and costs API credits, so it is strictly opt-in.
	OCRFallback bool

	// OCRDPI is the render resolution used for OCR fallback pages. Zero
	// means use the default (200).
	OCRDPI float64

	// DescribePrompt overrides the default prompt used for embedded-image
	// description.
	//
	// When empty (the default), the library applies DefaultDescribePromptTemplate
	// with the surrounding document text and any author-supplied alt-text
	// spliced in; it also honours the DecorativeMarker sentinel by
	// suppressing images the describer flags as DECORATIVE.
	//
	// When set to a non-empty string, the caller's prompt is used verbatim
	// for every image — surrounding document text and author alt-text are
	// NOT injected, and DecorativeMarker handling is disabled. Callers who
	// want to own prompt semantics take the whole prompt.
	DescribePrompt string

	// OCRPrompt overrides the default prompt used for OCR fallback pages.
	// Empty means use the library default.
	OCRPrompt string

	// IncludeComments, when true, surfaces reviewer comments (currently
	// DOCX word/comments.xml) inline as HTML comments at each anchor
	// position: "<!-- comment by AUTHOR (DATE): TEXT -->". HTML comments
	// survive markdown conversion untouched — invisible in rendered
	// markdown, present in the byte stream that LLMs tokenise.
	//
	// Off by default because reviewer chatter degrades the "what does this
	// document say" signal LLM-agent consumers expect (unresolved
	// disputes can be treated as document claims). Turn on for audit,
	// legal review, or version-archaeology workflows where reviewer
	// context is itself the content of interest.
	IncludeComments bool
}

// Default prompts and constants used by the library when the corresponding
// Options fields are zero-valued. Exported so CLI wrappers and tests can
// reference them.
const (
	// DefaultDescribePrompt is a flat prompt preserved for back-compat with
	// callers and CLI wrappers that want a complete prompt string rather
	// than the context-aware template. New callers should prefer leaving
	// Options.DescribePrompt empty so the library applies
	// DefaultDescribePromptTemplate with surrounding document context.
	DefaultDescribePrompt = "Describe this image for a reader who cannot see it. Keep it under two sentences."

	// DefaultOCRPrompt is used when OCRPrompt is empty.
	DefaultOCRPrompt = "Transcribe all text in this image verbatim. Preserve headings, lists, and tables as markdown. Do not add commentary."

	// DefaultOCRDPI is the page-render resolution used for OCR fallback.
	DefaultOCRDPI = 200.0

	// DecorativeMarker is the sentinel an ImageDescriber may return to
	// signal that an image carries no informational content (logo, rule,
	// ornament). docconv drops such images from output rather than
	// emitting noisy placeholder alt-text.
	//
	// Honoured only when DescribePrompt is empty (library-owned prompt
	// path); caller-supplied prompts bypass DECORATIVE handling because
	// the caller's prompt may use the word for other purposes.
	DecorativeMarker = "DECORATIVE"
)

// DefaultDescribePromptTemplate is the prompt docconv applies when
// Options.DescribePrompt is empty. Apply with fmt.Sprintf and exactly two
// %s substitutions in order: surrounding document text (may be empty), and
// author-supplied alt-text (may be empty).
//
// Designed to be register-agnostic: surrounding text carries the tone
// (technical datasheet vs. narrative prose), so the prompt itself does not
// attempt to classify document type.
const DefaultDescribePromptTemplate = `Produce a concise caption of this image for inclusion in a markdown document that will be read by a language model.

Surrounding document text (may or may not be relevant to the image):
%s

Author-supplied alt-text (may be empty; when present, refine rather than replace):
%s

Rules:
- 1-3 sentences, plain text, no preamble. Inline markdown emphasis (backticks, asterisks, underscores) is allowed when it clarifies a term.
- If the surrounding text describes the image (e.g. "Figure 3: ...", a caption, or a reference like "as shown above"), ground your caption in that description and add only what the image itself reveals.
- If author-supplied alt-text is present, treat it as authoritative intent. Expand or correct it against what you actually see; do not discard it.
- If both surrounding text and author alt-text are unrelated or empty, describe the image on its own.
- Prefer concrete detail (what it shows, labels, values, structure) over interpretation.
- If the image is a logo, icon, decorative rule, or purely decorative, return exactly: DECORATIVE`

// resolvedOptions returns a non-nil Options with zero values filled in from
// the library defaults. Callers should pass resolvedOptions to backend
// functions rather than raw *Options to avoid repeated nil checks.
//
// DescribePrompt is deliberately NOT default-filled here: the shared image
// pipeline (replaceImagePlaceholders) treats an empty DescribePrompt as
// "library owns the prompt" and applies DefaultDescribePromptTemplate with
// surrounding-document context per image. A non-empty value signals caller
// opt-in to owning the prompt verbatim.
func resolvedOptions(opts *Options) Options {
	var out Options
	if opts != nil {
		out = *opts
	}
	if out.OCRDPI == 0 {
		out.OCRDPI = DefaultOCRDPI
	}
	if out.OCRPrompt == "" {
		out.OCRPrompt = DefaultOCRPrompt
	}
	return out
}

// Extract returns markdown-formatted text from any supported document on disk.
// A nil *Options is treated as the zero value.
func Extract(path string, opts *Options) (string, error) {
	f, err := Detect(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	resolved := resolvedOptions(opts)
	return extract(context.Background(), data, f, resolved)
}

// ExtractReader extracts from an io.Reader with an explicit format hint.
// Pass FormatAuto to request magic-byte detection when reading from a stream
// where the file name is unavailable.
func ExtractReader(r io.Reader, format Format, opts *Options) (string, error) {
	if format == FormatAuto {
		detected, wrapped, err := DetectReader(r)
		if err != nil {
			return "", err
		}
		format = detected
		r = wrapped
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	resolved := resolvedOptions(opts)
	return extract(context.Background(), data, format, resolved)
}

// ExtractReaderContext is like ExtractReader but honours a caller-supplied
// context for cancellation and timeouts (useful when LLMClient is involved).
func ExtractReaderContext(ctx context.Context, r io.Reader, format Format, opts *Options) (string, error) {
	if format == FormatAuto {
		detected, wrapped, err := DetectReader(r)
		if err != nil {
			return "", err
		}
		format = detected
		r = wrapped
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	resolved := resolvedOptions(opts)
	return extract(ctx, data, format, resolved)
}

// extract is the central dispatch. All format-specific backends take the
// already-resolved options and the full document bytes.
func extract(ctx context.Context, data []byte, format Format, opts Options) (string, error) {
	var (
		md   string
		meta Metadata
		err  error
	)

	switch format {
	case FormatDOCX:
		md, meta, err = extractDOCX(ctx, data, opts)
	case FormatPPTX:
		md, meta, err = extractPPTX(ctx, data, opts)
	case FormatPDF, FormatEPUB, FormatMOBI:
		md, meta, err = extractFitz(ctx, data, format, opts)
	case FormatXLSX:
		md, meta, err = extractXLSX(ctx, data, opts)
	case FormatHTML:
		md, meta, err = extractHTML(ctx, data, opts)
	case FormatText:
		md, meta, err = extractText(data)
	case FormatImage:
		return "", ErrUnsupportedFormat
	default:
		return "", ErrUnsupportedFormat
	}

	if err != nil {
		return "", err
	}

	if opts.IncludeMetadata {
		md = renderMetadataFrontMatter(meta) + md
	}

	// Final NFC normalization. mdconv.tidy() already normalises
	// everything that passes through html-to-markdown, but a few
	// backends (XLSX, plain text) assemble markdown directly and the
	// metadata front-matter is concatenated post-tidy. Normalising once
	// here is cheap (no-op when already NFC) and guarantees every byte
	// the library hands back to callers is in the same Unicode form.
	if !norm.NFC.IsNormalString(md) {
		md = norm.NFC.String(md)
	}

	return md, nil
}

// ensureNotEmpty returns ErrNoText when the markdown body is empty after
// trimming whitespace. Used by backends that want to signal "no extractable
// text" consistently.
func ensureNotEmpty(md string) error {
	if len(bytes.TrimSpace([]byte(md))) == 0 {
		return ErrNoText
	}
	return nil
}

// wrapBackendError adds a short context prefix to backend errors so callers
// can tell which format handler produced them.
func wrapBackendError(format Format, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", format.String(), err)
}
