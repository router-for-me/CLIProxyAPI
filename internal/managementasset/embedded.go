package managementasset

import (
	_ "embed"
	"os"
	"strings"
)

// embeddedPanel is papa-proxy's own management console, built from console/
// by scripts/build-console.sh.
//
//go:embed panel/management.html
var embeddedPanel []byte

// EmbeddedPanel returns the built-in management console.
func EmbeddedPanel() []byte {
	return embeddedPanel
}

// UseEmbeddedPanel reports whether the built-in console is served instead of
// a downloaded one. It is, unless MANAGEMENT_STATIC_PATH points at a custom
// panel. Serving it also stops the updater from fetching upstream's panel.
func UseEmbeddedPanel() bool {
	return len(embeddedPanel) > 0 && strings.TrimSpace(os.Getenv("MANAGEMENT_STATIC_PATH")) == ""
}
