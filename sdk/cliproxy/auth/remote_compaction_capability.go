package auth

import (
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// AttributeRemoteCompactionV2 marks an explicitly configured provider as
// supporting the Responses remote compaction v2 protocol.
const AttributeRemoteCompactionV2 = "remote_compaction_v2"

const remoteCompactionRequiredMetadataKey = "cliproxy.remote_compaction_v2.required"

// requestHasRemoteCompactionTrigger reports whether a Responses request asks
// the upstream to compact a conversation. It intentionally only inspects the
// input item type and does not depend on executor internals.
func requestHasRemoteCompactionTrigger(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if strings.TrimSpace(item.Get("type").String()) == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}

func markRemoteCompactionRequirement(opts cliproxyexecutor.Options, payload []byte) cliproxyexecutor.Options {
	if !requestHasRemoteCompactionTrigger(payload) && !requestHasRemoteCompactionTrigger(opts.OriginalRequest) {
		return opts
	}
	opts.EnsureMetadata()[remoteCompactionRequiredMetadataKey] = true
	return opts
}

// ensureRemoteCompactionCapability fails before selection when no configured
// credential can handle the request. This keeps the failure request-scoped so
// it cannot cool down credentials or trigger credential retries.
func (m *Manager) ensureRemoteCompactionCapability(providers []string, opts cliproxyexecutor.Options) error {
	if m == nil || !remoteCompactionRequired(opts) || m.HomeEnabled() {
		return nil
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" {
			providerSet[provider] = struct{}{}
		}
	}
	if len(providerSet) == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled || !authHandlesRemoteCompactionTrigger(auth) {
			continue
		}
		provider := executorKeyFromAuth(auth)
		if _, ok := providerSet[provider]; !ok {
			continue
		}
		if _, ok := m.executors[provider]; !ok {
			continue
		}
		return nil
	}
	return remoteCompactionUnsupportedError()
}

func remoteCompactionRequired(opts cliproxyexecutor.Options) bool {
	if len(opts.Metadata) > 0 {
		switch value := opts.Metadata[remoteCompactionRequiredMetadataKey].(type) {
		case bool:
			if value {
				return true
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(value), "true") {
				return true
			}
		}
	}
	return requestHasRemoteCompactionTrigger(opts.OriginalRequest)
}

func authSupportsRemoteCompactionV2(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil && strings.EqualFold(strings.TrimSpace(auth.Attributes[AttributeRemoteCompactionV2]), "true") {
		return true
	}
	// Native Codex OAuth/API credentials speak the Responses compaction
	// protocol by default. Compatibility credentials must opt in explicitly.
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") && !isOpenAICompatAuth(auth)
}

// authHandlesRemoteCompactionTrigger reports whether a credential can consume a
// compaction_trigger item. Native Codex and explicit opt-in providers speak
// remote compaction v2. xAI keeps origin's v1 adapter (strip trigger, POST
// /responses/compact) so xAI-only pools remain selectable.
func authHandlesRemoteCompactionTrigger(auth *Auth) bool {
	if authSupportsRemoteCompactionV2(auth) {
		return true
	}
	return auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "xai")
}

func isOpenAICompatAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["compat_name"]) != "" {
		return true
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	return provider == "openai-compatibility" || strings.HasPrefix(provider, "openai-compatibility:")
}

func remoteCompactionUnsupportedError() *Error {
	return NewRequestScopedError("remote compaction v2 is not supported by any eligible provider", http.StatusNotImplemented)
}
