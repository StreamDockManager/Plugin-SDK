# Go Counter starter

Build a ZIP, install it in Manager, and test a Counter that saves a separate count for each binding.

| Item      | Value                                       |
| --------- | ------------------------------------------- |
| Version   | `0.1.1`                                     |
| Plugin ID | `app.streamdockmanager.plugins.examples.go` |
| Tools     | Go 1.27.4+ and Python 3.11+                 |
| Output    | `examples/artifacts/`                       |

[Build](#1-build-the-plugin) · [Install](#2-install-and-try-it) ·
[Customize](#3-make-it-yours) · [Verify](#check-behavior)

## 1. Build the plugin

[Clone the SDK](../../README.md#get-the-source) and install
[the build tools for this starter](../../README.md#install-build-tools).

From `examples/sdk-example-go/`:

```sh
go mod download
go mod verify
python3 ../tools/package.py go
```

The starter pins `Go/v0.1.0` in `go.mod` and `go.sum`; the starter itself is 0.1.1.
Use the [local workspace setup](../../Go/README.md#work-on-the-sdk) to test SDK source
changes without replacing the published dependency pin. Users do not need Go.

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
| Counter logic         | `main.go`                               |
| Plugin ID and actions | `manifest.json`                         |
| Settings UI           | `inspector.html`, `inspector.js`        |
| Display layout        | Shared `../assets/display/counter.json` |

[Generate a new project](../README.md#create-your-own-project) to get a new ID and separate source.

## Add optional features

See [the Go helpers](../../Go/README.md#reusable-helpers) for state, discovery, networking,
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
python3 ../tools/acceptance.py go --host /absolute/path/to/sdk_acceptance
```

The first command only validates files. For the second, build the ZIP and Manager’s
[disposable acceptance host](../README.md#validate-behavior) first.
It checks registration, settings, independent counts, and shutdown. It does not test
physical hardware or qualify vendor hosts.

## License

[MIT](../../LICENSE). Keep the license and [third-party notices](../THIRD_PARTY_NOTICES.md)
when redistributing.

[All starters](../README.md) · [SDK overview](../../README.md)
