package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	kiroclaude "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/kiro/claude"
	kiroopenai "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator/kiro/openai"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// kiroEndpointConfig bundles endpoint URL with its compatible Origin and AmzTarget values.
// This solves the "triple mismatch" problem where different endpoints require matching
// Origin and X-Amz-Target header values.
//
// Based on reference implementations:
// - amq2api-main: Uses Amazon Q endpoint with CLI origin and AmazonQDeveloperStreamingService target
// - AIClient-2-API: Uses CodeWhisperer endpoint with AI_EDITOR origin and AmazonCodeWhispererStreamingService target
type kiroEndpointConfig struct {
	URL       string // Endpoint URL
	Origin    string // Request Origin: "CLI" for Amazon Q quota, "AI_EDITOR" for Kiro IDE quota
	AmzTarget string // X-Amz-Target header value
	Name      string // Endpoint name for logging
}

// kiroDefaultRegion is the default AWS region for Kiro API endpoints.
// Used when no region is specified in auth metadata.
const kiroDefaultRegion = "us-east-1"

// extractRegionFromProfileARN extracts the AWS region from a ProfileARN.
// ARN format: arn:aws:codewhisperer:REGION:ACCOUNT:profile/PROFILE_ID
// Returns empty string if region cannot be extracted.
func extractRegionFromProfileARN(profileArn string) string {
	if profileArn == "" {
		return ""
	}
	parts := strings.Split(profileArn, ":")
	if len(parts) >= 4 && parts[3] != "" {
		return parts[3]
	}
	return ""
}

// buildKiroEndpointConfigs creates endpoint configurations for the specified region.
// This enables dynamic region support for Enterprise/IdC users in non-us-east-1 regions.
//
// Uses Q endpoint (q.{region}.amazonaws.com) as primary for ALL auth types:
// - Works universally across all AWS regions (CodeWhisperer endpoint only exists in us-east-1)
// - Uses /generateAssistantResponse path with AI_EDITOR origin
// - Does NOT require X-Amz-Target header
//
// The AmzTarget field is kept for backward compatibility but should be empty
// to indicate that the header should NOT be set.
func buildKiroEndpointConfigs(region string) []kiroEndpointConfig {
	if region == "" {
		region = kiroDefaultRegion
	}
	return []kiroEndpointConfig{
		{
			// Primary: Q endpoint - works for all regions and auth types
			URL:       fmt.Sprintf("https://q.%s.amazonaws.com/generateAssistantResponse", region),
			Origin:    "AI_EDITOR",
			AmzTarget: "", // Empty = don't set X-Amz-Target header
			Name:      "AmazonQ",
		},
		{
			// Fallback: CodeWhisperer endpoint (legacy, only works in us-east-1)
			URL:       fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region),
			Origin:    "AI_EDITOR",
			AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
			Name:      "CodeWhisperer",
		},
	}
}

// resolveKiroAPIRegion determines the AWS region for Kiro API calls.
// Region priority:
// 1. auth.Metadata["api_region"] - explicit API region override
// 2. ProfileARN region - extracted from arn:aws:service:REGION:account:resource
// 3. kiroDefaultRegion (us-east-1) - fallback
// Note: OIDC "region" is NOT used - it's for token refresh, not API calls
func resolveKiroAPIRegion(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return kiroDefaultRegion
	}
	// Priority 1: Explicit api_region override
	if r, ok := auth.Metadata["api_region"].(string); ok && r != "" {
		log.Debugf("kiro: using region %s (source: api_region)", r)
		return r
	}
	// Priority 2: Extract from ProfileARN
	if profileArn, ok := auth.Metadata["profile_arn"].(string); ok && profileArn != "" {
		if arnRegion := extractRegionFromProfileARN(profileArn); arnRegion != "" {
			log.Debugf("kiro: using region %s (source: profile_arn)", arnRegion)
			return arnRegion
		}
	}
	// Note: OIDC "region" field is NOT used for API endpoint
	// Kiro API only exists in us-east-1, while OIDC region can vary (e.g., ap-northeast-2)
	// Using OIDC region for API calls causes DNS failures
	log.Debugf("kiro: using region %s (source: default)", kiroDefaultRegion)
	return kiroDefaultRegion
}

// kiroEndpointConfigs is kept for backward compatibility with default us-east-1 region.
// Prefer using buildKiroEndpointConfigs(region) for dynamic region support.
var kiroEndpointConfigs = buildKiroEndpointConfigs(kiroDefaultRegion)

