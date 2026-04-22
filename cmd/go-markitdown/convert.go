package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/roelfdiedericks/go-markitdown/docconv"
)

type convertFlags struct {
	output           string
	includeImages    bool
	imageDir         string
	metadata         bool
	format           string
	describer        string
	describerTimeout time.Duration
	ocrFallback      bool
	ocrDPI           float64
	verbose          bool
}

func newConvertCmd() *cobra.Command {
	var f convertFlags

	cmd := &cobra.Command{
		Use:   "convert [flags] <input|->",
		Short: "Convert a document to markdown",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConvert(args[0], f)
		},
	}

	cmd.Flags().StringVarP(&f.output, "output", "o", "", "Write markdown to PATH instead of stdout")
	cmd.Flags().BoolVar(&f.includeImages, "include-images", false, "Reference embedded images in the markdown output")
	cmd.Flags().StringVar(&f.imageDir, "image-dir", "", "Extract embedded images to PATH. Implies --include-images")
	cmd.Flags().BoolVar(&f.metadata, "metadata", false, "Prepend YAML front-matter with document metadata")
	cmd.Flags().StringVar(&f.format, "format", "auto", "Force a specific format (pdf, docx, xlsx, pptx, epub, mobi, html, text, auto)")
	cmd.Flags().StringVar(&f.describer, "describer", "", "Shell command that describes images (stdin: image bytes, env: GO_MARKITDOWN_PROMPT/_MIME, stdout: description)")
	cmd.Flags().DurationVar(&f.describerTimeout, "describer-timeout", 60*time.Second, "Per-image timeout for the describer command")
	cmd.Flags().BoolVar(&f.ocrFallback, "ocr-fallback", false, "When text extraction returns nothing, render each page and OCR via --describer")
	cmd.Flags().Float64Var(&f.ocrDPI, "ocr-dpi", 200, "Render DPI for OCR fallback pages")
	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "Log progress to stderr")

	return cmd
}

func runConvert(input string, f convertFlags) error {
	format, err := parseFormatFlag(f.format)
	if err != nil {
		return badArgsError(err)
	}

	// Implicitly enable --include-images when --image-dir is set.
	if f.imageDir != "" {
		f.includeImages = true
	}

	// Set up an ImageDescriber if the user provided one.
	var describer docconv.ImageDescriber
	if f.describer != "" {
		parsed, perr := parseDescriberCommand(f.describer)
		if perr != nil {
			return badArgsError(fmt.Errorf("--describer: %w", perr))
		}
		describer = &subprocessDescriber{
			command: parsed,
			timeout: f.describerTimeout,
			verbose: f.verbose,
		}
	}

	if f.ocrFallback && describer == nil {
		fmt.Fprintln(os.Stderr, "go-markitdown: --ocr-fallback requested but --describer not set; OCR will be skipped")
	}

	opts := &docconv.Options{
		LLMClient:       describer,
		IncludeImages:   f.includeImages,
		ImageDir:        f.imageDir,
		IncludeMetadata: f.metadata,
		OCRFallback:     f.ocrFallback && describer != nil,
		OCRDPI:          f.ocrDPI,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	data, reader, err := loadInput(input)
	if err != nil {
		return err
	}

	if f.verbose {
		fmt.Fprintf(os.Stderr, "go-markitdown: converting %s (format=%s)\n", input, format.String())
	}

	var md string
	if data != nil {
		md, err = docconv.ExtractReaderContext(ctx, readerFromBytes(data), format, opts)
	} else {
		md, err = docconv.ExtractReaderContext(ctx, reader, format, opts)
	}
	if err != nil {
		return err
	}

	return writeOutput(f.output, md)
}

// loadInput returns either raw bytes (for stdin) or a reader (for files). We
// read stdin into memory because go-fitz requires a byte slice and we have
// no reliable way to know the format without buffering anyway.
func loadInput(input string) ([]byte, io.Reader, error) {
	if input == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, nil, err
		}
		return data, nil, nil
	}
	if _, err := os.Stat(input); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, badArgsError(fmt.Errorf("input not found: %s", input))
		}
		return nil, nil, err
	}
	// Read whole file into memory; the library is byte-slice oriented
	// for the heavy backends anyway.
	data, err := os.ReadFile(input)
	if err != nil {
		return nil, nil, err
	}
	return data, nil, nil
}

func readerFromBytes(b []byte) io.Reader {
	return &bytesReader{b: b}
}

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

func writeOutput(path, md string) error {
	if path == "" {
		_, err := fmt.Fprint(os.Stdout, ensureTrailingNewline(md))
		return err
	}
	if err := os.WriteFile(path, []byte(ensureTrailingNewline(md)), 0o644); err != nil {
		return err
	}
	return nil
}

func ensureTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func parseFormatFlag(s string) (docconv.Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return docconv.FormatAuto, nil
	case "pdf":
		return docconv.FormatPDF, nil
	case "docx":
		return docconv.FormatDOCX, nil
	case "xlsx":
		return docconv.FormatXLSX, nil
	case "pptx":
		return docconv.FormatPPTX, nil
	case "epub":
		return docconv.FormatEPUB, nil
	case "mobi":
		return docconv.FormatMOBI, nil
	case "html":
		return docconv.FormatHTML, nil
	case "text", "txt":
		return docconv.FormatText, nil
	}
	return docconv.FormatAuto, fmt.Errorf("unknown --format %q", s)
}
