# go-markitdown SPEC2 — MIME-capability Helpers

**Status:** Proposed
**Created:** 2026-04-21
**Authors:** Ratpup (roelfdiedericks) + RoDent (goclaw agent)
**Target:** `github.com/roelfdiedericks/go-markitdown/docconv` (library)
**Depends on:** existing `Format` enum and `Detect` / `DetectReader` in [`docconv/detect.go`](docconv/detect.go)
**Scope:** purely additive — no changes to existing API or behaviour.

## Context

The first consumer of this library, [goclaw](https://github.com/roelfdiedericks/goclaw), needs to decide whether a user-uploaded file is one that `docconv` can extract — **without reading the file**. Uploads come through goclaw's gateway already tagged with a MIME type (from content sniffing done at ingest time), so the fastest, cheapest, most honest check is:

> "Given this MIME string, can `docconv` extract it?"

Today `docconv` can answer "given this file on disk" (`Detect`) and "given this reader" (`DetectReader`), but not "given this MIME string". Goclaw needs the MIME-keyed answer so the gateway can add a hint to the LLM like `Call document_extract with this path to read the contents` for PDFs/OOXML/EPUB/HTML/text files — but stay quiet for, say, `image/png` or `application/zip`.

Rather than having goclaw maintain its own `map[string]bool` of supported MIMEs (which will drift every time the library gains or drops a format), we'd like `docconv` to own the knowledge. This keeps the library as the single source of truth for "what I can extract", and any other Go consumer gets the same capability check for free.

## Deliverable

Two new exported functions in [`docconv/detect.go`](docconv/detect.go):

```go
// FromMIME returns the Format matching a MIME type, or FormatAuto if the MIME
// is not one docconv can extract. Parameters (anything after ';') are ignored
// so "text/plain; charset=utf-8" resolves the same as "text/plain". Matching
// is case-insensitive.
func FromMIME(mime string) Format

// Supports reports whether docconv can extract content from the given MIME
// type. Equivalent to FromMIME(mime) != FormatAuto. Intended for callers that
// only need a yes/no answer (e.g. deciding whether to offer a "read this
// document" action in a UI).
func Supports(mime string) bool
```

### MIME → Format mapping (initial)

| MIME                                                                          | Format        |
| ----------------------------------------------------------------------------- | ------------- |
| `application/pdf`                                                             | `FormatPDF`   |
| `application/vnd.openxmlformats-officedocument.wordprocessingml.document`     | `FormatDOCX`  |
| `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`           | `FormatXLSX`  |
| `application/vnd.openxmlformats-officedocument.presentationml.presentation`   | `FormatPPTX`  |
| `application/epub+zip`                                                        | `FormatEPUB`  |
| `application/x-mobipocket-ebook`                                              | `FormatMOBI`  |
| `text/html`, `application/xhtml+xml`                                          | `FormatHTML`  |
| `text/plain`, `text/markdown`                                                 | `FormatText`  |

Anything else → `FormatAuto` (i.e. `Supports(...)` returns `false`). Images (`image/*`) are explicitly **not** supported by this check — `FormatImage` exists in the library but callers should send images directly to a vision model, and the current `Extract` path returns `ErrUnsupportedFormat` for image inputs anyway. Keeping `Supports("image/png")` false matches the guidance in the comment on `FormatImage` in [`docconv/detect.go`](docconv/detect.go).

### Semantics and edge cases

- **Case-insensitive match** on the MIME type (`application/PDF` === `application/pdf`).
- **Strip parameters**: anything after the first `;` is ignored. `text/plain; charset=utf-8` → `FormatText`. `text/html;profile=mobile` → `FormatHTML`.
- **Whitespace**: trim leading/trailing whitespace on the type portion (callers may pass strings like `"  application/pdf  "`).
- **Empty or malformed input** (`""`, `"garbage"`, `"application/"`) → `FormatAuto` / `false`. Never panic.
- **No I/O, no allocations in the hot path** beyond what a `switch` on a lowercased string requires. This is called on every incoming file attachment; it must be cheap.
- **Stable for callers**: once a MIME is mapped to a `Format`, that mapping shouldn't change across releases (callers pin on module versions). Adding new MIMEs in future releases is fine; removing or remapping an existing entry is a breaking change.

### Suggested implementation sketch

```go
// FromMIME returns the Format matching a MIME type, or FormatAuto if unknown.
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

// Supports reports whether docconv can extract content from the given MIME.
func Supports(mime string) bool { return FromMIME(mime) != FormatAuto }
```

Drop both into the existing [`docconv/detect.go`](docconv/detect.go), right after `Detect` / `DetectReader`, so the whole detection story lives in one file.

## Tests

Add a table-driven test in [`docconv/detect_test.go`](docconv/detect_test.go) (next to the existing `TestDetectFixtures` / `TestDetectReader`):

```go
func TestFromMIME(t *testing.T) {
    cases := []struct {
        mime   string
        expect Format
        supp   bool
    }{
        // Happy paths — each supported MIME.
        {"application/pdf", FormatPDF, true},
        {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", FormatDOCX, true},
        {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", FormatXLSX, true},
        {"application/vnd.openxmlformats-officedocument.presentationml.presentation", FormatPPTX, true},
        {"application/epub+zip", FormatEPUB, true},
        {"application/x-mobipocket-ebook", FormatMOBI, true},
        {"text/html", FormatHTML, true},
        {"application/xhtml+xml", FormatHTML, true},
        {"text/plain", FormatText, true},
        {"text/markdown", FormatText, true},

        // Case + parameter robustness.
        {"APPLICATION/PDF", FormatPDF, true},
        {"text/plain; charset=utf-8", FormatText, true},
        {"  text/html  ", FormatHTML, true},

        // Negatives — library either can't or shouldn't handle via Supports.
        {"image/png", FormatAuto, false},
        {"image/jpeg", FormatAuto, false},
        {"application/zip", FormatAuto, false},
        {"application/octet-stream", FormatAuto, false},
        {"", FormatAuto, false},
        {"garbage", FormatAuto, false},
        {"application/", FormatAuto, false},
    }

    for _, tc := range cases {
        t.Run(tc.mime, func(t *testing.T) {
            if got := FromMIME(tc.mime); got != tc.expect {
                t.Errorf("FromMIME(%q) = %s, want %s", tc.mime, got, tc.expect)
            }
            if got := Supports(tc.mime); got != tc.supp {
                t.Errorf("Supports(%q) = %v, want %v", tc.mime, got, tc.supp)
            }
        })
    }
}
```

No new fixtures or testdata files needed — this is pure string-level logic.

## Documentation

- Add a short section to [`README.md`](README.md) under the API summary: one sentence + a 3-line example showing `if docconv.Supports(mime) { ... }`. No need to list every MIME in the README; the table lives in this SPEC and in the godoc on `FromMIME`.
- Godoc comments on both functions should be complete (the function signatures above are already worded for godoc).
- Add an entry under the "Unreleased" / next-release section of [`CHANGELOG.md`](CHANGELOG.md):
  - `docconv: new Supports(mime) / FromMIME(mime) helpers for MIME-keyed capability checks, so callers can decide whether to feed a file to Extract without touching disk.`

## Release

- This is a **non-breaking, additive** change. A patch-level bump is sufficient (e.g. `v0.x.y` → `v0.x.(y+1)`), consistent with the project's existing versioning.
- Tag the release so goclaw can pin a stable version in its `go.mod`. Goclaw's integration is blocked on having a tag to pin.

## Out of scope (for this SPEC)

- File-extension-keyed helpers (`FromExt`, `SupportsExt`). The existing `detectFromExtension` is unexported and that's fine for now; if a future consumer needs extension-level checks we can mirror the MIME helpers at that point.
- Reverse lookup `Format → []string` of canonical MIMEs. Nothing needs it today; add it only when a concrete caller asks for it.
- A full `mime.Type` / RFC-6838 parser. `strings.IndexByte(';')` + `TrimSpace` + `ToLower` is enough; we don't need to support things like quoted parameters.
- Changing existing `Detect` / `DetectReader` to return a `(Format, MIME)` pair. Keep this change surface minimal.

## Summary for the maintainer

1. Add `FromMIME(mime string) Format` and `Supports(mime string) bool` to `docconv/detect.go`, using the mapping table and semantics above.
2. Add one table-driven test (`TestFromMIME`) covering happy paths, case/parameter robustness, and a handful of negatives.
3. Update `README.md` with a short usage blurb and `CHANGELOG.md` with one line under the next release.
4. Tag a new patch release so goclaw can pin to it.

No changes to existing behaviour, file layout, or the rest of the API. The whole change should be roughly 25 lines of code + 30 lines of test + a short doc/CHANGELOG update.
