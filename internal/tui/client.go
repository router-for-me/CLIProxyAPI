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
type Client struct {
	baseURL   string
	secretKey string
	http      *http.Client

	// configETagMu guards configETag, the ETag returned by the latest
	// /v8/management/config read or write. It is replayed as an If-Match
	// precondition on configuration writes.
	configETagMu sync.Mutex
	configETag   string
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

func (c *Client) doRequest(method, path string, body io.Reader) ([]byte, int, error) {
	data, code, _, err := c.doRequestWithHeaders(method, path, body, nil)
	return data, code, err
}

// doRequestWithHeaders sends a request with extra headers and also returns the
// response headers so callers can capture management ETags.
func (c *Client) doRequestWithHeaders(method, path string, body io.Reader, extraHeaders map[string]string) ([]byte, int, http.Header, error) {
	url := c.baseURL + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, 0, nil, err
	}
	if c.secretKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.secretKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range extraHeaders {
		if value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, nil, err
	}
	return data, resp.StatusCode, resp.Header, nil
}

// isHTTPNotFound reports whether an error returned by get/put/patch-style
// helpers is an HTTP 404, which signals an unregistered management route.
func isHTTPNotFound(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "HTTP 404:")
}

// httpStatusError formats a non-2xx management response.
func httpStatusError(code int, data []byte) error {
	return fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
}

// routesUnavailableError reports that neither the current nor the deprecated
// management route for an operation is served by the target host.
func routesUnavailableError(v8Path, v0Path string) error {
	return fmt.Errorf("management endpoint unavailable: neither %s nor %s is served by this server", v8Path, v0Path)
}

func (c *Client) savedConfigETag() string {
	c.configETagMu.Lock()
	defer c.configETagMu.Unlock()
	return c.configETag
}

func (c *Client) storeConfigETag(tag string) {
	c.configETagMu.Lock()
	defer c.configETagMu.Unlock()
	c.configETag = tag
}

// configWriteHeaders builds the headers for a v8 configuration write, adding
// the saved ETag as an If-Match precondition when one is known.
func (c *Client) configWriteHeaders() map[string]string {
	headers := map[string]string{}
	if etag := c.savedConfigETag(); etag != "" {
		headers["If-Match"] = etag
	}
	return headers
}

