// Package handlers provides core API handler functionality for the CLI Proxy API server.
// It includes common types, client management, load balancing, and error handling
// shared across all API endpoint handlers (OpenAI, Claude, Gemini).
package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"golang.org/x/net/context"
)

// ErrorResponse represents a standard error response format for the API.
// It contains a single ErrorDetail field.
type ErrorResponse struct {
	// Error contains detailed information about the error that occurred.
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides specific information about an error that occurred.
// It includes a human-readable message, an error type, and an optional error code.
type ErrorDetail struct {
	// Message is a human-readable message providing more details about the error.
	Message string `json:"message"`

	// Type is the category of error that occurred (e.g., "invalid_request_error").
	Type string `json:"type"`

	// Code is a short code identifying the error, if applicable.
	Code string `json:"code,omitempty"`
}

// BaseAPIHandler contains the handlers for API endpoints.
// It holds a pool of clients to interact with the backend service and manages
// load balancing, client selection, and configuration.
type BaseAPIHandler struct {
	// AuthManager manages auth lifecycle and execution in the new architecture.
	AuthManager *coreauth.Manager

	// Cfg holds the current application configuration.
	Cfg *config.SDKConfig

	// OpenAICompatProviders is a list of provider names for OpenAI compatibility.
	OpenAICompatProviders []string
}

// NewBaseAPIHandlers creates a new API handlers instance.
// It takes a slice of clients and configuration as input.
//
// Parameters:
//   - cliClients: A slice of AI service clients
//   - cfg: The application configuration
//
// Returns:
//   - *BaseAPIHandler: A new API handlers instance
func NewBaseAPIHandlers(cfg *config.SDKConfig, authManager *coreauth.Manager, openAICompatProviders []string) *BaseAPIHandler {
	return &BaseAPIHandler{
		Cfg:                   cfg,
		AuthManager:           authManager,
		OpenAICompatProviders: openAICompatProviders,
	}
}

// UpdateClients updates the handlers' client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (h *BaseAPIHandler) UpdateClients(cfg *config.SDKConfig) { h.Cfg = cfg }

// GetAlt extracts the 'alt' parameter from the request query string.
// It checks both 'alt' and '$alt' parameters and returns the appropriate value.
//
// Parameters:
//   - c: The Gin context containing the HTTP request
//
// Returns:
//   - string: The alt parameter value, or empty string if it's "sse"
func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	var alt string
	var hasAlt bool
	alt, hasAlt = c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

// GetContextWithCancel creates a new context with cancellation capabilities.
// It embeds the Gin context and the API handler into the new context for later use.
// The returned cancel function also handles logging the API response if request logging is enabled.
//
// Parameters:
//   - handler: The API handler associated with the request.
//   - c: The Gin context of the current request.
//   - ctx: The parent context.
//
// Returns:
//   - context.Context: The new context with cancellation and embedded values.
//   - APIHandlerCancelFunc: A function to cancel the context and log the response.
func (h *BaseAPIHandler) GetContextWithCancel(handler interfaces.APIHandler, c *gin.Context, ctx context.Context) (context.Context, APIHandlerCancelFunc) {
	newCtx, cancel := context.WithCancel(ctx)
	newCtx = context.WithValue(newCtx, "gin", c)
	newCtx = context.WithValue(newCtx, "handler", handler)
	return newCtx, func(params ...interface{}) {
		if h.Cfg.RequestLog {
			if len(params) == 1 {
				data := params[0]
				switch data.(type) {
				case []byte:
					appendAPIResponse(c, data.([]byte))
				case error:
					appendAPIResponse(c, []byte(data.(error).Error()))
				case string:
					appendAPIResponse(c, []byte(data.(string)))
				case bool:
				case nil:
				}
			}
		}

		cancel()
	}
}

// appendAPIResponse preserves any previously captured API response and appends new data.
func appendAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}

	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}

	c.Set("API_RESPONSE", bytes.Clone(data))
}

// ExecuteWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: cloneBytes(rawJSON),
	}
	if cloned := cloneMetadata(metadata); cloned != nil {
		req.Metadata = cloned
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: cloneBytes(rawJSON),
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	if cloned := cloneMetadata(metadata); cloned != nil {
		opts.Metadata = cloned
	}
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err != nil {
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	return cloneBytes(resp.Payload), nil
}

// ExecuteCountWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: cloneBytes(rawJSON),
	}
	if cloned := cloneMetadata(metadata); cloned != nil {
		req.Metadata = cloned
	}
	opts := coreexecutor.Options{
		Stream:          false,
		Alt:             alt,
		OriginalRequest: cloneBytes(rawJSON),
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	if cloned := cloneMetadata(metadata); cloned != nil {
		opts.Metadata = cloned
	}
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	return cloneBytes(resp.Payload), nil
}

