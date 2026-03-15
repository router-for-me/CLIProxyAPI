"""Scraper tools: extract structured data from web pages.

Inspired by D4Vinci/Scrapling. Uses requests + BeautifulSoup for
CSS selector-based data extraction.
"""

import json
import logging
import re

import requests

from agent import tool
from tools.browser import _is_private_url

LOGGER = logging.getLogger("shadow_claw_gateway.tools.scraper")

_REQUEST_TIMEOUT = 30
_MAX_CONTENT_BYTES = 1_000_000


@tool(
    "scrape_data",
    "Extract structured data from a webpage using CSS selectors. "
    "Returns text content of matching elements.",
    {
        "type": "object",
        "properties": {
            "url": {
                "type": "string",
                "description": "The URL to scrape",
            },
            "selectors": {
                "type": "array",
                "items": {"type": "string"},
                "description": "CSS selectors to extract (e.g., ['h1', '.price', '#title']). "
                "If not provided, extracts main text content.",
            },
        },
        "required": ["url"],
    },
)
async def scrape_data(url: str, selectors: list[str] | None = None) -> str:
    if _is_private_url(url):
        return "URL blocked: cannot access internal/private addresses."

    try:
        resp = requests.get(
            url,
            timeout=_REQUEST_TIMEOUT,
            headers={"User-Agent": "Shadow-Claw/1.0"},
        )
        resp.raise_for_status()
    except requests.RequestException as e:
        return f"Error fetching {url}: {e}"

    try:
        from bs4 import BeautifulSoup
    except ImportError:
        return "beautifulsoup4 is required. Install with: pip install beautifulsoup4"

    html = resp.content[:_MAX_CONTENT_BYTES].decode("utf-8", errors="replace")
    soup = BeautifulSoup(html, "html.parser")

    if not selectors:
        for tag in soup(["script", "style"]):
            tag.decompose()
        text = soup.get_text(separator="\n", strip=True)
        return json.dumps({"url": url, "text": text[:3000]}, ensure_ascii=False)

    results = {}
    for sel in selectors:
        elements = soup.select(sel)
        results[sel] = [el.get_text(strip=True) for el in elements[:20]]

    return json.dumps({"url": url, "data": results}, ensure_ascii=False, indent=2)


@tool(
    "scrape_links",
    "Extract all links from a webpage, optionally filtered by a pattern.",
    {
        "type": "object",
        "properties": {
            "url": {
                "type": "string",
                "description": "The URL to scrape links from",
            },
            "pattern": {
                "type": "string",
                "description": "Optional regex pattern to filter links (e.g., '.*\\.pdf$')",
            },
        },
        "required": ["url"],
    },
)
async def scrape_links(url: str, pattern: str | None = None) -> str:
    if _is_private_url(url):
        return "URL blocked: cannot access internal/private addresses."

    try:
        resp = requests.get(
            url,
            timeout=_REQUEST_TIMEOUT,
            headers={"User-Agent": "Shadow-Claw/1.0"},
        )
        resp.raise_for_status()
    except requests.RequestException as e:
        return f"Error fetching {url}: {e}"

    try:
        from bs4 import BeautifulSoup
    except ImportError:
        return "beautifulsoup4 is required. Install with: pip install beautifulsoup4"

    soup = BeautifulSoup(resp.text, "html.parser")
    links = []
    for a in soup.find_all("a", href=True):
        href = a["href"]
        text = a.get_text(strip=True) or ""
        if pattern:
            try:
                if not re.search(pattern, href):
                    continue
            except re.error:
                return f"Invalid regex pattern: {pattern}"
        links.append({"href": href, "text": text})

    if not links:
        return "No links found matching the criteria."

    return json.dumps({"url": url, "links": links[:50]}, ensure_ascii=False, indent=2)
