# Changelog

All notable changes to go-markitdown will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] stable - 2026-04-22 — LLM-ready document pipeline

Comprehensive upgrade to image handling, OCR, text quality, and DOCX structural content across all backends. The output format is a superset of v0.1 (all previously rendered content is still there, same shape); the library is now architected to give an LLM the best possible context per image with no manual configuration required.

#### Added

- **Unified image describer pipeline.** Every backend (PDF, DOCX, PPTX, XLSX, HTML, EPUB, MOBI) now routes images through the same shared post-pass. Images are extracted, deduplicated by content hash, and passed to `Options.LLMClient.DescribeImage` with a context-aware prompt built from surrounding document text and any author-supplied alt-text.
- **Context-aware default prompt.** When `Options.DescribePrompt` is empty (the default), the library applies `DefaultDescribePromptTemplate` with neighbouring prose and author alt-text spliced in per image. When non-empty, the caller's prompt is used verbatim — context injection and `DECORATIVE` handling are disabled.
- **`DecorativeMarker` sentinel.** Describers may return the literal string `"DECORATIVE"` to flag logos, ornaments, and rules; the library strips such images from the output rather than emitting placeholder alt-text. Honoured only on the library-owned prompt path.
- **Author alt-text propagation.** DOCX `w:docPr/@descr`, PPTX `p:cNvPr/@descr`, XLSX `GraphicOptions.AltText`, and HTML `alt=""` all flow into the describer prompt as `AuthorAltText`, so the LLM can refine rather than invent captions.
- **Per-page OCR fallback.** `Options.OCRFallback` now runs page-by-page (previously document-wide only when the entire document was empty). Mixed text/scanned PDFs are fully recovered — scanned pages are re-rendered at `OCRDPI` and transcribed via `ImageDescriber` while text pages pass through unchanged.
- **PDF hyphenation rejoin.** `go-fitz`'s per-page HTML is now post-processed to rejoin lowercase-hyphen-newline-lowercase sequences ("confi-\nguration" → "configuration") before markdown conversion, so LLM tokenisers, search, and embeddings see whole words. Uppercase second halves, digits, and legitimate compound words are preserved.
- **Page markers.** PDF, EPUB, and MOBI now emit `<!-- Page N of M -->` at every page boundary, mirroring PPTX's `<!-- Slide number: N -->`. Enables grounded citation by LLM agents reading the output.
- **Unicode NFC normalisation.** All markdown output is normalised to Unicode Normalization Form C as the final conversion step. Prevents silent hash / embedding / dedup failures from mixed composed/decomposed representations.
- **DOCX footnotes and endnotes.** `word/footnotes.xml` and `word/endnotes.xml` are parsed and rendered as GFM footnote syntax: in-body references become `[^fn-N]` anchors and a `## Footnotes` block is appended at the end.
- **DOCX reviewer comments (opt-in).** `Options.IncludeComments = true` surfaces `word/comments.xml` content as inline HTML comments at the original anchor point. Default is `false` so LLM content signal isn't diluted with review-cycle metadata unless explicitly requested.
- **HTML `data:` URI capture.** Inline base64 `<img>` tags are decoded, deduplicated, and routed through the describer pipeline just like OOXML images. Real `http(s)://` URLs are left untouched — the library never fetches remote resources.
- **XLSX embedded images.** Pictures anchored on each sheet are extracted via `excelize.GetPictures`, rendered after the sheet's table, and described with sheet + anchor context.

#### Changed

- **`Options.DescribePrompt` semantics.** Empty now means "library-owned prompt with context" (new behaviour); previously it defaulted to a hardcoded string. Callers who want full control of the prompt must now explicitly set it. The legacy `DefaultDescribePrompt` constant is retained for reference but is no longer applied automatically.
- **`compare-msft-llm`** CPQ.pdf added to the informational fixtures list. The parity comparison is explicitly documented as informational — Microsoft's markitdown image-description quality is not a target.

#### Internal

- New package entry points: `docconv/internal/docx/authoralt.go`, `docconv/internal/docx/footnotes.go`, `docconv/internal/docx/comments.go`.
- Shared helpers in `docconv/images.go`: `shortHash`, `extensionForMime`, `buildDefaultDescribePrompt`, `joinContext`.
- New tests: `docconv/images_test.go`, `docconv/xlsx_test.go`, `docconv/html_test.go`, `docconv/internal/mdconv/mdconv_test.go`; per-feature additions in the existing backend test files.

