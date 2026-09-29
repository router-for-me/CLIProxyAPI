package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

type responsesWSRequestContextKey struct{}

type responsesWSRequestState struct {
	requestID string
	handle    func(*gin.Context) *types.NewAPIError
	apiError  *types.NewAPIError
}

// Each response.create runs the ordinary request middleware to completion,
// without routing another HTTP request or retaining a pooled Gin context.
var responsesWSRequestEngine = sync.OnceValue(func() *gin.Engine {
	engine := gin.New()
	engine.ForwardedByClientIP = false
	_ = engine.SetTrustedProxies(nil)
	engine.POST("/v1/responses", func(c *gin.Context) {
		state := c.Request.Context().Value(responsesWSRequestContextKey{}).(*responsesWSRequestState)
		c.Set(common.RequestIdKey, state.requestID)
		common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
		c.Next()
	}, middleware.BodyStorageCleanup(), middleware.TokenAuth(), middleware.ModelRequestRateLimit(), func(c *gin.Context) {
		state := c.Request.Context().Value(responsesWSRequestContextKey{}).(*responsesWSRequestState)
		state.apiError = state.handle(c)
		if state.apiError != nil {
			status := state.apiError.StatusCode
			if status < http.StatusBadRequest {
				status = http.StatusInternalServerError
			}
			c.Status(status)
		}
	})
	return engine
})

type responsesWSResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *responsesWSResponseWriter) Header() http.Header {
	return w.header
}

func (w *responsesWSResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *responsesWSResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(data)
}

func newResponsesWSRequestRunner(c *gin.Context) relay.ResponsesWSRequestRunner {
	// Capture credentials before channel selection or header overrides. Resolve
	// the peer once with the public router's trusted-proxy configuration.
	headers := c.Request.Header.Clone()
	for _, name := range []string{"Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol", "Content-Length", "Content-Encoding"} {
		headers.Del(name)
	}
	remoteAddr := net.JoinHostPort(c.ClientIP(), "0")
	return func(request *http.Request, requestID string, handle func(*gin.Context) *types.NewAPIError) *types.NewAPIError {
		state := &responsesWSRequestState{requestID: requestID, handle: handle}
		ctx := context.WithValue(request.Context(), responsesWSRequestContextKey{}, state)
		ctx = context.WithValue(ctx, common.RequestIdKey, requestID)
		request = request.Clone(ctx)
		request.Method = http.MethodPost
		request.URL.Path = "/v1/responses"
		request.URL.RawPath = ""
		request.Header = headers.Clone()
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = remoteAddr
		response := &responsesWSResponseWriter{header: make(http.Header)}
		responsesWSRequestEngine().ServeHTTP(response, request)
		if state.apiError != nil {
			return state.apiError
		}
		if response.status < http.StatusBadRequest {
			return nil
		}
		var body struct {
			Error *types.OpenAIError `json:"error"`
		}
		if common.Unmarshal(response.body.Bytes(), &body) == nil && body.Error != nil {
			return types.WithOpenAIError(*body.Error, response.status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		// The existing in-memory rate limiter returns a bare 429 response.
		return types.NewErrorWithStatusCode(errors.New(http.StatusText(response.status)), types.ErrorCodeInvalidRequest, response.status, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
}

func ResponsesWebSocket(c *gin.Context) {
	requestID := c.GetString(common.RequestIdKey)
	runner := newResponsesWSRequestRunner(c)
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	if apiError := relay.ResponsesWebSocketHelper(c, ws, runner); apiError != nil {
		logger.LogError(c, fmt.Sprintf("responses websocket relay error: %s", common.LocalLogPreview(apiError.Error())))
		apiError.SetMessage(common.MessageWithRequestId(apiError.Error(), requestID))
		helper.WssError(c, ws, apiError.ToOpenAIError())
	}
}
