package middleware

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

const maxInferenceModelProbeBytes = 64 << 10

type usagePublisher func(context.Context, usage.Record)

// InferenceObservabilityMiddleware records HTTP failures which happen before
// an executor can publish a usage record (for example request validation,
// authentication selection, and auth-unavailable failures). Executor records
// win through a synchronous request-context marker, so a failed request is
// represented exactly once.
func InferenceObservabilityMiddleware() gin.HandlerFunc {
	return inferenceObservabilityMiddleware(usage.PublishRecord)
}

func inferenceObservabilityMiddleware(publish usagePublisher) gin.HandlerFunc {
	if publish == nil {
		publish = usage.PublishRecord
	}
	return func(c *gin.Context) {
		if c == nil {
			return
		}
		if c.Request == nil || c.Request.URL == nil {
			c.Next()
			return
		}

		operation, routeModel, tracked := classifyInferenceRoute(c.Request.Method, c.Request.URL.Path)
		if !tracked {
			c.Next()
			return
		}

		startedAt := time.Now()
		modelProbe := peekAndRestoreRequestBody(c.Request, maxInferenceModelProbeBytes)
		model := inferenceRequestModel(modelProbe, routeModel, c.Query("model"))
		trackedCtx, tracker := usage.WithRequestTracking(c.Request.Context())
		c.Request = c.Request.WithContext(trackedCtx)

		c.Next()

		status := c.Writer.Status()
		if status < http.StatusBadRequest || tracker.Published() {
			return
		}

		publish(trackedCtx, usage.Record{
			Provider:    "proxy",
			Operation:   operation,
			Model:       model,
			Alias:       model,
			RequestedAt: startedAt,
			Latency:     time.Since(startedAt),
			Failed:      true,
			Fail: usage.Failure{
				StatusCode: status,
				Body:       fmt.Sprintf("proxy request failed before executor usage was published: %d %s", status, http.StatusText(status)),
			},
		})
	}
}

func classifyInferenceRoute(method, path string) (operation, model string, ok bool) {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)

	if method == http.MethodGet {
		switch path {
		case "/v1/responses", "/backend-api/codex/responses":
			return "inference", "", true
		default:
			return "", "", false
		}
	}
	if method != http.MethodPost {
		return "", "", false
	}

	switch path {
	case "/v1/chat/completions", "/v1/completions", "/v1/messages", "/v1/responses",
		"/v1/images/generations", "/v1/images/edits", "/v1/videos", "/v1/videos/generations",
		"/v1/videos/edits", "/v1/videos/extensions", "/openai/v1/videos", "/v1beta/interactions",
		"/backend-api/codex/responses":
		return "inference", "", true
	case "/v1/messages/count_tokens":
		return "count_tokens", "", true
	case "/v1/responses/compact", "/backend-api/codex/responses/compact":
		return "compaction", "", true
	}

	const geminiModelsPrefix = "/v1beta/models/"
	if !strings.HasPrefix(path, geminiModelsPrefix) {
		return "", "", false
	}
	actionPath := strings.TrimPrefix(path, geminiModelsPrefix)
	separator := strings.LastIndexByte(actionPath, ':')
	if separator <= 0 || separator == len(actionPath)-1 {
		return "", "", false
	}
	model = strings.TrimSpace(actionPath[:separator])
	switch actionPath[separator+1:] {
	case "generateContent", "streamGenerateContent":
		return "inference", model, true
	case "countTokens":
		return "count_tokens", model, true
	default:
		return "", "", false
	}
}

func inferenceRequestModel(probe []byte, routeModel, queryModel string) string {
	model := strings.TrimSpace(gjson.GetBytes(probe, "model").String())
	if model == "" {
		// Interactions may address a named agent instead of a model. Keeping the
		// agent in the model column makes the failure attributable but unpriced.
		model = strings.TrimSpace(gjson.GetBytes(probe, "agent").String())
	}
	if model == "" {
		model = strings.TrimSpace(routeModel)
	}
	if model == "" {
		model = strings.TrimSpace(queryModel)
	}
	return sanitizeObservedModel(model)
}

func sanitizeObservedModel(model string) string {
	model = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(model))
	const maxModelRunes = 256
	runes := []rune(model)
	if len(runes) > maxModelRunes {
		model = string(runes[:maxModelRunes])
	}
	return model
}

func peekAndRestoreRequestBody(req *http.Request, limit int64) []byte {
	if req == nil || req.Body == nil || req.Body == http.NoBody || limit <= 0 {
		return nil
	}
	body := req.Body
	probe, _ := io.ReadAll(io.LimitReader(body, limit))
	replayed := io.MultiReader(bytes.NewReader(probe), body)
	req.Body = &replayedRequestBody{Reader: replayed, closer: body}
	return probe
}

type replayedRequestBody struct {
	io.Reader
	closer io.Closer
}

func (b *replayedRequestBody) Close() error {
	if b == nil || b.closer == nil {
		return nil
	}
	return b.closer.Close()
}
