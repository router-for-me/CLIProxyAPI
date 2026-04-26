package executor

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type codexRequestIdentity struct {
	userAgent  string
	originator string
}

func codexIsAPIKeyAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["api_key"]) != ""
}

func codexResolvedUserAgent(target http.Header, source http.Header, auth *cliproxyauth.Auth, cfg *config.Config) string {
	return codexResolvedIdentity(target, source, auth, cfg).userAgent
}

func codexConfiguredUserAgent(cfg *config.Config, auth *cliproxyauth.Auth) string {
	userAgent, _ := codexHeaderDefaults(cfg, auth)
	return userAgent
}

func codexResolvedOriginator(target http.Header, source http.Header, auth *cliproxyauth.Auth) string {
	return codexResolvedIdentity(target, source, auth, nil).originator
}

func codexResolvedIdentity(target http.Header, source http.Header, auth *cliproxyauth.Auth, cfg *config.Config) codexRequestIdentity {
	identity := codexRequestIdentity{
		originator: codexResolvedOriginatorValue(target, source, auth),
	}
	configuredUserAgent := codexConfiguredUserAgent(cfg, auth)
	authUserAgent := codexAuthUserAgent(auth)
	switch {
	case configuredUserAgent != "":
		identity.userAgent = configuredUserAgent
	case authUserAgent != "":
		identity.userAgent = authUserAgent
	case target != nil && strings.TrimSpace(target.Get("User-Agent")) != "":
		identity.userAgent = strings.TrimSpace(target.Get("User-Agent"))
	case source != nil && strings.TrimSpace(source.Get("User-Agent")) != "":
		identity.userAgent = strings.TrimSpace(source.Get("User-Agent"))
	default:
		identity.userAgent = misc.CodexCLIUserAgentWithOriginator(identity.originator)
	}
	return identity
}

func codexResolvedOriginatorValue(target http.Header, source http.Header, auth *cliproxyauth.Auth) string {
	if authOriginator := codexAuthOriginator(auth); authOriginator != "" {
		return authOriginator
	}
	if target != nil {
		if originator := strings.TrimSpace(target.Get("Originator")); originator != "" {
			return originator
		}
	}
	if source != nil {
		if originator := strings.TrimSpace(source.Get("Originator")); originator != "" {
			return originator
		}
	}
	return codexOriginator
}

func codexAuthOriginator(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		for _, key := range []string{"header:Originator", "originator"} {
			if originator := strings.TrimSpace(auth.Attributes[key]); originator != "" {
				return originator
			}
		}
	}
	if auth.Metadata == nil {
		return ""
	}
	for _, key := range []string{"originator", "Originator"} {
		if originator, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(originator) != "" {
			return strings.TrimSpace(originator)
		}
	}
	return ""
}

type codexSessionHeaderOptions struct {
	includeRequestID bool
}

func codexEnsureSessionHeaders(target http.Header, source http.Header, auth *cliproxyauth.Auth, opts codexSessionHeaderOptions) string {
	if target == nil {
		return ""
	}
	conversationID := firstNonEmptyHeaderValue(target, source, "Conversation_id")
	sessionID := firstNonEmptyHeaderValue(target, source, "Session_id")
	if sessionID == "" {
		sessionID = conversationID
	}
	if sessionID == "" {
		sessionID = codexTurnMetadataSessionID(target, source)
	}
	if sessionID == "" {
		if apiKey, _ := codexCreds(auth); strings.TrimSpace(apiKey) != "" {
			sessionID = helps.CachedSessionID(apiKey)
		} else {
			sessionID = uuid.NewString()
		}
	}
	target.Set("Session_id", sessionID)

	requestID := firstNonEmptyHeaderValue(target, source, "X-Client-Request-Id")
	if opts.includeRequestID && requestID == "" {
		requestID = conversationID
	}
	if opts.includeRequestID && requestID == "" {
		requestID = sessionID
	}
	if requestID != "" {
		target.Set("X-Client-Request-Id", requestID)
	} else {
		target.Del("X-Client-Request-Id")
	}
	target.Del("Conversation_id")
	return sessionID
}

func firstNonEmptyHeaderValue(target http.Header, source http.Header, key string) string {
	if target != nil {
		if value := strings.TrimSpace(target.Get(key)); value != "" {
			return value
		}
	}
	if source != nil {
		if value := strings.TrimSpace(source.Get(key)); value != "" {
			return value
		}
	}
	return ""
}
