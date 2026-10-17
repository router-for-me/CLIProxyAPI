#!/usr/bin/env python3
"""Build and verify pinned, portable CLIProxyAPI runtimes using only the stdlib.

This manifest is a distribution contract, NOT Desktop's component-lock.json.
Nothing in this tool changes Desktop locks, Git refs, credentials or releases.
"""
from __future__ import annotations

import argparse
import copy
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from datetime import datetime, timezone

REPOSITORY = "https://github.com/PinableAgents/CLIProxyAPI"
TARGETS = tuple(f"{system}/{arch}" for system in ("windows", "darwin", "linux")
                for arch in ("amd64", "arm64"))
PROFILE = "desktop-portable"
MAX_BINARY = 256 * 1024 * 1024
MAX_METADATA = 64 * 1024
HEX40 = re.compile(r"[0-9a-f]{40}\Z")
HEX64 = re.compile(r"[0-9a-f]{64}\Z")
VERSION = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,79}\Z")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def encoded(value: object) -> bytes:
    return (json.dumps(value, ensure_ascii=True, indent=2, sort_keys=True) + "\n").encode()


def no_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON key: {key}")
        result[key] = value
    return result


def read_json(data: bytes) -> dict:
    require(len(data) <= MAX_METADATA, "manifest exceeds size limit")
    result = json.loads(data, object_pairs_hook=no_duplicate_keys)
    require(isinstance(result, dict), "manifest must be an object")
    return result


def run(*args: str, cwd: Path | None = None, env=None) -> str:
    result = subprocess.run(args, cwd=cwd, env=env, check=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            text=True)
    return result.stdout.strip()


def target_parts(target: str) -> tuple[str, str]:
    require(target in TARGETS, f"unsupported target {target!r}; choose {', '.join(TARGETS)}")
    return tuple(target.split("/"))


def binary_name(target: str) -> str:
    system, _ = target_parts(target)
    return "cli-proxy-api.exe" if system == "windows" else "cli-proxy-api"


def archive_name(version: str, target: str) -> str:
    require(bool(VERSION.fullmatch(version)), "version must be 1-80 safe ASCII characters")
    system, arch = target_parts(target)
    extension = "zip" if system == "windows" else "tar.gz"
    return f"CLIProxyAPI_pinable_{version}_{system}_{arch}.{extension}"


def validate_binary(data: bytes, target: str) -> None:
    """Check real executable headers, including static Linux linkage, not suffixes."""
    system, arch = target_parts(target)
    require(64 <= len(data) <= MAX_BINARY, "invalid executable size")
    if system == "windows":
        require(data[:2] == b"MZ", "expected PE executable")
        offset = struct.unpack_from("<I", data, 60)[0]
        require(offset + 26 <= len(data), "truncated PE header")
        require(data[offset:offset + 4] == b"PE\0\0", "invalid PE signature")
        require(struct.unpack_from("<H", data, offset + 4)[0] ==
                {"amd64": 0x8664, "arm64": 0xaa64}[arch], "PE architecture mismatch")
        require(struct.unpack_from("<H", data, offset + 24)[0] == 0x20b,
                "expected PE32+ executable")
    elif system == "darwin":
        require(data[:4] == b"\xcf\xfa\xed\xfe", "expected thin 64-bit Mach-O executable")
        require(struct.unpack_from("<I", data, 4)[0] ==
                {"amd64": 0x1000007, "arm64": 0x100000c}[arch], "Mach-O architecture mismatch")
        require(struct.unpack_from("<I", data, 12)[0] == 2, "Mach-O must be executable")
    else:
        require(data[:6] == b"\x7fELF\x02\x01", "expected little-endian ELF64 executable")
        require(struct.unpack_from("<H", data, 18)[0] ==
                {"amd64": 62, "arm64": 183}[arch], "ELF architecture mismatch")
        require(struct.unpack_from("<H", data, 16)[0] in (2, 3), "ELF must be executable")
        offset = struct.unpack_from("<Q", data, 32)[0]
        size, count = struct.unpack_from("<HH", data, 54)
        require(size >= 56 and count > 0 and offset + size * count <= len(data),
                "invalid ELF program headers")
        for i in range(count):
            require(struct.unpack_from("<I", data, offset + i * size)[0] != 3,
                    "portable Linux runtime must not depend on a dynamic interpreter")


