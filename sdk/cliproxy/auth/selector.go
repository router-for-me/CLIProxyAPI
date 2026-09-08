package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/credentialweight"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

// RoundRobinSelector provides a simple provider scoped round-robin selection strategy.
//
// Rotation continues from the identity of the previous pick rather than from a numeric
// index. Candidate slices shrink whenever a retry excludes already tried credentials or a
// credential enters cooldown, and indexing a monotonic counter into a shrinking slice
// silently re-seats the rotation, which starves some credentials and hammers others.
type RoundRobinSelector struct {
	mu         sync.Mutex
	lastPicked map[string]string
	maxKeys    int
}

// WeightedRoundRobinSelector provides smooth weighted round-robin selection.
type WeightedRoundRobinSelector struct {
	mu      sync.Mutex
	states  map[string]*smoothWeightedState
	maxKeys int
}

type smoothWeightedState struct {
	current map[string]int64
	weights map[string]int64
}

type weightedSelectorStateModelKey struct{}

func withWeightedSelectorStateModel(ctx context.Context, selector Selector, routeModel string) context.Context {
	if _, ok := selector.(*WeightedRoundRobinSelector); !ok || strings.TrimSpace(routeModel) == "" {
		return ctx
	}
	return context.WithValue(ctx, weightedSelectorStateModelKey{}, routeModel)
}

func weightedSelectorStateModel(ctx context.Context, availabilityModel string) string {
	if ctx != nil {
		if routeModel, ok := ctx.Value(weightedSelectorStateModelKey{}).(string); ok && strings.TrimSpace(routeModel) != "" {
			return routeModel
		}
	}
	return availabilityModel
}

// FillFirstSelector selects the first available credential (deterministic ordering).
// This "burns" one account before moving to the next, which can help stagger
// rolling-window subscription caps (e.g. chat message limits).
type FillFirstSelector struct{}

type blockReason int

const (
	blockReasonNone blockReason = iota
	blockReasonCooldown
	blockReasonDisabled
	blockReasonOther
)

type modelCooldownError struct {
	model    string
	resetIn  time.Duration
	provider string
	cause    error
}

// NewModelCooldownError creates an error representing model-level cooldown.
func NewModelCooldownError(model, provider string, resetIn time.Duration) error {
	return newModelCooldownErrorWithCause(model, provider, resetIn, nil)
}

func newModelCooldownError(model, provider string, resetIn time.Duration) *modelCooldownError {
	return newModelCooldownErrorWithCause(model, provider, resetIn, nil)
}

func newModelCooldownErrorWithCause(model, provider string, resetIn time.Duration, cause error) *modelCooldownError {
	if resetIn < 0 {
		resetIn = 0
	}
	return &modelCooldownError{
		model:    model,
		provider: provider,
		resetIn:  resetIn,
		cause:    cause,
	}
}

func (e *modelCooldownError) IsModelCooldown() bool {
	return true
}

func (e *modelCooldownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *modelCooldownError) Error() string {
	modelName := e.model
	if modelName == "" {
		modelName = "requested model"
	}
	message := fmt.Sprintf("All credentials for model %s are cooling down", modelName)
	if e.provider != "" {
		message = fmt.Sprintf("%s via provider %s", message, e.provider)
	}
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	displayDuration := e.resetIn
	if displayDuration > 0 && displayDuration < time.Second {
		displayDuration = time.Second
	} else {
		displayDuration = displayDuration.Round(time.Second)
	}
	errorBody := map[string]any{
		"code":          "model_cooldown",
		"message":       message,
		"model":         e.model,
		"reset_time":    displayDuration.String(),
		"reset_seconds": resetSeconds,
	}
	if e.provider != "" {
		errorBody["provider"] = e.provider
	}
	if e.cause != nil {
		if causeText := ExtractUpstreamErrorSummary(e.cause.Error()); causeText != "" {
			errorBody["last_upstream_error"] = causeText
			message += fmt.Sprintf(" (last error: %s)", causeText)
			errorBody["message"] = message
		}
	}
	payload := map[string]any{"error": errorBody}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"error":{"code":"model_cooldown","message":"%s"}}`, message)
	}
	return string(data)
}

var (
	sanitizerSchemeAuthPattern             = regexp.MustCompile(`(?i)((?:[A-Za-z0-9.+_\-]+:)?//)(?:[^:\s/@]+:[^@\s]+|[^@\s/]+)@`)
	sanitizerQueryParamPattern             = regexp.MustCompile(`(?i)([?&][A-Za-z0-9_.-]*(?:key|token|secret|password|auth|sig|signature)=)[^&\s,\r\n;]+`)
	sanitizerCookiePattern                 = regexp.MustCompile(`(?i)\b(?:set-)?cookie\s*:[^\r\n]+`)
	sanitizerAuthHeaderPattern             = regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*[^\r\n]+`)
	sanitizerNaturalSecretPattern          = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:api[ _-]?key|access[ _-]?token|client[ _-]?secret|private[ _-]?key|secret[ _-]?key|password|secret|token|credentials?|sessionid))\s*(?:(?:is|was|provided|used)?\s*[:= ]\s*|\s+is\s+|\s+was\s+|\s+provided\s+|\s+)(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|(?:[^\r\n;,|]+?(?:\s+(?:and|with|for|via)\s+|[,;|]|\r|\n|$)|[^\r\n;,|]+))`)
	sanitizerKVPattern                     = regexp.MustCompile(`(?i)((?:'|")?(?:[A-Za-z0-9_.-]*(?:key|token|secret|password|credential|credentials|bearer|sessionid|auth|signature|sig))(?:'|")?\s*[=:]\s*)(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|(?:[^\r\n;,|]+?(?:\s+(?:and|with|for|via)\s+|[,;|]|\r|\n|$)|[^\r\n;,|]+))`)
	sanitizerInvalidTokenPattern           = regexp.MustCompile(`(?i)\b(invalid|bad|expired|unknown)\s+(?:api\s+key|access\s+token|refresh\s+token|token|key|secret|password|credentials?|bearer)\s*(?:[:= ]\s*)?(?:"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\s,\r\n;]+)`)
	sanitizerSKKeyPattern                  = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9._~+/=-]{6,}|ghp_[A-Za-z0-9._~+/=-]{6,})\b`)
	sanitizerBearerPattern                 = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	sanitizerDoubleQuotedPathPattern       = regexp.MustCompile(`"/[^"\r\n]+"`)
	sanitizerSingleQuotedPathPattern       = regexp.MustCompile(`'/[^'\r\n]+'`)
	sanitizerBacktickQuotedPathPattern     = regexp.MustCompile("`/[^`\r\n]+`")
	sanitizerPathConnectorPattern          = regexp.MustCompile(`(?i)\s+(to|from|into|onto|for|via|with|and)\s+/`)
	sanitizerUnixPathBeforeColonPattern    = regexp.MustCompile(`(^|[\s\(\[\{<"';,=])(/(?:[^/\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/\s\r\n"',;?#()<>{}\[\]]+)*/)*[^/:\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/:\s\r\n"',;?#()<>{}\[\]]+)*):\s+[A-Za-z0-9]`)
	sanitizerUnixPathBeforeNextPathPattern = regexp.MustCompile(`(^|[\s\(\[\{<"';,=])(/(?:[^/\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/\s\r\n"',;?#()<>{}\[\]]+)*/)*[^/:\s\r\n"',;?#()<>{}\[\]]+(?::[^/\s\r\n"',;?#()<>{}\[\]]+|\s+[^/:\s\r\n"',;?#()<>{}\[\]]+)*)(\s+/)`)
	sanitizerUnixPathStandardPattern       = regexp.MustCompile(`(^|[\s\(\[\{<"';,=])(/(?:[^/\s\r\n"',;?#()<>{}\[\]]+(?:\s+[^/\s\r\n"',;?#()<>{}\[\]]+)*/)*[^/:\s\r\n"',;?#()<>{}\[\]]+(?::[^/:\s\r\n"',;?#()<>{}\[\]]+)?)`)
	sanitizerFileExtPathPattern            = regexp.MustCompile(`(^|[\s"'` + "`" + `(\[,;=])(/[^\s:\r\n"'` + "`" + `,;\])>]+(?:\s+[^\s:\r\n"'` + "`" + `,;\])>]+)*\.(?:json|yaml|yml|key|pem|txt|log|toml|conf|env|crt|cer))`)
	sanitizerWindowsPathPattern            = regexp.MustCompile(`(?i)\b[A-Za-z]:\\[^\r\n:,;'"<>]+`)
	sanitizerWindowsUNCPathPattern         = regexp.MustCompile(`\\\\[^\r\n:,;'"<>]+\\[^\r\n:,;'"<>]+`)
)

