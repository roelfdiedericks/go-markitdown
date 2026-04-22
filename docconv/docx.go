package docconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	fumiama "github.com/fumiama/go-docx"

	"github.com/roelfdiedericks/go-markitdown/docconv/internal/docx"
	"github.com/roelfdiedericks/go-markitdown/docconv/internal/mdconv"
)

// extractDOCX parses a DOCX with fumiama/go-docx, walks it into semantic
// HTML, and converts the HTML to markdown via mdconv.
//
// Image bytes and hyperlink targets come from the fumiama document directly
// (it loads word/media/* and resolves r:id via (*Docx).ReferTarget). Heading
// styles, list numbering, and core metadata are pulled from a second
// zip.Reader opened over the same buffered bytes because fumiama does not
// expose those zip parts.
//
// If the fumiama parse fails (malformed document, or fumiama's strict
// non-rId rejection in parseDocRelation), the orchestrator falls back to
// the go-fitz path via extractFitzFallback so we never regress output
// against the pre-native-backend behaviour.
func extractDOCX(ctx context.Context, data []byte, opts Options) (string, Metadata, error) {
	doc, err := fumiama.Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if md, meta, ferr := extractFitzFallback(ctx, data, FormatDOCX, opts); ferr == nil {
			return md, meta, nil
		} else if !errors.Is(ferr, ErrFitzRequired) {
			// Preserve original parse error plus fallback error.
			return "", Metadata{Format: FormatDOCX.String()},
				fmt.Errorf("docx: parse failed (%w); fitz fallback also failed: %v", err, ferr)
		}
		return "", Metadata{Format: FormatDOCX.String()}, wrapBackendError(FormatDOCX, err)
	}

	styles, lists, props, _ := docx.LoadExtras(data)
	authorAlts, _ := docx.LoadAuthorAlts(data)
	notes, _ := docx.LoadNotes(data)
	noteRefs, _ := docx.LoadNoteRefs(data)

	// Reviewer comments are opt-in (Options.IncludeComments). When off,
	// we still read them cheaply via a nil CommentRefIndex — parsing is
	// O(notes) and the table is dropped immediately.
	var commentRefs *docx.CommentRefIndex
	if opts.IncludeComments {
		if commentTable, cerr := docx.LoadComments(data); cerr == nil && !commentTable.Empty() {
			commentRefs, _ = docx.LoadCommentRefs(data, commentTable)
		}
	}

	walk := &docx.WalkCtx{
		Doc:        doc,
		Styles:     styles,
		Lists:      lists,
		AuthorAlts: authorAlts,
		NoteRefs:   noteRefs,
		Comments:   commentRefs,
		Images:     map[string]docx.ImageRef{},
	}
	htmlBody := docx.Walk(walk)

	md, err := mdconv.Convert(htmlBody)
	if err != nil {
		return "", Metadata{Format: FormatDOCX.String()}, wrapBackendError(FormatDOCX, fmt.Errorf("markdown: %w", err))
	}

	// If the whole document came back empty, either route through OCR
	// fallback (which uses go-fitz to render pages) or surface ErrNoText.
	if strings.TrimSpace(md) == "" {
		if opts.OCRFallback && opts.LLMClient != nil {
			ocrMD, meta, ferr := extractFitzFallback(ctx, data, FormatDOCX, opts)
			if ferr != nil {
				return "", metaFromProps(props, FormatDOCX), wrapBackendError(FormatDOCX, ferr)
			}
			return ocrMD, meta, nil
		}
		return "", metaFromProps(props, FormatDOCX), ErrNoText
	}

	// Replace image placeholder tokens with final markdown references
	// (optionally with describer-generated alt-text).
	images := make(map[string]extractedImage, len(walk.Images))
	i := 0
	for id, ref := range walk.Images {
		images[id] = extractedImage{
			ID:            ref.ID,
			Index:         i,
			Data:          ref.Data,
			MimeType:      ref.MimeType,
			Extension:     ref.Extension,
			ContextBefore: ref.ContextBefore,
			ContextAfter:  ref.ContextAfter,
			AuthorAltText: ref.AuthorAltText,
		}
		i++
	}
	md, err = replaceImagePlaceholders(ctx, md, images, opts)
	if err != nil {
		return "", metaFromProps(props, FormatDOCX), wrapBackendError(FormatDOCX, err)
	}

	// Append GFM footnote definitions last. References inline in the
	// body point back here; renderers that support footnotes link the
	// two, renderers that don't show them as plain "[^fn-1]" tokens.
	// Either way the LLM reader sees the association.
	if !notes.Empty() {
		md = strings.TrimRight(md, "\n") + "\n\n" + notes.RenderNoteBlock()
	}

	return strings.TrimSpace(md), metaFromProps(props, FormatDOCX), nil
}

// metaFromProps converts the docx.DocProps bundle (nil-safe) to the docconv
// Metadata shape.
func metaFromProps(p *docx.DocProps, format Format) Metadata {
	m := Metadata{Format: format.String()}
	if p == nil {
		return m
	}
	m.Title = p.Title
	m.Author = p.Author
	m.Subject = p.Subject
	m.Keywords = p.Keywords
	m.Created = p.Created
	m.Modified = p.Modified
	return m
}
