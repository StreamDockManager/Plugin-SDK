# Python Counter starter

Build your first plugin with a ready-to-use Counter. It displays a count, increments once on button release, and saves separate settings for each button binding. Build the ZIP first, try it in Manager, then customize the source.

| Item               | Value                                                                        |
| ------------------ | ---------------------------------------------------------------------------- |
| Version            | `0.1.0`                                                                      |
| Plugin ID          | `app.streamdockmanager.plugins.examples.python`                              |
| Build requirements | Python 3.11+, a virtual environment, and the pinned PyInstaller dependencies |
| Package output     | `../artifacts/`                                                              |

**Before you start:** clone the [SDK repository](../../README.md#get-the-source), install this starter’s build tools, and have Stream Dock Manager available to install the finished ZIP.

[Build](#1-build-the-plugin) · [Install](#2-install-and-try-it) · [Customize](#3-make-it-yours) · [Verify](#check-behavior)

## 1. Build the plugin

Install [Python 3.11+](https://www.python.org/downloads/) and confirm `python3 --version`. Run from this `sdk-example-python/` directory in a POSIX shell:

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -e ../../Python
python -m pip install -r requirements.txt
python ../tools/package.py python
```

The editable install selects this checkout's SDK before the pinned requirements are installed. Use the same activated environment for packaging and acceptance.

| Shell                  | Activate the environment     |
| ---------------------- | ---------------------------- |
| macOS/Linux            | `. .venv/bin/activate`       |
| Windows PowerShell     | `.venv\Scripts\Activate.ps1` |
| Windows Command Prompt | `.venv\Scripts\activate.bat` |

On Windows, use `python` or `py -3` to create the environment if `python3` is unavailable. Build on the target OS and architecture; the ZIP includes Python, so users need no separate interpreter.

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
| Counter behavior            | `main.py`                               |
| Plugin identity and actions | `manifest.json`                         |
| Settings UI                 | `inspector.html` and `inspector.js`     |
| Display elements            | Shared `../assets/display/counter.json` |

For a new identity and destination, use the [project generator](../README.md#create-your-own-project) instead of copying a built ZIP.

## Add optional features

The SDK also provides managed state, discovery workflows, service WebSockets, observation jobs, shared reads, compact displays, and thumbnail decoding. Follow the [Python SDK guide](../../Python/README.md#reusable-helpers) to enable dependencies and use these helpers. Keep optional codecs out of the Counter unless an action needs artwork, and update dependency locks when enabling them.

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
python3 ../tools/acceptance.py python --host /absolute/path/to/sdk_acceptance
```

Acceptance covers registration, counting, settings, per-binding isolation, and shutdown using disposable host data. It does not establish physical hardware, VSD Craft, or Elgato reference-host compatibility.

Keep the PyInstaller folder bundle intact, including `bin/_internal/`. Preserve `counter.spec` and its startup hooks; moving to one-file extraction can exceed Manager's registration deadline.

## License

This starter uses the [MIT license](../../LICENSE). Keep the license and [third-party notices](../THIRD_PARTY_NOTICES.md) with distributed packages.

[All starters](../README.md) · [SDK overview](../../README.md)
