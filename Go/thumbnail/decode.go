// Package thumbnail provides bounded standard-library image decoding and sampling
package thumbnail

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
)

const MaxInputBytes = 64 << 20
const MaxSourcePixels = 32_000_000
const MaxDimension = 320

// Limits bounds encoded input and decoded dimensions for one call
// Start with DefaultLimits when overriding a single field. Larger limits can
// increase codec memory use; limits must be positive safe integers
type Limits struct {
	MaxInputBytes   int
	MaxSourcePixels int64
}

// Return the default encoded-byte and source-pixel limits
func DefaultLimits() Limits { return Limits{MaxInputBytes, MaxSourcePixels} }

// Validate optional decoding limits
func limits(options []Limits) (Limits, error) {
	if len(options) > 1 {
		return Limits{}, errors.New("thumbnail: only one limits option is supported")
	}
	l := DefaultLimits()
	if len(options) == 1 {
		l = options[0]
	}
	if l.MaxInputBytes < 1 || l.MaxSourcePixels < 1 || int64(l.MaxInputBytes) > 9007199254740991 || l.MaxSourcePixels > 9007199254740991 {
		return l, errors.New("thumbnail: invalid limits")
	}
	return l, nil
}

type cancelReader struct {
	ctx    context.Context
	reader io.Reader
}

// Read image bytes while observing cancellation
func (r cancelReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

// Inspect image metadata within byte and pixel limits
func config(ctx context.Context, data []byte, options ...Limits) (image.Config, error) {
	if err := ctx.Err(); err != nil {
		return image.Config{}, err
	}
	l, err := limits(options)
	if err != nil {
		return image.Config{}, err
	}
	if len(data) == 0 || len(data) > l.MaxInputBytes {
		return image.Config{}, errors.New("thumbnail: input exceeds supported limits")
	}
	cfg, _, err := image.DecodeConfig(cancelReader{ctx, bytes.NewReader(data)})
	if err != nil {
		return cfg, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width) > l.MaxSourcePixels/int64(cfg.Height) {
		return cfg, errors.New("thumbnail: invalid source dimensions")
	}
	return cfg, nil
}

// Decode retains a bounded full source image while producing an RGBA thumbnail
// Cancellation is cooperative at reads, sample rows, and result publication
func Decode(ctx context.Context, data []byte, width, height int, options ...Limits) (image.Image, error) {
	if width < 1 || height < 1 || width > MaxDimension || height > MaxDimension {
		return nil, errors.New("thumbnail: output exceeds supported limits")
	}
	cfg, err := config(ctx, data, options...)
	if err != nil {
		return nil, err
	}
	source, _, err := image.Decode(cancelReader{ctx, bytes.NewReader(data)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(ctx.Err(), err)
		}
		return nil, err
	}
	out := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	for y := range height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sy := int(int64(y) * int64(cfg.Height) / int64(height))
		for x := range width {
			sx := int(int64(x) * int64(cfg.Width) / int64(width))
			if sx >= bounds.Dx() || sy >= bounds.Dy() {
				continue
			}
			out.Set(x, y, source.At(bounds.Min.X+sx, bounds.Min.Y+sy))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