def validate_manifest(manifest: dict) -> str:
    require(set(manifest) == {"schema_version", "component", "profile", "version", "source",
                             "target", "host_contract_version", "build", "capabilities",
                             "binary", "license", "archive"}, "unexpected manifest fields")
    require(manifest["schema_version"] == 1 and manifest["component"] == "cliproxyapi",
            "unsupported manifest schema/component")
    require(manifest["profile"] == PROFILE and manifest["host_contract_version"] == "1",
            "unsupported runtime profile/host contract")
    require(isinstance(manifest["version"], str) and bool(VERSION.fullmatch(manifest["version"])),
            "invalid runtime version")
    source = manifest["source"]
    require(set(source) == {"repository", "commit", "commit_time"}, "invalid source fields")
    require(source["repository"] == REPOSITORY and bool(HEX40.fullmatch(source["commit"])),
            "untrusted repository or non-pinned commit")
    datetime.strptime(source["commit_time"], "%Y-%m-%dT%H:%M:%SZ")
    target = manifest["target"]
    require(isinstance(target, str), "target must be a string")
    system, _ = target_parts(target)
    build = manifest["build"]
    require(set(build) == {"go_version", "cgo_enabled", "trimpath", "buildvcs", "signing"},
            "invalid build metadata")
    require(isinstance(build["go_version"], str) and build["go_version"].startswith("go1."),
            "invalid Go toolchain")
    require(build["cgo_enabled"] is False and build["trimpath"] is True and
            build["buildvcs"] is False, "runtime does not use the pinned portable build profile")
    require(build["signing"] == ("ad-hoc" if system == "darwin" else "unsigned"),
            "unexpected signing policy")
    require(manifest["capabilities"] == {"dynamic_library_plugins": system == "windows",
            "dynamic_library_plugins_tested": False,
            "parent_monitor": True, "runtime_control": True, "ephemeral_api_key": True},
            "unexpected runtime capabilities")
    for field, name, limit in (("binary", binary_name(target), MAX_BINARY),
                               ("license", "LICENSE", MAX_METADATA),
                               ("archive", archive_name(manifest["version"], target), MAX_BINARY)):
        entry = manifest[field]
        require(set(entry) == {"file", "sha256", "size"}, f"invalid {field} fields")
        require(entry["file"] == name, f"unsafe/unexpected {field} filename")
        require(isinstance(entry["sha256"], str) and bool(HEX64.fullmatch(entry["sha256"])),
                f"invalid {field} SHA256")
        require(type(entry["size"]) is int and 0 < entry["size"] <= limit,
                f"invalid {field} size")
    return target


def file_record(name: str, data: bytes) -> dict:
    return {"file": name, "sha256": digest(data), "size": len(data)}


