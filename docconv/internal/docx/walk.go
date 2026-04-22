package docx

import (
	"fmt"
	pathpkg "path"
	"strings"

	fumiama "github.com/fumiama/go-docx"
)

// ImageRef carries everything the walker knows about an embedded image so
// that the orchestrator can emit descriptions, write files, etc.
type ImageRef struct {
	// ID is the rId from word/_rels/document.xml.rels.
	ID string
	// MediaName is the file name under word/media/ ("image1.png").
	MediaName string
	// Data is the raw encoded image bytes.
	Data []byte
	// MimeType is derived from the file extension.
	MimeType string
	// Extension includes the leading dot (".png").
	Extension string

	// ContextBefore / ContextAfter are short plaintext windows of the
	// neighbouring paragraphs / table cells at the time the image was
	// encountered, flowing through to the default describer prompt.
	ContextBefore string
	ContextAfter  string

	// AuthorAltText is the wp:docPr/@descr attribute for this drawing,
	// when the document author supplied one. Empty when the drawing has
	// no descr.
	AuthorAltText string
}

// WalkCtx holds everything the walker needs across a single call. Callers
// build the context, pass it to Walk, and then read out ctx.Images to get
// the full set of referenced images in document order.
type WalkCtx struct {
	// Doc is the parsed fumiama document.
	Doc *fumiama.Docx
	// Styles resolves w:pStyle to heading levels. Optional.
	Styles *StyleTable
	// Lists resolves numPr to bullet/ordered format. Optional.
	Lists *ListTable
	// AuthorAlts resolves rId -> wp:docPr/@descr. Optional; a nil value
	// makes Lookup return "" for every rId.
	AuthorAlts *AuthorAltTable
	// NoteRefs maps body-item index to in-body foot/end-note refs, so
	// the walker can splice [^fn-N] / [^en-N] anchors at the end of
	// each paragraph/table containing them. Optional.
	NoteRefs *NoteRefIndex
	// Comments maps body-item index to reviewer comments anchored at
	// that item. Optional; when non-nil, the walker emits HTML
	// comments after the item's HTML so they survive mdconv.
	Comments *CommentRefIndex
	// Images is populated by Walk with every image encountered, keyed
	// by rId in document order.
	Images map[string]ImageRef

	// itemTexts mirrors Doc.Body.Items by index, pre-flattened to
	// plaintext once at the top of Walk. Image context windows are
	// assembled by concatenating neighbouring entries, which keeps
	// per-image work linear in window size rather than quadratic in
	// document size.
	itemTexts []string
	// currentItem is the index of the item currently being walked.
	// Updated by the outer loop and read by extractImage so the image
	// knows where it sits in document order.
	currentItem int
}

// contextWindowChars is the maximum chars on each side of an image the
// walker assembles for ContextBefore/ContextAfter. Enough to capture the
// surrounding paragraph or table cell plus immediate neighbours; short
// enough to keep the describer prompt compact.
const contextWindowChars = 400

// Walk converts the document body to semantic HTML. Images are replaced
// with <img src="rid:<id>" alt=""/> placeholders; the orchestrator
// resolves those against WalkCtx.Images after mdconv has run.
func Walk(ctx *WalkCtx) string {
	if ctx == nil || ctx.Doc == nil {
		return ""
	}
	if ctx.Images == nil {
		ctx.Images = map[string]ImageRef{}
	}

	var b strings.Builder
	items := ctx.Doc.Document.Body.Items

	// Precompute the plaintext form of every item so extractImage can
	// assemble context windows in O(window) rather than re-flattening
	// the whole body for each image.
	ctx.itemTexts = make([]string, len(items))
	for idx, it := range items {
		ctx.itemTexts[idx] = flattenItemText(it)
	}

	i := 0
	for i < len(items) {
		ctx.currentItem = i
		switch v := items[i].(type) {
		case *fumiama.Paragraph:
			// Group consecutive list-item paragraphs sharing the
			// same numId into one list. When the numId changes we
			// start a fresh <ol>/<ul>, because a numId switch in
			// OOXML means "different list" — this is how the
			// "1,2,3,4,5 / bullet,bullet / 1" pattern from the
			// sample fixture stays faithful to the source.
			if isListItem(v) {
				headID := listNumID(v)
				j := i + 1
				for j < len(items) {
					p, ok := items[j].(*fumiama.Paragraph)
					if !ok || !isListItem(p) || listNumID(p) != headID {
						break
					}
					j++
				}
				writeList(&b, items[i:j], ctx, i)
				i = j
				continue
			}
			writeParagraph(&b, v, ctx)
			writeItemAnnotations(&b, ctx, i)
		case *fumiama.Table:
			writeTable(&b, v, ctx)
			writeItemAnnotations(&b, ctx, i)
		}
		i++
	}
	return b.String()
}

