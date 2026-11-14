package thumbnail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"os"
	"strings"
	"testing"
)

// Verify shared thumbnail helpers and configurable limits
func TestSharedThumbnailHelpersAndConfigurableLimits(t *testing.T) {
	var f struct {
		Thumbnails struct {
			Input  int   `json:"max_input_bytes"`
			Pixels int64 `json:"max_source_pixels"`
			Fit    []struct {
				Source, Box, Output []int
				Pixels              int64
			}
		}
	}
	raw, _ := os.ReadFile("../testdata/helpers-v1.json")
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	s := f.Thumbnails
	if MaxInputBytes != s.Input || MaxSourcePixels != s.Pixels {
		t.Fatal("defaults differ")
	}
	for _, c := range s.Fit {
		limits := DefaultLimits()
		if c.Pixels != 0 {
			limits.MaxSourcePixels = c.Pixels
		}
		w, h, err := Fit(c.Source[0], c.Source[1], c.Box[0], c.Box[1], limits)
		if err != nil || w != c.Output[0] || h != c.Output[1] {
			t.Fatal(c, w, h, err)
		}
	}
	if _, _, err := Fit(8000, 8000, 320, 320); err == nil {
		t.Fatal("default source cap")
	}
	ctx := context.Background()
	im := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	im.SetNRGBA(0, 0, color.NRGBA{12, 34, 56, 128})
	encoded, err := PNG(ctx, im)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxInputBytes = len(encoded) - 1
	if _, err = Decode(ctx, encoded, 3, 2, limits); err == nil {
		t.Fatal("input override ignored")
	}
	limits.MaxInputBytes = len(encoded)
	limits.MaxSourcePixels = 5
	if _, err = Decode(ctx, encoded, 3, 2, limits); err == nil {
		t.Fatal("pixel override ignored")
	}
	limits.MaxSourcePixels = 6
	rendered, err := Render(ctx, encoded, 320, 320, limits)
	if err != nil || rendered.Width != 3 || rendered.Height != 2 {
		t.Fatal(rendered, err)
	}
	data, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(rendered.DataURI, "data:image/png;base64,"))
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || color.NRGBAModel.Convert(decoded.At(0, 0)).(color.NRGBA) != im.NRGBAAt(0, 0) {
		t.Fatal("PNG roundtrip", err)
	}
	for _, data := range [][]byte{encoded[:len(encoded)/2], make([]byte, MaxInputBytes+1)} {
		if _, err := Decode(ctx, data, 1, 1); err == nil {
			t.Fatal("invalid input")
		}
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := PNG(c, im); err == nil {
		t.Fatal("cancelled PNG")
	}
	if _, err := Decode(ctx, encoded, 1, 1, Limits{}); err == nil {
		t.Fatal("invalid override")
	}
}
