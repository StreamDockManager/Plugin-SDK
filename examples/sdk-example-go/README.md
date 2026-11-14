# Go Counter starter

Build your first plugin with a ready-to-use Counter. It displays a count, increments once on button release, and saves separate settings for each button binding. Build the ZIP first, try it in Manager, then customize the source.

| Item               | Value                                       |
| ------------------ | ------------------------------------------- |
| Version            | `0.1.0`                                     |
| Plugin ID          | `app.streamdockmanager.plugins.examples.go` |
| Build requirements | Go 1.27.4+ and Python 3.11+                 |
| Package output     | `../artifacts/`                             |

**Before you start:** clone the [SDK repository](../../README.md#get-the-source), install this starter’s build tools, and have Stream Dock Manager available to install the finished ZIP.

[Build](#1-build-the-plugin) · [Install](#2-install-and-try-it) · [Customize](#3-make-it-yours) · [Verify](#check-behavior)

## 1. Build the plugin

Install [Go 1.27.4+](https://go.dev/doc/install) and [Python 3.11+](https://www.python.org/downloads/), then check `go version` and `python3 --version`. Run from this `sdk-example-go/` directory:

```sh
go mod download
python3 ../tools/package.py go
```

The SDK version is pinned in `go.mod`. The first download after the release tag exists records its checksums in `go.sum`; review and retain those Go-generated entries. If the SDK repository is private, authenticate Git access and include `github.com/StreamDockManager/Plugin-SDK` in `GOPRIVATE` (and any explicitly set `GONOPROXY`/`GONOSUMDB` patterns) before downloading modules. End users do not need Go.

Before the first SDK tag is available, build this maintained starter against the adjacent SDK
source with a temporary workspace. From the SDK repository root in a POSIX shell:

```sh
sdk_root="$PWD"
check=$(mktemp -d)
(
    cd "$check" || exit
    GOWORK=off go work init "$sdk_root/Go" "$sdk_root/examples/sdk-example-go"
    export GOWORK="$check/go.work"
    go work edit "-replace=github.com/StreamDockManager/Plugin-SDK/Go@v0.1.0=$sdk_root/Go"
    cd "$sdk_root/examples/sdk-example-go" || exit
    go build -o "$check/counter" .
)
```

This verifies the checkout without writing a local replacement or unpublished checksums into
the starter's tracked module files. After release, use the normal versioned build above.

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
| Counter behavior            | `main.go`                               |
| Plugin identity and actions | `manifest.json`                         |
| Settings UI                 | `inspector.html` and `inspector.js`     |
| Display elements            | Shared `../assets/display/counter.json` |

For a new identity and destination, use the [project generator](../README.md#create-your-own-project) instead of copying a built ZIP.

For local SDK changes, use the [temporary Go workspace workflow](../../Go/README.md#work-on-the-sdk). Keep the pinned dependency and checksums in published source.

## Add optional features

The SDK also provides managed state, discovery workflows, service WebSockets, observation jobs, shared reads, compact displays, and thumbnail decoding. Follow the [Go SDK guide](../../Go/README.md#reusable-helpers) to enable dependencies and use these helpers. Keep optional codecs out of the Counter unless an action needs artwork, and update dependency locks when enabling them.

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
python3 ../tools/acceptance.py go --host /absolute/path/to/sdk_acceptance
```

Acceptance covers registration, counting, settings, per-binding isolation, and shutdown using disposable host data. It does not establish physical hardware, VSD Craft, or Elgato reference-host compatibility.

## License

This starter uses the [MIT license](../../LICENSE). Keep the license and [third-party notices](../THIRD_PARTY_NOTICES.md) with distributed packages.

[All starters](../README.md) · [SDK overview](../../README.md)
