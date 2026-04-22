// Package pptx walks a single PPTX slide XML and emits semantic HTML.
//
// The walker is deliberately small: PowerPoint's slide XML is genuinely
// simpler than Word's document XML, and no open-source Go library offers
// structure-preserving PPTX extraction without a commercial dependency.
// See the architecture plan for details.
//
// The walker is stateless and reads a slide at a time. Per-slide context
// (image relationships, image bytes) is supplied by the caller via
// WalkCtx — the walker emits <img src="rid:<id>" alt=""/> placeholders,
// exactly matching the DOCX walker, so downstream image replacement works
// identically.
package pptx

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ImageRef describes an image referenced from the slide. Populated by the
// walker from blip rIds and resolved by the caller through the slide's
// _rels/slideN.xml.rels.
type ImageRef struct {
	// ID is the rId as it appears on the a:blip r:embed attribute.
	ID string
	// Target is the target path the caller resolved from rels, e.g.
	// "ppt/media/image1.png". Populated by the caller, not the walker.
	Target string
	// Data holds the media bytes once the caller has read them.
	Data []byte
	// MimeType is derived by the caller from the target extension.
	MimeType string
	// Extension includes the leading dot.
	Extension string

	// ContextBefore is the concatenation of the slide's shape text
	// (title + body paragraphs) at the time the image was registered.
	// Cross-slide leakage is deliberately avoided — context is slide-
	// scoped only.
	ContextBefore string

	// AuthorAltText is the p:nvPicPr/p:cNvPr/@descr value (the "alt text"
	// field in PowerPoint's image properties). Empty when the slide
	// author did not supply one.
	AuthorAltText string
}

// WalkCtx carries per-slide state the walker needs: which images were
// encountered, and optional metadata for downstream use. The caller fills
// in Images entries as it resolves rels; the walker only writes blip rIds
// into the map with zero-value refs.
type WalkCtx struct {
	// Images is keyed by rId and populated by the walker. Each entry is
	// a placeholder (zero-valued except for ID) until the caller fills
	// in Target/Data/MimeType/Extension by consulting the slide rels.
	Images map[string]ImageRef
}

// Walk parses a single slide XML payload and returns semantic HTML. The
// enclosing caller is expected to wrap the result in a per-slide <section>
// and join slides with "---\n" separators.
//
// The walker never returns an error of its own; malformed XML is surfaced
// as an error from the underlying stdlib decoder. The caller is free to
// fall back to extractFitz on failure, exactly like the DOCX backend.
func Walk(r io.Reader, ctx *WalkCtx) (string, error) {
	if ctx == nil {
		ctx = &WalkCtx{}
	}
	if ctx.Images == nil {
		ctx.Images = map[string]ImageRef{}
	}

	var slide slideXML
	if err := xml.NewDecoder(r).Decode(&slide); err != nil {
		return "", fmt.Errorf("pptx: decode slide: %w", err)
	}

	// Pre-flatten slide text so each picture can carry it as
	// ContextBefore. Building this once per slide keeps image
	// registration cheap even on slides with dozens of pictures.
	slideText := collectSlideText(&slide)

	var b strings.Builder
	b.WriteString("<section>")
	for _, sp := range slide.CSld.SpTree.Shapes {
		writeShape(&b, &sp, ctx)
	}
	// Picture shapes (p:pic) live alongside p:sp under p:spTree. They
	// carry image references directly — no text body.
	for _, pic := range slide.CSld.SpTree.Pictures {
		rid := pic.BlipFill.Blip.Embed
		if rid == "" {
			continue
		}
		// First-write wins: later slides that reuse the same rId
		// (e.g. a slide-master logo) keep the earlier slide's
		// context, which is the slide where the image first
		// appears in document order.
		if _, seen := ctx.Images[rid]; !seen {
			descr := strings.TrimSpace(pic.NvPicPr.CNvPr.Descr)
			if descr == "" {
				descr = strings.TrimSpace(pic.NvPicPr.CNvPr.Title)
			}
			ctx.Images[rid] = ImageRef{
				ID:            rid,
				ContextBefore: slideText,
				AuthorAltText: descr,
			}
		}
		fmt.Fprintf(&b, `<p><img src="rid:%s" alt=""/></p>`, rid)
	}
	// Graphic frames (p:graphicFrame) wrap tables, charts, and other
	// DrawingML content. Tables are the important case for us; charts
	// are flagged with a comment so downstream consumers know something
	// visual was dropped rather than silently missing it.
	for _, gf := range slide.CSld.SpTree.GraphicFrames {
		writeGraphicFrame(&b, &gf, ctx)
	}
	b.WriteString("</section>")
	return b.String(), nil
}

