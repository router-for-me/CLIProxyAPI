package cliproxy

import (
	"bytes"
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	modelListSourceOpenAI = "openai-models"
	modelListSourceCodex  = "codex-models"
)

type modelListResponseInterceptor interface {
	InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse
}

// modelListPluginMiddleware lets the existing ResponseInterceptor plugin hook
// post-process GET /v1/models responses. This keeps model policy in plugins
// while preserving the stock endpoint expected by Codex and other clients.
func modelListPluginMiddleware(host modelListResponseInterceptor) gin.HandlerFunc {
	return func(c *gin.Context) {
		if host == nil || c == nil || c.Request == nil || c.Request.URL == nil ||
			c.Request.Method != http.MethodGet || c.Request.URL.Path != "/v1/models" {
			c.Next()
			return
		}

		originalWriter := c.Writer
		bufferedWriter := &modelListBufferedWriter{ResponseWriter: originalWriter}
		c.Writer = bufferedWriter
		c.Next()
		c.Writer = originalWriter

		statusCode := bufferedWriter.Status()
		body := bytes.Clone(bufferedWriter.body.Bytes())
		responseHeaders := originalWriter.Header().Clone()

		// Authentication and handler errors are delivered unchanged. Frontend auth
		// remains responsible for allowing or rejecting access to the endpoint.
		if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices || len(body) == 0 {
			writeModelListResponse(originalWriter, statusCode, responseHeaders, body)
			return
		}

		metadata := map[string]any{
			"request_path": c.Request.URL.Path,
		}
		if accessProvider, exists := c.Get("accessProvider"); exists {
			if provider, ok := accessProvider.(string); ok && provider != "" {
				metadata["access_provider"] = provider
			}
		}
		if accessMetadata, exists := c.Get("accessMetadata"); exists && accessMetadata != nil {
			metadata["access_metadata"] = accessMetadata
		}

		sourceFormat := modelListSourceOpenAI
		if _, exists := c.Request.URL.Query()["client_version"]; exists {
			sourceFormat = modelListSourceCodex
			metadata["client_version"] = c.Query("client_version")
		}

		filtered := host.InterceptResponse(c.Request.Context(), pluginapi.ResponseInterceptRequest{
			SourceFormat:    sourceFormat,
			Stream:          false,
			RequestHeaders:  c.Request.Header.Clone(),
			ResponseHeaders: responseHeaders,
			Body:            body,
			StatusCode:      statusCode,
			Metadata:        metadata,
		})

		if filtered.Headers != nil {
			responseHeaders = filtered.Headers.Clone()
		}
		if len(filtered.Body) > 0 {
			body = bytes.Clone(filtered.Body)
		}
		writeModelListResponse(originalWriter, statusCode, responseHeaders, body)
	}
}

type modelListBufferedWriter struct {
	gin.ResponseWriter
	body       bytes.Buffer
	statusCode int
	size       int
	written    bool
}

func (w *modelListBufferedWriter) WriteHeader(code int) {
	if w == nil || w.written {
		return
	}
	w.statusCode = code
	w.written = true
}

func (w *modelListBufferedWriter) WriteHeaderNow() {
	if w == nil || w.written {
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (w *modelListBufferedWriter) Write(data []byte) (int, error) {
	if w == nil {
		return 0, nil
	}
	w.WriteHeaderNow()
	n, err := w.body.Write(data)
	w.size += n
	return n, err
}

func (w *modelListBufferedWriter) WriteString(data string) (int, error) {
	if w == nil {
		return 0, nil
	}
	w.WriteHeaderNow()
	n, err := w.body.WriteString(data)
	w.size += n
	return n, err
}

func (w *modelListBufferedWriter) Status() int {
	if w == nil || w.statusCode == 0 {
		return http.StatusOK
	}
	return w.statusCode
}

func (w *modelListBufferedWriter) Size() int {
	if w == nil || !w.written {
		return -1
	}
	return w.size
}

func (w *modelListBufferedWriter) Written() bool {
	return w != nil && w.written
}

func writeModelListResponse(writer gin.ResponseWriter, statusCode int, headers http.Header, body []byte) {
	if writer == nil {
		return
	}
	currentHeaders := writer.Header()
	for key := range currentHeaders {
		delete(currentHeaders, key)
	}
	for key, values := range headers {
		currentHeaders[key] = append([]string(nil), values...)
	}
	// The body may have changed after interception.
	currentHeaders.Del("Content-Length")
	writer.WriteHeader(statusCode)
	_, _ = writer.Write(body)
}
