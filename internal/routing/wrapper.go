package routing

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing/ctxkeys"
	"github.com/sirupsen/logrus"
)

// ProxyFunc is the function type for proxying requests.
type ProxyFunc func(c *gin.Context)

// ModelRoutingWrapper wraps HTTP handlers with unified model routing logic.
// It replaces the FallbackHandler logic with a Router-based approach.
type ModelRoutingWrapper struct {
	router    *Router
	extractor ModelExtractor
	rewriter  ModelRewriter
	proxyFunc ProxyFunc
	logger    *logrus.Logger
}

// NewModelRoutingWrapper creates a new ModelRoutingWrapper with the given dependencies.
// If extractor is nil, a DefaultModelExtractor is used.
// If rewriter is nil, a DefaultModelRewriter is used.
// proxyFunc is called for AMP_CREDITS route type; if nil, the handler will be called instead.
func NewModelRoutingWrapper(router *Router, extractor ModelExtractor, rewriter ModelRewriter, proxyFunc ProxyFunc) *ModelRoutingWrapper {
	if extractor == nil {
		extractor = NewModelExtractor()
	}
	if rewriter == nil {
		rewriter = NewModelRewriter()
	}
	return &ModelRoutingWrapper{
		router:    router,
		extractor: extractor,
		rewriter:  rewriter,
		proxyFunc: proxyFunc,
		logger:    logrus.New(),
	}
}

// SetLogger sets the logger for the wrapper.
func (w *ModelRoutingWrapper) SetLogger(logger *logrus.Logger) {
	w.logger = logger
}

// Wrap wraps a gin.HandlerFunc with model routing logic.
// The returned handler will:
// 1. Extract the model from the request
// 2. Get a routing decision from the Router
// 3. Handle the request according to the decision type (LOCAL_PROVIDER, MODEL_MAPPING, AMP_CREDITS)
func (w *ModelRoutingWrapper) Wrap(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read request body
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			w.logger.Errorf("routing wrapper: failed to read request body: %v", err)
			handler(c)
			return
		}

		// Extract model from request
		ginParams := map[string]string{
			"action": c.Param("action"),
			"path":   c.Param("path"),
		}
		modelName, err := w.extractor.Extract(bodyBytes, ginParams)
		if err != nil {
			w.logger.Warnf("routing wrapper: failed to extract model: %v", err)
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			handler(c)
			return
		}

		if modelName == "" {
			// No model found, proceed with original handler
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			handler(c)
			return
		}

		// Get routing decision
		req := RoutingRequest{
			RequestedModel:      modelName,
			PreferLocalProvider: true,
			ForceModelMapping:   false, // TODO: Get from config
		}
		decision := w.router.ResolveV2(req)

		// Store decision in context for downstream handlers
		c.Set(string(ctxkeys.RoutingDecision), decision)

		// Handle based on route type
		switch decision.RouteType {
		case RouteTypeLocalProvider:
			w.handleLocalProvider(c, handler, bodyBytes, decision)
		case RouteTypeModelMapping:
			w.handleModelMapping(c, handler, bodyBytes, decision)
		case RouteTypeAmpCredits:
			w.handleAmpCredits(c, handler, bodyBytes)
		default:
			// No provider available
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			handler(c)
		}
	}
}

// handleLocalProvider handles the LOCAL_PROVIDER route type.
func (w *ModelRoutingWrapper) handleLocalProvider(c *gin.Context, handler gin.HandlerFunc, bodyBytes []byte, decision *RoutingDecision) {
	// Filter Anthropic-Beta header for local provider
	filterAnthropicBetaHeader(c)

	// Restore body with original content
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Call handler
	handler(c)
}

