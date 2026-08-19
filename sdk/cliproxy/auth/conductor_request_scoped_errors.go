package auth

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Request-scoped error actions.
const (
	RequestScopedActionStop                = "stop"
	RequestScopedActionStopAndCooldown     = "stop-and-cooldown"
	RequestScopedActionContinue            = "continue"
	RequestScopedActionContinueAndCooldown = "continue-and-cooldown"
)

type requestStopError struct {
	error
}

func (e requestStopError) Unwrap() error {
	return e.error
}

func (e requestStopError) IsRequestStop() bool {
	return true
}

func isRequestStopError(err error) bool {
	if err == nil {
		return false
	}
	type stopChecker interface {
		IsRequestStop() bool
	}
	var sc stopChecker
	return errors.As(err, &sc) && sc != nil && sc.IsRequestStop()
}

func unwrapRequestStopError(err error) error {
	var stopErr requestStopError
	if errors.As(err, &stopErr) {
		return stopErr.error
	}
	return err
}

func wrapRequestStopError(err error) error {
	if err == nil {
		return nil
	}
	return requestStopError{error: unwrapRequestStopError(err)}
}

func (m *Manager) runtimeConfigSnapshot() *internalconfig.Config {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg
}

// extractRequestScopedErrorRules retrieves the configured RequestScopedErrorRule list for an auth.
func extractRequestScopedErrorRules(auth *Auth, cfg *internalconfig.Config) []internalconfig.RequestScopedErrorRule {
	if auth != nil && auth.Metadata != nil {
		if raw, ok := auth.Metadata["request_scoped_errors"]; ok && raw != nil {
			switch typed := raw.(type) {
			case []internalconfig.RequestScopedErrorRule:
				if len(typed) > 0 {
					return typed
				}
			case []any:
				var rules []internalconfig.RequestScopedErrorRule
				if data, errMarshal := json.Marshal(typed); errMarshal == nil {
					if errUnmarshal := json.Unmarshal(data, &rules); errUnmarshal == nil && len(rules) > 0 {
						return rules
					}
				}
			}
		}
	}
	if cfg == nil || auth == nil {
		return nil
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	index := -1
	if auth.Attributes != nil {
		if idxStr, ok := auth.Attributes[AttributeConfigIndex]; ok {
			if parsed, errIndex := strconv.Atoi(strings.TrimSpace(idxStr)); errIndex == nil && parsed >= 0 {
				index = parsed
			}
		}
	}

	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = auth.Attributes["provider_key"]
		compatName = auth.Attributes["compat_name"]
	}
	if compatName == "" {
		if strings.HasPrefix(provider, "openai-compatible-") {
			compatName = strings.TrimPrefix(provider, "openai-compatible-")
		} else if strings.HasPrefix(provider, "openai-compatibility:") {
			compatName = strings.TrimPrefix(provider, "openai-compatibility:")
		}
	}
	if compatName != "" || providerKey != "" || provider == "openai-compatibility" || strings.HasPrefix(provider, "openai-compatibility:") || strings.HasPrefix(provider, "openai-compatible") {
		if entry := resolveOpenAICompatConfigForAuth(cfg, auth, providerKey, compatName); entry != nil {
			return entry.RequestScopedErrors
		}
	}

	switch provider {
	case "claude":
		if index >= 0 && index < len(cfg.ClaudeKey) {
			return cfg.ClaudeKey[index].RequestScopedErrors
		}
	case "codex":
		if index >= 0 && index < len(cfg.CodexKey) {
			return cfg.CodexKey[index].RequestScopedErrors
		}
	case "xai":
		if index >= 0 && index < len(cfg.XAIKey) {
			return cfg.XAIKey[index].RequestScopedErrors
		}
	case "gemini":
		if index >= 0 && index < len(cfg.GeminiKey) {
			return cfg.GeminiKey[index].RequestScopedErrors
		}
	case "interactions", "gemini-interactions":
		if index >= 0 && index < len(cfg.InteractionsKey) {
			return cfg.InteractionsKey[index].RequestScopedErrors
		}
	}

	return nil
}

func extractErrorBody(err error) string {
	if err == nil {
		return ""
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var rbp responseBodyProvider
	if errors.As(err, &rbp) && rbp != nil {
		if b := rbp.ResponseBody(); len(b) > 0 {
			return string(b)
		}
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil && authErr.Message != "" {
		return authErr.Message
	}
	return err.Error()
}

// matchRequestScopedErrorAction evaluates an error against the auth's RequestScopedErrors rules.
// If a rule matches, it returns (action, true).
// If no rule matches, it returns ("", false).
func matchRequestScopedErrorAction(auth *Auth, err error, cfg *internalconfig.Config) (string, bool) {
	if err == nil {
		return "", false
	}
	rules := extractRequestScopedErrorRules(auth, cfg)
	if len(rules) == 0 {
		return "", false
	}

	statusCode := statusCodeFromError(err)
	body := extractErrorBody(err)

	for _, rule := range rules {
		if rule.Status <= 0 || rule.Status != statusCode {
			continue
		}
		if len(rule.Match) == 0 && len(rule.MatchRegexr) == 0 {
			continue
		}

		matched := false
		for _, substr := range rule.Match {
			if substr != "" && strings.Contains(body, substr) {
				matched = true
				break
			}
		}
		if !matched {
			for _, pattern := range rule.MatchRegexr {
				if pattern != "" {
					if re, errCompile := regexp.Compile(pattern); errCompile == nil && re.MatchString(body) {
						matched = true
						break
					}
				}
			}
		}
		if !matched {
			continue
		}

		action := strings.ToLower(strings.TrimSpace(rule.Action))
		switch action {
		case RequestScopedActionStop,
			RequestScopedActionStopAndCooldown,
			RequestScopedActionContinue,
			RequestScopedActionContinueAndCooldown:
			return action, true
		default:
			continue
		}
	}

	return "", false
}

func applyRequestScopedActionToResult(action string, okAction bool, result *Result) {
	if !okAction || result == nil || result.Error == nil {
		return
	}
	if action == RequestScopedActionStop || action == RequestScopedActionContinue {
		result.Error.Code = ErrorCodeRequestScoped
	} else if action == RequestScopedActionStopAndCooldown || action == RequestScopedActionContinueAndCooldown {
		result.Error.Code = ErrorCodeForceCooldown
	}
}

func isRequestScopedStop(action string, okAction bool) bool {
	return okAction && (action == RequestScopedActionStop || action == RequestScopedActionStopAndCooldown)
}
