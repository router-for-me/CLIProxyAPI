package routing

import (
	"path"
	"strings"
)

// ClassifyRequest determines the request type based on endpoint path and model name.
// Uses path-based classification as primary signal, model family matching as secondary.
func ClassifyRequest(requestPath, model string) RequestType {
	if rt := classifyByPath(requestPath); rt != RequestTypeOther {
		return rt
	}

	if model != "" {
		if rt := classifyByModel(model); rt != RequestTypeOther {
			return rt
		}
	}

	return RequestTypeOther
}

// classifyByPath determines request type from the API endpoint path.
func classifyByPath(requestPath string) RequestType {
	normalized := path.Clean(requestPath)
	normalized = strings.TrimPrefix(normalized, "/")

	switch {
	case strings.HasSuffix(normalized, "/chat/completions"):
		return RequestTypeChat
	case strings.Contains(normalized, "/chat/completions"):
		return RequestTypeChat
	case normalized == "v1/chat/completions":
		return RequestTypeChat

	case strings.HasSuffix(normalized, "/completions") && !strings.Contains(normalized, "chat"):
		return RequestTypeCompletion
	case normalized == "v1/completions":
		return RequestTypeCompletion

	case strings.HasSuffix(normalized, "/embeddings"):
		return RequestTypeEmbedding
	case strings.Contains(normalized, "/embeddings"):
		return RequestTypeEmbedding
	case normalized == "v1/embeddings":
		return RequestTypeEmbedding

	case strings.Contains(normalized, "messages"):
		return RequestTypeChat

	default:
		return RequestTypeOther
	}
}

// classifyByModel determines request type from the model name using configured patterns.
func classifyByModel(model string) RequestType {
	cfg := GetConfig()
	if cfg == nil {
		return RequestTypeOther
	}

	model = strings.ToLower(model)

	for _, pattern := range cfg.ModelFamilies.Chat {
		if matchPattern(model, strings.ToLower(pattern)) {
			return RequestTypeChat
		}
	}

	for _, pattern := range cfg.ModelFamilies.Completion {
		if matchPattern(model, strings.ToLower(pattern)) {
			return RequestTypeCompletion
		}
	}

	for _, pattern := range cfg.ModelFamilies.Embedding {
		if matchPattern(model, strings.ToLower(pattern)) {
			return RequestTypeEmbedding
		}
	}

	return RequestTypeOther
}

// matchPattern performs simple glob-style matching.
// Supports * as a wildcard that matches any characters.
func matchPattern(s, pattern string) bool {
	if !strings.Contains(pattern, "*") {
		return s == pattern
	}

	parts := strings.Split(pattern, "*")
	if len(parts) == 0 {
		return true
	}

	if parts[0] != "" && !strings.HasPrefix(s, parts[0]) {
		return false
	}

	pos := len(parts[0])
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			continue
		}

		idx := strings.Index(s[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
	}

	if parts[len(parts)-1] != "" && !strings.HasSuffix(s, parts[len(parts)-1]) {
		return false
	}

	return true
}

// RequestContext carries classification data through the request pipeline.
type RequestContext struct {
	Type          RequestType
	Model         string
	Path          string
	ProfileID     string
	ProviderGroup *ProviderGroup
}

// NewRequestContext creates a context from request parameters.
func NewRequestContext(requestPath, model string) *RequestContext {
	ctx := &RequestContext{
		Path:  requestPath,
		Model: model,
		Type:  ClassifyRequest(requestPath, model),
	}

	cfg := GetConfig()
	if cfg != nil {
		if profile := cfg.GetActiveProfile(); profile != nil {
			ctx.ProfileID = profile.ID
		}
		ctx.ProviderGroup = cfg.ResolveProviderGroup(ctx.Type)
	}

	return ctx
}

// GetAccountIDs returns the account IDs from the resolved provider group.
func (ctx *RequestContext) GetAccountIDs() []string {
	if ctx.ProviderGroup == nil {
		return nil
	}
	return ctx.ProviderGroup.AccountIDs
}
