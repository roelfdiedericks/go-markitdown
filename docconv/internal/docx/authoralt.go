package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// AuthorAltTable maps relationship IDs (the rId on a:blip/@r:embed) to the
// author-supplied alt-text (the descr attribute on wp:docPr). Populated by
// a secondary scan of word/document.xml because fumiama/go-docx's WPDocPr
// struct drops the descr attribute during unmarshal.
//
// Not every drawing carries descr. Missing entries mean "author did not
// annotate this image"; the walker treats that as empty AuthorAltText.
type AuthorAltTable struct {
	ByRelID map[string]string
}

// Lookup returns the author alt-text for an rId or "" if the drawing had
// no descr (or the blip could not be matched to a docPr on the same
// drawing). Safe on nil receiver.
func (a *AuthorAltTable) Lookup(rid string) string {
	if a == nil || a.ByRelID == nil {
		return ""
	}
	return a.ByRelID[rid]
}

// LoadAuthorAlts opens a second zip.Reader over the docx bytes and builds
// an AuthorAltTable by walking word/document.xml. For each <w:drawing>
// block it pairs the inner wp:docPr/@descr (if present) with the first
// a:blip/@r:embed inside the same drawing, which is the correct pairing
// because each drawing contains at most one primary image.
//
// Returns a non-nil table even on parse failure; the library is resilient
// to missing alt-text (it's an optional input to the describer prompt).
func LoadAuthorAlts(data []byte) (*AuthorAltTable, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return &AuthorAltTable{ByRelID: map[string]string{}}, fmt.Errorf("author alts: open zip: %w", err)
	}
	body, err := readNamed(zr, "word/document.xml")
	if err != nil || body == nil {
		return &AuthorAltTable{ByRelID: map[string]string{}}, err
	}

	return parseAuthorAlts(body), nil
}

// parseAuthorAlts walks document.xml with a streaming xml.Decoder. When it
// enters a <drawing> element (namespace-agnostic match on local name), it
// records every descr attribute on docPr and every embed attribute on
// blip. On drawing close, if both were present, it maps embed -> descr.
//
// Streaming keeps memory bounded even on very large documents and lets us
// pair docPr with its sibling blip without building a full AST.
func parseAuthorAlts(body []byte) *AuthorAltTable {
	out := &AuthorAltTable{ByRelID: map[string]string{}}
	dec := xml.NewDecoder(bytes.NewReader(body))

	type drawingCtx struct {
		descrs []string
		embeds []string
	}
	var (
		depth int
		stack []*drawingCtx
	)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Give up gracefully on any XML error — we'd rather
			// have a partial table than abort the whole extract.
			return out
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, "drawing") {
				stack = append(stack, &drawingCtx{})
				depth++
				continue
			}
			if len(stack) == 0 {
				continue
			}
			cur := stack[len(stack)-1]
			if strings.EqualFold(t.Name.Local, "docPr") {
				if v := attr(t.Attr, "descr"); v != "" {
					cur.descrs = append(cur.descrs, v)
				}
			}
			if strings.EqualFold(t.Name.Local, "blip") {
				if v := attr(t.Attr, "embed"); v != "" {
					cur.embeds = append(cur.embeds, v)
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "drawing") && len(stack) > 0 {
				cur := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				depth--
				// Pair up descrs with embeds positionally. Most
				// drawings have one of each; when a drawing has
				// multiple images (rare but allowed), apply each
				// descr to the corresponding blip.
				for i, rid := range cur.embeds {
					if i >= len(cur.descrs) {
						break
					}
					if rid == "" {
						continue
					}
					out.ByRelID[rid] = cur.descrs[i]
				}
			}
		}
	}
	return out
}

// attr returns the value of an attribute matched case-insensitively on
// local name, or "" when absent. Namespace prefix (e.g. r:embed) doesn't
// matter — xml.Decoder exposes the local name to us.
func attr(attrs []xml.Attr, local string) string {
	for _, a := range attrs {
		if strings.EqualFold(a.Name.Local, local) {
			return a.Value
		}
	}
	return ""
}
