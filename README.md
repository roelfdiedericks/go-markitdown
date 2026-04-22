# go-markitdown

Convert common document formats (PDF, DOCX, XLSX, PPTX, EPUB, MOBI, HTML, plain text) into clean, LLM-ready markdown. Shipped as a Go library (`docconv`) and a companion CLI. Native Go, CGO via MuPDF, no system packages required.

Inspired by Microsoft's Python [`markitdown`](https://github.com/microsoft/markitdown), but written to embed cleanly into other Go applications. First consumer is [goclaw](https://github.com/roelfdiedericks/goclaw), which feeds documents into LLM context.

> **Status:** v0.2 / LLM-ready document pipeline. API is stabilising. See [SPEC.md](SPEC.md) for the full design rationale.

## Features

- One function (`docconv.Extract`) for every supported format.
- Readable markdown out — structure preserved, images described or referenced, tables rendered as markdown (with CSV fallback when rows are too wide).
- Pluggable image descriptions and OCR via a small `ImageDescriber` interface. No LLM SDK dependency baked in.
- Ships a CLI (`go-markitdown`) with a `--describer` hook so non-Go users can wire any shell-callable vision model into the flow.
- Optional YAML front-matter with document metadata.
- Single static binary per target; cross-compiles to linux/darwin × amd64/arm64.
- Pure-Go fallback build (`nofitz`) for CGO-free environments — degraded format support.

## Supported formats

| Format | Text / structure | Image extraction | Backend |
|--------|------------------|------------------|---------|
| PDF    | Yes (hyphen rejoin + `<!-- Page N of M -->` markers) | Yes (inline images + per-page OCR fallback) | `go-fitz` (MuPDF) |
| DOCX   | Yes (headings, tables, lists, hyperlinks, footnotes/endnotes, optional comments) | Yes (via `word/media/`, with `w:docPr/@descr` alt-text) | `fumiama/go-docx` → `html-to-markdown`; `go-fitz` fallback on parse error |
| XLSX   | Yes (tables / CSV) | Yes (embedded pictures anchored per sheet, with `GraphicOptions.AltText`) | `excelize` |
| PPTX   | Yes (per-slide sections with `<!-- Slide number: N -->` markers, title detection, `a:tbl` tables) | Yes (via `ppt/media/` + slide rels, with `p:cNvPr/@descr` alt-text) | hand-rolled stdlib-xml walker; `go-fitz` fallback on parse error |
| EPUB   | Yes (`<!-- Page N of M -->` markers) | Yes              | `go-fitz` |
| MOBI   | Yes (`<!-- Page N of M -->` markers) | Yes              | `go-fitz` |
| HTML   | Yes              | Inline references + `data:` URI capture (real URLs left untouched) | `go-readability` → `html-to-markdown` |
| Text   | Yes              | —                | stdlib |
| Images | —                | —                | Returns `ErrUnsupportedFormat`; pass to a multimodal model directly. |

DOCX and PPTX use structure-preserving native parsers and fall back to `go-fitz` only when the primary parse fails — the fallback keeps the output usable on malformed inputs without penalising well-formed documents, which is the common case.

**DOCX limitations.** `fumiama/go-docx` focuses on the document body. As of v0.2 we splice in footnotes and endnotes (rendered as GFM `[^fn-N]` anchors plus a `## Footnotes` block) and optionally surface reviewer comments (via `Options.IncludeComments`). Still dropped: headers/footers, tracked changes, tables of contents, `w:sdt` content controls, bookmarks, and field codes. Internal bookmark links like `[Top of this Page](#anchor)` render as plain text.

Scanned / text-less PDFs used to return `ErrNoText` unless OCR fallback was enabled. As of v0.2, OCR runs per page: each page whose extracted text is effectively empty (e.g. a scan in an otherwise-text PDF) is re-rendered and transcribed via your `ImageDescriber`. Enable with `--ocr-fallback` (CLI) or `Options.OCRFallback` (library); requires an `ImageDescriber`.

### Image handling (v0.2)

Every backend now flows through the same shared image post-pass:

