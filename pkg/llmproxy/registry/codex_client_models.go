package registry

import _ "embed"

//go:embed models/codex_client_models.json
var codexClientModelsJSON []byte

// GetCodexClientModelsJSON returns the embedded Codex client model catalog.
func GetCodexClientModelsJSON() []byte {
	return append([]byte(nil), codexClientModelsJSON...)
}