// getKiroEndpointConfigs returns the list of Kiro API endpoint configurations to try in order.
// Supports dynamic region based on auth metadata "api_region", "profile_arn", or "region" field.
// Supports reordering based on "preferred_endpoint" in auth metadata/attributes.
//
// Region priority:
// 1. auth.Metadata["api_region"] - explicit API region override
// 2. ProfileARN region - extracted from arn:aws:service:REGION:account:resource
// 3. kiroDefaultRegion (us-east-1) - fallback
// Note: OIDC "region" is NOT used - it's for token refresh, not API calls
func getKiroEndpointConfigs(auth *cliproxyauth.Auth) []kiroEndpointConfig {
	if auth == nil {
		return kiroEndpointConfigs
	}

	// Determine API region using shared resolution logic
	region := resolveKiroAPIRegion(auth)

	// Build endpoint configs for the specified region
	endpointConfigs := buildKiroEndpointConfigs(region)

	// For IDC auth, use Q endpoint with AI_EDITOR origin
	// IDC tokens work with Q endpoint using Bearer auth
	// The difference is only in how tokens are refreshed (OIDC with clientId/clientSecret for IDC)
	// NOT in how API calls are made - both Social and IDC use the same endpoint/origin
	if auth.Metadata != nil {
		authMethod, _ := auth.Metadata["auth_method"].(string)
		if strings.ToLower(authMethod) == "idc" {
			log.Debugf("kiro: IDC auth, using Q endpoint (region: %s)", region)
			return endpointConfigs
		}
	}

	// Check for preference
	var preference string
	if auth.Metadata != nil {
		if p, ok := auth.Metadata["preferred_endpoint"].(string); ok {
			preference = p
		}
	}
	// Check attributes as fallback (e.g. from HTTP headers)
	if preference == "" && auth.Attributes != nil {
		preference = auth.Attributes["preferred_endpoint"]
	}

	if preference == "" {
		return endpointConfigs
	}

	preference = strings.ToLower(strings.TrimSpace(preference))

	// Create new slice to avoid modifying global state
	var sorted []kiroEndpointConfig
	var remaining []kiroEndpointConfig

	for _, cfg := range endpointConfigs {
		name := strings.ToLower(cfg.Name)
		// Check for matches
		// CodeWhisperer aliases: codewhisperer, ide
		// AmazonQ aliases: amazonq, q, cli
		isMatch := false
		if (preference == "codewhisperer" || preference == "ide") && name == "codewhisperer" {
			isMatch = true
		} else if (preference == "amazonq" || preference == "q" || preference == "cli") && name == "amazonq" {
			isMatch = true
		}

		if isMatch {
			sorted = append(sorted, cfg)
		} else {
			remaining = append(remaining, cfg)
		}
	}

	// If preference didn't match anything, return default
	if len(sorted) == 0 {
		return endpointConfigs
	}

	// Combine: preferred first, then others
	return append(sorted, remaining...)
}

// isIDCAuth checks if the auth uses IDC (Identity Center) authentication method.
func isIDCAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	authMethod, _ := auth.Metadata["auth_method"].(string)
	return strings.ToLower(authMethod) == "idc"
}

// buildKiroPayloadForFormat builds the Kiro API payload based on the source format.
// This is critical because OpenAI and Claude formats have different tool structures:
// - OpenAI: tools[].function.name, tools[].function.description
// - Claude: tools[].name, tools[].description
// headers parameter allows checking Anthropic-Beta header for thinking mode detection.
// Returns the serialized JSON payload and a boolean indicating whether thinking mode was injected.
func buildKiroPayloadForFormat(body []byte, modelID, profileArn, origin string, isAgentic, isChatOnly bool, sourceFormat sdktranslator.Format, headers http.Header) ([]byte, bool) {
	switch sourceFormat.String() {
	case "openai":
		log.Debugf("kiro: using OpenAI payload builder for source format: %s", sourceFormat.String())
		return kiroopenai.BuildKiroPayloadFromOpenAI(body, modelID, profileArn, origin, isAgentic, isChatOnly, headers, nil)
	case "kiro":
		// Body is already in Kiro format — pass through directly
		log.Debugf("kiro: body already in Kiro format, passing through directly")
		return sanitizeKiroPayload(body), false
	default:
		// Default to Claude format
		log.Debugf("kiro: using Claude payload builder for source format: %s", sourceFormat.String())
		return kiroclaude.BuildKiroPayload(body, modelID, profileArn, origin, isAgentic, isChatOnly, headers, nil)
	}
}

func sanitizeKiroPayload(body []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	if _, exists := payload["user"]; !exists {
		return body
	}
	delete(payload, "user")
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return sanitized
}

func kiroCredentials(auth *cliproxyauth.Auth) (accessToken, profileArn string) {
	if auth == nil {
		return "", ""
	}

	// Try Metadata first (wrapper format)
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profile_arn"].(string); ok {
			profileArn = arn
		}
	}

	// Try Attributes
	if accessToken == "" && auth.Attributes != nil {
		accessToken = auth.Attributes["access_token"]
		profileArn = auth.Attributes["profile_arn"]
	}

	// Try direct fields from flat JSON format (new AWS Builder ID format)
	if accessToken == "" && auth.Metadata != nil {
		if token, ok := auth.Metadata["accessToken"].(string); ok {
			accessToken = token
		}
		if arn, ok := auth.Metadata["profileArn"].(string); ok {
			profileArn = arn
		}
	}

	return accessToken, profileArn
}

