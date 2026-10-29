package handlers

// Codex account catalog (client.codex.account-catalog).
//
// Codex's model picker shows one provider's catalog. When this proxy is that provider and forwards the caller's own
// ChatGPT login (a Codex OAuth credential whose headers include "Authorization": "$Authorization"), the catalog Codex
// should see is the one ChatGPT's Codex backend returns for that login: the account's own models, names, effort levels
// and per-model settings (instructions, shell and tool modes), which change as OpenAI ships. This file fetches that list
// with the caller's own headers, passes its entries through byte for byte, and appends the catalog entries the
// configured Codex API-key providers return for their listed models. It also lets models that only the account lists
// (not this proxy's registry) route to the forwarding credentials, with effort passed through untouched.
//
// The caller's login is used for the one upstream request and never stored, logged or cached.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/sjson"
)

const (
	codexDefaultBackendURL   = "https://chatgpt.com/backend-api/codex"
	codexAccountCatalogTTL   = 60 * time.Second
	codexProviderCatalogTTL  = 5 * time.Minute
	codexCatalogFetchTimeout = 15 * time.Second
	codexCatalogMaxBytes     = 4 << 20
)

// Headers Codex sends with its catalog request that ChatGPT's backend may rely on. Everything else stays here.
var codexCatalogForwardHeaders = []string{"Authorization", "ChatGPT-Account-ID", "X-OpenAI-Fedramp", "Originator", "User-Agent", "Version", "Accept-Language"}

type accountCatalogEntry struct {
	body    []byte
	etag    string
	fetched time.Time
}

type providerCatalogEntry struct {
	models  map[string]json.RawMessage
	fetched time.Time
}

// accountModel is what routing needs to know about a model only the account lists.
type accountModel struct {
	displayName string
	efforts     []string
}

var codexAccountCatalog = struct {
	mu        sync.Mutex
	byCaller  map[string]accountCatalogEntry // keyed by a hash of the caller's login, client version and account id
	providers map[string]providerCatalogEntry
	known     map[string]accountModel // slugs seen in any account catalog
	client    *http.Client
}{
	byCaller:  map[string]accountCatalogEntry{},
	providers: map[string]providerCatalogEntry{},
	known:     map[string]accountModel{},
	client:    &http.Client{Timeout: codexCatalogFetchTimeout, Transport: http.DefaultTransport},
}

// CodexAccountCatalogEnabled reports whether client.codex.account-catalog is on.
func (h *BaseAPIHandler) CodexAccountCatalogEnabled() bool {
	cfg := h.CurrentConfig()
	return cfg != nil && cfg.Client.Codex.AccountCatalog
}

func isForwardingCodexCredential(a *coreauth.Auth) bool {
	if a == nil || a.Disabled || !strings.EqualFold(a.Provider, "codex") || a.Attributes == nil {
		return false
	}
	if strings.TrimSpace(a.Attributes["api_key"]) != "" {
		return false
	}
	for k, v := range a.Attributes {
		if strings.EqualFold(k, "header:Authorization") && strings.EqualFold(strings.TrimSpace(v), "$Authorization") {
			return true
		}
	}
	return false
}

func (h *BaseAPIHandler) forwardingCodexCredentials() []*coreauth.Auth {
	if h == nil || h.AuthManager == nil {
		return nil
	}
	var out []*coreauth.Auth
	for _, a := range h.AuthManager.List() {
		if isForwardingCodexCredential(a) {
			out = append(out, a)
		}
	}
	return out
}

func codexAPIKeyCredentials(m *coreauth.Manager) []*coreauth.Auth {
	if m == nil {
		return nil
	}
	var out []*coreauth.Auth
	for _, a := range m.List() {
		if a != nil && !a.Disabled && strings.EqualFold(a.Provider, "codex") && a.Attributes != nil &&
			strings.TrimSpace(a.Attributes["api_key"]) != "" && strings.TrimSpace(a.Attributes["base_url"]) != "" {
			out = append(out, a)
		}
	}
	return out
}

