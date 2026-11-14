package thumbnail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image/color"
	"os"
	"testing"
)

// Verify thumbnail shared formats and limits
func TestThumbnailSharedFormatsAndLimits(t *testing.T) {
	data, err := os.ReadFile("../testdata/extensions-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Thumbnails []struct {
			Name, Data               string
			Width, Height, Tolerance int
			RGBA                     []int
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Thumbnails {
		raw, _ := base64.StdEncoding.DecodeString(c.Data)
		im, err := Decode(context.Background(), raw, c.Width, c.Height)
		if err != nil {
			t.Fatal(c.Name, err)
		}
		for y := range c.Height {
			for x := range c.Width {
				pixel := color.NRGBAModel.Convert(im.At(x, y)).(color.NRGBA)
				for channel, got := range []byte{pixel.R, pixel.G, pixel.B, pixel.A} {
					at := (y*c.Width+x)*4 + channel
					if channel != 3 && c.RGBA[at-channel+3] == 0 {
						continue
					}
					delta := int(got) - c.RGBA[at]
					if delta > c.Tolerance || delta < -c.Tolerance {
						t.Fatal(c.Name, at, got, c.RGBA[at])
					}
				}
			}
		}
		if _, err = Decode(context.Background(), raw, 321, 1); err == nil {
			t.Fatal("accepted oversized output")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err = Decode(ctx, raw, 2, 2); err == nil {
			t.Fatal("ignored cancellation")
		}
	}
	for _, raw := range [][]byte{nil, []byte("invalid"), make([]byte, (16<<20)+1)} {
		if _, err := Decode(context.Background(), raw, 1, 1); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
}
