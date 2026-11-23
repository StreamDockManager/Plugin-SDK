# Python plugin SDK

Write a Python plugin and package its interpreter with it. Users install a ZIP, not Python.

[Build Counter](#make-your-first-plugin) · [Install the SDK](#install-for-local-development) ·
[Package a plugin](#package-for-users) · [SDK checks](#test-the-sdk)

## Requirements

| Item        | Value                                                        |
| ----------- | ------------------------------------------------------------ |
| Build tools | [Python 3.11+](https://www.python.org/downloads/), pip, venv |
| SDK package | `stream-dock-manager-plugin-sdk` 0.1.1                       |
| Import      | `stream_dock_manager_sdk`                                    |
| Packaging   | Pinned PyInstaller requirements on the target OS and CPU     |

## Install for local development

[Clone the SDK](../README.md#get-the-source). From its root:

```sh
python3 -m venv .venv
. .venv/bin/activate
python -m pip install -e Python/
```

On Windows, use `python` or `py -3` if needed and activate with
`.venv\Scripts\Activate.ps1` in PowerShell or `.venv\Scripts\activate.bat` in Command Prompt.
This installs the local SDK, not a PyPI release.

## Make your first plugin

From the SDK root with that environment active:

```sh
cd examples/sdk-example-python
python -m pip install -r requirements.txt
python ../tools/package.py python
```

Import the printed ZIP in Manager, enable it, and save a Counter binding. Press and release
to test it. See the [Counter guide](../examples/sdk-example-python/README.md) or
[generate your own plugin ID](../README.md#create-a-plugin).

| API                              | Use it to                                            |
| -------------------------------- | ---------------------------------------------------- |
| `Handler.handle`, `run(handler)` | Process lifecycle and action events                  |
| `defer_action()`                 | Complete an action after cooperative background work |
| `hoststorage.PortableSettings`   | Read and update portable settings                    |
| `hoststorage.HostStore`          | Use explicitly negotiated private host storage       |

Apply background results on the owned event loop. Host storage does not open a
plugin SQLite file or silently select local JSON.

## Package for users

Run `python tools/package.py --project .` from a generated project's root.
For the maintained Counter, use the [command above](#make-your-first-plugin).
The packager prints the ZIP path.

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

See the [shared behavior and limits](../REFERENCE.md#reusable-plugin-helpers) for persistence, discovery, cancellation, and rendering semantics.

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
