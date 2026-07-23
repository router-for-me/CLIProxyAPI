#!/usr/bin/env python3
"""Audit CLIProxyAPI outbound network destinations without reading secret values.

The audit has two complementary modes:

* static: scan production Go/config text for literal HTTP(S)/WS(S) destinations;
* runtime: sample the live container network namespace via /proc/<pid>/net/tcp*.

It deliberately reports destination metadata only. It never prints request bodies,
headers, API keys, OAuth tokens, or configuration secret values.
"""

from __future__ import annotations

import argparse
import ipaddress
import json
from pathlib import Path
import re
import socket
import struct
import subprocess
import time
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from typing import Iterable
from urllib.parse import urlsplit

URL_RE = re.compile(r"(?:https?|wss?)://[^\s\"'`<>\\)]+")
SKIP_DIRS = {".git", "vendor", "examples", "test", "tests"}
SKIP_SUFFIXES = ("_test.go",)

KNOWN_PURPOSES = {
    "api.openai.com": "OpenAI API provider (prompts/files are intentionally forwarded when configured)",
    "chatgpt.com": "OpenAI/Codex OAuth API and model catalogue",
    "auth.openai.com": "OpenAI OAuth authentication",
    "platform.openai.com": "OpenAI OAuth success redirect/documentation",
    "api.anthropic.com": "Anthropic API provider (prompts are intentionally forwarded when configured)",
    "claude.ai": "Claude OAuth/API provider",
    "console.anthropic.com": "Anthropic OAuth success redirect/documentation",
    "docs.anthropic.com": "Anthropic documentation link embedded in provider error text",
    "generativelanguage.googleapis.com": "Google Gemini API provider (prompts are intentionally forwarded when configured)",
    "aiplatform.googleapis.com": "Google Vertex AI provider (prompts are intentionally forwarded when configured)",
    "oauth2.googleapis.com": "Google OAuth token exchange",
    "accounts.google.com": "Google OAuth authentication",
    "www.googleapis.com": "Google OAuth/user/project APIs",
    "cloudcode-pa.googleapis.com": "Google Antigravity provider/model catalogue",
    "daily-cloudcode-pa.googleapis.com": "Google Antigravity daily endpoint",
    "daily-cloudcode-pa.sandbox.googleapis.com": "Google Antigravity sandbox endpoint",
    "api.x.ai": "xAI API provider (prompts/media are intentionally forwarded when configured)",
    "auth.x.ai": "xAI OAuth authentication",
    "cli-chat-proxy.grok.com": "xAI CLI chat provider (prompts are intentionally forwarded when configured)",
    "api.kimi.com": "Kimi API provider (prompts are intentionally forwarded when configured)",
    "auth.kimi.com": "Kimi OAuth authentication",
    "raw.githubusercontent.com": "Remote model registry, plugin registry, or management assets",
    "github.com": "Source/plugin release metadata or downloads",
    "api.github.com": "GitHub release metadata or plugin downloads",
    "models.router-for.me": "CLIProxyAPI remote model registry fallback",
    "cpamc.router-for.me": "Management panel fallback download",
    "antigravity-hub-auto-updater-974169037036.us-central1.run.app": "Antigravity client-version manifest lookup",
    "api.ipify.org": "Public-IP discovery used by SSH/OAuth helper",
    "ifconfig.me": "Public-IP discovery used by SSH/OAuth helper",
    "icanhazip.com": "Public-IP discovery used by SSH/OAuth helper",
    "ipinfo.io": "Public-IP discovery used by SSH/OAuth helper",
}

PROVIDER_SUFFIXES = (
    "openai.com",
    "anthropic.com",
    "claude.ai",
    "googleapis.com",
    "x.ai",
    "grok.com",
    "kimi.com",
)

# URL-like strings in comments, generated HTML/SVG, schema identifiers, or
# format templates are not necessarily network destinations.
NON_NETWORK_HOSTS = {"host", "json-schema.org", "www.w3.org"}


@dataclass(frozen=True)
class StaticFinding:
    host: str
    scheme: str
    endpoint: str
    source: str
    line: int
    purpose: str
    category: str


@dataclass(frozen=True)
class RuntimeFinding:
    observed_at: str
    protocol: str
    state: str
    direction: str
    local_ip: str
    local_port: int
    remote_ip: str
    remote_port: int
    hostnames: tuple[str, ...]
    scope: str


