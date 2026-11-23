# Rust Counter starter

Build a ZIP, install it in Manager, and test a Counter that saves a separate count for each binding.

| Item      | Value                                                |
| --------- | ---------------------------------------------------- |
| Version   | `0.1.1`                                              |
| Plugin ID | `app.streamdockmanager.plugins.examples.rust`        |
| Tools     | Rust 1.89+, Cargo, a native linker, and Python 3.11+ |
| Output    | `examples/artifacts/`                                |

[Build](#1-build-the-plugin) · [Install](#2-install-and-try-it) ·
[Customize](#3-make-it-yours) · [Verify](#check-behavior)

## 1. Build the plugin

[Clone the SDK](../../README.md#get-the-source) and install
[the build tools for this starter](../../README.md#install-build-tools).

Save a local Cargo config outside the project. Set its path to this SDK checkout's `Rust/` folder:

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
The checkout builds locally without publishing to crates.io. Keep development paths out of
`Cargo.toml` and keep the committed `Cargo.lock`. Users do not need Rust.

The packager prints the ZIP path, including its target OS and CPU. Manager does not compile
plugins at launch. For other hosts, check the [compatibility table](../../README.md#host-compatibility-at-a-glance).

## 2. Install and try it

1. Open **Plugins → Import plugin** in Manager and review the ZIP.
2. Enable the plugin, assign **Counter** to a button, and **Save binding**.
3. Press and release to go from **Count 0** to **Count 1**. Edit the prefix or reset in plugin settings.

Use **Icon, label & live display → Plugin display** to change the look. Apply appearance,
then save the binding.

## 3. Make it yours

| Change                | File                                    |
| --------------------- | --------------------------------------- |
| Counter logic         | `main.rs`                               |
| Plugin ID and actions | `manifest.json`                         |
| Settings UI           | `inspector.html`, `inspector.js`        |
| Display layout        | Shared `../assets/display/counter.json` |

[Generate a new project](../README.md#create-your-own-project) to get a new ID and separate source.

## Add optional features

See [the Rust helpers](../../Rust/README.md#reusable-helpers) for state, discovery, networking,
workers and thumbnail decoding. Add optional dependencies only for features you use.

## Troubleshooting

| Problem                               | Next step                                                                  |
| ------------------------------------- | -------------------------------------------------------------------------- |
| No ZIP produced                       | Check language dependencies and run the command from the stated directory. |
| Counter is missing or does not change | Enable the plugin, save the binding, and test a full press and release.    |
| The package does not run elsewhere    | Build and verify it on the target OS and architecture.                     |

## Check behavior

From the starter directory:

```sh
python3 ../tools/validate.py
python3 ../tools/acceptance.py rust --host /absolute/path/to/sdk_acceptance
```

The first command only validates files. For the second, build the ZIP and Manager’s
[disposable acceptance host](../README.md#validate-behavior) first.
It checks registration, settings, independent counts, and shutdown. It does not test
physical hardware or qualify vendor hosts.

## License

[MIT](../../LICENSE). Keep the license and [third-party notices](../THIRD_PARTY_NOTICES.md)
when redistributing.

[All starters](../README.md) · [SDK overview](../../README.md)
