package ooxml

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	pathpkg "path"
	"strings"
)

// Rels maps relationship IDs to their resolved zip-relative target paths.
//
// Example for a slide at ppt/slides/slide1.xml whose rels part lists
//
//	<Relationship Id="rId2" Target="../media/image1.png" ... />
//
// Rels["rId2"] == "ppt/media/image1.png".
type Rels map[string]string

// relsXML mirrors the subset of the OOXML Relationships schema we care about.
type relsXML struct {
	XMLName xml.Name    `xml:"Relationships"`
	Items   []relsEntry `xml:"Relationship"`
}

type relsEntry struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
	Mode   string `xml:"TargetMode,attr"`
}

// ParseRels reads the rels part that corresponds to partPath from zr and
// returns a resolved id -> target map.
//
// partPath is the path of the main part, e.g. "ppt/slides/slide1.xml".
// ParseRels locates the sibling rels file at "ppt/slides/_rels/slide1.xml.rels"
// and parses it. Relative targets in the rels file are joined against the
// main part's directory so callers always receive zip-absolute paths.
//
// External targets (TargetMode="External") are returned unchanged.
//
// Missing rels files return an empty, non-nil map and no error — plenty of
// OOXML parts have no relationships.
func ParseRels(zr *zip.Reader, partPath string) (Rels, error) {
	relsPath := siblingRelsPath(partPath)
	f := findFile(zr, relsPath)
	if f == nil {
		return Rels{}, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", relsPath, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relsPath, err)
	}

	var parsed relsXML
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", relsPath, err)
	}

	baseDir := pathpkg.Dir(partPath)
	out := make(Rels, len(parsed.Items))
	for _, it := range parsed.Items {
		if it.ID == "" {
			continue
		}
		if strings.EqualFold(it.Mode, "External") {
			out[it.ID] = it.Target
			continue
		}
		out[it.ID] = resolveRelTarget(baseDir, it.Target)
	}
	return out, nil
}

// siblingRelsPath returns the rels file path for partPath. The rule is:
// insert "_rels/" before the file name and append ".rels".
//
// Example: ppt/slides/slide1.xml -> ppt/slides/_rels/slide1.xml.rels
// The top-level part "word/document.xml" -> "word/_rels/document.xml.rels".
func siblingRelsPath(partPath string) string {
	dir, base := pathpkg.Split(partPath)
	return dir + "_rels/" + base + ".rels"
}

// resolveRelTarget joins a relative target against the directory of the
// source part. Absolute targets ("/word/...") are returned with the leading
// slash stripped so they stay zip-relative.
func resolveRelTarget(baseDir, target string) string {
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	joined := pathpkg.Join(baseDir, target)
	// pathpkg.Join calls Clean which handles any "../" segments.
	return joined
}

// findFile returns the zip entry with exactly this name, or nil. Zip paths
// are case-sensitive in the OOXML spec so we match byte-for-byte.
func findFile(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// ReadMediaBytes returns the bytes of a media file at zipPath. Small helper
// used by backends that resolve image relationships to file paths. Returns
// a wrapped error when the entry is missing or unreadable.
func ReadMediaBytes(zr *zip.Reader, zipPath string) ([]byte, error) {
	f := findFile(zr, zipPath)
	if f == nil {
		return nil, fmt.Errorf("ooxml: media %q not found in zip", zipPath)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("ooxml: open %q: %w", zipPath, err)
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