func (c *Client) get(path string) ([]byte, error) {
	data, code, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *Client) put(path string, body io.Reader) ([]byte, error) {
	data, code, err := c.doRequest("PUT", path, body)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *Client) patch(path string, body io.Reader) ([]byte, error) {
	data, code, err := c.doRequest("PATCH", path, body)
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

// postJSON sends a JSON body via POST and checks for errors.
func (c *Client) postJSON(path string, body any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	data, code, err := c.doRequest("POST", path, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	if code >= 400 {
		return httpStatusError(code, data)
	}
	return nil
}

// GetConfig fetches the parsed config. It prefers the current
// /v8/management/config route and falls back to the deprecated
// /v0/management/config route for servers that only serve the old contract.
// The v8 tree is projected onto the flat legacy keys the tabs consume.
func (c *Client) GetConfig() (map[string]any, error) {
	data, code, header, err := c.doRequestWithHeaders("GET", "/v8/management/config", nil, nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		legacy, errLegacy := c.getJSON("/v0/management/config")
		if errLegacy != nil {
			if isHTTPNotFound(errLegacy) {
				return nil, routesUnavailableError("/v8/management/config", "/v0/management/config")
			}
			return nil, errLegacy
		}
		c.storeConfigETag("")
		return legacy, nil
	}
	if code >= 400 {
		return nil, httpStatusError(code, data)
	}
	c.storeConfigETag(header.Get("ETag"))
	var v8Config map[string]any
	if err = json.Unmarshal(data, &v8Config); err != nil {
		return nil, err
	}
	return legacyConfigFromV8(v8Config), nil
}

// GetConfigYAML fetches the config.yaml content, preferring the current
// /v8/management/config.yaml route with a fallback to the deprecated one.
func (c *Client) GetConfigYAML() (string, error) {
	data, code, header, err := c.doRequestWithHeaders("GET", "/v8/management/config.yaml", nil, nil)
	if err != nil {
		return "", err
	}
	if code == http.StatusNotFound {
		legacy, errLegacy := c.get("/v0/management/config.yaml")
		if errLegacy != nil {
			if isHTTPNotFound(errLegacy) {
				return "", routesUnavailableError("/v8/management/config.yaml", "/v0/management/config.yaml")
			}
			return "", errLegacy
		}
		c.storeConfigETag("")
		return string(legacy), nil
	}
	if code >= 400 {
		return "", httpStatusError(code, data)
	}
	c.storeConfigETag(header.Get("ETag"))
	return string(data), nil
}

// PutConfigYAML uploads new config.yaml content. It prefers the current
// /v8/management/config.yaml route and carries the ETag saved by the latest
// read as an If-Match precondition; a 404 falls back to the deprecated
// /v0/management/config.yaml route for servers that only serve the old
// contract.
func (c *Client) PutConfigYAML(yamlContent string) error {
	headers := c.configWriteHeaders()
	headers["Content-Type"] = "application/yaml"
	data, code, respHeader, err := c.doRequestWithHeaders("PUT", "/v8/management/config.yaml", strings.NewReader(yamlContent), headers)
	if err != nil {
		return err
	}
	if code == http.StatusNotFound {
		if _, errLegacy := c.put("/v0/management/config.yaml", strings.NewReader(yamlContent)); errLegacy != nil {
			if isHTTPNotFound(errLegacy) {
				return routesUnavailableError("/v8/management/config.yaml", "/v0/management/config.yaml")
			}
			return errLegacy
		}
		c.storeConfigETag("")
		return nil
	}
	if code >= 400 {
		return httpStatusError(code, data)
	}
	c.storeConfigETag(respHeader.Get("ETag"))
	return nil
}

// configFieldV8Paths maps the legacy /v0/management field paths used by the
// TUI onto their current /v8/management/config equivalents.
var configFieldV8Paths = map[string]string{
	"port":                                "server/port",
	"host":                                "server/host",
	"debug":                               "observability/logs/debug",
	"proxy-url":                           "requests/proxy-url",
	"request-retry":                       "routing/retry/request-retry",
	"max-retry-credentials":               "routing/retry/max-retry-credentials",
	"max-retry-interval":                  "routing/retry/max-retry-interval",
	"force-model-prefix":                  "routing/force-model-prefix",
	"logging-to-file":                     "observability/logs/logging-to-file",
	"logs-max-total-size-mb":              "observability/logs/logs-max-total-size-mb",
	"error-logs-max-files":                "observability/logs/error-logs-max-files",
	"usage-statistics-enabled":            "observability/usage/usage-statistics-enabled",
	"request-log":                         "observability/logs/request-log",
	"ws-auth":                             "oauth/providers/aistudio/ws-auth",
	"routing/strategy":                    "routing/strategy",
	"quota-exceeded/switch-project":       "quota-exceeded/switch-project",
	"quota-exceeded/switch-preview-model": "quota-exceeded/switch-preview-model",
}

// legacyConfigFromV8 projects the parts of the current /v8/management/config
// tree the TUI reads onto the flat legacy keys the existing tabs expect, so a
// v8-only server keeps feeding the same tab rendering.
func legacyConfigFromV8(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	out := make(map[string]any)
	projections := []struct{ legacy, v8 string }{
		{"port", "server.port"},
		{"host", "server.host"},
		{"debug", "observability.logs.debug"},
		{"proxy-url", "requests.proxy-url"},
		{"request-retry", "routing.retry.request-retry"},
		{"max-retry-credentials", "routing.retry.max-retry-credentials"},
		{"max-retry-interval", "routing.retry.max-retry-interval"},
		{"force-model-prefix", "routing.force-model-prefix"},
		{"logging-to-file", "observability.logs.logging-to-file"},
		{"logs-max-total-size-mb", "observability.logs.logs-max-total-size-mb"},
		{"error-logs-max-files", "observability.logs.error-logs-max-files"},
		{"usage-statistics-enabled", "observability.usage.usage-statistics-enabled"},
		{"request-log", "observability.logs.request-log"},
		{"ws-auth", "oauth.providers.aistudio.ws-auth"},
	}
	for _, projection := range projections {
		if value, ok := v8NestedValue(cfg, projection.v8); ok {
			out[projection.legacy] = value
		}
	}
	if quota, ok := cfg["quota-exceeded"].(map[string]any); ok {
		out["quota-exceeded"] = quota
	}
	if strategy, ok := v8NestedValue(cfg, "routing.strategy"); ok {
		out["routing"] = map[string]any{"strategy": strategy}
	}
	return out
}

// v8NestedValue reads a dotted path from a decoded v8 configuration tree and
// reports whether the path is present, so explicit false/null stay visible.
func v8NestedValue(cfg map[string]any, path string) (any, bool) {
	var current any = cfg
	for _, key := range strings.Split(path, ".") {
		node, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = node[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// GetAuthFiles lists auth credential files.
// API returns {"files": [...]}.
func (c *Client) GetAuthFiles() ([]map[string]any, error) {
	wrapper, err := c.getJSON("/v8/management/credentials")
	if err != nil {
		if isHTTPNotFound(err) {
			wrapper, err = c.getJSON("/v0/management/auth-files")
		}
		if err != nil {
			return nil, err
		}
	}
	return extractList(wrapper, "files")
}

// DeleteAuthFile deletes a single auth file by name via the current
// /v8/management/credentials route, falling back to the deprecated
// /v0/management/auth-files route for servers that only serve the old contract.
func (c *Client) DeleteAuthFile(name string) error {
	query := url.Values{}
	query.Set("name", name)
	encoded := query.Encode()
	_, code, err := c.doRequest("DELETE", "/v8/management/credentials?"+encoded, nil)
	if err != nil {
		return err
	}
	if code == http.StatusNotFound {
		_, code, err = c.doRequest("DELETE", "/v0/management/auth-files?"+encoded, nil)
		if err != nil {
			return err
		}
	}
	if code >= 400 {
		return fmt.Errorf("delete failed (HTTP %d)", code)
	}
	return nil
}

// ToggleAuthFile enables or disables an auth file via the current
// /v8/management/credentials/status route, falling back to the deprecated
// /v0/management/auth-files/status route.
func (c *Client) ToggleAuthFile(name string, disabled bool) error {
	body, _ := json.Marshal(map[string]any{"name": name, "disabled": disabled})
	if _, err := c.patch("/v8/management/credentials/status", strings.NewReader(string(body))); !isHTTPNotFound(err) {
		return err
	}
	_, err := c.patch("/v0/management/auth-files/status", strings.NewReader(string(body)))
	return err
}

// PatchAuthFileFields updates editable fields on an auth file via the current
// /v8/management/credentials/fields route, falling back to the deprecated
// /v0/management/auth-files/fields route.
func (c *Client) PatchAuthFileFields(name string, fields map[string]any) error {
	fields["name"] = name
	body, _ := json.Marshal(fields)
	if _, err := c.patch("/v8/management/credentials/fields", strings.NewReader(string(body))); !isHTTPNotFound(err) {
		return err
	}
	_, err := c.patch("/v0/management/auth-files/fields", strings.NewReader(string(body)))
	return err
}

// RefreshAuthFile triggers a forced refresh of a single auth credential via
// the current /v8/management/credentials/refresh route, falling back to the
// deprecated /v0/management/auth-files/refresh route.
func (c *Client) RefreshAuthFile(name string) error {
	return c.postAuthRefresh(map[string]any{"name": name})
}

// RefreshAllAuthFiles triggers a forced refresh of all auth credentials via
// the current /v8/management/credentials/refresh route, falling back to the
// deprecated /v0/management/auth-files/refresh route.
func (c *Client) RefreshAllAuthFiles() error {
	return c.postAuthRefresh(map[string]any{"all": true})
}

func (c *Client) postAuthRefresh(body map[string]any) error {
	if err := c.postJSON("/v8/management/credentials/refresh", body); !isHTTPNotFound(err) {
		return err
	}
	return c.postJSON("/v0/management/auth-files/refresh", body)
}

// GetLogs fetches log lines from the server.
func (c *Client) GetLogs(after int64, limit int) ([]string, int64, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if after > 0 {
		query.Set("after", strconv.FormatInt(after, 10))
	}

	path := "/v0/management/logs"
	encodedQuery := query.Encode()
	if encodedQuery != "" {
		path += "?" + encodedQuery
	}

	wrapper, err := c.getJSON(path)
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

// GetAPIKeys fetches the list of API keys.
// API returns {"api-keys": [...]}.
func (c *Client) GetAPIKeys() ([]string, error) {
	wrapper, err := c.getJSON("/v0/management/api-keys")
	if err != nil {
		return nil, err
	}
	arr, ok := wrapper["api-keys"]
	if !ok {
		return nil, nil
	}
	raw, err := json.Marshal(arr)
	if err != nil {
		return nil, err
	}
	var result []string
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// AddAPIKey adds a new API key by sending old=nil, new=key which appends.
func (c *Client) AddAPIKey(key string) error {
	body := map[string]any{"old": nil, "new": key}
	jsonBody, _ := json.Marshal(body)
	_, err := c.patch("/v0/management/api-keys", strings.NewReader(string(jsonBody)))
	return err
}

// EditAPIKey replaces an API key at the given index.
func (c *Client) EditAPIKey(index int, newValue string) error {
	body := map[string]any{"index": index, "value": newValue}
	jsonBody, _ := json.Marshal(body)
	_, err := c.patch("/v0/management/api-keys", strings.NewReader(string(jsonBody)))
	return err
}

// DeleteAPIKey deletes an API key by index.
func (c *Client) DeleteAPIKey(index int) error {
	_, code, err := c.doRequest("DELETE", fmt.Sprintf("/v0/management/api-keys?index=%d", index), nil)
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("delete failed (HTTP %d)", code)
	}
	return nil
}

// GetGeminiKeys fetches Gemini API keys.
// API returns {"gemini-api-key": [...]}.
func (c *Client) GetGeminiKeys() ([]map[string]any, error) {
	return c.getWrappedKeyList("/v0/management/gemini-api-key", "gemini-api-key")
}

// GetInteractionsKeys fetches native Interactions API keys.
// API returns {"interactions-api-key": [...]}.
func (c *Client) GetInteractionsKeys() ([]map[string]any, error) {
	return c.getWrappedKeyList("/v0/management/interactions-api-key", "interactions-api-key")
}

// GetClaudeKeys fetches Claude API keys.
func (c *Client) GetClaudeKeys() ([]map[string]any, error) {
	return c.getWrappedKeyList("/v0/management/claude-api-key", "claude-api-key")
}

// GetCodexKeys fetches Codex API keys.
func (c *Client) GetCodexKeys() ([]map[string]any, error) {
	return c.getWrappedKeyList("/v0/management/codex-api-key", "codex-api-key")
}

// GetXAIKeys fetches xAI API keys.
func (c *Client) GetXAIKeys() ([]map[string]any, error) {
	return c.getWrappedKeyList("/v0/management/xai-api-key", "xai-api-key")
}

// GetVertexKeys fetches Vertex API keys.
func (c *Client) GetVertexKeys() ([]map[string]any, error) {
	return c.getWrappedKeyList("/v0/management/vertex-api-key", "vertex-api-key")
}

// GetOpenAICompat fetches OpenAI compatibility entries.
func (c *Client) GetOpenAICompat() ([]map[string]any, error) {
	return c.getWrappedKeyList("/v0/management/openai-compatibility", "openai-compatibility")
}

// getWrappedKeyList fetches a wrapped list from the API.
func (c *Client) getWrappedKeyList(path, key string) ([]map[string]any, error) {
	wrapper, err := c.getJSON(path)
	if err != nil {
		return nil, err
	}
	return extractList(wrapper, key)
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
	wrapper, err := c.getJSON("/v0/management/debug")
	if err != nil {
		return false, err
	}
	if v, ok := wrapper["debug"]; ok {
		if b, ok := v.(bool); ok {
			return b, nil
		}
	}
	return false, nil
}

// GetAuthStatus polls the OAuth session status.
// Returns status ("wait", "ok", "error") and optional error message.
func (c *Client) GetAuthStatus(state string) (string, string, error) {
	query := url.Values{}
	query.Set("state", state)
	path := "/v0/management/get-auth-status?" + query.Encode()
	wrapper, err := c.getJSON(path)
	if err != nil {
		return "", "", err
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
	path := "/v0/management/oauth-session?" + query.Encode()
	_, code, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("HTTP %d", code)
	}
	return nil
}

// ----- Config field update methods -----

// putConfigField updates a single configuration field. It prefers the current
// /v8/management/config/<path> route with an If-Match precondition derived
// from the latest read and falls back to the deprecated
// /v0/management/<path> route with its {"value": ...} body shape.
func (c *Client) putConfigField(path string, value any) error {
	v8Path, hasV8Path := configFieldV8Paths[path]
	if hasV8Path {
		if err := c.putV8ConfigValue(v8Path, value); !isHTTPNotFound(err) {
			return err
		}
	}
	body, errMarshal := json.Marshal(map[string]any{"value": value})
	if errMarshal != nil {
		return errMarshal
	}
	if _, err := c.put("/v0/management/"+path, strings.NewReader(string(body))); err != nil {
		if isHTTPNotFound(err) && hasV8Path {
			return routesUnavailableError("/v8/management/config/"+v8Path, "/v0/management/"+path)
		}
		return err
	}
	if hasV8Path {
		c.storeConfigETag("")
	}
	return nil
}

// putV8ConfigValue writes one configuration value through the current
// /v8/management/config route and stores the ETag of the saved version.
func (c *Client) putV8ConfigValue(v8Path string, value any) error {
	body, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return errMarshal
	}
	data, code, respHeader, err := c.doRequestWithHeaders("PUT", "/v8/management/config/"+v8Path, strings.NewReader(string(body)), c.configWriteHeaders())
	if err != nil {
		return err
	}
	if code >= 400 {
		return httpStatusError(code, data)
	}
	c.storeConfigETag(respHeader.Get("ETag"))
	return nil
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

// DeleteField sends a DELETE request for a config field. It prefers the
// current /v8/management/config/<path> route with an If-Match precondition
// and falls back to the deprecated /v0/management/<path> route.
func (c *Client) DeleteField(path string) error {
	v8Path, hasV8Path := configFieldV8Paths[path]
	if hasV8Path {
		data, code, respHeader, err := c.doRequestWithHeaders("DELETE", "/v8/management/config/"+v8Path, nil, c.configWriteHeaders())
		if err != nil {
			return err
		}
		if code < 400 {
			c.storeConfigETag(respHeader.Get("ETag"))
			return nil
		}
		if code != http.StatusNotFound {
			return httpStatusError(code, data)
		}
	}
	_, _, err := c.doRequest("DELETE", "/v0/management/"+path, nil)
	return err
}