// collectSlideText flattens every p:sp text body on a slide into one
// compact whitespace-collapsed string. Used as ContextBefore for pictures
// on the slide, so the describer knows it's looking at (say) a slide
// titled "Network architecture" rather than some random unlabelled
// image.
func collectSlideText(slide *slideXML) string {
	var b strings.Builder
	for _, sp := range slide.CSld.SpTree.Shapes {
		for _, p := range sp.TxBody.Paragraphs {
			text := plainParagraphText(&p)
			if text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(text)
		}
	}
	return strings.TrimSpace(b.String())
}

// plainParagraphText returns the text content of a p:p stripped of any
// HTML escaping and run formatting. Runs are concatenated with spaces so
// the result reads naturally when the describer sees it.
func plainParagraphText(p *paragraphXML) string {
	var b strings.Builder
	for _, n := range p.Nodes {
		local := strings.ToLower(n.XMLName.Local)
		if local == "r" || local == "fld" {
			var r runXML
			if err := xml.Unmarshal(n.Raw, &r); err == nil {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(r.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// writeShape handles one p:sp. Title placeholders render as <h2>; other
// text frames render as a sequence of paragraphs. Tables inside shapes
// are handled separately (they live in a graphicFrame sibling, not a
// regular sp, but we surface any a:tbl we find on the shape for
// robustness).
func writeShape(b *strings.Builder, sp *shapeXML, ctx *WalkCtx) {
	isTitle := false
	switch strings.ToLower(sp.NvSpPr.NvPr.Ph.Type) {
	case "title", "ctrtitle":
		isTitle = true
	}

	// Render text body: grouped paragraphs.
	for _, p := range sp.TxBody.Paragraphs {
		text := renderParagraph(&p, ctx)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if isTitle {
			// Collapse the first non-empty title paragraph into
			// an <h2> and treat subsequent paragraphs of the same
			// title shape as regular text lines below it.
			fmt.Fprintf(b, "<h2>%s</h2>", text)
			isTitle = false
			continue
		}
		fmt.Fprintf(b, "<p>%s</p>", text)
	}
}

// writeGraphicFrame handles p:graphicFrame: the container DrawingML uses
// for tables, charts, and other non-shape graphics. We emit tables as
// semantic <table>; charts and other graphic types are replaced with a
// short HTML comment so the reader can tell something was dropped.
func writeGraphicFrame(b *strings.Builder, gf *graphicFrameXML, ctx *WalkCtx) {
	if gf.Graphic.GraphicData.Tbl != nil {
		writeTable(b, gf.Graphic.GraphicData.Tbl, ctx)
		return
	}
	// Fallthrough: chart (c:chart), embedded OLE, or an unknown
	// DrawingML type. Emit a paragraph with a short marker so the
	// reader can tell something visual was dropped rather than
	// silently missing it. Parens avoid triggering html-to-markdown's
	// bracket escaping.
	uri := strings.TrimSpace(gf.Graphic.GraphicData.URI)
	switch {
	case strings.Contains(uri, "/chart"):
		b.WriteString(`<p><em>(chart omitted)</em></p>`)
	case strings.Contains(uri, "/ole"):
		b.WriteString(`<p><em>(embedded object omitted)</em></p>`)
	case uri != "":
		fmt.Fprintf(b, `<p><em>(graphic omitted: %s)</em></p>`, escapeHTML(uri))
	}
}

// writeTable emits a <table> with a single header row from the first
// <a:tr> followed by body rows. PowerPoint tables do not carry an
// explicit "this row is a header" marker, so we follow the convention
// Microsoft markitdown and most hand-crafted markdown converters use:
// promote the first row to <th>. Empty tables are elided entirely.
func writeTable(b *strings.Builder, tbl *tableXML, ctx *WalkCtx) {
	if tbl == nil || len(tbl.Rows) == 0 {
		return
	}
	b.WriteString("<table>")
	for i, row := range tbl.Rows {
		cellTag := "td"
		if i == 0 {
			cellTag = "th"
		}
		b.WriteString("<tr>")
		for _, cell := range row.Cells {
			var cellBody strings.Builder
			for _, p := range cell.TxBody.Paragraphs {
				text := renderParagraph(&p, ctx)
				if strings.TrimSpace(text) == "" {
					continue
				}
				if cellBody.Len() > 0 {
					cellBody.WriteString("<br/>")
				}
				cellBody.WriteString(text)
			}
			fmt.Fprintf(b, "<%s>%s</%s>", cellTag, cellBody.String(), cellTag)
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
}

// renderParagraph serialises <a:p> into HTML inline content. Runs are
// concatenated; <a:br> becomes a hard line break; hyperlinks are emitted
// as <a> with the rId preserved in a data-attribute so the caller can
// rewrite it later if desired.
func renderParagraph(p *paragraphXML, ctx *WalkCtx) string {
	_ = ctx // reserved for future rid resolution of hyperlinks
	var b strings.Builder
	for _, n := range p.Nodes {
		switch strings.ToLower(n.XMLName.Local) {
		case "r":
			// Text run: a:r > a:t
			var r runXML
			if err := xml.Unmarshal(n.Raw, &r); err != nil {
				continue
			}
			if r.RPr.Link.ID != "" {
				fmt.Fprintf(&b, `<a href="rid:%s">%s</a>`, r.RPr.Link.ID, escapeHTML(r.Text))
				continue
			}
			formatted := applyFormatting(escapeHTML(r.Text), &r.RPr)
			b.WriteString(formatted)
		case "br":
			b.WriteString("<br/>")
		case "fld":
			// Field: treat as plain text from the inner a:t.
			var f runXML
			if err := xml.Unmarshal(n.Raw, &f); err == nil {
				b.WriteString(escapeHTML(f.Text))
			}
		}
	}
	return b.String()
}

// applyFormatting wraps rendered text in the HTML tags implied by run
// properties (bold/italic/underline/strike). Ordering is fixed so nested
// tags stay balanced.
func applyFormatting(text string, rpr *runPropsXML) string {
	if rpr == nil {
		return text
	}
	var openSB, closeSB strings.Builder
	var closers []string
	if strings.EqualFold(rpr.B, "1") || strings.EqualFold(rpr.B, "true") {
		openSB.WriteString("<strong>")
		closers = append(closers, "</strong>")
	}
	if strings.EqualFold(rpr.I, "1") || strings.EqualFold(rpr.I, "true") {
		openSB.WriteString("<em>")
		closers = append(closers, "</em>")
	}
	if rpr.U != "" && !strings.EqualFold(rpr.U, "none") {
		openSB.WriteString("<u>")
		closers = append(closers, "</u>")
	}
	if strings.EqualFold(rpr.Strike, "sngStrike") || strings.EqualFold(rpr.Strike, "dblStrike") {
		openSB.WriteString("<s>")
		closers = append(closers, "</s>")
	}
	for i := len(closers) - 1; i >= 0; i-- {
		closeSB.WriteString(closers[i])
	}
	return openSB.String() + text + closeSB.String()
}

// escapeHTML replaces the three PCDATA-special characters.
func escapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(s)
}

// ---------- XML schema structs ----------
//
// These structs cover the subset of DrawingML / PresentationML we need.
// encoding/xml matches by local name and ignores namespaces, which is
// exactly what we want here: slide XML mixes p:, a:, and r: freely.

type slideXML struct {
	XMLName xml.Name    `xml:"sld"`
	CSld    commonSlide `xml:"cSld"`
}

type commonSlide struct {
	SpTree shapeTree `xml:"spTree"`
}

type shapeTree struct {
	Shapes        []shapeXML         `xml:"sp"`
	Pictures      []pictureXML       `xml:"pic"`
	GraphicFrames []graphicFrameXML  `xml:"graphicFrame"`
}

type shapeXML struct {
	NvSpPr nvSpPrXML `xml:"nvSpPr"`
	TxBody txBodyXML `xml:"txBody"`
}

type nvSpPrXML struct {
	NvPr nvPrXML `xml:"nvPr"`
}

type nvPrXML struct {
	Ph placeholderXML `xml:"ph"`
}

type placeholderXML struct {
	Type string `xml:"type,attr"`
}

type txBodyXML struct {
	Paragraphs []paragraphXML `xml:"p"`
}

// paragraphXML captures the raw child elements of <a:p> so we can handle
// them in source order (runs and breaks can interleave). We keep each
// node's raw XML so the per-node unmarshal stays simple.
type paragraphXML struct {
	Nodes []paraNodeXML `xml:",any"`
}

type paraNodeXML struct {
	XMLName xml.Name
	Raw     []byte `xml:",innerxml"`
}

// UnmarshalXML is implemented only to capture the raw bytes of each child
// element (including its opening tag's attributes) so later unmarshal
// passes see correctly-formed XML. The default ",innerxml" tag only gives
// us the element's inner content, which loses the outer attributes we
// need (e.g. run properties). We re-wrap with the original element name.
func (n *paraNodeXML) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	n.XMLName = start.Name
	// Read the element as raw bytes by decoding into a wrapper that
	// captures InnerXML, then re-wrap with the original tag so
	// downstream xml.Unmarshal sees a complete element.
	var body struct {
		Inner string `xml:",innerxml"`
	}
	if err := d.DecodeElement(&body, &start); err != nil {
		return err
	}
	var attrs strings.Builder
	for _, a := range start.Attr {
		fmt.Fprintf(&attrs, ` %s=%q`, a.Name.Local, a.Value)
	}
	n.Raw = []byte(fmt.Sprintf(
		`<%s%s>%s</%s>`,
		start.Name.Local, attrs.String(), body.Inner, start.Name.Local,
	))
	return nil
}

// runXML is the per-run view. We unmarshal each <a:r>/<a:fld> from its
// captured raw bytes.
type runXML struct {
	RPr  runPropsXML `xml:"rPr"`
	Text string      `xml:"t"`
}

type runPropsXML struct {
	B      string       `xml:"b,attr"`
	I      string       `xml:"i,attr"`
	U      string       `xml:"u,attr"`
	Strike string       `xml:"strike,attr"`
	Link   hyperlinkXML `xml:"hlinkClick"`
}

type hyperlinkXML struct {
	ID string `xml:"id,attr"`
}

type pictureXML struct {
	NvPicPr  nvPicPrXML  `xml:"nvPicPr"`
	BlipFill blipFillXML `xml:"blipFill"`
}

// nvPicPrXML captures the cNvPr element holding the author-supplied
// descr ("alt text" in PowerPoint's image properties dialog). We only
// need descr; everything else on cNvPr is ignored.
type nvPicPrXML struct {
	CNvPr cNvPrXML `xml:"cNvPr"`
}

type cNvPrXML struct {
	Descr string `xml:"descr,attr"`
	Title string `xml:"title,attr"`
}

type blipFillXML struct {
	Blip blipXML `xml:"blip"`
}

type blipXML struct {
	Embed string `xml:"embed,attr"`
}

// graphicFrameXML wraps tables and charts. We only dig into a:tbl here;
// charts are surfaced as a "[chart omitted]" marker because the chart
// data lives in a separate chart XML part we deliberately do not walk
// (out of scope for v0.1 per the plan).
type graphicFrameXML struct {
	Graphic graphicXML `xml:"graphic"`
}

type graphicXML struct {
	GraphicData graphicDataXML `xml:"graphicData"`
}

type graphicDataXML struct {
	URI string    `xml:"uri,attr"`
	Tbl *tableXML `xml:"tbl"`
}

// tableXML captures the minimum subset of a:tbl we need: rows and their
// cells. Column widths, grid spans, styling, and header bands are
// deliberately ignored — they would add noise to the markdown without
// improving the LLM-readable content.
type tableXML struct {
	Rows []tableRowXML `xml:"tr"`
}

type tableRowXML struct {
	Cells []tableCellXML `xml:"tc"`
}

type tableCellXML struct {
	TxBody txBodyXML `xml:"txBody"`
}
