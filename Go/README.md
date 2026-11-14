# Go plugin SDK

Build native Stream Dock Manager plugins in Go. Users install your compiled ZIP and do not need Go on their machine.

[SDK overview](../README.md) · [Starter plugins](../examples/README.md) · [License](../LICENSE)

## Get started

| Requirement     | Details                                      |
| --------------- | -------------------------------------------- |
| Go              | 1.27.4 or newer compatible with `go.mod`     |
| Authoring tools | Python 3.11+ for packaging                   |
| Module          | `github.com/StreamDockManager/Plugin-SDK/Go` |
| Import name     | `sdk`                                        |

Install [Go](https://go.dev/doc/install) and [Python](https://www.python.org/downloads/), then confirm `go version` and `python3 --version`. See the [shared setup notes](../README.md#install-build-tools) for Windows commands and native build tools.

The [GitHub repository](https://github.com/StreamDockManager/Plugin-SDK) is named
Plugin-SDK. Continue to use the Go module path above for imports and dependency pins.

### Build your first plugin

Start with the [Go Counter](../examples/sdk-example-go/README.md), which selects an exact SDK version. After [cloning the SDK](../README.md#get-the-source), run from its repository root:

```sh
cd examples/sdk-example-go
go mod download
python3 ../tools/package.py go
```

The packager prints a ZIP path in `examples/artifacts/`. Install it through **Plugins → Import plugin** in Manager, enable it, assign its Counter action to a button, and save the binding. One press and release should change **Count 0** to **Count 1**. End users do not install this Go module.

### Start a project with your own identity

For your own project, use the [SDK generator](../README.md#create-a-plugin). Keep a versioned SDK dependency in `go.mod`; private repositories require authenticated Git access and the SDK module prefix in `GOPRIVATE` (plus any explicitly configured `GONOPROXY`/`GONOSUMDB` patterns).

## Common APIs

| API                        | Purpose                                            |
| -------------------------- | -------------------------------------------------- |
| `Run`, `Handler`           | Start the plugin and handle lifecycle/input events |
| `Scheduler`, `DeferAction` | Schedule work and complete actions asynchronously  |
| `NewPortableSettings`      | Read and update action/global settings             |
| `NewHostStore`             | Use negotiated private host storage                |
| `/httptrack`               | Opt in to HTTP diagnostics                         |

The host transport uses `coder/websocket`; image codecs use the Go standard library. Host storage requires `plugin-storage-v1`; it does not silently switch to a local file.

## Reusable helpers

Import `Go/managedstate`, `Go/requests`, `Go/connectedws`, or `Go/thumbnail` from the SDK module. `authoring.StartObservationJob`, `authoring.Present`, and `authoring.CompactDisplay` are available without extra dependencies. Thumbnail decoding returns an `image.Image`:

```go
image, err := thumbnail.Decode(ctx, encodedBytes, 72, 72)
```

Use the SDK packaging helpers to retain dependency notices. Image codecs come from the Go standard library.

See the [shared behavior and limits](../README.md#reusable-plugin-helpers) for persistence, discovery, cancellation, and rendering semantics.

## Caches, discovery, and operation history

Import `Go/cache`, `Go/discovery`, and `Go/operations` from the SDK module. Use `cache.New[T](entries, bytes)` and explicit `cache.NewPersistence[T](hostStore)` for private storage. `discovery.Runner.Run` schedules read-only inspector work through the authoring engine. `operations.New[T]` creates a ledger; call `Reserve` before submission and `Update` after observing its outcome.

To fit an image and return a display-ready PNG data URI with custom limits:

```go
limits := thumbnail.DefaultLimits()
limits.MaxInputBytes = 128 << 20
limits.MaxSourcePixels = 64_000_000
result, err := thumbnail.Render(ctx, encodedBytes, 256, 256, limits)
// result.DataURI, result.Width, result.Height
```

Omit `limits` to use 64 MiB / 32 MP. `thumbnail.Fit`, `FitDecode`, `PNG`, and `DataURI` are also available separately. Pass complete positive limits when supplying a `Limits` value.

## WebSocket transport

Host connections and external service sessions use [`coder/websocket`](https://github.com/coder/websocket), pinned in `go.mod`. The host transport remains loopback-only, bypasses proxies, rejects redirects, and keeps the 1 MiB message limit. Compression is disabled. HTTP/TLS code is now part of the core Go dependency closure; optional service clients and image codecs still require their own imports.

Service authentication callbacks receive the opening context and a `*websocket.Conn`. Use that context for every read and write:

```go
func authenticate(ctx context.Context, conn *websocket.Conn) error {
    var greeting map[string]any
    return wsjson.Read(ctx, conn, &greeting)
}
```

Import `websocket` and `wsjson` from `github.com/coder/websocket` and `github.com/coder/websocket/wsjson`. Existing Gorilla-based handshake callbacks need this signature change. Service connections retain system TLS verification, direct dialing, bounded requests, 30-second pings, and a 90-second pong allowance. Cancelling before writer admission sends nothing. Cancelling an admitted request stops waiting promptly; an already-started frame can still reach the peer and its delivery is uncertain. Writes have a separate eight-second session deadline; a failed or timed-out write, or cancelled authentication, closes the connection. Commands are never replayed automatically.

## Scheduling and restoration

Timed work requires `Scheduler`; handlers without it are event-only and no longer receive implicit one-second polling. The authoring engine implements scheduling, with a zero deadline suspending idle polling. Set `Feature.ShouldLogAction` for action classification; the old setter is removed.

`Engine.Restore(dst)` returns an error directly and replaces the destination only after successful decoding. File stores accept only the current version-1 envelope. `Store.Load(dst)` has no migration callback. Rejected state blocks writes until an explicit successful reload, preserving the file for user-directed recovery.

## Job generation migration

`authoring.Engine.Generations` has been removed. This is a source-incompatible Go API change:
read `job.Generation` from a checked `job, ok := engine.Jobs[id]` lookup instead. A missing job
must reject progress and completion, even if a previous generation is known. The engine's monotonic
allocator remains internal scheduling state; it is not the current identity for a particular job.
Update consumers together with their immutable SDK revision pins after that revision is published.

## Binding state compatibility

The authoring engine groups binding-local settings, presentation and lifecycle cleanup
through an internal live view. Its exported maps remain the authoritative storage, including
direct mutations and map replacement by existing consumers. Shared probes, samples and jobs
retain their own ownership and cancellation paths. No consumer migration or revision-pin
change is required for this internal refactor; replacing those maps with physical binding
records would require a separately coordinated breaking API migration.

## Work on the SDK

Run from this `Go/` directory:

```sh
go test ./...
```

| Check           | Run from `Go/`        | Scope                        |
| --------------- | --------------------- | ---------------------------- |
| Compile         | `go build ./...`      | All Go SDK packages          |
| Tests           | `go test ./...`       | Go unit and behavioral tests |
| Thumbnail tests | `go test ./thumbnail` | Optional image helper        |

To test local SDK changes in a plugin, run these commands from the plugin's Go module directory:

```sh
go work init . /absolute/path/to/StreamDockManager-Plugins-SDK/Go
go test ./...
```

If you already have a Go workspace, add this SDK with `go work use` instead. Keep local workspace files out of commits. Shared behavior must remain consistent with Rust, Python, and Node.js. See the [SDK overview](../README.md).

## Troubleshooting

| Problem                         | Next step                                                                                                        |
| ------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| Module download fails           | Check the pinned SDK revision and repository access; configure `GOPRIVATE` only when private access requires it. |
| Local SDK edits are not visible | Use an explicit temporary Go workspace; do not commit sibling-path replacements.                                 |

## License

SDK code is [MIT-licensed](../LICENSE). Preserve dependency notices in distributed packages; Manager and the official plugins have separate licenses.
