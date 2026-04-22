package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

// FootnoteKind distinguishes foot- from end- notes so we can namespace
// their markdown anchors ("fn-" vs "en-") and render separate GFM
// footnote blocks at the end of the document.
type FootnoteKind int

const (
	Footnote FootnoteKind = iota
	Endnote
)

// Note is one extracted footnote or endnote, ready to be rendered as GFM.
type Note struct {
	// Kind is Footnote or Endnote.
	Kind FootnoteKind
	// ID is the w:id attribute on <w:footnote>/<w:endnote>. We use it as
	// the stable key for the anchor (rendered as [^fn-<ID>]).
	ID string
	// Text is the flattened plaintext body of the note. Newlines inside
	// are collapsed to spaces so the note fits on a single line of GFM
	// "[^fn-N]: ..." syntax.
	Text string
}

// Anchor returns the GFM anchor form for this note: "fn-<ID>" or
// "en-<ID>". The "^" and brackets are added by callers.
func (n Note) Anchor() string {
	if n.Kind == Endnote {
		return "en-" + n.ID
	}
	return "fn-" + n.ID
}

// NoteTable holds every foot- and end- note in a document, keyed by
// (kind, id). References in the body (emitted by the walker as
// [^fn-<id>] / [^en-<id>]) resolve against this table at output time.
type NoteTable struct {
	Notes map[string]Note
}

// Empty reports whether the table has nothing worth rendering.
func (t *NoteTable) Empty() bool {
	return t == nil || len(t.Notes) == 0
}

// noteRefKey is the map key for NoteTable: combines kind and id so the
// same numeric id across footnotes and endnotes does not collide.
func noteRefKey(k FootnoteKind, id string) string {
	if k == Endnote {
		return "en:" + id
	}
	return "fn:" + id
}

// Lookup returns the note registered for (kind, id), or (zero, false).
func (t *NoteTable) Lookup(k FootnoteKind, id string) (Note, bool) {
	if t == nil || t.Notes == nil {
		return Note{}, false
	}
	n, ok := t.Notes[noteRefKey(k, id)]
	return n, ok
}

// NoteRef is a single in-body reference to a note, carrying the
// pre-built GFM anchor ("fn-3", "en-1") so consumers don't need to
// re-derive it.
type NoteRef struct {
	Kind FootnoteKind
	ID   string
}

// Anchor returns the GFM anchor for the reference: "fn-<ID>" / "en-<ID>".
func (r NoteRef) Anchor() string {
	if r.Kind == Endnote {
		return "en-" + r.ID
	}
	return "fn-" + r.ID
}

// NoteRefIndex maps a body item index (0-based position in
// Doc.Body.Items) to the list of note references contained within that
// item, in source order. Tables count as a single item even when notes
// appear inside cells.
type NoteRefIndex struct {
	ByItem map[int][]NoteRef
}

// Empty reports whether the index has nothing to splice.
func (i *NoteRefIndex) Empty() bool {
	return i == nil || len(i.ByItem) == 0
}

// Refs returns the references for item idx, or nil. Safe on nil receiver.
func (i *NoteRefIndex) Refs(idx int) []NoteRef {
	if i == nil || i.ByItem == nil {
		return nil
	}
	return i.ByItem[idx]
}

// LoadNoteRefs streams word/document.xml and builds a NoteRefIndex by
// counting the body-level paragraph/table elements and collecting every
// w:footnoteReference / w:endnoteReference inside each.
//
// Body-level indexing matches fumiama's Body.Items order (paragraphs and
// tables interleaved in source order), which is what the walker uses for
// ctx.currentItem.
func LoadNoteRefs(data []byte) (*NoteRefIndex, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return &NoteRefIndex{ByItem: map[int][]NoteRef{}}, fmt.Errorf("note refs: open zip: %w", err)
	}
	body, _ := readNamed(zr, "word/document.xml")
	if body == nil {
		return &NoteRefIndex{ByItem: map[int][]NoteRef{}}, nil
	}
	return parseNoteRefs(body), nil
}

// parseNoteRefs scans document.xml body children. Whenever it enters a
// <w:body>-child element (p or tbl), it starts a new item bucket and
// collects every footnote/endnote reference discovered up to the matching
// end element, then increments the item index.
func parseNoteRefs(body []byte) *NoteRefIndex {
	idx := &NoteRefIndex{ByItem: map[int][]NoteRef{}}
	dec := xml.NewDecoder(bytes.NewReader(body))

	var (
		inBody  bool
		itemIdx = -1
		// depth of item wrapper (p or tbl) we are currently inside.
		// 0 means "not inside an item"; >0 while inside so nested
		// structures (tables > rows > cells > paragraphs) stay
		// attributed to the outer body item.
		itemDepth int
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			if !inBody {
				if local == "body" {
					inBody = true
				}
				continue
			}
			if itemDepth == 0 {
				if local == "p" || local == "tbl" {
					itemIdx++
					itemDepth = 1
				}
				continue
			}
			itemDepth++
			if local == "footnotereference" {
				id := attr(t.Attr, "id")
				if id != "" {
					idx.ByItem[itemIdx] = append(idx.ByItem[itemIdx], NoteRef{Kind: Footnote, ID: id})
				}
			}
			if local == "endnotereference" {
				id := attr(t.Attr, "id")
				if id != "" {
					idx.ByItem[itemIdx] = append(idx.ByItem[itemIdx], NoteRef{Kind: Endnote, ID: id})
				}
			}
		case xml.EndElement:
			if !inBody {
				continue
			}
			if itemDepth > 0 {
				itemDepth--
			}
			if strings.ToLower(t.Name.Local) == "body" {
				inBody = false
			}
		}
	}
	return idx
}

