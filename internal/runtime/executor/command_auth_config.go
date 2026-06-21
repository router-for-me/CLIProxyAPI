package executor

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func commandAuthMatches(authCfg *config.CommandAuthConfig, cfgBase, attrBase string, auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	attrCommandKey := strings.TrimSpace(auth.Attributes[cliproxyauth.AttrAuthCommandKey])
	if attrCommandKey == "" {
		return false
	}
	cfgCommandKey := config.CommandAuthIdentity(authCfg)
	if cfgCommandKey == "" || !strings.EqualFold(attrCommandKey, cfgCommandKey) {
		return false
	}
	if attrBase == "" {
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	cfgBase = strings.TrimSpace(cfgBase)
	return cfgBase == "" || strings.EqualFold(cfgBase, attrBase)
}

func commandAuthMetadataToken(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	token, _ := auth.Metadata["access_token"].(string)
	return strings.TrimSpace(token)
}
