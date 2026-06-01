package opencode

import (
	"bytes"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MappedModelContextKey is the Gin context key for passing mapped model names to
// downstream handlers that read it (e.g. the Gemini bridge in the merged routes).
const MappedModelContextKey = "opencode_mapped_model"

// MappingHandler wraps a standard SDK handler with OpenCode model-mapping logic.
// Unlike Amp's FallbackHandler, it has no reverse-proxy fallback: OpenCode users
// point a custom provider baseURL at the proxy deliberately, so when no provider
// and no mapping resolve, the wrapped handler returns its normal error response.
type MappingHandler struct {
	modelMapper        ModelMapper
	forceModelMappings func() bool
}

// NewMappingHandler creates a mapping handler. forceModelMappings may be nil
// (treated as always-false).
func NewMappingHandler(mapper ModelMapper, forceModelMappings func() bool) *MappingHandler {
	if forceModelMappings == nil {
		forceModelMappings = func() bool { return false }
	}
	return &MappingHandler{
		modelMapper:        mapper,
		forceModelMappings: forceModelMappings,
	}
}

// Wrap wraps a gin.HandlerFunc, rewriting the request model when a mapping applies.
func (h *MappingHandler) Wrap(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			log.Errorf("opencode mapping: failed to read request body: %v", err)
			handler(c)
			return
		}
		// Restore the body for the handler regardless of mapping outcome.
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		modelName := extractModelFromRequest(bodyBytes, c)
		if modelName == "" || h.modelMapper == nil {
			handler(c)
			return
		}

		suffixResult := thinking.ParseSuffix(modelName)
		normalizedModel := suffixResult.ModelName

		resolveMappedModel := func() string {
			mapped := h.modelMapper.MapModel(modelName)
			if mapped == "" {
				mapped = h.modelMapper.MapModel(normalizedModel)
			}
			return strings.TrimSpace(mapped)
		}

		mappedModel := ""
		if h.forceModelMappings() {
			// Force mode: mappings take precedence over local providers.
			mappedModel = resolveMappedModel()
		} else if len(util.GetProviderName(normalizedModel)) == 0 {
			// Default mode: only map when no local provider serves the model.
			mappedModel = resolveMappedModel()
		}

		if mappedModel != "" && mappedModel != modelName {
			if rewritten, ok := rewriteModelInRequest(bodyBytes, mappedModel); ok {
				bodyBytes = rewritten
				c.Set(MappedModelContextKey, mappedModel)
				log.Debugf("opencode model mapping: %s -> %s", modelName, mappedModel)
			}
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		handler(c)
	}
}

// rewriteModelInRequest replaces the model name in a JSON request body. It returns
// the original body and false when the body has no top-level "model" field (e.g.
// Gemini requests carry the model in the URL path).
func rewriteModelInRequest(body []byte, newModel string) ([]byte, bool) {
	if !gjson.GetBytes(body, "model").Exists() {
		return body, false
	}
	result, err := sjson.SetBytes(body, "model", newModel)
	if err != nil {
		log.Warnf("opencode model mapping: failed to rewrite model in request body: %v", err)
		return body, false
	}
	return result, true
}

// extractModelFromRequest attempts to extract the model name from various request
// formats: the JSON body (OpenAI/Claude), the Gemini :action route parameter, or a
// /models/{model}:method path segment.
func extractModelFromRequest(body []byte, c *gin.Context) string {
	if result := gjson.GetBytes(body, "model"); result.Exists() && result.Type == gjson.String {
		return result.String()
	}

	// Gemini native: /models/{model}:method -> *action parameter.
	if action := c.Param("action"); action != "" {
		if parts := strings.Split(strings.TrimPrefix(action, "/"), ":"); len(parts) > 0 && parts[0] != "" {
			return strings.TrimPrefix(parts[0], "/")
		}
	}
	return ""
}
