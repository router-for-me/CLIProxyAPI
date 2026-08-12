package helps

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

const (
	// claudeAnthropicVersion is the only Anthropic-Version Claude Code sends.
	claudeAnthropicVersion = "2023-06-01"
	// claudeDefaultStainlessTimeout is the X-Stainless-Timeout every measured
	// native helper sends. It is deliberately NOT read from
	// claude-header-defaults.timeout: applyClaudeHeaders routes a confirmed client
	// through misc.EnsureHeader, which prefers the incoming header and only falls
	// back to the configured value when the caller sent none. A confirmed helper
	// therefore always forwards its own 600, so comparing against the operator
	// value would make any non-600 configuration reject every genuine helper.
	claudeDefaultStainlessTimeout = "600"
)

var (
	claudeCodeUserAgentPattern        = regexp.MustCompile(`(?i)^claude-cli/`)
	claudeCodeUserAgentDetailsPattern = regexp.MustCompile(`(?i)^claude-cli/\S+\s+\(external,\s*([^,)]+)(?:,\s*agent-sdk/([^,)]+))?`)
	claudeCodeNativeUserAgentPattern  = regexp.MustCompile(`(?i)^claude-cli/[0-9]+\.[0-9]+\.[0-9]+\s+\(external,\s*[^,)]+(?:,\s*agent-sdk/[0-9]+\.[0-9]+\.[0-9]+)?\)$`)
)

var claudeCodeSubclientByEntrypoint = map[string]string{
	"cli":                       "claude-code-cli",
	"mcp":                       "claude-code-mcp",
	"bench":                     "claude-code-bench",
	"sdk-cli":                   "claude-code-cli-sdk",
	"sdk-ts":                    "claude-code-sdk-ts",
	"sdk-py":                    "claude-code-sdk-py",
	"claude-vscode":             "claude-code-vscode",
	"claude-code-github-action": "claude-code-gh-action",
	"local-agent":               "claude-local-agent",
	"local_agent":               "claude-local-agent",
	"claude-desktop":            "claude-desktop",
	"claude-desktop-3p":         "claude-desktop-3p",
	"remote":                    "claude-remote",
	"remote_baku":               "claude-remote-baku",
	"remote_cowork":             "claude-remote-cowork",
	"remote_trigger":            "claude-remote-trigger",
	"remote_desktop":            "claude-remote-desktop",
	"remote_mobile":             "claude-remote-mobile",
	"claude_in_slack":           "claude-in-slack",
	"claude-in-slack":           "claude-in-slack",
	"claude-in-teams":           "claude-in-teams",
	"claude-security":           "claude-security",
	"ssh-remote":                "claude-ssh-remote",
	"claude-coworker":           "claude-coworker",
	"claude-coworker-terminal":  "claude-coworker-terminal",
}

// Only product surfaces with verified 2.1.220 wire behavior are eligible for
// pass-through. Other first-party-looking entrypoints are cloaked until their
// CPA-reachable request shape has been captured and reviewed.
var nativeClaudeEntrypoints = map[string]bool{
	"cli":           true,
	"sdk-cli":       true,
	"claude-vscode": true,
}

type claudeCodeHelperShape uint8

const (
	claudeCodeHelperShapeNone claudeCodeHelperShape = iota
	claudeCodeHelperShapeMinimal
	claudeCodeHelperShapeStructured

	claudeCodeHelperModel = "claude-haiku-4-5-20251001"
)

// These are the six exact beta sequences observed across 14 markerless native
// Claude Code 2.1.220 Haiku helper requests. Keeping the allowlist exact avoids
// turning the helper exception into a generic no-claude-code-beta bypass.
var measuredClaudeCodeHelperBetaProfiles = map[string]claudeCodeHelperShape{
	claudeCodeHelperBetaProfile(true):  claudeCodeHelperShapeMinimal,
	claudeCodeHelperBetaProfile(false): claudeCodeHelperShapeMinimal,
	claudeCodeHelperBetaProfile(true,
		"advisor-tool-2026-03-01",
		"structured-outputs-2025-12-15",
		"cache-diagnosis-2026-04-07",
	): claudeCodeHelperShapeStructured,
	claudeCodeHelperBetaProfile(true,
		"structured-outputs-2025-12-15",
		"fallback-credit-2026-06-01",
	): claudeCodeHelperShapeStructured,
	claudeCodeHelperBetaProfile(true,
		"structured-outputs-2025-12-15",
	): claudeCodeHelperShapeStructured,
	claudeCodeHelperBetaProfile(false,
		"structured-outputs-2025-12-15",
	): claudeCodeHelperShapeStructured,
}

