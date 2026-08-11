// Package managementdashboard exposes the management usage dashboard assets
// embedded in the server executable.
package managementdashboard

import (
	"bytes"
	_ "embed"
)

const (
	DashboardAsset  = "dashboard.html"
	StylesheetAsset = "dashboard.css"
	ScriptAsset     = "dashboard.js"
	LauncherAsset   = "launcher.css"

	launcherMarker = `data-cliproxy-usage-launcher`
	launcherHTML   = `<link rel="stylesheet" href="/management/usage/launcher.css" data-cliproxy-usage-launcher="stylesheet">
<a class="cliproxy-usage-launcher" href="/management/usage" target="_blank" rel="noopener noreferrer" data-cliproxy-usage-launcher="link" aria-label="Open usage and billing dashboard"><span aria-hidden="true">&#x2726;</span><span>Usage &amp; billing</span></a>`
)

//go:embed dashboard.html
var dashboardHTML []byte

//go:embed dashboard.css
var dashboardCSS []byte

//go:embed dashboard.js
var dashboardJS []byte

//go:embed launcher.css
var launcherCSS []byte

// Asset returns an embedded asset and its MIME type. Names are matched exactly;
// paths, aliases, and case variations are intentionally rejected. The returned
// bytes are a copy and may be safely modified by the caller.
func Asset(name string) (data []byte, contentType string, ok bool) {
	switch name {
	case DashboardAsset:
		return cloneAsset(dashboardHTML), "text/html; charset=utf-8", true
	case StylesheetAsset:
		return cloneAsset(dashboardCSS), "text/css; charset=utf-8", true
	case ScriptAsset:
		return cloneAsset(dashboardJS), "text/javascript; charset=utf-8", true
	case LauncherAsset:
		return cloneAsset(launcherCSS), "text/css; charset=utf-8", true
	default:
		return nil, "", false
	}
}

// DashboardHTML returns a copy of the embedded dashboard document.
func DashboardHTML() []byte {
	data, _, _ := Asset(DashboardAsset)
	return data
}

// InjectLauncher adds the dashboard launcher to a plausible HTML document.
// Injection happens in memory and is idempotent. Non-HTML and bodyless input is
// returned unchanged.
func InjectLauncher(page []byte) []byte {
	if len(page) == 0 || bytes.Contains(page, []byte(launcherMarker)) {
		return page
	}

	lower := lowerASCII(page)
	htmlAt := openingTagIndex(lower, "html", 0)
	bodyAt := openingTagIndex(lower, "body", 0)
	bodyEnd := closingTagIndex(lower, "body")
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(page, []byte{0xef, 0xbb, 0xbf}))
	if len(trimmed) == 0 || trimmed[0] != '<' || htmlAt < 0 || bodyAt < htmlAt || bodyEnd < bodyAt {
		return page
	}

	injected := make([]byte, 0, len(page)+len(launcherHTML)+2)
	injected = append(injected, page[:bodyEnd]...)
	injected = append(injected, '\n')
	injected = append(injected, launcherHTML...)
	injected = append(injected, '\n')
	injected = append(injected, page[bodyEnd:]...)
	return injected
}

func cloneAsset(data []byte) []byte {
	return append([]byte(nil), data...)
}

func openingTagIndex(lower []byte, name string, start int) int {
	return tagIndex(lower, "<"+name, start)
}

func closingTagIndex(lower []byte, name string) int {
	needle := []byte("</" + name)
	for searchEnd := len(lower); searchEnd > 0; {
		index := bytes.LastIndex(lower[:searchEnd], needle)
		if index < 0 {
			return -1
		}
		after := index + len(needle)
		if after < len(lower) && isTagBoundary(lower[after]) {
			return index
		}
		searchEnd = index
	}
	return -1
}

func tagIndex(lower []byte, tag string, start int) int {
	needle := []byte(tag)
	for start < len(lower) {
		relative := bytes.Index(lower[start:], needle)
		if relative < 0 {
			return -1
		}
		index := start + relative
		after := index + len(needle)
		if after < len(lower) && isTagBoundary(lower[after]) {
			return index
		}
		start = index + 1
	}
	return -1
}

func lowerASCII(input []byte) []byte {
	lower := cloneAsset(input)
	for index, value := range lower {
		if value >= 'A' && value <= 'Z' {
			lower[index] = value + ('a' - 'A')
		}
	}
	return lower
}

func isTagBoundary(value byte) bool {
	return value == '>' || value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f'
}
