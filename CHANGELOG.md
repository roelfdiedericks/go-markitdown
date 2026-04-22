# Changelog

All notable changes to go-markitdown will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/roelfdiedericks/go-markitdown/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/roelfdiedericks/go-markitdown/releases/tag/v0.1.0