// ExtractUpstreamErrorSummary extracts and sanitizes a concise error summary from upstream error strings.
func ExtractUpstreamErrorSummary(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	jsonPart := raw
	if idx := strings.Index(raw, ": {"); idx != -1 && idx < 50 {
		jsonPart = strings.TrimSpace(raw[idx+2:])
	}
	if gjson.Valid(jsonPart) {
		parsed := gjson.Parse(jsonPart)
		var code, message string
		if errNode := parsed.Get("error"); errNode.Exists() {
			if errNode.IsObject() {
				code = strings.TrimSpace(errNode.Get("code").String())
				if code == "" {
					code = strings.TrimSpace(errNode.Get("type").String())
				}
				message = strings.TrimSpace(errNode.Get("message").String())
			} else if errNode.Type == gjson.String {
				message = strings.TrimSpace(errNode.String())
			}
		}
		if code == "" && message == "" {
			code = strings.TrimSpace(parsed.Get("code").String())
			if code == "" {
				code = strings.TrimSpace(parsed.Get("type").String())
			}
			message = strings.TrimSpace(parsed.Get("message").String())
		}
		var summary string
		if code != "" && message != "" {
			if strings.EqualFold(code, message) || strings.Contains(strings.ToLower(message), strings.ToLower(code)) {
				summary = message
			} else {
				summary = code + ": " + message
			}
		} else if message != "" {
			summary = message
		} else if code != "" {
			summary = code
		}
		if summary != "" {
			return SanitizeUpstreamErrorSummary(summary)
		}
	}
	return SanitizeUpstreamErrorSummary(raw)
}

func sanitizeUpstreamErrorSummaryNoTruncate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = sanitizerSchemeAuthPattern.ReplaceAllString(s, "${1}[REDACTED_AUTH]@")
	s = sanitizerQueryParamPattern.ReplaceAllString(s, "${1}[REDACTED]")
	s = sanitizerDoubleQuotedPathPattern.ReplaceAllString(s, `"[REDACTED_PATH]"`)
	s = sanitizerSingleQuotedPathPattern.ReplaceAllString(s, `'[REDACTED_PATH]'`)
	s = sanitizerBacktickQuotedPathPattern.ReplaceAllString(s, "`[REDACTED_PATH]`")
	s = sanitizerWindowsPathPattern.ReplaceAllString(s, "[REDACTED_PATH]")
	s = sanitizerWindowsUNCPathPattern.ReplaceAllString(s, "[REDACTED_PATH]")

	// Handle connector-separated paths like "copy /tmp/a TO /tmp/b: denied"
	if loc := sanitizerPathConnectorPattern.FindStringSubmatchIndex(s); loc != nil {
		firstPart := s[:loc[0]]
		connRaw := s[loc[0] : loc[1]-1]
		secondPart := "/" + s[loc[1]:]
		return sanitizeUpstreamErrorSummaryNoTruncate(firstPart) + connRaw + sanitizeUpstreamErrorSummaryNoTruncate(secondPart)
	}

	// Handle path before colon error delimiter, finding the first known error or first colon
	var colonIdx = -1
	knownErrorPrefixes := []string{
		"permission denied", "no such file", "file not found", "access denied",
		"operation not permitted", "denied", "read-only", "is a directory",
		"not a directory", "cannot find", "no space", "connection refused",
		"timeout", "failed", "error", "not supported", "invalid argument",
	}
	for _, errWord := range knownErrorPrefixes {
		target := ": " + errWord
		if idx := strings.Index(strings.ToLower(s), target); idx != -1 {
			if colonIdx == -1 || idx < colonIdx {
				colonIdx = idx
			}
		}
	}
	if colonIdx == -1 {
		colonIdx = strings.Index(s, ": ")
	}

	if colonIdx != -1 {
		prefix := s[:colonIdx]
		suffix := s[colonIdx:]
		slashIdx := -1
		for i := 0; i < len(prefix); i++ {
			if prefix[i] == '/' {
				if i > 0 && prefix[i-1] == '/' {
					continue
				}
				if i >= 6 && (strings.HasSuffix(prefix[:i], "http:/") || strings.HasSuffix(prefix[:i], "https:/") || strings.HasSuffix(prefix[:i], "://")) {
					continue
				}
				if i == 0 || prefix[i-1] == ' ' || prefix[i-1] == '\t' || prefix[i-1] == '(' || prefix[i-1] == '[' || prefix[i-1] == '{' || prefix[i-1] == '<' || prefix[i-1] == '"' || prefix[i-1] == '\'' || prefix[i-1] == '`' || prefix[i-1] == '=' {
					slashIdx = i
					break
				}
			}
		}
		if slashIdx != -1 {
			lead := prefix[:slashIdx]
			pathPart := prefix[slashIdx:]
			trailPunct := ""
			for len(pathPart) > 0 && (pathPart[len(pathPart)-1] == ')' || pathPart[len(pathPart)-1] == ']' || pathPart[len(pathPart)-1] == '}' || pathPart[len(pathPart)-1] == '>') {
				trailPunct = string(pathPart[len(pathPart)-1]) + trailPunct
				pathPart = pathPart[:len(pathPart)-1]
			}
			if strings.Contains(pathPart, " /") {
				segments := strings.Split(pathPart, " /")
				for j := range segments {
					segments[j] = "[REDACTED_PATH]"
				}
				pathPart = strings.Join(segments, " ")
			} else {
				pathPart = "[REDACTED_PATH]"
			}
			s = lead + pathPart + trailPunct + suffix
		}
	}

	for i := 0; i < 3; i++ {
		prev := s
		s = sanitizerUnixPathStandardPattern.ReplaceAllString(s, "${1}[REDACTED_PATH]")
		if s == prev {
			break
		}
	}
	s = sanitizerFileExtPathPattern.ReplaceAllString(s, "${1}[REDACTED_PATH]")
	s = sanitizerCookiePattern.ReplaceAllString(s, "Cookie: [REDACTED]")
	s = sanitizerAuthHeaderPattern.ReplaceAllString(s, "Authorization: [REDACTED]")
	s = sanitizerSKKeyPattern.ReplaceAllString(s, "sk-[REDACTED]")
	s = sanitizerBearerPattern.ReplaceAllString(s, "Bearer [REDACTED]")
	s = sanitizerInvalidTokenPattern.ReplaceAllString(s, `${1} token [REDACTED]`)
	s = sanitizerNaturalSecretPattern.ReplaceAllString(s, `${1}: [REDACTED]`)
	s = sanitizerKVPattern.ReplaceAllString(s, `${1}[REDACTED]`)
	return s
}

// SanitizeUpstreamErrorSummary removes sensitive credentials, tokens, and paths, and bounds length.
func SanitizeUpstreamErrorSummary(s string) string {
	s = sanitizeUpstreamErrorSummaryNoTruncate(s)
	runes := []rune(s)
	if len(runes) > 256 {
		if len(runes) > 253 {
			return string(runes[:253]) + "..."
		}
		return string(runes) + "..."
	}
	return s
}

func (e *modelCooldownError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e *modelCooldownError) Headers() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	resetSeconds := int(math.Ceil(e.resetIn.Seconds()))
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	headers.Set("Retry-After", strconv.Itoa(resetSeconds))
	return headers
}

func authPriority(auth *Auth) int {
	if auth == nil || auth.Attributes == nil {
		return 0
	}
	raw := strings.TrimSpace(auth.Attributes["priority"])
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return parsed
}

