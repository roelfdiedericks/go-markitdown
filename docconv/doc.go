// Package docconv converts common document formats (PDF, DOCX, XLSX, PPTX,
// EPUB, MOBI, HTML, plain text) into clean, LLM-ready markdown.
//
// Backend selection:
//   - PDF, EPUB, MOBI use MuPDF via go-fitz (CGO).
//   - DOCX uses fumiama/go-docx for structured parsing; go-fitz acts as a
//     fallback when the native parse fails.
//   - PPTX uses a hand-rolled stdlib-xml walker over ppt/slides/slide*.xml
//     for structured parsing; go-fitz acts as a fallback.
//   - XLSX uses excelize.
//   - HTML goes through go-readability plus html-to-markdown.
//
// Under the "nofitz" build tag, DOCX/PPTX/XLSX/HTML/text all still work;
// PDF/EPUB/MOBI return ErrFitzRequired.
//
// The simplest entry point is Extract:
//
//	md, err := docconv.Extract("report.pdf", nil)
//
// Callers that want image description in the output can plug in an
// ImageDescriber (typically a vision LLM adapter). With OCRFallback set, the
// same interface is also used to transcribe pages of scanned documents that
// have no extractable text.
package docconv
