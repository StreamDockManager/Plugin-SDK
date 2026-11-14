# Rust Counter starter

Build your first plugin with a ready-to-use Counter. It displays a count, increments once on button release, and saves separate settings for each button binding. Build the ZIP first, try it in Manager, then customize the source.

| Item               | Value                                         |
| ------------------ | --------------------------------------------- |
| Version            | `0.1.0`                                       |
| Plugin ID          | `app.streamdockmanager.plugins.examples.rust` |
| Build requirements | Rust 1.89+, Cargo, Python 3.11+               |
| Package output     | `../artifacts/`                               |

**Before you start:** clone the [SDK repository](../../README.md#get-the-source), install this starter’s build tools, and have Stream Dock Manager available to install the finished ZIP.

[Build](#1-build-the-plugin) · [Install](#2-install-and-try-it) · [Customize](#3-make-it-yours) · [Verify](#check-behavior)

## 1. Build the plugin

Install [Rust 1.89+ and Cargo](https://www.rust-lang.org/tools/install), [Python 3.11+](https://www.python.org/downloads/), and your platform's native linker. Check `rustc --version`, `cargo --version`, and `python3 --version`.

The SDK's initial registry publication is separate from this checkout. To build with local SDK source, save this configuration **outside the project**, replacing the path with your SDK checkout:

```toml
[patch.crates-io]
stream-dock-manager-plugin-sdk = { path = "/absolute/path/to/StreamDockManager-Plugins-SDK/Rust" }
```

Then run from this `sdk-example-rust/` directory, using the absolute path to that configuration file:

```sh
export STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG=/absolute/path/to/sdk-local.toml
python3 ../tools/package.py rust
```

In PowerShell, set `$env:STREAM_DOCK_MANAGER_SDK_CARGO_CONFIG = 'C:\path\to\sdk-local.toml'` before running the Python command. The starter keeps an exact SDK version in `Cargo.toml` and a committed `Cargo.lock`; keep local paths out of those published dependencies. See the [Rust SDK setup](../../Rust/README.md#get-started) for details. End users do not need Rust.

The packager prints the finished ZIP path under `examples/artifacts/`. Its filename includes the language, operating system, and architecture. Build tools are used only during authoring; Manager never compiles the plugin at launch.

## 2. Install and try it

1. Open **Plugins → Import plugin** in Manager and select the ZIP.
2. Review and install the package.
3. Enable the plugin, assign **Counter** to a button, and save the binding.
4. Press and release the button. It should change from **Count 0** to **Count 1**.
5. Open plugin settings to edit the prefix or reset the count.

Each binding owns its own count. Appearance is edited separately through **Icon, label & live display → Plugin display**. Apply the appearance draft, then save the binding.

## 3. Make it yours

| Edit                        | File or location                        |
| --------------------------- | --------------------------------------- |
| Counter behavior            | `main.rs`                               |
| Plugin identity and actions | `manifest.json`                         |
| Settings UI                 | `inspector.html` and `inspector.js`     |
| Display elements            | Shared `../assets/display/counter.json` |

For a new identity and destination, use the [project generator](../README.md#create-your-own-project) instead of copying a built ZIP.

## Add optional features

The SDK also provides managed state, discovery workflows, service WebSockets, observation jobs, shared reads, compact displays, and thumbnail decoding. Follow the [Rust SDK guide](../../Rust/README.md#reusable-helpers) to enable dependencies and use these helpers. Keep optional codecs out of the Counter unless an action needs artwork, and update dependency locks when enabling them.

## Troubleshooting

| Problem                                  | Next step                                                                                           |
| ---------------------------------------- | --------------------------------------------------------------------------------------------------- |
| Packaging fails before writing a ZIP     | Check the language dependencies above and run from the stated directory.                            |
| The action is missing                    | Review the package installation, enable the plugin, and reopen the action picker.                   |
| The count does not change                | Save the binding and test a full press **and release**. Each binding has separate settings.         |
| A native build does not launch elsewhere | Rebuild for that operating system and architecture. A successful build is not native qualification. |

## Check behavior

From this starter directory, validate the maintained sources and any existing example packages:

```sh
python3 ../tools/validate.py
```

This static check does not launch the plugin. For executable acceptance, first build the plugin ZIP above, then build Manager's `sdk_acceptance` host separately using the [acceptance setup](../README.md#validate-behavior). From this starter directory, run:

```sh
python3 ../tools/acceptance.py rust --host /absolute/path/to/sdk_acceptance
```

Acceptance covers registration, counting, settings, per-binding isolation, and shutdown using disposable host data. It does not establish physical hardware, VSD Craft, or Elgato reference-host compatibility.

## License

This starter uses the [MIT license](../../LICENSE). Keep the license and [third-party notices](../THIRD_PARTY_NOTICES.md) with distributed packages.

[All starters](../README.md) · [SDK overview](../../README.md)
