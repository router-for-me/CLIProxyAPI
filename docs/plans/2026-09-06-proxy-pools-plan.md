# Proxy Pools Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Adopt the 9router Proxy Pools workflow as a named PG `proxy_pools` entity with per-upstream-entry binding, pool test/health/batch/bulk operations, `noProxy`/`strict` composite-URL transport semantics, relay pools (`x-relay-target`/`x-relay-path` header-mode), and one-click relay deploy to Cloudflare/Vercel/Deno — plus a dashboard page and editor pickers.

**Architecture:** Pools are a configuration-time concern: binding columns (`proxy_pool_id` on rows + entries) are resolved inside the existing `upstreamsync` render into the concrete `ProxyURL` field (composite URL with `?no_proxy=…&strict=true` suffix) or a new `Auth.RelayBaseURL`; `sdk/proxyutil.Parse` learns the suffix; executors/conductor are untouched. Design: `docs/plans/2026-09-06-proxy-pools-design.md` (commit 732d4ab0).

**Tech Stack:** Go 1.26 (Gin, database/sql Postgres), React + Vite SPA (`node:test` for JS), no new dependencies.

**Worktree:** `.worktrees/proxy-pools`, branch `feat/proxy-pools`. Baseline: build ✅, `go test ./...` 94 pkgs OK except pre-existing `TestMountDashboardRoutes` (worktree has no `dist/` — run `make dash-embed` in Task 18 to fix; passes on main tree).

**Conventions (read once, apply everywhere):**
- Go comments in English; `gofmt -w .` before every commit; logrus (no `log.Fatal`); no timeouts after upstream connection established except the pre-existing whitelisted ones (relay-deploy HTTP calls are credential/setup-phase calls, not request-serving — but keep them under the management APICall-timeout spirit: pass explicit `http.Client{Timeout: 60s}` per platform call, they are deployment orchestration, not proxy traffic).
- `docs/plans` is gitignored — always `git add -f docs/plans/<file>`.
- Tests requiring Postgres skip unless `PGSTORE_TEST_DSN` is set (pattern: `internal/store/pg_apikeys_test.go:16`).
- SPA tests: `node --import ./scripts/register-jsx.mjs --test` (npm test); API list responses use endpoint-specific keys (`pools`), always `Array.isArray()`-guard.

**Key integration points (verified):**

| Concern | Location |
|---|---|
| Store pattern | `internal/store/pg_upstream_providers.go` (struct, `UpstreamProviderStore` interface, `NewUpstreamProviderStore(parent *PostgresStore)`, `replaceChildrenTx`) |
| Table names | `internal/store/postgresstore.go:43-47` defaults + `PGConfig` fields ~155-171 + `Migrate` at :669 (idempotent `ADD COLUMN IF NOT EXISTS` blocks) |
| `upstream_providers` CREATE | `postgresstore.go` `ensureUpstreamProvidersSchema` (~line 1540-1630) |
| Store wiring | `cmd/server/main.go:816` `store.NewUpstreamProviderStore(pgStoreInst)` → `api.PgStoreHandles{...}` :846 → `internal/api/server.go:242` setter |
| Handler wiring | `internal/api/handlers/management/handler.go`: field :100, `SetUpstreamProvidersStore` :361, `applyUpstreamProviders` :931 |
| 503 pattern | `upstream_providers.go:40` `upstreamProvidersStore(c)` + `pgNotConfigured(c)` |
| Routes | `internal/api/server_management.go:351-355` (register after upstream-providers) |
| Render | `internal/upstreamsync/render.go`: `buildClaudeKey` :161 (proxy at :162-165), `openAICompatFromProvider` :212 (entry at :233) |
| Seed round-trip | `internal/upstreamsync/seed.go`: `providerFromClaudeKey` :180 (:187 ProxyURL), `providerFromOpenAICompat` :237 (:258) |
| DTO | `internal/api/handlers/management/upstream_providers_types.go`: row `ProxyURL` :24, entry `upstreamProviderEntryReq` :67 |
| Auth struct | `sdk/cliproxy/auth/types.go:105-106` (`ProxyURL`), synthesized in `internal/watcher/synthesizer/config.go` (claude :158/187, compat :381/426) |
| Transport constructors | `internal/runtime/executor/helps/proxy_helpers.go:36` `NewProxyAwareHTTPClient`; `utls_client.go:363` `NewUtlsHTTPClient`; `sdk/cliproxy/rtprovider.go:26`; websocket `codex_websockets_connection.go:157` |
| proxyutil | `sdk/proxyutil/proxy.go`: `Parse` :47, `BuildHTTPTransport` :96, `BuildDialer` :132, `Redact` :263 |
| Client API | `web/dashboard/src/api/client.js` (`fetchJSON`, list fns ~1780+), `managementEndpoints.js` registry (~:227 proxy-url group) |
| SPA | `web/dashboard/src/App.jsx:293`, `components/Sidebar.jsx:432`, editor `pages/upstream-provider-editor/` (`schemas.js`, `EntriesEditor.jsx`, `form.js`) |

---

## Task 1: proxyutil composite-URL suffix (noProxy + strict) — Parse

**Files:**
- Modify: `sdk/proxyutil/proxy.go`
- Test: `sdk/proxyutil/proxy_test.go`

**Step 1: Write failing tests**

Add to `sdk/proxyutil/proxy_test.go` (it already tests Parse modes; keep style):

```go
// TestParseCompositeSuffix verifies the pool-attribute query suffix rendered
// by upstreamsync: ?no_proxy=…&strict=false is parsed into typed fields and
// stripped from the proxy URL.
func TestParseCompositeSuffix(t *testing.T) {
	setting, err := Parse("socks5://user:pass@host:1080?no_proxy=api.anthropic.com,.internal&strict=true")
	if err != nil {
		t.Fatalf("parse composite: %v", err)
	}
	if setting.Mode != ModeProxy {
		t.Fatalf("mode = %v, want ModeProxy", setting.Mode)
	}
	if setting.URL.String() != "socks5://user:pass@host:1080" {
		t.Fatalf("stripped URL = %q", setting.URL.String())
	}
	if got := strings.Join(setting.NoProxy, ","); got != "api.anthropic.com,.internal" {
		t.Fatalf("noProxy = %q", got)
	}
	if !setting.Strict {
		t.Fatal("strict = false, want true")
	}
}

func TestParseCompositeSuffixDefaults(t *testing.T) {
	// No suffix at all: strict defaults true (NixLLM fail-hard default),
	// noProxy empty.
	setting, err := Parse("http://proxy:8080")
	if err != nil {
		t.Fatalf("parse plain: %v", err)
	}
	if !setting.Strict {
		t.Fatal("plain URL must default strict=true")
	}
	if len(setting.NoProxy) != 0 {
		t.Fatalf("noProxy = %v, want empty", setting.NoProxy)
	}
}

func TestParseCompositeSuffixRejectsUnknownKeys(t *testing.T) {
	if _, err := Parse("http://proxy:8080?surprise=1"); err == nil {
		t.Fatal("unknown suffix key must be rejected (fail-fast on hand-edited YAML)")
	}
	// bare "no_proxy=" / "strict=" with no value must fail too
	if _, err := Parse("http://proxy:8080?strict="); err == nil {
		t.Fatal("empty strict value must be rejected")
	}
}
```

**Step 2: Run, verify FAIL**

Run: `go test ./sdk/proxyutil/ -run 'TestParseComposite' -v`
Expected: FAIL (`setting.NoProxy undefined`, `setting.Strict undefined`, unknown keys accepted).

**Step 3: Implement**

In `sdk/proxyutil/proxy.go`:

3a. Extend `Setting` (after the `URL *url.URL` field):

```go
// NoProxy lists host patterns (exact, ".suffix" subdomain, or "*") that must
// bypass the proxy even when the URL configures one. Populated from the
// composite suffix "?no_proxy=a,b".
NoProxy []string
// Strict is the failure policy: true (default) fails the request when the
// proxy fails; false retries once directly with a warning log.
Strict bool
```

3b. Add a helper above `Parse`:

```go
// parseCompositeSuffix splits a "?no_proxy=…&strict=…" query off the raw
// proxy value. It returns the cleaned URL string plus the decoded settings.
// Unknown or empty-valued keys are rejected so hand-edited rendered YAML
// fails fast instead of silently dropping attributes.
func parseCompositeSuffix(raw string) (cleaned string, noProxy []string, strict bool, err error) {
	strict = true // fail-hard default (design decision 4)
	qIdx := strings.Index(raw, "?")
	if qIdx < 0 {
		return raw, nil, strict, nil
	}
	cleaned = raw[:qIdx]
	values, errParse := url.ParseQuery(raw[qIdx+1:])
	if errParse != nil {
		return "", nil, false, fmt.Errorf("parse proxy suffix: %w", errParse)
	}
	for key, vals := range values {
		if len(vals) != 1 || strings.TrimSpace(vals[0]) == "" {
			return "", nil, false, fmt.Errorf("proxy suffix key %q must have exactly one value", key)
		}
		switch key {
		case "no_proxy":
			for _, part := range strings.Split(vals[0], ",") {
				if p := strings.TrimSpace(part); p != "" {
					noProxy = append(noProxy, strings.ToLower(p))
				}
			}
		case "strict":
			switch strings.ToLower(strings.TrimSpace(vals[0])) {
			case "true":
				strict = true
			case "false":
				strict = false
			default:
				return "", nil, false, fmt.Errorf("proxy suffix strict=%q must be true|false", vals[0])
			}
		default:
			return "", nil, false, fmt.Errorf("unknown proxy suffix key %q", key)
		}
	}
	if len(noProxy) == 0 {
		return "", nil, false, fmt.Errorf("proxy suffix has no_proxy key but no host patterns")
	}
	return cleaned, noProxy, strict, nil
}
```

3c. In `Parse`, replace the direct `url.Parse(trimmed)` with:

```go
	cleaned, noProxy, strict, errSuffix := parseCompositeSuffix(trimmed)
	if errSuffix != nil {
		setting.Mode = ModeInvalid
		return setting, errSuffix
	}
	setting.NoProxy = noProxy
	setting.Strict = strict

	parsedURL, errParse := url.Parse(cleaned)
```

and keep the rest identical but use `cleaned` (the `direct`/`none` sentinels are checked before this line, untouched).

3d. Update `Redact` so redaction hides credentials but keeps host (+ strip suffix — it contains no secrets but keeps logs short):

```go
func Redact(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if cleaned, _, _, errSuffix := parseCompositeSuffix(trimmed); errSuffix == nil {
		trimmed = cleaned
	}
	// …rest unchanged
```

**Step 4: Run, verify PASS**

Run: `go test ./sdk/proxyutil/ -v`
Expected: all PASS including existing tests (`TestParse*` modes unchanged — plain URLs have no suffix).

**Step 5: Commit**

```bash
gofmt -w sdk/proxyutil
git add sdk/proxyutil
git commit -m "feat(proxyutil): parse no_proxy/strict composite suffix"
```

---

## Task 2: proxyutil — noProxy host matching + transport behavior

**Files:**
- Modify: `sdk/proxyutil/proxy.go`
- Test: `sdk/proxyutil/proxy_test.go`

**Step 1: Write failing tests**

```go
// TestParseNoProxyBypassesProxyMode verifies that a target host matching the
// no_proxy list flips the setting to direct mode at transport-build time.
func TestParseNoProxyBypassesProxyMode(t *testing.T) {
	cases := []struct {
		name     string
		noProxy  string
		target   string
		wantSkip bool
	}{
		{"exact host", "api.anthropic.com", "https://api.anthropic.com/v1", true},
		{"subdomain of .suffix", ".internal", "https://a.b.internal/x", true},
		{"bare suffix match", ".internal", "https://internal/x", true},
		{"wildcard", "*", "https://anything.example/x", true},
		{"no match", "api.anthropic.com", "https://api.openai.com/v1", false},
		{"case-insensitive", "API.Anthropic.com", "https://api.ANTHROPIC.com/v1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting, err := Parse("http://proxy:8080?no_proxy=" + url.QueryEscape(tc.noProxy))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := setting.Bypasses(tc.target); got != tc.wantSkip {
				t.Fatalf("Bypasses(%q) = %v, want %v", tc.target, got, tc.wantSkip)
			}
		})
	}
}

func TestBuildHTTPTransportNoProxyYieldsDirect(t *testing.T) {
	transport, mode, err := BuildHTTPTransport("http://proxy:8080?no_proxy=%2A")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if mode != ModeProxy {
		t.Fatalf("mode = %v (per-URL mode stays proxy; Bypasses is evaluated per target)", mode)
	}
	if transport == nil || transport.Proxy != nil {
		t.Fatal("no_proxy=* transport must have nil Proxy (direct)")
	}
}
```

