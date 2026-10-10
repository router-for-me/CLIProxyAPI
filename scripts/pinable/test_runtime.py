#!/usr/bin/env python3
"""Distribution-contract tests; header fixtures are not real server executables."""
import copy
import io
import json
import os
from pathlib import Path
import struct
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile

sys.path.insert(0, str(Path(__file__).parent))
import runtime

COMMIT = "a" * 40


def executable(target):
    data = bytearray(256)
    system, arch = target.split("/")
    if system == "windows":
        data[:2] = b"MZ"
        struct.pack_into("<I", data, 60, 128)
        data[128:132] = b"PE\0\0"
        struct.pack_into("<H", data, 132, {"amd64": 0x8664, "arm64": 0xaa64}[arch])
        struct.pack_into("<H", data, 152, 0x20b)
    elif system == "darwin":
        data[:4] = b"\xcf\xfa\xed\xfe"
        struct.pack_into("<I", data, 4, {"amd64": 0x1000007, "arm64": 0x100000c}[arch])
        struct.pack_into("<I", data, 12, 2)
    else:
        data[:6] = b"\x7fELF\x02\x01"
        struct.pack_into("<HH", data, 16, 2, {"amd64": 62, "arm64": 183}[arch])
        struct.pack_into("<Q", data, 32, 64)
        struct.pack_into("<HH", data, 54, 56, 1)
        struct.pack_into("<I", data, 64, 1)
    return bytes(data)


