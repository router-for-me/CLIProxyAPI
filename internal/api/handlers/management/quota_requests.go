package management

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func quotaProviderKey(provider string) string {
	key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(provider)), "_", "-")
	switch key {
	case "kimi-ai", "kimi.ai", "kimi.com":
		return "kimi"
	default:
		return key
	}
}

// Quota requests resolve credential-only information on the server, without
// requiring callers to download the owner's authentication file.
func prepareCredentialQuotaRequest(auth *coreauth.Auth, target *url.URL, headers map[string]string) error {
	if auth == nil || target.Scheme != "https" {
		return nil
	}
	host := strings.ToLower(target.Hostname())
	switch quotaProviderKey(auth.Provider) {
	case "antigravity":
		if headers != nil && hubQuotaRouteAllowed("antigravity", host, target.Path) {
			// Google gates quota groups by client version, not just model execution.
			for key := range headers {
				if strings.EqualFold(key, "User-Agent") {
					delete(headers, key)
				}
			}
			headers["User-Agent"] = misc.AntigravityUserAgent()
		}
	case "kiro":
		if !hubQuotaRouteAllowed("kiro", host, target.Path) {
			return nil
		}
		control, errParse := url.Parse(kiro.ControlURL(auth.Metadata))
		if errParse != nil || control.Host == "" {
			return errors.New("invalid Kiro control endpoint")
		}
		target.Scheme, target.Host = control.Scheme, control.Host
		query := target.Query()
		query.Set("origin", "AI_EDITOR")
		query.Set("resourceType", "AGENTIC_REQUEST")
		query.Set("isEmailRequired", "true")
		if arn := kiro.String(auth.Metadata, "profile_arn"); arn != "" {
			query.Set("profileArn", arn)
		} else {
			query.Del("profileArn")
		}
		target.RawQuery = query.Encode()
	case "kimi":
		if (host == "api.kimi.com" || host == "api.kimi.ai") && target.Path == "/coding/v1/usages" {
			regionalAuth := *auth
			regionalAuth.Provider = strings.ReplaceAll(auth.Provider, "_", "-")
			if kimiauth.IsKimiAIAuth(&regionalAuth) {
				target.Host = "api.kimi.ai"
			} else {
				target.Host = "api.kimi.com"
			}
		}
	case "meta":
		if host != "api.meta.ai" || target.Path != "/muse-code/key" {
			return nil
		}
		for key, value := range headers {
			if !strings.EqualFold(key, "Authorization") || !strings.Contains(value, "$DCA_TOKEN$") {
				continue
			}
			raw, _ := auth.Metadata["dca_token"].(string)
			token := strings.TrimSpace(raw)
			if !strings.HasPrefix(token, "dca:") || len(token) <= len("dca:") || strings.ContainsFunc(token, unicode.IsSpace) {
				return errors.New("Meta DCA token unavailable")
			}
			headers[key] = strings.ReplaceAll(value, "$DCA_TOKEN$", token)
		}
	}
	return nil
}