func authWeight(auth *Auth) int64 {
	if auth == nil {
		return credentialweight.Default
	}
	if rawWeight, ok := auth.Attributes[AttributeWeight]; ok && strings.TrimSpace(rawWeight) != "" {
		weight, errParse := credentialweight.ParseString(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	if rawWeight, ok := auth.Metadata[AttributeWeight]; ok {
		weight, errParse := credentialweight.ParseValue(rawWeight)
		if errParse != nil {
			return 0
		}
		return weight
	}
	return credentialweight.Default
}

func canonicalModelKey(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(model)
	modelName := strings.TrimSpace(parsed.ModelName)
	if modelName == "" {
		return model
	}
	return modelName
}

func authWebsocketsEnabled(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if len(auth.Attributes) > 0 {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(auth.Metadata) == 0 {
		return false
	}
	raw, ok := auth.Metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func preferCodexWebsocketAuths(ctx context.Context, provider string, available []*Auth) []*Auth {
	if len(available) == 0 {
		return available
	}
	if !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return available
	}
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return available
	}

	wsEnabled := make([]*Auth, 0, len(available))
	for i := 0; i < len(available); i++ {
		candidate := available[i]
		if authWebsocketsEnabled(candidate) {
			wsEnabled = append(wsEnabled, candidate)
		}
	}
	if len(wsEnabled) > 0 {
		return wsEnabled
	}
	return available
}

func collectAvailableByPriority(auths []*Auth, model string, now time.Time) (available map[int][]*Auth, cooldownCount int, earliest time.Time) {
	available = make(map[int][]*Auth)
	for i := 0; i < len(auths); i++ {
		candidate := auths[i]
		blocked, reason, next := isAuthBlockedForModel(candidate, model, now)
		if !blocked {
			priority := authPriority(candidate)
			available[priority] = append(available[priority], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
			if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}
	return available, cooldownCount, earliest
}

func getAvailableAuths(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, false)
}

type prevalidatedAuthCandidatesKey struct{}

func getSelectorAvailableAuths(ctx context.Context, auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getSelectorAvailableAuthsWithPriorityMode(ctx, auths, provider, model, now, false)
}

func getSelectorAvailableAuthsAcrossPriorities(ctx context.Context, auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getSelectorAvailableAuthsWithPriorityMode(ctx, auths, provider, model, now, true)
}

func getSelectorAvailableAuthsWithPriorityMode(ctx context.Context, auths []*Auth, provider, model string, now time.Time, allPriorities bool) ([]*Auth, error) {
	if ctx != nil {
		if validated, _ := ctx.Value(prevalidatedAuthCandidatesKey{}).(bool); validated && len(auths) > 0 {
			// The manager already resolved each credential's upstream model and supplied
			// ID-sorted candidates. Rechecking the alias or an empty model would apply
			// unrelated cooldowns. Affinity bindings may span all priority tiers, but
			// fallback selection must still use the highest available tier.
			if !allPriorities {
				return highestPriorityAuths(auths), nil
			}
			return auths, nil
		}
	}
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, allPriorities)
}

func getAvailableAuthsAcrossPriorities(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	return getAvailableAuthsWithPriorityMode(auths, provider, model, now, true)
}

func getAvailableAuthsWithPriorityMode(auths []*Auth, provider, model string, now time.Time, allPriorities bool) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority, cooldownCount, earliest := collectAvailableByPriority(auths, model, now)
	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(model, providerForError, resetIn)
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	return availableAuthsFromPriorityBuckets(availableByPriority, allPriorities), nil
}

// availableAuthsFromPriorityBuckets flattens availability buckets into a stable, ID-sorted slice.
// When allPriorities is false only the highest available priority tier is returned.
// When allPriorities is true every tier is merged, so the result carries no priority ordering:
// use it for membership checks or feed it to highestPriorityAuths, never as a priority-ordered
// selection order.
func availableAuthsFromPriorityBuckets(availableByPriority map[int][]*Auth, allPriorities bool) []*Auth {
	var candidates []*Auth
	if allPriorities {
		total := 0
		for _, bucket := range availableByPriority {
			total += len(bucket)
		}
		candidates = make([]*Auth, 0, total)
		for _, bucket := range availableByPriority {
			candidates = append(candidates, bucket...)
		}
	} else {
		bestPriority := 0
		found := false
		for priority := range availableByPriority {
			if !found || priority > bestPriority {
				bestPriority = priority
				found = true
			}
		}
		bucket := availableByPriority[bestPriority]
		candidates = make([]*Auth, 0, len(bucket))
		candidates = append(candidates, bucket...)
	}
	if len(candidates) > 1 {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	}
	return candidates
}

// highestPriorityAuths narrows an availability slice to its highest priority tier while
// preserving the input order. The input slice is returned unchanged when every candidate
// already shares the highest priority, so the common single-tier case allocates nothing.
func highestPriorityAuths(auths []*Auth) []*Auth {
	if len(auths) <= 1 {
		return auths
	}
	bestPriority := 0
	bestCount := 0
	for _, auth := range auths {
		priority := authPriority(auth)
		switch {
		case bestCount == 0 || priority > bestPriority:
			bestPriority = priority
			bestCount = 1
		case priority == bestPriority:
			bestCount++
		}
	}
	if bestCount == len(auths) {
		return auths
	}
	highest := make([]*Auth, 0, bestCount)
	for _, auth := range auths {
		if authPriority(auth) == bestPriority {
			highest = append(highest, auth)
		}
	}
	return highest
}

// Pick selects the next available auth for the provider in a round-robin manner.
func (s *RoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	key := provider + ":" + canonicalModelKey(model)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastPicked == nil {
		s.lastPicked = make(map[string]string)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}

	s.ensureRotationKey(key, limit)
	picked := available[successorIndex(available, s.lastPicked[key])]
	s.lastPicked[key] = picked.ID
	return picked, nil
}

// successorIndex returns the index of the first candidate ordered after lastID, wrapping to
// the start of the ring. Candidates arrive sorted by ID, so this resumes the rotation at the
// credential that follows the previous pick even when candidates were filtered out in
// between. An empty lastID starts at the head.
func successorIndex(available []*Auth, lastID string) int {
	if lastID == "" {
		return 0
	}
	index := sort.Search(len(available), func(i int) bool { return available[i].ID > lastID })
	if index >= len(available) {
		return 0
	}
	return index
}

// ensureRotationKey ensures the rotation map has capacity for the given key.
// Must be called with s.mu held.
func (s *RoundRobinSelector) ensureRotationKey(key string, limit int) {
	if _, ok := s.lastPicked[key]; !ok && len(s.lastPicked) >= limit {
		s.lastPicked = make(map[string]string)
	}
}

func positiveWeightAuths(auths []*Auth) []*Auth {
	weightedCandidates := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if authWeight(auth) > 0 {
			weightedCandidates = append(weightedCandidates, auth)
		}
	}
	return weightedCandidates
}

// Pick selects the next available auth using smooth weighted round-robin.
func (s *WeightedRoundRobinSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	available, errAvailable := getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, time.Now())
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	stateModel := weightedSelectorStateModel(ctx, model)
	key := provider + ":" + canonicalModelKey(stateModel)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]*smoothWeightedState)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.states[key]; !ok && len(s.states) >= limit {
		s.states = make(map[string]*smoothWeightedState)
	}
	state := s.states[key]
	if state == nil {
		state = &smoothWeightedState{}
		s.states[key] = state
	}
	weights := authWeightVector(available)
	state.prepare(weights)
	picked := pickSmoothWeightedAuth(available, state.current)
	if picked == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
	}
	return picked, nil
}

// maxSmoothWeightedStateEntries bounds a single accumulator map so credentials that are
// removed permanently cannot leak entries. Real pools stay far below this bound, so the
// transient subsets produced by retry exclusions and cooldowns are never pruned.
const maxSmoothWeightedStateEntries = 1024

// prepare syncs the configured weights into the state without discarding accumulated
// credits. Credits are reset only when a credential's configured weight actually changes,
// never when the candidate set shrinks temporarily (retry exclusions, cooldowns, session
// affinity), because discarding credits there would collapse selection onto the first
// candidate in slice order.
func (s *smoothWeightedState) prepare(weights map[string]int64) {
	if s.current == nil || weightsConfigChanged(s.weights, weights) {
		s.current = make(map[string]int64, len(weights))
	}
	if s.weights == nil {
		s.weights = make(map[string]int64, len(weights))
	}
	for authID, weight := range weights {
		s.weights[authID] = weight
	}
	s.pruneStale(weights)
}

// pruneStale drops entries for credentials outside the current candidate set, but only
// once a map exceeds the safety bound, so ordinary transient exclusions keep their credits.
func (s *smoothWeightedState) pruneStale(weights map[string]int64) {
	if len(s.current) <= maxSmoothWeightedStateEntries && len(s.weights) <= maxSmoothWeightedStateEntries {
		return
	}
	for authID := range s.current {
		if _, ok := weights[authID]; !ok {
			delete(s.current, authID)
		}
	}
	for authID := range s.weights {
		if _, ok := weights[authID]; !ok {
			delete(s.weights, authID)
		}
	}
}

// weightsConfigChanged reports whether any credential present in both vectors has a
// different configured weight. Credentials that are merely missing from one side are
// ignored, since a candidate subset is not a configuration change.
func weightsConfigChanged(left, right map[string]int64) bool {
	if len(left) == 0 {
		return false
	}
	for authID, weight := range right {
		if previous, ok := left[authID]; ok && previous != weight {
			return true
		}
	}
	return false
}

func authWeightVector(auths []*Auth) map[string]int64 {
	weights := make(map[string]int64, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if weight := authWeight(auth); weight > 0 {
			weights[auth.ID] = weight
		}
	}
	return weights
}

func pickSmoothWeightedAuth(auths []*Auth, current map[string]int64) *Auth {
	var picked *Auth
	var pickedCurrent int64
	var totalWeight int64
	for _, auth := range auths {
		weight := authWeight(auth)
		if auth == nil || weight <= 0 {
			continue
		}
		current[auth.ID] = saturatingAddInt64(current[auth.ID], weight)
		totalWeight = saturatingAddInt64(totalWeight, weight)
		if picked == nil || current[auth.ID] > pickedCurrent {
			picked = auth
			pickedCurrent = current[auth.ID]
		}
	}
	if picked == nil {
		return nil
	}
	current[picked.ID] = saturatingAddInt64(current[picked.ID], -totalWeight)
	return picked
}

func saturatingAddInt64(value, delta int64) int64 {
	if delta > 0 && value > math.MaxInt64-delta {
		return math.MaxInt64
	}
	if delta < 0 && value < math.MinInt64-delta {
		return math.MinInt64
	}
	return value + delta
}

// Pick selects the first available auth for the provider in a deterministic manner.
func (s *FillFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return available[0], nil
}