def classify_host(host: str) -> tuple[str, str]:
    host = host.lower().rstrip(".")
    purpose = KNOWN_PURPOSES.get(host, "Unclassified external destination; inspect the source location")
    if host in {"localhost", "127.0.0.1", "::1"}:
        return "local", "Local callback/service"
    try:
        ip = ipaddress.ip_address(host)
    except ValueError:
        ip = None
    if ip and (ip.is_private or ip.is_loopback or ip.is_link_local):
        return "private", "Private/local network destination"
    if any(host == suffix or host.endswith("." + suffix) for suffix in PROVIDER_SUFFIXES):
        return "provider", purpose
    if host in KNOWN_PURPOSES:
        return "support", purpose
    return "unknown", purpose


def production_files(repo: Path) -> Iterable[Path]:
    for path in repo.rglob("*.go"):
        rel = path.relative_to(repo)
        if any(part in SKIP_DIRS for part in rel.parts):
            continue
        if path.name.endswith(SKIP_SUFFIXES):
            continue
        yield path


def clean_url(raw: str) -> str:
    return raw.rstrip(".,;:]}>")


def is_network_candidate(path: Path, line: str, url: str) -> bool:
    parsed = urlsplit(url)
    host = (parsed.hostname or "").lower()
    if not host or host in NON_NETWORK_HOSTS or "%" in host:
        return False
    stripped = line.lstrip()
    if stripped.startswith(("//", "#")):
        return False
    if path.suffix.lower() in {".html", ".svg"}:
        return False
    return True


def sanitized_endpoint(url: str) -> str:
    parsed = urlsplit(url)
    host = parsed.hostname or ""
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    try:
        parsed_port = parsed.port
    except ValueError:
        parsed_port = None
    port = f":{parsed_port}" if parsed_port else ""
    return f"{parsed.scheme}://{host}{port}"


def strip_inline_comment(path: Path, line: str) -> str:
    if path.suffix.lower() in {".yaml", ".yml"}:
        in_single = False
        in_double = False
        escaped = False
        for index, char in enumerate(line):
            if escaped:
                escaped = False
                continue
            if char == "\\" and in_double:
                escaped = True
                continue
            if char == "'" and not in_double:
                in_single = not in_single
            elif char == '"' and not in_single:
                in_double = not in_double
            elif char == "#" and not in_single and not in_double:
                return line[:index]
    return line


def strip_go_comments(lines: list[str]) -> list[str]:
    output: list[str] = []
    in_block = False
    in_string = ""
    escaped = False
    for line in lines:
        chars: list[str] = []
        index = 0
        while index < len(line):
            char = line[index]
            next_char = line[index + 1] if index + 1 < len(line) else ""
            if in_block:
                if char == "*" and next_char == "/":
                    in_block = False
                    index += 2
                else:
                    index += 1
                continue
            if in_string:
                chars.append(char)
                if in_string == "`":
                    if char == "`":
                        in_string = ""
                elif escaped:
                    escaped = False
                elif char == "\\":
                    escaped = True
                elif char == in_string:
                    in_string = ""
                index += 1
                continue
            if char == "/" and next_char == "/":
                break
            if char == "/" and next_char == "*":
                in_block = True
                index += 2
                continue
            if char in {'"', "'", "`"}:
                in_string = char
            chars.append(char)
            index += 1
        output.append("".join(chars))
    return output


def scan_static(repo: Path, config: Path | None) -> list[StaticFinding]:
    files = list(production_files(repo))
    if config and config.exists():
        files.append(config)
    findings: set[StaticFinding] = set()
    for path in files:
        try:
            lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError:
            continue
        if path.suffix.lower() == ".go":
            lines = strip_go_comments(lines)
        for number, line in enumerate(lines, 1):
            scan_line = strip_inline_comment(path, line)
            for match in URL_RE.finditer(scan_line):
                url = clean_url(match.group(0))
                if not is_network_candidate(path, scan_line, url):
                    continue
                parsed = urlsplit(url)
                host = (parsed.hostname or "").lower()
                if not host:
                    continue
                category, purpose = classify_host(host)
                findings.add(
                    StaticFinding(
                        host=host,
                        scheme=parsed.scheme,
                        endpoint=sanitized_endpoint(url),
                        source=str(path.relative_to(repo)) if path.is_relative_to(repo) else str(path),
                        line=number,
                        purpose=purpose,
                        category=category,
                    )
                )
    return sorted(findings, key=lambda item: (item.category, item.host, item.source, item.line, item.endpoint))


def container_pid(container: str) -> int:
    result = subprocess.run(
        ["docker", "inspect", "--format", "{{.State.Pid}}", container],
        check=True,
        capture_output=True,
        text=True,
    )
    pid = int(result.stdout.strip())
    if pid <= 0:
        raise RuntimeError(f"container {container!r} is not running")
    return pid


def decode_ipv4(value: str) -> str:
    return socket.inet_ntop(socket.AF_INET, struct.pack("<I", int(value, 16)))


