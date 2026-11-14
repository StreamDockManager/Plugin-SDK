# Python plugin SDK

Write a Stream Dock Manager plugin in Python, then package it with its own interpreter. Users do not need a separate Python installation.

[SDK overview](../README.md) · [Starter plugins](../examples/README.md) · [License](../LICENSE)

## Requirements

| Item             | What you need                                                                    |
| ---------------- | -------------------------------------------------------------------------------- |
| Development      | Python 3.11+, pip, and a virtual environment                                     |
| SDK package      | `stream-dock-manager-plugin-sdk`, version `0.1.0`                                |
| Python import    | `stream_dock_manager_sdk`                                                        |
| Plugin packaging | The starter's pinned PyInstaller requirements, on the target OS and architecture |
| End-user runtime | Included in the packaged plugin; no separate Python installation                 |

Install [Python](https://www.python.org/downloads/) and check `python3 --version`. On Windows, use `python` or `py -3` if `python3` is unavailable.

## Install for local development

Use Python 3.11+ within the range declared in [pyproject.toml](pyproject.toml). From this `Python/` directory:

```sh
python3 -m venv .venv
```

| Shell                  | Activate the environment     |
| ---------------------- | ---------------------------- |
| macOS/Linux            | `. .venv/bin/activate`       |
| Windows PowerShell     | `.venv\Scripts\Activate.ps1` |
| Windows Command Prompt | `.venv\Scripts\activate.bat` |

Then install the local SDK:

```sh
python -m pip install -e .
```

The package is `stream-dock-manager-plugin-sdk`; import it as `stream_dock_manager_sdk`. Registry publication of the initial release is separate from this local setup.

## Make your first plugin

Follow the [Python Counter](../examples/sdk-example-python/README.md) to build a working ZIP, or use the [generator](../README.md#create-a-plugin) for your own identity.

| API                            | Purpose                                   |
| ------------------------------ | ----------------------------------------- |
| `Handler.handle`               | Receive lifecycle and action events       |
| `run(handler)`                 | Run the plugin's event loop               |
| `defer_action()`               | Complete an action after background work  |
| `hoststorage.PortableSettings` | Read and update portable settings         |
| `hoststorage.HostStore`        | Use the optional negotiated private store |

Background work is cooperative and applies results through the owned event loop. Host storage does not open a plugin SQLite file or silently select local JSON.

## Package for users

For the maintained Counter, follow its [environment and dependency setup](../examples/sdk-example-python/README.md#1-build-the-plugin), then run from `examples/sdk-example-python/` with that environment active:

```sh
python ../tools/package.py python
```

For a generated project, run `python tools/package.py --project .` from the project root instead. The packager prints the completed ZIP path.

The maintained starter uses PyInstaller's folder bundle. Build on the target operating system and architecture, and keep the executable and its `_internal/` folder together. Install the ZIP through Manager's package review, then enable the plugin and save its action binding.

Preserve the starter's startup hooks and pinned build dependencies; they keep imports within the host's registration deadline.

## Reusable helpers

Import `managedstate`, `requests`, or `connectedws` from `stream_dock_manager_sdk`. Install the optional image codec when using `thumbnail.decode`:

```sh
python -m pip install '/absolute/path/to/SDK/Python[thumbnail]'
```

```python
from stream_dock_manager_sdk.thumbnail import decode
image = decode(encoded_bytes, 72, 72, cancel=cancel_event)
# image.width, image.height, image.data (RGBA bytes)
```

`Plugin.observe` schedules display-only follow-up work. Include Pillow in your plugin's authoring dependencies before freezing the application; installed plugins need no separate Python or codec installation.

See the [shared behavior and limits](../README.md#reusable-plugin-helpers) for persistence, discovery, cancellation, and rendering semantics.

## Caches, discovery, and operation history

Import `Cache`, `Budget`, and `Persistence` from `stream_dock_manager_sdk.cache`, `Runner`/`Request` from `.discovery`, and `Ledger` from `.operations`. Private persistence requires an explicit `HostStore`. Schedule discovery through bounded background work using `ticket.cancel`; complete and expire requests on the owner loop, and clear their context on close. Reserve ledger capacity before sending commands.

The thumbnail extra provides aspect fit, PNG encoding, and data URIs:

```python
from stream_dock_manager_sdk.thumbnail import Limits, render
result = render(encoded_bytes, 256, 256, cancel, limits=Limits(
    max_input_bytes=128 * 1024 * 1024,
    max_source_pixels=64_000_000,
))
# result.data_uri, result.width, result.height
```

Omit `limits` or either constructor field for the 64 MiB / 32 MP defaults. `fit`, `fit_decode`, `png`, and `data_uri` are available separately. Run decoding in background work; Pillow retains its own image safeguards.

## Test the SDK

With the environment active, run from `Python/`:

```sh
python -m pip install -e '.[thumbnail]'
python -m unittest discover -s tests
```

The thumbnail extra installs the codec used by image tests. The base SDK does not require it unless your plugin uses those helpers.

See the [SDK overview](../README.md) for shared contracts, other languages, and coordinated development.

## Troubleshooting

| Problem                            | Next step                                                                                |
| ---------------------------------- | ---------------------------------------------------------------------------------------- |
| A packaged plugin is missing files | Keep the PyInstaller folder bundle intact, including `_internal/`.                       |
| Imports fail during authoring      | Activate the intended virtual environment and install the local SDK there.               |
| A package fails on another OS      | Rebuild with the target OS and architecture; a frozen interpreter is not cross-platform. |

## License

The SDK is [MIT-licensed](../LICENSE). Packaging preserves the separately licensed runtime, dependencies, and startup-hook notices.
