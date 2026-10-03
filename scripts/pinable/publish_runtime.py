#!/usr/bin/env python3
"""Explicit, immutable GitHub prerelease publication; never runs on push or PR."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

sys.path.insert(0, str(Path(__file__).parent))
import runtime


def api(path, payload=None):
    args = ["gh", "api", path]
    if payload is not None:
        args += ["--method", "POST", "--input", "-"]
    result = subprocess.run(args, input=json.dumps(payload) if payload is not None else None,
                            check=True, text=True, stdout=subprocess.PIPE)
    return json.loads(result.stdout)


def validate(directory, expected_commit):
    index = json.loads((directory / "runtime-index.json").read_text())
    runtime.require(index["source"]["commit"] == expected_commit and
                    runtime.HEX40.fullmatch(expected_commit), "source mismatch")
    paths = sorted(directory.glob("*.manifest.json"))
    manifests = [runtime.verify(p, expected_commit)[0] for p in paths]
    runtime.require(len(manifests) == 6 and {m["target"] for m in manifests} == set(runtime.TARGETS),
                    "incomplete runtime set")
    runtime.require(sorted(manifests, key=lambda m: m["target"]) == index["targets"], "index mismatch")
    runtime.require(all(m["version"] == index["version"] for m in manifests), "mixed versions")
    validation = json.loads((directory / "validation.json").read_text())
    reports = validation["native_smoke"]
    runtime.require(validation["quality"] == "passed" and validation["source_commit"] == expected_commit,
                    "quality check did not pass")
    hashes = {m["target"]: m["binary"]["sha256"] for m in manifests}
    runtime.require(len(reports) == 6 and {r["target"] for r in reports} == set(runtime.TARGETS)
                    and all(r["status"] == "passed" and r["source_commit"] == expected_commit
                            and r["binary_sha256"] == hashes[r["target"]] for r in reports),
                    "native smoke validation incomplete")
    files = {m["archive"]["file"] for m in manifests} | {p.name for p in paths}
    files |= {"runtime-index.json", "validation.json", "SHA256SUMS"}
    runtime.require({p.name for p in directory.iterdir()} == files, "unexpected release file")
    seen = set()
    for line in (directory / "SHA256SUMS").read_text().splitlines():
        checksum, name = line.split("  ", 1)
        runtime.require(name in files - {"SHA256SUMS"} and name not in seen, "unsafe/duplicate checksum entry")
        path = directory / name
        runtime.require(path.is_file() and not path.is_symlink() and
                        hashlib.sha256(path.read_bytes()).hexdigest() == checksum, "release checksum mismatch")
        seen.add(name)
    runtime.require(seen == files - {"SHA256SUMS"}, "incomplete checksums")
    return index, sorted(files)


def publish(directory, expected_commit):
    repo = "PinableAgents/CLIProxyAPI"
    runtime.require(os.environ.get("GITHUB_REPOSITORY") == repo and
                    os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch" and
                    os.environ.get("GITHUB_REF") == "refs/heads/main", "publication is manual and main-only")
    index, files = validate(directory, expected_commit)
    runtime.require(api(f"repos/{repo}/git/ref/heads/main")["object"]["sha"] == expected_commit,
                    "main moved after validation; build the new revision before publishing")
    tag = "pinable-runtime-" + index["version"]
    refs = api(f"repos/{repo}/git/matching-refs/tags/{tag}")
    runtime.require(not any(r["ref"] == "refs/tags/" + tag for r in refs),
                    "runtime tag already exists; publication never overwrites artifacts")
    notes = (f"Pinable Desktop portable runtime, source `{expected_commit}`.\n\n"
             "All six platform executables passed native host-contract smoke checks. "
             "Normal Go regression tests passed before publication.\n\n"
             "Use runtime-index.json and SHA256SUMS to approve exact binary hashes in Desktop's "
             "component lock. No lock is changed automatically. CGO=0 disables the Unix "
             "dynamic-library loader; Windows retains its DLL loader. Plugin execution is not certified "
             "by this pipeline. macOS binaries are ad-hoc signed, not Developer ID signed/notarized; Windows "
             "binaries are unsigned. This is not a Desktop application installer.")
    release = api(f"repos/{repo}/releases", {"tag_name": tag, "target_commitish": expected_commit,
                  "name": "Pinable runtime " + index["version"], "body": notes,
                  "draft": True, "prerelease": True, "make_latest": "false"})
    subprocess.run(["gh", "release", "upload", tag, "--repo", repo,
                    *[str(directory / name) for name in files]], check=True)
    subprocess.run(["gh", "api", f"repos/{repo}/releases/{release['id']}", "--method", "PATCH",
                    "--input", "-"], input=json.dumps({"draft": False, "make_latest": "false"}),
                   text=True, check=True)
    print(release["html_url"])


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--expected-commit", required=True)
    args = parser.parse_args()
    publish(args.directory.resolve(), args.expected_commit)