// writeItemAnnotations appends any reviewer-comment HTML comments and
// foot/end-note reference anchors registered for this body item. Emitted
// AFTER the item's HTML so the anchors sit at the tail of the paragraph
// when rendered — close enough to the actual in-body position that they
// still associate with the right content block for a human or LLM reader.
//
// Comments come first (so reviewer context sits flush against the
// paragraph it annotated); note anchors follow (so they appear right
// before the paragraph break, mirroring how Word renders them inline).
func writeItemAnnotations(b *strings.Builder, ctx *WalkCtx, idx int) {
	if ctx.Comments != nil {
		for _, c := range ctx.Comments.At(idx) {
			// HTML comments survive html-to-markdown untouched,
			// which is what we want: invisible to rendered
			// markdown viewers, visible to LLM tokenisers.
			b.WriteString(c.HTMLComment())
		}
	}
	refs := ctx.NoteRefs.Refs(idx)
	for _, r := range refs {
		fmt.Fprintf(b, "[^%s]", r.Anchor())
	}
}

// listNumID returns the w:numId of a list-item paragraph, or "" when the
// paragraph has no numbering properties. Used to split the contiguous
// list-item run into one <ol>/<ul> per distinct numId.
func listNumID(p *fumiama.Paragraph) string {
	if p == nil || p.Properties == nil || p.Properties.NumProperties == nil || p.Properties.NumProperties.NumID == nil {
		return ""
	}
	return p.Properties.NumProperties.NumID.Val
}

// isListItem returns true when the paragraph has numPr (w:numPr) properties.
// Paragraphs carrying numPr are list items regardless of whether we can
// resolve the numbering format.
func isListItem(p *fumiama.Paragraph) bool {
	return p != nil &&
		p.Properties != nil &&
		p.Properties.NumProperties != nil &&
		p.Properties.NumProperties.NumID != nil
}

// writeList emits an <ul> / <ol> block for a contiguous run of list-item
// paragraphs. When the numbering format is unknown the list is rendered as
// <ul> for safety (markdown bullets are almost always acceptable).
//
// startIdx is the absolute index of items[0] inside the body, used to keep
// ctx.currentItem accurate while iterating the group (images inside list
// items should see their correct neighbours, not the last value the outer
// loop set).
func writeList(b *strings.Builder, items []interface{}, ctx *WalkCtx, startIdx int) {
	ordered := false
	// Look at the first item's numPr to pick ul vs ol.
	if first, ok := items[0].(*fumiama.Paragraph); ok && first.Properties != nil {
		np := first.Properties.NumProperties
		if np != nil && np.NumID != nil {
			ilvl := ""
			if np.Ilvl != nil {
				ilvl = np.Ilvl.Val
			}
			if f, ok := ctx.Lists.Format(np.NumID.Val, ilvl); ok {
				ordered = f.Ordered
			}
		}
	}

	if ordered {
		b.WriteString("<ol>")
	} else {
		b.WriteString("<ul>")
	}
	for off, it := range items {
		p, ok := it.(*fumiama.Paragraph)
		if !ok {
			continue
		}
		absIdx := startIdx + off
		ctx.currentItem = absIdx
		b.WriteString("<li>")
		writeRuns(b, p, ctx)
		// Splice per-item annotations inside the <li> so each list
		// entry carries its own notes/comments rather than all the
		// list's annotations bunching up at the end.
		writeItemAnnotations(b, ctx, absIdx)
		b.WriteString("</li>")
	}
	if ordered {
		b.WriteString("</ol>")
	} else {
		b.WriteString("</ul>")
	}
}

// writeParagraph emits a <p> or heading tag for a single paragraph.
func writeParagraph(b *strings.Builder, p *fumiama.Paragraph, ctx *WalkCtx) {
	level := paragraphHeadingLevel(p, ctx)
	openTag := "p"
	if level > 0 {
		openTag = fmt.Sprintf("h%d", level)
	}

	// Skip entirely empty paragraphs (very common in OOXML as visual
	// spacers) rather than emitting empty <p></p> which bloats output.
	var inner strings.Builder
	writeRuns(&inner, p, ctx)
	text := strings.TrimSpace(inner.String())
	if text == "" {
		return
	}

	fmt.Fprintf(b, "<%s>%s</%s>", openTag, inner.String(), openTag)
}

