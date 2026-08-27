package registry

import (
	"net/url"
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

// isHTTPSource reports whether a source entry must be fetched over HTTP(S).
// Everything else is treated as a filesystem path so users can point the
// overrides at purely local files without any hosting setup.
func isHTTPSource(source string) bool {
	l := strings.ToLower(strings.TrimSpace(source))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// readLocalCatalog loads raw catalog bytes from a filesystem path entry.
// Accepts plain paths relative to the process working directory as well as
// file:// URIs on every platform.
func readLocalCatalog(source string) ([]byte, error) {
	p := strings.TrimSpace(source)
	if len(p) >= 7 && strings.EqualFold(p[:7], "file://") {
		u, err := url.Parse(p)
		if err != nil {
			return nil, err
		}
		p = u.Path
	}
	return os.ReadFile(p)
}
