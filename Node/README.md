# Node.js plugin SDK

Write Stream Dock Manager plugins in JavaScript or TypeScript. The SDK provides ESM modules and TypeScript declarations for **Node.js LTS**.

[SDK overview](../README.md) · [Starter plugins](../examples/README.md) · [License](../LICENSE)

## Requirements

| Item                 | What you need                                                           |
| -------------------- | ----------------------------------------------------------------------- |
| Developer runtime    | Node.js 26 and npm for the repository's CI-matched authoring toolchain  |
| Package engine range | `>=22 <27`; use a currently supported LTS release for installed plugins |
| Packaging tools      | Python 3.11+                                                            |
| Package              | `stream-dock-manager-plugin-sdk`, version `0.1.0`                       |
| Plugin runtime       | A supported Node.js LTS selected or installed in Manager                |

Install [Node.js](https://nodejs.org/en/download) and [Python](https://www.python.org/downloads/), then check `node --version`, `npm --version`, and `python3 --version`. See [shared setup notes](../README.md#install-build-tools) for Windows command names.

For a complete first plugin, follow the [Node.js Counter build guide](../examples/sdk-example-nodejs/README.md). The steps below explain using the SDK in a project of your own.

## Build and install a local SDK package

Build the SDK from the public source repository. Use Node.js 26 for this repository's authoring toolchain:

```sh
git clone https://github.com/StreamDockManager/Plugin-SDK.git
cd Plugin-SDK/Node
```

If you already have the checkout, open its `Node/` directory instead. Then run:

```sh
npm ci
npm run build
npm pack
```

This creates `Node/stream-dock-manager-plugin-sdk-0.1.0.tgz`. In your plugin project, install that local package:

```sh
npm install --save-exact /absolute/path/to/stream-dock-manager-plugin-sdk-0.1.0.tgz
```

The Node.js SDK is not deployed to an npm registry. `npm pack` only creates a local tarball for installation, testing, or file-based distribution; it does not publish anything. Dependency installation still uses npm for third-party packages. For a complete plugin project, use the [generator](../README.md#create-a-plugin) or [Node.js Counter](../examples/sdk-example-nodejs/README.md).

## Start a plugin

```js
import { run } from 'stream-dock-manager-plugin-sdk';
import { Plugin } from 'stream-dock-manager-plugin-sdk/authoring';

const plugin = new Plugin();
plugin.register('com.example.plugin.counter', {
    render: () => ({ title: 'Count 0' }),
});
await run(plugin);
```

This minimal action displays a title; use the Counter starter for input and settings behavior. The registered action ID must match your manifest. Manager supplies launch arguments when it starts the installed plugin, so running this file alone is not the installation workflow.

## Package and run

Inside a generated project, provision its authoring dependencies, then run:

```sh
python3 tools/package.py --project .
```

Declare dynamic assets and workers in `plugin-project.json`. Packaging bundles each entry and worker, includes required assets and license notices, and writes a plugin ZIP. Installed plugins launch without `node_modules`, downloads, or compilation.

Install the ZIP in Manager, select a supported Node.js LTS release in **Settings → Runtimes**, and enable the plugin. Manager can install Node privately or use an explicitly selected existing installation.

## Choose the modules you need

| Import                      | Use it for                                        |
| --------------------------- | ------------------------------------------------- |
| Package root                | `run` and `Client`                                |
| `/authoring`                | Plugin/action helpers                             |
| `/settings`, `/hoststorage` | Portable settings and negotiated host storage     |
| `/storage`                  | Explicit local file storage                       |
| `/workers`, `/process`      | Background workers and subprocess helpers         |
| `/httpclient`, `/network`   | Service requests and optional network diagnostics |
| `/inspectors`, `/testkit`   | Settings UI and tests                             |

Importing the SDK does not launch a plugin or start background work. Optional modules can be tree shaken out of the final plugin; the package declares its browser inspector assets as side effects so bundlers retain them when needed. The entire `Node/` directory is the SDK; there is no additional runtime wrapper.

## Reusable helpers

Import `/managedstate`, `/requests`, `/connectedws`, or `/thumbnail`. The last two have optional peer dependencies; install the ones you use in your plugin project:

```sh
# Only for /connectedws:
npm install --save-exact ws@8.22.2
# TypeScript projects using service WebSockets:
npm install --save-dev --save-exact @types/ws@8.18.2
# Only for /thumbnail:
npm install --save-exact jpeg-js@0.4.4 pngjs@7.0.0 omggif@1.0.10
```

```js
import { decode } from 'stream-dock-manager-plugin-sdk/thumbnail';
const image = decode(encodedBytes, 72, 72);
// image.width, image.height, image.data (RGBA bytes)
```

Run synchronous decoding inside a declared `/workers` worker so it cannot stall plugin events. Packaging bundles the worker, codec dependencies, and their notices. `Plugin.observe` is for asynchronous display-only follow-up work; it does not move synchronous CPU work off the event loop.

See the [shared behavior and limits](../README.md#reusable-plugin-helpers) for persistence, discovery, cancellation, and rendering semantics.

## Caches, discovery, and operation history

Import `Cache`, `Budget`, and `Persistence` from `/cache`, `Runner` from `/discovery`, and `Ledger` from `/operations`. Private cache persistence requires your explicitly selected `HostStore`. Schedule discovery tickets using bounded background work; use `ticket.signal`, complete results on the owner loop, and expire requests at `nextDeadline()`. Clear the context on close. Reserve ledger capacity before sending commands and update only from observed outcomes.

The optional thumbnail module also provides `fit`, `fitDecode`, `png`, `dataURI`, and `render`:

```js
import { render } from 'stream-dock-manager-plugin-sdk/thumbnail';
const result = render(encodedBytes, 256, 256, () => signal.aborted, {
    maxInputBytes: 128 * 1024 * 1024,
    maxSourcePixels: 64_000_000,
});
// result.dataURI, result.width, result.height
```

Omit the limits object or either field for the 64 MiB / 32 MP defaults. Run CPU-heavy decoding in a declared SDK worker; synchronous codec work cannot process event-loop abort events mid-call. On cancellation, the SDK worker pool discards the result immediately but lets an in-flight SDK PNG call finish before terminating the worker. This avoids a native zlib crash observed on Windows with Node 26. This protection applies only to PNG work started through the SDK thumbnail API.

## Check SDK changes

Run from `Node/` after `npm ci`:

```sh
npm run check
npm test
```

| Command         | Result                                            |
| --------------- | ------------------------------------------------- |
| `npm run check` | Type-check without emitting files                 |
| `npm test`      | Rebuild and run the Node SDK tests                |
| `npm run build` | Emit the SDK and packaged web assets into `dist/` |
| `npm pack`      | Create a local npm tarball from the built package |

`STREAM_DOCK_MANAGER_SDK_JS_ROOT` is an explicit local-source override for authoring tools. Keep development paths out of published dependency manifests. See the [SDK overview](../README.md) for shared behavior and parity requirements.

## Troubleshooting

| Problem                       | Next step                                                                                              |
| ----------------------------- | ------------------------------------------------------------------------------------------------------ |
| The plugin asks for a runtime | Choose a supported Node.js LTS in Manager. Authoring dependencies are not an installed plugin runtime. |
| An asset or worker is missing | Declare it in `plugin-project.json` and rebuild the ZIP.                                               |
| Image processing blocks input | Move synchronous decoding into a declared SDK worker.                                                  |

## Host-owned shutdown

An established, idle plugin session ends normally when its original parent process
has demonstrably exited or reparented, even if that host drops TCP without a
WebSocket close handshake. This accommodates OpenDeck process exit; it does not
claim the host exited successfully rather than crashed. Detached/PID-1 launches,
unestablished connections, owner-alive errors, queued output, buffered bytes,
active callbacks, and pending settings/RPC operations retain their failure
behavior. Pending writes are never reported as committed by this rule, and
handler cleanup failures remain errors.

## License

The SDK is [MIT-licensed](../LICENSE). Optional codecs and service clients retain their own licenses; keep the notices emitted by the packager.