**Step 2: Run, verify FAIL**

Run: `go test ./sdk/proxyutil/ -run 'NoProxy' -v`
Expected: FAIL (`Bypasses undefined`).

**Step 3: Implement**

3a. Method on `Setting`:

```go
// Bypasses reports whether the given target URL's host matches the noProxy
// pattern list and must therefore skip the proxy.
func (s Setting) Bypasses(targetURL string) bool {
	if len(s.NoProxy) == 0 {
		return false
	}
	host := ""
	if parsed, errParse := url.Parse(targetURL); errParse == nil {
		host = strings.ToLower(parsed.Hostname())
	} else if h, _, errSplit := net.SplitHostPort(targetURL); errSplit == nil {
		host = strings.ToLower(h)
	}
	if host == "" {
		return false
	}
	for _, pattern := range s.NoProxy {
		switch {
		case pattern == "*":
			return true
		case strings.HasPrefix(pattern, "."):
			if host == pattern[1:] || strings.HasSuffix(host, pattern) {
				return true
			}
		case host == pattern || strings.HasSuffix(host, "."+pattern):
			return true
		}
	}
	return false
}
```

3b. In `BuildHTTPTransport` `ModeProxy` branch, before constructing (both socks and http arms share this — compute once at branch head):

```go
		if setting.Bypasses("https://" + setting.URL.Host) {
			// Degenerate: proxy URL's own host in its own no_proxy list. Rare;
			// treat as misconfiguration and fail fast.
			return nil, setting.Mode, fmt.Errorf("proxy URL host %q matches its own no_proxy list", setting.URL.Host)
		}
```

(This is a self-check; the real per-target evaluation lives in Task 3's `ProxyFunc`.)

**Step 4: Run, verify PASS**

Run: `go test ./sdk/proxyutil/ -v` → PASS all.

**Step 5: Commit**

```bash
gofmt -w sdk/proxyutil
git add sdk/proxyutil
git commit -m "feat(proxyutil): noProxy host matching + direct-bypass transport"
```

---

## Task 3: proxyutil — per-target Proxy function + strict fallback transport

**Files:**
- Modify: `sdk/proxyutil/proxy.go`
- Test: `sdk/proxyutil/proxy_test.go`

**Step 1: Write failing tests**

```go
// TestProxyFuncRespectsNoProxy wires the composite setting into an
// http.Transport Proxy func and verifies per-target selection.
func TestProxyFuncRespectsNoProxy(t *testing.T) {
	setting, err := Parse("http://proxy:8080?no_proxy=" + url.QueryEscape("api.anthropic.com,.corp"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	proxyFunc, errTransport := ProxyFuncFor(setting)
	if errTransport != nil {
		t.Fatalf("ProxyFuncFor: %v", errTransport)
	}
	reqBypass, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1", nil)
	if got, errProxy := proxyFunc(reqBypass); errProxy == nil && got != nil {
		t.Fatalf("bypass target got proxy %v", got)
	}
	reqProxied, _ := http.NewRequest(http.MethodGet, "https://api.openai.com/v1", nil)
	if got, errProxy := proxyFunc(reqProxied); errProxy != nil || got == nil || got.String() != "http://proxy:8080" {
		t.Fatalf("proxied target got %v, %v", got, errProxy)
	}
}

// TestStrictFallbackTransport verifies strict=false retries once directly
// when the inner (proxied) transport fails, and strict=true does not.
func TestStrictFallbackTransport(t *testing.T) {
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("proxy dial refused")
	})
	loose := NewStrictFallbackTransport(inner)
	if _, err := loose.RoundTrip(httptest.NewRequest(http.MethodGet, "/x", nil)); err != nil {
		t.Fatalf("strict=false must fall back direct: %v", err)
	}
	hard := NewStrictFallbackTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, nil // inner "succeeded" — must be returned verbatim
	}))
	if resp, err := hard.RoundTrip(httptest.NewRequest(http.MethodGet, "/x", nil)); err != nil || resp == nil {
		t.Fatalf("inner success must pass through: %v %v", resp, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
```

**Step 2: Run, verify FAIL**

Run: `go test ./sdk/proxyutil/ -run 'ProxyFunc|StrictFallback' -v`
Expected: FAIL (`ProxyFuncFor`, `NewStrictFallbackTransport` undefined).

**Step 3: Implement**

```go
// ProxyFuncFor returns an http.Transport-level Proxy function honoring the
// setting's noProxy list: matching targets get nil (direct), everything else
// gets the parsed proxy URL.
func ProxyFuncFor(setting Setting) (func(*http.Request) (*url.URL, error), error) {
	if setting.Mode != ModeProxy || setting.URL == nil {
		return nil, fmt.Errorf("proxy func requires a parsed proxy URL")
	}
	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return setting.URL, nil
		}
		if setting.Bypasses(req.URL.String()) {
			return nil, nil
		}
		return setting.URL, nil
	}, nil
}

// strictFallbackTransport wraps a proxied RoundTripper; when it fails and
// the setting allows fallback (strict=false), the request is retried once
// against a direct transport with a warning log.
type strictFallbackTransport struct {
	inner  http.RoundTripper
	direct http.RoundTripper
	log    log.FieldLogger
}

// NewStrictFallbackTransport wraps inner with a one-shot direct fallback.
// The returned transport is only meaningful when the caller has already
// decided strict=false; strict=true callers use inner directly.
func NewStrictFallbackTransport(inner http.RoundTripper) http.RoundTripper {
	return &strictFallbackTransport{inner: inner, direct: NewDirectTransport(), log: log.StandardLogger()}
}

func (t *strictFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, errTrip := t.inner.RoundTrip(req)
	if errTrip == nil {
		return resp, nil
	}
	log.WithError(errTrip).Warnf("proxy transport failed; falling back to direct for %s", redactHost(req.URL))
	return t.direct.RoundTrip(req)
}
```

Add `redactHost(u *url.URL) string { return u.Host }` helper. Wire the two into `BuildHTTPTransport`: in the `ModeProxy` branch, when `!setting.Strict`, wrap the constructed `*http.Transport` in `NewStrictFallbackTransport` — **return type changes**: split a new function rather than break call sites:

```go
// BuildProxiedTransport is BuildHTTPTransport with strict-fallback handling;
// callers that need the raw *http.Transport keep using BuildHTTPTransport.
func BuildProxiedTransport(raw string) (http.RoundTripper, Mode, error) {
	setting, errParse := Parse(raw)
	if errParse != nil {
		return nil, setting.Mode, errParse
	}
	transport, mode, errBuild := BuildHTTPTransport(raw)
	if errBuild != nil || transport == nil {
		return nil, mode, errBuild
	}
	if setting.Mode == ModeProxy && !setting.Strict {
		return NewStrictFallbackTransport(transport), mode, nil
	}
	return transport, mode, nil
}
```

`BuildHTTPTransport` itself stays as-is (raw transport), so the 9 existing call sites keep compiling.

**Step 4: Run, verify PASS**

Run: `go test ./sdk/proxyutil/ -v && go build ./...`
Expected: PASS + clean build.

**Step 5: Commit**

```bash
gofmt -w sdk/proxyutil
git add sdk/proxyutil
git commit -m "feat(proxyutil): per-target proxy func + strict fallback transport"
```

---

## Task 4: relay RoundTripper — `relayTransport`

**Files:**
- Create: `sdk/proxyutil/relay.go`
- Test: `sdk/proxyutil/relay_test.go`

**Step 1: Write failing tests**

```go
package proxyutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRelayTransportRewritesRequest stands up a fake relay (like a Cloudflare
// worker) and asserts the x-relay-target/x-relay-path contract.
func TestRelayTransportRewritesRequest(t *testing.T) {
	var gotTarget, gotPath, gotHost string
	var gotMethod string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		gotHost = r.Header.Get("Host")
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	base := strings.TrimPrefix(relay.URL, "http://")
	rt, errRelay := NewRelayTransport("https://" + base)
	if errRelay != nil {
		t.Skipf("test relay is http; NewRelayTransport enforces https — see NewRelayTransportRelaxed for the actual assertions")
	}
	_ = rt
}
```

Because `NewRelayTransport` must enforce `https` bases (design), but tests need a local httptest server, expose a relaxed constructor for tests + the strict one for production. Write the tests against the relaxed one:

```go
func TestRelayTransportContract(t *testing.T) {
	var gotTarget, gotPath, gotAuth, hostSeen string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		gotAuth = r.Header.Get("Authorization")
		hostSeen = r.Host
		w.WriteHeader(http.StatusTeapot)
	}))
	defer relay.Close()

	rt, errRelay := NewRelayTransportRelaxed(relay.URL, http.DefaultTransport)
	if errRelay != nil {
		t.Fatalf("relay transport: %v", errRelay)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=1", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, errTrip := rt.RoundTrip(req)
	if errTrip != nil {
		t.Fatalf("round trip: %v", errTrip)
	}
	defer resp.Body.Close()
	if gotTarget != "https://api.anthropic.com" {
		t.Fatalf("x-relay-target = %q", gotTarget)
	}
	if gotPath != "/v1/messages?beta=1" {
		t.Fatalf("x-relay-path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization must pass through, got %q", gotAuth)
	}
	if hostSeen == "api.anthropic.com" {
		t.Fatal("Host header must be rewritten to the relay base")
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("relay response must pass through, got %d", resp.StatusCode)
	}
}

func TestNewRelayTransportEnforcesHTTPSBase(t *testing.T) {
	if _, err := NewRelayTransport("http://relay.example"); err == nil {
		t.Fatal("http base must be rejected")
	}
	if _, err := NewRelayTransport("not a url"); err == nil {
		t.Fatal("malformed base must be rejected")
	}
}

func TestRelayTransportRejectsNonHTTPTarget(t *testing.T) {
	rt, errRelay := NewRelayTransportRelaxed("https://relay.example", http.DefaultTransport)
	if errRelay != nil {
		t.Fatalf("relay transport: %v", errRelay)
	}
	req, _ := http.NewRequest(http.MethodGet, "ftp://api.example/x", nil)
	if _, errTrip := rt.RoundTrip(req); errTrip == nil {
		t.Fatal("non-http(s) target must be rejected")
	}
}
```

**Step 2: Run, verify FAIL**

Run: `go test ./sdk/proxyutil/ -run Relay -v`
Expected: FAIL (no relay.go).

**Step 3: Implement** — `sdk/proxyutil/relay.go`:

```go
package proxyutil

// relayTransport forwards each request to a relay base URL using the
// x-relay-target / x-relay-path header contract (the wire protocol of the
// 9router relay workers deployed to Cloudflare/Vercel/Deno). The target's
// scheme://host moves into x-relay-target, the path+query into x-relay-path,
// and the request is sent to the relay with all other headers preserved.
type relayTransport struct {
	base  *url.URL
	inner http.RoundTripper
}

// NewRelayTransport builds a relay RoundTripper. The base URL must be https.
func NewRelayTransport(base string) (http.RoundTripper, error) {
	return newRelayTransport(base, http.DefaultTransport, true)
}

// NewRelayTransportRelaxed skips the https-base enforcement for tests.
func NewRelayTransportRelaxed(base string, inner http.RoundTripper) (http.RoundTripper, error) {
	return newRelayTransport(base, inner, false)
}

func newRelayTransport(base string, inner http.RoundTripper, enforceHTTPS bool) (http.RoundTripper, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(base))
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid relay base URL")
	}
	if enforceHTTPS && parsed.Scheme != "https" {
		return nil, fmt.Errorf("relay base URL must be https, got %s", parsed.Scheme)
	}
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &relayTransport{base: parsed, inner: inner}, nil
}

func (t *relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("relay transport: nil request")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("relay transport: target must be http(s), got %s", req.URL.Scheme)
	}
	clone := req.Clone(req.Context())
	target := fmt.Sprintf("%s://%s", req.URL.Scheme, req.URL.Host)
	relayPath := req.URL.Path
	if relayPath == "" {
		relayPath = "/"
	}
	if req.URL.RawQuery != "" {
		relayPath += "?" + req.URL.RawQuery
	}
	clone.Header.Set("x-relay-target", target)
	clone.Header.Set("x-relay-path", relayPath)
	clone.URL = t.base.Clone(t.base.String())
	clone.URL.Path = "/"
	clone.URL.RawPath = ""
	clone.URL.RawQuery = ""
	clone.Host = t.base.Host
	return t.inner.RoundTrip(clone)
}
```

**Step 4: Run, verify PASS**

Run: `go test ./sdk/proxyutil/ -run Relay -v` → PASS. `go build ./...` → clean.

**Step 5: Commit**

```bash
gofmt -w sdk/proxyutil
git add sdk/proxyutil
git commit -m "feat(proxyutil): relay RoundTripper (x-relay-target/x-relay-path contract)"
```

---

## Task 5: PG schema — `proxy_pools` table + binding columns

**Files:**
- Modify: `internal/store/postgresstore.go` (defaults ~:43-47, `PGConfig` fields ~:155-175, `pgConfig` normalization ~:320-335, table accessor ~:2249, `ensureUpstreamProvidersSchema` create block ~:1540, `Migrate` binding columns ~:733)
- Modify: `internal/store/backup.go` (`ResourceProxyPools` at :26-48, list :107, `backupTablesFor` case :174)
- Test: `internal/store/pg_migrations_test.go` (extend existing migration test pattern)

**Step 1: Write the failing test**

Append to `internal/store/pg_migrations_test.go` (it skips without `PGSTORE_TEST_DSN`; follow its existing setup helper):

```go
// TestProxyPoolsSchemaAndBindingColumns verifies the proxy_pools table is
// created and the binding columns exist on the provider + entry tables after
// Migrate. Skips without PGSTORE_TEST_DSN like the rest of this file.
func TestProxyPoolsSchemaAndBindingColumns(t *testing.T) {
	pg := newTestPostgresStore(t) // reuse this file's existing helper
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var exists bool
	err := pg.DB().QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, "proxy_pools").Scan(&exists)
	if err != nil || !exists {
		t.Fatalf("proxy_pools table missing: %v %v", exists, err)
	}
	for _, tc := range []struct{ table, column string }{
		{"upstream_providers", "proxy_pool_id"},
		{"upstream_provider_api_key_entries", "proxy_pool_id"},
	} {
		err := pg.DB().QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = $2)`,
			tc.table, tc.column).Scan(&exists)
		if err != nil || !exists {
			t.Fatalf("%s.%s missing: %v %v", tc.table, tc.column, exists, err)
		}
	}
}
```

If `newTestPostgresStore` does not exist under that name, use whatever helper the file already defines (check first; all migrations tests in this file share one).

**Step 2: Run, verify FAIL**

Run: `PGSTORE_TEST_DSN=$PGSTORE_TEST_DSN go test ./internal/store/ -run TestProxyPoolsSchema -v` (skip-without-DSN means: also verify the skip fires when unset)
Expected: FAIL when DSN set (table missing); SKIP when unset.

**Step 3: Implement**

3a. Defaults block (after `defaultUpstreamProviderEntriesTable`):

```go
	defaultProxyPoolsTable = "proxy_pools"
