"""OSINT tools: investigate usernames, phones, and domains.

Wraps maigret, PhoneInfoga, and theHarvester as subprocess calls.
Each tool sanitises input, runs with a timeout, and parses output.
"""

import asyncio
import json
import logging
import os
import re
import shutil
import tempfile

from agent import tool

LOGGER = logging.getLogger("shadow_claw_gateway.tools.osint")

_SUBPROCESS_TIMEOUT = 120  # seconds
_OSINT_DIR = os.path.join(tempfile.gettempdir(), "shadow_claw_osint")

# ---------------------------------------------------------------------------
# Input validation
# ---------------------------------------------------------------------------

_RE_USERNAME = re.compile(r"^[a-zA-Z0-9_.@-]{1,64}$")
_RE_PHONE = re.compile(r"^\+?[0-9]{7,15}$")
_RE_DOMAIN = re.compile(
    r"^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$"
)


def _validate_username(username: str) -> str | None:
    username = username.strip().lstrip("@")
    if not _RE_USERNAME.match(username):
        return None
    return username


def _validate_phone(number: str) -> str | None:
    number = number.strip().replace(" ", "").replace("-", "").replace("(", "").replace(")", "")
    if not _RE_PHONE.match(number):
        return None
    return number


def _validate_domain(domain: str) -> str | None:
    domain = domain.strip().lower()
    if domain.startswith("http://") or domain.startswith("https://"):
        from urllib.parse import urlparse
        domain = urlparse(domain).hostname or ""
    if not _RE_DOMAIN.match(domain):
        return None
    return domain


def _ensure_dir(subdir: str = "") -> str:
    path = os.path.join(_OSINT_DIR, subdir) if subdir else _OSINT_DIR
    os.makedirs(path, exist_ok=True)
    return path


# ---------------------------------------------------------------------------
# Subprocess runner
# ---------------------------------------------------------------------------

async def _run_cmd(cmd: list[str], timeout: int = _SUBPROCESS_TIMEOUT) -> tuple[int, str, str]:
    """Run a command asynchronously with timeout. Returns (returncode, stdout, stderr)."""
    try:
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
        return (
            proc.returncode or 0,
            stdout.decode("utf-8", errors="replace"),
            stderr.decode("utf-8", errors="replace"),
        )
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        return -1, "", f"Command timed out after {timeout}s"
    except FileNotFoundError:
        return -1, "", f"Command not found: {cmd[0]}. Install it first."


# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------

@tool(
    "osint_username",
    "Investigate a username across 3000+ social networks. "
    "Returns a list of found profiles and optionally generates a PDF report.",
    {
        "type": "object",
        "properties": {
            "username": {
                "type": "string",
                "description": "Username to investigate (e.g., 'johndoe')",
            },
        },
        "required": ["username"],
    },
)
async def osint_username(username: str) -> str:
    clean = _validate_username(username)
    if not clean:
        return "Invalid username. Use only letters, numbers, dots, underscores, and hyphens (max 64 chars)."

    if not shutil.which("maigret"):
        return "maigret not installed. Run: pip install maigret"

    outdir = _ensure_dir(f"maigret_{clean}")
    cmd = ["maigret", clean, "--pdf", "-o", outdir, "--timeout", "30"]

    LOGGER.info("Running maigret for username: %s", clean)
    rc, stdout, stderr = await _run_cmd(cmd)

    if rc == -1:
        return stderr

    # Parse output — maigret prints found accounts to stdout
    lines = stdout.strip().split("\n")
    found = [l for l in lines if "[+]" in l or "http" in l.lower()]

    # Check for PDF
    pdf_files = [f for f in os.listdir(outdir) if f.endswith(".pdf")] if os.path.isdir(outdir) else []
    pdf_path = os.path.join(outdir, pdf_files[0]) if pdf_files else None

    summary = f"OSINT report for @{clean}:\n\n"
    if found:
        summary += f"Found {len(found)} profiles:\n"
        summary += "\n".join(found[:20])  # cap at 20 for Telegram
        if len(found) > 20:
            summary += f"\n... and {len(found) - 20} more."
    else:
        summary += "No profiles found."

    if pdf_path:
        summary += f"\n\n[PDF report saved: {pdf_path}]"

    return summary


@tool(
    "osint_phone",
    "Look up information about a phone number: carrier, country, "
    "area, and linked social media accounts.",
    {
        "type": "object",
        "properties": {
            "number": {
                "type": "string",
                "description": "Phone number with country code (e.g., '+5511999999999')",
            },
        },
        "required": ["number"],
    },
)
async def osint_phone(number: str) -> str:
    clean = _validate_phone(number)
    if not clean:
        return "Invalid phone number. Use international format: +CCNNNNNNNNN (7-15 digits)."

    if not shutil.which("phoneinfoga"):
        return "phoneinfoga not installed. Download from: https://github.com/sundowndev/phoneinfoga/releases"

    cmd = ["phoneinfoga", "scan", "-n", clean]

    LOGGER.info("Running phoneinfoga for number: %s", clean[:6] + "***")
    rc, stdout, stderr = await _run_cmd(cmd, timeout=60)

    if rc == -1:
        return stderr

    if not stdout.strip():
        return f"No information found for {clean}."

    # Truncate if too long
    output = stdout.strip()
    if len(output) > 3000:
        output = output[:3000] + "\n... (truncated)"

    return f"Phone OSINT for {clean}:\n\n{output}"


@tool(
    "osint_domain",
    "Harvest emails, subdomains, and employee names from a domain. "
    "Useful for investigating companies or organizations.",
    {
        "type": "object",
        "properties": {
            "domain": {
                "type": "string",
                "description": "Domain to investigate (e.g., 'example.com')",
            },
        },
        "required": ["domain"],
    },
)
async def osint_domain(domain: str) -> str:
    clean = _validate_domain(domain)
    if not clean:
        return "Invalid domain. Provide a valid domain name (e.g., 'example.com')."

    if not shutil.which("theHarvester"):
        return "theHarvester not installed. Run: pip install theHarvester"

    outdir = _ensure_dir(f"harvester_{clean}")
    outfile = os.path.join(outdir, "results")
    cmd = [
        "theHarvester",
        "-d", clean,
        "-b", "duckduckgo,crtsh,rapiddns,urlscan",
        "-f", outfile,
    ]

    LOGGER.info("Running theHarvester for domain: %s", clean)
    rc, stdout, stderr = await _run_cmd(cmd)

    if rc == -1:
        return stderr

    # Try to read JSON results
    json_path = outfile + ".json"
    if os.path.exists(json_path):
        try:
            with open(json_path) as f:
                data = json.load(f)
            emails = data.get("emails", [])
            hosts = data.get("hosts", [])
            ips = data.get("ips", [])

            parts = [f"Domain OSINT for {clean}:\n"]
            if emails:
                parts.append(f"Emails ({len(emails)}):")
                parts.extend(f"  - {e}" for e in emails[:15])
            if hosts:
                parts.append(f"\nSubdomains ({len(hosts)}):")
                parts.extend(f"  - {h}" for h in hosts[:15])
            if ips:
                parts.append(f"\nIPs ({len(ips)}):")
                parts.extend(f"  - {ip}" for ip in ips[:10])
            return "\n".join(parts)
        except (json.JSONDecodeError, OSError):
            pass

    # Fallback: return raw stdout
    output = stdout.strip()
    if len(output) > 3000:
        output = output[:3000] + "\n... (truncated)"
    return f"Domain OSINT for {clean}:\n\n{output}" if output else f"No results for {clean}."
