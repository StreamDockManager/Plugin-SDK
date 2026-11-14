import importlib.util
import json
from pathlib import Path
import struct
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

sys.path.insert(0, str(Path(__file__).parents[1] / "tools"))

SPEC = importlib.util.spec_from_file_location("unified", Path(__file__).parents[1] / "tools/unified.py")
unified = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(unified)


class UnifiedTests(unittest.TestCase):
    def fixture(self, directory, target, version="0.1.0", asset=b"same"):
        system, arch = target.split("/")
        data = bytearray(128)
        if system == "darwin":
            data[:4] = b"\xcf\xfa\xed\xfe"
            struct.pack_into("<I", data, 4, 0x100000c if arch == "arm64" else 0x1000007)
            struct.pack_into("<I", data, 12, 2)
        elif system == "linux":
            data[:6] = b"\x7fELF\x02\x01"
            struct.pack_into("<H", data, 16, 2)
            struct.pack_into("<H", data, 18, 62)
        else:
            data[:2] = b"MZ"
            struct.pack_into("<I", data, 60, 64)
            data[64:68] = b"PE\0\0"
            struct.pack_into("<H", data, 68, 0x8664)
            struct.pack_into("<H", data, 88, 0x20b)
        entry = "bin/plugin.exe" if system == "windows" else "bin/plugin"
        manifest = {"UUID": "org.test.plugin", "Version": version, "SDKVersion": 1, "CodePath": entry,
                    "OS": [{"Platform": {"darwin": "mac", "linux": "linux", "windows": "windows"}[system], "MinimumVersion": "0"}]}
        path = directory / (target.replace("/", "-") + ".zip")
        with zipfile.ZipFile(path, "w") as z:
            for name, value in {"manifest.json": json.dumps(manifest).encode(), entry: bytes(data), "icon.png": asset, "RUNTIME_LICENSES.txt": target.encode()}.items():
                info = zipfile.ZipInfo("org.test.plugin.sdPlugin/" + name)
                info.external_attr = (0o100755 if name == entry else 0o100644) << 16
                z.writestr(info, value)
        return path

    def test_all_members_use_explicit_deflate_level_six(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            inputs = self.inputs(directory)
            original = zipfile._get_compressor
            with patch.object(zipfile, '_get_compressor', wraps=original) as compressor:
                output = unified.merge(inputs, directory / 'deflate6.zip')
            self.assertTrue(compressor.call_args_list)
            self.assertTrue(all(call.args == (zipfile.ZIP_DEFLATED, 6) for call in compressor.call_args_list))
            with zipfile.ZipFile(output) as archive:
                self.assertIsNone(archive.testzip())
                self.assertTrue(all(item.compress_type == zipfile.ZIP_DEFLATED for item in archive.infolist()))

    def inputs(self, directory):
        return {t: self.fixture(directory, t) for t in ("darwin/arm64", "linux/amd64", "windows/amd64")}

    def test_opendeck_merge_normalizes_assets_and_tracks_host(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            inputs = self.inputs(d)
            for target, path in inputs.items():
                contents, modes, manifest = unified.read_package(path)
                manifest["Icon"] = "icon.png"
                if target.startswith("linux/"):
                    manifest["CodePathLin"] = manifest.pop("CodePath")
                contents["manifest.json"] = json.dumps(manifest).encode()
                with zipfile.ZipFile(path, "w") as archive:
                    for name, value in contents.items():
                        info = zipfile.ZipInfo("org.test.plugin.sdPlugin/" + name)
                        info.external_attr = modes[name] << 16
                        archive.writestr(info, value)
            output = unified.merge(inputs, d / "oa.zip", host="opendeck")
            self.assertEqual(unified.validate(output, host="opendeck")["targets"], sorted(inputs))
            with self.assertRaises(ValueError):
                unified.validate(output)
            _, _, manifest = unified.read_package(output)
            self.assertEqual(manifest["Icon"], "icon")
            self.assertEqual(manifest["CodePathLin"], manifest["CodePaths"]["x86_64-unknown-linux-gnu"])
            self.assertEqual(manifest["CodePathMac"], manifest["CodePaths"]["aarch64-apple-darwin"])
            self.assertEqual(len(manifest["CodePaths"]), 3)

    def test_opendeck_fallback_prefers_x86_without_changing_exact_routes(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            inputs = self.inputs(d)
            inputs["darwin/amd64"] = self.fixture(d, "darwin/amd64")
            output = unified.merge(inputs, d / "oa.zip", host="opendeck")
            contents, modes, manifest = unified.read_package(output)
            self.assertEqual(manifest["CodePathMac"], manifest["CodePaths"]["x86_64-apple-darwin"])
            self.assertIn("aarch64-apple-darwin", manifest["CodePaths"])
            manifest["CodePathMac"] = manifest["CodePathWin"]
            contents["manifest.json"] = json.dumps(manifest).encode()
            with zipfile.ZipFile(output, "w") as archive:
                for name, value in contents.items():
                    info = zipfile.ZipInfo("org.test.plugin.sdPlugin/" + name)
                    info.external_attr = modes[name] << 16
                    archive.writestr(info, value)
            with self.assertRaises(ValueError):
                unified.validate(output, host="opendeck")

    def test_deterministic_and_routed(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            inputs = self.inputs(d)
            a = unified.merge(inputs, d / "a.zip")
            b = unified.merge(dict(reversed(list(inputs.items()))), d / "b.zip")
            self.assertEqual(a.read_bytes(), b.read_bytes())
            self.assertEqual(unified.validate(a)["targets"], sorted(inputs))
            with zipfile.ZipFile(a) as z:
                m = json.loads(z.read("org.test.plugin.sdPlugin/manifest.json"))
                self.assertEqual(len(m["CodePaths"]), 3)
                self.assertNotIn("CodePath", m)
                for entry in m["CodePaths"].values():
                    self.assertTrue(z.getinfo("org.test.plugin.sdPlugin/" + entry).external_attr >> 16 & 0o111)
                self.assertIn("org.test.plugin.sdPlugin/target-audits/linux-amd64/RUNTIME_LICENSES.txt", z.namelist())

    def test_reject_missing_os_version_assets_and_cpu(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            inputs = self.inputs(d)
            for bad in ({"linux/amd64": inputs["linux/amd64"]}, {**inputs, "darwin/amd64": inputs["darwin/arm64"]}):
                with self.assertRaises(ValueError): unified.merge(bad, d / "out.zip")
            self.fixture(d, "linux/amd64", version="9.0.0")
            with self.assertRaises(ValueError): unified.merge(inputs, d / "out.zip")
            self.fixture(d, "linux/amd64", asset=b"changed")
            with self.assertRaises(ValueError): unified.merge(inputs, d / "out.zip")
            self.assertFalse((d / "out.zip").exists())

    def test_failed_merge_preserves_existing_output(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            output = d / "existing.zip"
            output.write_bytes(b"keep existing")
            inputs = self.inputs(d)
            with zipfile.ZipFile(inputs["linux/amd64"], "a") as z:
                info = zipfile.ZipInfo("org.test.plugin.sdPlugin/link")
                info.create_system = 3
                info.external_attr = 0o120777 << 16
                z.writestr(info, b"/outside")
            with self.assertRaises(ValueError): unified.merge(inputs, output)
            self.assertEqual(output.read_bytes(), b"keep existing")

    def test_reject_reserved_and_prefix_namespaces(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            for name in ("target-audits/linux-amd64/RUNTIME_LICENSES.txt", "TARGET-AUDITS/extra", "unified-package.json", "icon.png/child", "MANIFEST.JSON/child"):
                inputs = self.inputs(d)
                for path in inputs.values():
                    with zipfile.ZipFile(path, "a") as z:
                        z.writestr("org.test.plugin.sdPlugin/" + name, b"bad")
                with self.assertRaises(ValueError): unified.merge(inputs, d / "out.zip")

    def test_reject_extra_os_claim_and_linux_fallback(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            for update in ({"CodePathLin": "bin/plugin"}, {"OS": [{"Platform": "linux"}, {"Platform": "windows"}]}):
                inputs = self.inputs(d)
                path = inputs["linux/amd64"]
                with zipfile.ZipFile(path) as z:
                    entries = [(i, z.read(i)) for i in z.infolist()]
                with zipfile.ZipFile(path, "w") as z:
                    for info, data in entries:
                        if info.filename.endswith("/manifest.json"):
                            manifest = json.loads(data)
                            manifest.update(update)
                            data = json.dumps(manifest).encode()
                        z.writestr(info, data)
                with self.assertRaises(ValueError): unified.merge(inputs, d / "out.zip")

    def test_shared_executable_mode_preserved(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            inputs = self.inputs(d)
            for path in inputs.values():
                with zipfile.ZipFile(path, "a") as z:
                    info = zipfile.ZipInfo("org.test.plugin.sdPlugin/helper.sh")
                    info.external_attr = 0o100755 << 16
                    z.writestr(info, b"#!/bin/sh")
            with zipfile.ZipFile(unified.merge(inputs, d / "out.zip")) as z:
                self.assertTrue(z.getinfo("org.test.plugin.sdPlugin/helper.sh").external_attr >> 16 & 0o111)

    def test_reject_traversal_and_duplicates(self):
        with tempfile.TemporaryDirectory() as d:
            d = Path(d)
            for member in ("org.test.plugin.sdPlugin/../escape", "org.test.plugin.sdPlugin/ICON.PNG"):
                inputs = self.inputs(d)
                with zipfile.ZipFile(inputs["linux/amd64"], "a") as z: z.writestr(member, b"bad")
                with self.assertRaises(ValueError): unified.merge(inputs, d / "out.zip")


if __name__ == "__main__": unittest.main()
