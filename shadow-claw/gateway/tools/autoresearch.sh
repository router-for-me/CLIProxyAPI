#!/usr/bin/env bash
# autoresearch - Web search tool for Shadow-Claw
# Reads prompt from stdin or first argument
# Uses DuckDuckGo HTML endpoint (no API key needed)
set -euo pipefail

PROMPT="${1:-$(cat)}"
if [ -z "$PROMPT" ]; then
    echo "Error: no search query provided" >&2
    exit 1
fi

ENCODED=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$PROMPT")

# Fetch DuckDuckGo HTML results
RAW=$(curl -sL --max-time 15 \
    -H "User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36" \
    "https://html.duckduckgo.com/html/?q=${ENCODED}")

# Extract result titles and snippets
RESULTS=$(echo "$RAW" | python3 -c "
import sys, html, re

content = sys.stdin.read()
# Extract result blocks
titles = re.findall(r'class=\"result__a\"[^>]*>(.*?)</a>', content, re.DOTALL)
snippets = re.findall(r'class=\"result__snippet\"[^>]*>(.*?)</td>', content, re.DOTALL)
urls = re.findall(r'class=\"result__url\"[^>]*>(.*?)</a>', content, re.DOTALL)

if not titles:
    print('No results found.')
    sys.exit(0)

clean = lambda s: html.unescape(re.sub(r'<[^>]+>', '', s)).strip()

count = min(len(titles), 5)
for i in range(count):
    t = clean(titles[i]) if i < len(titles) else ''
    s = clean(snippets[i]) if i < len(snippets) else ''
    u = clean(urls[i]) if i < len(urls) else ''
    print(f'{i+1}. {t}')
    if u:
        print(f'   {u}')
    if s:
        print(f'   {s}')
    print()
")

echo "Search results for: $PROMPT"
echo "---"
echo "$RESULTS"
