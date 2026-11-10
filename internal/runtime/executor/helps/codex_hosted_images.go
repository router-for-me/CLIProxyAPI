package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const codexResponsesLiteHeaderName = "X-OpenAI-Internal-Codex-Responses-Lite"

// CodexModelUsesResponsesLite identifies models whose native Codex route uses Responses Lite.
func CodexModelUsesResponsesLite(model string) bool {
	slug := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	if idx := strings.LastIndex(slug, "/"); idx >= 0 {
		slug = strings.TrimSpace(slug[idx+1:])
	}
	return slug != "" && (strings.HasPrefix(slug, "gpt-6") || registry.CodexClientModelUsesResponsesLite(slug))
}

// CodexResponsesLiteRequestDoc reads the route hints without changing the payload.
func CodexResponsesLiteRequestDoc(doc *ojson.Doc, headers http.Header) bool {
	if strings.EqualFold(strings.TrimSpace(headers.Get(codexResponsesLiteHeaderName)), "true") {
		return true
	}
	if doc == nil {
		return false
	}
	value := doc.Get("client_metadata.ws_request_header_x_openai_internal_codex_responses_lite")
	return value.Type == ojson.True || (value.Type == ojson.String && strings.EqualFold(strings.TrimSpace(value.String()), "true"))
}

// CodexResponsesLiteRequestForAuthDoc reports whether the request uses the
// Codex Responses-Lite protocol after resolving client_metadata, client
// headers, credential header: attributes, and models.json override_header.
func CodexResponsesLiteRequestForAuthDoc(doc *ojson.Doc, modelName string, auth *cliproxyauth.Auth, headers http.Header) bool {
	if CodexResponsesLiteRequestDoc(doc, nil) {
		return true
	}
	var effective string
	if headers != nil {
		effective = strings.TrimSpace(headers.Get(codexResponsesLiteHeaderName))
	}
	if auth != nil && len(auth.Attributes) > 0 {
		resolved := (&http.Request{Header: http.Header{}}).WithContext(context.Background())
		util.ApplyCustomHeadersFromAttrs(resolved, auth.Attributes, headers)
		if opLite := strings.TrimSpace(resolved.Header.Get(codexResponsesLiteHeaderName)); opLite != "" {
			effective = opLite
		}
	}
	if overrides := registry.ModelOverrideHeaders(modelName); len(overrides) > 0 {
		for key, value := range overrides {
			if strings.EqualFold(strings.TrimSpace(key), codexResponsesLiteHeaderName) {
				effective = strings.TrimSpace(value)
			}
		}
	}
	return strings.EqualFold(effective, "true")
}

// CodexRequestHasReasoningForHostedImageDoc reports whether the request carries
// a reasoning configuration (top-level reasoning.effort, configuration_update
// input item, or model thinking suffix). Upstream Codex /responses binds hosted
// image_generation to gpt-image-2-codex, which rejects complexity whenever
// reasoning effort is present on Responses-Lite models (#7270), whereas
// standard Responses requests without reasoning effort succeed (#7384).
func CodexRequestHasReasoningForHostedImageDoc(doc *ojson.Doc, model string) bool {
	if thinking.HasThinkingSuffix(model) {
		return true
	}
	if doc == nil {
		return false
	}
	if thinking.HasThinkingSuffix(doc.Get("model").String()) {
		return true
	}
	if reasoning := doc.Get("reasoning"); reasoning.Exists() && reasoning.Type != ojson.Null {
		if reasoning.IsObject() {
			if effort := reasoning.Get("effort"); effort.Exists() && effort.Type != ojson.Null && strings.TrimSpace(effort.String()) != "" {
				return true
			}
		} else if strings.TrimSpace(reasoning.String()) != "" {
			return true
		}
	}
	if input := doc.Get("input"); input.IsArray() {
		for _, item := range input.Array() {
			if strings.TrimSpace(item.Get("type").String()) != "configuration_update" {
				continue
			}
			if effort := item.Get("reasoning.effort"); effort.Exists() && effort.Type != ojson.Null && strings.TrimSpace(effort.String()) != "" {
				return true
			}
		}
	}
	return false
}

// ValidateCodexHostedImageGenerationDoc checks the final, configured Responses payload
// after all outbound headers, including credential and model overrides, are resolved.
// Native Codex Responses Lite cannot serve forced hosted images, while API-key, SIWC,
// explicit passthrough, and standalone Images API requests retain their own contracts.
func ValidateCodexHostedImageGenerationDoc(doc *ojson.Doc, upstreamHeaders http.Header, cfg *config.Config, nativeCodex bool) error {
	if !nativeCodex || doc == nil || (cfg != nil && cfg.DisableImageGeneration == config.DisableImageGenerationPassthrough) {
		return nil
	}
	model := doc.Get("model").String()
	if !CodexModelUsesResponsesLite(model) && !CodexResponsesLiteRequestDoc(doc, upstreamHeaders) {
		return nil
	}
	if !codexRequestForcesImageGenerationDoc(doc) {
		return nil
	}
	return cliproxyauth.NewRequestScopedError("Hosted image_generation cannot be forced on this Codex Responses-Lite route. Use /v1/images/generations or /v1/images/edits instead.", http.StatusBadRequest)
}

func codexCollectDeclaredToolsDoc(doc *ojson.Doc) []ojson.Result {
	var tools []ojson.Result
	if declared := doc.Get("tools"); declared.IsArray() {
		tools = append(tools, declared.Array()...)
	}
	if input := doc.Get("input"); input.IsArray() {
		for _, item := range input.Array() {
			if strings.TrimSpace(item.Get("type").String()) == "additional_tools" {
				if declared := item.Get("tools"); declared.IsArray() {
					tools = append(tools, declared.Array()...)
				}
			}
		}
	}
	return tools
}

func codexRequestForcesImageGenerationDoc(doc *ojson.Doc) bool {
	tools := codexCollectDeclaredToolsDoc(doc)
	hasImageGen := false
	allImageGen := len(tools) > 0
	for _, tool := range tools {
		if strings.TrimSpace(tool.Get("type").String()) == "image_generation" {
			hasImageGen = true
		} else {
			allImageGen = false
		}
	}
	if !hasImageGen {
		return false
	}
	choice := doc.Get("tool_choice")
	if choice.Type == ojson.String {
		return strings.EqualFold(strings.TrimSpace(choice.String()), "required") && allImageGen
	}
	if !choice.IsObject() {
		return false
	}
	switch strings.TrimSpace(choice.Get("type").String()) {
	case "image_generation":
		return true
	case "allowed_tools":
		if !strings.EqualFold(strings.TrimSpace(choice.Get("mode").String()), "required") {
			return false
		}
		allowed := choice.Get("tools")
		if !allowed.IsArray() || len(allowed.Array()) == 0 {
			return false
		}
		for _, tool := range allowed.Array() {
			if strings.TrimSpace(tool.Get("type").String()) != "image_generation" {
				return false
			}
		}
		return true
	default:
		return false
	}
}
