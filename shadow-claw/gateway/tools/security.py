"""Security tools: basic web security scanning.

Inspired by KeygraphHQ/shannon. Performs non-invasive security checks
like HTTP header analysis and basic vulnerability detection.
"""

import json
import logging

import requests

from agent import tool
from tools.browser import _is_private_url

LOGGER = logging.getLogger("shadow_claw_gateway.tools.security")

_REQUEST_TIMEOUT = 15

_SECURITY_HEADERS = [
    "Strict-Transport-Security",
    "Content-Security-Policy",
    "X-Content-Type-Options",
    "X-Frame-Options",
    "X-XSS-Protection",
    "Referrer-Policy",
    "Permissions-Policy",
]


@tool(
    "security_headers",
    "Check HTTP security headers of a website. "
    "Identifies missing security headers and rates the configuration.",
    {
        "type": "object",
        "properties": {
            "url": {
                "type": "string",
                "description": "URL to check security headers for",
            },
        },
        "required": ["url"],
    },
)
async def security_headers(url: str) -> str:
    if _is_private_url(url):
        return "URL blocked: cannot scan internal/private addresses."

    try:
        resp = requests.head(
            url,
            timeout=_REQUEST_TIMEOUT,
            headers={"User-Agent": "Shadow-Claw-Security/1.0"},
            allow_redirects=True,
        )
    except requests.RequestException as e:
        return f"Error connecting to {url}: {e}"

    present = []
    missing = []
    for header in _SECURITY_HEADERS:
        value = resp.headers.get(header)
        if value:
            present.append(f"  + {header}: {value}")
        else:
            missing.append(f"  - {header}: MISSING")

    score = len(present) / len(_SECURITY_HEADERS) * 100

    lines = [
        f"Security Headers Report for {url}",
        f"Score: {score:.0f}% ({len(present)}/{len(_SECURITY_HEADERS)} headers present)",
        "",
        "Present:",
    ]
    lines.extend(present or ["  (none)"])
    lines.append("\nMissing:")
    lines.extend(missing or ["  (none)"])

    return "\n".join(lines)


@tool(
    "security_scan",
    "Perform a basic non-invasive security scan of a web target. "
    "Checks headers, server info, and common misconfigurations.",
    {
        "type": "object",
        "properties": {
            "target_url": {
                "type": "string",
                "description": "URL to scan",
            },
        },
        "required": ["target_url"],
    },
)
async def security_scan(target_url: str) -> str:
    if _is_private_url(target_url):
        return "URL blocked: cannot scan internal/private addresses."

    findings = []

    try:
        resp = requests.get(
            target_url,
            timeout=_REQUEST_TIMEOUT,
            headers={"User-Agent": "Shadow-Claw-Security/1.0"},
            allow_redirects=True,
        )
    except requests.RequestException as e:
        return f"Error connecting to {target_url}: {e}"

    # Check server header disclosure
    server = resp.headers.get("Server", "")
    if server:
        findings.append(f"[INFO] Server header disclosed: {server}")

    # Check X-Powered-By
    powered = resp.headers.get("X-Powered-By", "")
    if powered:
        findings.append(f"[WARN] X-Powered-By disclosed: {powered}")

    # Check HTTPS
    if not target_url.startswith("https"):
        findings.append("[WARN] Site not using HTTPS")

    # Check missing security headers
    for header in _SECURITY_HEADERS:
        if not resp.headers.get(header):
            findings.append(f"[WARN] Missing security header: {header}")

    # Check cookies without secure flag
    for cookie in resp.cookies:
        if not cookie.secure:
            findings.append(f"[WARN] Cookie '{cookie.name}' missing Secure flag")
        if "httponly" not in str(cookie._rest).lower():
            findings.append(f"[INFO] Cookie '{cookie.name}' missing HttpOnly flag")

    if not findings:
        return f"No issues found for {target_url}. Basic scan passed."

    lines = [f"Security Scan Report for {target_url}", f"Findings ({len(findings)}):"]
    lines.extend(findings)
    return "\n".join(lines)