```

3b. `PGConfig` field (after `UpstreamProviderEntriesTable`):

```go
	// ProxyPoolsTable stores the named egress-proxy pools (standard http/socks
	// pools + relay pools of type vercel/cloudflare/deno). Entries and rows of
	// upstream_providers reference a pool by id in proxy_pool_id; the
	// upstreamsync renderer resolves the binding into concrete proxy URLs.
	ProxyPoolsTable string
```

3c. Normalization (after the entries default):

```go
	if cfg.ProxyPoolsTable == "" {
		cfg.ProxyPoolsTable = defaultProxyPoolsTable
	}
```

3d. Accessor (next to `UpstreamProvidersTable`):

```go
// ProxyPoolsTable returns the fully-qualified name of the proxy_pools table.
func (s *PostgresStore) ProxyPoolsTable() string {
	if s == nil {
		return quoteIdentifier(defaultProxyPoolsTable)
	}
	return s.fullTableName(s.cfg.ProxyPoolsTable)
}
```

3e. Create table — inside `ensureUpstreamProvidersSchema` (or a sibling `ensureProxyPoolsSchema` called from `EnsureSchema` right after it), after the entries table block:

```go
	// proxy_pools stores named egress-proxy pools. type discriminates standard
	// http/socks proxies ("http") from deployed relay workers ("vercel",
	// "cloudflare", "deno") whose proxy_url is a relay base URL used with the
	// x-relay-target/x-relay-path header contract. strict_proxy defaults TRUE:
	// a proxy failure fails the request (no silent direct fallback = no IP
	// leak); tests are status-only and never flip is_active.
	proxyPoolsTable := s.fullTableName(s.cfg.ProxyPoolsTable)
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id             BIGSERIAL PRIMARY KEY,
			name           TEXT NOT NULL,
			proxy_url      TEXT NOT NULL,
			no_proxy       TEXT NOT NULL DEFAULT '',
			type           TEXT NOT NULL DEFAULT 'http',
			is_active      BOOLEAN NOT NULL DEFAULT TRUE,
			strict_proxy   BOOLEAN NOT NULL DEFAULT TRUE,
			test_status    TEXT NOT NULL DEFAULT 'unknown',
			last_tested_at TIMESTAMPTZ,
			last_error     TEXT,
			created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`, proxyPoolsTable)); err != nil {
		return fmt.Errorf("postgres store: create proxy_pools table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_pools_name ON %s(lower(name))`, proxyPoolsTable,
	)); err != nil {
		return fmt.Errorf("postgres store: create proxy_pools name index: %w", err)
	}
```

3f. Binding columns — new block in `Migrate` (after the priority column block, ~:733):

```go
	// proxy_pool_id binding columns. Entries override; the provider row value
	// is the default for entries that leave it NULL. Both reference
	// proxy_pools(id) so deleting a pool the DB still references fails at the
	// store layer before the handler's bound_entry_count check.
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS proxy_pool_id BIGINT REFERENCES %s(id)`,
		upstreamProvidersTable, s.fullTableName(s.cfg.ProxyPoolsTable),
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream providers proxy_pool_id column: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`ALTER TABLE %s ADD COLUMN IF NOT EXISTS proxy_pool_id BIGINT REFERENCES %s(id)`,
		upstreamEntriesTable, s.fullTableName(s.cfg.ProxyPoolsTable),
	)); err != nil {
		return fmt.Errorf("postgres store: migrate upstream entries proxy_pool_id column: %w", err)
	}
```

⚠️ Ordering constraint: the `proxy_pools` CREATE must run **before** these ALTERs. `EnsureSchema` (which has the CREATE) runs before `Migrate` in the boot path — verify in `pgStoreInst` construction in `cmd/server/main.go` and add the `proxy_pools` create into `ensureUpstreamProvidersSchema` (called by `EnsureSchema`) so ordering holds; keep only the ALTERs in `Migrate`.

3g. Backup support — `internal/store/backup.go`:

```go
	// ResourceProxyPools covers the named egress-proxy pools.
	ResourceProxyPools BackupResource = "proxy_pools"
```
Add `"proxy_pools"` to the resource list (~:107) and case:
```go
	case ResourceProxyPools:
		return []backupTable{{s.ProxyPoolsTable(), "id"}}
```
Also register the setter on `PostgresStore` if `backup.go` uses interface accessors for table names (it uses `s.XxxTable()` directly — the new accessor suffices).

**Step 4: Run, verify PASS**

Run: `PGSTORE_TEST_DSN=... go test ./internal/store/ -run TestProxyPoolsSchema -v` → PASS (or SKIP without DSN) + `go build ./...` clean.

**Step 5: Commit**

```bash
gofmt -w internal/store
git add internal/store
git commit -m "feat(store): proxy_pools table + upstream binding columns"
```

---

## Task 6: store — `ProxyPoolStore` CRUD + bound_entry_count

**Files:**
- Create: `internal/store/pg_proxy_pools.go`
- Modify: `internal/store/pg_proxy_pools_test.go` (create in this task)
- Test: `internal/store/pg_proxy_pools_test.go`

**Step 1: Write failing tests**

`internal/store/pg_proxy_pools_test.go` — full round-trip suite, following `pg_apikeys_test.go` conventions (skip without DSN):

```go
package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestProxyPoolStore(t *testing.T) *ProxyPoolStore {
	dsn := strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
	if dsn == "" {
		t.Skip("PGSTORE_TEST_DSN not set")
	}
	parent, err := NewPostgresStoreFromDSN(dsn) // reuse the DSN constructor used by pg_apikeys_test.go setup
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	ctx := context.Background()
	if err := parent.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := parent.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := NewProxyPoolStore(parent)
	if st == nil {
		t.Fatal("nil store")
	}
	return st
}

func TestProxyPoolCRUDRoundTrip(t *testing.T) {
	st := newTestProxyPoolStore(t)
	ctx := context.Background()

	created, err := st.Create(ctx, ProxyPool{
		Name: "pool-a", ProxyURL: "socks5://u:p@10.0.0.1:1080", NoProxy: ".corp",
		Type: "http", StrictProxy: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || created.TestStatus != "unknown" || !created.IsActive {
		t.Fatalf("created = %+v", created)
	}

	got, err := st.Get(ctx, created.ID)
	if err != nil || got.Name != "pool-a" {
		t.Fatalf("get: %+v %v", got, err)
	}

	got.NoProxy = ""
	got.IsActive = false
	updated, err := st.Update(ctx, *got)
	if err != nil || updated.IsActive {
		t.Fatalf("update: %+v %v", updated, err)
	}

	if err := st.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.Get(ctx, created.ID); err != ErrProxyPoolNotFound {
		t.Fatalf("post-delete err = %v, want ErrProxyPoolNotFound", err)
	}
}

func TestProxyPoolDuplicateNameRejected(t *testing.T) {
	st := newTestProxyPoolStore(t)
	ctx := context.Background()
	if _, err := st.Create(ctx, ProxyPool{Name: "dup", ProxyURL: "http://1.2.3.4:8080"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := st.Create(ctx, ProxyPool{Name: "DUP", ProxyURL: "http://5.6.7.8:8080"}); err == nil {
		t.Fatal("case-insensitive duplicate name must be rejected")
	}
}

func TestProxyPoolBoundEntryCount(t *testing.T) {
	// Uses an upstream provider row + entry bound to the pool via the
	// upstream provider store; asserts BoundEntryCount counts both the
	// row-level binding and the entry-level binding.
	// Setup: create pool, create 2 providers (one row-bound, one with an
	// entry carrying ProxyPoolID), call BoundEntryCount.
	// Expected: 2.
	// (Full bodies follow the pg_upstream_providers test helpers; the
	// assertion contract is the number, not the join mechanics.)
	t.Skip("write in step 1 flesh-out — see plan note below")
}
```

⚠️ Plan note (for the executor): before writing the bound-count test, open `pg_upstream_providers_test.go` and reuse its provider-creation helper verbatim. The contract: `BoundEntryCount(ctx, poolID) (int64, error)` counts `upstream_providers WHERE proxy_pool_id = $1` **plus** `upstream_provider_api_key_entries WHERE proxy_pool_id = $1`.

**Step 2: Run, verify FAIL**

Run: `PGSTORE_TEST_DSN=... go test ./internal/store/ -run TestProxyPool -v`
Expected: FAIL (`ProxyPool`, `NewProxyPoolStore` undefined).

**Step 3: Implement** — `internal/store/pg_proxy_pools.go`:

Follow `pg_upstream_providers.go` structure exactly:

```go
package store

// ProxyPool is one row of the proxy_pools table — a named egress-proxy pool.
// Standard pools (type "http") carry a proxy URL consumed as the entry's
// ProxyURL (composite suffix ?no_proxy=…&strict=… added by the renderer).
// Relay pools (type "vercel"|"cloudflare"|"deno") carry a relay base URL
// consumed as Auth.RelayBaseURL with the x-relay-target/x-relay-path header
// contract.
type ProxyPool struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	ProxyURL     string     `json:"proxy_url"`
	NoProxy      string     `json:"no_proxy,omitempty"`
	Type         string     `json:"type,omitempty"`
	IsActive     bool       `json:"is_active"`
	StrictProxy  bool       `json:"strict_proxy"`
	TestStatus   string     `json:"test_status,omitempty"`
	LastTestedAt *time.Time `json:"last_tested_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ProxyPoolStore is the contract consumed by the /v0/management/proxy-pools