func isAuthBlockedForModel(auth *Auth, model string, now time.Time) (bool, blockReason, time.Time) {
	if auth == nil {
		return true, blockReasonOther, time.Time{}
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		return true, blockReasonDisabled, time.Time{}
	}
	if exp, ok := auth.AccessTokenExpirationTime(); ok && !exp.IsZero() && !exp.After(now) {
		return true, blockReasonOther, time.Time{}
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		return true, blockReasonCooldown, auth.Quota.NextRecoverAt
	}
	if model != "" {
		if len(auth.ModelStates) > 0 {
			modelKey := canonicalModelKey(model)
			matched := false
			blocked := false
			blockedReason := blockReasonNone
			nextRetry := time.Time{}
			for stateModel, state := range auth.ModelStates {
				if state == nil || canonicalModelKey(stateModel) != modelKey {
					continue
				}
				matched = true
				if state.Status == StatusDisabled {
					return true, blockReasonDisabled, time.Time{}
				}
				stateBlocked, reason, next := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
				if !stateBlocked {
					continue
				}
				if next.IsZero() {
					return true, reason, time.Time{}
				}
				if !blocked || next.After(nextRetry) || (next.Equal(nextRetry) && reason == blockReasonCooldown) {
					blocked = true
					blockedReason = reason
					nextRetry = next
				}
			}
			if matched {
				return blocked, blockedReason, nextRetry
			}
			return false, blockReasonNone, time.Time{}
		}
		return availabilityBlock(auth.Unavailable, auth.Quota.Exceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
	}
	quotaExceeded := auth.Quota.Exceeded
	// When model is empty and the credential has individual model states, auth.Quota.Exceeded
	// is an aggregate of single-model quota cooldowns (reason "quota"). As long as the credential
	// itself is not unavailable (not all models failed) and not under a credential-wide quota,
	// do not treat individual model cooldowns as blocking the entire credential.
	if len(auth.ModelStates) > 0 && auth.Quota.Reason != "credential_quota" && !auth.Unavailable {
		quotaExceeded = false
	}
	return availabilityBlock(auth.Unavailable, quotaExceeded, auth.NextRetryAfter, auth.Quota.NextRecoverAt, now)
}

func availabilityBlock(unavailable, quotaExceeded bool, nextRetryAfter, nextRecoverAt, now time.Time) (bool, blockReason, time.Time) {
	if !unavailable && !quotaExceeded {
		return false, blockReasonNone, time.Time{}
	}

	hasRecoveryTime := !nextRetryAfter.IsZero() || !nextRecoverAt.IsZero()
	var next time.Time
	for _, candidate := range []time.Time{nextRetryAfter, nextRecoverAt} {
		if candidate.After(now) && (next.IsZero() || candidate.After(next)) {
			next = candidate
		}
	}
	if !next.IsZero() {
		if quotaExceeded {
			return true, blockReasonCooldown, next
		}
		return true, blockReasonOther, next
	}
	if hasRecoveryTime {
		return false, blockReasonNone, time.Time{}
	}
	return true, blockReasonOther, time.Time{}
}

// SessionAffinitySelector wraps another selector with session-sticky behavior.
// It extracts session ID from multiple sources and maintains session-to-auth
// mappings with automatic failover when the bound auth becomes unavailable.
type SessionAffinitySelector struct {
	fallback         Selector
	cache            *SessionCache
	matcher          *cliproxysession.MerklePrefixMatcher
	subagentAffinity bool
}

// SessionAffinityConfig configures the session affinity selector.
type SessionAffinityConfig struct {
	Fallback         Selector
	TTL              time.Duration
	SubagentAffinity *bool
}

// NewSessionAffinitySelector creates a new session-aware selector.
func NewSessionAffinitySelector(fallback Selector) *SessionAffinitySelector {
	return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Hour,
	})
}

// NewSessionAffinitySelectorWithConfig creates a selector with custom configuration.
func NewSessionAffinitySelectorWithConfig(cfg SessionAffinityConfig) *SessionAffinitySelector {
	if cfg.Fallback == nil {
		cfg.Fallback = &RoundRobinSelector{}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	subagentAffinity := true
	if cfg.SubagentAffinity != nil {
		subagentAffinity = *cfg.SubagentAffinity
	}
	return &SessionAffinitySelector{
		fallback:         cfg.Fallback,
		cache:            NewSessionCache(cfg.TTL),
		matcher:          cliproxysession.NewMerklePrefixMatcher(cfg.TTL),
		subagentAffinity: subagentAffinity,
	}
}

// Trees returns a backward-compatible in-memory session tree store.
// Deprecated: Session tree management has moved to Home.
func (s *SessionAffinitySelector) Trees() *cliproxysession.InMemorySessionTreeStore {
	return cliproxysession.NewInMemorySessionTreeStore(0, time.Hour)
}

// Pick selects an auth with session affinity when possible.
// Explicit Claude Code, Codex, OpenCode, pi, and request-body session signals
// are absolute authority. Requests without those signals use the Merkle LCP
// matcher before retaining the legacy derived/hash fallback behavior.
//
// An established binding outranks credential priority: a bound credential that is still
// available is reused even when a higher-priority credential recovers. Credential priority
// applies to cold bindings, requests without a session, and genuine bound-credential
// failover, so the fallback selector only ever receives the highest available priority tier.
//
// Note: The cache key includes provider, session ID, and model to handle cases where
// a session uses multiple models (e.g., gemini-2.5-pro and gemini-3-flash-preview)
// that may be supported by different auth credentials, and to avoid cross-provider conflicts.
func (s *SessionAffinitySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	entry := selectorLogEntry(ctx)
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any)
	}
	opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey] = provider
	opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = model

	// Explicit harness identities are absolute authority. The LCP matcher is only
	// consulted when no header, body, or execution-session identity is present.
	explicitID, explicitFallbackID := extractExplicitSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if explicitID == "" {
		if auth, handled, errLCP := s.pickLCP(ctx, provider, model, opts, auths, entry); handled || errLCP != nil {
			return auth, errLCP
		}
	} else if opts.Metadata != nil {
		delete(opts.Metadata, cliproxyexecutor.LCPAffinitySessionIDMetadataKey)
		delete(opts.Metadata, cliproxyexecutor.LCPAccessGenerationMetadataKey)
		if explicitFallbackID != "" {
			opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = cliproxysession.BoundSessionIdentity(explicitFallbackID)
		} else {
			delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		if isFork, ok := opts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); !ok || !isFork {
			delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
		}
	}

	primaryID, fallbackID := explicitID, explicitFallbackID
	if primaryID == "" {
		primaryID, fallbackID = extractSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	}
	if primaryID != "" {
		primaryID = cliproxysession.BoundSessionIdentity(primaryID)
		if fallbackID != "" {
			fallbackID = cliproxysession.BoundSessionIdentity(fallbackID)
		}
		if opts.Metadata != nil {
			opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = primaryID
		}
	}
	now := time.Now()
	availabilityCandidates := auths
	if _, weighted := s.fallback.(*WeightedRoundRobinSelector); weighted {
		availabilityCandidates = positiveWeightAuths(auths)
	}
	if primaryID == "" {
		fallbackAuths, errAvailable := getSelectorAvailableAuths(ctx, availabilityCandidates, provider, model, now)
		if errAvailable != nil {
			return nil, errAvailable
		}
		entry.Debugf("session-affinity: no session ID extracted, falling back to default selector | provider=%s model=%s", provider, model)
		return s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	}

	// A single availability pass serves both lookups: the bound credential is validated against
	// every priority tier, while the fallback selector keeps seeing only the highest tier.
	available, err := getSelectorAvailableAuthsAcrossPriorities(ctx, availabilityCandidates, provider, model, now)
	if err != nil {
		return nil, err
	}
	fallbackAuths := highestPriorityAuths(available)

	modelKey := canonicalModelKey(model)
	cacheKey := provider + "::" + primaryID + "::" + modelKey
	isFork := false
	if opts.Metadata != nil {
		if forkFlag, ok := opts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); ok && forkFlag {
			isFork = true
		}
	}
	isSubagent := !isFork && isSubagentSession(primaryID, fallbackID)
	fallbackKey := ""
	if fallbackID != "" && fallbackID != primaryID {
		fallbackKey = provider + "::" + fallbackID + "::" + modelKey
	}
	bind := func(authID string) {
		if fallbackKey != "" && !isSubagent && !isFork {
			s.cache.SetAliases(authID, cacheKey, fallbackKey)
		} else {
			s.cache.Set(cacheKey, authID)
		}
	}

	if cachedAuthID, ok := s.cache.GetAndRefresh(cacheKey); ok {
		for _, auth := range available {
			if auth.ID == cachedAuthID {
				bind(auth.ID)
				entry.Infof("session-affinity: cache hit | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
				return auth, nil
			}
		}
		// Cached auth not available, reselect via fallback selector for even distribution
		auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
		if err != nil {
			return nil, err
		}
		if auth == nil {
			return nil, nil
		}
		bind(auth.ID)
		entry.Infof("session-affinity: cache hit but auth unavailable, reselected | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
		return auth, nil
	}

	if fallbackKey != "" {
		if cachedAuthID, ok := s.cache.Get(fallbackKey); ok {
			for _, auth := range available {
				if auth.ID == cachedAuthID {
					if !isSubagent || s.subagentAffinity {
						bind(auth.ID)
						if isFork {
							entry.Infof("session-affinity: fork cache hit | session=%s parent=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
						} else {
							entry.Infof("session-affinity: fallback cache hit | session=%s fallback=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
						}
						return auth, nil
					}
				}
			}
		}
	}

	auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, nil
	}
	bind(auth.ID)
	if isFork && fallbackID != "" {
		entry.Infof("session-affinity: fork bound to new auth | session=%s parent=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), truncateSessionID(fallbackID), auth.ID, provider, model)
	} else {
		entry.Infof("session-affinity: cache miss, new binding | session=%s auth=%s provider=%s model=%s", truncateSessionID(primaryID), auth.ID, provider, model)
	}
	return auth, nil
}

func (s *SessionAffinitySelector) pickLCP(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth, entry *log.Entry) (*Auth, bool, error) {
	if s == nil || s.matcher == nil {
		return nil, false, nil
	}
	namespace := lcpAffinityNamespace(provider, model, opts.Metadata)
	if namespace == "" {
		return nil, false, nil
	}
	turns := cliproxysession.ExtractCanonicalTurns(opts.SourceFormat, opts.OriginalRequest)
	if len(turns) == 0 {
		return nil, false, nil
	}
	fingerprints, minPrefixLength := s.matcher.Prepare(turns)
	if len(fingerprints) == 0 || minPrefixLength <= 0 || minPrefixLength > len(fingerprints) {
		return nil, false, nil
	}
	if opts.Metadata != nil {
		opts.Metadata[cliproxyexecutor.LCPFingerprintMetadataKey] = fingerprints
		opts.Metadata[cliproxyexecutor.LCPMinPrefixLengthMetadataKey] = minPrefixLength
	}

	availabilityCandidates := auths
	if _, weighted := s.fallback.(*WeightedRoundRobinSelector); weighted {
		availabilityCandidates = positiveWeightAuths(auths)
	}
	available, errAvailable := getSelectorAvailableAuthsAcrossPriorities(ctx, availabilityCandidates, provider, model, time.Now())
	if errAvailable != nil {
		return nil, true, errAvailable
	}

	if match, ok := s.matcher.MatchFingerprints(namespace, fingerprints, minPrefixLength); ok {
		for _, auth := range available {
			if auth == nil || auth.ID != match.AuthID {
				continue
			}
			if match.SessionID != "" {
				opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] = match.SessionID
				opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = match.SessionID
			}
			if match.ParentSessionID != "" {
				opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = match.ParentSessionID
			} else if opts.Metadata != nil {
				delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
			}
			if match.AccessNumber > 0 && opts.Metadata != nil {
				opts.Metadata[cliproxyexecutor.LCPAccessGenerationMetadataKey] = match.AccessNumber
			}
			if match.IsFork {
				if opts.Metadata != nil {
					opts.Metadata[cliproxyexecutor.IsForkMetadataKey] = true
				}
				entry.Infof("session-affinity: LCP fork hit | session=%s parent=%s prefix=%d auth=%s provider=%s model=%s", truncateSessionID(match.SessionID), truncateSessionID(match.ParentSessionID), match.PrefixLength, auth.ID, provider, model)
			} else {
				if opts.Metadata != nil {
					delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
				}
				entry.Infof("session-affinity: LCP cache hit | session=%s prefix=%d auth=%s provider=%s model=%s", truncateSessionID(match.SessionID), match.PrefixLength, auth.ID, provider, model)
			}
			return auth, true, nil
		}
	}

	fallbackAuths := highestPriorityAuths(available)
	auth, errPick := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
	if errPick != nil {
		return nil, true, errPick
	}
	if auth == nil {
		return nil, true, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	if bindRes := s.matcher.BindFingerprintsWithResult(namespace, fingerprints, minPrefixLength, auth.ID); bindRes.SessionID != "" {
		opts.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey] = bindRes.SessionID
		opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = bindRes.SessionID
		if bindRes.ParentSessionID != "" {
			opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = bindRes.ParentSessionID
		} else if opts.Metadata != nil {
			delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
		if bindRes.AccessNumber > 0 && opts.Metadata != nil {
			opts.Metadata[cliproxyexecutor.LCPAccessGenerationMetadataKey] = bindRes.AccessNumber
		}
		if bindRes.IsFork {
			if opts.Metadata != nil {
				opts.Metadata[cliproxyexecutor.IsForkMetadataKey] = true
			}
			entry.Infof("session-affinity: LCP fork bound to new auth | session=%s parent=%s auth=%s provider=%s model=%s", truncateSessionID(bindRes.SessionID), truncateSessionID(bindRes.ParentSessionID), auth.ID, provider, model)
		} else {
			if opts.Metadata != nil {
				delete(opts.Metadata, cliproxyexecutor.IsForkMetadataKey)
			}
			entry.Infof("session-affinity: LCP cache miss, new binding | session=%s auth=%s provider=%s model=%s", truncateSessionID(bindRes.SessionID), auth.ID, provider, model)
		}
	}
	return auth, true, nil
}

func lcpAffinityNamespace(provider, model string, metadata map[string]any) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = canonicalModelKey(model)
	callerScope := sessionMetadataString(metadata, cliproxyexecutor.CallerScopeMetadataKey)
	if provider == "" || callerScope == "" {
		return ""
	}
	return strings.Join([]string{"lcp:v1", provider, model, callerScope}, "::")
}

func lcpFingerprintsFromMetadata(metadata map[string]any) ([]string, int) {
	if metadata == nil {
		return nil, 0
	}
	rawFingerprints, ok := metadata[cliproxyexecutor.LCPFingerprintMetadataKey]
	if !ok || rawFingerprints == nil {
		return nil, 0
	}
	var fingerprints []string
	switch v := rawFingerprints.(type) {
	case []string:
		fingerprints = v
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				fingerprints = append(fingerprints, s)
			}
		}
	}
	minPrefixLength, _ := metadata[cliproxyexecutor.LCPMinPrefixLengthMetadataKey].(int)
	return fingerprints, minPrefixLength
}

func sessionMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func selectorLogEntry(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

// truncateSessionID shortens session ID for logging (first 8 chars + "...")
func truncateSessionID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:8] + "..."
}

// Stop releases resources held by the selector.
func (s *SessionAffinitySelector) Stop() {
	if s == nil {
		return
	}
	if s.cache != nil {
		s.cache.Stop()
	}
	if s.matcher != nil {
		s.matcher.Clear()
	}
}

// InvalidateAuth removes all session bindings for a specific auth.
// Called when an auth becomes rate-limited or unavailable.
func (s *SessionAffinitySelector) InvalidateAuth(authID string) {
	if s == nil {
		return
	}
	if s.cache != nil {
		s.cache.InvalidateAuth(authID)
	}
	if s.matcher != nil {
		s.matcher.InvalidateAuth(authID)
	}
}

// OnResult handles session affinity binding or release based on execution outcome.
func (s *SessionAffinitySelector) OnResult(res Result) {
	if s == nil || res.AuthID == "" {
		return
	}

	explicitID, explicitFallbackID := extractExplicitSessionIDs(res.Options.Headers, res.Options.OriginalRequest, res.Options.Metadata)
	if explicitID != "" {
		explicitID = cliproxysession.BoundSessionIdentity(explicitID)
		if explicitFallbackID != "" {
			explicitFallbackID = cliproxysession.BoundSessionIdentity(explicitFallbackID)
		}
	}
	ns := res.Provider
	if raw, ok := res.Options.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey].(string); ok && raw != "" {
		ns = raw
	}
	nsModel := canonicalModelKey(res.Model)
	if raw, ok := res.Options.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey].(string); ok && raw != "" {
		nsModel = canonicalModelKey(raw)
	}

	if res.Error != nil && shouldSkipCredentialCooldown(res.Error) {
		// Request-scoped or caller-attributed failures are not evidence that the
		// selected credential is unhealthy, so preserve both explicit and LCP bindings.
		return
	}

	// LCP bindings are independent from explicit harness bindings. A successful
	// extension is recorded as a new sequence while credential-attributed failures
	// only remove the exact sequence that was attempted.
	if explicitID == "" && s.matcher != nil {
		if namespace := lcpAffinityNamespace(ns, nsModel, res.Options.Metadata); namespace != "" {
			fingerprints, minPrefixLength := lcpFingerprintsFromMetadata(res.Options.Metadata)
			if len(fingerprints) == 0 {
				turns := cliproxysession.ExtractCanonicalTurns(res.Options.SourceFormat, res.Options.OriginalRequest)
				fingerprints, minPrefixLength = s.matcher.Prepare(turns)
			}
			if len(fingerprints) > 0 && minPrefixLength > 0 && minPrefixLength <= len(fingerprints) {
				if res.Success {
					s.matcher.TouchFingerprints(namespace, fingerprints, minPrefixLength, res.AuthID)
				} else {
					var generation uint64
					if res.Options.Metadata != nil {
						if gen, ok := res.Options.Metadata[cliproxyexecutor.LCPAccessGenerationMetadataKey].(uint64); ok {
							generation = gen
						}
					}
					s.matcher.RemoveFingerprintsBefore(namespace, fingerprints, res.AuthID, generation)
				}
			}
		}
	}

	if s.cache == nil {
		return
	}
	if explicitID == "" && s.matcher != nil && res.Options.Metadata != nil {
		if _, isLCP := res.Options.Metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey]; isLCP {
			return
		}
	}
	primaryID, fallbackID := explicitID, explicitFallbackID
	if primaryID == "" {
		primaryID, fallbackID = extractSessionIDs(res.Options.Headers, res.Options.OriginalRequest, res.Options.Metadata)
	}
	if primaryID == "" && fallbackID == "" {
		return
	}
	if primaryID != "" {
		primaryID = cliproxysession.BoundSessionIdentity(primaryID)
	}
	if fallbackID != "" {
		fallbackID = cliproxysession.BoundSessionIdentity(fallbackID)
	}

	cacheKey := ns + "::" + primaryID + "::" + nsModel
	var fallbackKey string
	if fallbackID != "" && fallbackID != primaryID && !isSubagentSession(primaryID, fallbackID) {
		fallbackKey = ns + "::" + fallbackID + "::" + nsModel
	}
	if res.Success {
		s.cache.Touch(cacheKey, res.AuthID)
		if fallbackKey != "" {
			s.cache.Touch(fallbackKey, res.AuthID)
		}
		return
	}

	s.cache.CompareAndDelete(cacheKey, res.AuthID)
	if fallbackKey != "" {
		s.cache.CompareAndDelete(fallbackKey, res.AuthID)
	}
}

