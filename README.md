# Stream Dock Manager Plugin SDK

Build a button, dial, or display plugin in **Go, Rust, Python, or Node.js**. Start by
building and installing Counter. It increments once per release and tracks each binding separately.

**Development preview · SDK checkout and starters 0.1.1 · Protocol v1 · MIT**

[Quick start](#get-the-source) · [Install the ZIP](#install-and-try-it) ·
[Generate a project](#create-a-plugin) · [Host compatibility](#host-compatibility-at-a-glance)

## Choose a language

| SDK                        | First plugin                                             | What the installed plugin needs           |
| -------------------------- | -------------------------------------------------------- | ----------------------------------------- |
| [Go](Go/README.md)         | [Go Counter](examples/sdk-example-go/README.md)          | Native executable in the ZIP              |
| [Rust](Rust/README.md)     | [Rust Counter](examples/sdk-example-rust/README.md)      | Native executable in the ZIP              |
| [Python](Python/README.md) | [Python Counter](examples/sdk-example-python/README.md)  | Interpreter included in the ZIP           |
| [Node.js](Node/README.md)  | [Node.js Counter](examples/sdk-example-nodejs/README.md) | Supported Node.js LTS selected in Manager |

## Install build tools

Install [Git](https://git-scm.com/downloads), [Python 3.11+](https://www.python.org/downloads/)
for the package tools, and **only** the toolchain for your language.

| Language | Developer toolchain                                                                        |
| -------- | ------------------------------------------------------------------------------------------ |
| Go       | [Go 1.27.4+](https://go.dev/doc/install)                                                   |
| Rust     | [Rust 1.89+ and Cargo](https://www.rust-lang.org/tools/install), plus your platform linker |
| Python   | A virtual environment and the starter's pinned PyInstaller requirements                    |
| Node.js  | [Node.js 26 and npm](https://nodejs.org/en/download) for the repository's authoring tools  |

On Windows, use `python` or `py -3` when `python3` is unavailable. For native builds,
install platform tools such as Xcode Command Line Tools or Visual Studio C++ Build Tools.
First-time dependency installation may need network access.

## Get the source

```sh
git clone https://github.com/StreamDockManager/Plugin-SDK.git
cd Plugin-SDK
```

Follow your language's Counter guide above. For a first build, Go Counter uses the
published SDK module and needs no local dependency overrides:

```sh
cd examples/sdk-example-go
go mod download
go mod verify
python3 ../tools/package.py go
```

Use the printed ZIP in `examples/artifacts/`. Only a packaged plugin is installable,
not the SDK itself. The Go starter pins `v0.1.0`; use the [local Go workspace](Go/README.md#work-on-the-sdk)
for newer APIs from this checkout.

## Install and try it

1. In Manager, open **Plugins → Import plugin**, choose the ZIP, and review the install.
2. If using Node.js, select or install a supported LTS runtime. Enable the plugin.
3. Assign **Counter** to a button and save the binding. Press and release to see
   **Count 0** become **Count 1**. Open plugin settings to change the prefix or reset the count.

Example packages are not added to the official catalog automatically.

## Create a plugin

From the SDK repository root, generate an offline starter with a fresh ID and a new folder:

```sh
python3 examples/tools/generate.py \
  --destination /absolute/existing-parent/my-plugin \
  --identity com.example.my-plugin \
  --language go \
  --name "My plugin"
```

Choose `go`, `rust`, `python`, or `nodejs`. Open the generated README for language
setup, then run the following from the new project:

```sh
python3 tools/validate.py --project .
python3 tools/package.py --project .
```

The packager prints the ZIP path under the project's `artifacts/`. Manager does not
download dependencies or compile plugins when it launches them.

## Host compatibility at a glance

| Host                | What to expect                                                                                                                                                                                                             |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Stream Dock Manager | Default package target. Optional host APIs require capability negotiation.                                                                                                                                                 |
| OpenDeck            | Export a separate OpenAction package. Native Go, Rust, and C++ use the [universal workflow](REFERENCE.md#creating-opendeck--openaction-packages); Node.js has a separate portable ZIP. Confirm support on the actual host. |
| VSD Craft           | Shared events are implemented, but native interoperability is not fully qualified. **Node.js SDK plugins are unsupported** because the bundled runtime does not meet SDK requirements.                                     |
| Elgato Stream Deck  | Shared protocol features are implemented; native-host compatibility is not verified.                                                                                                                                       |

An inspector opening does not prove backend registration. Changing Node.js in Manager
does not update VSD Craft. See the [qualification reference](REFERENCE.md#vendor-compatibility-qualification)
for evidence and limits; respect vendor licensing and protected services.

## Independence and compatibility

This independent project is not affiliated with, endorsed by, or sponsored by VSDInside,
MiraBox, OpenDeck, or Elgato Stream Deck. Product names identify compatibility targets.

## Where to go next

| Task                                      | Guide                                                               |
| ----------------------------------------- | ------------------------------------------------------------------- |
| Customize, package, or test Counter       | [Starter guide](examples/README.md)                                 |
| Add settings, storage, or display helpers | [Shared capabilities](REFERENCE.md#shared-capabilities)             |
| Change or run SDK checks                  | [SDK development](REFERENCE.md#develop-the-sdk)                     |
| Prepare downloadable SDK artifacts        | [Release procedure](REFERENCE.md#prepare-and-publish-sdk-downloads) |

## Manager-owned Vault setup slots

Use a versioned `StreamDockManager.SetupSlots` declaration and request only the exact
entry and scope a plugin needs. See the [Vault authoring rules](REFERENCE.md#manager-owned-vault-setup-slots)
for supported kinds, approval, and secret handling.

### Manager permissions and brokered requests

See the [permissions and broker reference](REFERENCE.md#manager-permissions-and-brokered-requests)
for action-scoped approvals, clipboard and webhook review, Vault references, and notifications.
Vendor protocol compatibility does not imply access to these Manager services.

## Troubleshooting

| Problem                                    | Next step                                                                                                    |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------ |
| A dependency cannot be found               | Follow the language guide's local SDK setup. The Node.js SDK is not published to npm.                        |
| The generator refuses the destination      | Choose a new folder under an existing parent. It will not overwrite.                                         |
| The action does nothing                    | Enable the plugin, save the binding, and test a full press **and release**. For Node.js, check its runtime.  |
| A native package does not launch elsewhere | Match the target OS and CPU, then test on that host.                                                         |
| An optional host API is unavailable        | Check the negotiated capability. Private host storage does not fall back to local files or broader settings. |

## Local cleanup

Preview with `CLEAN_DRY_RUN=1 make clean`. After stopping builds, watchers and development
apps, `make clean` removes these repository-local outputs:

| Scope                   | Selected paths                                                              |
| ----------------------- | --------------------------------------------------------------------------- |
| Root outputs and caches | `dist/`, `target/`, `artifacts/`, `.cache/go-build/`, `.cache/rust-target/` |
| SDK outputs             | `Node/dist/`, `Rust/target/`, `Python/build/`, `Python/dist/`               |
| Starter outputs         | `examples/sdk-example-rust/target/`, `examples/sdk-example-nodejs/dist/`    |

Cleanup refuses tracked files, nested repositories, and symlinks or junctions. It preserves
source, dependencies, global caches, signing credentials, installed app data, and
`.cache/signed-dev-data`. Pass custom `DATA_DIR` or `MANAGER_DATA_DIR` again to protect
overlapping output paths. Save needed artifacts first.

## License

SDKs, tools, and starters are [MIT-licensed](LICENSE). Retain copyright, license, and
[third-party notices](examples/THIRD_PARTY_NOTICES.md) when redistributing. Manager and
the official plugins are separately GPL-3.0-or-later.