// ServeCodexAccountCatalog answers a Codex catalog request with the caller's account catalog. It returns false
// (and writes nothing) when it doesn't apply: the switch is off, no credential forwards the caller's login, or the
// caller sent no ChatGPT bearer. Upstream errors are passed back with their status, so Codex keeps its own fallback
// and refreshes its login on a 401 as it would against ChatGPT.
func (h *BaseAPIHandler) ServeCodexAccountCatalog(w http.ResponseWriter, r *http.Request, clientVersion string) bool {
	if !h.CodexAccountCatalogEnabled() || r == nil {
		return false
	}
	creds := h.forwardingCodexCredentials()
	bearer := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(creds) == 0 || !strings.HasPrefix(strings.ToLower(bearer), "bearer ") || len(bearer) < 20 {
		return false
	}
	base := codexDefaultBackendURL
	if b := strings.TrimSpace(creds[0].Attributes["base_url"]); b != "" {
		base = b
	}

	sum := sha256.Sum256([]byte(bearer + "\x00" + r.Header.Get("ChatGPT-Account-ID") + "\x00" + clientVersion))
	key := hex.EncodeToString(sum[:])
	codexAccountCatalog.mu.Lock()
	cached, ok := codexAccountCatalog.byCaller[key]
	codexAccountCatalog.mu.Unlock()
	if !ok || time.Since(cached.fetched) > codexAccountCatalogTTL {
		status, body, etag, err := fetchAccountCatalog(r.Context(), base, clientVersion, r.Header)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "account catalog unavailable: "+err.Error())
			return true
		}
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return true
		}
		merged, slugs, errMerge := h.mergeProviderCatalogs(r.Context(), body, clientVersion)
		if errMerge != nil {
			writeJSONError(w, http.StatusBadGateway, "account catalog unreadable: "+errMerge.Error())
			return true
		}
		cached = accountCatalogEntry{body: merged, etag: etag, fetched: time.Now()}
		codexAccountCatalog.mu.Lock()
		codexAccountCatalog.byCaller[key] = cached
		for slug, m := range slugs {
			codexAccountCatalog.known[slug] = m
		}
		for k, v := range codexAccountCatalog.byCaller { // drop stale entries; nothing outlives a few TTLs
			if time.Since(v.fetched) > 5*codexAccountCatalogTTL {
				delete(codexAccountCatalog.byCaller, k)
			}
		}
		codexAccountCatalog.mu.Unlock()
		h.registerAccountModels(creds)
	}
	w.Header().Set("Content-Type", "application/json")
	if cached.etag != "" {
		w.Header().Set("ETag", cached.etag)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cached.body)
	return true
}

func fetchAccountCatalog(ctx context.Context, base, clientVersion string, in http.Header) (int, []byte, string, error) {
	u := strings.TrimSuffix(base, "/") + "/models?client_version=" + url.QueryEscape(clientVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, "", err
	}
	for _, name := range codexCatalogForwardHeaders {
		if v := in.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := codexAccountCatalog.client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, codexCatalogMaxBytes))
	if err != nil {
		return 0, nil, "", err
	}
	return resp.StatusCode, body, resp.Header.Get("ETag"), nil
}

