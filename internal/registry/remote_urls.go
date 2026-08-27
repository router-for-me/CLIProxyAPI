package registry

import (
	"os"
	"strings"
)

// resolveRemoteURLs returns the ordered remote catalog URLs to try.

// Entries are tried in order and the first successful fetch wins. When the
// given environment variable is set, it supplies extra URLs (comma separated,
// whitespace trimmed) that are tried BEFORE the defaults; the defaults remain
// appended so a broken or unreachable custom source never disables catalog
// updates. This lets users serve newer model slots (for example slugs visible
// in ~/.codex/models_cache.json) without waiting for an upstream catalog
// release and makes catalog changes testable against a real instance.

// Recognized variables:
//   CPA_REMOTE_MODELS_URL               overrides/models.json (provider routing)
//   CPA_REMOTE_CODEX_CLIENT_MODELS_URL  codex_client_models.json (client view)

// Example:
//
//	export CPA_REMOTE_MODELS_URL='https://raw.githubusercontent.com/<user>/models/main/models.json'
func resolveRemoteURLs(envKey string, defaults []string) []string {
	var custom []string
	for _, raw := range strings.Split(os.Getenv(envKey), ",") {
		u := strings.TrimSpace(raw)
		if u != "" {
			custom = append(custom, u)
		}
	}
	return append(custom, defaults...)
}
