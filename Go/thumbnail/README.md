# Go thumbnail helpers

Turn JPEG, PNG, or the first frame of a GIF into a small image for a plugin display. These helpers use Go's standard image codecs; there is no native library or separate codec installation.

[Quick start](#get-started) · [Limits](#limits-and-memory) · [Go SDK](../README.md)

## Get started

Use [Go 1.27.4+ and the Go SDK](../README.md#get-started). Decode caller-supplied bytes
in bounded background work, not the event loop:

```go
import "github.com/StreamDockManager/Plugin-SDK/Go/thumbnail"

// Use your operation's cancellation context.
result, err := thumbnail.Render(ctx, encodedBytes, 72, 72)
```

Check `err`, then use `result.DataURI`, `result.Width`, or `result.Height`. Use
`thumbnail.Decode(ctx, encodedBytes, 72, 72)` when you need an `image.Image` instead.

## Choose a helper

| Helper               | Result                                                           |
| -------------------- | ---------------------------------------------------------------- |
| `Decode`             | Decode and sample to the requested dimensions.                   |
| `Fit` / `FitDecode`  | Fit while preserving aspect ratio; `FitDecode` does not upscale. |
| `PNG`                | Encode RGBA8 pixels as PNG.                                      |
| `DataURI` / `Render` | Produce a display-ready PNG data URI.                            |

## Limits and memory

| Setting       | Default                                            |
| ------------- | -------------------------------------------------- |
| Encoded input | 64 MiB                                             |
| Source pixels | 32,000,000                                         |
| Output size   | 1–320 pixels per dimension                         |
| GIF frames    | First frame only                                   |
| Sampling      | Top-left pixel of each cell, without EXIF rotation |

The full source image is decoded before sampling. Memory use depends on the source, not only the output size. Cancellation is cooperative during reads, sampling, and publication; a codec may continue CPU work until its next check. Keep decoding off the event loop.

Override input limits per call, without changing the fixed output bound:

```go
limits := thumbnail.DefaultLimits()
limits.MaxInputBytes = 128 << 20
limits.MaxSourcePixels = 64_000_000
result, err := thumbnail.Render(ctx, encodedBytes, 256, 256, limits)
```

Supply positive values. The same optional limits apply to `Decode`, `Fit`, and `FitDecode`. Match your HTTP/file read limit to the chosen decoder limit; these helpers do not download images.

## Development check

From the SDK's `Go/` directory:

```sh
go test ./thumbnail
```

## Troubleshooting

| Problem                                      | What to check                                                                                       |
| -------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| Decoding returns an error                    | Check the format, input byte count, source dimensions, output dimensions, and cancellation context. |
| A small output still uses substantial memory | Source decoding happens before sampling. Lower the source pixel limit and bound the input reader.   |
| Image work delays button handling            | Schedule decoding as bounded background work, then apply the result on the plugin's owner loop.     |

[Go SDK](../README.md) · [Shared helper contracts](../../REFERENCE.md#reusable-plugin-helpers) · [MIT license](../../LICENSE)
