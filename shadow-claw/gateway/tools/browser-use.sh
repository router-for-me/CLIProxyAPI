#!/usr/bin/env bash
# browser-use - Headless web fetch tool for Shadow-Claw
# Reads URL or instruction from stdin or first argument
# Fetches page content and extracts readable text
set -euo pipefail

PROMPT="${1:-$(cat)}"
if [ -z "$PROMPT" ]; then
    echo "Error: no URL or instruction provided" >&2
    exit 1
fi

# Extract URL from prompt - accept with or without protocol
URL=$(echo "$PROMPT" | grep -oE '(https?://[^ ]+|www\.[^ ]+|[a-zA-Z0-9-]+\.[a-zA-Z]{2,}\.[a-zA-Z]{2,}[^ ]*)' | head -1 || true)

# Add https:// if missing
if [ -n "$URL" ] && ! echo "$URL" | grep -q '^https\?://'; then
    URL="https://$URL"
fi

if [ -z "$URL" ]; then
    # If no URL, try to search and fetch first result
    ENCODED=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$PROMPT")
    echo "No URL found. Searching: $PROMPT"
    echo "---"
    URL=$(curl -sL --max-time 10 \
        -H "User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36" \
        "https://html.duckduckgo.com/html/?q=${ENCODED}" | \
        python3 -c "
import sys, re
content = sys.stdin.read()
urls = re.findall(r'class=\"result__url\"[^>]*>\s*(https?://[^<\s]+)', content)
if urls:
    print(urls[0].strip())
" 2>/dev/null || true)

    if [ -z "$URL" ]; then
        echo "Could not find a relevant URL."
        exit 1
    fi
    echo "Found: $URL"
    echo ""
fi

echo "Fetching: $URL"
echo "---"

# Fetch page and extract text content
RAW=$(curl -sL --max-time 20 \
    -H "User-Agent: Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36" \
    "$URL" 2>/dev/null)

if [ -z "$RAW" ]; then
    echo "Error: could not fetch $URL"
    exit 1
fi

# Extract readable text (strip HTML, collapse whitespace)
echo "$RAW" | python3 -c "
import sys, re, html

content = sys.stdin.read()

# Remove script and style blocks
content = re.sub(r'<(script|style)[^>]*>.*?</\1>', '', content, flags=re.DOTALL|re.IGNORECASE)

# Get title
title_match = re.search(r'<title[^>]*>(.*?)</title>', content, re.DOTALL|re.IGNORECASE)
title = html.unescape(title_match.group(1).strip()) if title_match else 'No title'

# Strip all remaining tags
text = re.sub(r'<[^>]+>', ' ', content)
text = html.unescape(text)

# Collapse whitespace
text = re.sub(r'[ \t]+', ' ', text)
text = re.sub(r'\n\s*\n', '\n\n', text)
text = text.strip()

# Limit output
lines = text.split('\n')
output = '\n'.join(lines[:80])
if len(lines) > 80:
    output += f'\n\n[... truncated {len(lines) - 80} more lines]'

print(f'Title: {title}')
print(f'URL: {sys.argv[1] if len(sys.argv) > 1 else \"stdin\"}')
print('---')
print(output)
" "$URL"
