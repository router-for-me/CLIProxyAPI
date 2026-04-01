package managementasset

import (
	_ "embed"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

//go:embed static/management.html
var embeddedManagementHTML []byte

// ManagementFileName exposes the control panel asset filename.
const ManagementFileName = "management.html"

var currentConfigPtr atomic.Pointer[config.Config]

// SetCurrentConfig stores the latest configuration snapshot for management asset decisions.
func SetCurrentConfig(cfg *config.Config) {
	if cfg == nil {
		currentConfigPtr.Store(nil)
		return
	}
	currentConfigPtr.Store(cfg)
}

// EmbeddedHTML returns the embedded management control panel HTML content.
func EmbeddedHTML() []byte {
	return embeddedManagementHTML
}
