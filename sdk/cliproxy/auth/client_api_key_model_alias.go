package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type clientAPIKeyModelAliasTable map[string]map[string]oauthModelAliasEntry

func compileClientAPIKeyModelAliasTable(entries internalconfig.ClientAPIKeys) clientAPIKeyModelAliasTable {
	if len(entries) == 0 {
		return nil
	}
	out := make(clientAPIKeyModelAliasTable, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		if key == "" || len(entry.ModelAliases) == 0 {
			continue
		}
		rev := make(map[string]oauthModelAliasEntry, len(entry.ModelAliases))
		for _, aliasEntry := range entry.ModelAliases {
			name := strings.TrimSpace(aliasEntry.Name)
			alias := strings.TrimSpace(aliasEntry.Alias)
			if name == "" || alias == "" || strings.EqualFold(name, alias) {
				continue
			}
			aliasKey := strings.ToLower(alias)
			if _, exists := rev[aliasKey]; exists {
				continue
			}
			rev[aliasKey] = oauthModelAliasEntry{
				upstreamModel: name,
				configAlias:   alias,
				forceMapping:  aliasEntry.ForceMapping,
			}
		}
		if len(rev) > 0 {
			out[strings.ToLower(key)] = rev
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SetClientAPIKeyModelAliases updates per-client API key model alias mappings.
func (m *Manager) SetClientAPIKeyModelAliases(entries internalconfig.ClientAPIKeys) {
	if m == nil {
		return
	}
	table := compileClientAPIKeyModelAliasTable(entries)
	m.clientAPIKeyModelAlias.Store(table)
}

func (m *Manager) clientAPIKeyModelAliasTable() clientAPIKeyModelAliasTable {
	if m == nil {
		return nil
	}
	raw := m.clientAPIKeyModelAlias.Load()
	table, _ := raw.(clientAPIKeyModelAliasTable)
	return table
}

// ClientAPIKeyPrincipalFromContext returns the authenticated client API key from a request context.
func ClientAPIKeyPrincipalFromContext(ctx interface {
	Value(any) any
}) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) })
	if !ok || ginCtx == nil {
		return ""
	}
	raw, ok := ginCtx.Get("userApiKey")
	if !ok {
		return ""
	}
	return contextStringValue(raw)
}

func (m *Manager) resolveClientAPIKeyModelAliasWithResult(clientKey, requestedModel string) OAuthModelAliasResult {
	if m == nil {
		return OAuthModelAliasResult{}
	}
	clientKey = strings.TrimSpace(clientKey)
	if clientKey == "" {
		return OAuthModelAliasResult{}
	}
	table := m.clientAPIKeyModelAliasTable()
	if len(table) == 0 {
		return OAuthModelAliasResult{}
	}
	rev := table[strings.ToLower(clientKey)]
	if len(rev) == 0 {
		return OAuthModelAliasResult{}
	}
	requestResult, candidates := modelAliasLookupCandidates(requestedModel)
	if len(candidates) == 0 {
		return OAuthModelAliasResult{}
	}
	baseModel := requestResult.ModelName
	if baseModel == "" {
		baseModel = strings.TrimSpace(requestedModel)
	}
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate))
		if key == "" {
			continue
		}
		entry, exists := rev[key]
		if !exists {
			continue
		}
		targetModel := entry.upstreamModel
		if targetModel == "" {
			continue
		}
		if strings.EqualFold(targetModel, baseModel) {
			if !entry.forceMapping {
				return OAuthModelAliasResult{}
			}
			return OAuthModelAliasResult{
				UpstreamModel: preserveResolvedModelSuffix(targetModel, requestResult),
				ForceMapping:  entry.forceMapping,
				OriginalAlias: oauthModelAliasForceMappingResponseModel(entry.configAlias),
			}
		}
		originalAlias := requestedModel
		if entry.forceMapping {
			originalAlias = oauthModelAliasForceMappingResponseModel(entry.configAlias)
		}
		return OAuthModelAliasResult{
			UpstreamModel: preserveResolvedModelSuffix(targetModel, requestResult),
			ForceMapping:  entry.forceMapping,
			OriginalAlias: originalAlias,
		}
	}
	return OAuthModelAliasResult{}
}

func (m *Manager) applyClientAPIKeyModelAlias(clientKey, requestedModel string) string {
	result := m.resolveClientAPIKeyModelAliasWithResult(clientKey, requestedModel)
	if result.UpstreamModel == "" {
		return requestedModel
	}
	return result.UpstreamModel
}

// ApplyClientAPIKeyModelAlias resolves the upstream model name for a client API key alias.
func (m *Manager) ApplyClientAPIKeyModelAlias(clientKey, requestedModel string) string {
	return m.applyClientAPIKeyModelAlias(clientKey, requestedModel)
}

// RuntimeConfig returns the latest application config snapshot used by the auth manager.
func (m *Manager) RuntimeConfig() *internalconfig.Config {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg
}

func (m *Manager) resolveExecutionAliasResultForRequestedWithClient(ctx interface {
	Value(any) any
}, auth *Auth, requestedModel string) OAuthModelAliasResult {
	clientKey := ClientAPIKeyPrincipalFromContext(ctx)
	if result := m.resolveClientAPIKeyModelAliasWithResult(clientKey, requestedModel); result.UpstreamModel != "" {
		return result
	}
	return m.resolveExecutionAliasResultForRequested(auth, requestedModel)
}
