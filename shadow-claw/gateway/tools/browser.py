"""Browser tools: browse URLs and search the web.

Inspired by browser-use/browser-use. Uses requests + BeautifulSoup for
lightweight page fetching. Includes SSRF blocklist for private IPs.
"""

import ipaddress
import logging
import re
import socket
from urllib.parse import urlparse

import requests

from agent import tool
from config import truncate_text

LOGGER = logging.getLogger("shadow_claw_gateway.tools.browser")

_MAX_CONTENT_BYTES = 1_000_000  # 1MB
_MAX_TEXT_CHARS = 4000
_REQUEST_TIMEOUT = 30

_BLOCKED_NETWORKS = [
    ipaddress.ip_network("127.0.0.0/8"),
    ipaddress.ip_network("10.0.0.0/8"),
    ipaddress.ip_network("172.16.0.0/12"),
    ipaddress.ip_network("192.168.0.0/16"),
    ipaddress.ip_network("169.254.0.0/16"),
    ipaddress.ip_network("::1/128"),
    ipaddress.ip_network("fc00::/7"),
    ipaddress.ip_network("fe80::/10"),
]


def _is_private_url(url: str) -> bool:
    """Return True if *url* resolves to a private/internal address."""
    try:
        parsed = urlparse(url)
        hostname = parsed.hostname or ""
        if hostname in ("localhost", ""):
            return True
        try:
            # Try parsing as a raw IP first (fast path).
            addr = ipaddress.ip_address(hostname)
            return any(addr in net for net in _BLOCKED_NETWORKS)
        except ValueError:
            pass
        # DNS name — resolve and check every returned address.
        for _family, _type, _proto, _canon, sockaddr in socket.getaddrinfo(hostname, None):
            try:
                addr = ipaddress.ip_address(sockaddr[0])
                if any(addr in net for net in _BLOCKED_NETWORKS):
                    return True
            except ValueError:
                pass
        return False
    except (OSError, TypeError):
        # getaddrinfo failure (NXDOMAIN, etc.) — block to be safe.
        return True


def _extract_text(html: str) -> str:
    """Extract visible text from HTML, with BeautifulSoup if available."""
    try:
        from bs4 import BeautifulSoup
        soup = BeautifulSoup(html, "html.parser")
        for tag in soup(["script", "style", "nav", "footer", "header"]):
            tag.decompose()
        text = soup.get_text(separator="\n", strip=True)
    except ImportError:
        # Fallback: strip HTML tags with regex
        text = re.sub(r"<[^>]+>", " ", html)
        text = re.sub(r"\s+", " ", text).strip()
    return text


@tool(
    "browse_url",
    "Navigate to a URL and extract its text content. "
    "Optionally answer a specific question about the page.",
    {
        "type": "object",
        "properties": {
            "url": {
                "type": "string",
                "description": "The URL to browse",
            },
            "question": {
                "type": "string",
                "description": "Optional question to answer about the page content",
            },
        },
        "required": ["url"],
    },
)
async def browse_url(url: str, question: str | None = None) -> str:
    if _is_private_url(url):
        return "URL blocked: cannot access internal/private addresses."

    try:
        resp = requests.get(
            url,
            timeout=_REQUEST_TIMEOUT,
            headers={"User-Agent": "Shadow-Claw/1.0"},
            stream=True,
        )
        # Check content length before reading
        content_length = int(resp.headers.get("content-length", 0))
        if content_length > _MAX_CONTENT_BYTES:
            return f"Page too large ({content_length} bytes). Maximum is {_MAX_CONTENT_BYTES} bytes."

        content = resp.content[:_MAX_CONTENT_BYTES]
        html = content.decode("utf-8", errors="replace")
    except requests.Timeout:
        return f"Request to {url} timed out after {_REQUEST_TIMEOUT}s."
    except requests.ConnectionError as e:
        return f"Could not connect to {url}: {e}"
    except requests.RequestException as e:
        return f"Error fetching {url}: {e}"

    text = _extract_text(html)
    text = truncate_text(text, _MAX_TEXT_CHARS)

    if question:
        return f"Page content from {url}:\n\n{text}\n\n(Answer the question: {question})"
    return f"Page content from {url}:\n\n{text}"


@tool(
    "browse_search",
    "Search the web for a query and return top results. "
    "Uses DuckDuckGo HTML search.",
    {
        "type": "object",
        "properties": {
            "query": {
                "type": "string",
                "description": "Search query",
            },
        },
        "required": ["query"],
    },
)
async def browse_search(query: str) -> str:
    try:
        resp = requests.get(
            "https://html.duckduckgo.com/html/",
            params={"q": query},
            timeout=_REQUEST_TIMEOUT,
            headers={"User-Agent": "Shadow-Claw/1.0"},
        )
        resp.raise_for_status()
    except requests.RequestException as e:
        return f"Search failed: {e}"

    try:
        from bs4 import BeautifulSoup
        soup = BeautifulSoup(resp.text, "html.parser")
        results = []
        for item in soup.select(".result__body")[:5]:
            title_el = item.select_one(".result__a")
            snippet_el = item.select_one(".result__snippet")
            title = title_el.get_text(strip=True) if title_el else "No title"
            link = title_el.get("href", "") if title_el else ""
            snippet = snippet_el.get_text(strip=True) if snippet_el else ""
            results.append(f"- {title}\n  {link}\n  {snippet}")
        if results:
            return f"Search results for '{query}':\n\n" + "\n\n".join(results)
        return f"No search results found for '{query}'."
    except ImportError:
        return "beautifulsoup4 is required for web search. Install with: pip install beautifulsoup4"
