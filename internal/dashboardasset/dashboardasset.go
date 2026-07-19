// Package dashboardasset serves the embedded NixLLM dashboard SPA (built from
// web/dashboard via Vite into web/dashboard/dist). The dashboard is mounted
// at /dashboard on the same Go HTTP server that hosts the management API, so
// production deployments need no separate static-file host.
//
// The package takes precedence by build-tag: when the SPA has been compiled
// (i.e. web/dashboard/dist is populated and present at build time via
// `npm run build`), the embed directive picks it up; otherwise we fall back
// to dev mode (the user runs `npm run dev` on :9173 and lets Vite proxy
// /v0 to the Go server).
package dashboardasset

import (
	"embed"
	"io/fs"
)

// distFS holds the built dashboard bundle. When the dist directory is empty
// at build time, the embed directive still compiles (it requires at least
// one file, hence the included .gitkeep placeholder).
//
//go:embed dist/*
var distFS embed.FS

// FileSystem returns a fs.FS rooted at the dashboard's dist directory. The
// returned filesystem is suitable for http.FileServer usage.
func FileSystem() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Should never happen at runtime — the embed guarantees the sub
		// directory exists. Fall back to the raw FS so callers don't panic.
		return distFS
	}
	return sub
}

// Available reports whether a real dashboard build is present. It checks
// for any file larger than the placeholder .gitkeep; true means the Go
// server should serve the SPA at /dashboard, false means the caller should
// instruct the user to run `npm run dev` (or `npm run build`).
func Available() bool {
	entries, err := distFS.ReadDir("dist")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && e.Name() != ".gitkeep" {
			return true
		}
	}
	return false
}