// mergeProviderCatalogs appends each Codex API-key provider's own catalog entries (for the models configured on it)
// after the account's entries. Account entries are kept as raw JSON, so nothing in them changes.
func (h *BaseAPIHandler) mergeProviderCatalogs(ctx context.Context, accountBody []byte, clientVersion string) ([]byte, map[string]accountModel, error) {
	var account struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(accountBody, &account); err != nil {
		return nil, nil, err
	}
	slugs := map[string]accountModel{}
	seen := map[string]bool{}
	maxPriority := int64(0)
	for _, raw := range account.Models {
		var m struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Priority    int64  `json:"priority"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Slug != "" {
			if m.Priority > maxPriority {
				maxPriority = m.Priority
			}
			seen[m.Slug] = true
			am := accountModel{displayName: m.DisplayName}
			for _, l := range m.Levels {
				am.efforts = append(am.efforts, l.Effort)
			}
			slugs[m.Slug] = am
		}
	}
	out := account.Models
	for _, a := range codexAPIKeyCredentials(h.AuthManager) {
		listed := registry.GetGlobalRegistry().GetModelsForClient(a.ID)
		if len(listed) == 0 {
			continue
		}
		entries := providerCatalog(ctx, a, clientVersion)
		for _, info := range listed {
			if info == nil || seen[info.ID] {
				continue
			}
			if raw, ok := entries[info.ID]; ok {
				// Provider rows sort after every account row (Codex orders the picker by priority and takes the first
				// listed model as the default), keeping their own relative order.
				maxPriority++
				if moved, errSet := sjson.SetBytes(raw, "priority", maxPriority); errSet == nil {
					raw = moved
				}
				out = append(out, raw)
				seen[info.ID] = true
			}
		}
	}
	merged, err := json.Marshal(map[string]any{"models": out})
	return merged, slugs, err
}

func providerCatalog(ctx context.Context, a *coreauth.Auth, clientVersion string) map[string]json.RawMessage {
	cacheKey := a.ID + "\x00" + clientVersion
	codexAccountCatalog.mu.Lock()
	cached, ok := codexAccountCatalog.providers[cacheKey]
	codexAccountCatalog.mu.Unlock()
	if ok && time.Since(cached.fetched) < codexProviderCatalogTTL {
		return cached.models
	}
	models := map[string]json.RawMessage{}
	u := strings.TrimSuffix(a.Attributes["base_url"], "/") + "/models?client_version=" + url.QueryEscape(clientVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.Attributes["api_key"]))
		req.Header.Set("Accept", "application/json")
		if resp, errDo := codexAccountCatalog.client.Do(req); errDo == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, codexCatalogMaxBytes))
			resp.Body.Close()
			var cat struct {
				Models []json.RawMessage `json:"models"`
			}
			if resp.StatusCode == http.StatusOK && json.Unmarshal(body, &cat) == nil {
				for _, raw := range cat.Models {
					var m struct {
						Slug string `json:"slug"`
					}
					if json.Unmarshal(raw, &m) == nil && m.Slug != "" {
						models[m.Slug] = raw
					}
				}
			}
		}
	}
	codexAccountCatalog.mu.Lock()
	codexAccountCatalog.providers[cacheKey] = providerCatalogEntry{models: models, fetched: time.Now()}
	codexAccountCatalog.mu.Unlock()
	return models
}

// registerAccountModels adds models only the account lists to every forwarding credential, so the router can send
// them there. They pass reasoning settings through unchanged (SupportConfigurationUpdate), as ChatGPT validates them.
func (h *BaseAPIHandler) registerAccountModels(creds []*coreauth.Auth) {
	codexAccountCatalog.mu.Lock()
	known := make(map[string]accountModel, len(codexAccountCatalog.known))
	for k, v := range codexAccountCatalog.known {
		known[k] = v
	}
	codexAccountCatalog.mu.Unlock()
	reg := registry.GetGlobalRegistry()
	for _, a := range creds {
		current := reg.GetModelsForClient(a.ID)
		have := map[string]bool{}
		for _, m := range current {
			if m != nil {
				have[m.ID] = true
			}
		}
		added := false
		next := append([]*registry.ModelInfo(nil), current...)
		for slug, m := range known {
			if have[slug] {
				continue
			}
			next = append(next, &registry.ModelInfo{
				ID: slug, Object: "model", OwnedBy: "openai", Type: "openai", DisplayName: m.displayName,
				SupportConfigurationUpdate: true, Thinking: &registry.ThinkingSupport{Levels: m.efforts},
			})
			added = true
		}
		if added {
			reg.RegisterClient(a.ID, "codex", next)
		}
	}
}

// RouteCodexAccountModel makes a model that only an account catalog listed routable, registering it on the
// forwarding credentials if a registry refresh dropped it. It reports whether the model is now routable.
func (h *BaseAPIHandler) RouteCodexAccountModel(model string) bool {
	if !h.CodexAccountCatalogEnabled() {
		return false
	}
	codexAccountCatalog.mu.Lock()
	_, ok := codexAccountCatalog.known[model]
	codexAccountCatalog.mu.Unlock()
	if !ok {
		return false
	}
	creds := h.forwardingCodexCredentials()
	if len(creds) == 0 {
		return false
	}
	h.registerAccountModels(creds)
	return true
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "server_error"}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