// handleModelMapping handles the MODEL_MAPPING route type.
func (w *ModelRoutingWrapper) handleModelMapping(c *gin.Context, handler gin.HandlerFunc, bodyBytes []byte, decision *RoutingDecision) {
	// Rewrite request body with mapped model
	rewrittenBody, err := w.rewriter.RewriteRequestBody(bodyBytes, decision.ResolvedModel)
	if err != nil {
		w.logger.Warnf("routing wrapper: failed to rewrite request body: %v", err)
		rewrittenBody = bodyBytes
	}
	_ = rewrittenBody

	// Store mapped model in context
	c.Set(string(ctxkeys.MappedModel), decision.ResolvedModel)

	// Store fallback models in context if present
	if len(decision.FallbackModels) > 0 {
		c.Set(string(ctxkeys.FallbackModels), decision.FallbackModels)
	}

	// Filter Anthropic-Beta header for local provider
	filterAnthropicBetaHeader(c)

	// Restore body with rewritten content
	c.Request.Body = io.NopCloser(bytes.NewReader(rewrittenBody))

	// Wrap response writer to rewrite model back
	wrappedWriter, cleanup := w.rewriter.WrapResponseWriter(c.Writer, decision.ResolvedModel, decision.ResolvedModel)
	c.Writer = &ginResponseWriterAdapter{ResponseWriter: wrappedWriter, original: c.Writer}

	// Call handler
	handler(c)

	// Cleanup (flush response rewriting)
	cleanup()
}

// handleAmpCredits handles the AMP_CREDITS route type.
// It calls the proxy function directly if available, otherwise passes to handler.
// Does NOT filter headers or rewrite body - proxy handles everything.
func (w *ModelRoutingWrapper) handleAmpCredits(c *gin.Context, handler gin.HandlerFunc, bodyBytes []byte) {
	// Restore body with original content (no rewriting for proxy)
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Call proxy function if available, otherwise fall back to handler
	if w.proxyFunc != nil {
		w.proxyFunc(c)
	} else {
		handler(c)
	}
}

// filterAnthropicBetaHeader filters Anthropic-Beta header for local providers.
func filterAnthropicBetaHeader(c *gin.Context) {
	if betaHeader := c.Request.Header.Get("Anthropic-Beta"); betaHeader != "" {
		filtered := filterBetaFeatures(betaHeader, "context-1m-2025-08-07")
		if filtered != "" {
			c.Request.Header.Set("Anthropic-Beta", filtered)
		} else {
			c.Request.Header.Del("Anthropic-Beta")
		}
	}
}

// filterBetaFeatures removes specified beta features from the header.
func filterBetaFeatures(betaHeader, featureToRemove string) string {
	// Simple implementation - can be enhanced
	if betaHeader == featureToRemove {
		return ""
	}
	return betaHeader
}

// ginResponseWriterAdapter adapts http.ResponseWriter to gin.ResponseWriter.
type ginResponseWriterAdapter struct {
	http.ResponseWriter
	original gin.ResponseWriter
}

func (a *ginResponseWriterAdapter) WriteHeader(code int) {
	a.ResponseWriter.WriteHeader(code)
}

func (a *ginResponseWriterAdapter) Write(data []byte) (int, error) {
	return a.ResponseWriter.Write(data)
}

func (a *ginResponseWriterAdapter) Header() http.Header {
	return a.ResponseWriter.Header()
}

// CloseNotify implements http.CloseNotifier.
func (a *ginResponseWriterAdapter) CloseNotify() <-chan bool {
	if notifier, ok := a.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	return a.original.CloseNotify()
}

// Flush implements http.Flusher.
func (a *ginResponseWriterAdapter) Flush() {
	if flusher, ok := a.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker.
func (a *ginResponseWriterAdapter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := a.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return a.original.Hijack()
}

// Status returns the HTTP status code.
func (a *ginResponseWriterAdapter) Status() int {
	return a.original.Status()
}

// Size returns the number of bytes already written into the response http body.
func (a *ginResponseWriterAdapter) Size() int {
	return a.original.Size()
}

// Written returns whether or not the response for this context has been written.
func (a *ginResponseWriterAdapter) Written() bool {
	return a.original.Written()
}

// WriteHeaderNow forces WriteHeader to be called.
func (a *ginResponseWriterAdapter) WriteHeaderNow() {
	a.original.WriteHeaderNow()
}

// WriteString writes the given string into the response body.
func (a *ginResponseWriterAdapter) WriteString(s string) (int, error) {
	return a.Write([]byte(s))
}

// Pusher returns the http.Pusher for server push.
func (a *ginResponseWriterAdapter) Pusher() http.Pusher {
	if pusher, ok := a.ResponseWriter.(http.Pusher); ok {
		return pusher
	}
	return nil
}
