package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// Comment is one DOCX reviewer comment, ready to render as an HTML
// comment inline.
type Comment struct {
	// ID is the w:id attribute on <w:comment>.
	ID string
	// Author is w:author; may be empty.
	Author string
	// Date is w:date; may be empty.
	Date string
	// Text is the flattened plaintext body of the comment. Whitespace
	// is collapsed; the text is safe to splice inside an HTML comment
	// (we also strip the "--" close-marker risk at render time).
	Text string
}

// HTMLComment returns the <!-- ... --> form we splice into the body. The
// format is intentionally explicit so LLM readers can tell the string
// originated as a reviewer comment and is not an author assertion.
//
// Sequences of two or more hyphens in Text are neutralised to avoid
// prematurely terminating the HTML comment.
func (c Comment) HTMLComment() string {
	safe := strings.ReplaceAll(c.Text, "--", "- -")
	author := strings.TrimSpace(c.Author)
	date := strings.TrimSpace(c.Date)
	switch {
	case author != "" && date != "":
		return fmt.Sprintf("<!-- comment by %s (%s): %s -->", author, date, safe)
	case author != "":
		return fmt.Sprintf("<!-- comment by %s: %s -->", author, safe)
	case date != "":
		return fmt.Sprintf("<!-- comment (%s): %s -->", date, safe)
	default:
		return fmt.Sprintf("<!-- comment: %s -->", safe)
	}
}

// CommentTable holds every reviewer comment defined in word/comments.xml
// keyed by id. Body references resolve against this table via
// CommentRefIndex at walk time.
type CommentTable struct {
	ByID map[string]Comment
}

// Empty reports whether the table has nothing to splice.
func (t *CommentTable) Empty() bool {
	return t == nil || len(t.ByID) == 0
}

// CommentRefIndex maps a body item index to the list of reviewer
// comments anchored at that item (i.e. the range start for the comment
// lives inside that paragraph/table).
type CommentRefIndex struct {
	ByItem map[int][]Comment
}

// Empty reports whether the index has nothing to splice.
func (i *CommentRefIndex) Empty() bool {
	return i == nil || len(i.ByItem) == 0
}

// At returns the comments anchored at item idx, or nil. Safe on nil
// receiver.
func (i *CommentRefIndex) At(idx int) []Comment {
	if i == nil || i.ByItem == nil {
		return nil
	}
	return i.ByItem[idx]
}

// LoadComments reads word/comments.xml and builds a CommentTable. Returns
// a non-nil, possibly empty table on any error.
func LoadComments(data []byte) (*CommentTable, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return &CommentTable{ByID: map[string]Comment{}}, fmt.Errorf("comments: open zip: %w", err)
	}
	body, _ := readNamed(zr, "word/comments.xml")
	if body == nil {
		return &CommentTable{ByID: map[string]Comment{}}, nil
	}
	return parseComments(body), nil
}

// parseComments streams comments.xml and returns the comment table.
// Skips comments with empty bodies.
func parseComments(body []byte) *CommentTable {
	dec := xml.NewDecoder(bytes.NewReader(body))
	tbl := &CommentTable{ByID: map[string]Comment{}}
	var (
		current   *Comment
		textBuf   strings.Builder
		inText    bool
		inComment bool
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			switch local {
			case "comment":
				current = &Comment{
					ID:     attr(t.Attr, "id"),
					Author: attr(t.Attr, "author"),
					Date:   attr(t.Attr, "date"),
				}
				textBuf.Reset()
				inComment = true
			case "t":
				if inComment {
					inText = true
				}
			case "p":
				if inComment && textBuf.Len() > 0 {
					textBuf.WriteByte(' ')
				}
			}
		case xml.CharData:
			if inText {
				textBuf.Write(t)
			}
		case xml.EndElement:
			local := strings.ToLower(t.Name.Local)
			switch local {
			case "comment":
				if current != nil && current.ID != "" {
					text := collapseWhitespace(textBuf.String())
					if text != "" {
						current.Text = text
						tbl.ByID[current.ID] = *current
					}
				}
				current = nil
				inComment = false
			case "t":
				inText = false
			}
		}
	}
	return tbl
}

// LoadCommentRefs streams document.xml to associate each comment id with
// the body item it anchors. We match on <w:commentRangeStart w:id=..>
// (preferred) and fall back to <w:commentReference w:id=..> if the
// former is absent. Both appear in practice.
func LoadCommentRefs(data []byte, tbl *CommentTable) (*CommentRefIndex, error) {
	if tbl.Empty() {
		return &CommentRefIndex{ByItem: map[int][]Comment{}}, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return &CommentRefIndex{ByItem: map[int][]Comment{}}, fmt.Errorf("comment refs: open zip: %w", err)
	}
	body, _ := readNamed(zr, "word/document.xml")
	if body == nil {
		return &CommentRefIndex{ByItem: map[int][]Comment{}}, nil
	}
	return parseCommentRefs(body, tbl), nil
}

// parseCommentRefs walks document.xml body and maps commentRangeStart
// ids to the body item they sit inside.
func parseCommentRefs(body []byte, tbl *CommentTable) *CommentRefIndex {
	idx := &CommentRefIndex{ByItem: map[int][]Comment{}}
	dec := xml.NewDecoder(bytes.NewReader(body))

	var (
		inBody    bool
		itemIdx   = -1
		itemDepth int
		// Track which comment ids we've already anchored so a rangeEnd
		// further along doesn't double-add the same comment at a later
		// body item.
		seen = map[string]bool{}
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
			if local == "commentrangestart" || local == "commentreference" {
				id := attr(t.Attr, "id")
				if id == "" || seen[id] {
					continue
				}
				c, ok := tbl.ByID[id]
				if !ok {
					continue
				}
				idx.ByItem[itemIdx] = append(idx.ByItem[itemIdx], c)
				seen[id] = true
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
