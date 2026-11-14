# Go thumbnail helpers

Turn JPEG, PNG, or the first frame of a GIF into a small image for a plugin display. These helpers use Go's standard image codecs; there is no native library or separate codec installation.

| Requirement  | Details                                                      |
| ------------ | ------------------------------------------------------------ |
| Language     | Go 1.27.4+; use the [Go SDK setup](../README.md#get-started) |
| Input        | Caller-supplied JPEG, PNG, or GIF bytes                      |
| Output       | A sampled image, PNG bytes, or a display-ready data URI      |
| Dependencies | Go SDK and standard-library codecs only                      |

## Get started

Add the [Go SDK](../README.md#get-started) to your plugin, then import its thumbnail package:

```go
import "github.com/StreamDockManager/Plugin-SDK/Go/thumbnail"

// Run in background work with your operation's context.
image, err := thumbnail.Decode(ctx, encodedBytes, 72, 72)
```

Check `err` before using the returned `image.Image`. Use `Render` when you want a display-ready PNG data URI rather than raw pixels:

```go
result, err := thumbnail.Render(ctx, encodedBytes, 72, 72)
// result.DataURI, result.Width, result.Height
```

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

[Go SDK](../README.md) · [Shared helper contracts](../../README.md#reusable-plugin-helpers) · [MIT license](../../LICENSE)
