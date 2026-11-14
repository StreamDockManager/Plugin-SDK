# Stream Dock Manager Plugin SDK

Build a plugin with **Go, Rust, Python, or Node.js**. Start with a working Counter plugin, customize its actions and settings, and package it as a ZIP. The default package targets Manager; the explicit OpenDeck export produces a universal package for macOS, Windows, and Linux.

**Development preview · SDKs and starters: 0.1.0 · Protocol: v1 · MIT**

New here? Choose one language and build its Counter starter first. You do not need to install every toolchain to write one plugin.

[Choose a language](#choose-a-language) · [Install build tools](#install-build-tools) · [Create a plugin](#create-a-plugin) · [Install it](#install-and-try-it) · [OpenDeck packages](#creating-opendeck--openaction-packages) · [SDK development](#develop-the-sdk)

## Independence and compatibility

This is an independent project and is not affiliated with, endorsed by, or sponsored by
VSDInside, MiraBox, OpenDeck, or Elgato Stream Deck. Product names are used to identify
products and describe compatibility; they do not imply an official relationship.

## Choose a language

| SDK                        | Best starting point                      | What users install                                                    |
| -------------------------- | ---------------------------------------- | --------------------------------------------------------------------- |
| [Go](Go/README.md)         | Native plugins and service integrations  | A compiled plugin ZIP                                                 |
| [Rust](Rust/README.md)     | Native plugins with Rust APIs            | A compiled plugin ZIP                                                 |
| [Python](Python/README.md) | Python plugins packaged with PyInstaller | A ZIP including the Python interpreter                                |
| [Node.js](Node/README.md)  | JavaScript or TypeScript plugins         | A plugin ZIP plus a supported Node.js LTS runtime selected in Manager |

## Install build tools

Install only the tools for your chosen language. These are **developer requirements**; plugin users need Manager and, for JavaScript plugins, a supported Node.js LTS runtime.

| Tool                 | Required for                                      | Install and check                                                                                                          |
| -------------------- | ------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| Git                  | Getting the source                                | [Install Git](https://git-scm.com/downloads), then `git --version`                                                         |
| Python 3.11+         | All starter generation, validation, and packaging | [Install Python](https://www.python.org/downloads/), then `python3 --version`                                              |
| Go 1.27.4+           | Go SDK and starter                                | [Install Go](https://go.dev/doc/install), then `go version`                                                                |
| Rust 1.89+ and Cargo | Rust SDK and starter                              | [Install Rust](https://www.rust-lang.org/tools/install), then `rustc --version` and `cargo --version`                      |
| Node.js and npm      | Node SDK and starter                              | [Install Node.js](https://nodejs.org/en/download); repository CI uses Node 26. Check `node --version` and `npm --version`. |

Commands use a POSIX shell unless a Windows alternative is shown. On Windows, use `python` or `py -3` in place of `python3` when that is your Python command. Native builds also need the platform's linker and development tools, such as Xcode Command Line Tools on macOS or Visual Studio C++ Build Tools on Windows.

SDK packages are built from this public source checkout. The Node.js SDK is not deployed to an npm registry: its npm tarball is for local installation, testing, and file-based distribution. The language guides describe each SDK’s setup. First-time dependency installation and builds may need network access; the project generator itself is offline.

## Get the source

```sh
git clone https://github.com/StreamDockManager/Plugin-SDK.git
cd Plugin-SDK
```

For the shortest route to a working plugin, open your language's [Counter starter guide](examples/README.md#pick-a-starter), build its ZIP, and follow [Install and try it](#install-and-try-it). The SDK libraries are not installed as plugins on their own.

## Create a plugin

From the SDK repository root:

```sh
python3 examples/tools/generate.py \
  --destination /absolute/existing-parent/my-plugin \
  --identity com.example.my-plugin \
  --language nodejs \
  --name "My plugin"
```

Choose `nodejs`, `python`, `go`, or `rust`. The destination must not already exist. Generation is offline and creates source, assets, a local settings inspector, dependency declarations, and packaging tools.

Open the generated project's README and provision its language dependencies. Then, inside the new project:

```sh
python3 tools/validate.py --project .
python3 tools/package.py --project .
```

The packager prints the output ZIP path under the new project's `artifacts/` directory. It builds only when you explicitly run it; plugins do not download dependencies or invoke compilers when launched by Manager.

Prefer an existing example? Follow the [starter guide](examples/README.md).

## Install and try it

1. Open **Plugins → Import plugin** in Manager and select the ZIP.
2. Review the package and install it.
3. For Node.js, select or install a supported LTS version when prompted.
4. Enable the plugin, assign its Counter action to a button, and save the binding.
5. Press and release the button. Its count should increase once. Open plugin settings to change its label or reset the count.

Examples are development packages and are not automatically added to the official catalog.

## Troubleshooting

| Problem                                   | What to check                                                                                                      |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| A dependency cannot be found              | Follow the language guide’s local SDK setup; build the Node.js SDK from this public checkout, not an npm registry. |
| The generator refuses the destination     | Choose a new folder inside an existing parent directory. It will not overwrite a project.                          |
| The plugin installs but does nothing      | Enable it, assign its action, and save the binding. Node plugins also need a supported LTS runtime.                |
| A native package fails on another machine | Match both operating system and CPU architecture. Building an archive does not qualify it on another platform.     |
| A host storage call is unavailable        | Check negotiated capabilities; do not silently fall back to files or broader settings.                             |

## Shared capabilities

| Area            | Supported behavior                                                |
| --------------- | ----------------------------------------------------------------- |
| Lifecycle       | Registration, appearances, input, disappearance, and shutdown     |
| Display         | Titles, images, layouts, feedback, and state                      |
| Settings        | Portable action/global settings and local inspector UI            |
| Private storage | Optional negotiated `plugin-storage-v1` host storage              |
| Work            | Deferred completion, cancellation, and bounded background work    |
| Diagnostics     | Typed logs and optional network tracing with credential redaction |

### Reusable plugin helpers

All four SDKs support these helpers. Import only what your plugin needs.

| Helper             | Behavior                                                                                                                                                    |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Managed state      | Explicit session, portable-settings, or private-host mode; dirty tracking, record diffs, conflict detection, and receipt recovery after an uncertain commit |
| Discovery          | A shared inspector controller for request IDs, loading, cancellation, timeouts, stale replies, and preserving saved selections                              |
| Service WebSockets | Reusable authenticated sessions, bounded correlated requests, heartbeats, independent request cancellation, and uncertain-command errors                    |
| Observation jobs   | Refresh display data after a command without changing that command's completion result                                                                      |
| Shared reads       | Coalesce matching reads while keeping subscriber cancellation independent; retain Retry-After cooldowns                                                     |
| Semantic displays  | Primary value, label, status, progress, and compact layouts for small square screens                                                                        |
| Bounded caches     | Entry/byte budgets, LRU eviction, expiry, shared byte reservations, and optional private-host persistence                                                   |
| Backend discovery  | Bounded pending requests, request replacement, matching cancellation, deadlines, and bounded reply envelopes                                                |
| Operation history  | Reserve capacity before submission; track pending, confirmed, failed, and uncertain outcomes without automatic retries                                      |
| Thumbnail decoding | Optional JPEG, PNG, and first-frame GIF decoding, aspect-fit sizing, PNG/data-URI output, and cancellation checks                                           |

<details>
<summary>Helper behavior, resource limits, and integration notes</summary>

Use `StreamDockManagerWorkflows.DiscoveryController` from the packaged inspector assets. Send requests through `inspector.send`, pass matching replies to `receive(payload, payload.devices)`, and use `selection(savedId)` to retain a saved item that is absent from the latest results. Standard connector replies echo `request_id`. Cancellation retires a picker request immediately; provide `cancelOp` when your plugin supports cancelling the underlying read. Otherwise the read keeps its existing timeout and its late reply is ignored.

Managed state never chooses a persistence mode for you. Private state requires negotiated host storage; conflicts require an explicit reload/reconciliation. An unacknowledged portable-settings send remains `sent`, not a confirmed commit.

Service sessions authenticate through a caller-supplied handshake and use normal system TLS trust. Use a fresh request ID for each request within a live session. Commands are never replayed automatically: once sent, a lost reply can mean an uncertain outcome. Shared reads are for idempotent reads only, with at most 32 retained operations, an eight-second shared deadline, and a 1 MiB JSON result limit. A cancelled worker retains its capacity slot until it exits.

Thumbnail inputs default to 64 MiB and 32,000,000 source pixels, with outputs from 1×1 to 320×320. All SDKs use top-left nearest-neighbor sampling and the first GIF frame. Lossless fixtures match; JPEG color/rounding may differ between codec libraries. Cancellation is cooperative, so run decoding in background work; Node plugins should use a declared worker for responsive cancellation. All implementations may temporarily hold the bounded full source image. The Go SDK uses standard-library codecs without copied parser sources.

Supply per-call thumbnail limits to accept larger inputs or enforce a smaller plugin-specific budget. A 32 MP RGBA8 buffer is about 128 MB before source buffers and decoder scratch space; higher limits increase potential memory use. The 320×320 output cap remains fixed. Limits do not download data: apply the same byte limit to your HTTP/file reader. See each language guide for an override example. Codec-specific limits still apply; the SDK does not disable process-wide library safeguards.

Cache values are immutable caller-owned snapshots. Charge their retained size when inserting; the SDK also charges UTF-8 keys and entry overhead. A cache has no hidden file fallback. `Persistence` uses an explicitly selected private host namespace, restores atomically, and flushes in batches of at most 32 records with commit-receipt recovery. A flush spanning several batches is not one atomic transaction. LRU recency is session-local; values and expiry timestamps persist. Weather retains its existing forecast serialization for compatibility.

The backend discovery `Runner` coordinates at most 32 pending requests. Schedule its tickets through bounded background work, complete them on the owner loop, call `expire`/`Expire` at the next deadline, and clear the owning context when it closes. Replacing or canceling a request cancels only its ticket; stale cancellation IDs and late replies are ignored. Replies are bounded to 256 KiB. Go's `Runner.Run` connects this directly to `authoring.Engine`. The other SDKs expose the same coordinator with their native cancellation signals, events, or atomic flags for use with their background executors. Discovery is for reads; keep commands separately scheduled.

Operation ledgers never perform I/O or retry a request. Reserve a record before submitting a side effect, then update its outcome. Only retire confirmed or failed records from inactive groups; pending, uncertain, and pinned records stay protected. Cache and ledger byte budgets measure logical retained values rather than total process memory. Serialize access on the owner loop or with your own lock; shared `Budget` reservations are thread-safe where the language supports threads.

HTTP `Retry-After` parsing is shared by each SDK's HTTP and service clients.
Unsigned ASCII seconds and the three HTTP date formats are supported. Delays clamp
to 9,223,372,036 seconds, the largest whole-second Go duration; oversized digit
strings never overflow or throw. Past dates clamp to the caller's current time.
Dates require exact English token case, a matching weekday, years 1970–9999 and
UTC; RFC850 two-digit years use 00–69 for 2000–2069 and 70–99 for 1970–1999.
Invalid and signed values have no retry timestamp. Shared regression cases live
in `contracts/fixtures/retry-after-v1.json`.

Host extensions are negotiated. Private storage does not silently fall back to files or a broader settings scope. The four SDKs share protocol semantics while using each language's conventions.

</details>

## Portable dynamic images

For runtime `setImage` updates shared between Manager and OpenAction/OpenDeck,
encode trusted PNG or SVG bytes as a data URI instead of relying on filename
suffix rules. Manager uses exact file paths; other hosts may append an image
suffix. Existing image/path methods are unchanged and do not rewrite paths.

| SDK     | Byte encoder                              |
| ------- | ----------------------------------------- |
| Go      | `sdk.ImageDataURI("image/png", pngBytes)` |
| Rust    | `image_data_uri("image/png", &png_bytes)` |
| Python  | `image_data_uri("image/png", png_bytes)`  |
| Node.js | `imageDataURI('image/png', pngBytes)`     |

These package-root helpers accept only `image/png` or `image/svg+xml` and
1–524,288 input bytes, returning a base64 `data:` URI. They perform no filesystem
or network access, grant no permissions, and do not decode or sanitize images.
Use trusted image content, especially for SVG. The normal complete-message size
limit still applies. The maintained Go/Rust/Python/Node counter starters use
trusted inline SVG data URIs for their dynamic image updates; manifest assets remain
packaged separately. PNG is also supported when a target renderer lacks SVG.

The Go, Python and Node.js starters use standard-library encoding for their two
fixed SVG literals; Rust embeds the equivalent pre-encoded literals. This keeps
all starters compatible with their existing SDK dependencies. New plugin code
can use the bounded public helpers above. No dependency pins are changed.

## Vendor compatibility qualification

The SDK is intended to support backward compatibility with **VSD Craft**, **OpenDeck**, and **Elgato Stream Deck**, where their plugin protocols and available capabilities overlap. This is a design goal, not verified support or a guarantee that every SDK-built plugin works unchanged in all three hosts. Test the specific plugin, host version, operating system, and required features before claiming support. OpenDeck is not currently included in the reference-host qualification matrix below.

**Node.js SDK plugins are currently unsupported in VSD Craft because its bundled Node.js runtime does not meet this SDK’s requirements.** Selecting a newer runtime in Manager does not upgrade VSD Craft’s runtime. In this host build, the test plugin’s inspector could open and save settings, but its Node.js backend registration was not observed; inspector success is not proof that the plugin runs. Other VSD Craft versions and platforms need separate runtime and native-host verification. Use Manager with a supported LTS runtime for Node.js plugins; do not lower the SDK’s runtime requirement to claim VSD Craft support.

### Creating OpenDeck / OpenAction packages

OpenDeck distribution is **universal only**: one ZIP covers macOS, Windows and
Linux. Manager/VSD packages keep their existing system-specific workflow.

For native plugins, first build the normal target archives on each target
platform, then combine matching versions and assets without executing foreign
binaries:

```sh
python3 examples/tools/package.py --host opendeck \
  --input darwin/arm64=/path/plugin-macos.zip \
  --input windows/amd64=/path/plugin-windows.zip \
  --input linux/amd64=/path/plugin-linux.zip \
  --output /path/releases
```

This emits `<plugin-id>-opendeck-universal.zip`. Missing operating systems are
rejected. Repeat inputs for additional CPU architectures. Modern OpenDeck selects
exact `CodePaths` target triples; legacy per-OS paths select x86_64 when provided,
otherwise ARM64. Older hosts can run only that fallback architecture. Validate
with `examples/tools/unified.py --host opendeck --validate /path/archive.zip`.
Native toolchains and platform-specific dependency qualification remain required.

For portable Node.js plugins, `python3 examples/tools/package.py --project
/path/to/plugin --host opendeck` builds one JavaScript archive declaring all three
systems. Platform-specific entrypoints, restricted OS declarations and packaged
native library/executable extensions are rejected. This does not prove arbitrary
JavaScript dependencies portable. A supported Node.js LTS installation must be
available to OpenDeck on every destination. Validate this archive with
`examples/tools/validate.py --host opendeck --project /path/to/plugin --package
/path/archive.zip`.

Exports preserve source manifests and required capabilities. OpenAction image
references use deterministic byte-preserving aliases when PNG/SVG stems collide.
No per-system OpenDeck release ZIP is emitted; `--host manager` remains unchanged.

The four SDKs use compatible native WebSocket registration arguments and shared action/settings events. Their shared inspector bridge exposes `connectOpenActionSocket` alongside the existing registration names. Optional device-provider, settings-window, and deep-link helpers are described below; these do not qualify every OpenAction extension or host. Plugins requiring Manager-only negotiated capabilities must reject hosts that do not supply them.

#### OpenDeck device-provider extension

The opt-in `OpenActionDeviceProvider` helpers construct namespace-scoped provider envelopes for registration/removal, image refresh, key/encoder input, and touchscreen presses. They parse the host’s provider `setImage` and `setBrightness` outputs separately from action-plugin messages. Go exposes the type in its SDK package; Rust uses `openaction_device`; Python exposes `openaction_device`; Node exposes the `stream-dock-manager-plugin-sdk/openaction-device` package subpath. These helpers do not send anything, open hardware, allocate namespaces, or change default Manager/VSD behavior. Declare the same two-character ASCII `DeviceNamespace` in the manifest, obtain a namespace appropriate for your plugin, and explicitly route the envelopes through your owned plugin connection. The host still enforces namespace ownership; constructing a helper is not authorization.

This extension follows the pinned OpenDeck implementation, not a claim that every OpenAction host implements device providers. It is unrelated to Manager capability grants. Treat host image strings as untrusted, bounded input; parsing does not fetch URLs, sanitize SVG, decode images, or apply brightness to hardware. A provider `setImage` with null controller, position, and image requests a screen clear. Provider-specific hardware transport, platform permissions, disconnect cleanup, and real-device qualification remain the plugin author’s responsibility. The source-pinned envelope inventory is in [the provider contract](contracts/vendor/openaction-device-provider.json). Automated protocol checks do not replace testing a provider with its target OpenDeck version and hardware.

#### Opt-in settings UI and deep-link notifications

Set `HasSettingsInterface: true` in your manifest only if your plugin implements a settings window. Handle the host’s `showSettingsInterface` event through your existing SDK event handler; the SDK does not create or open a window automatically. The opt-in helpers are `OpenActionSettingsRequested` / `OpenActionDeepLink` in Go, `openaction_settings_requested` / `openaction_deep_link` in Rust’s `openaction_events` module and Python, and `openActionSettingsRequested` / `openActionDeepLink` in Node.js.

The deep-link helper reads `didReceiveDeepLink.payload.url`, rejects missing or non-string URLs, and returns the original string without decoding, opening it, or executing commands. Treat that value as untrusted input and validate it before acting. OpenAction documents `openaction://plugins/message/<PLUGIN_DIRECTORY_NAME>/<MESSAGE_CONTENT>` and the Stream Deck-compatible URL form; these helpers do not install URL handlers or assume other hosts support them. Unrelated events return an unmatched result. These additions do not alter Manager/VSD defaults or establish native-host qualification.

References: [OpenAction API](https://openaction.amankhanna.me/), including its manifest image-path and registration contracts, and [OpenDeck source revision `3ddecc1`](https://github.com/nekename/OpenDeck/tree/3ddecc1ce8952a423916cf066724356267b97410). Source review found that `OS` is required even for Node.js plugins and that image references are resolved by appending `.svg`, `@2x.png`, or `.png`; a reference such as `icon.png` therefore resolves incorrectly. Source review is not installed-host qualification. The new export path still requires native-host testing for each package and platform.

**Compatibility is a target, not a release certification.** The reference-host matrix is still unverified. Manager-only capabilities require negotiation, and the SDK does not bypass protected packages or vendor licensing.

<details>
<summary>Reference hosts, release gates, and compatibility contracts</summary>

Current VSD Craft and Elgato Stream Deck plugin interoperability is a required target, not yet a qualified release claim. The [consumer matrix](contracts/vendor/compatibility.json) covers all four SDKs, four starters, official plugins on macOS and Windows. Every row records vendor application build, runtime, device/controller, date, evidence and outstanding blockers. These reference-host rows are currently **unverified**. Linux portability is separate from running either vendor application.

Source baselines for review are VSDinside `4ac0234ed42196d782f6b663b2f82c2f3d159dfe` and Elgato `9d9636e9ea956ff4be0246e7ab8e3679866b324f`, package `@elgato/streamdeck` 3.0.4. These are pinned source baselines, not vendor application versions or proof of complete current support. Use a supported Node LTS; the upstream package's minimum engine version is not a runtime recommendation.

Run `python3 tools/check_vendor_compatibility.py` to validate the matrix and `--release` to enforce every required reference-host row. The release gate intentionally fails until qualification is complete. Weekly CI uses its read-only GitHub token to review official repository revisions, the `@elgato/streamdeck` and `@elgato/schemas` package versions, and Elgato’s official JSON release-note feed. Local `--upstream` review uses anonymous public requests and never reads personal saved credentials. Only GitHub Actions uses its supplied read-only `GITHUB_TOKEN`. Requests use HTTPS, fixed official providers, bounded responses and no redirects; credentials go only to the GitHub API. No downloaded vendor code is executed. VSD Craft has no established authoritative machine-readable app version feed: its download page remains a documented manual-review blocker, not successful automated coverage. App release-note baselines are observations, not supported versions or installer build qualification. Review VSD app releases manually before every release; update licensed fixtures and command/event coverage and rerun reference-host qualification before updating claims.

Standard vendor feedback/layout commands do not require Manager capabilities. Inspector messages preserve arbitrary JSON payloads, including arrays and scalars. These behaviors are regression-tested against pinned, licensed Elgato contract source included with the matrix; they do not establish complete vendor-host qualification.

On hosts without Manager extensions, portable action/global settings remain host-owned. Private storage, receipts and other Manager features require explicit capability negotiation; lack of these capabilities must not trigger local-file persistence. Vendor installation and browser inspector behavior still need packaged reference-host testing. This SDK does not grant Marketplace access or bypass DRM, licensing, vendor-only services, proprietary dependencies, or platform-native binary requirements.

</details>

## Scheduling and saved-state rules

<details>
<summary>Rules for timed work and explicit file storage</summary>

### Explicit scheduling

Across all four SDKs, a poll callback alone no longer enables hidden one-second polling. Implement the scheduler/next-poll method and return an explicit deadline for timed work. Returning no deadline leaves an event-only handler idle. Maintained authoring engines and connectors already implement this contract.

### Current file-state contract

Explicit file storage accepts only a `{"version":1,"data":...}` envelope. Raw JSON, version zero, other versions and malformed state are rejected before mutation, and writes remain blocked until an explicit successful reload. No automatic migration, deletion, reset or overwrite is performed. Official production plugins use host-managed state. Go `Restore` returns its error directly; the split `RestoreError` API and storage migration callbacks have been removed.

</details>

## Develop the SDK

You only need this section when changing the SDK itself. Writing one plugin does not require building all four libraries.

Run from the repository root after installing all four language toolchains. Create and activate a Python environment first:

```sh
python3 -m venv .venv
. .venv/bin/activate
python tools/build.py deps
python tools/build.py check
python tools/build.py test
python tools/build.py package
```

On Windows PowerShell, activate with `.venv\Scripts\Activate.ps1`; in Command Prompt, use `.venv\Scripts\activate.bat`. Full checks use Go 1.27.4+, Rust, Node.js 26/npm, and Python with the thumbnail extra installed by `deps`.

| Command                           | What it does                                                                    | Make equivalent  |
| --------------------------------- | ------------------------------------------------------------------------------- | ---------------- |
| `python tools/build.py deps`      | Install/fetch SDK development dependencies                                      | `make deps`      |
| `python tools/build.py check`     | Check shared snapshots/parity, compile SDKs, and validate starter sources       | `make check`     |
| `python tools/build.py test`      | Run cross-language behavioral, inspector, and authoring-tool tests              | `make test`      |
| `python tools/build.py package`   | Prepare local Go ZIP, Rust crate, Python wheel, and npm tarball in `artifacts/` | `make package`   |
| `python tools/build.py fmt-check` | Check the configured Node, Go, and Rust formatting                              | `make fmt-check` |

`package` prepares SDK distribution candidates, not the starter plugin ZIPs, and never publishes. Its Node.js tarball is a local artifact, not an npm registry deployment. The `examples/tools/package.py` command builds those plugins separately. Use each language guide to install or test a local SDK candidate in your plugin project.

| Folder                             | Contents                                                 |
| ---------------------------------- | -------------------------------------------------------- |
| `Go/`, `Rust/`, `Python/`, `Node/` | SDK implementations and language tests                   |
| `examples/`                        | Four starters, generator, packager, and acceptance tools |
| `contracts/`                       | Shared parity contracts and fixtures                     |
| `web/`                             | Shared inspector assets                                  |
| `tools/`                           | Build, synchronization, and parity checks                |

Regenerate shared assets/contracts through their existing tools. Behavioral tests are required for parity; matching API names alone is not enough.

## ZIP compression policy

First-party ZIP outputs use DEFLATE with explicit compression level 6 for every
member: the Go source SDK archive, Python SDK wheel, starter packages and unified
packages. Starter package cache keys include this policy. Freshly built Python
wheels are normalized without changing member payloads, metadata or RECORD
hashes; signed/encrypted and externally downloaded wheels are not rewritten.
Node npm tarballs and Rust Cargo `.crate` files keep their native non-ZIP formats.

## License

The SDKs, shared inspector assets, authoring tools, and maintained starters are licensed under [MIT](LICENSE). Keep the copyright and license notice when redistributing SDK code. Third-party dependencies retain their own licenses; see the [example dependency notices](examples/THIRD_PARTY_NOTICES.md).

Stream Dock Manager and the official plugins are separately licensed under GPL-3.0-or-later.

## Prepare and publish SDK downloads

Maintainers can use two manual GitHub Actions workflows from **main**. Neither runs on a
push or tag, and neither publishes to npm, PyPI, or crates.io. Do not change the package
versions just to retry a failed run.

For a development copy or fork, run **SDK checks** instead. It runs the same test matrix and
validates candidate packages. **Prepare Release** runs the checks there too, but only the canonical
repository can produce an artifact for publication.

1. Run **Prepare Release** on main. It runs the existing multi-platform SDK checks,
   builds the Go ZIP, Rust crate, Python wheel, and Node.js npm tarball, and validates
   the candidates outside their source checkout. The artifact
   `sdk-release-<run-id>-<attempt>` contains those four files, `SHA256SUMS`, and
   `release-manifest.json`; it expires after 14 days.
2. Review that exact successful run and its candidate files. Run **Publish Release**
   on main with its `prepare_run_id`. Publication checks the workflow, source commit,
   run attempt, GitHub artifact digest, inventory, and every package checksum before
   creating a GitHub **prerelease** with downloadable files.

For the current version, publication creates immutable lightweight tags `v0.1.0` and
`Go/v0.1.0` at the prepared source commit. The `Go/` prefix is required because the Go
module lives in a repository subdirectory ([Go module directories](https://go.dev/ref/mod#module-directories)).
The Go tag makes that module version discoverable by Go tooling; it is not an npm
registry deployment. Other language packages are installed from the downloaded files
as described in their language guides.

Retries must use the same prepare run and attempt. Matching existing tags/assets are
retained, missing draft assets can be uploaded, and conflicting bytes or tag targets
fail rather than being overwritten. A published incomplete release is not repaired
automatically. If a prepare rerun creates different candidates after publication,
use a separately reviewed version change instead of moving a version tag.

These are **development-preview downloads**, not a claim of completed reference-host
qualification. The normal compatibility inventory check still runs; the separate
`python3 tools/check_vendor_compatibility.py --release` qualification gate remains
strict and must pass before claiming vendor-qualified compatibility. Workflow tests
can be run offline with `python3 -m unittest discover -s tests -p test_release.py -v`.
No GitHub workflow or download is published by a local `make package` command.