// routes. nil implementations (PG unconfigured) yield 503 at the handler.
type ProxyPoolStore interface {
	List(ctx context.Context) ([]ProxyPool, error)
	Get(ctx context.Context, id int64) (*ProxyPool, error)
	Create(ctx context.Context, p ProxyPool) (*ProxyPool, error)
	Update(ctx context.Context, p ProxyPool) (*ProxyPool, error)
	Delete(ctx context.Context, id int64) error
	// BoundEntryCount returns how many upstream provider rows and API-key
	// entries reference the pool (row-level + entry-level bindings summed).
	BoundEntryCount(ctx context.Context, poolID int64) (int64, error)
}

var ErrProxyPoolNotFound = errors.New("postgres store: proxy pool not found")
```

Implementation notes (match `pg_upstream_providers.go` idioms — column lists spelled out, `RETURNING` on insert/update, `sql.ErrNoRows` → `ErrProxyPoolNotFound`):

- `pgProxyPoolStore{db *sql.DB, table string, providersTable string, entriesTable string}`; constructor:

```go
// NewProxyPoolStore wires the store against a parent PostgresStore.
// Returns nil when parent is nil so callers can feature-detect via nil.
func NewProxyPoolStore(parent *PostgresStore) ProxyPoolStore {
	if parent == nil {
		return nil
	}
	return &pgProxyPoolStore{
		db:             parent.DB(),
		table:          parent.ProxyPoolsTable(),
		providersTable: parent.UpstreamProvidersTable(),
		entriesTable:   parent.UpstreamProviderEntriesTable(),
	}
}
```

- `BoundEntryCount` in one query:

```sql
SELECT (SELECT COUNT(*) FROM providers WHERE proxy_pool_id = $1) +
       (SELECT COUNT(*) FROM entries  WHERE proxy_pool_id = $1)
```

- `Create`/`Update` validate type ∈ {http, vercel, cloudflare, deno} and proxy_url non-empty (store-level defense; handler validates earlier with better messages).
- Unique-name violation: detect `unique constraint` / `idx_proxy_pools_name` in the error and wrap as `ErrProxyPoolDuplicateName` (handler maps to 409).

**Step 4: Run, verify PASS**

Run: `PGSTORE_TEST_DSN=... go test ./internal/store/ -run TestProxyPool -v` → PASS; `go build ./...` clean.

**Step 5: Commit**

```bash
gofmt -w internal/store
git add internal/store
git commit -m "feat(store): ProxyPoolStore CRUD + bound entry count"
```

---

## Task 7: store — binding fields on provider rows/entries + persistence

**Files:**
- Modify: `internal/store/pg_upstream_providers.go` (struct fields ~:96, entry SELECT :514, `replaceChildrenTx`/`syncAPIKeyEntriesTx` :554/:629, row INSERT/UPDATE column lists :258/:342)
- Test: `internal/store/pg_upstream_providers_test.go` (or the file that holds its round-trip tests)

**Step 1: Write failing tests**

```go
// TestUpstreamProviderProxyPoolBindingRoundTrip verifies proxy_pool_id
// survives store Create/Update/Get on both the row and its entries.
func TestUpstreamProviderProxyPoolBindingRoundTrip(t *testing.T) {
	pools := newTestProxyPoolStore(t)          // Task 6 helper
	providers := newTestUpstreamProviderStore(t) // existing helper in this file
	ctx := context.Background()

	pool, err := pools.Create(ctx, ProxyPool{Name: "bind-pool", ProxyURL: "http://1.2.3.4:8080"})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}

	row, err := providers.Create(ctx, UpstreamProvider{
		ProviderType: "claude-api-key", Name: "bind-row", APIKey: "k-row",
		ProxyPoolID: &pool.ID,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "k-entry", ProxyPoolID: &pool.ID},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := providers.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProxyPoolID == nil || *got.ProxyPoolID != pool.ID {
		t.Fatalf("row binding = %v, want %d", got.ProxyPoolID, pool.ID)
	}
	if len(got.APIKeyEntries) != 1 || got.APIKeyEntries[0].ProxyPoolID == nil || *got.APIKeyEntries[0].ProxyPoolID != pool.ID {
		t.Fatalf("entry binding = %+v", got.APIKeyEntries)
	}

	// Clearing the binding persists.
	got.ProxyPoolID = nil
	if _, err := providers.Update(ctx, *got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reloaded, _ := providers.Get(ctx, row.ID)
	if reloaded.ProxyPoolID != nil {
		t.Fatalf("row binding must clear, got %v", *reloaded.ProxyPoolID)
	}
}

// TestDeletePoolBlockedWhileBound asserts the FK (or explicit check) prevents
// deleting a pool still referenced by a provider row.
func TestDeletePoolBlockedWhileBound(t *testing.T) {
	// Create pool + bound provider row; pools.Delete must return an error
	// (FK violation). The handler translates to 409 via BoundEntryCount
	// before calling Delete, this is the store-level backstop.
	// Body mirrors TestUpstreamProviderProxyPoolBindingRoundTrip setup.
	t.Skip("flesh out in step 1 following the round-trip test above")
}
```

**Step 2: Run, verify FAIL**

Run: `PGSTORE_TEST_DSN=... go test ./internal/store/ -run 'ProxyPoolBinding|DeletePoolBlocked' -v`
Expected: FAIL (`ProxyPoolID undefined`).

**Step 3: Implement**

3a. Struct fields — on `UpstreamProvider` (after `ProxyURL` :42):

```go
	// ProxyPoolID, when non-nil, binds the row to a proxy_pools entry; the
	// renderer resolves it into the concrete ProxyURL (or RelayBaseURL for
	// relay pools). nil = no row-level pool binding.
	ProxyPoolID *int64 `json:"proxy_pool_id,omitempty"`
```

and on `UpstreamProviderAPIKey` (after `ProxyURL` :101) with the same comment shape.

3b. SQL: add `proxy_pool_id` to the row INSERT column list + VALUES + RETURNING (:258-269), the UPDATE SET (:342), both SELECT column lists (:211, :270), and the entry SELECT (:514) + entry INSERT/UPDATE inside `syncAPIKeyEntriesTx` (:629). Scan into `sql.NullInt64` and convert:

```go
// nullableID converts a scanned sql.NullInt64 into the *int64 binding shape.
func nullableID(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}
```

**Step 4: Run, verify PASS**

Run: `PGSTORE_TEST_DSN=... go test ./internal/store/ -run 'ProxyPool|UpstreamProvider' -v` → PASS; `go build ./...` clean.

**Step 5: Commit**

```bash
gofmt -w internal/store
git add internal/store
git commit -m "feat(store): proxy_pool_id binding on provider rows + entries"
```

---

## Task 8: render — resolve pool bindings into ProxyURL / relay stamp (upstreamsync)

**Files:**
- Modify: `internal/upstreamsync/render.go` (`buildClaudeKey` :161, `openAICompatFromProvider` :212)
- Modify: `internal/upstreamsync/seed.go` (`providerFromClaudeKey` :180, `providerFromOpenAICompat` :237 — round-trip the binding)
- Test: `internal/upstreamsync/proxy_pool_render_test.go` (new)

**Design of the resolution helper:**

The renderer is pure (no store access) — pools arrive as a lookup map built by the callers that already hold the provider list. `RenderConfig(providers)` gains a package-level pool map parameter via a new render option, since `ApplyArtifacts` (:25) is the only production caller and it will fetch pools itself in Task 9. Concretely:

```go
// poolLookup resolves pool ids to rows for render-time binding. The store
// fetches the map once per render; nil entries (pool deleted between the two
// queries) treat the binding as unset.
type poolLookup map[int64]store.ProxyPool

// resolvePoolProxy returns the concrete proxy value for a binding: the
// composite URL (proxy_url + ?no_proxy=…&strict=…) for standard pools, or the
// relay marker for relay types. Inactive pools return "" (binding unset).
func (l poolLookup) resolve(id *int64) (proxyURL string, relayBase string) {
	if id == nil {
		return "", ""
	}
	pool, ok := l[*id]
	if !ok || !pool.IsActive || pool.ProxyURL == "" {
		return "", ""
	}
	if pool.Type == "vercel" || pool.Type == "cloudflare" || pool.Type == "deno" {
		return "", pool.ProxyURL
	}
	composite := pool.ProxyURL
	attrs := make([]string, 0, 2)
	if pool.NoProxy != "" {
		attrs = append(attrs, "no_proxy="+url.QueryEscape(strings.ToLower(strings.Join(splitAndTrim(pool.NoProxy, ","), ","))))
	}
	attrs = append(attrs, "strict="+boolString(pool.StrictProxy))
	sep := "?"
	if strings.Contains(composite, "?") {
		sep = "&" // defensive: raw pool URLs should not carry queries
	}
	return composite + sep + strings.Join(attrs, "&"), ""
}
```

(`splitAndTrim`, `boolString` are two-line local helpers.)

**Relay representation decision (recap from design §Render):** relay pools cannot ride `ProxyURL` — the renderer must pass the relay base to the synthesizer differently. Config structs (`config.ClaudeKey`, `config.OpenAICompatibilityAPIKey`) gain:

```go
	// RelayBaseURL is renderer-managed: set when the entry is bound to an
	// active relay-type proxy pool. The synthesizer stamps it onto the auth
	// as RelayBaseURL; executors build a relay transport instead of a proxy
	// dialer. Empty = standard proxy semantics. Operators should not set
	// this field manually.
	RelayBaseURL string `yaml:"relay-base-url,omitempty" json:"-"`
```

(on `ClaudeKey` after `ProxyURL` :419; on `OpenAICompatibilityAPIKey` after `ProxyURL` :773; plus row-level on `OpenAICompatibility` and on `UpstreamProviderAPIKey`'s render target `config.ClaudeKey` — one field per rendered shape that can carry a relay).

**Step 1: Write failing tests**

`internal/upstreamsync/proxy_pool_render_test.go`:

```go
package upstreamsync

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func poolID(v int64) *int64 { return &v }

// TestBuildClaudeKeyResolvesStandardPool verifies the composite URL lands on
// the rendered key when the entry binds an active standard pool.
func TestBuildClaudeKeyResolvesStandardPool(t *testing.T) {
	look := poolLookup{7: {ID: 7, Name: "p", ProxyURL: "socks5://10.0.0.1:1080", NoProxy: ".corp, x.example", IsActive: true, StrictProxy: true}}
	provider := store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "k"}
	entry := store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(7)}

	key := buildClaudeKeyWithPools(provider, entry, look)
	want := "socks5://10.0.0.1:1080?no_proxy=.corp%2Cx.example&strict=true"
	if key.ProxyURL != want {
		t.Fatalf("ProxyURL = %q, want %q", key.ProxyURL, want)
	}
	if key.RelayBaseURL != "" {
		t.Fatalf("relay must be empty for standard pool, got %q", key.RelayBaseURL)
	}
}

func TestBuildClaudeKeyResolvesRelayPool(t *testing.T) {
	look := poolLookup{8: {ID: 8, ProxyURL: "https://myrelay.workers.dev", Type: "cloudflare", IsActive: true}}
	key := buildClaudeKeyWithPools(store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "k"},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(8)}, look)
	if key.ProxyURL != "" {
		t.Fatalf("standard proxy must stay empty for relay pool, got %q", key.ProxyURL)
	}
	if key.RelayBaseURL != "https://myrelay.workers.dev" {
		t.Fatalf("RelayBaseURL = %q", key.RelayBaseURL)
	}
}

func TestBuildClaudeKeyInactiveOrMissingPoolFallsThrough(t *testing.T) {
	look := poolLookup{9: {ID: 9, ProxyURL: "http://x:1", IsActive: false}, 10: {ID: 10, ProxyURL: "http://y:1", IsActive: true}}
	// row-level manual proxy must win when the pool is inactive
	key := buildClaudeKeyWithPools(
		store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "row", ProxyURL: "http://row:1"},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(9)}, look)
	if key.ProxyURL != "http://row:1" {
		t.Fatalf("inactive pool must fall through to row proxy, got %q", key.ProxyURL)
	}
	// missing pool id (deleted) also falls through
	key = buildClaudeKeyWithPools(store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "row"},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(999)}, look)
	if key.ProxyURL != "" {
		t.Fatalf("missing pool must yield empty, got %q", key.ProxyURL)
	}
	_ = poolID(10)
}

