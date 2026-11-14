# SPDX-License-Identifier: MIT
# Copyright (c) 2026 LouisT. See authoring_common.LICENSE in generated projects.
"""Combine trusted, independently built target packages without executing binaries."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import stat
import struct
import tempfile
import zipfile

TRIPLES = {
    "darwin/amd64": "x86_64-apple-darwin", "darwin/arm64": "aarch64-apple-darwin",
    "linux/amd64": "x86_64-unknown-linux-gnu", "linux/arm64": "aarch64-unknown-linux-gnu",
    "windows/amd64": "x86_64-pc-windows-msvc", "windows/arm64": "aarch64-pc-windows-msvc",
}
KEYS = {"darwin": "CodePathMac", "windows": "CodePathWin", "linux": "CodePathLinux"}
AUDITS = {"package-audit.json", "runtime-dependencies.json", "RUNTIME_LICENSES.txt", "THIRD_PARTY_LICENSES.txt"}
MAX_BYTES = 256 * 1024 * 1024


# Validate the native operating-system and architecture pair
def native_target(data):
    try:
        if data[:4] == b"\x7fELF" and data[4:6] == b"\x02\x01" and struct.unpack_from("<H", data, 16)[0] in (2, 3):
            return "linux/" + {62: "amd64", 183: "arm64"}[struct.unpack_from("<H", data, 18)[0]]
        if data[:4] == b"\xcf\xfa\xed\xfe" and struct.unpack_from("<I", data, 12)[0] == 2:
            return "darwin/" + {0x1000007: "amd64", 0x100000c: "arm64"}[struct.unpack_from("<I", data, 4)[0]]
        if data[:2] == b"MZ":
            offset = struct.unpack_from("<I", data, 60)[0]
            if data[offset:offset+4] == b"PE\0\0" and struct.unpack_from("<H", data, offset+24)[0] == 0x20b:
                return "windows/" + {0x8664: "amd64", 0xaa64: "arm64"}[struct.unpack_from("<H", data, offset+4)[0]]
    except (KeyError, struct.error, IndexError):
        pass
    raise ValueError("Expected a supported native executable; unified merger requires native target archives")


# Check namespace
def check_namespace(names):
    folded = set()
    for name in names:
        key = name.casefold()
        if key in folded:
            raise ValueError("Case-colliding output paths")
        folded.add(key)
    for name in folded:
        parts = name.split("/")
        if any("/".join(parts[:n]) in folded for n in range(1, len(parts))):
            raise ValueError("File/directory output collision")


# Read package
def read_package(path):
    if Path(path).stat().st_size > MAX_BYTES:
        raise ValueError("Compressed input exceeds size limit")
    contents, modes, seen = {}, {}, set()
    with zipfile.ZipFile(path) as archive:
        entries = archive.infolist()
        if len(entries) > 4096 or sum(i.file_size for i in entries) > MAX_BYTES:
            raise ValueError("Input package exceeds bounded inventory")
        roots = set()
        for item in entries:
            name = item.filename
            parts = PurePosixPath(name).parts
            mode = item.external_attr >> 16
            if (not parts or name.startswith("/") or "\\" in name or ":" in name
                    or any(p in ("..", ".", "") for p in name.rstrip("/").split("/"))
                    or stat.S_IFMT(mode) not in (0, stat.S_IFREG, stat.S_IFDIR) or item.flag_bits & 1):
                raise ValueError("Unsafe or encrypted package entry")
            if name.casefold() in seen:
                raise ValueError("Duplicate or case-colliding package entry")
            seen.add(name.casefold())
            roots.add(parts[0])
            if item.is_dir():
                continue
            if len(parts) < 2:
                raise ValueError("Expected a top-level plugin bundle")
            relative = "/".join(parts[1:])
            contents[relative] = archive.read(item)
            modes[relative] = mode
        if len(roots) != 1 or not next(iter(roots)).endswith(".sdPlugin"):
            raise ValueError("Expected one plugin bundle")
    check_namespace(contents)
    manifest = json.loads(contents["manifest.json"])
    if next(iter(roots)) != manifest.get("UUID", "") + ".sdPlugin":
        raise ValueError("Bundle identity mismatch")
    return contents, modes, manifest


# Legacy hosts cannot select CPU architecture; prefer x86_64 per OS
def legacy_paths(paths):
    """Legacy hosts cannot select CPU architecture; prefer x86_64 per OS."""
    result = {}
    for system, key in (("darwin", "CodePathMac"), ("windows", "CodePathWin"), ("linux", "CodePathLin")):
        for arch in ("amd64", "arm64"):
            entry = paths.get(TRIPLES[system + "/" + arch])
            if entry is not None:
                result[key] = entry
                break
    return result


# Offline structural and executable validation; not native runtime qualification
def validate(path, host="manager"):
    """Offline structural and executable validation; not native runtime qualification."""
    if host not in ("manager", "opendeck"):
        raise ValueError("Unsupported package host")
    contents, _, manifest = read_package(path)
    paths = manifest.get("CodePaths")
    if not isinstance(paths, dict) or not paths:
        raise ValueError("Expected a unified CodePaths manifest")
    fallback_keys = ("CodePath", "CodePathMac", "CodePathWin", "CodePathLinux", "CodePathLin")
    actual_fallbacks = {key: manifest[key] for key in fallback_keys if key in manifest}
    if host == "manager" and actual_fallbacks:
        raise ValueError("Unified Manager manifests must not retain platform fallback entries")
    if host == "opendeck" and actual_fallbacks != legacy_paths(paths):
        raise ValueError("OpenAction legacy fallbacks must match the selected CodePaths executables")
    reverse = {triple: target for target, triple in TRIPLES.items()}
    targets = set()
    for triple, entry in paths.items():
        if triple not in reverse or not isinstance(entry, str) or entry not in contents:
            raise ValueError("Invalid unified target entry")
        target = reverse[triple]
        if not entry.startswith("bin/" + target.replace("/", "-") + "/") or native_target(contents[entry]) != target:
            raise ValueError("Unified executable does not match target")
        targets.add(target)
    systems = {target.split("/")[0] for target in targets}
    if systems != set(KEYS):
        raise ValueError("Unified bundle must cover Windows, macOS, and Linux")
    if {item.get("Platform") for item in manifest.get("OS", [])} != {"mac", "windows", "linux"}:
        raise ValueError("Unified OS requirements disagree with targets")
    inventory = json.loads(contents["unified-package.json"])
    if inventory.get("host", "manager") != host:
        raise ValueError("Unified package host differs; select the matching --host")
    if host == "opendeck":
        from openaction import export_manifest
        for platform in ("mac", "windows", "linux"):
            exported = export_manifest(manifest, contents, platform)
            if {k: v for k, v in exported.items() if k != "OS"} != {k: v for k, v in manifest.items() if k != "OS"}:
                raise ValueError("Unified OpenAction image references are not canonical")
    records = inventory.get("inputs", [])
    if inventory.get("schema_version") != 1 or len(records) != len(targets) or {x.get("target") for x in records} != targets:
        raise ValueError("Unified input inventory disagrees with targets")
    for record in records:
        digest = record.get("sha256", "")
        if not isinstance(digest, str) or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
            raise ValueError("Invalid unified input digest")
    return {"identity": manifest["UUID"], "targets": sorted(targets), "execution": "none; static validation only"}


# inputs maps explicit OS/architecture names to existing validated packages
def merge(inputs, output, host="manager"):
    """inputs maps explicit OS/architecture names to existing validated packages."""
    if host not in ("manager", "opendeck"):
        raise ValueError("Unsupported package host")
    if not inputs or set(t.split("/")[0] for t in inputs) != set(KEYS):
        raise ValueError("Unified bundles require Windows, macOS, and Linux inputs")
    common, common_modes, files, modes, baseline, result = None, None, {}, {}, None, None
    paths, systems, inventory = {}, {}, []
    for target, source in sorted(inputs.items()):
        if target not in TRIPLES:
            raise ValueError("Unsupported target: " + target)
        contents, source_modes, manifest = read_package(source)
        system = target.split("/")[0]
        expected_platform = {"darwin": "mac", "windows": "windows", "linux": "linux"}[system]
        if ("CodePaths" in manifest or (host == "manager" and "CodePathLin" in manifest)
                or not manifest.get("OS")
                or any(x.get("Platform") != expected_platform for x in manifest["OS"])):
            raise ValueError("Input must be an OS-specific package declaring the selected platform")
        if host == "opendeck":
            from openaction import export_manifest
            manifest = export_manifest(manifest, contents, expected_platform, materialize_aliases=True)
            for name in contents:
                source_modes.setdefault(name, 0o100644)
            if "CodePathLinux" in manifest:
                raise ValueError("OpenAction uses CodePathLin, not CodePathLinux")
        key = "CodePathLin" if host == "opendeck" and system == "linux" else KEYS[system]
        entry = manifest.get(key, manifest.get("CodePath"))
        if entry not in contents or not entry.startswith("bin/"):
            raise ValueError("Target entry must be an existing bin/ executable")
        if native_target(contents[entry]) != target:
            raise ValueError("Executable does not match declared target: " + target)
        identity = {k: v for k, v in manifest.items() if k not in {"OS", "CodePath", "CodePathMac", "CodePathWin", "CodePathLinux", "CodePathLin", "CodePaths"}}
        if baseline is not None and identity != baseline:
            raise ValueError("Target manifests differ in identity, version, actions, or capabilities")
        baseline = identity
        result = dict(identity)
        shared = {k: v for k, v in contents.items() if k != "manifest.json" and not k.startswith("bin/") and k not in AUDITS}
        if common is not None and common != shared:
            raise ValueError("Target shared assets differ; rebuild all packages from the same source")
        shared_modes = {name: source_modes[name] & 0o111 for name in shared}
        if common_modes is not None and common_modes != shared_modes:
            raise ValueError("Shared asset executable modes differ")
        for name in shared:
            first = name.split("/")[0].casefold()
            if first in {"bin", "target-audits", "unified-package.json"}:
                raise ValueError("Input uses reserved unified output namespace")
        common = shared
        common_modes = shared_modes
        label = target.replace("/", "-")
        for name, data in contents.items():
            if name.startswith("bin/"):
                destination = "bin/" + label + "/" + name[4:]
            elif name in AUDITS:
                destination = "target-audits/" + label + "/" + name
            else:
                continue
            files[destination] = data
            modes[destination] = source_modes[name]
        paths[TRIPLES[target]] = "bin/" + label + "/" + entry[4:]
        for requirement in manifest.get("OS", []):
            platform = requirement["Platform"]
            if platform in systems and systems[platform] != requirement:
                raise ValueError("Conflicting OS requirements")
            systems[platform] = requirement
        inventory.append({"target": target, "sha256": hashlib.sha256(Path(source).read_bytes()).hexdigest()})
    check_namespace([*files, *common, "manifest.json", "unified-package.json"])
    files.update(common)
    modes.update(common_modes)
    result["CodePaths"] = paths
    if host == "opendeck":
        result.update(legacy_paths(paths))
    result["OS"] = [systems[k] for k in sorted(systems)]
    files["manifest.json"] = (json.dumps(result, indent=4, sort_keys=True) + "\n").encode()
    files["unified-package.json"] = (json.dumps({"schema_version": 1, "inputs": inventory, **({"host": host} if host != "manager" else {})}, indent=2) + "\n").encode()
    if len(files) > 4096 or sum(map(len, files.values())) > MAX_BYTES:
        raise ValueError("Unified package exceeds bounded inventory")
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".unified-", suffix=".zip", dir=output.parent)
    os.close(fd)
    try:
        with zipfile.ZipFile(temporary, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
            for name, data in sorted(files.items()):
                info = zipfile.ZipInfo(result["UUID"] + ".sdPlugin/" + name, (2026, 1, 1, 0, 0, 0))
                info.create_system = 3
                info.external_attr = (0o100755 if modes.get(name, 0) & 0o111 else 0o100644) << 16
                archive.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=6)
        if Path(temporary).stat().st_size > MAX_BYTES:
            raise ValueError("Compressed unified package exceeds size limit")
        validate(temporary, host=host)
        os.replace(temporary, output)
    finally:
        Path(temporary).unlink(missing_ok=True)
    return output


# Run the unified command and enforce its input options
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", action="append", metavar="OS/ARCH=ZIP")
    parser.add_argument("--validate", type=Path, help="Validate an existing unified archive without executing it")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--host", choices=["manager", "opendeck"], default="manager")
    args = parser.parse_args()
    if args.validate:
        if args.input or args.output:
            parser.error("--validate cannot be combined with --input or --output")
        print(json.dumps(validate(args.validate, host=args.host), indent=2))
        return
    if not args.input or not args.output:
        parser.error("Merging requires --input and --output")
    inputs = {}
    for item in args.input:
        target, separator, path = item.partition("=")
        if not separator or target in inputs:
            parser.error("Inputs must be unique OS/ARCH=ZIP values")
        inputs[target] = Path(path)
    print(merge(inputs, args.output, host=args.host))


if __name__ == "__main__":
    main()
