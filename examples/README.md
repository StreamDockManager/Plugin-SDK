# Starter plugins

Start with a working Counter. It increments once on button release, has a local settings
panel, and keeps a separate count for each binding.

**Version 0.1.1 · Development packages · MIT**

[Pick a starter](#pick-a-starter) · [Build and install](#build-and-install) ·
[Generator](#create-your-own-project) · [Validate](#validate-behavior) ·
[Unified bundles](#optional-unified-plugin-bundles)

## Pick a starter

[Clone the SDK](../README.md#get-the-source), install [your language's build tools](../README.md#install-build-tools),
and follow the setup in the corresponding starter guide. You only need one toolchain.

| Starter                                         | SDK setup                                                      | What the ZIP needs at runtime             |
| ----------------------------------------------- | -------------------------------------------------------------- | ----------------------------------------- |
| [Go Counter](sdk-example-go/README.md)          | Download the pinned Go SDK and verify checksums                | Included native executable                |
| [Rust Counter](sdk-example-rust/README.md)      | Use the exact crate version with the local Cargo override      | Included native executable                |
| [Python Counter](sdk-example-python/README.md)  | In a venv, install the SDK and pinned PyInstaller requirements | Included Python interpreter               |
| [Node.js Counter](sdk-example-nodejs/README.md) | Build `Node/` and set `STREAM_DOCK_MANAGER_SDK_JS_ROOT`        | Supported Node.js LTS selected in Manager |

## Build and install

From the SDK root, run the packager for the language you set up:

```sh
python3 examples/tools/package.py go
```

Replace `go` with `rust`, `python`, or `nodejs`. For Python, use the active environment's
interpreter. The command prints a ZIP path in `examples/artifacts/`; each ZIP contains one
`.sdPlugin` folder, its runtime files, artwork, and license notices.

1. In Manager, choose **Plugins → Import plugin**, then review and install the ZIP.
2. Enable the plugin. For Node.js, choose a supported LTS runtime if prompted.
3. Assign **Counter** to a button and **Save binding**. Press and release once to see
   **Count 0** become **Count 1**. Use plugin settings to edit the prefix or reset the count.

Native archives are OS/CPU-specific. A successful build does not qualify the plugin on another
host. See the [host compatibility table](../README.md#host-compatibility-at-a-glance) before
using a different app; the VSD Craft bundled runtime does not support the Node.js SDK.

## Create your own project

Use the [generator command](../README.md#create-a-plugin) with a unique plugin ID and a
destination that does not exist. Its parent directory must exist. The generator runs offline
and creates the source, assets, inspector, manifest, dependency declarations, and tools.
It does not build, install, or publish the plugin.

After following the generated README's dependency setup, run:

```sh
python3 tools/validate.py --project .
python3 tools/package.py --project .
```

Use `--no-build` only to repackage an already-built artifact. To verify a specific ZIP,
run `python3 tools/validate.py --project . --package /absolute/path/to/plugin.zip`.

| Layout            | Build command from its root            | Output                |
| ----------------- | -------------------------------------- | --------------------- |
| SDK checkout      | `python3 examples/tools/package.py go` | `examples/artifacts/` |
| Generated project | `python3 tools/package.py --project .` | `artifacts/`          |

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

For local SDK setup and development checks, see the [SDK overview](../README.md) and the linked language guides.

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

OS-specific ZIPs remain the default for Manager/VSD packages. The native merger works
with Go, Rust, and C++ archives. It does not combine frozen Python or Node.js packages,
cross-compile executables, or execute foreign binaries. Manager support for exact OS/CPU
`CodePaths` is required. For portable Node.js OpenDeck export, use the
[separate JavaScript workflow](../REFERENCE.md#creating-opendeck--openaction-packages).

Combine archives from the **same source revision and version** after validating
each target package. Run this command from `examples/`:

```sh
python3 tools/unified.py \
  --input windows/amd64=/path/plugin-windows-amd64.zip \
  --input darwin/arm64=/path/plugin-darwin-arm64.zip \
  --input linux/amd64=/path/plugin-linux-amd64.zip \
  --output /path/plugin-unified.zip
```

For OpenDeck native plugins, pass `--host opendeck` both when merging and validating.
All three operating systems are required. The inputs are regular Manager-format target
archives. `package.py --host opendeck --input OS/ARCH=ZIP ...` also accepts those inputs.
Do not distribute a single-target native archive as an OpenDeck release.

| Routing or verification  | Behavior                                                                                                                                 |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------- |
| Modern host              | Exact OS/CPU entries in `CodePaths`                                                                                                      |
| Legacy OS fields         | `CodePathWin`, `CodePathMac`, and `CodePathLin` use x86_64 if included, otherwise ARM64; legacy hosts cannot switch to another CPU build |
| Additional architectures | Repeat `--input`; every executable header must match the declared target                                                                 |
| Integrity                | Identity, actions, capabilities, version, and shared assets must match; inputs remain unchanged                                          |
| Archive layout           | Runtime files under `bin/<os>-<arch>/`; dependency notices and checks under `target-audits/<os>-<arch>/`                                 |

OpenDeck mode writes OpenAction image references and records the host in its inventory.
Use a separate output name such as `plugin-opendeck-unified.zip`. Output is deterministic,
bounded, and replaced atomically. Package validation does not establish native execution.

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
