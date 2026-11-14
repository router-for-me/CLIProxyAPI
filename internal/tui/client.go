package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client wraps HTTP calls to the management API.
//
// The client prefers the v8 management contract and falls back to the
// deprecated /v0/management routes only when the v8 route is not implemented
// (404/405/501) by the target server. Configuration reads normalize the v8
// tree-shaped document into the flat key set the TUI presents, so the tab
// models keep working on either contract.
type Client struct {
	baseURL   string
	secretKey string
	http      *http.Client

	// probeMu guards the cached result of whether the target server exposes the
	// v8 management contract, so a 404 on a missing config node is not mistaken
	// for an absent route. Transient errors (network failure, 5xx) are not
	// cached and are reported separately from a definitive "unsupported".
	probeMu     sync.Mutex
	v8Probed    bool
	v8Supported bool
}

// NewClient creates a new management API client targeting localhost on the given port.
func NewClient(port int, secretKey string) *Client {
	return NewClientWithBaseURL(fmt.Sprintf("http://127.0.0.1:%d", port), secretKey)
}

// NewClientWithBaseURL creates a new management API client targeting the specified base URL.
func NewClientWithBaseURL(baseURL string, secretKey string) *Client {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8317"
	} else {
		lower := strings.ToLower(baseURL)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			baseURL = "http://" + baseURL
		}
		baseURL = strings.TrimRight(baseURL, "/")
	}
	return &Client{
		baseURL:   baseURL,
		secretKey: strings.TrimSpace(secretKey),
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// BaseURL returns the client's configured management API base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// SetSecretKey updates management API bearer token used by this client.
func (c *Client) SetSecretKey(secretKey string) {
	c.secretKey = strings.TrimSpace(secretKey)
}

func (c *Client) doRequest(method, path string, body io.Reader) ([]byte, int, http.Header, error) {
	return c.doRequestHeaders(method, path, body, nil)
}

func (c *Client) doRequestHeaders(method, path string, body io.Reader, headers http.Header) ([]byte, int, http.Header, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, 0, nil, err
	}
	if c.secretKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.secretKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range headers {
		// Set so a caller-supplied Content-Type replaces the JSON default.
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	return data, resp.StatusCode, resp.Header, nil
}

// isRouteUnsupported reports whether an HTTP status means the route does not
// exist on this server, so the caller may fall back to the other contract.
func isRouteUnsupported(code int) bool {
	return code == http.StatusNotFound || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented
}

// supportsV8 reports whether the target server implements the v8 management
// contract. The verdict is cached per client. A reachable /v8/management/config
// proves the contract exists; only an unimplemented root route (404/405/501)
// means the server predates v8. The second return value is false when the
// probe could not get a definitive answer (network error or 5xx), in which
// case the result is not cached and callers must not silently treat config
// reads as empty.
func (c *Client) supportsV8() (supported, definitive bool) {
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	if c.v8Probed {
		return c.v8Supported, true
	}
	_, code, err := c.getJSONStatus("/v8/management/config")
	if err != nil && code == 0 {
		// Transport-level failure: no definitive verdict, do not cache.
		return false, false
	}
	switch {
	case code == http.StatusNotFound || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented:
		// Definitively unimplemented.
		c.v8Probed = true
		c.v8Supported = false
		return false, true
	case code == http.StatusOK:
		if err != nil {
			// Reachable route with an undecodable body: non-definitive.
			return false, false
		}
		c.v8Probed = true
		c.v8Supported = true
		return true, true
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		// Auth-gated: the request was challenged before routing ran, so this
		// does NOT prove the v8 contract exists (an auth-first proxy can 401
		// any path on a v0-only server). Report non-definitive and do not
		// cache, so per-request fallback keeps working; the caller may still
		// proceed to authentication on either contract.
		return false, false
	default:
		// 429, 5xx, and any other status cannot prove the contract exists;
		// treat as non-definitive and do not cache.
		return false, false
	}
}

func (c *Client) get(path string) ([]byte, error) {
	data, code, _, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *Client) put(path string, body io.Reader) ([]byte, error) {
	data, code, _, err := c.doRequest("PUT", path, body)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *Client) patch(path string, body io.Reader) ([]byte, error) {
	data, code, _, err := c.doRequest("PATCH", path, body)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// getJSON fetches a path and unmarshals JSON into a generic map.
func (c *Client) getJSON(path string) (map[string]any, error) {
	data, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// getJSONStatus returns the decoded JSON object plus the HTTP status so callers
// can detect unsupported routes and fall back to the other contract.
func (c *Client) getJSONStatus(path string) (map[string]any, int, error) {
	data, code, _, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, code, err
	}
	if code >= 400 {
		return nil, code, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, code, err
	}
	return result, code, nil
}

// getJSONV8 fetches v8Path first and falls back to v0Path only when the v8
// route is not implemented by the server.
func (c *Client) getJSONV8(v8Path, v0Path string) (map[string]any, error) {
	result, code, err := c.getJSONStatus(v8Path)
	if err == nil {
		return result, nil
	}
	if isRouteUnsupported(code) {
		return c.getJSON(v0Path)
	}
	return nil, err
}

// ----- Config layout mapping -----

// flatToV8ConfigPath maps a flat v0 config field name used by the TUI to its
// path inside the v8 configuration tree. Fields not present here keep their
// own (already nested) path, so e.g. "quota-exceeded/switch-project" and
// "routing/strategy" resolve to the same v8 location.
var flatToV8ConfigPath = map[string]string{
	"debug":                               "observability/logs/debug",
	"logging-to-file":                     "observability/logs/logging-to-file",
	"logs-max-total-size-mb":              "observability/logs/logs-max-total-size-mb",
	"error-logs-max-files":                "observability/logs/error-logs-max-files",
	"request-log":                         "observability/logs/request-log",
	"usage-statistics-enabled":            "observability/usage/usage-statistics-enabled",
	"proxy-url":                           "requests/proxy-url",
	"request-retry":                       "routing/retry/request-retry",
	"max-retry-interval":                  "routing/retry/max-retry-interval",
	"max-retry-credentials":               "routing/retry/max-retry-credentials",
	"force-model-prefix":                  "routing/force-model-prefix",
	"ws-auth":                             "oauth/providers/aistudio/ws-auth",
	"routing/strategy":                    "routing/strategy",
	"quota-exceeded/switch-project":       "quota-exceeded/switch-project",
	"quota-exceeded/switch-preview-model": "quota-exceeded/switch-preview-model",
}

// v8ConfigPath resolves a TUI field path to its v8 config path.
func v8ConfigPath(flat string) string {
	if p, ok := flatToV8ConfigPath[flat]; ok {
		return p
	}
	return flat
}

// errV8ConfigNodeMissing is returned by v8ConfigGet when the v8 contract is
// available but the requested config node does not exist (the server returns
// 404 {"error":"not_found"} for an absent field). Callers treat this as an
// empty value rather than falling back to v0.
var errV8ConfigNodeMissing = fmt.Errorf("v8 config node not set")

// v8ConfigGet fetches a node from the v8 config tree. The returned value is the
// raw decoded JSON node at that path. A 404 on an existing contract maps to
// errV8ConfigNodeMissing; a 404 because the route itself is absent is returned
// as an HTTP status error so callers can still fall back to v0.
func (c *Client) v8ConfigGet(path string) (any, int, error) {
	value, code, _, err := c.v8ConfigGetWithHeaders(path)
	return value, code, err
}

// v8ConfigGetWithHeaders is v8ConfigGet plus the response headers, so callers
// performing a read-modify-write can reuse the config ETag for If-Match.
func (c *Client) v8ConfigGetWithHeaders(path string) (any, int, http.Header, error) {
	data, code, hdr, err := c.doRequest("GET", "/v8/management/config/"+path, nil)
	if err != nil {
		return nil, code, hdr, err
	}
	if code == http.StatusNotFound {
		if supported, definitive := c.supportsV8(); definitive && supported {
			// The v8 contract exists; the node itself is absent. When the node
			// GET did not echo an ETag, fall back to the root config ETag so a
			// create-on-missing-node write can still be conditional.
			if hdr == nil || hdr.Get("ETag") == "" {
				if rootHdr := c.rootConfigHeaders(); rootHdr != nil {
					if hdr == nil {
						hdr = http.Header{}
					}
					hdr.Set("ETag", rootHdr.Get("ETag"))
				}
			}
			return nil, code, hdr, errV8ConfigNodeMissing
		}
	}
	if code >= 400 {
		return nil, code, hdr, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, code, hdr, err
	}
	return result, code, hdr, nil
}

// rootConfigHeaders returns the response headers of a GET on the v8 root
// config document, used to obtain a config ETag for conditional writes when a
// child node does not yet exist.
func (c *Client) rootConfigHeaders() http.Header {
	_, code, hdr, err := c.doRequest("GET", "/v8/management/config", nil)
	if err != nil || code >= 400 {
		return nil
	}
	return hdr
}

// flattenV8Config converts the v8 tree-shaped configuration document into the
// flat key set the TUI tabs read. Fields the TUI displays are projected to
// their v8 location with server-aligned defaults; provider groups are kept
// under api-keys-providers for reference. Fields with no flat equivalent are
// not surfaced on the flat view.
func flattenV8Config(v8 map[string]any) map[string]any {
	flat := make(map[string]any, len(v8)+16)
	get := func(path ...string) any {
		cur := any(v8)
		for _, p := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[p]
		}
		return cur
	}
	// setDef records the v8 value, or the supplied default when the field is
	// absent from the persisted document. Defaults mirror internal/config
	// runtime defaults so the TUI shows the effective value, not a zero value.
	setDef := func(key string, v any, def any) {
		if v == nil {
			flat[key] = def
			return
		}
		flat[key] = v
	}

	// Numeric defaults use float64 because the TUI reads them via getFloat,
	// which accepts only float64/json.Number.
	setDef("port", get("server", "port"), float64(0))
	setDef("host", get("server", "host"), "")
	setDef("debug", get("observability", "logs", "debug"), false)
	setDef("proxy-url", get("requests", "proxy-url"), "")
	setDef("request-retry", get("routing", "retry", "request-retry"), float64(0))
	setDef("max-retry-interval", get("routing", "retry", "max-retry-interval"), float64(0))
	setDef("force-model-prefix", get("routing", "force-model-prefix"), false)
	setDef("logging-to-file", get("observability", "logs", "logging-to-file"), false)
	setDef("logs-max-total-size-mb", get("observability", "logs", "logs-max-total-size-mb"), float64(0))
	setDef("error-logs-max-files", get("observability", "logs", "error-logs-max-files"), float64(10))
	setDef("usage-statistics-enabled", get("observability", "usage", "usage-statistics-enabled"), false)
	setDef("request-log", get("observability", "logs", "request-log"), false)
	if v := get("quota-exceeded"); v != nil {
		flat["quota-exceeded"] = v
	}
	if v := get("routing"); v != nil {
		flat["routing"] = v
	}
	setDef("ws-auth", get("oauth", "providers", "aistudio", "ws-auth"), true)
	if v := get("access", "api-keys"); v != nil {
		flat["api-keys"] = v
	}
	if v := get("api-keys"); v != nil {
		flat["api-keys-providers"] = v
	}
	return flat
}

// ErrManagementUnsupported is returned by CheckManagementSupport when neither
// the v8 nor the v0 management contract is reachable on the target server.
var ErrManagementUnsupported = fmt.Errorf("management API not supported by target server")

// mgmtReachable classifies one management-config probe.
type mgmtReachable int

const (
	mgmtUnreachable mgmtReachable = iota // transport failure / no verdict
	mgmtUnsupported                      // definitive 404/405/501: route absent
	mgmtAuthGated                        // 401/403: a management gate exists
	mgmtOK                               // 200: contract served
)

// probeManagement classifies a single config endpoint probe.
func (c *Client) probeManagement(path string) (mgmtReachable, error) {
	_, code, err := c.getJSONStatus(path)
	switch {
	case err == nil:
		return mgmtOK, nil
	case code == 0:
		return mgmtUnreachable, err
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return mgmtAuthGated, nil
	case isRouteUnsupported(code):
		return mgmtUnsupported, nil
	case code >= 500 || code == http.StatusTooManyRequests:
		return mgmtUnreachable, err
	default:
		return mgmtUnreachable, err
	}
}

// CheckManagementSupport reports whether the target exposes a usable
// management API on either contract. It is definitive only when a contract is
// actually served or clearly absent; a transient failure on one contract does
// not get reported as "unsupported" while the other is merely unreachable.
func (c *Client) CheckManagementSupport() error {
	v8, errV8 := c.probeManagement("/v8/management/config")
	if v8 == mgmtOK || v8 == mgmtAuthGated {
		return nil
	}
	v0, errV0 := c.probeManagement("/v0/management/config")
	if v0 == mgmtOK || v0 == mgmtAuthGated {
		return nil
	}
	// Only a definitive "route absent" on BOTH contracts is unsupported.
	if v8 == mgmtUnsupported && v0 == mgmtUnsupported {
		return ErrManagementUnsupported
	}
	// Otherwise we could not prove support (e.g. one unreachable, one absent):
	// surface the most informative error without claiming "unsupported".
	if errV8 != nil {
		return fmt.Errorf("management API probe failed: %w", errV8)
	}
	if errV0 != nil {
		return fmt.Errorf("management API probe failed: %w", errV0)
	}
	return ErrManagementUnsupported
}

// GetConfig fetches the parsed config. It prefers the v8 document, normalized
// to the flat shape the TUI presents, and falls back to the legacy v0 config.
func (c *Client) GetConfig() (map[string]any, error) {
	v8cfg, code, err := c.getJSONStatus("/v8/management/config")
	if err == nil {
		return flattenV8Config(v8cfg), nil
	}
	if isRouteUnsupported(code) {
		return c.getJSON("/v0/management/config")
	}
	return nil, err
}

// GetConfigYAML fetches the raw config.yaml content.
func (c *Client) GetConfigYAML() (string, error) {
	data, code, _, err := c.doRequest("GET", "/v8/management/config.yaml", nil)
	if err == nil && code < 400 {
		return string(data), nil
	}
	if isRouteUnsupported(code) {
		data, err = c.get("/v0/management/config.yaml")
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
}

// PutConfigYAML uploads new config.yaml content, preferring the v8 endpoint.
func (c *Client) PutConfigYAML(yamlContent string) error {
	data, code, _, err := c.doRequestHeaders("PUT", "/v8/management/config.yaml", strings.NewReader(yamlContent), http.Header{"Content-Type": {"application/yaml"}})
	if err == nil && code < 400 {
		return nil
	}
	if isRouteUnsupported(code) {
		_, err = c.put("/v0/management/config.yaml", strings.NewReader(yamlContent))
		return err
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
}

// ----- Credentials (auth files) -----

// GetAuthFiles lists auth credential files.
// API returns {"files": [...]}.
func (c *Client) GetAuthFiles() ([]map[string]any, error) {
	wrapper, code, err := c.getJSONStatus("/v8/management/credentials")
	if err == nil {
		return extractList(wrapper, "files")
	}
	if isRouteUnsupported(code) {
		wrapper, err = c.getJSON("/v0/management/auth-files")
		if err != nil {
			return nil, err
		}
		return extractList(wrapper, "files")
	}
	return nil, err
}

// DeleteAuthFile deletes a single auth file by name.
func (c *Client) DeleteAuthFile(name string) error {
	query := url.Values{}
	query.Set("name", name)
	v8Path := "/v8/management/credentials?" + query.Encode()
	v0Path := "/v0/management/auth-files?" + query.Encode()
	data, code, _, err := c.doRequest("DELETE", v8Path, nil)
	if err == nil && code < 400 {
		return nil
	}
	if isRouteUnsupported(code) {
		if code != http.StatusNotFound {
			return c.deleteV0(v0Path)
		}
		supported, definitive := c.supportsV8()
		if definitive && supported {
			return fmt.Errorf("delete failed (HTTP %d): %s", code, strings.TrimSpace(string(data)))
		}
		if !definitive {
			return fmt.Errorf("v8 route unavailable and v8 support could not be determined (HTTP %d); not falling back", code)
		}
		return c.deleteV0(v0Path)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("delete failed (HTTP %d)", code)
}

func (c *Client) deleteV0(path string) error {
	_, code, _, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("delete failed (HTTP %d)", code)
	}
	return nil
}

// ToggleAuthFile enables or disables an auth file.
func (c *Client) ToggleAuthFile(name string, disabled bool) error {
	body, _ := json.Marshal(map[string]any{"name": name, "disabled": disabled})
	return c.doV8ThenV0Write("PATCH", "/v8/management/credentials/status", body, "PATCH", "/v0/management/auth-files/status")
}

// PatchAuthFileFields updates editable fields on an auth file.
func (c *Client) PatchAuthFileFields(name string, fields map[string]any) error {
	fields["name"] = name
	body, _ := json.Marshal(fields)
	return c.doV8ThenV0Write("PATCH", "/v8/management/credentials/fields", body, "PATCH", "/v0/management/auth-files/fields")
}

// RefreshAuthFile triggers a forced refresh of a single auth credential.
func (c *Client) RefreshAuthFile(name string) error {
	return c.postJSONV8("/v8/management/credentials/refresh", "/v0/management/auth-files/refresh", map[string]any{"name": name})
}

// RefreshAllAuthFiles triggers a forced refresh of all auth credentials.
func (c *Client) RefreshAllAuthFiles() error {
	return c.postJSONV8("/v8/management/credentials/refresh", "/v0/management/auth-files/refresh", map[string]any{"all": true})
}

func (c *Client) postJSONV8(v8Path, v0Path string, body any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return c.doV8ThenV0Write("POST", v8Path, jsonBody, "POST", v0Path)
}

// doV8ThenV0Write performs a write against a v8 route, falling back to the v0
// route only when the v8 route is genuinely not implemented by this server.
// A business 404 (e.g. the named credential/field does not exist) on a
// reachable v8 route is returned as-is rather than retried against v0, which
// would both mask the real error and re-hit the deprecated contract.
func (c *Client) doV8ThenV0Write(v8Method, v8Path string, v8Body []byte, v0Method, v0Path string) error {
	data, code, _, err := c.doRequest(v8Method, v8Path, strings.NewReader(string(v8Body)))
	if err == nil && code < 400 {
		return nil
	}
	if isRouteUnsupported(code) {
		// 405/501 always mean this specific route is unimplemented -> v0.
		// A 404 may mean either an unimplemented route or a missing resource;
		// only fall back when v8 is definitively absent. When v8 is definitively
		// present, surface the business error. When the probe cannot decide,
		// do not guess a second protocol.
		if code != http.StatusNotFound {
			return c.writeV0(v0Method, v0Path, v8Body)
		}
		supported, definitive := c.supportsV8()
		if definitive && supported {
			return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
		}
		if !definitive {
			return fmt.Errorf("v8 route unavailable and v8 support could not be determined (HTTP %d); not falling back", code)
		}
		return c.writeV0(v0Method, v0Path, v8Body)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
}

func (c *Client) writeV0(method, path string, body []byte) error {
	data, code, _, err := c.doRequest(method, path, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return nil
}

// ----- Logs -----

// GetLogs fetches log lines from the server.
func (c *Client) GetLogs(after int64, limit int) ([]string, int64, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if after > 0 {
		query.Set("after", strconv.FormatInt(after, 10))
	}

	v8Path := "/v8/management/observability/logs"
	v0Path := "/v0/management/logs"
	encodedQuery := query.Encode()
	if encodedQuery != "" {
		v8Path += "?" + encodedQuery
		v0Path += "?" + encodedQuery
	}

	wrapper, err := c.getJSONV8(v8Path, v0Path)
	if err != nil {
		return nil, after, err
	}

	lines := []string{}
	if rawLines, ok := wrapper["lines"]; ok && rawLines != nil {
		rawJSON, errMarshal := json.Marshal(rawLines)
		if errMarshal != nil {
			return nil, after, errMarshal
		}
		if errUnmarshal := json.Unmarshal(rawJSON, &lines); errUnmarshal != nil {
			return nil, after, errUnmarshal
		}
	}

	latest := after
	if rawLatest, ok := wrapper["latest-timestamp"]; ok {
		switch value := rawLatest.(type) {
		case float64:
			latest = int64(value)
		case json.Number:
			if parsed, errParse := value.Int64(); errParse == nil {
				latest = parsed
			}
		case int64:
			latest = value
		case int:
			latest = int64(value)
		}
	}
	if latest < after {
		latest = after
	}

	return lines, latest, nil
}

// ----- Client API keys (access.api-keys) -----

// GetAPIKeys fetches the list of API keys.
// API returns {"api-keys": [...]} on v0; the v8 equivalent is the bare array at
// /v8/management/config/access/api-keys.
func (c *Client) GetAPIKeys() ([]string, error) {
	value, code, err := c.v8ConfigGet("access/api-keys")
	if err == nil {
		return stringSlice(value)
	}
	if err == errV8ConfigNodeMissing {
		return nil, nil
	}
	if isRouteUnsupported(code) {
		wrapper, errGet := c.getJSON("/v0/management/api-keys")
		if errGet != nil {
			return nil, errGet
		}
		return stringSlice(wrapper["api-keys"])
	}
	return nil, err
}

// replaceAPIKeys writes the complete access.api-keys list through the v8
// config endpoint. The ETag captured on the read is sent as If-Match so a
// write is rejected with 412 if another client saved after the read. A 405/501
// write status means this specific route is unimplemented and falls back to
// the supplied v0 operation; 412 and other statuses are surfaced.
func (c *Client) replaceAPIKeys(keys []string, etag string, v0 func() error) error {
	body, _ := json.Marshal(keys)
	headers := http.Header{}
	if etag != "" {
		headers.Set("If-Match", etag)
	}
	data, code, _, err := c.doRequestHeaders("PUT", "/v8/management/config/access/api-keys", strings.NewReader(string(body)), headers)
	if err == nil && code < 400 {
		return nil
	}
	if code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented {
		return v0()
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
}

// mutateAPIKeys reads the current access.api-keys list, applies mutator, and
// writes it back conditionally so concurrent edits are not silently lost.
// It falls back to the v0 contract only when v8 is unsupported.
func (c *Client) mutateAPIKeys(mutator func(keys []string) ([]string, error), v0 func() error) error {
	keys, etag, code, err := c.apiKeysV8()
	if err == nil {
		if etag == "" {
			return fmt.Errorf("cannot modify api-keys: server returned no config version (ETag); refusing unconditional replace")
		}
		next, errMut := mutator(keys)
		if errMut != nil {
			return errMut
		}
		return c.replaceAPIKeys(next, etag, v0)
	}
	if isRouteUnsupported(code) {
		return v0()
	}
	return err
}

// AddAPIKey adds a new API key. On v8 it appends to access.api-keys; on v0 it
// falls back to the legacy patch contract.
func (c *Client) AddAPIKey(key string) error {
	return c.mutateAPIKeys(func(keys []string) ([]string, error) {
		return append(keys, key), nil
	}, func() error {
		body := map[string]any{"old": nil, "new": key}
		jsonBody, _ := json.Marshal(body)
		_, err := c.patch("/v0/management/api-keys", strings.NewReader(string(jsonBody)))
		return err
	})
}

// EditAPIKey replaces an API key at the given index. expected is the key value
// the caller displayed when the user chose this index; if the server list no
// longer has that value at this position the write is refused instead of
// silently editing a shifted entry.
func (c *Client) EditAPIKey(index int, newValue string, expected ...string) error {
	return c.mutateAPIKeys(func(keys []string) ([]string, error) {
		if index < 0 || index >= len(keys) {
			return nil, fmt.Errorf("index %d out of range", index)
		}
		if len(expected) > 0 && keys[index] != expected[0] {
			return nil, fmt.Errorf("api-keys changed since selection (entry at index %d no longer matches); please refresh", index)
		}
		keys[index] = newValue
		return keys, nil
	}, func() error {
		body := map[string]any{"index": index, "value": newValue}
		jsonBody, _ := json.Marshal(body)
		_, err := c.patch("/v0/management/api-keys", strings.NewReader(string(jsonBody)))
		return err
	})
}

// DeleteAPIKey deletes an API key by index. expected optionally binds the key
// value shown to the user, so a concurrent reorder/delete is refused rather
// than removing the wrong entry.
func (c *Client) DeleteAPIKey(index int, expected ...string) error {
	return c.mutateAPIKeys(func(keys []string) ([]string, error) {
		if index < 0 || index >= len(keys) {
			return nil, fmt.Errorf("index %d out of range", index)
		}
		if len(expected) > 0 && keys[index] != expected[0] {
			return nil, fmt.Errorf("api-keys changed since selection (entry at index %d no longer matches); please refresh", index)
		}
		return append(keys[:index], keys[index+1:]...), nil
	}, func() error {
		_, code, _, err := c.doRequest("DELETE", fmt.Sprintf("/v0/management/api-keys?index=%d", index), nil)
		if err != nil {
			return err
		}
		if code >= 400 {
			return fmt.Errorf("delete failed (HTTP %d)", code)
		}
		return nil
	})
}

// apiKeysV8 reads the access.api-keys list together with the ETag of the exact
// config snapshot it came from, so a subsequent replace is conditional on that
// same version. Reading the root document (rather than the child node) keeps
// the list value and its version consistent even when the node is absent.
func (c *Client) apiKeysV8() ([]string, string, int, error) {
	data, code, hdr, err := c.doRequest("GET", "/v8/management/config", nil)
	etag := ""
	if hdr != nil {
		etag = hdr.Get("ETag")
	}
	if err != nil {
		return nil, "", code, err
	}
	if code >= 400 {
		return nil, "", code, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, "", code, err
	}
	var node any
	if access, ok := doc["access"].(map[string]any); ok {
		node = access["api-keys"]
	}
	keys, err := stringSlice(node)
	return keys, etag, code, err
}

func stringSlice(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ----- Provider upstream key lists (read-only display) -----

// providerV8ConfigKey maps a provider list identifier to its key under the v8
// api-keys tree.
var providerV8ConfigKey = map[string]string{
	"gemini":               "gemini",
	"interactions":         "interactions",
	"claude":               "claude",
	"codex":                "codex",
	"xai":                  "xai",
	"meta":                 "meta",
	"vertex":               "vertex",
	"openai-compatibility": "openai-compatibility",
}

// GetGeminiKeys fetches Gemini API keys.
// API returns {"gemini-api-key": [...]}.
func (c *Client) GetGeminiKeys() ([]map[string]any, error) {
	return c.getProviderKeyList("gemini", "/v0/management/gemini-api-key", "gemini-api-key")
}

// GetInteractionsKeys fetches native Interactions API keys.
// API returns {"interactions-api-key": [...]}.
func (c *Client) GetInteractionsKeys() ([]map[string]any, error) {
	return c.getProviderKeyList("interactions", "/v0/management/interactions-api-key", "interactions-api-key")
}

// GetClaudeKeys fetches Claude API keys.
// API returns {"claude-api-key": [...]}.
func (c *Client) GetClaudeKeys() ([]map[string]any, error) {
	return c.getProviderKeyList("claude", "/v0/management/claude-api-key", "claude-api-key")
}

// GetCodexKeys fetches Codex API keys.
// API returns {"codex-api-key": [...]}.
func (c *Client) GetCodexKeys() ([]map[string]any, error) {
	return c.getProviderKeyList("codex", "/v0/management/codex-api-key", "codex-api-key")
}

// GetXAIKeys fetches xAI API keys.
// API returns {"xai-api-key": [...]}.
func (c *Client) GetXAIKeys() ([]map[string]any, error) {
	return c.getProviderKeyList("xai", "/v0/management/xai-api-key", "xai-api-key")
}

// GetVertexKeys fetches Vertex API keys.
// API returns {"vertex-api-key": [...]}.
func (c *Client) GetVertexKeys() ([]map[string]any, error) {
	return c.getProviderKeyList("vertex", "/v0/management/vertex-api-key", "vertex-api-key")
}

// GetOpenAICompat fetches OpenAI compatibility entries.
// API returns {"openai-compatibility": [...]}.
func (c *Client) GetOpenAICompat() ([]map[string]any, error) {
	return c.getProviderKeyList("openai-compatibility", "/v0/management/openai-compatibility", "openai-compatibility")
}

// getProviderKeyList fetches a provider upstream-key list, preferring the v8
// config tree (api-keys/<provider>) and normalizing its grouped shape to the
// flat legacy list the TUI renders. It falls back to the v0 wrapped list.
func (c *Client) getProviderKeyList(provider, v0Path, v0WrapKey string) ([]map[string]any, error) {
	v8Key := providerV8ConfigKey[provider]
	value, code, err := c.v8ConfigGet("api-keys/" + v8Key)
	if err == nil {
		return flattenProviderKeyList(provider, value), nil
	}
	if err == errV8ConfigNodeMissing {
		return nil, nil
	}
	if isRouteUnsupported(code) {
		wrapper, errGet := c.getJSON(v0Path)
		if errGet != nil {
			return nil, errGet
		}
		return extractList(wrapper, v0WrapKey)
	}
	return nil, err
}

// flattenProviderKeyList converts the v8 api-keys.<provider> value into the
// flat legacy list the TUI renders. For key-grouped providers the groups each
// carry a keys array; group-level prefix/base-url/proxy-url are inherited by
// each key that does not override them. The openai-compatibility provider is a
// list of upstream entries (name/base-url/prefix/api-key-entries), not a keys
// list, so its groups are returned unchanged.
func flattenProviderKeyList(provider string, value any) []map[string]any {
	groups, ok := value.([]any)
	if !ok || len(groups) == 0 {
		return nil
	}
	if provider == "openai-compatibility" {
		out := make([]map[string]any, 0, len(groups))
		for _, g := range groups {
			if group, ok := g.(map[string]any); ok {
				out = append(out, group)
			}
		}
		return out
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		groupPrefix := group["prefix"]
		groupBase := group["base-url"]
		groupProxy := group["proxy-url"]
		keys, hasKeys := group["keys"].([]any)
		if hasKeys && len(keys) == 0 {
			// A group with an explicit empty keys list yields no credentials.
			continue
		}
		if len(keys) == 0 {
			// Legacy flat entry without a keys array: only counts when it
			// carries a real api-key, not just a group name.
			if apiKey, ok := group["api-key"].(string); ok && apiKey != "" {
				out = append(out, map[string]any{"api-key": apiKey})
			}
			continue
		}
		for _, k := range keys {
			keyMap, ok := k.(map[string]any)
			if !ok {
				continue
			}
			entry := make(map[string]any, len(keyMap)+3)
			for k2, v2 := range keyMap {
				entry[k2] = v2
			}
			// v8 treats an explicit per-key null the same as an absent field:
			// both inherit the group value.
			inherit := func(field string, groupVal any) {
				if v, exists := entry[field]; !exists || v == nil {
					if groupVal != nil {
						entry[field] = groupVal
					}
				}
			}
			inherit("prefix", groupPrefix)
			inherit("base-url", groupBase)
			inherit("proxy-url", groupProxy)
			out = append(out, entry)
		}
	}
	return out
}

// extractList pulls an array of maps from a wrapper object by key.
func extractList(wrapper map[string]any, key string) ([]map[string]any, error) {
	arr, ok := wrapper[key]
	if !ok || arr == nil {
		return nil, nil
	}
	raw, err := json.Marshal(arr)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetDebug fetches the current debug setting.
func (c *Client) GetDebug() (bool, error) {
	value, code, err := c.v8ConfigGet("observability/logs/debug")
	if err == nil {
		if b, ok := value.(bool); ok {
			return b, nil
		}
		return false, nil
	}
	if err == errV8ConfigNodeMissing {
		return false, nil
	}
	if isRouteUnsupported(code) {
		wrapper, errGet := c.getJSON("/v0/management/debug")
		if errGet != nil {
			return false, errGet
		}
		if v, ok := wrapper["debug"]; ok {
			if b, ok := v.(bool); ok {
				return b, nil
			}
		}
		return false, nil
	}
	return false, err
}

// ----- OAuth session -----

// oauthV8Provider maps a legacy provider auth-url path segment to its v8
// provider identifier.
var oauthV8Provider = map[string]string{
	"anthropic-auth-url":   "claude",
	"codex-auth-url":       "codex",
	"antigravity-auth-url": "antigravity",
	"kimi-auth-url":        "kimi",
	"kimi-ai-auth-url":     "kimi-ai",
	"xai-auth-url":         "xai",
	"devin-auth-url":       "devin",
	"meta-auth-url":        "meta",
}

// StartOAuthFlow begins an OAuth login for the given provider. It prefers the
// shared v8 endpoint and falls back to the provider-specific v0 route.
// legacyAPIPath is the v0 path segment (e.g. "codex-auth-url").
func (c *Client) StartOAuthFlow(legacyAPIPath string) (map[string]any, error) {
	provider := oauthV8Provider[strings.TrimPrefix(legacyAPIPath, "/v0/management/")]
	if provider == "" {
		provider = strings.TrimSuffix(strings.TrimPrefix(legacyAPIPath, "/v0/management/"), "-auth-url")
	}
	query := url.Values{}
	query.Set("provider", provider)
	query.Set("is_webui", "true")
	v8Path := "/v8/management/oauth/auth-url?" + query.Encode()
	wrapper, code, err := c.getJSONStatus(v8Path)
	if err == nil {
		return wrapper, nil
	}
	if isRouteUnsupported(code) {
		return c.getJSON("/v0/management/" + legacyAPIPath + "?is_webui=true")
	}
	return nil, err
}

// SubmitOAuthCallback posts a callback URL to the management server, preferring
// the v8 callback endpoint.
func (c *Client) SubmitOAuthCallback(providerKey, redirectURL, state string) error {
	body := map[string]string{
		"provider":     providerKey,
		"redirect_url": redirectURL,
		"state":        state,
	}
	jsonBody, _ := json.Marshal(body)
	return c.doV8ThenV0Write("POST", "/v8/management/oauth/callback", jsonBody, "POST", "/v0/management/oauth-callback")
}

// GetAuthStatus polls the OAuth session status.
// Returns status ("wait", "ok", "error") and optional error message.
func (c *Client) GetAuthStatus(state string) (string, string, error) {
	query := url.Values{}
	query.Set("state", state)
	v8Path := "/v8/management/oauth/status?" + query.Encode()
	wrapper, code, err := c.getJSONStatus(v8Path)
	if err != nil {
		if isRouteUnsupported(code) {
			wrapper, err = c.getJSON("/v0/management/get-auth-status?" + query.Encode())
			if err != nil {
				return "", "", err
			}
		} else {
			return "", "", err
		}
	}
	status := getString(wrapper, "status")
	errMsg := getString(wrapper, "error")
	return status, errMsg, nil
}

// CancelAuthSession cancels a pending OAuth session on the management server.
func (c *Client) CancelAuthSession(state string) error {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil
	}
	query := url.Values{}
	query.Set("state", state)
	v8Path := "/v8/management/oauth/session?" + query.Encode()
	_, code, _, err := c.doRequest("DELETE", v8Path, nil)
	if err == nil && code < 400 {
		return nil
	}
	if isRouteUnsupported(code) {
		_, code, _, err = c.doRequest("DELETE", "/v0/management/oauth-session?"+query.Encode(), nil)
		if err != nil {
			return err
		}
		if code >= 400 {
			return fmt.Errorf("HTTP %d", code)
		}
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("HTTP %d", code)
}

// ----- Config field update methods -----

// putConfigField writes a single config field, preferring the v8 path-based
// config endpoint with the raw value, and falling back to the v0 {value:...}
// envelope contract.
func (c *Client) putConfigField(flatPath string, value any) error {
	v8Path := "/v8/management/config/" + v8ConfigPath(flatPath)
	v8Body, _ := json.Marshal(value)
	data, code, _, err := c.doRequest("PUT", v8Path, strings.NewReader(string(v8Body)))
	if err == nil && code < 400 {
		return nil
	}
	if isRouteUnsupported(code) {
		v0Body, _ := json.Marshal(map[string]any{"value": value})
		_, err = c.put("/v0/management/"+flatPath, strings.NewReader(string(v0Body)))
		return err
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
}

// PutBoolField updates a boolean config field.
func (c *Client) PutBoolField(path string, value bool) error {
	return c.putConfigField(path, value)
}

// PutIntField updates an integer config field.
func (c *Client) PutIntField(path string, value int) error {
	return c.putConfigField(path, value)
}

// PutStringField updates a string config field.
func (c *Client) PutStringField(path string, value string) error {
	return c.putConfigField(path, value)
}

// DeleteField sends a DELETE request for a config field, preferring v8.
func (c *Client) DeleteField(path string) error {
	v8Path := "/v8/management/config/" + v8ConfigPath(path)
	data, code, _, err := c.doRequest("DELETE", v8Path, nil)
	if err == nil && code < 400 {
		return nil
	}
	if code == http.StatusNotFound {
		supported, definitive := c.supportsV8()
		if definitive && supported {
			// v8 exists but the field is absent: nothing to delete.
			return nil
		}
		if !definitive {
			return fmt.Errorf("v8 route returned 404 and v8 support could not be determined; not falling back")
		}
	}
	if isRouteUnsupported(code) {
		_, code, _, err = c.doRequest("DELETE", "/v0/management/"+path, nil)
		if err != nil {
			return err
		}
		if code >= 400 {
			return fmt.Errorf("delete failed (HTTP %d)", code)
		}
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("delete failed (HTTP %d): %s", code, strings.TrimSpace(string(data)))
}
