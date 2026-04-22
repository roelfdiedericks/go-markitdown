// Package docx walks the fumiama/go-docx AST and emits semantic HTML.
//
// fumiama/go-docx loads word/document.xml, word/_rels/document.xml.rels, and
// word/media/*, but leaves word/styles.xml, word/numbering.xml, and
// docProps/core.xml untouched. We open a second zip.Reader over the same
// buffered document bytes to pull the structural information we need for
// heading-level detection and metadata.
package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// StyleTable is the subset of word/styles.xml we need for heading detection.
//
// Heading lookup is two-phase:
//
//   - If the style has an explicit outline level (0-8) we use that directly.
//   - Otherwise we pattern-match the style ID or display name ("Heading 1"
//     etc.).
//
// The map is keyed by the styleId that paragraphs reference via w:pStyle.
type StyleTable struct {
	Levels map[string]int // styleID -> heading level (1-6), 0 means "not a heading"
}

// HeadingLevel returns 1-6 for a heading style, or 0 when the style is not
// a heading or the id is unknown.
func (s *StyleTable) HeadingLevel(styleID string) int {
	if s == nil {
		return 0
	}
	return s.Levels[styleID]
}

// ListFormat describes the numbering format of a list level. Only the info
// the walker cares about is kept.
type ListFormat struct {
	// Ordered is true when the level uses a numeric/alphabetic format. A
	// "bullet" numFmt means Ordered == false.
	Ordered bool
}

// ListTable resolves the (numId, ilvl) pair on a paragraph's numPr to a
// ListFormat. Populated from word/numbering.xml; callers should treat a nil
// receiver as "no numbering info available".
type ListTable struct {
	// numIdToAbstract maps num.numId to abstractNum.abstractNumId.
	numIdToAbstract map[string]string
	// levels maps abstractNumId -> ilvl -> format.
	levels map[string]map[string]ListFormat
}

// Format returns the resolved list format for the (numId, ilvl) pair, or
// (ListFormat{}, false) if the pair is unknown.
func (l *ListTable) Format(numID, ilvl string) (ListFormat, bool) {
	if l == nil {
		return ListFormat{}, false
	}
	absID, ok := l.numIdToAbstract[numID]
	if !ok {
		return ListFormat{}, false
	}
	levels, ok := l.levels[absID]
	if !ok {
		return ListFormat{}, false
	}
	f, ok := levels[ilvl]
	return f, ok
}

// DocProps is the subset of docProps/core.xml we surface as Metadata.
type DocProps struct {
	Title    string
	Author   string
	Subject  string
	Keywords string
	Created  string
	Modified string
}

// LoadExtras opens a second zip.Reader over the buffered docx bytes and
// returns parsed styles, numbering, and core properties. Any individual
// subtable that cannot be read is returned as a nil/empty value rather than
// failing the whole load — these parts are advisory, not required for a
// sensible walk.
func LoadExtras(data []byte) (*StyleTable, *ListTable, *DocProps, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("docx extras: open zip: %w", err)
	}

	styles, _ := loadStyles(zr)
	lists, _ := loadNumbering(zr)
	props, _ := loadCoreProps(zr)
	return styles, lists, props, nil
}

func loadStyles(zr *zip.Reader) (*StyleTable, error) {
	body, err := readNamed(zr, "word/styles.xml")
	if err != nil {
		return nil, err
	}
	if body == nil {
		return &StyleTable{Levels: map[string]int{}}, nil
	}

	type stylesXML struct {
		Styles []struct {
			StyleID string `xml:"styleId,attr"`
			Name    struct {
				Val string `xml:"val,attr"`
			} `xml:"name"`
			PPr struct {
				OutlineLvl struct {
					Val string `xml:"val,attr"`
				} `xml:"outlineLvl"`
			} `xml:"pPr"`
		} `xml:"style"`
	}
	var parsed stylesXML
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return &StyleTable{Levels: map[string]int{}}, fmt.Errorf("parse styles.xml: %w", err)
	}

	levels := make(map[string]int, len(parsed.Styles))
	for _, s := range parsed.Styles {
		if s.StyleID == "" {
			continue
		}
		if lvl := detectHeadingLevel(s.StyleID, s.Name.Val, s.PPr.OutlineLvl.Val); lvl > 0 {
			levels[s.StyleID] = lvl
		}
	}
	return &StyleTable{Levels: levels}, nil
}

