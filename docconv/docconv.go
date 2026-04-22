package docconv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
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
	// description. Empty means use the library default.
	DescribePrompt string

	// OCRPrompt overrides the default prompt used for OCR fallback pages.
	// Empty means use the library default.
	OCRPrompt string
}

// Default prompts used by the library when Options.{Describe,OCR}Prompt is
// empty. These are exported so CLI wrappers and tests can reference them.
const (
	DefaultDescribePrompt = "Describe this image for a reader who cannot see it. Keep it under two sentences."
	DefaultOCRPrompt      = "Transcribe all text in this image verbatim. Preserve headings, lists, and tables as markdown. Do not add commentary."
	DefaultOCRDPI         = 200.0
)

// resolvedOptions returns a non-nil Options with zero values filled in from
// the library defaults. Callers should pass resolvedOptions to backend
// functions rather than raw *Options to avoid repeated nil checks.
func resolvedOptions(opts *Options) Options {
	var out Options
	if opts != nil {
		out = *opts
	}
	if out.OCRDPI == 0 {
		out.OCRDPI = DefaultOCRDPI
	}
	if out.DescribePrompt == "" {
		out.DescribePrompt = DefaultDescribePrompt
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
