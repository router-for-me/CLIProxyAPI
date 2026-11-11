package safemode

import (
	"html"
	"strings"
)

var exampleAPIKeys = map[string]struct{}{
	"your-api-key-1": {},
	"your-api-key-2": {},
	"your-api-key-3": {},
}

// ExampleAPIKeys returns configured top-level API keys that still use template values.
func ExampleAPIKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}

	matches := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(exampleAPIKeys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if _, ok := exampleAPIKeys[trimmed]; !ok {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		matches = append(matches, trimmed)
	}
	if len(matches) == 0 {
		return nil
	}
	return matches
}

// HasExampleAPIKeys reports whether any configured top-level API key is a template value.
func HasExampleAPIKeys(keys []string) bool {
	return len(ExampleAPIKeys(keys)) > 0
}

// ExampleAPIKeyWarningPageHTML returns the setup warning page HTML.
func ExampleAPIKeyWarningPageHTML(keys []string, managementPath string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Example API key detected</title><style>body{margin:0;font-family:Arial,sans-serif;background:#f6f8fa;color:#1f2328}.wrap{max-width:760px;margin:12vh auto;padding:0 24px}.panel{background:#fff;border:1px solid #d0d7de;border-radius:8px;padding:28px;box-shadow:0 8px 24px rgba(140,149,159,.2)}h1{margin:0 0 12px;font-size:28px;line-height:1.25}p{font-size:16px;line-height:1.55}code{background:#f6f8fa;border:1px solid #d0d7de;border-radius:4px;padding:2px 5px}.keys{margin:16px 0;padding-left:22px}.actions{margin-top:24px}.button{display:inline-block;border-radius:6px;background:#0969da;color:#fff;text-decoration:none;font-weight:600;padding:10px 16px}.button:hover{background:#0759b8}</style></head><body><main class="wrap"><section class="panel"><h1>Example API key detected</h1><p>Proxy API endpoints are disabled because the top-level <code>api-keys</code> configuration still contains template values.</p>`)
	if len(keys) > 0 {
		b.WriteString(`<p>Replace these values before using the proxy:</p><ul class="keys">`)
		for _, key := range keys {
			b.WriteString(`<li><code>`)
			b.WriteString(html.EscapeString(key))
			b.WriteString(`</code></li>`)
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(`<p>Set strong random API keys, then retry the proxy endpoint.</p>`)
	if trimmed := strings.TrimSpace(managementPath); trimmed != "" {
		b.WriteString(`<div class="actions"><a class="button" href="`)
		b.WriteString(html.EscapeString(trimmed))
		b.WriteString(`">Open Management</a></div>`)
	}
	b.WriteString(`</section></main></body></html>`)
	return b.String()
}
