package helps

import (
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

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
	if strings.EqualFold(strings.TrimSpace(headers.Get("X-OpenAI-Internal-Codex-Responses-Lite")), "true") {
		return true
	}
	if doc == nil {
		return false
	}
	value := doc.Get("client_metadata.ws_request_header_x_openai_internal_codex_responses_lite")
	return value.Type == ojson.True || (value.Type == ojson.String && strings.EqualFold(strings.TrimSpace(value.String()), "true"))
}

// ValidateCodexHostedImageGenerationDoc checks the final, configured Responses payload
// after all outbound headers, including credential and model overrides, are resolved.
// Native Codex Responses Lite cannot serve forced hosted images. API-key, SIWC,
// explicit passthrough, and standalone Images API requests retain their own contracts.
func ValidateCodexHostedImageGenerationDoc(doc *ojson.Doc, upstreamHeaders http.Header, cfg *config.Config, nativeCodex bool) error {
	if !nativeCodex || doc == nil || (cfg != nil && cfg.DisableImageGeneration == config.DisableImageGenerationPassthrough) {
		return nil
	}
	if !CodexModelUsesResponsesLite(doc.Get("model").String()) && !CodexResponsesLiteRequestDoc(doc, upstreamHeaders) {
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
