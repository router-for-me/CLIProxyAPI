package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// 独立搜索不经过模型执行流程，因此在此补齐插件的请求生命周期。
type codexSearchLifecycle struct {
	host     handlers.PluginInterceptorHost
	request  pluginapi.RequestInterceptRequest
	started  time.Time
	rejected bool
}

func (s *Server) newCodexSearchLifecycle(ctx context.Context, c *gin.Context, model, requestedModel string, body []byte, headers http.Header) *codexSearchLifecycle {
	return &codexSearchLifecycle{
		host:    s.handlers.InterceptorHostSnapshot(),
		started: time.Now(),
		request: pluginapi.RequestInterceptRequest{
			RequestID: uuid.NewString(), TraceID: logging.GetRequestID(ctx),
			SourceFormat: codexAlphaSearchSourceFormat, Model: model, RequestedModel: requestedModel,
			Headers: headers.Clone(), Body: body,
			Metadata: map[string]any{coreexecutor.RequestedModelMetadataKey: requestedModel, coreexecutor.RequestPathMetadataKey: c.Request.URL.Path},
		},
	}
}

func (l *codexSearchLifecycle) intercept(ctx context.Context, c *gin.Context, selectedAuthID string) bool {
	if l.host == nil {
		return true
	}
	var response pluginapi.RequestInterceptResponse
	if selectedAuthID == "" {
		response = l.host.InterceptRequestBeforeAuth(ctx, l.request)
	} else {
		l.request.ToFormat = "codex"
		l.request.Metadata["selected_auth_id"] = selectedAuthID
		response = l.host.InterceptRequestAfterAuth(ctx, l.request)
	}
	if response.Terminate {
		l.rejected = true
		status := response.StatusCode
		if status < 200 || status > 599 {
			status = http.StatusForbidden
		}
		for key, values := range response.ResponseHeaders {
			c.Writer.Header()[key] = append([]string(nil), values...)
		}
		c.Data(status, c.Writer.Header().Get("Content-Type"), response.ResponseBody)
		return false
	}
	// 宿主返回最终请求头，其中已包含各插件对请求头的删除操作。
	if response.Headers != nil {
		l.request.Headers = response.Headers.Clone()
	}
	for _, key := range response.ClearHeaders {
		l.request.Headers.Del(key)
	}
	if len(response.Body) > 0 {
		l.request.Body = response.Body
	}
	return true
}

func (l *codexSearchLifecycle) complete(ctx context.Context, c *gin.Context) {
	host, ok := l.host.(interface {
		CompleteRequest(context.Context, pluginapi.RequestCompletion)
	})
	if !ok {
		return
	}
	status := c.Writer.Status()
	outcome := pluginapi.RequestCompletionSucceeded
	message := ""
	if l.rejected {
		outcome = pluginapi.RequestCompletionRejected
	} else if ctx.Err() != nil {
		outcome, status, message = pluginapi.RequestCompletionCanceled, 0, ctx.Err().Error()
	} else if status >= 400 {
		outcome = pluginapi.RequestCompletionFailed
	}
	host.CompleteRequest(ctx, pluginapi.RequestCompletion{
		RequestID: l.request.RequestID, TraceID: l.request.TraceID,
		SourceFormat: l.request.SourceFormat, Model: l.request.Model, RequestedModel: l.request.RequestedModel,
		StartedAt: l.started, CompletedAt: time.Now(), Outcome: outcome, StatusCode: status, Error: message,
		Metadata: l.request.Metadata,
	})
}