// LoadNotes reads word/footnotes.xml and word/endnotes.xml from the docx
// bytes and returns a combined NoteTable. Separator and continuation
// notes (type="separator"/"continuationSeparator") are filtered out;
// Word's convention for their ids is 0 and -1, and we also drop any note
// whose body is effectively empty. Returns a non-nil, possibly empty
// table on any error.
func LoadNotes(data []byte) (*NoteTable, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return &NoteTable{Notes: map[string]Note{}}, fmt.Errorf("notes: open zip: %w", err)
	}
	tbl := &NoteTable{Notes: map[string]Note{}}
	if body, _ := readNamed(zr, "word/footnotes.xml"); body != nil {
		for _, n := range parseNotes(body, "footnote", Footnote) {
			tbl.Notes[noteRefKey(n.Kind, n.ID)] = n
		}
	}
	if body, _ := readNamed(zr, "word/endnotes.xml"); body != nil {
		for _, n := range parseNotes(body, "endnote", Endnote) {
			tbl.Notes[noteRefKey(n.Kind, n.ID)] = n
		}
	}
	return tbl, nil
}

// parseNotes streams through a footnotes.xml / endnotes.xml body and
// returns every non-separator note with its flattened text. elemName is
// the local element name ("footnote" or "endnote") inside the document.
func parseNotes(body []byte, elemName string, kind FootnoteKind) []Note {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var (
		out       []Note
		current   *Note
		textBuf   strings.Builder
		inCurrent bool
		inText    bool
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case strings.ToLower(elemName):
				// Skip separators: Word uses type="separator" or
				// "continuationSeparator" for the horizontal
				// rule + continuation separator; their bodies
				// have no content anyway but we filter on type
				// to be safe.
				noteType := attr(t.Attr, "type")
				if strings.EqualFold(noteType, "separator") ||
					strings.EqualFold(noteType, "continuationSeparator") {
					_ = dec.Skip()
					continue
				}
				id := attr(t.Attr, "id")
				current = &Note{Kind: kind, ID: id}
				textBuf.Reset()
				inCurrent = true
			case "t":
				if inCurrent {
					inText = true
				}
			case "p":
				// Paragraph boundary inside a note: separate
				// blocks with a single space so multi-paragraph
				// notes come out as one compact line.
				if inCurrent && textBuf.Len() > 0 {
					textBuf.WriteByte(' ')
				}
			case "tab":
				if inCurrent {
					textBuf.WriteByte(' ')
				}
			}
		case xml.CharData:
			if inText {
				textBuf.Write(t)
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case strings.ToLower(elemName):
				if current != nil {
					text := collapseWhitespace(textBuf.String())
					if text != "" {
						current.Text = text
						out = append(out, *current)
					}
					current = nil
					inCurrent = false
				}
			case "t":
				inText = false
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return noteIDLess(out[i].ID, out[j].ID)
	})
	return out
}

// noteIDLess orders note IDs numerically when both parse as ints,
// lexicographically otherwise. Word writes 0, 1, 2, 10 which would
// alphabetise as 0, 1, 10, 2 and misorder the GFM block otherwise.
func noteIDLess(a, b string) bool {
	ai, aok := tryAtoi(a)
	bi, bok := tryAtoi(b)
	if aok && bok {
		return ai < bi
	}
	return a < b
}

func tryAtoi(s string) (int, bool) {
	n := 0
	neg := false
	i := 0
	if len(s) > 0 && s[0] == '-' {
		neg = true
		i = 1
	}
	if i == len(s) {
		return 0, false
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

// RenderNoteBlock returns the GFM footnote definition block for every
// note in the table, in numeric id order with footnotes first, then
// endnotes. Empty tables produce an empty string.
//
// Format:
//
//	[^fn-1]: The text of footnote 1.
//	[^fn-2]: The text of footnote 2.
//	[^en-1]: The text of endnote 1.
func (t *NoteTable) RenderNoteBlock() string {
	if t.Empty() {
		return ""
	}
	var (
		footnotes []Note
		endnotes  []Note
	)
	for _, n := range t.Notes {
		if n.Kind == Endnote {
			endnotes = append(endnotes, n)
		} else {
			footnotes = append(footnotes, n)
		}
	}
	sort.SliceStable(footnotes, func(i, j int) bool { return noteIDLess(footnotes[i].ID, footnotes[j].ID) })
	sort.SliceStable(endnotes, func(i, j int) bool { return noteIDLess(endnotes[i].ID, endnotes[j].ID) })

	var b strings.Builder
	emit := func(n Note) {
		fmt.Fprintf(&b, "[^%s]: %s\n", n.Anchor(), n.Text)
	}
	for _, n := range footnotes {
		emit(n)
	}
	for _, n := range endnotes {
		emit(n)
	}
	return b.String()
}
