# Starter plugins

Try a working Counter plugin, then adapt it into your own project. Every starter shows a count, increments once on button release, saves per-binding settings, and includes a local settings panel.

**Four starters · Version 0.1.0 · Development packages · MIT**

Start with one language below. Build its ZIP, install it in Manager, and confirm the Counter works before changing its identity or behavior.

[Choose a starter](#pick-a-starter) · [Build and install](#build-and-install) · [Create a project](#create-your-own-project) · [Validate](#validate-behavior) · [Unified bundles](#optional-unified-plugin-bundles)

## Pick a starter

| Starter                                 | Build tools                                            | User runtime                              |
| --------------------------------------- | ------------------------------------------------------ | ----------------------------------------- |
| [Go](sdk-example-go/README.md)          | Go 1.27.4+, Python 3.11+                               | Native executable                         |
| [Rust](sdk-example-rust/README.md)      | Rust 1.89+, Cargo, Python 3.11+                        | Native executable                         |
| [Python](sdk-example-python/README.md)  | Python 3.11+, venv, pinned PyInstaller dependencies    | Included Python interpreter               |
| [Node.js](sdk-example-nodejs/README.md) | Node.js 26 for repository authoring, npm, Python 3.11+ | Supported Node.js LTS selected in Manager |

Need the tools? Start with the [installation links and platform notes](../README.md#install-build-tools). You do not need all four toolchains to build one starter.

Follow the language-specific instructions first. The Go, Rust, Python, and Node starters depend on the standalone SDK; provision its pinned version or an explicit local development override before packaging.

| Language | SDK setup before packaging                                                                                     |
| -------- | -------------------------------------------------------------------------------------------------------------- |
| Go       | Download the revision and checksums pinned in `go.mod` and `go.sum`.                                           |
| Rust     | Use the exact crate dependency; before publication, supply the documented local Cargo patch configuration.     |
| Python   | Activate a virtual environment, install the local SDK, then install the starter's pinned requirements.         |
| Node.js  | Build `Node/` and set `STREAM_DOCK_MANAGER_SDK_JS_ROOT` to that absolute directory for the maintained starter. |

## Build and install

From the SDK repository root, after provisioning dependencies:

```sh
python3 examples/tools/package.py go
```

Replace `go` with `rust`, `python`, or `nodejs`. For Python, use the active development environment's interpreter. ZIPs are written to `examples/artifacts/`; the command prints the exact path. Each archive contains one installable `.sdPlugin` folder, its runtime files, assets, and license notices.

1. Open **Plugins → Import plugin** in Manager and select the ZIP.
2. Review and install it. For Node.js, choose a supported LTS runtime.
3. Enable the plugin and bind **Counter** to a button.
4. Save the binding, then press and release the button.
5. Open plugin settings to change the label prefix or reset the count.

Each binding has its own count. Native packages are platform-specific. Build and qualify them on their target OS/architecture; a successful cross-build alone does not verify execution. VSD Craft and Elgato reference-host compatibility remain unverified.

## Create your own project

Run from the SDK repository root:

```sh
python3 examples/tools/generate.py \
  --destination /absolute/existing-parent/my-plugin \
  --identity com.example.my-plugin \
  --language nodejs \
  --name "My plugin"
```

The destination must not exist, and its parent directory must already exist. Generation is offline and includes the source, assets, inspector, manifest, dependency declarations, and tools. It does not build, install, or publish anything. Choose one of the four language names from the table above; Node.js uses `nodejs`.

In the new project, provision the language dependencies, then run:

```sh
python3 tools/validate.py --project .
python3 tools/package.py --project .
```

Use `--no-build` only when repackaging an already-built artifact. The packager prints the ZIP path. Validate a specific archive with `python3 tools/validate.py --project . --package /absolute/path/to/plugin.zip`.

| Layout                           | Build command                          | Output directory      |
| -------------------------------- | -------------------------------------- | --------------------- |
| This SDK checkout, from its root | `python3 examples/tools/package.py go` | `examples/artifacts/` |
| Generated project, from its root | `python3 tools/package.py --project .` | `artifacts/`          |

## Customize the display

| Change                                 | Where to make it                                           |
| -------------------------------------- | ---------------------------------------------------------- |
| Prefix or count reset                  | Plugin settings                                            |
| Fonts, colors, layout, and state icons | **Icon, label & live display → Plugin display** in Manager |
| Action behavior                        | The starter's source file                                  |
| Dynamic assets or workers              | `plugin-project.json`                                      |
| Identity and advertised actions        | The manifest and generated project configuration           |

Appearance changes use **Apply appearance**, followed by **Save binding**. Cancel discards the appearance draft. Suggested layouts are included in the packages.

## Example tools

All Python and JavaScript authoring scripts live in [`tools/`](tools/). Run these commands from the SDK repository root:

| Task                                        | Command                                                                           |
| ------------------------------------------- | --------------------------------------------------------------------------------- |
| Generate a project                          | `python3 examples/tools/generate.py --help`                                       |
| Build a maintained starter                  | `python3 examples/tools/package.py go`                                            |
| Check starter sources and existing packages | `python3 examples/tools/validate.py`                                              |
| Run host acceptance                         | `python3 examples/tools/acceptance.py go --host /absolute/path/to/sdk_acceptance` |
| Run authoring-tool tests                    | `python3 -m unittest discover -s examples/tests`                                  |

`project.py`, `profiles.py`, `descriptor.py`, `audit.py`, `buildcache.py`, `cpp_transport.py`, and `javascript.py`/`javascript.mjs` support these commands. The packager invokes the JavaScript bundling helper; you do not need to run it directly. Generated standalone projects include their own `tools/` directory and usage instructions.

## Validate behavior

Static checks, from the SDK root:

```sh
python3 examples/tools/validate.py
```

For host acceptance, obtain the [Manager source](https://github.com/StreamDockManager/Manager) and its native build prerequisites. From that separate Manager checkout, build the Counter acceptance host:

```sh
python3 scripts/prepare_patched_deps.py
cargo build --locked -p stream-dock-manager-plugin-host --example sdk_acceptance
```

By default the executable is `target/debug/examples/sdk_acceptance` (`sdk_acceptance.exe` on Windows). If you use a custom Cargo target directory, use its corresponding output path. Return to the SDK repository root and pass the absolute executable path; the plugin ZIP must already be built:

```sh
python3 examples/tools/acceptance.py go --host /absolute/path/to/sdk_acceptance
```

For Node.js, set `STREAM_DOCK_MANAGER_NODE` to a compatible executable when selecting the test runtime. The acceptance host uses disposable data; it does not install into your normal Manager configuration. Native hardware, desktop permissions, and other platforms need separate qualification.

Generated projects require both `--host` and `--scenario`. To test an unmodified generated Counter, run `python3 tools/acceptance.py --project . --host /absolute/path/to/sdk_acceptance --scenario counter` inside that project. For a customized plugin, build Manager's `sdk_protocol_probe` example and select `--scenario protocol` instead. Node.js generated projects require an explicit absolute `STREAM_DOCK_MANAGER_NODE` path.

For local SDK setup and development checks, see the [SDK overview](../README.md) and the language guides linked there.

## Maintain the starter templates

The commands in this section run from `examples/`, not the SDK root.

The canonical starter property inspector is in `inspector/`. Run
`python3 ../tools/sync_starter_inspectors.py` after editing it. SDK checks reject
drift across the four self-contained starter snapshots; generated projects retain
their own inspector files for customization.

Authoring primitives (`tools/authoring_common.py`) are MIT-licensed, versioned in
`tools/authoring_common.v1.json`, and copied into generated projects. Run
`python3 tools/sync_authoring.py --check` from this directory to check the canonical
snapshot. To update a consumer explicitly, pass its tools directory to that command
without `--check`; use `--check` with the same directory to detect drift. Product-specific
capability, licensing, packaging and profile safety policies remain in their wrappers.

## Optional unified plugin bundles

| Choose          | When to use it                                                                 |
| --------------- | ------------------------------------------------------------------------------ |
| OS-specific ZIP | The normal, smaller download for one operating system and CPU.                 |
| Unified ZIP     | An optional single download with Windows, macOS, and Linux native executables. |

For Manager/VSD, OS-specific archives remain the normal distribution and Manager download default. OpenDeck requires a universal archive; see the OpenDeck instructions below.
A unified archive is an optional single-plugin download containing separate native
runtimes for Windows, macOS, and Linux, selected by exact OS/CPU `CodePaths`. It
requires a Manager version supporting native `CodePaths`; it is not a claim of
compatibility with third-party hosts. This merger supports native Go, Rust, and C++ packages. Node.js and frozen Python
packages are not supported by this merger; their existing package workflows remain
unchanged. Native builds must still be produced and validated with each platform's toolchain.
The merger never executes foreign binaries or cross-compiles them implicitly.

Combine archives from the **same source revision and version** after validating
each target package. Run this command from `examples/`:

```sh
python3 tools/unified.py \
  --input windows/amd64=/path/plugin-windows-amd64.zip \
  --input darwin/arm64=/path/plugin-darwin-arm64.zip \
  --input linux/amd64=/path/plugin-linux-amd64.zip \
  --output /path/plugin-unified.zip
```

For OpenDeck, universal output is mandatory. Add `--host opendeck` to both
merging and validation commands. Native inputs are normal Manager-format target
archives; `package.py --host opendeck --input OS/ARCH=ZIP ...` is also available.
A single native project cannot produce a system-specific OpenDeck release.
This exports OpenAction image references, accepts its `CodePathLin` override, and
records the selected host in the archive inventory. Use a separate output filename
such as `plugin-opendeck-unified.zip`. Modern OpenDeck uses exact `CodePaths`. Legacy `CodePathWin`, `CodePathMac`
and `CodePathLin` entries select x86_64 when included, otherwise ARM64. Older hosts
without `CodePaths` support can run only that selected fallback architecture;
they cannot automatically select another CPU build.
This remains native Go/Rust/C++ packaging, not Node.js or frozen Python qualification.

Repeat `--input` for additional supported architectures. All three operating
systems are required. Inputs must agree on identity, actions, capabilities,
version, and shared assets. Executable headers must match the declared targets.
Runtime files retain their relative layout under `bin/<os>-<arch>/`; licenses
and dependency reports are retained under `target-audits/<os>-<arch>/`. Inputs
are not modified. Output is deterministic, bounded, and replaced atomically.
Node.js starter packages already carry portable JavaScript; this native merger
does not combine them. Unsupported platforms must not be advertised as supported.

Validate unified output with `python3 tools/unified.py --validate /path/plugin-unified.zip`.
The merger runs this check automatically before replacing output. The existing
`tools/validate.py --package` command validates source-matched OS-specific archives,
not unified archives. Unified validation checks safe inventory, manifest routing,
all target executable headers, and target metadata without executing binaries;
it does not replace per-target source checks or native runtime testing.

## Troubleshooting

| Problem                                    | Next step                                                                                                |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------------- |
| Packaging cannot find an SDK dependency    | Finish the selected language's setup first. Local candidates and overrides are documented above.         |
| `--project` fails for a maintained starter | Use the language argument for in-repository examples. `--project` selects a generated standalone layout. |
| Acceptance cannot find the host            | Build the host in the Manager checkout, then pass an existing absolute executable path.                  |
| The Counter appears but does not increment | Enable the plugin, save the binding, and test a full press and release.                                  |
| A package does not run on another platform | Build and test for that OS and CPU. The ZIP filename is not execution evidence.                          |

## License

The maintained starters and shared authoring tools are [MIT-licensed](../LICENSE). Generated projects and example ZIPs include the license and [third-party notices](THIRD_PARTY_NOTICES.md). Preserve those notices when distributing code or packages. Manager is a separate GPL-3.0-or-later application.