func isBodyForkCandidate(root, reqRoot gjson.Result, hasNestedReq bool) bool {
	if !root.Exists() {
		return false
	}
	for _, k := range []string{
		"forked_from_thread_id", "forked_from_id",
		"metadata.forked_from_thread_id", "metadata.forked_from_id",
		"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
	} {
		if val := normalizedSessionCandidate(root.Get(k).String()); val != "" {
			return true
		}
		if hasNestedReq {
			if val := normalizedSessionCandidate(reqRoot.Get(k).String()); val != "" {
				return true
			}
		}
	}
	return false
}

// normalizedSessionCandidate validates an explicit client-provided session signal.
// It keeps opaque printable IDs intact while rejecting values that are unsafe or
// implausibly large for routing keys and logs.
func normalizedSessionCandidate(raw string) string {
	return cliproxysession.NormalizeExplicitID(raw)
}

func isSubagentSession(primaryID, fallbackID string) bool {
	if strings.Contains(primaryID, ":agent:") {
		return true
	}
	if fallbackID == "" || primaryID == "" || primaryID == fallbackID {
		return false
	}
	return isHierarchyParent(primaryID, fallbackID)
}

func sessionHeaderValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := normalizedSessionCandidate(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, raw := range values {
			if value := normalizedSessionCandidate(raw); value != "" {
				return value
			}
		}
	}
	return ""
}

// CanonicalSessionID resolves the single authoritative session identity from request options and metadata.
func CanonicalSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	if explicitID, _ := extractExplicitSessionIDs(headers, payload, metadata); explicitID != "" {
		return cliproxysession.BoundSessionIdentity(explicitID)
	}
	if metadata != nil {
		if canonicalID, ok := metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string); ok && strings.TrimSpace(canonicalID) != "" {
			return cliproxysession.BoundSessionIdentity(strings.TrimSpace(canonicalID))
		}
		if lcpID, ok := metadata[cliproxyexecutor.LCPAffinitySessionIDMetadataKey].(string); ok && strings.TrimSpace(lcpID) != "" {
			return cliproxysession.BoundSessionIdentity(strings.TrimSpace(lcpID))
		}
	}
	return cliproxysession.BoundSessionIdentity(ExtractSessionID(headers, payload, metadata))
}

// ExtractSessionID extracts a session identifier from explicit client signals,
// then falls back to execution metadata, derived identity, and message history.
// Priority order:
//  1. X-Claude-Code-Session-Id
//  2. Claude Code metadata.user_id session
//  3. Session-Id / Session_id (Codex and compatible clients)
//  4. X-Session-ID
//  5. X-Session-Affinity (OpenCode)
//  6. X-Client-Request-Id (pi Responses)
//  7. session_id / sessionId
//  8. prompt_cache_key, with conversation / conversation.id as an alias
//  9. metadata.user_id and conversation_id legacy body fields
//  10. explicit execution session metadata
//  11. stable context-derived session identity
//  12. stable hash from initial message content
func ExtractSessionID(headers http.Header, payload []byte, metadata map[string]any) string {
	primary, _ := extractSessionIDs(headers, payload, metadata)
	return primary
}