def write_archive(path: Path, members: dict[str, bytes], executable: str) -> None:
    """Deterministic archive metadata and explicit permissions on all platforms."""
    if path.suffix == ".zip":
        with zipfile.ZipFile(path, "x", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as out:
            for name in sorted(members):
                entry = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
                entry.create_system = 3
                entry.external_attr = (stat.S_IFREG | (0o755 if name == executable else 0o644)) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                out.writestr(entry, members[name])
    else:
        with path.open("xb") as raw:
            with gzip.GzipFile(filename="", fileobj=raw, mode="wb", mtime=0) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as out:
                    for name in sorted(members):
                        entry = tarfile.TarInfo(name)
                        entry.size = len(members[name])
                        entry.mode = 0o755 if name == executable else 0o644
                        out.addfile(entry, io.BytesIO(members[name]))


def read_archive(path: Path, executable: str) -> dict[str, bytes]:
    allowed = {executable: MAX_BINARY, "LICENSE": MAX_METADATA, "runtime.json": MAX_METADATA}
    result = {}

    def read_member(name, size, mode, regular, reader):
        require(name in allowed and name not in result, f"unexpected or duplicate archive entry: {name}")
        require(regular and 0 < size <= allowed[name], f"unsafe archive member: {name}")
        require(mode & 0o7777 == (0o755 if name == executable else 0o644),
                f"incorrect archive permissions: {name}")
        with reader() as stream:
            value = stream.read(size + 1)
        require(len(value) == size, f"truncated/oversized archive member: {name}")
        result[name] = value

    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as archive:
            require(len(archive.infolist()) == 3, "archive must contain exactly three files")
            for entry in archive.infolist():
                mode = entry.external_attr >> 16
                read_member(entry.filename, entry.file_size, mode, stat.S_ISREG(mode),
                            lambda entry=entry: archive.open(entry))
    else:
        with tarfile.open(path, "r:gz") as archive:
            # Stream headers; do not extract, follow links, or allocate all members first.
            for entry in archive:
                read_member(entry.name, entry.size, entry.mode, entry.isreg(),
                            lambda entry=entry: archive.extractfile(entry))
    require(set(result) == set(allowed), "archive is incomplete")
    return result


def verify(manifest_path: Path, expected_commit: str | None = None,
           expected_sha256: str | None = None) -> tuple[dict, bytes]:
    require(manifest_path.stat().st_size <= MAX_METADATA, "manifest too large")
    manifest = read_json(manifest_path.read_bytes())
    target = validate_manifest(manifest)
    if expected_commit is not None:
        require(bool(HEX40.fullmatch(expected_commit)) and manifest["source"]["commit"] == expected_commit,
                "runtime source commit does not match the approved lock")
    if expected_sha256 is not None:
        require(bool(HEX64.fullmatch(expected_sha256)) and manifest["binary"]["sha256"] == expected_sha256,
                "binary SHA256 does not match the approved lock")
    archive = manifest_path.parent / manifest["archive"]["file"]
    require(not archive.is_symlink() and archive.is_file(), "archive must be a regular file")
    require(archive.stat().st_size == manifest["archive"]["size"], "archive size mismatch")
    require(digest(archive.read_bytes()) == manifest["archive"]["sha256"], "archive checksum mismatch")
    members = read_archive(archive, binary_name(target))
    embedded = copy.deepcopy(manifest)
    del embedded["archive"]
    require(read_json(members["runtime.json"]) == embedded, "embedded runtime metadata mismatch")
    for field in ("binary", "license"):
        require(file_record(manifest[field]["file"], members[manifest[field]["file"]]) == manifest[field],
                f"{field} checksum/size mismatch")
    binary = members[manifest["binary"]["file"]]
    validate_binary(binary, target)
    return manifest, binary


def package(binary: Path, license_path: Path, output: Path, metadata: dict) -> Path:
    require(not output.exists(), f"refusing to overwrite output directory: {output}")
    data = binary.read_bytes()
    validate_binary(data, metadata["target"])
    manifest = copy.deepcopy(metadata)
    # Normalize Git's Windows checkout line endings to the canonical license text.
    license_data = license_path.read_bytes().replace(b"\r\n", b"\n")
    require(0 < len(license_data) <= MAX_METADATA, "missing/oversized license")
    manifest["binary"] = file_record(binary_name(manifest["target"]), data)
    manifest["license"] = file_record("LICENSE", license_data)
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".runtime-", dir=output.parent) as work:
        staging = Path(work) / "ready"
        staging.mkdir()
        archive = staging / archive_name(manifest["version"], manifest["target"])
        write_archive(archive, {manifest["binary"]["file"]: data, "LICENSE": license_data,
                               "runtime.json": encoded(manifest)}, manifest["binary"]["file"])
        manifest["archive"] = file_record(archive.name, archive.read_bytes())
        destination = staging / (archive.name + ".manifest.json")
        destination.write_bytes(encoded(manifest))
        verify(destination)
        os.rename(staging, output)
    return output / destination.name


def build(root: Path, output: Path, target: str, version: str) -> Path:
    system, arch = target_parts(target)
    root = root.resolve()
    require(Path(run("git", "rev-parse", "--show-toplevel", cwd=root)).resolve() == root,
            "--root must name the repository root")
    run("git", "diff", "--exit-code", "HEAD", "--", cwd=root)
    require(not run("git", "ls-files", "--others", "--exclude-standard", cwd=root),
            "untracked source files found; commit or remove them before building")
    commit = run("git", "rev-parse", "HEAD", cwd=root)
    require(bool(HEX40.fullmatch(commit)), "source must be a full Git SHA")
    stamp = datetime.fromtimestamp(int(run("git", "show", "-s", "--format=%ct", "HEAD", cwd=root)), timezone.utc)
    commit_time = stamp.strftime("%Y-%m-%dT%H:%M:%SZ")
    version = version or stamp.strftime("%Y%m%d%H%M%S") + "-" + commit[:12]
    archive_name(version, target)
    # Force the embedded toolchain, not an implicit download or a user's GOFLAGS.
    env = dict(os.environ, CGO_ENABLED="0", GOOS=system, GOARCH=arch, GOAMD64="v1",
               GOARM64="v8.0", GOFLAGS="", GOEXPERIMENT="", GOTOOLCHAIN="local",
               GOWORK="off", GOENV="off")
    go_version = run("go", "env", "GOVERSION", env=env)
    required = re.search(r"^go\s+(\S+)\s*$", (root / "go.mod").read_text(), re.MULTILINE)
    require(required is not None and go_version == "go" + required.group(1),
            "use exactly the Go version in go.mod to keep lock hashes reproducible")
    metadata = {"schema_version": 1, "component": "cliproxyapi", "profile": PROFILE,
                "version": version, "target": target, "host_contract_version": "1",
                "source": {"repository": REPOSITORY, "commit": commit, "commit_time": commit_time},
                "build": {"go_version": go_version, "cgo_enabled": False, "trimpath": True,
                          "buildvcs": False, "signing": "ad-hoc" if system == "darwin" else "unsigned"},
                "capabilities": {"dynamic_library_plugins": system == "windows",
                                 "dynamic_library_plugins_tested": False, "parent_monitor": True,
                                 "runtime_control": True, "ephemeral_api_key": True}}
    with tempfile.TemporaryDirectory(prefix="pinable-runtime-build-") as work:
        binary = Path(work) / binary_name(target)
        flags = f"-s -w -X main.Version={version} -X main.Commit={commit} -X main.BuildDate={commit_time}"
        run("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=" + flags,
            "-o", str(binary), "./cmd/server", cwd=root, env=env)
        if system == "darwin":
            require(sys.platform == "darwin", "macOS artifacts require a native macOS signing host")
            run("codesign", "--force", "--sign", "-", "--timestamp=none", "--identifier",
                "cc.pinable.cliproxyapi", str(binary))
            run("codesign", "--verify", "--strict", str(binary))
        return package(binary, root / "LICENSE", output.resolve(), metadata)