- **Context-aware prompts.** When `Options.DescribePrompt` is empty the library builds a prompt per image that carries the surrounding document text (paragraph neighbours for DOCX, slide text for PPTX, sheet text + anchor cell for XLSX, HTML windows for PDF/EPUB/MOBI/HTML) and any author-supplied alt-text (`w:docPr/@descr`, `p:cNvPr/@descr`, `GraphicOptions.AltText`, HTML `alt=""`). When `Options.DescribePrompt` is set, it is used verbatim and context is not injected — the caller owns the prompt.
- **Dedup by content hash.** Identical bytes are described once, regardless of how many times they appear. Logos repeated on every slide cost one describer call, not N.
- **Decorative sentinel.** The describer may return the sentinel string `DECORATIVE` to flag images that carry no informational content (logos, rules, ornaments). The library strips such images from the output instead of emitting noise alt-text. Honoured only on the library-owned prompt path.
- **`data:` URI capture.** PDF and HTML sources frequently embed images as inline base64; these are decoded, deduplicated, and optionally persisted via `ImageDir` just like any other image.
- **No remote I/O.** Real `http(s)://` image URLs in HTML are left untouched — the library never fetches them.

### Text quality (v0.2)

- **Hyphen rejoin.** PDF text extraction splits line-wrapped words (`confi-\nguration`). The fitz pipeline now rejoins them before markdown conversion so LLM tokenisers, search, and embeddings see whole words.
- **Page markers.** PDF, EPUB, and MOBI emit `<!-- Page N of M -->` at every page boundary (mirroring PPTX's `<!-- Slide number: N -->`) so LLM answers can cite back into the source.
- **Unicode NFC.** All output is normalised to Unicode Normalization Form C. Prevents the silent dedup and embedding failures that come from mixing composed/decomposed forms of the same text.

## Install

### CLI

```bash
go install github.com/roelfdiedericks/go-markitdown/cmd/go-markitdown@latest
```

### Library

```bash
go get github.com/roelfdiedericks/go-markitdown/docconv
```

First build takes ~30–60 seconds while MuPDF compiles from vendored C sources. Subsequent builds are cached.

## CLI usage

```bash
go-markitdown convert report.pdf > report.md
go-markitdown convert -o report.md report.pdf
go-markitdown convert --metadata report.pdf > report.md
go-markitdown detect report.pdf
cat report.pdf | go-markitdown convert -            # stdin
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-o, --output PATH` | stdout | Write markdown to PATH. |
| `--include-images` | false | Reference embedded images in the output. |
| `--image-dir PATH` | — | Write embedded images to PATH. Implies `--include-images`. |
| `--metadata` | false | Prepend YAML front-matter with title / author / page count / created. |
| `--format NAME` | auto | Force a format (`pdf`, `docx`, `html`, …) for piped input. |
| `--describer CMD` | — | Shell command that reads an image on stdin and prints a description on stdout. See below. |
| `--describer-timeout D` | 60s | Per-image timeout for `--describer`. |
| `--ocr-fallback` | false | If the document has no extractable text, render each page and OCR it via `--describer`. |
| `--ocr-dpi N` | 200 | Render DPI used for OCR fallback pages. |
| `-v, --verbose` | false | Log progress to stderr. |
| `--version` | — | Print version and exit. |

Exit codes: `0` success, `1` extraction error, `2` unsupported format, `3` invalid arguments.

When `-o` is set, the CLI is pipe-safe: only the markdown goes to the requested sink, logging goes to stderr.

### Image descriptions & OCR with an LLM

`go-markitdown` doesn't ship a built-in LLM client. The `--describer` flag takes the path to any executable that reads an image on stdin and prints a description on stdout. Two environment variables tell the hook what's being asked:

- `GO_MARKITDOWN_PROMPT` — the prompt (defaults cover both "describe this image" and "OCR this page", chosen automatically).
- `GO_MARKITDOWN_MIME` — the MIME type of the piped-in image.

The repo ships two working reference describers:

- [`examples/describer-anthropic.sh`](examples/describer-anthropic.sh) — Anthropic Messages API (uses `ANTHROPIC_KEY`, default model `claude-opus-4-5`).
- [`examples/describer-openai.sh`](examples/describer-openai.sh) — OpenAI Chat Completions with vision (uses `OPENAI_API_KEY`, default model `gpt-4o`, `temperature=0` for reproducibility). `OPENAI_BASE_URL` is honoured, so Azure OpenAI and OpenAI-compatible endpoints work too.

End-to-end test in four commands:

```bash
git clone https://github.com/roelfdiedericks/go-markitdown.git
cd go-markitdown
make build
echo "ANTHROPIC_KEY=sk-ant-..." > .env        # gitignored; the script auto-loads it
./go-markitdown convert --include-images \
    --describer ./examples/describer-anthropic.sh \
    docconv/testdata/test.docx > out.md
```

`ANTHROPIC_KEY` can also be exported directly in your shell; exported values win over `.env`. Override the env-file location with `GO_MARKITDOWN_ENV=/path/to/file`, or the model with `ANTHROPIC_MODEL=claude-opus-4-5`. Dependencies: `curl`, `jq`, `base64`.

Any other vision-capable LLM works — copy the script and swap the HTTP call. The contract is documented in the script header.

Convenience Makefile targets wrap the same commands:

```bash
make sample-describe    # defaults to test.docx; override SAMPLE_DESCRIBE_FIXTURE=...
make sample-ocr         # defaults to test.pdf;  override SAMPLE_OCR_FIXTURE=...
```

## Library usage

```go
import "github.com/roelfdiedericks/go-markitdown/docconv"

md, err := docconv.Extract("report.pdf", &docconv.Options{
    IncludeImages: true,
    LLMClient:     myVisionClient, // implements docconv.ImageDescriber
})
```

### Key types

```go
func Extract(path string, opts *Options) (string, error)
func ExtractReader(r io.Reader, format Format, opts *Options) (string, error)
func Detect(path string) (Format, error)
func DetectReader(r io.Reader) (Format, io.Reader, error)

type Options struct {
    LLMClient       ImageDescriber // optional; drives both image description and OCR
    IncludeImages   bool           // reference / describe embedded images
    ImageDir        string         // write extracted images to disk
    IncludeMetadata bool           // prepend YAML front-matter
    IncludeComments bool           // DOCX only: surface reviewer comments as HTML comments
    OCRFallback     bool           // OCR via LLMClient when a page's text is empty (per-page)
    OCRDPI          float64        // render DPI for OCR pages (default 200)
    DescribePrompt  string         // empty = library-owned context-aware prompt; non-empty = caller-owned verbatim
    OCRPrompt       string         // override default OCR prompt
}

type ImageDescriber interface {
    DescribeImage(ctx context.Context, img []byte, mimeType string, prompt string) (string, error)
}
```

The same `ImageDescriber` implementation is used for both embedded-image description (when `IncludeImages` is true) and page-level OCR (when `OCRFallback` is true). The library passes a different default prompt for each role; implementations can route on the prompt or pass it through verbatim.

### Capability check

Use `docconv.Supports(mime)` when you already know a file's MIME type (for example from an upload gateway) and want to decide whether to feed it to `Extract` without touching disk. `FromMIME(mime)` returns the matching `Format`, or `FormatAuto` if the MIME isn't one this library handles.

```go
if docconv.Supports(upload.MIME) {
    md, err := docconv.ExtractReader(upload.Body, docconv.FromMIME(upload.MIME), nil)
    // ...
}
```

Parameters (`;charset=...`) and case are ignored; `image/*` is deliberately unsupported — send images straight to a vision model.

### Typed errors

```go
ErrUnsupportedFormat  // format not supported by this build
ErrNoText             // no extractable text (scanned / image-only document)
ErrCorruptDocument    // unreadable input
ErrPasswordProtected  // encrypted input
ErrFitzRequired       // format needs go-fitz but binary was built with -tags nofitz
```

Use `errors.Is` to branch.

## Build

### Default (recommended)

```bash
make build           # local binary
make build-all       # cross-compile for linux/darwin × amd64/arm64
```

Default builds embed MuPDF via [`go-fitz`](https://github.com/gen2brain/go-fitz). No system `libmupdf` needed. Cross-compiles use `zig cc` for the CGO matrix — install zig from <https://ziglang.org/download/> and make sure it's on `PATH`.

### Pure-Go (`nofitz`)

```bash
make build-nofitz
```

For wasm or CGO-free containers. DOCX, PPTX, XLSX, HTML, and plain text all work without CGO thanks to the native structure-preserving parsers. Only PDF, EPUB, and MOBI return `ErrFitzRequired` in this build. Not the recommended build, but the only degradation is page-render / OCR-fallback and the three PDF-family formats.

### Other Make targets

`make test`, `make test-nofitz`, `make lint`, `make fmt`, `make vet`, `make tidy`, `make clean`, `make install`, `make sample` (runs the CLI against every fixture and writes `samples/`).

`make audit` runs `golangci-lint`, `govulncheck`, and `gitleaks` against the tree, auto-installing any missing tool into `$GOBIN` on first run. Gitleaks uses [`.gitleaks.toml`](.gitleaks.toml), which extends the stock rule set and allowlists test fixtures and local `.env` files.

## Parity comparison

We periodically diff our output against Microsoft's Python [`markitdown`](https://github.com/microsoft/markitdown) to calibrate quality on shared fixtures. This is a dev tool, not a CI check — `go test` stays Go-only and deterministic.

```bash
make compare-msft-install   # pipx install 'markitdown[all]' + inject openai (one-off)
make compare-msft           # structural-only: no LLM, fast, deterministic
make compare-msft-llm       # both sides describe images via OpenAI gpt-4o (uses .env)
```

Output lands under `samples/compare/`: per-fixture unified diffs plus `summary.md` with line / table / image counts. Primary parity targets are DOCX, PPTX, XLSX, and HTML; PDF is run for information only because the backends (MuPDF vs `pdfminer`) differ enough that byte-for-byte parity isn't meaningful.

The `--llm` mode (`make compare-msft-llm`) runs our CLI with `examples/describer-openai.sh` and drives Microsoft's Python API via `scripts/msft-markitdown-llm.py` so both tools describe images through the same `gpt-4o` endpoint. Image counts in the summary match when structural extraction is at parity; description prose will differ because the tools use different prompts.

## Roadmap

- **v0.2** — LLM-ready document pipeline: unified context-aware image describer hook across all formats; content-hash dedup; DECORATIVE stripping; per-page OCR; PDF hyphen rejoin; page markers; DOCX footnotes/endnotes; optional DOCX comments; Unicode NFC normalisation. (**done — this release**)
- **v0.3** — Adapters for classical OCR engines (Tesseract, PaddleOCR, cloud OCR) implementing `ImageDescriber`.
- **v0.4** — Structured extraction API (`ExtractStructured` returning a typed `Document` for callers that want to shape the output themselves).

Windows is not a priority for v1; `go-fitz` supports it with CGO if/when demand appears.

## License

`go-markitdown` is licensed under the **AGPL-3.0** (see [LICENSE](LICENSE)). Default builds link MuPDF (also AGPL-3.0) via `go-fitz`.

If you link `go-markitdown` into distributed software, your downstream project inherits AGPL obligations: source availability for the combined work (including modifications), and source availability for network-accessed services. Internal and personal use is unaffected. Commercial MuPDF licensing is available from [Artifex](https://artifex.com/) for projects that need to avoid AGPL — that's a concern for those consumers, not this library.

Transitive licenses: `excelize` — BSD-3-Clause · `go-readability` — MIT · `html-to-markdown/v2` — MIT · `cobra` — Apache-2.0 · `fumiama/go-docx` — AGPL-3.0 · MuPDF / `go-fitz` — AGPL-3.0.

## Acknowledgements

- [Microsoft markitdown](https://github.com/microsoft/markitdown) — Python equivalent that inspired the name.
- [go-fitz](https://github.com/gen2brain/go-fitz) + [MuPDF](https://mupdf.com/) — the heavy lifting for PDF/EPUB/MOBI/Office.
- [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown), [excelize](https://github.com/xuri/excelize), [go-readability (readeck fork)](https://codeberg.org/readeck/go-readability).