// extractExplicitSessionIDs returns only client- or execution-provided identities.
// LCP fallback must run after this function so explicit harness sessions remain authoritative.
func extractExplicitSessionIDs(headers http.Header, payload []byte, metadata map[string]any) (string, string) {
	var primary, fallback string

	// Extract parent candidate from payload once if payload is non-empty
	var root gjson.Result
	var reqRoot gjson.Result
	var hasNestedReq bool
	var parentIDCandidate string
	if len(payload) > 0 {
		root = util.ParseGJSONBytesNoCopy(payload)
		reqRoot = root
		req := root.Get("request")
		hasNestedReq = req.Exists() && !root.Get("contents").Exists()
		if hasNestedReq {
			reqRoot = req
		}
		for _, parentPath := range []string{
			"parent_session_id", "parentSessionId",
			"parent_thread_id", "parentThreadId",
			"forked_from_thread_id", "forked_from_id",
			"parent_conversation_id", "parentConversationId",
			"metadata.parent_session_id", "metadata.parent_thread_id",
			"metadata.forked_from_thread_id", "metadata.forked_from_id",
			"extra_body.parent_session_id", "extra_body.parent_thread_id",
			"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
		} {
			if psid := normalizedSessionCandidate(root.Get(parentPath).String()); psid != "" {
				parentIDCandidate = psid
				break
			}
			if hasNestedReq {
				if psid := normalizedSessionCandidate(reqRoot.Get(parentPath).String()); psid != "" {
					parentIDCandidate = psid
					break
				}
			}
		}
		if parentIDCandidate == "" {
			parentIDCandidate = cliproxysession.ClaudeMetadataParentSessionID(payload)
		}
	}

	// 1. Anthropic / Claude Code
	if sid := sessionHeaderValue(headers, "X-Claude-Code-Session-Id"); sid != "" {
		agentID := sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
		if agentID == "" && root.Exists() {
			agentID = normalizedSessionCandidate(root.Get("metadata.agent_id").String())
			if agentID == "" {
				agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
			}
			if agentID == "" && hasNestedReq {
				agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
				if agentID == "" {
					agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
				}
			}
		}
		if agentID == "" {
			_, _, agentID = cliproxysession.ClaudeMetadataIdentities(payload)
		}
		parentAgentID := sessionHeaderValue(headers, "X-Claude-Code-Parent-Agent-Id")
		if agentID != "" && agentID != "main" {
			primary = "claude:" + sid + ":agent:" + agentID
			fallback = "claude:" + sid
			if parentAgentID != "" && parentAgentID != "main" && parentAgentID != agentID {
				fallback = "claude:" + sid + ":agent:" + parentAgentID
			} else if parentIDCandidate != "" && parentIDCandidate != sid {
				fallback = "claude:" + parentIDCandidate
			}
			return primary, fallback
		}
		primary = "claude:" + sid
		if parentIDCandidate != "" && parentIDCandidate != sid {
			fallback = "claude:" + parentIDCandidate
		}
		return primary, fallback
	}
	if sid, parentSID, agentID := cliproxysession.ClaudeMetadataIdentities(payload); sid != "" {
		if agentID == "" {
			agentID = sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
		}
		if agentID == "" && root.Exists() {
			agentID = normalizedSessionCandidate(root.Get("metadata.agent_id").String())
			if agentID == "" {
				agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
			}
			if agentID == "" && hasNestedReq {
				agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
				if agentID == "" {
					agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
				}
			}
		}
		parentAgentID := sessionHeaderValue(headers, "X-Claude-Code-Parent-Agent-Id")
		if agentID != "" && agentID != "main" {
			primary = "claude:" + sid + ":agent:" + agentID
			fallback = "claude:" + sid
			if parentAgentID != "" && parentAgentID != "main" && parentAgentID != agentID {
				fallback = "claude:" + sid + ":agent:" + parentAgentID
			} else if parentSID != "" && parentSID != sid {
				fallback = "claude:" + parentSID
			} else if parentIDCandidate != "" && parentIDCandidate != sid {
				fallback = "claude:" + parentIDCandidate
			}
			return primary, fallback
		}
		primary = "claude:" + sid
		if parentSID != "" && parentSID != sid {
			fallback = "claude:" + parentSID
		} else if parentIDCandidate != "" && parentIDCandidate != sid {
			fallback = "claude:" + parentIDCandidate
		}
		return primary, fallback
	}

	// 2. OpenAI / Codex CLI
	sid := sessionHeaderValue(headers, "Session-Id")
	if sid == "" {
		sid = sessionHeaderValue(headers, "Session_id")
	}
	tid := sessionHeaderValue(headers, "Thread-Id")
	if tid == "" {
		tid = sessionHeaderValue(headers, "Thread_id")
	}

	var codexTurnMeta string
	for k, v := range headers {
		if strings.EqualFold(k, "X-Codex-Turn-Metadata") && len(v) > 0 {
			codexTurnMeta = strings.TrimSpace(v[0])
			break
		}
	}
	var codexTurnMetaJSON gjson.Result
	if codexTurnMeta != "" {
		codexTurnMetaJSON = gjson.Parse(codexTurnMeta)
	}

	if sid == "" && codexTurnMetaJSON.Exists() {
		sid = normalizedSessionCandidate(codexTurnMetaJSON.Get("session_id").String())
	}
	if tid == "" && codexTurnMetaJSON.Exists() {
		tid = normalizedSessionCandidate(codexTurnMetaJSON.Get("thread_id").String())
	}
	if tid == "" && sid != "" && root.Exists() {
		for _, path := range []string{"thread_id", "threadId", "metadata.thread_id"} {
			if tid = normalizedSessionCandidate(root.Get(path).String()); tid != "" {
				break
			}
			if hasNestedReq {
				if tid = normalizedSessionCandidate(reqRoot.Get(path).String()); tid != "" {
					break
				}
			}
		}
	}

	if sid != "" || tid != "" {
		parentThreadID := sessionHeaderValue(headers, "x-codex-parent-thread-id")
		if parentThreadID == "" {
			parentThreadID = sessionHeaderValue(headers, "X-Codex-Parent-Thread-Id")
		}
		if parentThreadID == "" && codexTurnMetaJSON.Exists() {
			parentThreadID = normalizedSessionCandidate(codexTurnMetaJSON.Get("parent_thread_id").String())
		}

		forkedFrom := ""
		if codexTurnMetaJSON.Exists() {
			forkedFrom = normalizedSessionCandidate(codexTurnMetaJSON.Get("forked_from_thread_id").String())
			if forkedFrom == "" {
				forkedFrom = normalizedSessionCandidate(codexTurnMetaJSON.Get("forked_from_id").String())
			}
		}
		if forkedFrom == "" && root.Exists() {
			for _, forkPath := range []string{
				"forked_from_thread_id", "forked_from_id",
				"metadata.forked_from_thread_id", "metadata.forked_from_id",
				"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
			} {
				if forkedFrom = normalizedSessionCandidate(root.Get(forkPath).String()); forkedFrom != "" {
					break
				}
				if hasNestedReq {
					if forkedFrom = normalizedSessionCandidate(reqRoot.Get(forkPath).String()); forkedFrom != "" {
						break
					}
				}
			}
		}

		cleanAgentName := ""
		if codexTurnMetaJSON.Exists() {
			rawName := codexTurnMetaJSON.Get("agent_name").String()
			rawName = strings.TrimPrefix(rawName, "/root/")
			rawName = strings.TrimPrefix(rawName, "/")
			rawName = strings.TrimSpace(rawName)
			rawName = normalizedSessionCandidate(rawName)
			if rawName != "" && rawName != "root" && rawName != "main" {
				cleanAgentName = rawName
			}
		}

		subVal := sessionHeaderValue(headers, "X-Openai-Subagent")
		subagentSignal := subVal != "" && !strings.EqualFold(subVal, "false") && subVal != "0"
		if codexTurnMetaJSON.Exists() && codexTurnMetaJSON.Get("subagent_kind").String() == "thread_spawn" {
			subagentSignal = true
		}

		// 1. Fork detection
		if forkedFrom != "" {
			forkSessionID := tid
			if forkSessionID == "" {
				forkSessionID = sid
			}
			if forkSessionID == forkedFrom && sid != "" && sid != forkedFrom {
				forkSessionID = sid
			}
			primary = "codex:" + forkSessionID
			fallback = "codex:" + forkedFrom
			if metadata != nil {
				metadata[cliproxyexecutor.IsForkMetadataKey] = true
				metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = fallback
			}
			return primary, fallback
		}

		// 2. Multi-Agent / Subagent detection
		if subagentSignal || (tid != "" && sid != "" && tid != sid) || (parentThreadID != "" && parentThreadID != tid && parentThreadID != sid) {
			childSessionID := tid
			if childSessionID == "" {
				childSessionID = sid
			}
			parentSID := parentThreadID
			if parentSID == "" {
				parentSID = sid
			}
			if cleanAgentName != "" && sid != "" {
				primary = "codex:" + sid + ":agent:" + cleanAgentName
				if parentSID != "" {
					fallback = "codex:" + parentSID
				} else if parentIDCandidate != "" && parentIDCandidate != sid {
					fallback = "codex:" + parentIDCandidate
				}
			} else {
				primary = "codex:" + childSessionID
				if parentSID != "" && parentSID != childSessionID {
					fallback = "codex:" + parentSID
				} else if parentIDCandidate != "" && parentIDCandidate != childSessionID {
					fallback = "codex:" + parentIDCandidate
				}
			}
			if metadata != nil && fallback != "" {
				metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = fallback
			}
			return primary, fallback
		}

		// 3. Normal session
		sessionID := sid
		if sessionID == "" {
			sessionID = tid
		}
		primary = "codex:" + sessionID
		if parentThreadID != "" && parentThreadID != sessionID {
			fallback = "codex:" + parentThreadID
		} else if parentIDCandidate != "" && parentIDCandidate != sessionID {
			fallback = "codex:" + parentIDCandidate
		}
		if metadata != nil && fallback != "" {
			metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = fallback
		}
		return primary, fallback
	}

	// 3. Antigravity CLI (agy) / Google Cloud Code
	if sid := sessionHeaderValue(headers, "X-Http-Session-Id"); sid != "" {
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID != "" && parentSID != sid {
			return "agy:" + sid, "agy:" + parentSID
		}
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "agy:" + sid, "agy:" + parentIDCandidate
		}
		return "agy:" + sid, ""
	}

	// 4. OpenCode / Pi Slot / Generic Headers
	if sid := sessionHeaderValue(headers, "X-Session-ID"); sid != "" {
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID != "" && parentSID != sid {
			return "header:" + sid, "header:" + parentSID
		}
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "header:" + sid, "header:" + parentIDCandidate
		}
		return "header:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Session-Affinity"); sid != "" {
		parentAffinity := sessionHeaderValue(headers, "X-Parent-Session-Affinity")
		if parentAffinity == "" {
			parentAffinity = sessionHeaderValue(headers, "X-Parent-Session-ID")
		}
		if parentAffinity != "" && parentAffinity != sid {
			return "affinity:" + sid, "affinity:" + parentAffinity
		}
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "affinity:" + sid, "affinity:" + parentIDCandidate
		}
		return "affinity:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Slot-Session-Id"); sid != "" {
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID != "" && parentSID != sid {
			return "slot:" + sid, "slot:" + parentSID
		}
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "slot:" + sid, "slot:" + parentIDCandidate
		}
		return "slot:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Conversation-Id"); sid != "" {
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "conv:" + sid, "conv:" + parentIDCandidate
		}
		return "conv:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Conversation-ID"); sid != "" {
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "conv:" + sid, "conv:" + parentIDCandidate
		}
		return "conv:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Thread-Id"); sid != "" {
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "thread:" + sid, "thread:" + parentIDCandidate
		}
		return "thread:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Thread-ID"); sid != "" {
		if parentIDCandidate != "" && parentIDCandidate != sid {
			return "thread:" + sid, "thread:" + parentIDCandidate
		}
		return "thread:" + sid, ""
	}
	if sid := sessionHeaderValue(headers, "X-Client-Request-Id"); sid != "" {
		return "clientreq:" + sid, ""
	}

	// 5. Body payload inspection
	if len(payload) > 0 && root.Exists() {
		reqRoot := root
		req := root.Get("request")
		hasNestedReq := req.Exists() && !root.Get("contents").Exists()
		if hasNestedReq {
			reqRoot = req
		}

		// Google Gemini Context Caching
		for _, cachePath := range []string{"cachedContent", "cached_content"} {
			cacheID := normalizedSessionCandidate(root.Get(cachePath).String())
			if cacheID == "" && hasNestedReq {
				cacheID = normalizedSessionCandidate(reqRoot.Get(cachePath).String())
			}
			if cacheID != "" {
				if parentIDCandidate != "" && parentIDCandidate != cacheID {
					return "geminicache:" + cacheID, "geminicache:" + parentIDCandidate
				}
				return "geminicache:" + cacheID, ""
			}
		}

		// OpenAI Assistants / Threads
		for _, threadPath := range []string{"thread_id", "threadId", "metadata.thread_id"} {
			tid := normalizedSessionCandidate(root.Get(threadPath).String())
			if tid == "" && hasNestedReq {
				tid = normalizedSessionCandidate(reqRoot.Get(threadPath).String())
			}
			if tid != "" {
				if parentIDCandidate != "" && parentIDCandidate != tid {
					if isBodyForkCandidate(root, reqRoot, hasNestedReq) && metadata != nil {
						metadata[cliproxyexecutor.IsForkMetadataKey] = true
						metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = "thread:" + parentIDCandidate
					}
					return "thread:" + tid, "thread:" + parentIDCandidate
				}
				return "thread:" + tid, ""
			}
		}

		// Session ID paths
		agentID := normalizedSessionCandidate(root.Get("metadata.agent_id").String())
		if agentID == "" {
			agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
		}
		if agentID == "" {
			agentID = sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
		}
		if agentID == "" {
			agentID = sessionHeaderValue(headers, "x-agent-id")
		}
		if agentID == "" && hasNestedReq {
			agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
			if agentID == "" {
				agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
			}
		}
		for _, path := range []string{"session_id", "sessionId", "sessionID", "metadata.session_id", "extra_body.session_id"} {
			sid := normalizedSessionCandidate(root.Get(path).String())
			if sid == "" && hasNestedReq {
				sid = normalizedSessionCandidate(reqRoot.Get(path).String())
			}
			if sid != "" {
				if agentID != "" && agentID != "main" {
					primary = "session:" + sid + ":agent:" + agentID
					fallback = "session:" + sid
					if parentIDCandidate != "" && parentIDCandidate != sid {
						fallback = "session:" + parentIDCandidate
					}
					return primary, fallback
				}
				if parentIDCandidate != "" && parentIDCandidate != sid {
					fallback = "session:" + parentIDCandidate
					if isBodyForkCandidate(root, reqRoot, hasNestedReq) && metadata != nil {
						metadata[cliproxyexecutor.IsForkMetadataKey] = true
						metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = fallback
					}
					return "session:" + sid, fallback
				}
				return "session:" + sid, ""
			}
		}

		conversationID := ""
		conversation := root.Get("conversation")
		if !conversation.Exists() && hasNestedReq {
			conversation = reqRoot.Get("conversation")
		}
		if sid := normalizedSessionCandidate(conversation.Get("id").String()); sid != "" {
			conversationID = "conv:" + sid
		} else if conversation.Type == gjson.String {
			if sid := normalizedSessionCandidate(conversation.String()); sid != "" {
				conversationID = "conv:" + sid
			}
		}
		pck := normalizedSessionCandidate(root.Get("prompt_cache_key").String())
		if pck == "" {
			pck = normalizedSessionCandidate(root.Get("promptCacheKey").String())
		}
		if pck == "" && hasNestedReq {
			pck = normalizedSessionCandidate(reqRoot.Get("prompt_cache_key").String())
			if pck == "" {
				pck = normalizedSessionCandidate(reqRoot.Get("promptCacheKey").String())
			}
		}
		if pck != "" {
			return "pck:" + pck, conversationID
		}
		if conversationID != "" {
			if parentIDCandidate != "" && ("conv:"+parentIDCandidate) != conversationID {
				return conversationID, "conv:" + parentIDCandidate
			}
			return conversationID, ""
		}
		userID := normalizedSessionCandidate(root.Get("metadata.user_id").String())
		if userID == "" && hasNestedReq {
			userID = normalizedSessionCandidate(reqRoot.Get("metadata.user_id").String())
		}
		if userID != "" {
			return "user:" + userID, ""
		}
		for _, convPath := range []string{"conversation_id", "conversationId", "chat_id", "chatId", "metadata.conversation_id", "extra_body.conversation_id"} {
			cid := normalizedSessionCandidate(root.Get(convPath).String())
			if cid == "" && hasNestedReq {
				cid = normalizedSessionCandidate(reqRoot.Get(convPath).String())
			}
			if cid != "" {
				if parentIDCandidate != "" && ("conv:"+parentIDCandidate) != ("conv:"+cid) {
					return "conv:" + cid, "conv:" + parentIDCandidate
				}
				return "conv:" + cid, ""
			}
		}
	}

	if executionID, ok := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok {
		if executionID = normalizedSessionCandidate(executionID); executionID != "" {
			return "execution:" + executionID, ""
		}
	}
	return "", ""
}