def decode_ipv6(value: str) -> str:
    packed = bytes.fromhex(value)
    # Linux /proc renders each 32-bit word in host order.
    packed = b"".join(packed[i : i + 4][::-1] for i in range(0, 16, 4))
    return socket.inet_ntop(socket.AF_INET6, packed)


def connection_scope(ip_text: str) -> str:
    ip = ipaddress.ip_address(ip_text)
    if ip.is_loopback:
        return "loopback"
    if ip.is_private:
        return "private"
    if ip.is_link_local:
        return "link-local"
    return "public"


def reverse_names(ip_text: str) -> tuple[str, ...]:
    try:
        primary, aliases, _ = socket.gethostbyaddr(ip_text)
    except (socket.herror, socket.gaierror, TimeoutError, OSError):
        return ()
    return tuple(sorted({primary.lower().rstrip("."), *(name.lower().rstrip(".") for name in aliases)}))


def owned_socket_inodes(pid: int) -> set[str] | None:
    inodes: set[str] = set()
    fd_dir = Path(f"/proc/{pid}/fd")
    try:
        entries = list(fd_dir.iterdir())
    except OSError:
        return None
    for entry in entries:
        try:
            target = entry.readlink().as_posix()
        except OSError:
            continue
        if target.startswith("socket:[") and target.endswith("]"):
            inodes.add(target[8:-1])
    return inodes


def read_proc_connections(pid: int, resolve_dns: bool) -> list[RuntimeFinding]:
    states = {
        "01": "ESTABLISHED",
        "02": "SYN_SENT",
        "03": "SYN_RECV",
        "04": "FIN_WAIT1",
        "05": "FIN_WAIT2",
        "06": "TIME_WAIT",
        "07": "CLOSE",
        "08": "CLOSE_WAIT",
        "09": "LAST_ACK",
        "0A": "LISTEN",
        "0B": "CLOSING",
    }
    observed = datetime.now(timezone.utc).isoformat()
    findings: list[RuntimeFinding] = []
    owned_inodes = owned_socket_inodes(pid)
    for name, decoder, proto in (("tcp", decode_ipv4, "tcp4"), ("tcp6", decode_ipv6, "tcp6")):
        path = Path(f"/proc/{pid}/net/{name}")
        try:
            rows = [row.split() for row in path.read_text().splitlines()[1:]]
        except OSError:
            continue
        listening_ports = {
            int(fields[1].split(":")[1], 16)
            for fields in rows
            if fields[3] == "0A"
        }
        for fields in rows:
            local_hex, remote_hex, state_hex = fields[1], fields[2], fields[3]
            inode = fields[9] if len(fields) > 9 else ""
            if state_hex == "0A" or (owned_inodes is not None and inode not in owned_inodes):
                continue
            local_address_hex, local_port_hex = local_hex.split(":")
            remote_address_hex, remote_port_hex = remote_hex.split(":")
            local_ip = decoder(local_address_hex)
            local_port = int(local_port_hex, 16)
            remote_ip = decoder(remote_address_hex)
            remote_port = int(remote_port_hex, 16)
            if remote_port == 0:
                continue
            direction = "inbound" if local_port in listening_ports or state_hex == "03" else "outbound-candidate"
            findings.append(
                RuntimeFinding(
                    observed_at=observed,
                    protocol=proto,
                    state=states.get(state_hex, state_hex),
                    direction=direction,
                    local_ip=local_ip,
                    local_port=local_port,
                    remote_ip=remote_ip,
                    remote_port=remote_port,
                    hostnames=reverse_names(remote_ip) if resolve_dns else (),
                    scope=connection_scope(remote_ip),
                )
            )
    return findings


def sample_runtime(container: str, seconds: float, interval: float, resolve_dns: bool) -> list[RuntimeFinding]:
    pid = container_pid(container)
    deadline = time.monotonic() + max(seconds, 0)
    unique: dict[tuple[str, str, int, int, str], RuntimeFinding] = {}
    while True:
        for finding in read_proc_connections(pid, resolve_dns):
            key = (finding.direction, finding.remote_ip, finding.local_port, finding.remote_port, finding.state)
            unique.setdefault(key, finding)
        if time.monotonic() >= deadline:
            break
        time.sleep(max(interval, 0.1))
    return sorted(
        unique.values(),
        key=lambda item: (item.direction, item.scope, item.remote_ip, item.remote_port, item.state),
    )