// paragraphHeadingLevel returns the heading level (1-6) for a paragraph, or 0
// for a non-heading. Falls through the style table before pattern-matching
// the raw style id as a last resort.
func paragraphHeadingLevel(p *fumiama.Paragraph, ctx *WalkCtx) int {
	if p == nil || p.Properties == nil || p.Properties.Style == nil {
		return 0
	}
	id := p.Properties.Style.Val
	if lvl := ctx.Styles.HeadingLevel(id); lvl > 0 {
		return lvl
	}
	// Fallback: style table may be nil/empty if styles.xml was unreadable.
	if lvl := matchHeadingPattern(id); lvl > 0 {
		return clampHeading(lvl)
	}
	return 0
}

// writeRuns iterates a paragraph's children and emits their HTML form. Runs
// adjacent to each other with the same formatting stack stay adjacent: we do
// not insert whitespace between them.
func writeRuns(b *strings.Builder, p *fumiama.Paragraph, ctx *WalkCtx) {
	for _, c := range p.Children {
		switch v := c.(type) {
		case *fumiama.Hyperlink:
			writeHyperlink(b, v, ctx)
		case *fumiama.Run:
			writeRun(b, v, ctx)
		}
	}
}

// writeRun emits one <w:r> worth of content, wrapping it in formatting tags
// as indicated by rPr.
func writeRun(b *strings.Builder, r *fumiama.Run, ctx *WalkCtx) {
	open, close := formattingTags(r.RunProperties)
	b.WriteString(open)
	for _, c := range r.Children {
		switch v := c.(type) {
		case *fumiama.Text:
			b.WriteString(escapeHTML(v.Text))
		case *fumiama.Tab:
			b.WriteString("    ")
		case *fumiama.BarterRabbet:
			b.WriteString("<br/>")
		case *fumiama.Drawing:
			if img := extractImage(v, ctx); img != "" {
				b.WriteString(img)
			}
		}
	}
	b.WriteString(close)
}

// writeHyperlink emits an <a> tag. We resolve the link target via the
// fumiama document's ReferTarget helper. Anchor-only links (same-document
// references) retain the anchor text as the href with a leading "#".
func writeHyperlink(b *strings.Builder, h *fumiama.Hyperlink, ctx *WalkCtx) {
	href := ""
	if h.ID != "" {
		if target, err := ctx.Doc.ReferTarget(h.ID); err == nil {
			href = target
		}
	}
	// Collect hyperlink body text. The Run carried by the hyperlink is
	// the visible label.
	var body strings.Builder
	writeRun(&body, &h.Run, ctx)
	text := body.String()
	if text == "" {
		text = escapeHTML(href)
	}

	if href == "" {
		b.WriteString(text)
		return
	}
	fmt.Fprintf(b, `<a href="%s">%s</a>`, escapeAttr(href), text)
}

// formattingTags returns the (open, close) HTML snippets implied by the run
// properties. The ordering is fixed so nested tags are balanced.
func formattingTags(rp *fumiama.RunProperties) (string, string) {
	if rp == nil {
		return "", ""
	}
	var openSB, closeSB strings.Builder
	var closers []string
	if rp.Bold != nil {
		openSB.WriteString("<strong>")
		closers = append(closers, "</strong>")
	}
	if rp.Italic != nil {
		openSB.WriteString("<em>")
		closers = append(closers, "</em>")
	}
	if rp.Underline != nil && !strings.EqualFold(rp.Underline.Val, "none") {
		openSB.WriteString("<u>")
		closers = append(closers, "</u>")
	}
	if rp.Strike != nil {
		openSB.WriteString("<s>")
		closers = append(closers, "</s>")
	}
	for i := len(closers) - 1; i >= 0; i-- {
		closeSB.WriteString(closers[i])
	}
	return openSB.String(), closeSB.String()
}