// extractSessionIDs returns (primaryID, fallbackID) for session affinity.
// fallbackID preserves an earlier binding when a stronger body identifier appears
// later, and lets callers bind both identifiers when both are present.
func extractSessionIDs(headers http.Header, payload []byte, metadata map[string]any) (string, string) {
	if primaryID, fallbackID := extractExplicitSessionIDs(headers, payload, metadata); primaryID != "" {
		return primaryID, fallbackID
	}
	if derivedID := normalizedSessionCandidate(cliproxysession.DerivedID(metadata)); derivedID != "" {
		return "derived:" + derivedID, ""
	}
	if len(payload) == 0 {
		return "", ""
	}
	return extractMessageHashIDs(payload)
}

func extractMessageHashIDs(payload []byte) (primaryID, fallbackID string) {
	var systemPrompt, firstUserMsg, firstAssistantMsg string

	// OpenAI/Claude messages format
	messages := gjson.GetBytes(payload, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			role := msg.Get("role").String()
			content := extractMessageContent(msg.Get("content"))
			if content == "" {
				return true
			}

			switch role {
			case "system":
				if systemPrompt == "" {
					systemPrompt = truncateString(content, 100)
				}
			case "user":
				if firstUserMsg == "" {
					firstUserMsg = truncateString(content, 100)
				}
			case "assistant":
				if firstAssistantMsg == "" {
					firstAssistantMsg = truncateString(content, 100)
				}
			}

			if systemPrompt != "" && firstUserMsg != "" && firstAssistantMsg != "" {
				return false
			}
			return true
		})
	}

	// Claude API: top-level "system" field (array or string)
	if systemPrompt == "" {
		topSystem := gjson.GetBytes(payload, "system")
		if topSystem.Exists() {
			if topSystem.IsArray() {
				topSystem.ForEach(func(_, part gjson.Result) bool {
					if text := part.Get("text").String(); text != "" && systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
						return false
					}
					return true
				})
			} else if topSystem.Type == gjson.String {
				systemPrompt = truncateString(topSystem.String(), 100)
			}
		}
	}

	// Gemini format
	if systemPrompt == "" && firstUserMsg == "" {
		sysInstr := gjson.GetBytes(payload, "systemInstruction.parts")
		if sysInstr.Exists() && sysInstr.IsArray() {
			sysInstr.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text").String(); text != "" && systemPrompt == "" {
					systemPrompt = truncateString(text, 100)
					return false
				}
				return true
			})
		}

		contents := gjson.GetBytes(payload, "contents")
		if contents.Exists() && contents.IsArray() {
			contents.ForEach(func(_, msg gjson.Result) bool {
				role := msg.Get("role").String()
				msg.Get("parts").ForEach(func(_, part gjson.Result) bool {
					text := part.Get("text").String()
					if text == "" {
						return true
					}
					switch role {
					case "user":
						if firstUserMsg == "" {
							firstUserMsg = truncateString(text, 100)
						}
					case "model":
						if firstAssistantMsg == "" {
							firstAssistantMsg = truncateString(text, 100)
						}
					}
					return false
				})
				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	// OpenAI Responses API format (v1/responses)
	if systemPrompt == "" && firstUserMsg == "" {
		if instr := gjson.GetBytes(payload, "instructions").String(); instr != "" {
			systemPrompt = truncateString(instr, 100)
		}

		input := gjson.GetBytes(payload, "input")
		if input.Exists() && input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				itemType := item.Get("type").String()
				if itemType == "reasoning" {
					return true
				}
				// Skip non-message typed items (function_call, function_call_output, etc.)
				// but allow items with no type that have a role (inline message format).
				if itemType != "" && itemType != "message" {
					return true
				}

				role := item.Get("role").String()
				if itemType == "" && role == "" {
					return true
				}

				// Handle both string content and array content (multimodal).
				content := item.Get("content")
				var text string
				if content.Type == gjson.String {
					text = content.String()
				} else {
					text = extractResponsesAPIContent(content)
				}
				if text == "" {
					return true
				}

				switch role {
				case "developer", "system":
					if systemPrompt == "" {
						systemPrompt = truncateString(text, 100)
					}
				case "user":
					if firstUserMsg == "" {
						firstUserMsg = truncateString(text, 100)
					}
				case "assistant":
					if firstAssistantMsg == "" {
						firstAssistantMsg = truncateString(text, 100)
					}
				}

				if firstUserMsg != "" && firstAssistantMsg != "" {
					return false
				}
				return true
			})
		}
	}

	if firstUserMsg == "" {
		return "", ""
	}

	shortHash := computeSessionHash(systemPrompt, firstUserMsg, "")
	if firstAssistantMsg == "" {
		return shortHash, ""
	}

	fullHash := computeSessionHash(systemPrompt, firstUserMsg, firstAssistantMsg)
	return fullHash, shortHash
}

func computeSessionHash(systemPrompt, userMsg, assistantMsg string) string {
	h := fnv.New64a()
	if systemPrompt != "" {
		h.Write([]byte("sys:" + systemPrompt + "\n"))
	}
	if userMsg != "" {
		h.Write([]byte("usr:" + userMsg + "\n"))
	}
	if assistantMsg != "" {
		h.Write([]byte("ast:" + assistantMsg + "\n"))
	}
	return fmt.Sprintf("msg:%016x", h.Sum64())
}

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// extractMessageContent extracts text content from a message content field.
// Handles both string content and array content (multimodal messages).
// For array content, extracts text from all text-type elements.
func extractMessageContent(content gjson.Result) string {
	// String content: "Hello world"
	if content.Type == gjson.String {
		return content.String()
	}

	// Array content: [{"type":"text","text":"Hello"},{"type":"image",...}]
	if content.IsArray() {
		var texts []string
		content.ForEach(func(_, part gjson.Result) bool {
			// Handle Claude format: {"type":"text","text":"content"}
			if part.Get("type").String() == "text" {
				if text := part.Get("text").String(); text != "" {
					texts = append(texts, text)
				}
			}
			// Handle OpenAI format: {"type":"text","text":"content"}
			// Same structure as Claude, already handled above
			return true
		})
		if len(texts) > 0 {
			return strings.Join(texts, " ")
		}
	}

	return ""
}

func extractResponsesAPIContent(content gjson.Result) string {
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		partType := part.Get("type").String()
		if partType == "input_text" || partType == "output_text" || partType == "text" {
			if text := part.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
		return true
	})
	if len(texts) > 0 {
		return strings.Join(texts, " ")
	}
	return ""
}

// extractSessionID is kept for backward compatibility.
// Deprecated: Use ExtractSessionID instead.
func extractSessionID(payload []byte) string {
	return ExtractSessionID(nil, payload, nil)
}
