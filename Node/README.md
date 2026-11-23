# Node.js plugin SDK

Write a JavaScript or TypeScript ESM plugin. Package it once and select a supported
Node.js LTS runtime in Manager.

[Build Counter](../examples/sdk-example-nodejs/README.md#1-build-the-plugin) ·
[Install the SDK](#build-and-install-a-local-sdk-package) · [Run a plugin](#package-and-run) ·
[SDK checks](#check-sdk-changes)

> **VSD Craft:** Its bundled Node.js does not meet this SDK's runtime requirements.
> A working inspector does not mean the backend registered. See
> [host compatibility](../README.md#host-compatibility-at-a-glance).

## Requirements

| Item        | Value                                                                                                   |
| ----------- | ------------------------------------------------------------------------------------------------------- |
| Authoring   | [Node.js 26 and npm](https://nodejs.org/en/download), [Python 3.11+](https://www.python.org/downloads/) |
| SDK package | `stream-dock-manager-plugin-sdk` 0.1.1, ESM with TypeScript declarations                                |
| Runtime     | Package engines `>=22 <27`; Manager requires a supported Node.js LTS                                    |

## Build and install a local SDK package

[Clone the SDK](../README.md#get-the-source). From the SDK root:

```sh
cd Node
npm ci
npm run build
npm pack
```

From your own plugin directory, install the printed tarball:

```sh
npm install --save-exact /absolute/path/to/Plugin-SDK/Node/stream-dock-manager-plugin-sdk-0.1.1.tgz
```

The SDK is not deployed to an npm registry. `npm pack` creates a local tarball; it does
not publish. First-time third-party dependency installation can use the npm registry.
To run the maintained sample instead, follow the [Counter guide](../examples/sdk-example-nodejs/README.md).
For your own plugin ID, use the [project generator](../README.md#create-a-plugin).

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
| `/vault-values`             | Permission-scoped Vault values and display text   |

Importing the SDK does not launch a plugin or start background work. Optional modules
can be omitted from the final bundle. For example, import `formatText` only if you use
[Vault display text](../REFERENCE.md#format-vault-references-in-display-text). The package
marks browser inspector assets as side effects so bundlers retain them when needed.
There is no separate Node.js runtime wrapper.

## Reusable helpers

Import `/managedstate`, `/requests`, `/connectedws`, or `/thumbnail`. The last two have optional peer dependencies; install the ones you use in your plugin project:

```sh
# Only for /connectedws:
npm install --save-exact ws@8.23.0
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

See the [shared behavior and limits](../REFERENCE.md#reusable-plugin-helpers) for persistence, discovery, cancellation, and rendering semantics.

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

## Worker input ownership

`new WorkerPool({ maxBufferBytes, maxQueuedBufferBytes })` optionally limits the backing bytes of ordinary ArrayBuffers and their views in a job and across waiting jobs. Multiple views of the same buffer count once. The default pool accepts up to 32 waiting jobs and runs up to four workers concurrently. Submitting a job normally snapshots its input. To hand ownership to the pool, pass the buffers as the fourth argument to `submit(url, input, signal, transfer)`. A successful transfer detaches those buffers from the caller. A budget rejection leaves them attached.

## License

The SDK is [MIT-licensed](../LICENSE). Optional codecs and service clients retain their own licenses; keep the notices emitted by the packager.