// findRealThinkingEndTag finds the real </thinking> end tag, skipping false positives.
// Returns -1 if no real end tag is found.
//
// Real </thinking> tags from Kiro API have specific characteristics:
// - Usually preceded by newline (.\n</thinking>)
// - Usually followed by newline (\n\n)
// - Not inside code blocks or inline code
//
// False positives (discussion text) have characteristics:
// - In the middle of a sentence
// - Preceded by discussion words like "标签", "tag", "returns"
// - Inside code blocks or inline code
//
// Parameters:
// - content: the content to search in
// - alreadyInCodeBlock: whether we're already inside a code block from previous chunks
// - alreadyInInlineCode: whether we're already inside inline code from previous chunks

// determineAgenticMode determines if the model is an agentic or chat-only variant.
// Returns (isAgentic, isChatOnly) based on model name suffixes.
func determineAgenticMode(model string) (isAgentic, isChatOnly bool) {
	isAgentic = strings.HasSuffix(model, "-agentic")
	isChatOnly = strings.HasSuffix(model, "-chat")
	return isAgentic, isChatOnly
}

func getMetadataString(metadata map[string]any, keys ...string) string {
	if metadata == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// getEffectiveProfileArn determines if profileArn should be included based on auth method.
// profileArn is only needed for social auth (Google OAuth), not for AWS SSO OIDC (Builder ID/IDC).
//
// Detection logic (matching kiro-openai-gateway):
// 1. Check auth_method field: "builder-id" or "idc"
// 2. Check auth_type field: "aws_sso_oidc" (from kiro-cli tokens)
// 3. Check for client_id + client_secret presence (AWS SSO OIDC signature)

// getEffectiveProfileArnWithWarning determines if profileArn should be included based on auth method,
// and logs a warning if profileArn is missing for non-builder-id auth.
// This consolidates the auth_method check that was previously done separately.
//
// AWS SSO OIDC (Builder ID/IDC) users don't need profileArn - sending it causes 403 errors.
// Only Kiro Desktop (social auth like Google/GitHub) users need profileArn.
//
// Detection logic (matching kiro-openai-gateway):
// 1. Check auth_method field: "builder-id" or "idc"
// 2. Check auth_type field: "aws_sso_oidc" (from kiro-cli tokens)
// 3. Check for client_id + client_secret presence (AWS SSO OIDC signature)
func getEffectiveProfileArnWithWarning(auth *cliproxyauth.Auth, profileArn string) string {
	if auth != nil && auth.Metadata != nil {
		// Check 1: auth_method field (from CLIProxyAPI tokens)
		authMethod := strings.ToLower(getMetadataString(auth.Metadata, "auth_method", "authMethod"))
		if authMethod == "builder-id" || authMethod == "idc" {
			return "" // AWS SSO OIDC - don't include profileArn
		}
		// Check 2: auth_type field (from kiro-cli tokens)
		if authType, ok := auth.Metadata["auth_type"].(string); ok && authType == "aws_sso_oidc" {
			return "" // AWS SSO OIDC - don't include profileArn
		}
		// Check 3: client_id + client_secret presence (AWS SSO OIDC signature, like kiro-openai-gateway)
		clientID := getMetadataString(auth.Metadata, "client_id", "clientId")
		clientSecret := getMetadataString(auth.Metadata, "client_secret", "clientSecret")
		if clientID != "" && clientSecret != "" {
			return "" // AWS SSO OIDC - don't include profileArn
		}
	}
	// For social auth (Kiro Desktop), profileArn is required
	if profileArn == "" {
		log.Warnf("kiro: profile ARN not found in auth, API calls may fail")
	}
	return profileArn
}

// mapModelToKiro maps external model names to Kiro model IDs.
// Supports both Kiro and Amazon Q prefixes since they use the same API.
// Agentic variants (-agentic suffix) map to the same backend model IDs.
func (e *KiroExecutor) mapModelToKiro(model string) string {
	modelMap := map[string]string{
		// Amazon Q format (amazonq- prefix) - same API as Kiro
		"amazonq-auto":                       "auto",
		"amazonq-claude-opus-4-6":            "claude-opus-4.6",
		"amazonq-claude-sonnet-4-6":          "claude-sonnet-4.6",
		"amazonq-claude-opus-4-5":            "claude-opus-4.5",
		"amazonq-claude-sonnet-4-5":          "claude-sonnet-4.5",
		"amazonq-claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"amazonq-claude-sonnet-4":            "claude-sonnet-4",
		"amazonq-claude-sonnet-4-20250514":   "claude-sonnet-4",
		"amazonq-claude-haiku-4-5":           "claude-haiku-4.5",
		// Kiro format (kiro- prefix) - valid model names that should be preserved
		"kiro-claude-opus-4-6":            "claude-opus-4.6",
		"kiro-claude-sonnet-4-6":          "claude-sonnet-4.6",
		"kiro-claude-opus-4-5":            "claude-opus-4.5",
		"kiro-claude-sonnet-4-5":          "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"kiro-claude-sonnet-4":            "claude-sonnet-4",
		"kiro-claude-sonnet-4-20250514":   "claude-sonnet-4",
		"kiro-claude-haiku-4-5":           "claude-haiku-4.5",
		"kiro-auto":                       "auto",
		// Native format (no prefix) - used by Kiro IDE directly
		"claude-opus-4-6":            "claude-opus-4.6",
		"claude-opus-4.6":            "claude-opus-4.6",
		"claude-sonnet-4-6":          "claude-sonnet-4.6",
		"claude-sonnet-4.6":          "claude-sonnet-4.6",
		"claude-opus-4-5":            "claude-opus-4.5",
		"claude-opus-4.5":            "claude-opus-4.5",
		"claude-haiku-4-5":           "claude-haiku-4.5",
		"claude-haiku-4.5":           "claude-haiku-4.5",
		"claude-sonnet-4-5":          "claude-sonnet-4.5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"claude-sonnet-4.5":          "claude-sonnet-4.5",
		"claude-sonnet-4":            "claude-sonnet-4",
		"claude-sonnet-4-20250514":   "claude-sonnet-4",
		"auto":                       "auto",
		// Agentic variants (same backend model IDs, but with special system prompt)
		"claude-opus-4.6-agentic":        "claude-opus-4.6",
		"claude-sonnet-4.6-agentic":      "claude-sonnet-4.6",
		"claude-opus-4.5-agentic":        "claude-opus-4.5",
		"claude-sonnet-4.5-agentic":      "claude-sonnet-4.5",
		"claude-sonnet-4-agentic":        "claude-sonnet-4",
		"claude-haiku-4.5-agentic":       "claude-haiku-4.5",
		"kiro-claude-opus-4-6-agentic":   "claude-opus-4.6",
		"kiro-claude-sonnet-4-6-agentic": "claude-sonnet-4.6",
		"kiro-claude-opus-4-5-agentic":   "claude-opus-4.5",
		"kiro-claude-sonnet-4-5-agentic": "claude-sonnet-4.5",
		"kiro-claude-sonnet-4-agentic":   "claude-sonnet-4",
		"kiro-claude-haiku-4-5-agentic":  "claude-haiku-4.5",
	}
	if kiroID, ok := modelMap[model]; ok {
		return kiroID
	}

	// Smart fallback: try to infer model type from name patterns
	modelLower := strings.ToLower(model)

	// Check for Haiku variants
	if strings.Contains(modelLower, "haiku") {
		log.Debug("kiro: unknown haiku variant, mapping to claude-haiku-4.5")
		return "claude-haiku-4.5"
	}

	// Check for Sonnet variants
	if strings.Contains(modelLower, "sonnet") {
		// Check for specific version patterns
		if strings.Contains(modelLower, "3-7") || strings.Contains(modelLower, "3.7") {
			log.Debug("kiro: unknown sonnet 3.7 variant, mapping to claude-3-7-sonnet-20250219")
			return "claude-3-7-sonnet-20250219"
		}
		if strings.Contains(modelLower, "4-6") || strings.Contains(modelLower, "4.6") {
			log.Debug("kiro: unknown sonnet 4.6 variant, mapping to claude-sonnet-4.6")
			return "claude-sonnet-4.6"
		}
		if strings.Contains(modelLower, "4-5") || strings.Contains(modelLower, "4.5") {
			log.Debug("kiro: unknown Sonnet 4.5 model, mapping to claude-sonnet-4.5")
			return "claude-sonnet-4.5"
		}
	}

	// Check for Opus variants
	if strings.Contains(modelLower, "opus") {
		if strings.Contains(modelLower, "4-6") || strings.Contains(modelLower, "4.6") {
			log.Debug("kiro: unknown Opus 4.6 model, mapping to claude-opus-4.6")
			return "claude-opus-4.6"
		}
		log.Debug("kiro: unknown opus variant, mapping to claude-opus-4.5")
		return "claude-opus-4.5"
	}

	// Final fallback to Sonnet 4.5 (most commonly used model)
	log.Warn("kiro: unknown model variant, falling back to claude-sonnet-4.5")
	return "claude-sonnet-4.5"
}

func kiroModelFingerprint(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:8])
}
