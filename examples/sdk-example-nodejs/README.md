# Node.js Counter starter

Build your first plugin with a ready-to-use Counter. It displays a count, increments once on button release, and saves separate settings for each button binding. Build the ZIP first, try it in Manager, then customize the source.

| Item               | Value                                                                     |
| ------------------ | ------------------------------------------------------------------------- |
| Version            | `0.1.0`                                                                   |
| Plugin ID          | `app.streamdockmanager.plugins.examples.nodejs`                           |
| Build requirements | Node.js (26 for this repository's authoring tools), npm, and Python 3.11+ |
| Package output     | `../artifacts/`                                                           |

**Before you start:** clone the [SDK repository](../../README.md#get-the-source), install this starter’s build tools, and have Stream Dock Manager available to install the finished ZIP.

[Build](#1-build-the-plugin) · [Install](#2-install-and-try-it) · [Customize](#3-make-it-yours) · [Verify](#check-behavior)

## 1. Build the plugin

Install [Node.js and npm](https://nodejs.org/en/download) plus [Python 3.11+](https://www.python.org/downloads/). Use Node.js 26 for the CI-matched authoring toolchain. Check `node --version`, `npm --version`, and `python3 --version`.

Run from this `sdk-example-nodejs/` directory in a POSIX shell:

```sh
cd ../../Node
npm ci
npm run build
export STREAM_DOCK_MANAGER_SDK_JS_ROOT="$(pwd)"
cd ../examples/sdk-example-nodejs
python3 ../tools/package.py nodejs
```

In PowerShell, use `$env:STREAM_DOCK_MANAGER_SDK_JS_ROOT = (Get-Location).Path` in place of the `export` line.

The explicit source override lets this starter's shared tools use the built local SDK and its installed bundler dependencies. It does not rewrite the starter's package manifest or lockfile. Keep it set for packaging and acceptance in this shell. For a generated standalone project, follow the [local npm package workflow](../../Node/README.md#build-and-install-a-local-sdk-package) instead.

The packager bundles the plugin; the installed ZIP does not need `node_modules`. Users select a supported Node.js LTS version in Manager.

The packager prints the finished ZIP path under `examples/artifacts/`. Its filename includes the language, operating system, and architecture. Build tools are used only during authoring; Manager never compiles the plugin at launch.

## 2. Install and try it

1. Open **Plugins → Import plugin** in Manager and select the ZIP.
2. Review and install the package, then choose a supported Node.js LTS runtime.
3. Enable the plugin, assign **Counter** to a button, and save the binding.
4. Press and release the button. It should change from **Count 0** to **Count 1**.
5. Open plugin settings to edit the prefix or reset the count.

Each binding owns its own count. Appearance is edited separately through **Icon, label & live display → Plugin display**. Apply the appearance draft, then save the binding.

## 3. Make it yours

| Edit                        | File or location                                   |
| --------------------------- | -------------------------------------------------- |
| Counter behavior            | `counter.mjs` (with `main.mjs` as the entry point) |
| Plugin identity and actions | `manifest.json`                                    |
| Settings UI                 | `inspector.html` and `inspector.js`                |
| Display elements            | Shared `../assets/display/counter.json`            |

For a new identity and destination, use the [project generator](../README.md#create-your-own-project) instead of copying a built ZIP.

## Add optional features

The SDK also provides managed state, discovery workflows, service WebSockets, observation jobs, shared reads, compact displays, and thumbnail decoding. Follow the [Node SDK guide](../../Node/README.md#reusable-helpers) to enable dependencies and use these helpers. Keep optional codecs out of the Counter unless an action needs artwork, and update dependency locks when enabling them.

## Troubleshooting

| Problem                              | Next step                                                                                   |
| ------------------------------------ | ------------------------------------------------------------------------------------------- |
| Packaging fails before writing a ZIP | Check the language dependencies above and run from the stated directory.                    |
| The action is missing                | Review the package installation, enable the plugin, and reopen the action picker.           |
| The count does not change            | Save the binding and test a full press **and release**. Each binding has separate settings. |
| The SDK or bundler cannot be found   | Build `Node/` first and set `STREAM_DOCK_MANAGER_SDK_JS_ROOT` to that absolute directory.   |

## Check behavior

From this starter directory, validate the maintained sources and any existing example packages:

```sh
python3 ../tools/validate.py
```

This static check does not launch the plugin. For executable acceptance, first build the plugin ZIP above, then build Manager's `sdk_acceptance` host separately using the [acceptance setup](../README.md#validate-behavior). From this starter directory, run:

```sh
python3 ../tools/acceptance.py nodejs --host /absolute/path/to/sdk_acceptance
```

Set `STREAM_DOCK_MANAGER_NODE` to a compatible Node executable for the test process. Acceptance covers registration, counting, settings, per-binding isolation, and shutdown using disposable host data. It does not establish physical hardware, VSD Craft, or Elgato reference-host compatibility.

## License

This starter uses the [MIT license](../../LICENSE). Keep the license and [third-party notices](../THIRD_PARTY_NOTICES.md) with distributed packages.

[All starters](../README.md) · [SDK overview](../../README.md)
