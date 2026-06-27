package executor

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func signAnthropicMessagesBody(body []byte) []byte {
	billingHeader := gjson.GetBytes(body, "system.0.text").String()
	if !strings.HasPrefix(billingHeader, "x-anthropic-billing-header:") {
		return body
	}
	updatedHeader, updated := stripClaudeBillingHeaderCCH(billingHeader)
	if !updated {
		return body
	}
	out, err := sjson.SetBytes(body, "system.0.text", updatedHeader)
	if err != nil {
		return body
	}
	return out
}

func stripClaudeBillingHeaderCCH(billingHeader string) (string, bool) {
	segments := strings.Split(billingHeader, ";")
	out := make([]string, 0, len(segments))
	updated := false
	for _, segment := range segments {
		if strings.HasPrefix(strings.TrimSpace(segment), "cch=") {
			updated = true
			continue
		}
		out = append(out, segment)
	}
	if !updated {
		return billingHeader, false
	}
	return strings.Join(out, ";"), true
}

func resolveClaudeKeyConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.ClaudeKey {
	if cfg == nil || auth == nil {
		return nil
	}

	apiKey, baseURL := claudeCreds(auth)
	if apiKey == "" {
		return nil
	}

	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if !strings.EqualFold(cfgKey, apiKey) {
			continue
		}
		if baseURL != "" && cfgBase != "" && !strings.EqualFold(cfgBase, baseURL) {
			continue
		}
		return entry
	}

	return nil
}

// resolveClaudeKeyCloakConfig finds the matching ClaudeKey config and returns its CloakConfig.
func resolveClaudeKeyCloakConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.CloakConfig {
	entry := resolveClaudeKeyConfig(cfg, auth)
	if entry == nil {
		return nil
	}
	return entry.Cloak
}

func experimentalCCHSigningEnabled(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	entry := resolveClaudeKeyConfig(cfg, auth)
	return entry != nil && entry.ExperimentalCCHSigning
}

func rebuildMidSystemMessageEnabled(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	if auth != nil && auth.Attributes != nil && strings.EqualFold(strings.TrimSpace(auth.Attributes["rebuild_mid_system_message"]), "true") {
		return true
	}
	entry := resolveClaudeKeyConfig(cfg, auth)
	return entry != nil && entry.RebuildMidSystemMessage
}
