package auth

import (
	"encoding/json"
	"os"
	"strings"
)

// ReloadMetadataFromFile replaces auth.Metadata with the JSON object stored at path.
// Token stores call it after persisting Storage-backed credentials so the in-memory
// auth record carries the full token payload (access_token, refresh_token, ...)
// rather than the minimal metadata assembled by login flows. Without this,
// deployments where auth-directory file watch events are unavailable keep a
// token-less record in memory until the next restart.
func ReloadMetadataFromFile(a *Auth, path string) {
	if a == nil || strings.TrimSpace(path) == "" {
		return
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil || len(data) == 0 {
		return
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil || len(metadata) == 0 {
		return
	}
	a.Metadata = metadata
}
