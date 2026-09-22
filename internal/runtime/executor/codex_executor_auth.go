package executor

import (
	"context"
	"strconv"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := codexauth.NewCodexAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
		if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeConfigIndex])); errIndex == nil && index >= 0 && index < len(e.cfg.CodexKey) {
			entry := &e.cfg.CodexKey[index]
			cfgKey := strings.TrimSpace(entry.APIKey)
			cfgBase := strings.TrimSpace(entry.BaseURL)
			if (attrKey == "" || strings.EqualFold(cfgKey, attrKey)) && (attrBase == "" || strings.EqualFold(cfgBase, attrBase)) {
				return entry
			}
		}
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func (e *CodexExecutor) resolveCodexModelIsCompat(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, baseModel string) bool {
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok && modelInfo != nil {
		return modelInfo.IsCompat
	}
	entry := e.resolveCodexConfig(auth)
	if entry != nil && len(entry.Models) > 0 {
		requested := strings.TrimSpace(req.Model)
		target := strings.TrimSpace(baseModel)
		for i := range entry.Models {
			name := strings.TrimSpace(entry.Models[i].Name)
			alias := strings.TrimSpace(entry.Models[i].Alias)
			if (target != "" && (strings.EqualFold(name, target) || strings.EqualFold(alias, target))) ||
				(requested != "" && (strings.EqualFold(name, requested) || strings.EqualFold(alias, requested))) {
				return entry.Models[i].IsCompat
			}
		}
		return false
	}
	if cliproxyauth.CodexAPIKeyModelIsCompat(e.cfg, auth, baseModel) || cliproxyauth.CodexAPIKeyModelIsCompat(e.cfg, auth, req.Model) {
		return true
	}
	return false
}
