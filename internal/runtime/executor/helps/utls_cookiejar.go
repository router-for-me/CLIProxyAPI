package helps

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/net/publicsuffix"
)

// accountCookieJars holds one persistent cookie jar per upstream account, keyed
// by auth.ID. Jars live for the process lifetime so Cloudflare clearance cookies
// survive across requests (and across the per-request http.Client instances that
// NewUtlsHTTPClient creates).
var (
	accountCookieJarsMu sync.Mutex
	accountCookieJars   = make(map[string]http.CookieJar)
)

// cookieJarForAuth returns a persistent, per-account cookie jar so Cloudflare
// clearance cookies (cf_clearance, __cf_bm, _cfuvid, __cflb, cf_chl_*) set by the
// upstream edge are stored and replayed on subsequent requests for the same
// account. This mirrors the real Codex CLI, whose reqwest cookie jar round-trips
// these cookies and is a primary lever against Cloudflare 403s; a stateless proxy
// that drops them looks different to the edge.
//
// Keyed by auth.ID so each upstream account keeps its own clearance. Returns nil
// when the jar is disabled or when the account has no stable identity, in which
// case the client falls back to prior cookieless behavior.
func cookieJarForAuth(cfg *config.Config, auth *cliproxyauth.Auth) http.CookieJar {
	if cfg != nil && cfg.DisableUpstreamCookieJar {
		return nil
	}
	if auth == nil {
		return nil
	}
	key := strings.TrimSpace(auth.ID)
	if key == "" {
		return nil
	}

	accountCookieJarsMu.Lock()
	defer accountCookieJarsMu.Unlock()
	if jar, ok := accountCookieJars[key]; ok {
		return jar
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil
	}
	accountCookieJars[key] = jar
	return jar
}