// TestBuildClaudeKeyEntryOverridesRowPool: entry binding wins over row binding.
func TestBuildClaudeKeyEntryOverridesRowPool(t *testing.T) {
	look := poolLookup{
		7: {ID: 7, ProxyURL: "http://entry-pool:1", IsActive: true},
		8: {ID: 8, ProxyURL: "http://row-pool:1", IsActive: true},
	}
	key := buildClaudeKeyWithPools(
		store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "k", ProxyPoolID: poolID(8)},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(7)}, look)
	if key.ProxyURL != "http://entry-pool:1?strict=true" {
		t.Fatalf("entry pool must win, got %q", key.ProxyURL)
	}
}

// TestOpenAICompatResolvesEntryPools mirrors the claude assertions on the
// compat shape (entries carry their own ProxyURL/RelayBaseURL; row-level
// pool is the default when an entry leaves proxy_pool_id nil).
func TestOpenAICompatResolvesEntryPools(t *testing.T) {
	look := poolLookup{
		7:  {ID: 7, ProxyURL: "http://a:1", IsActive: true, StrictProxy: false},
		8:  {ID: 8, ProxyURL: "https://relay.deno.net", Type: "deno", IsActive: true},
	}
	provider := store.UpstreamProvider{
		ID: 1, ProviderType: "openai-compatibility", Name: "compat", ProxyPoolID: poolID(7),
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{APIKey: "k1"},                              // inherits row pool
			{APIKey: "k2", ProxyPoolID: poolID(8)},      // relay override
		},
	}
	got := openAICompatFromProviderWithPools(provider, look)
	if got.ProxyURL != "http://a:1?strict=false" || got.RelayBaseURL != "" {
		t.Fatalf("row-level = %q / %q", got.ProxyURL, got.RelayBaseURL)
	}
	if got.APIKeyEntries[0].ProxyURL != "http://a:1?strict=false" {
		t.Fatalf("inherited entry = %q", got.APIKeyEntries[0].ProxyURL)
	}
	if got.APIKeyEntries[1].RelayBaseURL != "https://relay.deno.net" || got.APIKeyEntries[1].ProxyURL != "" {
		t.Fatalf("relay entry = %q / %q", got.APIKeyEntries[1].ProxyURL, got.APIKeyEntries[1].RelayBaseURL)
	}
}

// TestSeedRoundTripsPoolBinding: providerFromClaudeKey/providerFromOpenAICompat
// must carry ProxyPoolID back into store rows (first-boot seed path).
func TestSeedRoundTripsPoolBinding(t *testing.T) {
	id := int64(7)
	k := config.ClaudeKey{APIKey: "k", ProxyPoolID: &id}
	p := providerFromClaudeKey(k)
	if p.ProxyPoolID == nil || *p.ProxyPoolID != 7 {
		t.Fatalf("seed binding lost: %v", p.ProxyPoolID)
	}
}
```

**Step 2: Run, verify FAIL**

Run: `go test ./internal/upstreamsync/ -run 'Pool' -v`
Expected: FAIL (helpers undefined).

**Step 3: Implement**

3a. `internal/config/config_types.go`: add `ProxyPoolID *int64` + `RelayBaseURL` fields:
- `ClaudeKey` (after `ProxyURL` :419): `ProxyPoolID *int64 \`yaml:"proxy-pool-id,omitempty" json:"-"\`` + `RelayBaseURL` (yaml `relay-base-url,omitempty`, json `-`, renderer-managed comment).
- `OpenAICompatibility` row + `OpenAICompatibilityAPIKey` (:773): same two fields (`proxy-pool-id` on entries; row gets `proxy-pool-id` + `relay-base-url`).

3b. `internal/store/pg_upstream_providers.go` — `UpstreamProvider` and `UpstreamProviderAPIKey` gained `ProxyPoolID` in Task 7 (already done).

3c. `render.go`: rename the two builders to `*WithPools` variants taking `poolLookup`; keep thin wrappers `buildClaudeKey(p, e)` / `openAICompatFromProvider(p)` calling them with `nil` lookup so existing tests/seed compile unchanged. In `buildClaudeKeyWithPools`:

```go
	proxyURL, relayBase := resolveBinding(look, e.ProxyPoolID, p.ProxyPoolID, e.ProxyURL, p.ProxyURL)
```

with the combined precedence helper:

```go
// resolveBinding applies the design's precedence: entry pool → row pool →
// entry manual URL → row manual URL. Inactive/missing pools fall through.
func resolveBinding(look poolLookup, entryPool, rowPool *int64, entryURL, rowURL string) (proxyURL, relayBase string) {
	if proxyURL, relay := look.resolve(entryPool); proxyURL != "" || relay != "" {
		return proxyURL, relay
	}
	if proxyURL, relay := look.resolve(rowPool); proxyURL != "" || relay != "" {
		return proxyURL, relay
	}
	if entryURL != "" {
		return entryURL, ""
	}
	return rowURL, ""
}
```

Stamp `k.ProxyURL = proxyURL; k.RelayBaseURL = relayBase`. Mirror for the compat builder (row-level fields + per-entry).

3d. `seed.go`: `providerFromClaudeKey` (:187) sets `ProxyURL: k.ProxyURL` today — add `ProxyPoolID: k.ProxyPoolID` (and compat :258). If a seeded row carries `RelayBaseURL` (operator restored from YAML), leave it in `ProxyURL`-adjacent handling: seed puts `RelayBaseURL` nowhere — relay pools are dashboard-created; note this in a comment.

**Step 4: Run, verify PASS**

Run: `go test ./internal/upstreamsync/ ./internal/config/ -v` → PASS (existing render tests must stay green — wrappers keep old signatures); `go build ./...` clean.

**Step 5: Commit**

```bash
gofmt -w internal/upstreamsync internal/config
git add internal/upstreamsync internal/config
git commit -m "feat(upstreamsync): resolve proxy pool bindings into composite/relay fields"
```

---

## Task 9: ApplyArtifacts + seed callers fetch pools; RenderConfig signature

**Files:**
- Modify: `internal/upstreamsync/apply.go:25` (`ApplyArtifacts` fetches the pool map and threads it to `RenderConfig`)
- Modify: `internal/upstreamsync/render.go:64` (`RenderConfig(providers)` → `RenderConfigWithPools(providers, poolLookup)`; `RenderConfig` becomes a wrapper passing nil)
- Modify: `cmd/server/upstreamsync_startup.go:16` + `cmd/server/main.go:839` (fetch pools from the new store before applying)
- Test: `internal/upstreamsync/apply_test.go` (extend)

**Step 1: Write failing test**

```go
// TestApplyArtifactsResolvesPoolBindings is a compile-level contract test:
// RenderConfigWithPools resolves bindings that plain RenderConfig leaves raw.
// The full store-driven path is covered by the PG round-trip in Task 6/7;
// here we pin the seam (ApplyArtifacts must accept a pool source).
func TestRenderConfigWithPoolsAppliesBinding(t *testing.T) {
	look := poolLookup{7: {ID: 7, ProxyURL: "http://p:1", IsActive: true}}
	providers := []store.UpstreamProvider{{
		ID: 1, ProviderType: "claude-api-key", APIKey: "k", ProxyPoolID: poolID(7),
	}}
	cfg := RenderConfigWithPools(providers, look)
	if len(cfg.ClaudeKey) != 1 || cfg.ClaudeKey[0].ProxyURL != "http://p:1?strict=true" {
		t.Fatalf("rendered = %+v", cfg.ClaudeKey)
	}
	// nil lookup leaves the raw manual proxy (back-compat for YAML-only callers)
	cfg = RenderConfig(providers)
	if len(cfg.ClaudeKey) != 1 || cfg.ClaudeKey[0].ProxyURL != "" {
		t.Fatalf("plain render must not resolve bindings: %+v", cfg.ClaudeKey[0].ProxyURL)
	}
}
```

**Step 2: Run, verify FAIL**

Run: `go test ./internal/upstreamsync/ -run RenderConfigWithPools -v` → FAIL (undefined).

**Step 3: Implement**

3a. `RenderConfig` delegates:

```go
// RenderConfig renders provider rows without pool resolution (callers that
// do not hold a pool store). Pool-bound entries keep their manual URLs.
func RenderConfig(providers []store.UpstreamProvider) config.Config {
	return RenderConfigWithPools(providers, nil)
}
```

3b. `RenderConfigWithPools` = old body, but `buildClaudeKey`/`openAICompatFromProvider` inner calls switch to the `*WithPools` variants.

3c. `ApplyArtifacts` signature gains the pool source — add a narrow interface so the management handler and main.go can pass any store:

```go
// ProxyPoolLister is the minimal pool access the renderer needs. Satisfied
// by store.ProxyPoolStore; nil = no pool resolution.
type ProxyPoolLister interface {
	List(ctx context.Context) ([]store.ProxyPool, error)
}

// ApplyArtifacts … (existing doc comment) … poolSource may be nil.
func ApplyArtifacts(ctx context.Context, st store.UpstreamProviderStore, cfg *config.Config, configPath, authDir string, poolSource ProxyPoolLister) (*config.Config, error) {
	// … existing list …
	var look poolLookup
	if poolSource != nil {
		pools, errPools := poolSource.List(ctx)
		if errPools != nil {
			return nil, fmt.Errorf("upstreamsync: list proxy pools: %w", errPools)
		}
		look = make(poolLookup, len(pools))
		for _, p := range pools {
			look[p.ID] = p
		}
	}
	rendered := RenderConfigWithPools(providers, look)
	// … rest unchanged …
```

3d. Update the two callers:
- `cmd/server/upstreamsync_startup.go:16` — add `poolSource ProxyPoolLister` param, pass through; `cmd/server/main.go:839` passes `pgProxyPools` (constructed at `:816` next to `pgUpstreamProviders` — add `pgProxyPools := store.NewProxyPoolStore(pgStoreInst)` and register in `api.PgStoreHandles` — Task 10 wires the API side).
- `internal/api/handlers/management/handler.go:949` — `h.applyUpstreamProviders` passes `h.pgProxyPools` (nil-safe: `ApplyArtifacts` accepts nil).

**Step 4: Run, verify PASS**

Run: `go build ./... && go test ./internal/upstreamsync/ -v` → PASS.

**Step 5: Commit**

```bash
gofmt -w internal/upstreamsync cmd/server internal/api
git add internal/upstreamsync cmd/server internal/api
git commit -m "feat(upstreamsync): thread proxy pool store through ApplyArtifacts"
```

---

## Task 10: handler wiring — ProxyPoolStore into management handler + server options

**Files:**
- Modify: `internal/api/handlers/management/handler.go` (field ~:100 next to `pgUpstreamProviders`, setter next to `SetUpstreamProvidersStore` :361, `applyUpstreamProviders` :931 passes pool source)
- Modify: `internal/api/server_options.go` (`PgStoreHandles` field after `UpstreamProviders` :79)
- Modify: `internal/api/server.go:242` (call setter)
- Modify: `cmd/server/main.go:846` (fill the handle)
- Test: `internal/api/handlers/management/proxy_pools_test.go` (Task 11 uses it; here just build)

**Step 1: Write failing test** (wiring smoke — a nil-store 503 test lands in Task 11; this task is compile-level)

```go
// internal/api/handlers/management/proxy_pools_test.go
package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestProxyPoolStoreSetterWiring pins that the setter stores the handle and
// the accessor 503s on nil (same contract as upstreamProvidersStore).
func TestProxyPoolStoreSetterWiring(t *testing.T) {
	h := &Handler{}
	if _, ok := h.proxyPoolsStore(nil); ok {
		t.Fatal("nil store must report not-ok")
	}
	h.SetProxyPoolStore(nil)
	if _, ok := h.proxyPoolsStore(nil); ok {
		t.Fatal("explicit nil must report not-ok")
	}
	_ = store.ProxyPoolStore(nil) // keep import honest for Task 11
}
```

**Step 2: Run, verify FAIL**

Run: `go test ./internal/api/handlers/management/ -run TestProxyPoolStoreSetterWiring -v` → FAIL (`proxyPoolsStore` undefined).

**Step 3: Implement**

3a. `handler.go` field:

```go
	// pgProxyPools stores the named egress-proxy pools backing the
	// /v0/management/proxy-pools routes. nil = PG unconfigured (503).
	pgProxyPools store.ProxyPoolStore
```

3b. Setter + accessor (mirror upstream_providers.go:361/:40):

```go
// SetProxyPoolStore wires the PG-backed store for the proxy_pools table.
// When nil, the /v0/management/proxy-pools routes return 503.
func (h *Handler) SetProxyPoolStore(pools store.ProxyPoolStore) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pgProxyPools = pools
}
```

