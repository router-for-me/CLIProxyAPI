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
			// OpenAI/Claude requests carry the model in the JSON body; Gemini native
			// requests carry it in the :action URL path param (model:method). Apply
			// the mapping to whichever location actually holds the model.
			if rewritten, ok := rewriteModelInRequest(bodyBytes, mappedModel); ok {
				bodyBytes = rewritten
			} else {
				rewriteGeminiActionModel(c, mappedModel)
			}
			log.Debugf("opencode model mapping: %s -> %s", modelName, mappedModel)
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

// rewriteGeminiActionModel substitutes the model in the Gemini native :action path
// param (formatted as "model:method"), since the shared GeminiHandler resolves the
// model from the URL path rather than the request body. It preserves the method and
// any leading slash from the catch-all param value.
func rewriteGeminiActionModel(c *gin.Context, mappedModel string) {
	action := c.Param("action")
	if action == "" {
		return
	}
	leading := ""
	trimmed := action
	if strings.HasPrefix(trimmed, "/") {
		leading = "/"
		trimmed = trimmed[1:]
	}
	colon := strings.Index(trimmed, ":")
	if colon < 0 {
		return
	}
	setParam(c, "action", leading+mappedModel+trimmed[colon:])
}

// setParam replaces the value of an existing gin path param, or appends it if absent,
// so a subsequent ShouldBindUri/Param read observes the mapped value.
func setParam(c *gin.Context, key, value string) {
	for i := range c.Params {
		if c.Params[i].Key == key {
			c.Params[i].Value = value
			return
		}
	}
	c.Params = append(c.Params, gin.Param{Key: key, Value: value})
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