// extractImage walks a Drawing to its underlying ABlip, resolves the rId
// through the document relationships, fetches the media bytes from fumiama's
// in-memory media cache, stashes an ImageRef in ctx.Images, and returns the
// placeholder token for the markdown body.
func extractImage(d *fumiama.Drawing, ctx *WalkCtx) string {
	var blip *fumiama.ABlip
	switch {
	case d.Inline != nil && d.Inline.Graphic != nil && d.Inline.Graphic.GraphicData != nil && d.Inline.Graphic.GraphicData.Pic != nil && d.Inline.Graphic.GraphicData.Pic.BlipFill != nil:
		blip = &d.Inline.Graphic.GraphicData.Pic.BlipFill.Blip
	case d.Anchor != nil && d.Anchor.Graphic != nil && d.Anchor.Graphic.GraphicData != nil && d.Anchor.Graphic.GraphicData.Pic != nil && d.Anchor.Graphic.GraphicData.Pic.BlipFill != nil:
		blip = &d.Anchor.Graphic.GraphicData.Pic.BlipFill.Blip
	}
	if blip == nil || blip.Embed == "" {
		return ""
	}

	rID := blip.Embed
	target, err := ctx.Doc.ReferTarget(rID)
	if err != nil || target == "" {
		return ""
	}

	// Targets in word/_rels/document.xml.rels are relative to the word/
	// directory, e.g. "media/image1.png". fumiama keys its in-memory
	// media cache on just the filename, so strip any leading directory.
	mediaName := pathpkg.Base(target)
	media := ctx.Doc.Media(mediaName)
	if media == nil {
		return ""
	}

	ext := strings.ToLower(pathpkg.Ext(mediaName))
	mime := mimeForExt(ext)

	// Use the rId directly as the placeholder key. If the same image is
	// referenced twice we reuse the first entry — the shared image
	// pipeline dedupes describer calls per ID, so repeated logos cost
	// one describer call regardless of how often they appear.
	if _, seen := ctx.Images[rID]; !seen {
		before, after := contextAroundItem(ctx.itemTexts, ctx.currentItem, contextWindowChars)
		ctx.Images[rID] = ImageRef{
			ID:            rID,
			MediaName:     mediaName,
			Data:          media.Data,
			MimeType:      mime,
			Extension:     ext,
			ContextBefore: before,
			ContextAfter:  after,
			AuthorAltText: ctx.AuthorAlts.Lookup(rID),
		}
	}
	return imagePlaceholder(rID)
}

// contextAroundItem returns up to budget chars of neighbouring text on
// each side of idx, drawn from itemTexts. The current item itself appears
// in both before and after — an image inside paragraph P gets P's text on
// both sides as a grounding hint, plus adjacent paragraphs for flow.
//
// Gracefully handles out-of-range indexes by returning empty strings.
func contextAroundItem(itemTexts []string, idx, budget int) (string, string) {
	if idx < 0 || idx >= len(itemTexts) {
		return "", ""
	}
	var beforeParts []string
	remaining := budget
	beforeParts = append(beforeParts, itemTexts[idx])
	remaining -= len(itemTexts[idx])
	for j := idx - 1; j >= 0 && remaining > 0; j-- {
		t := itemTexts[j]
		if t == "" {
			continue
		}
		beforeParts = append([]string{t}, beforeParts...)
		remaining -= len(t)
	}
	before := strings.TrimSpace(strings.Join(beforeParts, " "))
	before = truncateTail(before, budget)

	var afterParts []string
	remaining = budget
	afterParts = append(afterParts, itemTexts[idx])
	remaining -= len(itemTexts[idx])
	for j := idx + 1; j < len(itemTexts) && remaining > 0; j++ {
		t := itemTexts[j]
		if t == "" {
			continue
		}
		afterParts = append(afterParts, t)
		remaining -= len(t)
	}
	after := strings.TrimSpace(strings.Join(afterParts, " "))
	after = truncateHead(after, budget)
	return before, after
}

// truncateTail keeps the last budget chars of s, trimming on a word
// boundary when the cut drops into a long word run.
func truncateTail(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	tail := s[len(s)-budget:]
	if space := strings.IndexByte(tail, ' '); space != -1 && space < len(tail)/4 {
		tail = tail[space+1:]
	}
	return tail
}

// truncateHead keeps the first budget chars of s, trimming on a word
// boundary when the cut drops into a long word run.
func truncateHead(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	head := s[:budget]
	if space := strings.LastIndexByte(head, ' '); space != -1 && space > len(head)*3/4 {
		head = head[:space]
	}
	return head
}