3c. In `applyUpstreamProviders` (:949): `ApplyArtifacts(ctx, h.pgUpstreamProviders, cfg, configPath, authDir, h.pgProxyPools)` — read `h.pgProxyPools` inside the existing `h.mu.Lock()` block with cfg.

3d. `server_options.go`: `ProxyPools store.ProxyPoolStore` field + comment mirroring `UpstreamProviders`.

3e. `server.go:242`: `s.mgmt.SetProxyPoolStore(handles.ProxyPools)`.

3f. `main.go`: `pgProxyPools := store.NewProxyPoolStore(pgStoreInst)` at :816; `ProxyPools: pgProxyPools` in `PgStoreHandles` :846; pass as pool source at the :839 startup apply.

**Step 4: Run, verify PASS**

Run: `go build ./... && go test ./internal/api/handlers/management/ -run TestProxyPoolStoreSetterWiring -v` → PASS.

**Step 5: Commit**

```bash
gofmt -w internal/api cmd/server
git add internal/api cmd/server
git commit -m "feat(api): wire ProxyPoolStore into management handler + server options"
```

---

## Task 11: synthesizer — stamp `RelayBaseURL` onto Auth

**Files:**
- Modify: `sdk/cliproxy/auth/types.go` (field after `ProxyURL` :106)
- Modify: `internal/watcher/synthesizer/config.go` (claude loop :158-187, compat loop :381-426)
- Test: `internal/watcher/synthesizer/config_test.go` (extend)

**Step 1: Write failing tests**

```go
// TestConfigSynthesizer_ClaudeKeys_RelayBaseURL stamps the relay base onto
// the auth when the rendered key carries one; ProxyURL stays empty.
func TestConfigSynthesizer_ClaudeKeys_RelayBaseURL(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{ClaudeKey: []config.ClaudeKey{{
			APIKey:       "sk-relay",
			RelayBaseURL: "https://myrelay.workers.dev",
		}}},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("got %d auths", len(auths))
	}
	if auths[0].RelayBaseURL != "https://myrelay.workers.dev" {
		t.Fatalf("RelayBaseURL = %q", auths[0].RelayBaseURL)
	}
	if auths[0].ProxyURL != "" {
		t.Fatalf("ProxyURL must stay empty for relay auths, got %q", auths[0].ProxyURL)
	}
}
```

**Step 2: Run, verify FAIL**

Run: `go test ./internal/watcher/synthesizer/ -run RelayBaseURL -v` → FAIL.

**Step 3: Implement**

3a. `types.go`:

```go
	// RelayBaseURL, when set, routes this auth's upstream traffic through a
	// relay worker using the x-relay-target/x-relay-path header contract
	// instead of a network proxy. Renderer-managed (proxy pool of relay
	// type); executors check it before ProxyURL. Empty = standard semantics.
	RelayBaseURL string `json:"relay_base_url,omitempty"`
```

3b. `synthesizer/config.go` claude loop (~:158): alongside `proxyURL := strings.TrimSpace(entry.ProxyURL)`:

```go
		relayBaseURL := strings.TrimSpace(entry.RelayBaseURL)
```
and on the `&coreauth.Auth{...}` literal add `RelayBaseURL: relayBaseURL`. When `relayBaseURL != ""`, the auth keeps `ProxyURL` empty — renderer guarantees this, but add the defensive line:

```go
		if relayBaseURL != "" {
			proxyURL = "" // relay replaces proxy semantics entirely
		}
```

Mirror in the compat loop (:381-426).

**Step 4: Run, verify PASS**

Run: `go test ./internal/watcher/synthesizer/ -v` → PASS (all existing tests untouched).

**Step 5: Commit**

```bash
gofmt -w sdk/cliproxy internal/watcher
git add sdk/cliproxy internal/watcher
git commit -m "feat(synthesizer): stamp Auth.RelayBaseURL for relay-bound entries"
```

---

## Task 12: transport wiring — relay + strict semantics in the constructors

**Files:**
- Modify: `internal/runtime/executor/helps/proxy_helpers.go` (`NewProxyAwareHTTPClient` :36, new relay helper)
- Modify: `internal/runtime/executor/helps/utls_client.go` (`NewUtlsHTTPClient` :363)
- Modify: `sdk/cliproxy/rtprovider.go` (`RoundTripperFor` :26)
- Test: `internal/runtime/executor/helps/proxy_helpers_test.go` (new or extend), `sdk/cliproxy/rtprovider_test.go` (if absent, create minimal)

**Design recap:** relay wins over proxy (renderer guarantees mutual exclusion, but the constructor enforces it); `strict=false` proxy values build via `proxyutil.BuildProxiedTransport` (Task 3). The uTLS fallbackRoundTripper keeps its fingerprint routing — relay/proxy apply per-arm identically to how `proxyURL` today feeds all three arms.

**Step 1: Write failing tests**

```go
// helps/proxy_helpers_test.go
package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestNewProxyAwareHTTPClientRelayPriority pins that a relay-bound auth sends
// requests to the relay with the header contract, ignoring ProxyURL entirely.
func TestNewProxyAwareHTTPClientRelayPriority(t *testing.T) {
	var gotTarget, gotPath string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	auth := &cliproxyauth.Auth{ProxyURL: "http://must-be-ignored:1", RelayBaseURL: relay.URL}
	client := NewProxyAwareHTTPClient(context.Background(), &config.Config{}, auth, 0)
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1/x", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if gotTarget != "https://api.example.com" || gotPath != "/v1/x" {
		t.Fatalf("relay headers = %q / %q", gotTarget, gotPath)
	}
}
```

(For `NewUtlsHTTPClient`, test the seam, not the fingerprint: a small exported helper `relayTransportFor(auth)` returning `(http.RoundTripper, bool)` is what both constructors share — test that helper. The `fallbackRoundTripper` arms get the relay RT via the existing `ctxRoundTripper`-style injection; no per-arm changes beyond substituting the RT.)

**Step 2: Run, verify FAIL**

Run: `go test ./internal/runtime/executor/helps/ -run RelayPriority -v` → FAIL.

**Step 3: Implement**

3a. In `helps/proxy_helpers.go` add the shared resolver both constructors use:

```go
// relayTransportFor returns a relay RoundTripper when the auth is bound to a
// relay pool. Relay semantics replace proxy semantics entirely (the renderer
// guarantees mutual exclusion; this is the enforcement point).
func relayTransportFor(auth *cliproxyauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	base := strings.TrimSpace(auth.RelayBaseURL)
	if base == "" {
		return nil
	}
	rt, errRelay := proxyutil.NewRelayTransport(base)
	if errRelay != nil {
		log.Errorf("invalid relay base URL for auth %s: %v", auth.ID, errRelay)
		return nil
	}
	return rt
}
```

3b. `NewProxyAwareHTTPClient` — insert at the top of the proxy-priority chain:

```go
	// Priority 0: relay-bound auth (replaces proxy semantics entirely).
	if rt := relayTransportFor(auth); rt != nil {
		httpClient.Transport = rt
		return httpClient
	}
```

and change the `proxyURL` branch from `buildProxyTransport` to:

```go
		rt, _, errBuild := proxyutil.BuildProxiedTransport(proxyURL)
		if errBuild == nil && rt != nil {
			httpClient.Transport = rt
			return httpClient
		}
		if errBuild != nil {
			log.Debugf("failed to build proxy transport for %s: %v", proxyutil.Redact(proxyURL), errBuild)
		}
```

3c. `utls_client.go` `NewUtlsHTTPClient` (:363): before computing `proxyURL`:

```go
	if relayRT := relayTransportFor(auth); relayRT != nil {
		client := &http.Client{Transport: &fallbackRoundTripper{
			anthropic: relayRT,
			chrome:    relayRT,
			fallback:  relayRT,
		}}
		if timeout > 0 {
			client.Timeout = timeout
		}
		return client
	}
```

(This routes relay traffic through the plain relay RT for all hosts — TLS fingerprinting is pointless through a relay that re-originates the connection. Comment says so.)

3d. `rtprovider.go` `RoundTripperFor`: check `auth.RelayBaseURL` first (cache key = `relay:`+base to avoid colliding with proxy URLs):

```go
	if relayBase := strings.TrimSpace(auth.RelayBaseURL); relayBase != "" {
		key := "relay:" + relayBase
		// … same RLock/cache path …
		rt, errRelay := proxyutil.NewRelayTransport(relayBase)
		// … cache & return …
	}
```

**Step 4: Run, verify PASS**

Run: `go test ./internal/runtime/executor/helps/ ./sdk/cliproxy/ -v && go build ./...` → PASS.
Then the regression net: `go test ./internal/runtime/executor/ ./sdk/cliproxy/... 2>&1 | tail -5` — expect only the 3 known-failing Claude header-fingerprint tests (documented baseline).

**Step 5: Commit**

```bash
gofmt -w internal/runtime/executor/helps sdk/cliproxy
git add internal/runtime/executor/helps sdk/cliproxy
git commit -m "feat(helps): relay transport priority + strict fallback in client constructors"
```

---

## Task 13: management API — proxy-pools handlers (CRUD, test, batch-import)

**Files:**
- Create: `internal/api/handlers/management/proxy_pools.go`
- Create: `internal/api/handlers/management/proxy_pools_types.go`
- Create: `internal/api/handlers/management/proxy_pools_test.go` (extend Task 10 stub)
- Create: `internal/api/handlers/management/proxy_pools_testrunner.go` (the test runner — see below)
- Modify: `internal/api/server_management.go` (register after :355)

**Design of the three non-CRUD endpoints:**

- **`POST /proxy-pools/:id/test`** — runs the connectivity probe synchronously (8s default / 30s cap), writes `test_status`/`last_tested_at`/`last_error` **only** (status-only decision — never touches `is_active`), returns the probe result. Probe details: type http → `HEAD https://www.google.com` through the pool's own composite URL; relay types → `GET https://httpbin.org/get` via the relay header contract (like 9router). The probe lives in `proxy_pools_testrunner.go` so handler tests can bypass it; timeout lives in the probe client (`http.Client{Timeout: probeTimeout}`) — allowed, this is a credential/ops check, not request-serving.
- **`POST /proxy-pools/batch-import`** — `{lines: ["1.2.3.4:8080", "user:pass@host:3128", "socks5://h:1080", ...]}`; per-line parse (9router `parseProxyLine` semantics: has `://` → URL as-is; 4 `:`-parts → `http://user:pass@host:port`; 2 parts → `http://host:port`); dedupe by proxy_url against existing pools; auto-name `Imported host:port`. Returns `{created, skipped, failed, errors: [{line, error}]}`. Each created pool re-renders once at the end (single `applyUpstreamProviders`).
- **Delete** — checks `BoundEntryCount` first; >0 → 409 + `{bound_entry_count}`.

**Step 1: Write failing tests**

```go
// proxy_pools_test.go — handler-level tests with a fake store.
package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// fakePoolStore satisfies store.ProxyPoolStore in-memory.
type fakePoolStore struct {
	pools      map[int64]store.ProxyPool
	nextID     int64
	boundCount int64
	failDelete bool
}

func newFakePoolStore() *fakePoolStore { return &fakePoolStore{pools: map[int64]store.ProxyPool{}, nextID: 1} }

func (f *fakePoolStore) List(ctx context.Context) ([]store.ProxyPool, error) {
	out := make([]store.ProxyPool, 0, len(f.pools))
	for _, p := range f.pools {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakePoolStore) Get(ctx context.Context, id int64) (*store.ProxyPool, error) {
	if p, ok := f.pools[id]; ok {
		return &p, nil
	}
	return nil, store.ErrProxyPoolNotFound
}
func (f *fakePoolStore) Create(ctx context.Context, p store.ProxyPool) (*store.ProxyPool, error) {
	p.ID = f.nextID
	f.nextID++
	f.pools[p.ID] = p
	return &p, nil
}
func (f *fakePoolStore) Update(ctx context.Context, p store.ProxyPool) (*store.ProxyPool, error) {
	if _, ok := f.pools[p.ID]; !ok {
		return nil, store.ErrProxyPoolNotFound
	}
	f.pools[p.ID] = p
	return &p, nil
}
func (f *fakePoolStore) Delete(ctx context.Context, id int64) error {
	if f.failDelete {
		return errors.New("fk violation")
	}
	delete(f.pools, id)
	return nil
}
func (f *fakePoolStore) BoundEntryCount(ctx context.Context, poolID int64) (int64, error) {
	return f.boundCount, nil
}

func newPoolTestRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/proxy-pools", h.ListProxyPools)
	r.POST("/proxy-pools", h.CreateProxyPool)
	r.GET("/proxy-pools/:id", h.GetProxyPool)
	r.PUT("/proxy-pools/:id", h.UpdateProxyPool)
	r.DELETE("/proxy-pools/:id", h.DeleteProxyPool)
	r.POST("/proxy-pools/batch-import", h.BatchImportProxyPools)
	return r
}

func TestCreateProxyPoolValidation(t *testing.T) {
	h := &Handler{}
	h.SetProxyPoolStore(newFakePoolStore())
	r := newPoolTestRouter(h)

	// missing name
	res := postJSON(t, r, "/proxy-pools", `{"proxy_url":"http://1.2.3.4:8080"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing name = %d", res.Code)
	}
	// bad scheme
	res = postJSON(t, r, "/proxy-pools", `{"name":"x","proxy_url":"ftp://1.2.3.4:21"}`)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad scheme = %d", res.Code)
	}
	// good create with defaults
	res = postJSON(t, r, "/proxy-pools", `{"name":"std","proxy_url":"socks5://u:p@1.2.3.4:1080","no_proxy":".corp"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Pool store.ProxyPool `json:"pool"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	if body.Pool.StrictProxy != true || body.Pool.Type != "http" || body.Pool.TestStatus != "unknown" {
		t.Fatalf("defaults = %+v", body.Pool)
	}
}