// detectHeadingLevel returns a 1-6 level for heading styles, or 0 otherwise.
//
// Precedence:
//  1. w:outlineLvl val attribute (0 => h1, 1 => h2, ...).
//  2. styleId matching "Heading[1-9]" / "Heading [1-9]" (case-insensitive).
//  3. display name matching "Heading [1-9]" (case-insensitive).
//
// Levels beyond 6 are clamped to 6 (markdown's maximum).
func detectHeadingLevel(styleID, displayName, outlineLvlVal string) int {
	if outlineLvlVal != "" {
		if n, ok := parseDigit(outlineLvlVal); ok {
			return clampHeading(n + 1)
		}
	}
	if n := matchHeadingPattern(styleID); n > 0 {
		return clampHeading(n)
	}
	if n := matchHeadingPattern(displayName); n > 0 {
		return clampHeading(n)
	}
	return 0
}

// matchHeadingPattern looks for "heading" (case-insensitive) followed by a
// digit, optionally separated by spaces, hyphens, or underscores. Returns
// the digit, or 0 on no match.
func matchHeadingPattern(s string) int {
	lower := strings.ToLower(strings.TrimSpace(s))
	idx := strings.Index(lower, "heading")
	if idx < 0 {
		return 0
	}
	rest := lower[idx+len("heading"):]
	// Skip separators.
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '-' || rest[0] == '_') {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return 0
	}
	n, ok := parseDigit(string(rest[0]))
	if !ok {
		return 0
	}
	return n
}

func parseDigit(s string) (int, bool) {
	if len(s) == 0 {
		return 0, false
	}
	c := s[0]
	if c < '0' || c > '9' {
		return 0, false
	}
	return int(c - '0'), true
}

func clampHeading(n int) int {
	if n < 1 {
		return 1
	}
	if n > 6 {
		return 6
	}
	return n
}

func loadNumbering(zr *zip.Reader) (*ListTable, error) {
	body, err := readNamed(zr, "word/numbering.xml")
	if err != nil {
		return nil, err
	}
	if body == nil {
		return &ListTable{
			numIdToAbstract: map[string]string{},
			levels:          map[string]map[string]ListFormat{},
		}, nil
	}

	type numberingXML struct {
		AbstractNums []struct {
			ID     string `xml:"abstractNumId,attr"`
			Levels []struct {
				Ilvl   string `xml:"ilvl,attr"`
				NumFmt struct {
					Val string `xml:"val,attr"`
				} `xml:"numFmt"`
			} `xml:"lvl"`
		} `xml:"abstractNum"`
		Nums []struct {
			NumID         string `xml:"numId,attr"`
			AbstractNumID struct {
				Val string `xml:"val,attr"`
			} `xml:"abstractNumId"`
		} `xml:"num"`
	}

	var parsed numberingXML
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return &ListTable{
			numIdToAbstract: map[string]string{},
			levels:          map[string]map[string]ListFormat{},
		}, fmt.Errorf("parse numbering.xml: %w", err)
	}

	lt := &ListTable{
		numIdToAbstract: make(map[string]string, len(parsed.Nums)),
		levels:          make(map[string]map[string]ListFormat, len(parsed.AbstractNums)),
	}
	for _, an := range parsed.AbstractNums {
		if an.ID == "" {
			continue
		}
		m := make(map[string]ListFormat, len(an.Levels))
		for _, lv := range an.Levels {
			m[lv.Ilvl] = ListFormat{Ordered: !strings.EqualFold(lv.NumFmt.Val, "bullet")}
		}
		lt.levels[an.ID] = m
	}
	for _, n := range parsed.Nums {
		if n.NumID == "" {
			continue
		}
		lt.numIdToAbstract[n.NumID] = n.AbstractNumID.Val
	}
	return lt, nil
}

func loadCoreProps(zr *zip.Reader) (*DocProps, error) {
	body, err := readNamed(zr, "docProps/core.xml")
	if err != nil {
		return nil, err
	}
	if body == nil {
		return &DocProps{}, nil
	}

	type coreXML struct {
		// The core-properties part uses multiple namespaces, but
		// encoding/xml ignores namespaces by default when matching by
		// local name.
		Title    string `xml:"title"`
		Creator  string `xml:"creator"`
		Subject  string `xml:"subject"`
		Keywords string `xml:"keywords"`
		Created  string `xml:"created"`
		Modified string `xml:"modified"`
	}

	var parsed coreXML
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return &DocProps{}, fmt.Errorf("parse core.xml: %w", err)
	}

	return &DocProps{
		Title:    strings.TrimSpace(parsed.Title),
		Author:   strings.TrimSpace(parsed.Creator),
		Subject:  strings.TrimSpace(parsed.Subject),
		Keywords: strings.TrimSpace(parsed.Keywords),
		Created:  strings.TrimSpace(parsed.Created),
		Modified: strings.TrimSpace(parsed.Modified),
	}, nil
}

// readNamed returns the raw bytes of an entry or (nil, nil) when it is not
// present in the archive.
func readNamed(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open %s: %w", name, err)
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, nil
}