// ClaudeCodeRequestDetection records the strong signals and first-party
// subclient identity used to distinguish an official Claude Code request from
// a client that only copied its User-Agent.
type ClaudeCodeRequestDetection struct {
	Confirmed       bool
	StrongSignals   bool
	NativeClient    bool
	XAppCLI         bool
	UserAgent       bool
	BetasPresent    bool
	MetadataUserID  bool
	HelperProfile   bool
	Entrypoint      string
	Subclient       string
	AgentSDKVersion string
}

// DetectClaudeCodeRequest first mirrors CCH's strong-signal contract, then
// applies CPA's native-client policy. Standard Messages requests require all
// four strong signals; count_tokens omits metadata.user_id. A separate narrow
// profile recognizes measured native Haiku helper requests that intentionally
// omit claude-code-20250219. Generic sdk-ts/sdk-py Agent SDK entrypoints remain
// unconfirmed and receive CLI cloaking.
func DetectClaudeCodeRequest(headers http.Header, payload []byte, countTokens bool, configs ...*config.Config) ClaudeCodeRequestDetection {
	var cfg *config.Config
	if len(configs) > 0 {
		cfg = configs[0]
	}
	userAgent := headerValue(headers, "User-Agent")
	entrypoint, agentSDKVersion := parseClaudeCodeUserAgentDetails(userAgent)
	detection := ClaudeCodeRequestDetection{
		XAppCLI:         headerValue(headers, "X-App") == "cli",
		UserAgent:       plausibleClaudeCodeUserAgent(userAgent, cfg),
		BetasPresent:    headerContainsClaudeCodeBeta(headers),
		Entrypoint:      entrypoint,
		Subclient:       claudeCodeSubclientByEntrypoint[entrypoint],
		AgentSDKVersion: agentSDKVersion,
	}

	metadataUserID := gjson.GetBytes(payload, "metadata.user_id")
	detection.MetadataUserID = metadataUserID.Exists() && metadataUserID.Type == gjson.String && isValidUserID(metadataUserID.String())
	detection.NativeClient = nativeClaudeEntrypoints[entrypoint]
	standardSignals := detection.XAppCLI && detection.UserAgent && detection.BetasPresent && (countTokens || detection.MetadataUserID)
	detection.HelperProfile = detection.NativeClient && matchesMeasuredClaudeCodeHelperProfile(headers, payload, countTokens, detection, cfg)
	detection.StrongSignals = standardSignals || detection.HelperProfile
	detection.Confirmed = detection.StrongSignals && detection.NativeClient
	return detection
}

func claudeCodeHelperBetaProfile(redactThinking bool, trailing ...string) string {
	betas := []string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14"}
	if redactThinking {
		betas = append(betas, "redact-thinking-2026-02-12")
	}
	betas = append(betas,
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
	)
	betas = append(betas, trailing...)
	return strings.Join(betas, ",")
}

func matchesMeasuredClaudeCodeHelperProfile(
	headers http.Header,
	payload []byte,
	countTokens bool,
	detection ClaudeCodeRequestDetection,
	cfg *config.Config,
) bool {
	if countTokens ||
		detection.Entrypoint != "cli" ||
		detection.BetasPresent ||
		!detection.XAppCLI ||
		!detection.UserAgent ||
		!detection.MetadataUserID {
		return false
	}

	shape := measuredClaudeCodeHelperBetaProfiles[normalizedClaudeBetaHeader(headers)]
	if shape == claudeCodeHelperShapeNone || measuredClaudeCodeHelperBodyShape(payload) != shape {
		return false
	}
	if !measuredClaudeCodeHelperHeadersMatch(headers, cfg, shape) {
		return false
	}
	return measuredClaudeCodeHelperSessionMatches(headers, payload)
}

