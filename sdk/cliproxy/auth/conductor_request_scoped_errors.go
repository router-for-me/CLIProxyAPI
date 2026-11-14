package auth

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type requestScopedErrorConfigContextKey struct{}

// WithRequestScopedErrorConfig stores the runtime config snapshot on ctx so
// usage reporters can evaluate request-scoped error rules when publishing
// attempt failures.
func WithRequestScopedErrorConfig(ctx context.Context, cfg *internalconfig.Config) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg == nil {
		return ctx
	}
	return context.WithValue(ctx, requestScopedErrorConfigContextKey{}, cfg)
}

// RequestScopedErrorConfigFromContext retrieves the runtime config snapshot
// stored by WithRequestScopedErrorConfig.
func RequestScopedErrorConfigFromContext(ctx context.Context) *internalconfig.Config {
	if ctx == nil {
		return nil
	}
	cfg, _ := ctx.Value(requestScopedErrorConfigContextKey{}).(*internalconfig.Config)
	return cfg
}

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

type schedulerRejectError struct {
	error
}

func (e schedulerRejectError) Unwrap() error {
	return e.error
}

func (e schedulerRejectError) IsSchedulerReject() bool {
	return true
}

func isSchedulerRejectError(err error) bool {
	if err == nil {
		return false
	}
	type rejectChecker interface {
		IsSchedulerReject() bool
	}
	var rc rejectChecker
	return errors.As(err, &rc) && rc != nil && rc.IsSchedulerReject()
}

func unwrapSchedulerRejectError(err error) error {
	var rejectErr schedulerRejectError
	if errors.As(err, &rejectErr) {
		return rejectErr.error
	}
	return err
}

func wrapSchedulerRejectError(err error) error {
	if err == nil {
		return nil
	}
	return schedulerRejectError{error: unwrapSchedulerRejectError(err)}
}

type roundEndedBySchedulerRejectError struct {
	cause error
}

func (e *roundEndedBySchedulerRejectError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *roundEndedBySchedulerRejectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func hasRoundEndedBySchedulerReject(err error) bool {
	var marked *roundEndedBySchedulerRejectError
	return errors.As(err, &marked) && marked != nil
}

func markRoundEndedBySchedulerReject(err error) error {
	if err == nil || hasRoundEndedBySchedulerReject(err) {
		return err
	}
	return &roundEndedBySchedulerRejectError{cause: err}
}

func (m *Manager) runtimeConfigSnapshot() *internalconfig.Config {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg
}

func (m *Manager) requestScopedErrorConfig(ctx context.Context) *internalconfig.Config {
	if cfg := RequestScopedErrorConfigFromContext(ctx); cfg != nil {
		return cfg
	}
	return m.runtimeConfigSnapshot()
}

func cloneRequestScopedErrorRules(rules []internalconfig.RequestScopedErrorRule) []internalconfig.RequestScopedErrorRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]internalconfig.RequestScopedErrorRule, len(rules))
	for i, r := range rules {
		out[i] = internalconfig.RequestScopedErrorRule{
			Status: r.Status,
			Action: r.Action,
		}
		if len(r.Match) > 0 {
			out[i].Match = append([]string(nil), r.Match...)
		}
		if len(r.MatchRegexr) > 0 {
			out[i].MatchRegexr = append([]string(nil), r.MatchRegexr...)
		}
	}
	return out
}

// ResolveRequestScopedErrorRules extracts and deep-copies the effective
// RequestScopedErrorRule list for an auth and config snapshot.
func ResolveRequestScopedErrorRules(auth *Auth, cfg *internalconfig.Config) []internalconfig.RequestScopedErrorRule {
	return cloneRequestScopedErrorRules(extractRequestScopedErrorRules(auth, cfg))
}

// MatchRequestScopedErrorRules evaluates an error against a pre-resolved rule list.
func MatchRequestScopedErrorRules(rules []internalconfig.RequestScopedErrorRule, err error) (string, bool) {
	if err == nil || len(rules) == 0 || cliproxyexecutor.IsUpstreamWebsocketTurnError(err) {
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

// extractRequestScopedErrorRules retrieves the configured RequestScopedErrorRule list for an auth.
func extractRequestScopedErrorRules(auth *Auth, cfg *internalconfig.Config) []internalconfig.RequestScopedErrorRule {
	if auth != nil && auth.Metadata != nil {
		raw, ok := auth.Metadata["request_scoped_errors"]
		if !ok {
			raw, ok = auth.Metadata["request-scoped-errors"]
		}
		if ok && raw != nil {
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

	if auth.AuthKind() == AuthKindOAuth {
		if len(cfg.OAuthRequestScopedErrors) > 0 {
			provider := strings.ToLower(strings.TrimSpace(auth.Provider))
			if rules, ok := cfg.OAuthRequestScopedErrors[provider]; ok && len(rules) > 0 {
				return rules
			}
		}
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
	case "meta":
		if index >= 0 && index < len(cfg.MetaKey) {
			return cfg.MetaKey[index].RequestScopedErrors
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

// MatchRequestScopedErrorAction evaluates an error against the auth's RequestScopedErrors rules.
// If a rule matches, it returns (action, true).
// If no rule matches, it returns ("", false).
func MatchRequestScopedErrorAction(auth *Auth, err error, cfg *internalconfig.Config) (string, bool) {
	return matchRequestScopedErrorAction(auth, err, cfg)
}

func matchRequestScopedErrorAction(auth *Auth, err error, cfg *internalconfig.Config) (string, bool) {
	if err == nil || cliproxyexecutor.IsUpstreamWebsocketTurnError(err) {
		return "", false
	}
	return MatchRequestScopedErrorRules(extractRequestScopedErrorRules(auth, cfg), err)
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

// ApplyRequestScopedErrorResult evaluates request-scoped error rules for auth
// and applies any matched action code onto result before MarkResult.
func (m *Manager) ApplyRequestScopedErrorResult(ctx context.Context, auth *Auth, err error, result *Result) (string, bool) {
	var cfg *internalconfig.Config
	if m != nil {
		cfg = m.requestScopedErrorConfig(ctx)
	}
	action, okAction := matchRequestScopedErrorAction(auth, err, cfg)
	applyRequestScopedActionToResult(action, okAction, result)
	return action, okAction
}

func isRequestScopedStop(action string, okAction bool) bool {
	return okAction && (action == RequestScopedActionStop || action == RequestScopedActionStopAndCooldown)
}