// flattenItemText returns a compact plaintext view of an items slot
// (paragraph or table). Image runs are skipped so the context window
// never mentions the placeholder text. Whitespace is collapsed.
func flattenItemText(item interface{}) string {
	var b strings.Builder
	switch v := item.(type) {
	case *fumiama.Paragraph:
		flattenParagraphText(&b, v)
	case *fumiama.Table:
		for i, row := range v.TableRows {
			if i > 0 {
				b.WriteByte(' ')
			}
			for j, cell := range row.TableCells {
				if j > 0 {
					b.WriteString(" | ")
				}
				for k, p := range cell.Paragraphs {
					if k > 0 {
						b.WriteByte(' ')
					}
					flattenParagraphText(&b, p)
				}
			}
		}
	}
	return collapseWhitespace(b.String())
}

func flattenParagraphText(b *strings.Builder, p *fumiama.Paragraph) {
	if p == nil {
		return
	}
	for _, c := range p.Children {
		switch v := c.(type) {
		case *fumiama.Hyperlink:
			for _, rc := range v.Run.Children {
				if t, ok := rc.(*fumiama.Text); ok {
					b.WriteString(t.Text)
					b.WriteByte(' ')
				}
			}
		case *fumiama.Run:
			for _, rc := range v.Children {
				if t, ok := rc.(*fumiama.Text); ok {
					b.WriteString(t.Text)
					b.WriteByte(' ')
				}
			}
		}
	}
}

// collapseWhitespace compresses any run of whitespace (including newlines
// and tabs) to a single space and trims the ends. Keeps the plaintext
// context windows compact and readable.
func collapseWhitespace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

// imagePlaceholder emits the HTML img element that the shared placeholder
// post-pass (see docconv/images.go) matches after html-to-markdown has run.
// We emit an <img> with src="rid:<id>" so the rId survives intact through
// markdown conversion. alt is left empty so the post-pass can fill it with
// either a describer-produced caption or a fallback label.
func imagePlaceholder(id string) string {
	return fmt.Sprintf(`<img src="rid:%s" alt=""/>`, id)
}

func mimeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".webp":
		return "image/webp"
	case ".tif", ".tiff":
		return "image/tiff"
	}
	return "application/octet-stream"
}

// writeTable emits a <table> with the first row as <thead>/<th>, which
// html-to-markdown renders as a pipe-table header. Nested tables inside
// cells are flattened to their inner text — one level of table nesting is
// out of scope for v0.1.
func writeTable(b *strings.Builder, t *fumiama.Table, ctx *WalkCtx) {
	if len(t.TableRows) == 0 {
		return
	}
	b.WriteString("<table>")
	for idx, row := range t.TableRows {
		openRowTag := "tr"
		cellTag := "td"
		if idx == 0 {
			// Wrap the first row as <thead> so html-to-markdown
			// emits a proper markdown header line.
			b.WriteString("<thead><tr>")
			cellTag = "th"
		} else if idx == 1 {
			// After the first row, switch to tbody.
			b.WriteString("<tbody>")
			b.WriteString("<")
			b.WriteString(openRowTag)
			b.WriteString(">")
		} else {
			b.WriteString("<")
			b.WriteString(openRowTag)
			b.WriteString(">")
		}
		for _, cell := range row.TableCells {
			b.WriteString("<")
			b.WriteString(cellTag)
			b.WriteString(">")
			writeCell(b, cell, ctx)
			b.WriteString("</")
			b.WriteString(cellTag)
			b.WriteString(">")
		}
		if idx == 0 {
			b.WriteString("</tr></thead>")
		} else {
			b.WriteString("</")
			b.WriteString(openRowTag)
			b.WriteString(">")
		}
	}
	// Close tbody if we opened one (tables with more than one row).
	if len(t.TableRows) > 1 {
		b.WriteString("</tbody>")
	}
	b.WriteString("</table>")
}

// writeCell emits the inline content of a single <w:tc>. Paragraphs inside
// cells are flattened into their runs with <br/> separators between them;
// cell-level block structure (multiple <p>) is a layout hint, not content.
func writeCell(b *strings.Builder, cell *fumiama.WTableCell, ctx *WalkCtx) {
	for i, p := range cell.Paragraphs {
		if i > 0 {
			b.WriteString("<br/>")
		}
		writeRuns(b, p, ctx)
	}
	// We deliberately ignore nested tables inside cells for v0.1 — see
	// Scope in the plan.
}

// escapeHTML replaces the five characters that matter inside PCDATA.
func escapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(s)
}

// escapeAttr is like escapeHTML but also quotes double-quotes for use inside
// attribute values.
func escapeAttr(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return replacer.Replace(s)
}