// normalizedClaudeBetaHeader joins every Anthropic-Beta value in wire order.
// Values() is tried first so canonical headers keep a deterministic order; the
// case-insensitive fallback only exists for hand-built header maps that store a
// non-canonical key, where ranging the map alone would be order-dependent.
func normalizedClaudeBetaHeader(headers http.Header) string {
	if headers == nil {
		return ""
	}
	values := headers.Values("Anthropic-Beta")
	if len(values) == 0 {
		keys := make([]string, 0, 2)
		for key := range headers {
			if strings.EqualFold(key, "Anthropic-Beta") {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			values = append(values, headers[key]...)
		}
	}
	betas := make([]string, 0, 12)
	for _, value := range values {
		for _, beta := range strings.Split(value, ",") {
			if beta = strings.TrimSpace(beta); beta != "" {
				betas = append(betas, beta)
			}
		}
	}
	return strings.Join(betas, ",")
}

// measuredClaudeCodeHelperHeadersMatch validates the helper transport envelope.
//
// Platform and software-version headers are deliberately NOT compared for
// equality. The device-profile pipeline this detector feeds already pins OS/Arch
// to the configured baseline and replaces a non-baseline software tuple instead
// of rejecting it, so demanding equality here would classify a genuine Claude
// Code helper from Windows/Linux, or from a different Node or SDK build, as a
// foreign client and cloak it. Values that carry real discriminating power - the
// exact beta allowlist, the body shape, the billing CCH and the session binding -
// stay strict.
func measuredClaudeCodeHelperHeadersMatch(headers http.Header, cfg *config.Config, shape claudeCodeHelperShape) bool {
	profile := defaultClaudeDeviceProfile(cfg)
	expected := map[string]string{
		"Accept":                  "application/json",
		"Content-Type":            "application/json",
		"X-Stainless-Lang":        "js",
		"X-Stainless-Runtime":     "node",
		"X-Stainless-Retry-Count": "0",
		"X-Stainless-Timeout":     claudeDefaultStainlessTimeout,
		"Anthropic-Version":       claudeAnthropicVersion,
		"Anthropic-Dangerous-Direct-Browser-Access": "true",
	}
	for name, want := range expected {
		if headerValue(headers, name) != want {
			return false
		}
	}
	// Presence is still required: the native SDK always sends these.
	for _, name := range []string{
		"X-Stainless-Package-Version",
		"X-Stainless-Runtime-Version",
		"X-Stainless-OS",
		"X-Stainless-Arch",
	} {
		if headerValue(headers, name) == "" {
			return false
		}
	}
	candidate := ClaudeDeviceProfile{
		UserAgent:      headerValue(headers, "User-Agent"),
		PackageVersion: headerValue(headers, "X-Stainless-Package-Version"),
		RuntimeVersion: headerValue(headers, "X-Stainless-Runtime-Version"),
	}
	if version, ok := parseClaudeCLIVersion(candidate.UserAgent); ok {
		candidate.version = version
		candidate.hasVersion = true
	}
	if !meetsClaudeDeviceProfileBaseline(candidate, profile) {
		return false
	}
	if async := headerValue(headers, "X-Stainless-Async"); (shape == claudeCodeHelperShapeStructured && async != "async") ||
		(shape == claudeCodeHelperShapeMinimal && async != "") {
		return false
	}
	compression := headerValue(headers, "Accept-Encoding")
	if (shape == claudeCodeHelperShapeStructured && compression != "gzip, deflate, br, zstd") ||
		(shape == claudeCodeHelperShapeMinimal && compression != "gzip") {
		return false
	}
	requestID := headerValue(headers, "X-Client-Request-Id")
	_, errRequestID := uuid.Parse(requestID)
	return errRequestID == nil
}

func measuredClaudeCodeHelperSessionMatches(headers http.Header, payload []byte) bool {
	metadata := gjson.GetBytes(payload, "metadata")
	if !metadata.IsObject() || !claudeJSONObjectHasKeys([]byte(metadata.Raw), []string{"user_id"}) {
		return false
	}
	userID := metadata.Get("user_id")
	if userID.Type != gjson.String || !isValidUserID(userID.String()) {
		return false
	}
	// The native metadata builder is
	//	{...extraMetadata, device_id, account_uuid, session_id, ...parentSessionId && {parent_session_id}}
	// in 2.1.220, 2.1.221 and 2.1.227 alike, so parent_session_id is a legitimate
	// optional trailing key for sub-agent and forked sessions. Rejecting it would
	// cloak the helper requests those sessions issue.
	identityRaw := []byte(userID.String())
	if !claudeJSONObjectHasKeys(identityRaw, []string{"device_id", "account_uuid", "session_id"}) &&
		!claudeJSONObjectHasKeys(identityRaw, []string{"device_id", "account_uuid", "session_id", "parent_session_id"}) {
		return false
	}
	return headerValue(headers, ClaudeCodeSessionHeader) == gjson.GetBytes(identityRaw, "session_id").String()
}

func measuredClaudeCodeHelperBodyShape(payload []byte) claudeCodeHelperShape {
	minimalKeys := []string{"model", "max_tokens", "messages", "metadata"}
	structuredKeys := []string{"model", "messages", "system", "tools", "metadata", "max_tokens", "thinking", "temperature", "output_config", "stream"}
	shape := claudeCodeHelperShapeNone
	switch {
	case claudeJSONObjectHasKeys(payload, minimalKeys):
		shape = claudeCodeHelperShapeMinimal
	case claudeJSONObjectHasKeys(payload, structuredKeys):
		shape = claudeCodeHelperShapeStructured
	default:
		return claudeCodeHelperShapeNone
	}

	maxTokens := gjson.GetBytes(payload, "max_tokens")
	if gjson.GetBytes(payload, "model").String() != claudeCodeHelperModel ||
		maxTokens.Type != gjson.Number {
		return claudeCodeHelperShapeNone
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() || len(messages.Array()) != 1 {
		return claudeCodeHelperShapeNone
	}
	message := messages.Get("0")
	if !claudeJSONObjectHasKeys([]byte(message.Raw), []string{"role", "content"}) ||
		message.Get("role").String() != "user" {
		return claudeCodeHelperShapeNone
	}

	if shape == claudeCodeHelperShapeMinimal {
		if maxTokens.Raw != "1" || message.Get("content").Type != gjson.String {
			return claudeCodeHelperShapeNone
		}
		return shape
	}

	content := message.Get("content")
	if !content.IsArray() || len(content.Array()) != 1 {
		return claudeCodeHelperShapeNone
	}
	contentBlock := content.Get("0")
	if !claudeJSONObjectHasKeys([]byte(contentBlock.Raw), []string{"type", "text"}) ||
		contentBlock.Get("type").String() != "text" {
		return claudeCodeHelperShapeNone
	}
	if !measuredClaudeCodeHelperSystemMatches(gjson.GetBytes(payload, "system")) {
		return claudeCodeHelperShapeNone
	}
	if tools := gjson.GetBytes(payload, "tools"); !tools.IsArray() || len(tools.Array()) != 0 {
		return claudeCodeHelperShapeNone
	}
	thinking := gjson.GetBytes(payload, "thinking")
	outputConfig := gjson.GetBytes(payload, "output_config")
	if !claudeJSONObjectHasKeys([]byte(thinking.Raw), []string{"type"}) ||
		thinking.Get("type").String() != "disabled" {
		return claudeCodeHelperShapeNone
	}
	format := outputConfig.Get("format")
	schema := format.Get("schema")
	properties := schema.Get("properties")
	titleProperty := properties.Get("title")
	required := schema.Get("required")
	additionalProperties := schema.Get("additionalProperties")
	if !claudeJSONObjectHasKeys([]byte(outputConfig.Raw), []string{"format"}) ||
		!claudeJSONObjectHasKeys([]byte(format.Raw), []string{"type", "schema"}) ||
		format.Get("type").String() != "json_schema" ||
		!claudeJSONObjectHasKeys([]byte(schema.Raw), []string{"type", "properties", "required", "additionalProperties"}) ||
		schema.Get("type").String() != "object" ||
		!claudeJSONObjectHasKeys([]byte(properties.Raw), []string{"title"}) ||
		!claudeJSONObjectHasKeys([]byte(titleProperty.Raw), []string{"type"}) ||
		titleProperty.Get("type").String() != "string" ||
		!required.IsArray() || len(required.Array()) != 1 || required.Get("0").String() != "title" ||
		additionalProperties.Type != gjson.False {
		return claudeCodeHelperShapeNone
	}
	temperature := gjson.GetBytes(payload, "temperature")
	if maxTokens.Raw != "32000" ||
		temperature.Raw != "1" ||
		gjson.GetBytes(payload, "stream").Type != gjson.True {
		return claudeCodeHelperShapeNone
	}
	return shape
}

func measuredClaudeCodeHelperSystemMatches(system gjson.Result) bool {
	if !system.IsArray() || len(system.Array()) != 3 {
		return false
	}
	for _, block := range system.Array() {
		if !claudeJSONObjectHasKeys([]byte(block.Raw), []string{"type", "text"}) || block.Get("type").String() != "text" {
			return false
		}
	}
	billing := system.Get("0.text").String()
	identity := system.Get("1.text").String()
	return strings.HasPrefix(billing, "x-anthropic-billing-header:") && measuredClaudeBillingCCH(billing) && strings.HasPrefix(identity, "You are Claude Code")
}

// measuredClaudeBillingCCH validates the five lowercase hexadecimal characters the
// native billing header carries. It duplicates isLowerHex in
// internal/runtime/executor/claude_signing.go because the signing side lives in the
// package that imports this one; keep the two definitions in step.
func measuredClaudeBillingCCH(billing string) bool {
	marker := strings.Index(billing, " cch=")
	if marker < 0 {
		return false
	}
	valueStart := marker + len(" cch=")
	valueEnd := valueStart + 5
	if valueEnd >= len(billing) || billing[valueEnd] != ';' {
		return false
	}
	for _, character := range billing[valueStart:valueEnd] {
		decimal := character >= '0' && character <= '9'
		lowerHex := character >= 'a' && character <= 'f'
		if !decimal && !lowerHex {
			return false
		}
	}
	return true
}

func claudeJSONObjectHasKeys(raw []byte, want []string) bool {
	if !json.Valid(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, errOpening := decoder.Token()
	if errOpening != nil || opening != json.Delim('{') {
		return false
	}
	keyIndex := 0
	for decoder.More() {
		token, errToken := decoder.Token()
		if errToken != nil {
			return false
		}
		key, okKey := token.(string)
		if !okKey || keyIndex >= len(want) || key != want[keyIndex] {
			return false
		}
		keyIndex++
		var value json.RawMessage
		if errValue := decoder.Decode(&value); errValue != nil {
			return false
		}
	}
	closing, errClosing := decoder.Token()
	return errClosing == nil && closing == json.Delim('}') && keyIndex == len(want)
}

func plausibleClaudeCodeUserAgent(userAgent string, cfg *config.Config) bool {
	userAgent = strings.TrimSpace(userAgent)
	if !claudeCodeUserAgentPattern.MatchString(userAgent) || !claudeCodeNativeUserAgentPattern.MatchString(userAgent) {
		return false
	}
	candidate, okCandidate := parseClaudeCLIVersion(userAgent)
	baseline, okBaseline := parseClaudeCLIVersion(defaultClaudeDeviceProfile(cfg).UserAgent)
	return okCandidate && okBaseline && plausibleClaudeCLIVersion(candidate, baseline)
}

func parseClaudeCodeUserAgentDetails(userAgent string) (entrypoint, agentSDKVersion string) {
	matches := claudeCodeUserAgentDetailsPattern.FindStringSubmatch(strings.TrimSpace(userAgent))
	if len(matches) < 2 {
		return "", ""
	}
	entrypoint = strings.ToLower(strings.TrimSpace(matches[1]))
	if len(matches) >= 3 {
		agentSDKVersion = strings.TrimSpace(matches[2])
	}
	return entrypoint, agentSDKVersion
}

func headerValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) || len(values) == 0 {
			continue
		}
		return values[0]
	}
	return ""
}

func headerContainsClaudeCodeBeta(headers http.Header) bool {
	if headers == nil {
		return false
	}
	for key, values := range headers {
		if !strings.EqualFold(key, "Anthropic-Beta") {
			continue
		}
		for _, value := range values {
			for _, beta := range strings.Split(value, ",") {
				if strings.TrimSpace(beta) == "claude-code-20250219" {
					return true
				}
			}
		}
	}
	return false
}