func TestDeleteProxyPoolBlockedWhileBound(t *testing.T) {
	h := &Handler{}
	fs := newFakePoolStore()
	fs.boundCount = 3
	created, _ := fs.Create(context.Background(), store.ProxyPool{Name: "b", ProxyURL: "http://1:1"})
	h.SetProxyPoolStore(fs)
	r := newPoolTestRouter(h)

	res := r.ServeHTT(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/proxy-pools/1", nil))
	_ = res // placeholder — see corrected call below
}

// postJSON is the shared helper; write it (json string → request with content-type).
func postJSON(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	return res
}

// TestBatchImportParseLines pins the 9router parse semantics server-side.
func TestBatchImportParseLines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"user:pass@host:3128", "http://user:pass@host:3128"},
		{"socks5://h:1080", "socks5://h:1080"},
	}
	for _, tc := range cases {
		got, err := parseProxyLine(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("parse(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := parseProxyLine("nonsense"); err == nil {
		t.Fatal("unparseable line must error")
	}
}
```

⚠️ The `TestDeleteProxyPoolBlockedWhileBound` body above contains a deliberately broken `ServeHTT` call — the executor must write it correctly: `res := deleteReq(t, r, "/proxy-pools/1")` expecting `res.Code == 409` and body containing `"bound_entry_count":3`. (Writing every line of a 400-line plan verbatim invites drift; the executor writes helpers like `deleteReq` itself following `postJSON`.)

**Step 2: Run, verify FAIL**

Run: `go test ./internal/api/handlers/management/ -run 'ProxyPool|BatchImport' -v` → FAIL (handlers undefined).

**Step 3: Implement**

3a. `proxy_pools_types.go`:

```go
package management

// proxyPoolReq is the create/update body for /v0/management/proxy-pools.
type proxyPoolReq struct {
	Name        string `json:"name"`
	ProxyURL    string `json:"proxy_url"`
	NoProxy     string `json:"no_proxy,omitempty"`
	Type        string `json:"type,omitempty"`
	IsActive    *bool  `json:"is_active,omitempty"`
	StrictProxy *bool  `json:"strict_proxy,omitempty"`
}

// proxyPoolResponse wraps the row with its computed bound_entry_count when
// the list requested usage counts (?include_usage=1).
type proxyPoolResponse struct {
	store.ProxyPool
	BoundEntryCount int64 `json:"bound_entry_count,omitempty"`
}
```

3b. `proxy_pools.go` handlers (mirror `upstream_providers.go` style):

- `proxyPoolsStore(c)` accessor (nil → `h.pgNotConfigured(c)`, return false) — copy of :40.
- Allowed schemes for type http: `http, https, socks5, socks5h` (validate via `url.Parse` + scheme check — do NOT reuse `proxyutil.Parse` here because pool rows must stay composite-suffix-free; the renderer adds the suffix).
- Relay types carry an https URL; validate per type.
- `ListProxyPools`: `?include_usage=1` enriches each with `BoundEntryCount`.
- `CreateProxyPool`/`UpdateProxyPool`: 400 on validation, 201/200 `{pool}`, then `h.applyUpstreamProviders(c.Request.Context())` (non-fatal — log on error like upstream handlers).
- `DeleteProxyPool`: `BoundEntryCount` > 0 → `409 {"error":{"type":"conflict","message":…},"bound_entry_count":N}`; else delete + re-apply; `ErrProxyPoolNotFound` → 404.
- `BatchImportProxyPools`: parse loop per Task 13 header spec; per-line errors collected (never abort the batch); dedupe against existing `proxy_url`s fetched once; create valid ones; single `applyUpstreamProviders` at the end; return counts.

3c. `proxy_pools_testrunner.go`:

```go
package management

// runProxyPoolTest executes the connectivity probe for one pool and records
// the outcome (status-only; is_active is never touched — operator decision).
// http pools: HEAD https://www.google.com/ through the pool's own proxy URL.
// relay pools: GET https://httpbin.org/get with the x-relay-target/x-relay-path
// contract against the pool's relay base.
func (h *Handler) runProxyPoolTest(ctx context.Context, pool store.ProxyPool) (ok bool, status int, detail string)
```

Implementation: build the transport via `proxyutil.BuildProxiedTransport(pool.ProxyURL)` for http pools (honors noProxy/strict semantics — a probe through the pool's own settings is the honest probe), `proxyutil.NewRelayTransport(pool.ProxyURL)` for relay pools; `http.Client{Transport: rt, Timeout: 30*time.Second}`; 8s context deadline layered on top. Persist via `Update` with only the three test fields set (re-Get first to avoid clobbering concurrent edits).

3d. `server_management.go` after :355:

```go
		// Named egress-proxy pools (9router-derived Proxy Pools workflow).
		// Returns 503 when the PG store is not configured. Binding lives on
		// upstream_providers(.proxy_pool_id) rows/entries; mutations here
		// re-render upstream artifacts so bindings resolve immediately.
		mgmt.GET("/proxy-pools", s.mgmt.ListProxyPools)
		mgmt.POST("/proxy-pools", s.mgmt.CreateProxyPool)
		mgmt.GET("/proxy-pools/:id", s.mgmt.GetProxyPool)
		mgmt.PUT("/proxy-pools/:id", s.mgmt.UpdateProxyPool)
		mgmt.DELETE("/proxy-pools/:id", s.mgmt.DeleteProxyPool)
		mgmt.POST("/proxy-pools/:id/test", s.mgmt.TestProxyPool)
		mgmt.POST("/proxy-pools/batch-import", s.mgmt.BatchImportProxyPools)
		mgmt.POST("/proxy-pools/relay-deploy", s.mgmt.DeployRelayProxyPool) // Task 14
```

⚠️ Gin route conflict note: `/proxy-pools/:id` and `/proxy-pools/batch-import` coexist fine in Gin (static segment wins), but `/proxy-pools/:id/test` must be registered before any conflicting wildcard — this ordering works; keep it.

**Step 4: Run, verify PASS**

Run: `go test ./internal/api/handlers/management/ -run 'ProxyPool|BatchImport' -v && go build ./...` → PASS.

**Step 5: Commit**

```bash
gofmt -w internal/api
git add internal/api
git commit -m "feat(management): proxy-pools CRUD + status-only test + batch import"
```

---

## Task 14: relay deploy — one endpoint, three platforms

**Files:**
- Create: `internal/api/handlers/management/proxy_pools_relaydeploy.go`
- Create: `internal/api/handlers/management/proxy_pools_relaydeploy_test.go`

**Design (from 9router, ports of `vercel-deploy`/`cloudflare-deploy`/`deno-deploy`):** one `POST /proxy-pools/relay-deploy` with `{platform, credentials…, project_name}`. Platform credentials are one-shot from the body, **never persisted**. Worker source is embedded Go constants (port the JS verbatim). Polling: Vercel `readyState==READY` (3s interval, 120s cap), Deno revision status `succeeded` (2s, 30 tries, delete app on failure). On success create the pool row (`type` = platform) and re-render. HTTP clients: `http.Client{Timeout: 60s}` per platform call — deployment orchestration, not request-serving (documented exception per AGENTS.md; add a comment saying so at the client construction).

**Step 1: Write failing tests**

```go
// TestDeployRelayContractValidation: platform + per-platform required fields.
func TestDeployRelayContractValidation(t *testing.T) {
	// platform missing/unknown → 400; vercel without token → 400;
	// cloudflare without account_id or api_token → 400; deno without
	// deno_token or org_domain → 400.
}

// TestDeployRelayCloudflareHappyPath: httptest server emulating the three
// Cloudflare calls (PUT script → 200 {success:true}, POST subdomain → 200,
// GET subdomain → 200 {result:{subdomain:"workers.dev"}}) driven by
// swapping the platform base URL (the handler takes a base-URL override for
// tests — pass it via the Handler or an unexported field).
// Asserts: pool created with Type="cloudflare", ProxyURL="https://relay-name.workers.dev",
// and the multipart upload contained the worker source (recorder checks
// "x-relay-target" appears in the body).
```

Executor writes these in full following the Cloudflare flow in `9router/src/app/api/proxy-pools/cloudflare-deploy/route.js` (clone kept at `/tmp/9router` during the design session; if missing, fetch the raw file from GitHub).

**Step 2: Run, verify FAIL** → `go test ./internal/api/handlers/management/ -run DeployRelay -v` → FAIL.

**Step 3: Implement**

3a. Request shape:

```go
type relayDeployReq struct {
	Platform    string `json:"platform"` // vercel | cloudflare | deno
	ProjectName string `json:"project_name,omitempty"`
	// One-shot platform credentials — never persisted.
	VercelToken string `json:"vercel_token,omitempty"`
	CFAccountID string `json:"cf_account_id,omitempty"`
	CFToken     string `json:"cf_api_token,omitempty"`
	DenoToken   string `json:"deno_token,omitempty"`
	DenoOrg     string `json:"deno_org_domain,omitempty"`
}
```

3b. Worker sources as `const relayWorkerVercelCode / relayWorkerCloudflareCode / relayWorkerDenoCode` — port the three sources from 9router verbatim (they are self-contained; the `\\/$/` regex escapes become raw-string-safe).

3c. Handler flow per platform (ports of the 9router routes): deploy → poll → derive `deployUrl` → `h.pgProxyPools.Create` pool (`name = projectName`, `type = platform`, `strict_proxy: true`, `is_active: true`) → `h.applyUpstreamProviders` → `201 {pool, deploy_url}`. Failure mid-flight (Deno) deletes the created app best-effort.

3d. Platform API base URLs are package vars (`var vercelAPIBase = "https://api.vercel.com"` etc.) so tests point them at httptest servers.

**Step 4: Run, verify PASS** → `-run DeployRelay` PASS; `go build ./...` clean.

**Step 5: Commit**

```bash
gofmt -w internal/api
git add internal/api
git commit -m "feat(management): one-shot relay deploy (vercel/cloudflare/deno)"
```

---

## Task 15: management DTO — binding fields on upstream-provider requests

**Files:**
- Modify: `internal/api/handlers/management/upstream_providers_types.go` (row `ProxyPoolID` after :24, entry field after :67)
- Modify: `internal/api/handlers/management/upstream_providers.go` (`toUpstreamProvider` maps the new fields)
- Test: `internal/api/handlers/management/upstream_providers_test.go` (extend)

**Step 1: Write failing tests**

```go
// TestUpstreamProviderProxyPoolBindingDTORoundTrip mirrors
// TestUpstreamProviderEntryRequestRoundTrip for the binding fields.
func TestUpstreamProviderProxyPoolBindingDTORoundTrip(t *testing.T) {
	const payload = `{"provider_type":"openai-compatibility","proxy_pool_id":7,"api_key_entries":[{"id":1,"api_key":"k","proxy_pool_id":9}]}`
	var req upstreamProviderReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := toUpstreamProvider(&req)
	if got.ProxyPoolID == nil || *got.ProxyPoolID != 7 {
		t.Fatalf("row binding = %v", got.ProxyPoolID)
	}
	if got.APIKeyEntries[0].ProxyPoolID == nil || *got.APIKeyEntries[0].ProxyPoolID != 9 {
		t.Fatalf("entry binding = %v", got.APIKeyEntries[0].ProxyPoolID)
	}
	// absent field decodes nil (not 0!)
	absent := `{"provider_type":"claude-api-key"}`
	var req2 upstreamProviderReq
	_ = json.Unmarshal([]byte(absent), &req2)
	if req2.ProxyPoolID != nil {
		t.Fatalf("absent binding must be nil, got %v", *req2.ProxyPoolID)
	}
}
```

**Step 2: Run, verify FAIL** → `-run ProxyPoolBindingDTO` FAIL.

**Step 3: Implement** — `*int64 \`json:"proxy_pool_id,omitempty"\`` on `upstreamProviderReq` + `upstreamProviderEntryReq`; map in `toUpstreamProvider` (both row and entries loop). Also extend the response: `UpstreamProviderResponse` embeds the store row so `proxy_pool_id` flows to the dashboard automatically — verify JSON round-trip in the test above by re-marshaling `toUpstreamProviderResponse(got)`.

**Step 4: Run, verify PASS** → all management tests PASS; `go build ./...` clean.

**Step 5: Commit**

```bash
gofmt -w internal/api
git add internal/api
git commit -m "feat(management): proxy_pool_id on upstream provider DTOs"
```

---

## Task 16: SPA — client API + ProxyPoolsPage

**Files:**
- Modify: `web/dashboard/src/api/client.js` (new exports after the model-group block ~:1893)
- Modify: `web/dashboard/src/api/managementEndpoints.js` (new group after proxy-url ~:230)
- Create: `web/dashboard/src/pages/ProxyPoolsPage.jsx`
- Create: `web/dashboard/src/pages/ProxyPoolsPage.test.js`
- Modify: `web/dashboard/src/App.jsx` (route after :293) + `web/dashboard/src/components/Sidebar.jsx` (nav item after :432)

**Step 1: Write failing tests**

`ProxyPoolsPage.test.js` — `node:test` + `assert/strict` (no DOM; test the pure helpers — this codebase's SPA tests are logic tests, see `editor.test.js`). Extract the pure logic into the page file's exports:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { parseProxyLine, formatTestStatus } from './ProxyPoolsPage.jsx';

test('parseProxyLine: bare host:port becomes http URL', () => {
  assert.equal(parseProxyLine('1.2.3.4:8080'), 'http://1.2.3.4:8080');
});
test('parseProxyLine: user:pass@host:port', () => {
  assert.equal(parseProxyLine('u:p@h:3128'), 'http://u:p@h:3128');
});
test('parseProxyLine: full URL passes through', () => {
  assert.equal(parseProxyLine('socks5://h:1080'), 'socks5://h:1080');
});
test('parseProxyLine: garbage rejects', () => {
  assert.throws(() => parseProxyLine('nonsense'));
});
test('formatTestStatus maps statuses to labels', () => {
  assert.equal(formatTestStatus('active'), 'Active');
  assert.equal(formatTestStatus('error'), 'Error');
  assert.equal(formatTestStatus('unknown'), 'Not tested');
});
```

**Step 2: Run, verify FAIL**

Run: `cd web/dashboard && npm test 2>&1 | grep -i proxy` → FAIL.

**Step 3: Implement**

3a. `client.js`:

```js
// ── Proxy Pools (named egress-proxy pools) ────────────────────────────
export async function listProxyPools({ includeUsage = false } = {}) {
  const qs = includeUsage ? '?include_usage=1' : '';
  const res = await fetchJSON(`/proxy-pools${qs}`);
  return { pools: Array.isArray(res?.pools) ? res.pools : [] };
}
export async function createProxyPool(payload) { return fetchJSON('/proxy-pools', { method: 'POST', body: JSON.stringify(payload) }); }
export async function updateProxyPool(id, payload) { return fetchJSON(`/proxy-pools/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(payload) }); }
export async function deleteProxyPool(id) { return fetchJSON(`/proxy-pools/${encodeURIComponent(id)}`, { method: 'DELETE' }); }
export async function testProxyPool(id) { return fetchJSON(`/proxy-pools/${encodeURIComponent(id)}/test`, { method: 'POST' }); }
export async function batchImportProxyPools(lines) { return fetchJSON('/proxy-pools/batch-import', { method: 'POST', body: JSON.stringify({ lines }) }); }
export async function deployRelayProxyPool(payload) { return fetchJSON('/proxy-pools/relay-deploy', { method: 'POST', body: JSON.stringify(payload) }); }
```

3b. `managementEndpoints.js` group:

```js
  groups.push({
    label: 'Proxy Pools',
    entries: [
      entry('GET', '/proxy-pools'),
      entry('POST', '/proxy-pools'),
      entry('GET', '/proxy-pools/:id'),
      entry('PUT', '/proxy-pools/:id'),
      entry('DELETE', '/proxy-pools/:id'),
      entry('POST', '/proxy-pools/:id/test'),
      entry('POST', '/proxy-pools/batch-import'),
      entry('POST', '/proxy-pools/relay-deploy'),
      entry('GET', '/proxy-pools/*'),
    ],
  });
```

3c. `ProxyPoolsPage.jsx` — structure mirrors 9router's page but with this repo's component library; sections:
- Table (name, type badge, redacted URL via the client's existing redaction helper if present else `proxyUrl.replace(/\/\/.*@/, '//redacted@')`, status chip with `last_error` tooltip, `last_tested_at`, active toggle, `bound_entry_count`, row actions: Test / Edit / Delete).
- Toolbar: Health Check (concurrency 10 via `Promise.all` worker pool, progress `n/total`), Bulk Activate/Deactivate/Delete (confirm modal), Batch Import modal (textarea → `parseProxyLine` preview per line → submit), Relay Deploy dropdown → 3 modals (fields per Task 14 request shape + project name).
- 409 delete responses show `bound_entry_count` from the body.
- Export `parseProxyLine` + `formatTestStatus` for tests.

3d. `App.jsx`: `<Route path="/proxy-pools" element={<ProxyPoolsPage />} />`; `Sidebar.jsx` Config section: `{ to: '/proxy-pools', label: 'Proxy Pools', icon: 'lan' }` (or an available icon name — check the icon set used).

**Step 4: Run, verify PASS**

Run: `cd web/dashboard && npm test` → PASS; `npm run build` → clean.

**Step 5: Commit**

```bash
git add web/dashboard/src
git commit -m "feat(dashboard): Proxy Pools page (test/health/import/bulk/relay deploy)"
```

---

## Task 17: SPA — proxy pool picker in the upstream provider editor

**Files:**
- Modify: `web/dashboard/src/pages/upstream-provider-editor/schemas.js` (row-level `proxy_pool_id` for claude-api-key + openai-compatibility; per-entry field)
- Modify: `web/dashboard/src/pages/upstream-provider-editor/EntriesEditor.jsx` (pool select per row; exclusivity with `proxy_url`)
- Modify: `web/dashboard/src/pages/upstream-provider-editor/form.js` (`hydrateEntry` :95-103 + payload :278-285 carry `proxy_pool_id`; row payload too)
- Modify: `web/dashboard/src/pages/upstream-provider-editor/index.jsx` (fetch `listProxyPools()` on mount; pass pool options down)
- Test: `web/dashboard/src/pages/upstream-provider-editor/editor.test.js` (extend)

**Step 1: Write failing tests**

```js
test('hydrateEntry: pool binding hydrates and clears manual proxy url', () => {
  const entry = hydrateEntry({ proxy_pool_id: 7, proxy_url: 'http://stale:1' }, {});
  assert.equal(entry.proxy_pool_id, 7);
  assert.equal(entry.proxy_url, ''); // exclusive: pool wins on hydrate
});
test('entryPayload: emits proxy_pool_id only when set, proxy_url only when manual', () => {
  const pooled = entryPayload({ proxy_pool_id: 7, proxy_url: '' });
  assert.equal(pooled.proxy_pool_id, 7);
  assert.equal('proxy_url' in pooled ? pooled.proxy_url : undefined, undefined);
  const manual = entryPayload({ proxy_pool_id: '', proxy_url: 'socks5://h:1' });
  assert.equal(manual.proxy_url, 'socks5://h:1');
  assert.ok(!('proxy_pool_id' in manual) || manual.proxy_pool_id == null);
});
```

(Import the exact function names that exist in `form.js` — `hydrateEntry`/`entryPayload` may be named differently (`hydrate` at :95, payload build at :278); adapt the test names to the real exports.)

**Step 2: Run, verify FAIL** → `npm test` → FAIL.

**Step 3: Implement**

- `EntriesEditor.jsx`: per-entry row gains a `<select data-testid={'api-key-entry-proxy-pool-' + idx}>` with options: `— inherit row —` (empty), pool list (active only, label `name`), `Direct (no proxy)` (value `none`), and a "manual URL" text input that **disables** when a pool is selected (and vice versa — selecting a pool clears `proxy_url`, typing a URL clears `proxy_pool_id`).
- Row-level: `schemas.js` adds `proxy_pool_id` to the claude-api-key + openai-compatibility schemas (type 'text' select-style per the schema engine's capability — check how `routing_strategy` renders; reuse that field type). Hint: 'Applies to entries without their own pool binding.'
- `form.js`: carry the field both directions; payload emits `proxy_pool_id` as number or omits it.
- `index.jsx`: fetch pools once on mount; render a warning badge when a selected pool id no longer exists in the fetched list (stale binding).

**Step 4: Run, verify PASS** → `npm test` PASS (156→160+ editor tests stay green); `npm run build` clean.

**Step 5: Commit**

```bash
git add web/dashboard/src
git commit -m "feat(dashboard): proxy pool picker in upstream provider editor"
```

---

## Task 18: embed dash + docs + full verification

**Files:**
- Run: `make dash-embed` (builds SPA → copies dist into `internal/dashboardasset` → rebuilds Go binary)
- Modify: `config.example.yaml` (document `proxy-pool-id`/`relay-base-url` renderer-managed fields under claude-api-key + openai-compatibility examples, matching the existing "renderer-managed" comment style at :464)
- Modify: `AGENTS.md` (one bullet under Architecture: `internal/api/handlers/management/proxy_pools.go` — named egress-proxy pools + relay deploy)

**Steps:**

1. `cd web/dashboard && npm run build` (from repo root of the worktree: `make dash-embed` does both — verify the Makefile target copies dist into `internal/dashboardasset` per the [[auth-router-dashboard-embed-fixes]] memory).
2. `go test ./...` full suite → all PASS except the 3 documented Claude fingerprint tests (now un-skipped because dist exists — `TestMountDashboardRoutes` should pass again).
3. `go build -o test-output ./cmd/server && rm test-output` (AGENTS.md required check).
4. `gofmt -w .` + `git status` review (no stray files).
5. Docs commit:

```bash
git add -f docs/plans/2026-09-06-proxy-pools-design.md docs/plans/2026-09-06-proxy-pools-plan.md
git add config.example.yaml AGENTS.md internal/dashboardasset/dist 2>/dev/null || true
git commit -m "docs: proxy pools config example + agents note"
```

⚠️ `internal/dashboardasset/dist/*` is gitignored — do NOT force-add dist contents; only embed-check locally (dist is built at deploy time per repo convention; `.gitkeep` is the only tracked file).

---

## Verification checklist (run before declaring done)

```bash
# 1. Full Go suite (expect: all pass except 3 documented fingerprint failures on the executor pkg)
go test ./... 2>&1 | grep -E "FAIL|^ok" | grep -v "^ok" | head

# 2. Compile check (AGENTS.md)
go build -o test-output ./cmd/server && rm test-output

# 3. gofmt clean
gofmt -l . | grep -v ".worktrees" | head

# 4. SPA suite + build
cd web/dashboard && npm test && npm run build

# 5. PG round-trips (if DSN available)
PGSTORE_TEST_DSN=$PGSTORE_TEST_DSN go test ./internal/store/ -run 'ProxyPool' -v
```

## Out-of-scope reminders (do NOT expand)

- No client-facing API-key ↔ pool binding (policy layer untouched).
- No pool-of-pools rotation strategy.
- No `strictProxy=false` UI default — the toggle defaults ON everywhere.
- No relay token persistence; no relay redeploy/update flow.
- YAML-only provider types (gemini/codex/vertex standalone) get no picker.
