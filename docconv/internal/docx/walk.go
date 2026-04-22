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
	// Images is populated by Walk with every image encountered, keyed
	// by rId in document order.
	Images map[string]ImageRef
}

// Walk converts the document body to semantic HTML. Images are replaced with
// placeholder tokens formatted as docconv expects: "![__rid_<id>__]()". The
// orchestrator resolves those placeholders against WalkCtx.Images.
func Walk(ctx *WalkCtx) string {
	if ctx == nil || ctx.Doc == nil {
		return ""
	}
	if ctx.Images == nil {
		ctx.Images = map[string]ImageRef{}
	}

	var b strings.Builder
	items := ctx.Doc.Document.Body.Items

	i := 0
	for i < len(items) {
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
				writeList(&b, items[i:j], ctx)
				i = j
				continue
			}
			writeParagraph(&b, v, ctx)
		case *fumiama.Table:
			writeTable(&b, v, ctx)
		}
		i++
	}
	return b.String()
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
func writeList(b *strings.Builder, items []interface{}, ctx *WalkCtx) {
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
	for _, it := range items {
		p, ok := it.(*fumiama.Paragraph)
		if !ok {
			continue
		}
		b.WriteString("<li>")
		writeRuns(b, p, ctx)
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
	// referenced twice we reuse the same entry — docconv's placeholder
	// replace is resilient to duplicate ids.
	ctx.Images[rID] = ImageRef{
		ID:        rID,
		MediaName: mediaName,
		Data:      media.Data,
		MimeType:  mime,
		Extension: ext,
	}
	return imagePlaceholder(rID)
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
