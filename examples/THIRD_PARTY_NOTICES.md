# SDK example notices

Project-owned starters, adapters, inspectors and packaging tools are MIT-licensed; the complete license is included as `LICENSE` in each example ZIP. Third-party components retain the licenses listed below.

Node.js examples require no third-party JavaScript packages. Runtime licenses/notices belong to the separately installed interpreter and are retained by Manager's runtime service.

Native packages collect their own dependency notices in `RUNTIME_LICENSES.txt`; no blanket `licenses/` folder is included. The following list describes dependencies used across the maintained examples. Build manifests and lockfiles record exact versions; see the upstream sources for complete notices and corresponding source.

- Python interpreter: Python Software Foundation License, https://docs.python.org/3/license.html. `wsproto` 1.3.2 and `h11` 0.16.0: MIT, https://github.com/python-hyper/wsproto and https://github.com/python-hyper/h11. PyInstaller 6.22.3: GPL-2.0-or-later with bootloader exception, https://pyinstaller.org/en/stable/license.html. The lazy inspect hook derives from PyInstaller's Apache-2.0 runtime hook, Copyright 2021-2023 PyInstaller Development Team. PyInstaller's exception permits distribution of the bundled program under its own license. The build venv is not shipped; Python and required modules are bundled beside the executable in `bin/_internal/`.
- Stream Dock Manager SDK 0.1.1 (Go, Rust, Python and Node.js): MIT, this repository. Previously published SDK revisions retain their original license; use the license included with the exact dependency revision you distribute.
- Rust `base64` 0.23.1: MIT/Apache-2.0, https://github.com/marshallpierce/rust-base64.
- Python native bundles may include OpenSSL through Python’s TLS modules; retain the packaged OpenSSL license alongside the interpreter notices.
- Go WebSocket transport: `coder/websocket` 1.8.15, ISC, https://github.com/coder/websocket/blob/v1.8.15/LICENSE.txt. Maintained Go starter locks may still select a historical SDK using `gorilla/websocket` 1.5.3 (BSD-2-Clause, https://github.com/gorilla/websocket/blob/v1.5.3/LICENSE); preserve the notices matching the packaged dependency. Go runtime: BSD-3-Clause, https://go.dev/LICENSE.
- Rust `tungstenite` 0.30.0: MIT/Apache-2.0, https://github.com/snapview/tungstenite-rs/tree/v0.30.0. `serde_json` and its locked dependencies: MIT/Apache-2.0, https://github.com/serde-rs/json. Cargo.lock supplies the reproducible dependency inventory.
- C++ IXWebSocket v12.1.0: BSD-3-Clause, https://github.com/machinezone/IXWebSocket/blob/v12.1.0/LICENSE.txt. nlohmann/json v3.12.0: MIT, https://github.com/nlohmann/json/blob/v3.12.0/LICENSE.MIT. CMake pins full upstream commit identities; TLS and zlib are disabled for the loopback adapter.

Release distributors should preserve complete dependency license files from their native build outputs and satisfy platform-runtime notices. Development example build artifacts are not a production plugin catalog release.
