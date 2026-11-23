# Rust plugin SDK

Build a native plugin. The final ZIP contains the executable; users do not need Rust or Cargo.

[Build Counter](#use-the-sdk-before-registry-publication) · [Setup](#get-started) ·
[APIs](#main-apis) · [SDK checks](#build-and-test-the-sdk)

## Get started

| Item             | Value                                                                                                                             |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| Build tools      | [Rust 1.89+ and Cargo](https://www.rust-lang.org/tools/install), native linker, [Python 3.11+](https://www.python.org/downloads/) |
| Crate            | `stream-dock-manager-plugin-sdk`                                                                                                  |
| Checkout version | `0.1.1`; keep an exact dependency and commit `Cargo.lock`                                                                         |

[Clone the SDK](../README.md#get-the-source), then use the local Cargo override below
with the [Rust Counter](../examples/sdk-example-rust/README.md). It does not require a
crates.io publication.

### Use the SDK before registry publication

Save this as `sdk-local.toml` **outside the plugin** and point it at your SDK checkout:

```toml
[patch.crates-io]
stream-dock-manager-plugin-sdk = { path = "/absolute/path/to/Plugin-SDK/Rust" }
```

From `examples/sdk-example-rust/`:

```sh
export STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG=/absolute/path/to/sdk-local.toml
python3 ../tools/package.py rust
```

PowerShell: `$env:STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG = 'C:\path\to\sdk-local.toml'`.
For direct Cargo commands, use `--config /absolute/path/to/sdk-local.toml`. Keep local paths
out of published dependencies.

Import the printed ZIP in Manager, enable it, save a Counter binding, and press and release.
To create your own plugin ID, use the [generator](../README.md#create-a-plugin).

## Main APIs

| API                         | Purpose                                                     |
| --------------------------- | ----------------------------------------------------------- |
| `Handler::handle` and `run` | Process plugin lifecycle and input                          |
| `Client`                    | Send bounded, serialized host messages                      |
| `Deferred`                  | Complete work later; dropping it unfinished reports failure |
| `PortableSettings`          | Use base action/global settings                             |
| `HostStore`                 | Use negotiated private storage with receipts                |

Callbacks run on the event-loop thread. Background work must observe cancellation. Private storage requires `plugin-storage-v1`; it never silently falls back to files. The separate `storage::Store` is an explicitly selected local JSON utility.

## Reusable helpers

`managedstate`, `requests`, and the authoring helpers are included in the library. Enable optional codecs and service sockets explicitly:

```toml
stream-dock-manager-plugin-sdk = { version = "=0.1.1", features = ["thumbnail", "service-websocket"] }
```

Use `thumbnail::decode(bytes, width, height, &cancel)` for an RGBA thumbnail, `connectedws::ServiceClient` for service sessions, and `Plugin::observe` for display-only follow-up work. Keep the same feature list when using a local path dependency.

See the [shared behavior and limits](../REFERENCE.md#reusable-plugin-helpers) for persistence, discovery, cancellation, and rendering semantics.

## Caches, discovery, and operation history

Use `cache::{Cache, Budget, Persistence}`, `discovery::{Runner, Request}`, and `operations::{Ledger, State}`. Private persistence requires an explicitly supplied `HostStore`. Schedule discovery on bounded background work using the ticket's cancellation flag; complete and expire requests on the owner loop, and clear the context on close. Reserve ledger capacity before sending external commands.

With the optional `thumbnail` feature enabled:

```rust
use stream_dock_manager_plugin_sdk::thumbnail::{self, Limits};
let result = thumbnail::render_with_limits(&encoded_bytes, 256, 256, &cancel, Limits {
    max_input_bytes: 128 << 20,
    max_source_pixels: 64_000_000,
})?;
// result.data_uri, result.width, result.height
```

`thumbnail::render` uses the 64 MiB / 32 MP defaults. Use `Limits::default()` with struct update syntax to override one field. `fit`, `fit_decode`, `png`, and `data_uri` are also available; the decode and fit functions have corresponding `_with_limits` variants.

## Build and test the SDK

Run from `Rust/`:

```sh
cargo check --locked --features http,thumbnail,service-websocket
cargo test --locked --features http,testkit-tls,thumbnail,service-websocket
```

| Feature             | Enables                                           |
| ------------------- | ------------------------------------------------- |
| `http`              | HTTP service client                               |
| `thumbnail`         | JPEG/PNG/GIF decoding and display output          |
| `service-websocket` | External service WebSockets with system TLS trust |
| `testkit-tls`       | TLS support for test fixtures                     |

Install packaged plugins through Manager's **Plugins → Import plugin** flow and explicitly enable them. See the [SDK overview](../README.md) for shared contracts and the other language implementations.

## Troubleshooting

| Problem                                         | Next step                                                                      |
| ----------------------------------------------- | ------------------------------------------------------------------------------ |
| The crate cannot be resolved before publication | Use the explicit local Cargo configuration described above.                    |
| The packager ignores the local SDK              | Set `STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG` to the absolute configuration path. |
| An optional API is missing                      | Enable its Cargo feature and keep that feature list in local overrides.        |

## License

The SDK is [MIT-licensed](../LICENSE). Optional codec and networking dependencies retain their own licenses; preserve their package notices.
