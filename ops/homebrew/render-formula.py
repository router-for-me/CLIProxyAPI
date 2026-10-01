#!/usr/bin/env python3
"""Render a reviewed formula from the release's complete checksum manifest."""

import argparse
import re
import sys
from pathlib import Path


def render(tag: str, manifest: str, template: str) -> str:
    core = r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    if not re.fullmatch(rf"v{core}(-upstream{core})?", tag, re.ASCII):
        raise ValueError(
            "expected a vX.Y.Z tag with an optional -upstreamX.Y.Z suffix"
        )
    version = tag[1:]

    # The version is declared explicitly in the template. Substitute it rather
    # than leaving the placeholder, so a formula never ships a stale version.
    declared = re.compile(r'^(  version )"[^"]*"$', re.MULTILINE)
    if not declared.search(template):
        raise ValueError("template is missing the explicit version declaration")
    template = declared.sub(lambda _match: f'  version "{version}"', template, count=1)

    checksums = {}
    for line in manifest.splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r"([a-fA-F0-9]{64}) [ *](\S+)", line)
        if not match:
            raise ValueError("invalid checksum manifest line")
        digest, name = match.groups()
        if name in checksums:
            raise ValueError(f"duplicate checksum: {name}")
        checksums[name] = digest.lower()
    pattern = re.compile(
        r'url "https://github.com/hrygo/CLIProxyAPI/releases/download/[^"/]+/'
        r'CLIProxyAPI_[^"/]+_(darwin|linux)_(aarch64|amd64)\.tar\.gz"\n'
        r'(\s*)sha256 "[^"]+"'
    )
    targets = {
        "darwin_aarch64", "darwin_amd64", "linux_aarch64", "linux_amd64",
    }
    found = [f"{match[1]}_{match[2]}" for match in pattern.finditer(template)]
    if len(found) != 4 or set(found) != targets:
        raise ValueError("expected four platform URL/checksum pairs")

    def replace(match):
        target = f"{match[1]}_{match[2]}"
        filename = f"CLIProxyAPI_{version}_{target}.tar.gz"
        if filename not in checksums:
            raise ValueError(f"missing checksum: {filename}")
        return (
            f'url "https://github.com/hrygo/CLIProxyAPI/releases/download/{tag}/{filename}"\n'
            f'{match[3]}sha256 "{checksums[filename]}"'
        )

    return pattern.sub(replace, template)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("tag")
    parser.add_argument("checksums", type=Path)
    args = parser.parse_args()
    try:
        template = Path(__file__).with_name("cli-proxy-api.rb").read_text()
        result = render(args.tag, args.checksums.read_text(), template)
    except (OSError, ValueError) as error:
        parser.exit(1, f"error: {error}\n")
    sys.stdout.write(result)


if __name__ == "__main__":
    main()
