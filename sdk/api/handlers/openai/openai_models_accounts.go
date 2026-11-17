package openai

import (
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// includeAccountsParam is the /v1/models query value that adds, per model, the kinds of credential serving it.
const includeAccountsParam = "accounts"

// modelAccount describes one credential that serves a model, without anything that identifies it.
type modelAccount struct {
	Provider string `json:"provider"`
	// Kind is "oauth" for a subscription login and "apikey" for an API key.
	Kind     string `json:"kind"`
	Priority int    `json:"priority"`
	// Host is the host of an API key's base URL, when it has one: an OpenAI-compatible entry's name is the operator's
	// choice, so the host is what tells the upstream service. Only the host: no credentials, port, path or query.
	Host string `json:"host,omitempty"`
}

// baseURLHost returns the lowercased host of an API key's base URL, or "" when there is none.
func baseURLHost(auth *coreauth.Auth) string {
	if auth.Attributes == nil || auth.AuthKind() != "apikey" {
		return ""
	}
	parsed, errParse := url.Parse(strings.TrimSpace(auth.Attributes["base_url"]))
	if errParse != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// wantsAccounts reports whether the request asked for the accounts of each model (`?include=accounts`).
func wantsAccounts(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	for _, value := range c.QueryArray("include") {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), includeAccountsParam) {
				return true
			}
		}
	}
	return false
}

// modelAccounts lists, per model ID, the enabled credentials that serve it, in the order CPA tries them: highest
// priority first. A client uses it to tell whether a model is reached through a subscription login or an API key,
// which can differ in limits, prompt-cache lifetime and price. Only provider, kind, priority and, for an API key with a
// base URL, its host are given.
func (h *OpenAIAPIHandler) modelAccounts() map[string][]modelAccount {
	if h == nil || h.BaseAPIHandler == nil || h.AuthManager == nil {
		return nil
	}
	modelRegistry := registry.GetGlobalRegistry()
	accounts := make(map[string][]modelAccount)
	for _, auth := range h.AuthManager.List() {
		if auth == nil || auth.Disabled {
			continue
		}
		account := modelAccount{Provider: auth.Provider, Kind: auth.AuthKind(), Priority: auth.Priority(), Host: baseURLHost(auth)}
		for _, model := range modelRegistry.GetModelsForClient(auth.ID) {
			if model == nil || model.ID == "" {
				continue
			}
			accounts[model.ID] = append(accounts[model.ID], account)
		}
	}
	for _, list := range accounts {
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Priority != list[j].Priority {
				return list[i].Priority > list[j].Priority
			}
			if list[i].Provider != list[j].Provider {
				return list[i].Provider < list[j].Provider
			}
			return list[i].Kind < list[j].Kind
		})
	}
	return accounts
}
