package thumbnail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
)

// Fit preserves aspect ratio, rounds down with a one-pixel minimum, and never upscales
func Fit(width, height, maxWidth, maxHeight int, options ...Limits) (int, int, error) {
	l, err := limits(options)
	if err != nil {
		return 0, 0, err
	}
	if width < 1 || height < 1 || int64(width) > l.MaxSourcePixels/int64(height) || maxWidth < 1 || maxHeight < 1 || maxWidth > MaxDimension || maxHeight > MaxDimension {
		return 0, 0, errors.New("thumbnail: invalid fit dimensions")
	}
	if width <= maxWidth && height <= maxHeight {
		return width, height, nil
	}
	if int64(maxWidth)*int64(height) <= int64(maxHeight)*int64(width) {
		return maxWidth, max(1, int(int64(height)*int64(maxWidth)/int64(width))), nil
	}
	return max(1, int(int64(width)*int64(maxHeight)/int64(height))), maxHeight, nil
}

// Decode an image without upscaling beyond its source dimensions
func FitDecode(ctx context.Context, data []byte, maxWidth, maxHeight int, options ...Limits) (image.Image, error) {
	cfg, err := config(ctx, data, options...)
	if err != nil {
		return nil, err
	}
	w, h, err := Fit(cfg.Width, cfg.Height, maxWidth, maxHeight, options...)
	if err != nil {
		return nil, err
	}
	return Decode(ctx, data, w, h, options...)
}

// Encode image pixels as PNG with cancellation checks
func PNG(ctx context.Context, source image.Image) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("thumbnail: missing image")
	}
	b := source.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 || w > MaxDimension || h > MaxDimension {
		return nil, errors.New("thumbnail: output exceeds supported limits")
	}
	// Use the same 8-bit straight-alpha representation as all other SDKs
	out, direct := source.(*image.NRGBA)
	if !direct {
		out = image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for x := range w {
				out.Set(x, y, source.At(b.Min.X+x, b.Min.Y+y))
			}
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, out); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

// Encode image pixels as a PNG data URI
func DataURI(ctx context.Context, source image.Image) (string, error) {
	encoded, err := PNG(ctx, source)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded), nil
}

type Rendered struct {
	Width, Height int
	DataURI       string
}

// Decode, aspect-fit and encode a display-ready thumbnail
func Render(ctx context.Context, data []byte, maxWidth, maxHeight int, options ...Limits) (Rendered, error) {
	source, err := FitDecode(ctx, data, maxWidth, maxHeight, options...)
	if err != nil {
		return Rendered{}, err
	}
	uri, err := DataURI(ctx, source)
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{source.Bounds().Dx(), source.Bounds().Dy(), uri}, nil
}