// ExecuteStreamWithAuthManager executes a streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, errChan
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: cloneBytes(rawJSON),
	}
	if cloned := cloneMetadata(metadata); cloned != nil {
		req.Metadata = cloned
	}
	opts := coreexecutor.Options{
		Stream:          true,
		Alt:             alt,
		OriginalRequest: cloneBytes(rawJSON),
		SourceFormat:    sdktranslator.FromString(handlerType),
	}
	if cloned := cloneMetadata(metadata); cloned != nil {
		opts.Metadata = cloned
	}
	chunks, err := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
	if err != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
		close(errChan)
		return nil, errChan
	}
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		for chunk := range chunks {
			if chunk.Err != nil {
				status := http.StatusInternalServerError
				if se, ok := chunk.Err.(interface{ StatusCode() int }); ok && se != nil {
					if code := se.StatusCode(); code > 0 {
						status = code
					}
				}
				var addon http.Header
				if he, ok := chunk.Err.(interface{ Headers() http.Header }); ok && he != nil {
					if hdr := he.Headers(); hdr != nil {
						addon = hdr.Clone()
					}
				}
				errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: chunk.Err, Addon: addon}
				return
			}
			if len(chunk.Payload) > 0 {
				dataChan <- cloneBytes(chunk.Payload)
			}
		}
	}()
	return dataChan, errChan
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, metadata map[string]any, err *interfaces.ErrorMessage) {
	// Resolve "auto" model to an actual available model first
	resolvedModelName := util.ResolveAutoModel(modelName)

	providerName, extractedModelName, isDynamic := h.parseDynamicModel(resolvedModelName)

	// First, normalize the model name to handle suffixes like "-thinking-128"
	// This needs to happen before determining the provider for non-dynamic models.
	normalizedModel, metadata = normalizeModelMetadata(resolvedModelName)

	if isDynamic {
		providers = []string{providerName}
		// For dynamic models, the extractedModelName is already normalized by parseDynamicModel
		// so we use it as the final normalizedModel.
		normalizedModel = extractedModelName
	} else {
		// For non-dynamic models, use the normalizedModel to get the provider name.
		providers = util.GetProviderName(normalizedModel)
	}

	if len(providers) == 0 {
		return nil, "", nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("unknown provider for model %s", modelName)}
	}

	// If it's a dynamic model, the normalizedModel was already set to extractedModelName.
	// If it's a non-dynamic model, normalizedModel was set by normalizeModelMetadata.
	// So, normalizedModel is already correctly set at this point.

	return providers, normalizedModel, metadata, nil
}

func (h *BaseAPIHandler) parseDynamicModel(modelName string) (providerName, model string, isDynamic bool) {
	var providerPart, modelPart string
	for _, sep := range []string{"://"} {
		if parts := strings.SplitN(modelName, sep, 2); len(parts) == 2 {
			providerPart = parts[0]
			modelPart = parts[1]
			break
		}
	}

	if providerPart == "" {
		return "", modelName, false
	}

	// Check if the provider is a configured openai-compatibility provider
	for _, pName := range h.OpenAICompatProviders {
		if pName == providerPart {
			return providerPart, modelPart, true
		}
	}

	return "", modelName, false
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func normalizeModelMetadata(modelName string) (string, map[string]any) {
	return util.NormalizeGeminiThinkingModel(modelName)
}

func cloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// WriteErrorResponse writes an error message to the response writer using the HTTP status embedded in the message.
func (h *BaseAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.Addon != nil {
		for key, values := range msg.Addon {
			if len(values) == 0 {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}
	c.Status(status)
	if msg != nil && msg.Error != nil {
		_, _ = c.Writer.Write([]byte(msg.Error.Error()))
	} else {
		_, _ = c.Writer.Write([]byte(http.StatusText(status)))
	}
}

func (h *BaseAPIHandler) LoggingAPIResponseError(ctx context.Context, err *interfaces.ErrorMessage) {
	if h.Cfg.RequestLog {
		if ginContext, ok := ctx.Value("gin").(*gin.Context); ok {
			if apiResponseErrors, isExist := ginContext.Get("API_RESPONSE_ERROR"); isExist {
				if slicesAPIResponseError, isOk := apiResponseErrors.([]*interfaces.ErrorMessage); isOk {
					slicesAPIResponseError = append(slicesAPIResponseError, err)
					ginContext.Set("API_RESPONSE_ERROR", slicesAPIResponseError)
				}
			} else {
				// Create new response data entry
				ginContext.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{err})
			}
		}
	}
}

// APIHandlerCancelFunc is a function type for canceling an API handler's context.
// It can optionally accept parameters, which are used for logging the response.
type APIHandlerCancelFunc func(params ...interface{})