def summarize(static: list[StaticFinding], runtime: list[RuntimeFinding]) -> dict:
    unknown_hosts = sorted({item.host for item in static if item.category == "unknown"})
    outbound_candidates = [item for item in runtime if item.direction == "outbound-candidate"]
    observed_public = sorted(
        {f"{item.remote_ip}:{item.remote_port}" for item in outbound_candidates if item.scope == "public"}
    )
    return {
        "static_destination_count": len({item.host for item in static}),
        "static_unknown_hosts": unknown_hosts,
        "runtime_peer_count": len(runtime),
        "runtime_public_outbound_candidates": observed_public,
        "limitations": [
            "Static scanning cannot discover destinations assembled entirely at runtime or supplied only through encrypted/secret configuration.",
            "Runtime /proc sampling sees TCP peer IP/port and state, not TLS hostnames, HTTP paths, headers, payloads, or transferred byte counts.",
            "Direction is inferred from listening ports and TCP state; entries are outbound candidates, not cryptographic proof of process-initiated upload.",
            "Socket rows are filtered to descriptors owned by the container init process when /proc permissions allow it; otherwise results are network-namespace scoped.",
            "Reverse DNS is optional because it creates DNS queries and can delay sampling.",
            "A quiet observation window does not prove that a dormant code path will never connect later.",
        ],
    }


def render_markdown(report: dict) -> str:
    lines = [
        "# CLIProxyAPI outbound network audit",
        "",
        f"Generated: `{report['generated_at']}`",
        f"Repository: `{report['repository']}`",
        f"Container: `{report['container']}`",
        "",
        "## Summary",
        "",
        f"- Literal destination hosts found: **{report['summary']['static_destination_count']}**",
        f"- Unclassified literal hosts: **{len(report['summary']['static_unknown_hosts'])}**",
        f"- Runtime TCP peers observed: **{report['summary']['runtime_peer_count']}**",
        f"- Public outbound candidates: **{len(report['summary']['runtime_public_outbound_candidates'])}**",
        "",
        "## Static destinations",
        "",
        "| Category | Host | Purpose | Source |",
        "|---|---|---|---|",
    ]
    grouped: dict[tuple[str, str, str], list[str]] = {}
    for item in report["static_findings"]:
        key = (item["category"], item["host"], item["purpose"])
        grouped.setdefault(key, []).append(f"`{item['source']}:{item['line']}`")
    for (category, host, purpose), sources in sorted(grouped.items()):
        source_text = ", ".join(sorted(set(sources))[:6])
        lines.append(f"| {category} | `{host}` | {purpose} | {source_text} |")
    lines.extend(
        [
            "",
            "## Runtime observations",
            "",
            "| Direction | Scope | Local | Peer | State | Reverse DNS |",
            "|---|---|---|---|---|---|",
        ]
    )
    for item in report["runtime_findings"]:
        names = ", ".join(f"`{name}`" for name in item["hostnames"]) or "—"
        lines.append(
            f"| {item['direction']} | {item['scope']} | `{item['local_ip']}:{item['local_port']}` | "
            f"`{item['remote_ip']}:{item['remote_port']}` | {item['state']} | {names} |"
        )
    if not report["runtime_findings"]:
        lines.append("| — | — | — | No non-listening TCP peer observed | — | — |")
    lines.extend(["", "## Limitations", ""])
    lines.extend(f"- {item}" for item in report["summary"]["limitations"])
    lines.append("")
    return "\n".join(lines)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--config", type=Path, default=Path("/home/francis_chiu/cliproxyapi/config.yaml"))
    parser.add_argument("--container", default="cli-proxy-api")
    parser.add_argument("--watch-seconds", type=float, default=10.0)
    parser.add_argument("--interval", type=float, default=0.25)
    parser.add_argument("--resolve-dns", action="store_true")
    parser.add_argument("--json-output", type=Path)
    parser.add_argument("--markdown-output", type=Path)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    static = scan_static(args.repo.resolve(), args.config.resolve() if args.config else None)
    runtime = sample_runtime(args.container, args.watch_seconds, args.interval, args.resolve_dns)
    report = {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "repository": str(args.repo.resolve()),
        "config": str(args.config.resolve()) if args.config else None,
        "container": args.container,
        "static_findings": [asdict(item) for item in static],
        "runtime_findings": [asdict(item) for item in runtime],
        "summary": summarize(static, runtime),
    }
    encoded = json.dumps(report, indent=2, ensure_ascii=False) + "\n"
    if args.json_output:
        args.json_output.parent.mkdir(parents=True, exist_ok=True)
        args.json_output.write_text(encoded, encoding="utf-8")
    if args.markdown_output:
        args.markdown_output.parent.mkdir(parents=True, exist_ok=True)
        args.markdown_output.write_text(render_markdown(report), encoding="utf-8")
    print(encoded, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