def stage(manifest_path: Path, destination: Path, expected_commit: str, expected_sha256: str) -> None:
    manifest, binary = verify(manifest_path, expected_commit, expected_sha256)
    require(destination.is_absolute(), "--output must be an absolute binary path")
    require(destination.name == manifest["binary"]["file"], "output filename must match the executable")
    require(not destination.exists() and not destination.is_symlink(), "refusing to replace an existing runtime")
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Exclusive creation prevents overwriting a running or concurrently installed binary.
    with destination.open("xb") as out:
        out.write(binary)
    destination.chmod(0o755)
    require(digest(destination.read_bytes()) == expected_sha256, "staged binary checksum mismatch")


def index(input_dir: Path, output: Path, expected_commit: str) -> dict:
    require(bool(HEX40.fullmatch(expected_commit)), "index requires an approved full source commit")
    paths = sorted(input_dir.rglob("*.manifest.json"))
    require(len(paths) == len(TARGETS), "index requires exactly six target manifests")
    manifests = [verify(path, expected_commit)[0] for path in paths]
    require({m["target"] for m in manifests} == set(TARGETS), "missing/duplicate platform")
    identity = lambda m: (m["version"], m["profile"], m["source"], m["build"]["go_version"], m["license"])
    require(all(identity(m) == identity(manifests[0]) for m in manifests), "mixed release metadata")
    require(not output.exists(), "refusing to overwrite runtime index output")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".runtime-index-", dir=output.parent) as temp:
        staging = Path(temp) / "ready"
        staging.mkdir()
        for path, manifest in zip(paths, manifests):
            for source in (path, path.parent / manifest["archive"]["file"]):
                shutil.copyfile(source, staging / source.name)
        result = {"schema_version": 1, "component": "cliproxyapi", "version": manifests[0]["version"],
                  "source": manifests[0]["source"], "profile": PROFILE,
                  "targets": sorted(manifests, key=lambda m: m["target"])}
        (staging / "runtime-index.json").write_bytes(encoded(result))
        sums = "".join(f"{digest(path.read_bytes())}  {path.name}\n" for path in sorted(staging.iterdir()))
        (staging / "SHA256SUMS").write_text(sums, encoding="utf-8")
        os.rename(staging, output)
    return result


def main(argv=None) -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("build")
    p.add_argument("--root", type=Path, default=Path.cwd())
    p.add_argument("--target", choices=TARGETS, required=True)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--version", default="")
    p = sub.add_parser("verify")
    p.add_argument("--manifest", type=Path, required=True)
    p.add_argument("--expected-commit")
    p.add_argument("--expected-sha256")
    p = sub.add_parser("stage")
    p.add_argument("--manifest", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--expected-commit", required=True)
    p.add_argument("--expected-sha256", required=True)
    p = sub.add_parser("index")
    p.add_argument("--input", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--expected-commit", required=True)
    args = parser.parse_args(argv)
    if args.command == "build":
        print(build(args.root, args.output, args.target, args.version))
    elif args.command == "verify":
        manifest, _ = verify(args.manifest, args.expected_commit, args.expected_sha256)
        print(f"Verified {manifest['target']} at {manifest['source']['commit']}")
    elif args.command == "stage":
        stage(args.manifest, args.output, args.expected_commit, args.expected_sha256)
        print(args.output)
    else:
        result = index(args.input, args.output, args.expected_commit)
        print(f"Verified release index: {len(result['targets'])} targets")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, struct.error, tarfile.TarError,
            zipfile.BadZipFile, subprocess.CalledProcessError) as error:
        print(f"pinable-runtime: {error}", file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError):
            print(error.stderr or "", file=sys.stderr)
        sys.exit(1)
