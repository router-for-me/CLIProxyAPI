package mgmt

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//go:embed all:dist
var dist embed.FS

// csp allows only same-origin scripts, styles, and API calls. The dashboard
// has no inline scripts and loads nothing from third parties.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'; object-src 'none'"

var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".json":  "application/json",
	".txt":   "text/plain; charset=utf-8",
}

// assetFiles lists embedded files relative to dist, e.g. "index.html", "assets/app-1a2b.js".
func assetFiles() []string {
	var files []string
	_ = fs.WalkDir(dist, "dist", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, "dist/")
		if _, ok := contentTypes[path.Ext(rel)]; ok {
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// Resources lists one exact resource route per embedded file; CPA does not
// support wildcard resource routes. Only index.html appears in CPA's menu.
func Resources() []pluginapi.ResourceRoute {
	var out []pluginapi.ResourceRoute
	for _, f := range assetFiles() {
		r := pluginapi.ResourceRoute{Path: ResourceBase + "/" + f}
		if f == "index.html" {
			r.Menu = "Quota Router"
			r.Description = "Claude and Codex quota, session affinity, and routing dashboard"
		}
		out = append(out, r)
	}
	return out
}

func serveAsset(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	rel := strings.TrimPrefix(req.Path, ResourceBase+"/")
	if rel == "" || strings.Contains(rel, "..") {
		rel = "index.html"
	}
	ct, ok := contentTypes[path.Ext(rel)]
	body, err := dist.ReadFile("dist/" + rel)
	if !ok || err != nil {
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Headers: http.Header{"Content-Type": {"text/plain; charset=utf-8"}}, Body: []byte("not found")}
	}
	h := http.Header{
		"Content-Type":                 {ct},
		"X-Content-Type-Options":       {"nosniff"},
		"Referrer-Policy":              {"no-referrer"},
		"Content-Security-Policy":      {csp},
		"Cross-Origin-Opener-Policy":   {"same-origin"},
		"Cross-Origin-Resource-Policy": {"same-origin"},
		"Permissions-Policy":           {"camera=(), microphone=(), geolocation=(), payment=()"},
	}
	if strings.HasPrefix(rel, "assets/") {
		// Hashed file names: safe to cache forever.
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: h, Body: body}
}
