package docconv

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"

	"github.com/xuri/excelize/v2"
)

// tinyPNG returns a valid 1x1 red PNG suitable as an XLSX image payload.
// Generated via image/png (rather than hard-coded bytes) because excelize
// validates the input and rejects malformed signatures.
var (
	tinyPNGOnce sync.Once
	tinyPNGData []byte
)

func tinyPNG() []byte {
	tinyPNGOnce.Do(func() {
		img := image.NewRGBA(image.Rect(0, 0, 2, 2))
		img.Set(0, 0, color.RGBA{255, 0, 0, 255})
		img.Set(1, 0, color.RGBA{0, 255, 0, 255})
		img.Set(0, 1, color.RGBA{0, 0, 255, 255})
		img.Set(1, 1, color.RGBA{255, 255, 0, 255})
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			panic("xlsx_test: png encode failed: " + err.Error())
		}
		tinyPNGData = buf.Bytes()
	})
	return tinyPNGData
}

// buildXLSXWithImage builds an in-memory xlsx containing a header row,
// some body text, and an anchored image on the configured cell. Returning
// raw bytes keeps the test independent of on-disk fixtures and makes the
// dedup + context assertions deterministic.
func buildXLSXWithImage(t *testing.T, cell string, altText string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sheet := "Sheet1"

	if err := f.SetCellValue(sheet, "A1", "Region"); err != nil {
		t.Fatalf("SetCellValue A1: %v", err)
	}
	if err := f.SetCellValue(sheet, "B1", "Revenue"); err != nil {
		t.Fatalf("SetCellValue B1: %v", err)
	}
	if err := f.SetCellValue(sheet, "A2", "SENTINEL-REGION"); err != nil {
		t.Fatalf("SetCellValue A2: %v", err)
	}
	if err := f.SetCellValue(sheet, "B2", "42"); err != nil {
		t.Fatalf("SetCellValue B2: %v", err)
	}

	opts := &excelize.GraphicOptions{AltText: altText}
	if err := f.AddPictureFromBytes(sheet, cell, &excelize.Picture{
		Extension: ".png",
		File:      tinyPNG(),
		Format:    opts,
	}); err != nil {
		t.Fatalf("AddPictureFromBytes: %v", err)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

// TestExtractXLSXImagesAppearAfterSheetTable confirms the image
// placeholder lands AFTER the sheet's table content in document order,
// and survives describer replacement.
func TestExtractXLSXImagesAppearAfterSheetTable(t *testing.T) {
	stub := &stubDescriber{reply: "a chart"}
	data := buildXLSXWithImage(t, "D5", "Quarterly revenue")

	md, _, err := extractXLSX(context.Background(), data, Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("extractXLSX: %v", err)
	}

	tableIdx := strings.Index(md, "| Region |")
	imgIdx := strings.Index(md, "a chart")
	if tableIdx < 0 {
		t.Fatalf("expected table header in output, got:\n%s", md)
	}
	if imgIdx < 0 {
		t.Fatalf("expected rendered image caption in output, got:\n%s", md)
	}
	if imgIdx < tableIdx {
		t.Fatalf("image appeared before the table (doc order regression):\n%s", md)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected exactly one describer call for one image, got %d", len(stub.calls))
	}
}

// TestExtractXLSXContextIncludesSheetAndAnchor verifies the describer
// prompt carries (a) the sheet name, (b) the anchor cell, and (c) flat
// sheet text so the describer can orient on an unlabelled chart.
func TestExtractXLSXContextIncludesSheetAndAnchor(t *testing.T) {
	stub := &stubDescriber{reply: "desc"}
	data := buildXLSXWithImage(t, "D5", "Quarterly revenue")

	_, _, err := extractXLSX(context.Background(), data, Options{
		IncludeImages: true,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("extractXLSX: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected one call, got %d", len(stub.calls))
	}
	prompt := stub.calls[0].prompt
	for _, want := range []string{
		`"Sheet1"`,          // sheet name
		"anchor D5",         // cell anchor
		"SENTINEL-REGION",   // sheet text snippet
		"Quarterly revenue", // author alt-text
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// TestExtractXLSXIncludeImagesFalseStrips confirms that with the flag
// off, the image never turns into a caption and no describer call is
// made — mirrors the invariant we check for every other backend.
func TestExtractXLSXIncludeImagesFalseStrips(t *testing.T) {
	stub := &stubDescriber{reply: "should-not-fire"}
	data := buildXLSXWithImage(t, "D5", "unused alt")

	md, _, err := extractXLSX(context.Background(), data, Options{
		IncludeImages: false,
		LLMClient:     stub,
	})
	if err != nil {
		t.Fatalf("extractXLSX: %v", err)
	}
	if strings.Contains(md, "rid:") || strings.Contains(md, "should-not-fire") {
		t.Errorf("expected no image artifacts, got:\n%s", md)
	}
	if len(stub.calls) != 0 {
		t.Errorf("describer fired with IncludeImages=false: %d calls", len(stub.calls))
	}
}

// TestFlattenSheetText bounds the preview to the given budget and uses
// " | " as the cell separator so the describer sees tabular data.
func TestFlattenSheetText(t *testing.T) {
	rows := [][]string{
		{"A", "B", "C"},
		{"x", "y", ""},
	}
	out := flattenSheetText(rows, 100)
	if !strings.Contains(out, "A | B | C") {
		t.Errorf("expected pipe-joined row: %q", out)
	}
	if !strings.Contains(out, "x | y") {
		t.Errorf("expected filtered empty cells: %q", out)
	}

	big := make([][]string, 100)
	for i := range big {
		big[i] = []string{"cell", "data", "here"}
	}
	trimmed := flattenSheetText(big, 40)
	if len([]rune(trimmed)) > 41 {
		t.Errorf("budget not enforced: %d runes in %q", len([]rune(trimmed)), trimmed)
	}
	if !strings.HasSuffix(trimmed, "…") {
		t.Errorf("expected ellipsis on truncated text: %q", trimmed)
	}
}
