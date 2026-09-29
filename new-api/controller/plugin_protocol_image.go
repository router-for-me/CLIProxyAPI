package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// serveTaskPluginImageProtocol answers an OpenAI Images API request that a
// task plugin claimed. The API is synchronous: the durable submission runs
// first on a context that survives a client disconnect, an asynchronous
// upstream task is then polled inside the request until it is terminal or the
// protocol timeout elapses, and the plugin's render hook finally shapes the
// OpenAI image response from the terminal task.
func serveTaskPluginImageProtocol(c *gin.Context, pinned pluginruntime.PinnedEndpoint, deps pluginProtocolBridgeDeps) {
	deps = deps.withDefaults()
	requestValue, exists := c.Get(pluginruntime.ContextKeyProtocolRequest)
	protocolRequest, ok := requestValue.(pluginruntime.ProtocolRequestContext)
	if !exists || !ok || protocolRequest.Protocol != pinned.Protocol || pinned.Plugin == nil {
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	clientRequest := c.Request
	var relayInfo *relaycommon.RelayInfo
	var relayInfoErr error
	var outcome *taskSubmissionOutcome
	var taskErr *dto.TaskError
	// DashScope bills the images as soon as it accepts the upstream call. A
	// client that gives up while the submission is in flight must not cancel
	// that call, the task row, its settlement, or its consume log, so the
	// submission runs on its own bounded context exactly like the Responses
	// bridge. Only the in-request wait and the response write observe the
	// client connection.
	func() {
		submissionContext, cancelSubmission := context.WithTimeout(
			context.WithoutCancel(clientRequest.Context()),
			deps.submissionTimeout,
		)
		c.Request = clientRequest.Clone(submissionContext)
		defer func() {
			c.Request = clientRequest
			cancelSubmission()
		}()

		relayInfo, relayInfoErr = relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
		if relayInfoErr != nil {
			return
		}
		relayInfo.IsStream = false
		relayInfo.OriginModelName = c.GetString("resolved_task_model")
		if action := c.GetString("task_action"); action != "" {
			relayInfo.Action = action
		}
		if taskErr = relay.ResolveOriginTask(c, relayInfo); taskErr != nil {
			return
		}
		if taskErr = relay.ApplyOriginTaskAffinity(c, relayInfo); taskErr != nil {
			return
		}
		outcome, taskErr = deps.submit(c, relayInfo)
	}()
	if clientRequest.Context().Err() != nil {
		// The client is gone. A durable task keeps its settlement and consume
		// log; an asynchronous one is finished by the background poller.
		return
	}
	if relayInfoErr != nil {
		logger.LogError(c, "build task protocol relay info failed: "+relayInfoErr.Error())
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if taskErr != nil {
		respondTaskPluginImageError(c, taskErr)
		return
	}
	if outcome == nil || outcome.Task == nil || outcome.Task.Platform != constant.TaskPlatform(pinned.Plugin.Meta.Key) {
		logger.LogError(c, "task protocol submission returned an invalid durable outcome")
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	task := outcome.Task
	if task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure {
		if taskErr = waitTaskPluginImageTask(c, task, deps); taskErr != nil {
			if c.Request.Context().Err() == nil {
				respondTaskPluginImageError(c, taskErr)
			}
			return
		}
	}
	if task.Status == model.TaskStatusFailure {
		reason := strings.TrimSpace(task.FailReason)
		if reason == "" {
			reason = "image generation failed"
		}
		respondTaskPluginImageError(c, service.TaskErrorWrapperLocal(errors.New(reason), "image_generation_failed", http.StatusBadRequest))
		return
	}
	view, err := service.BuildTaskPluginView(task)
	if err != nil {
		logger.LogError(c, "build task protocol view failed: "+err.Error())
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	viewValue, err := taskPluginProtocolJSONValue(view)
	if err != nil {
		logger.LogError(c, "encode task protocol view failed: "+err.Error())
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	payload, err := pinned.Plugin.Engine.CallPathWithAdmissionTimeout(c.Request.Context(), deps.admissionTimeout, "protocols", []string{pinned.Protocol, "render"}, protocolRequest.JSValue(), viewValue)
	if err != nil {
		logger.LogError(c, "task protocol image render hook failed: "+err.Error())
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	response, ok := payload.(map[string]any)
	data, hasData := response["data"].([]any)
	if !ok || !hasData {
		logger.LogError(c, "task protocol image render hook must return an object with a data array")
		respondPluginProtocolError(c, http.StatusInternalServerError, "task_protocol_error", "Task protocol request failed")
		return
	}
	if _, present := response["created"]; !present {
		createdAt := task.CreatedAt
		if createdAt == 0 {
			createdAt = task.SubmitTime
		}
		response["created"] = createdAt
	}
	// response_format is host-owned: the plugin renders upstream URLs and the
	// host inlines each image when the client asked for Base64.
	if taskPluginImageResponseFormat(protocolRequest) == "b64_json" {
		for _, entry := range data {
			item, isObject := entry.(map[string]any)
			if !isObject {
				continue
			}
			url, _ := item["url"].(string)
			if existing, _ := item["b64_json"].(string); strings.TrimSpace(url) == "" || existing != "" {
				continue
			}
			_, encoded, downloadErr := deps.downloadImage(url)
			if downloadErr != nil {
				logger.LogWarn(c, fmt.Sprintf("task protocol image download failed; returning the upstream URL instead of b64_json: %s", common.MaskSensitiveInfo(downloadErr.Error())))
				continue
			}
			item["b64_json"] = encoded
		}
	}
	c.JSON(http.StatusOK, response)
}

// waitTaskPluginImageTask polls an asynchronous image task inside the client
// request through the ordinary task polling path, so completion settles
// billing exactly as the background poller would, until the task is terminal
// or the protocol timeout elapses. A client disconnect stops waiting; the
// background poller then finishes the durable task.
func waitTaskPluginImageTask(c *gin.Context, task *model.Task, deps pluginProtocolBridgeDeps) *dto.TaskError {
	ctx, cancel := context.WithTimeout(c.Request.Context(), deps.submissionTimeout)
	defer cancel()
	for {
		timer := time.NewTimer(deps.imagePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && c.Request.Context().Err() == nil {
				return service.TaskErrorWrapperLocal(fmt.Errorf("image generation is still running; task %s settles when it completes", task.TaskID), "task_timeout", http.StatusGatewayTimeout)
			}
			return service.TaskErrorWrapperLocal(ctx.Err(), "request_cancelled", http.StatusRequestTimeout)
		case <-timer.C:
		}
		if err := deps.pollTask(ctx, task); err != nil {
			logger.LogWarn(c, fmt.Sprintf("task protocol image poll failed for task %s: %s", task.TaskID, common.MaskSensitiveInfo(err.Error())))
			continue
		}
		if task.Status == model.TaskStatusSuccess || task.Status == model.TaskStatusFailure {
			return nil
		}
	}
}

// pollTaskPluginImageTask runs one poll round for a single task through the
// same code path as the scheduled task poller, including status CAS and
// completion settlement.
func pollTaskPluginImageTask(ctx context.Context, task *model.Task) error {
	upstreamID := task.GetUpstreamTaskID()
	if upstreamID == "" {
		return errors.New("task has no upstream id")
	}
	return service.UpdateVideoTasks(ctx, task.Platform, map[int][]string{task.ChannelId: {upstreamID}}, map[string]*model.Task{upstreamID: task})
}

func taskPluginImageResponseFormat(request pluginruntime.ProtocolRequestContext) string {
	body, ok := request.Body.(map[string]any)
	if !ok {
		return ""
	}
	switch body["kind"] {
	case string(pluginruntime.BodyJSON):
		value, _ := body["value"].(map[string]any)
		format, _ := value["response_format"].(string)
		return strings.TrimSpace(format)
	case string(pluginruntime.BodyMultipart), string(pluginruntime.BodyForm):
		fields, _ := body["fields"].(map[string][]string)
		if values := fields["response_format"]; len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

// respondTaskPluginImageError writes an OpenAI error envelope for the Images
// API. DashScope failure bodies arrive as JSON {code, message}; their message
// is surfaced so a client sees the vendor reason instead of raw JSON.
func respondTaskPluginImageError(c *gin.Context, taskErr *dto.TaskError) {
	service.RecordRequestPolicyTermination(c, taskSubmissionAPIError(taskErr))
	status := http.StatusInternalServerError
	if taskErr.StatusCode >= 400 && taskErr.StatusCode <= 599 {
		status = taskErr.StatusCode
	}
	code := taskErr.Code
	message := taskErr.Message
	var upstream struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if common.UnmarshalJsonStr(message, &upstream) == nil && strings.TrimSpace(upstream.Message) != "" {
		message = upstream.Message
		if strings.TrimSpace(upstream.Code) != "" {
			code = upstream.Code
		}
	}
	errorType := "new_api_error"
	if status < http.StatusInternalServerError {
		errorType = "invalid_request_error"
	}
	c.JSON(status, gin.H{"error": gin.H{
		"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
		"type":    errorType,
		"code":    code,
	}})
}