## [0.1.1] stable - 2026-04-22

- docconv: new `Supports(mime)` / `FromMIME(mime)` helpers for MIME-keyed capability checks, so callers can decide whether to feed a file to `Extract` without touching disk.

## [0.1.0] stable - 2026-04-22

Initial public release. Go library (`docconv` package) plus a standalone `go-markitdown` CLI for extracting clean, LLM-ready markdown from common document formats.

- library: `docconv.Extract`, `docconv.ExtractReader`, and `docconv.ExtractReaderContext` entry points with a single `Options` struct covering image handling, metadata, OCR fallback, and describer wiring
- formats: PDF, DOCX, XLSX, PPTX, EPUB, MOBI, HTML, and plain text. Bare image files return `ErrUnsupportedFormat` so the caller can hand them directly to a multimodal model
- docx backend: `fumiama/go-docx` for structure-preserving parsing (headings, tables, lists, hyperlinks, embedded images via `word/media/`) with an automatic `go-fitz` fallback when the primary parse fails; known limitations (headers/footers, footnotes, comments, tracked changes, TOC, `w:sdt`, bookmarks, field codes) are documented in `README.md`
- pptx backend: hand-rolled stdlib-xml walker that emits one section per slide prefixed with `<!-- Slide number: N -->` markers, detects title placeholders, and extracts `a:tbl` tables; charts and embedded OLE objects render as explicit `(chart omitted)` / `(embedded object omitted)` markers. `go-fitz` acts as fallback on parse error
- xlsx backend: `excelize` emits per-sheet markdown tables with sheet-name headings and embedded-image extraction from `xl/media/`
- html backend: `go-readability` strips chrome (nav, ads, footers) before `html-to-markdown/v2` produces the markdown, with GFM-style pipe tables enabled via `table.NewTablePlugin`
- images: unified `ImageDescriber` interface handles both inline image description (`IncludeImages`) and page-level OCR transcription (`OCRFallback`) with distinct prompts for each role; library imports no LLM SDK directly
- reference describers: `examples/describer-anthropic.sh` (Anthropic Messages API, Claude Opus) and `examples/describer-openai.sh` (OpenAI Chat Completions, GPT-4o) — both source `.env` directly and stream large base64 payloads through temp files to dodge `ARG_MAX`
- cli: `--include-images`, `--include-metadata`, `--ocr-fallback`, `--describer` (subprocess hook that shells out once per image), `--format` override, `--output` to file, and stdin support
- nofitz build tag: pure-Go build for CGO-free environments. DOCX, PPTX, XLSX, HTML, and plain text keep working unchanged; only PDF, EPUB, and MOBI return `ErrFitzRequired`
- metadata: optional YAML front-matter (`--include-metadata`) surfacing title, author, page count, and creation date where the source format exposes it (DOCX via `docProps/core.xml`, PDF/EPUB/MOBI via go-fitz)
- parity tooling: `make compare-msft` and `make compare-msft-llm` diff our output against Microsoft's Python [`markitdown`](https://github.com/microsoft/markitdown) on shared fixtures (DOCX, PPTX, XLSX, HTML); LLM mode drives both sides through the same OpenAI model for apples-to-apples image-description comparison. Dev tool only — not wired into `go test`
- build system: `make build`, `make build-nofitz`, cross-compile targets for `{linux,darwin} x {amd64,arm64}` via `zig cc`, and `make install` into `$GOPATH/bin`
- audit: `make audit` runs `golangci-lint`, `govulncheck`, and `gitleaks` against the tree, auto-installing any missing tool into `$GOBIN` on first run; `.gitleaks.toml` extends the stock rule set and allowlists test fixtures and local `.env` files

[Unreleased]: https://github.com/roelfdiedericks/go-markitdown/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/roelfdiedericks/go-markitdown/releases/tag/v0.2.0
[0.1.1]: https://github.com/roelfdiedericks/go-markitdown/releases/tag/v0.1.1
[0.1.0]: https://github.com/roelfdiedericks/go-markitdown/releases/tag/v0.1.0