def metadata(target):
    return {"schema_version": 1, "component": "cliproxyapi", "profile": runtime.PROFILE,
            "version": "test-1", "target": target, "host_contract_version": "1",
            "source": {"repository": runtime.REPOSITORY, "commit": COMMIT,
                       "commit_time": "2026-09-22T00:00:00Z"},
            "build": {"go_version": "go1.26.0", "cgo_enabled": False,
                      "trimpath": True, "buildvcs": False,
                      "signing": "ad-hoc" if target.startswith("darwin/") else "unsigned"},
            "capabilities": {"dynamic_library_plugins": target.startswith("windows/"),
                             "dynamic_library_plugins_tested": False, "parent_monitor": True,
                             "runtime_control": True, "ephemeral_api_key": True}}


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.license = self.root / "LICENSE"
        self.license.write_text("MIT license fixture\n")
        self.serial = 0

    def bundle(self, target="linux/amd64", meta=None):
        self.serial += 1
        binary = self.root / f"fixture-{self.serial}"
        binary.write_bytes(executable(target))
        return runtime.package(binary, self.license,
                               self.root / f"bundle-{self.serial}", meta or metadata(target))

    def rewrite(self, path, change):
        value = json.loads(path.read_text())
        change(value)
        path.write_bytes(runtime.encoded(value))

    def test_all_six_platform_formats_roundtrip(self):
        for target in runtime.TARGETS:
            with self.subTest(target=target):
                path = self.bundle(target)
                manifest, binary = runtime.verify(path, COMMIT)
                self.assertEqual(binary, executable(target))
                self.assertEqual(manifest["binary"]["sha256"], runtime.digest(binary))

    def test_architecture_mismatches_rejected(self):
        for system in ("windows", "darwin", "linux"):
            with self.subTest(system=system), self.assertRaises(ValueError):
                runtime.validate_binary(executable(system + "/amd64"), system + "/arm64")

    def test_truncated_or_wrong_executable_rejected(self):
        for target in runtime.TARGETS:
            for data in (b"", b"not executable" * 10, executable(target)[:63]):
                with self.subTest(target=target, size=len(data)), self.assertRaises(ValueError):
                    runtime.validate_binary(data, target)

    def test_elf_interpreter_rejected(self):
        binary = bytearray(executable("linux/amd64"))
        struct.pack_into("<I", binary, 64, 3)
        with self.assertRaisesRegex(ValueError, "dynamic interpreter"):
            runtime.validate_binary(binary, "linux/amd64")

    def test_invalid_targets_and_versions_rejected(self):
        for target in ("windows/386", "darwin/universal", "../../windows", "linux/armv7"):
            with self.assertRaises(ValueError):
                runtime.target_parts(target)
        for version in ("", "../escape", "a b", "-X", "a\nb", "v" * 81, "x;rm", "版本"):
            with self.assertRaises(ValueError):
                runtime.archive_name(version, "linux/amd64")

    def test_archives_deterministic(self):
        for target in runtime.TARGETS:
            first = json.loads(self.bundle(target).read_text())
            second = json.loads(self.bundle(target).read_text())
            self.assertEqual(first, second)

    def test_license_line_endings_are_canonical(self):
        self.license.write_bytes(b"MIT license fixture\r\n")
        first = json.loads(self.bundle().read_text())
        self.license.write_bytes(b"MIT license fixture\n")
        second = json.loads(self.bundle().read_text())
        self.assertEqual(first, second)

    def test_output_not_overwritten(self):
        path = self.bundle()
        original = path.read_bytes()
        with self.assertRaises(ValueError):
            runtime.package(self.root / "fixture-1", self.license, path.parent, metadata("linux/amd64"))
        self.assertEqual(path.read_bytes(), original)

    def test_tampered_archive_rejected(self):
        path = self.bundle()
        manifest = json.loads(path.read_text())
        archive = path.parent / manifest["archive"]["file"]
        data = bytearray(archive.read_bytes())
        data[-1] ^= 1
        archive.write_bytes(data)
        with self.assertRaisesRegex(ValueError, "archive checksum"):
            runtime.verify(path)

    def test_approved_commit_and_hash_required_for_staging(self):
        path = self.bundle()
        with self.assertRaisesRegex(ValueError, "source commit"):
            runtime.verify(path, "b" * 40)
        with self.assertRaisesRegex(ValueError, "binary SHA256"):
            runtime.verify(path, COMMIT, "0" * 64)

    def test_manifest_filenames_and_contract_checked(self):
        for field, value in (("archive", "../escape.zip"), ("binary", "different.exe"), ("license", "/tmp/license")):
            path = self.bundle()
            self.rewrite(path, lambda m: m[field].update(file=value))
            with self.assertRaises(ValueError):
                runtime.verify(path)
        for change in (lambda m: m.update(host_contract_version="2"),
                       lambda m: m["build"].update(cgo_enabled=True),
                       lambda m: m["source"].update(repository="https://example.invalid"),
                       lambda m: m.update(extra="unexpected")):
            path = self.bundle()
            self.rewrite(path, change)
            with self.assertRaises(ValueError):
                runtime.verify(path)

    def test_embedded_manifest_must_match(self):
        path = self.bundle()
        self.rewrite(path, lambda m: m["source"].update(commit="b" * 40))
        with self.assertRaisesRegex(ValueError, "embedded"):
            runtime.verify(path)

    def test_duplicate_json_keys_rejected(self):
        with self.assertRaisesRegex(ValueError, "duplicate"):
            runtime.read_json(b'{"component":"safe","component":"unsafe"}')

    def test_zip_path_traversal_and_symlink_rejected(self):
        for name, mode in (("../escape", 0o100644), ("runtime.json", 0o120777)):
            path = self.root / "bad.zip"
            with zipfile.ZipFile(path, "w") as archive:
                for entry_name in ("cli-proxy-api.exe", "LICENSE", name):
                    entry = zipfile.ZipInfo(entry_name)
                    safe_mode = 0o100755 if entry_name == "cli-proxy-api.exe" else 0o100644
                    entry.external_attr = (mode if entry_name == name else safe_mode) << 16
                    archive.writestr(entry, "fake")
            with self.assertRaises(ValueError):
                runtime.read_archive(path, "cli-proxy-api.exe")

    def test_tar_link_path_and_permissions_rejected(self):
        for name, kind, mode in (("../escape", tarfile.REGTYPE, 0o644),
                                 ("cli-proxy-api", tarfile.SYMTYPE, 0o755),
                                 ("cli-proxy-api", tarfile.REGTYPE, 0o4755)):
            path = self.root / "bad.tar.gz"
            with tarfile.open(path, "w:gz") as archive:
                entry = tarfile.TarInfo(name)
                entry.type, entry.mode, entry.size = kind, mode, 4
                archive.addfile(entry, io.BytesIO(b"test"))
            with self.assertRaises(ValueError):
                runtime.read_archive(path, "cli-proxy-api")

    def test_stage_pinned_runtime_without_replacing(self):
        path = self.bundle("windows/arm64")
        manifest = json.loads(path.read_text())
        output = self.root / "stage" / "cli-proxy-api.exe"
        runtime.stage(path, output, COMMIT, manifest["binary"]["sha256"])
        self.assertEqual(output.read_bytes(), executable("windows/arm64"))
        with self.assertRaises(ValueError):
            runtime.stage(path, output, COMMIT, manifest["binary"]["sha256"])

    def test_stage_relative_path_rejected(self):
        path = self.bundle()
        manifest = json.loads(path.read_text())
        with self.assertRaises(ValueError):
            runtime.stage(path, Path("cli-proxy-api"), COMMIT, manifest["binary"]["sha256"])

    def test_index_requires_complete_consistent_set(self):
        self.bundle()
        with self.assertRaises(ValueError):
            runtime.index(self.root, self.root / "out", COMMIT)
        for target in runtime.TARGETS:
            if target != "linux/amd64":
                self.bundle(target)
        result = runtime.index(self.root, self.root / "out", COMMIT)
        self.assertEqual(len(result["targets"]), 6)
        sums = (self.root / "out" / "SHA256SUMS").read_text().splitlines()
        self.assertEqual(len(sums), 13)
        for line in sums:
            expected, name = line.split("  ")
            self.assertEqual(expected, runtime.digest((self.root / "out" / name).read_bytes()))

    def test_index_mixed_versions_rejected(self):
        for i, target in enumerate(runtime.TARGETS):
            meta = metadata(target)
            if i == 0:
                meta["version"] = "different"
            self.bundle(target, meta)
        with self.assertRaisesRegex(ValueError, "mixed"):
            runtime.index(self.root, self.root / "out", COMMIT)
        self.assertFalse((self.root / "out").exists())

    def test_build_profile_pins_source_and_go_flags(self):
        (self.root / "go.mod").write_text("module example.invalid/runtime\n\ngo 1.26.0\n")
        commands = []
        def command(*args, **kwargs):
            commands.append((args, kwargs))
            if args[:3] == ("git", "rev-parse", "--show-toplevel"):
                return str(self.root.resolve())
            if args[:2] == ("git", "rev-parse"):
                return COMMIT
            if args[:2] == ("git", "show"):
                return "1789948800"
            if args[:2] == ("go", "env"):
                return "go1.26.0"
            if args[:2] == ("go", "build"):
                Path(args[args.index("-o") + 1]).write_bytes(executable("linux/amd64"))
            return ""
        with mock.patch.object(runtime, "run", side_effect=command):
            path = runtime.build(self.root, self.root / "out", "linux/amd64", "test")
        manifest, _ = runtime.verify(path, COMMIT)
        args, kwargs = next(item for item in commands if item[0][:2] == ("go", "build"))
        self.assertIn("-trimpath", args)
        self.assertIn("-buildvcs=false", args)
        self.assertEqual(kwargs["env"]["CGO_ENABLED"], "0")
        self.assertEqual(kwargs["env"]["GOTOOLCHAIN"], "local")
        self.assertEqual(manifest["build"]["go_version"], "go1.26.0")


if __name__ == "__main__":
    unittest.main()
