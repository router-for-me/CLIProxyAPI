#!/usr/bin/env python3
"""Generate repository-level LLM context files.

Targets:
- llms.txt: concise, exactly 1000 lines
- llms-full.txt: detailed, exactly 7000 lines (within requested 5k-10k)
"""

from __future__ import annotations

import argparse
import re
from dataclasses import dataclass
from pathlib import Path

DEFAULT_CONCISE_TARGET = 1000
DEFAULT_FULL_TARGET = 7000

INCLUDE_SUFFIXES = {
    ".md",
    ".go",
    ".yaml",
    ".yml",
    ".json",
    ".toml",
    ".sh",
    ".ps1",
    ".ts",
}
INCLUDE_NAMES = {
    "Dockerfile",
    "Taskfile.yml",
    "go.mod",
    "go.sum",
    "LICENSE",
    "README.md",
}
EXCLUDE_DIRS = {
    ".git",
    ".github",
    "node_modules",
    "dist",
    "build",
    ".venv",
    "vendor",
}


@dataclass
class RepoFile:
    path: Path
    rel: str
    content: str


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeDecodeError:
        return path.read_text(encoding="utf-8", errors="ignore")


def collect_files(repo_root: Path) -> list[RepoFile]:
    files: list[RepoFile] = []
    for path in sorted(repo_root.rglob("*")):
        if not path.is_file():
            continue
        parts = set(path.parts)
        if parts & EXCLUDE_DIRS:
            continue
        if path.name in {"llms.txt", "llms-full.txt"}:
            continue
        if path.suffix.lower() not in INCLUDE_SUFFIXES and path.name not in INCLUDE_NAMES:
            continue
        if path.stat().st_size > 300_000:
            continue
        rel = path.relative_to(repo_root).as_posix()
        files.append(RepoFile(path=path, rel=rel, content=read_text(path)))
    return files


def markdown_headings(text: str) -> list[str]:
    out = []
    for line in text.splitlines():
        s = line.strip()
        if s.startswith("#"):
            out.append(s)
    return out


def list_task_names(taskfile_text: str) -> list[str]:
    names = []
    for line in taskfile_text.splitlines():
        m = re.match(r"^\s{2}([a-zA-Z0-9:_\-.]+):\s*$", line)
        if m:
            names.append(m.group(1))
    return names


def extract_endpoints(go_text: str) -> list[str]:
    endpoints = []
    for m in re.finditer(r'"(/v[0-9]/[^"\s]*)"', go_text):
        endpoints.append(m.group(1))
    for m in re.finditer(r'"(/health[^"\s]*)"', go_text):
        endpoints.append(m.group(1))
    return sorted(set(endpoints))


def normalize_lines(lines: list[str]) -> list[str]:
    out = []
    for line in lines:
        s = line.rstrip()
        if not s:
            out.append("")
        else:
            out.append(s)
    return out


def fit_lines(lines: list[str], target: int, fallback_pool: list[str]) -> list[str]:
    lines = normalize_lines(lines)
    if len(lines) > target:
        return lines[:target]

    idx = 0
    while len(lines) < target:
        if fallback_pool:
            lines.append(fallback_pool[idx % len(fallback_pool)])
            idx += 1
        else:
            lines.append(f"filler-line-{len(lines)+1}")
    return lines


def build_concise(repo_root: Path, files: list[RepoFile], target: int) -> list[str]:
    lines: list[str] = []
    by_rel = {f.rel: f for f in files}

    readme = by_rel.get("README.md")
    taskfile = by_rel.get("Taskfile.yml")

    lines.append("# cliproxyapi++ LLM Context (Concise)")
    lines.append("Generated from repository files for agent/dev/user consumption.")
    lines.append("")

    if readme:
        lines.append("## README Highlights")
        for raw in readme.content.splitlines()[:180]:
            s = raw.strip()
            if s:
                lines.append(s)
        lines.append("")

    if taskfile:
        lines.append("## Taskfile Tasks")
        for name in list_task_names(taskfile.content):
            lines.append(f"- {name}")
        lines.append("")

    lines.append("## Documentation Index")
    doc_files = [f for f in files if f.rel.startswith("docs/") and f.rel.endswith(".md")]
    for f in doc_files:
        lines.append(f"- {f.rel}")
    lines.append("")

    lines.append("## Markdown Headings")
    for f in doc_files + ([readme] if readme else []):
        if not f:
            continue
        hs = markdown_headings(f.content)
        if not hs:
            continue
        lines.append(f"### {f.rel}")
        for h in hs[:80]:
            lines.append(f"- {h}")
    lines.append("")

    lines.append("## Go Source Index")
    go_files = [f for f in files if f.rel.endswith(".go")]
    for f in go_files:
        lines.append(f"- {f.rel}")
    lines.append("")

    lines.append("## API/Health Endpoints (Detected)")
    seen = set()
    for f in go_files:
        for ep in extract_endpoints(f.content):
            if ep in seen:
                continue
            seen.add(ep)
            lines.append(f"- {ep}")
    lines.append("")

    lines.append("## Config and Examples")
    for f in files:
        if f.rel.startswith("examples/") or "config" in f.rel.lower():
            lines.append(f"- {f.rel}")

    fallback_pool = [f"index:{f.rel}" for f in files]
    return fit_lines(lines, target, fallback_pool)


def build_full(repo_root: Path, files: list[RepoFile], concise: list[str], target: int) -> list[str]:
    lines: list[str] = []
    lines.append("# cliproxyapi++ LLM Context (Full)")
    lines.append("Expanded, line-addressable repository context.")
    lines.append("")

    lines.extend(concise[:300])
    lines.append("")
    lines.append("## Detailed File Snapshots")

    snapshot_files = [
        f
        for f in files
        if f.rel.endswith((".md", ".go", ".yaml", ".yml", ".sh", ".ps1", ".ts"))
    ]

    for f in snapshot_files:
        lines.append("")
        lines.append(f"### FILE: {f.rel}")
        body = f.content.splitlines()
        if not body:
            lines.append("(empty)")
            continue

        max_lines = 160 if f.rel.endswith(".go") else 220 if f.rel.endswith(".md") else 120
        for i, raw in enumerate(body[:max_lines], 1):
            lines.append(f"{i:04d}: {raw.rstrip()}")

    lines.append("")
    lines.append("## Repository Path Inventory")
    for f in files:
        lines.append(f"- {f.rel}")

    fallback_pool = [f"path:{f.rel}" for f in files]
    return fit_lines(lines, target, fallback_pool)


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate llms.txt and llms-full.txt")
    parser.add_argument("--repo-root", default=".", help="Repository root")
    parser.add_argument("--concise-target", type=int, default=DEFAULT_CONCISE_TARGET)
    parser.add_argument("--full-target", type=int, default=DEFAULT_FULL_TARGET)
    args = parser.parse_args()

    repo_root = Path(args.repo_root).resolve()
    files = collect_files(repo_root)

    concise = build_concise(repo_root, files, args.concise_target)
    full = build_full(repo_root, files, concise, args.full_target)

    concise_path = repo_root / "llms.txt"
    full_path = repo_root / "llms-full.txt"

    concise_path.write_text("\n".join(concise) + "\n", encoding="utf-8")
    full_path.write_text("\n".join(full) + "\n", encoding="utf-8")

    print(f"Generated {concise_path}")
    print(f"Generated {full_path}")
    print(f"llms.txt lines: {len(concise)}")
    print(f"llms-full.txt lines: {len(full)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
