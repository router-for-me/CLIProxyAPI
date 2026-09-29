package jsplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type requestDescriptor struct {
	ResponseType   string            `json:"responseType"`
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers"`
	Body           any               `json:"body"`
	Credentialless bool              `json:"credentialless"`
	Action         string            `json:"action"`
	Model          string            `json:"model"`
	RewriteModel   string            `json:"rewriteModel"`
	BodyType       string            `json:"bodyType"`
	Parts          []requestPart     `json:"parts"`
}

type requestPart struct {
	Name     string `json:"name"`
	Value    any    `json:"value"`
	FileRef  string `json:"fileRef"`
	Filename string `json:"filename"`
}

type submitResponse struct {
	TaskID    string      `json:"taskId"`
	TaskData  any         `json:"taskData"`
	Immediate *taskResult `json:"immediate"`
	State     any         `json:"state"`
}
type taskResult struct {
	Code             int     `json:"code"`
	TaskID           string  `json:"taskId"`
	Status           string  `json:"status"`
	Progress         string  `json:"progress"`
	Reason           string  `json:"reason"`
	URL              string  `json:"url"`
	RemoteURL        string  `json:"remoteUrl"`
	CompletionTokens float64 `json:"completionTokens"`
	TotalTokens      float64 `json:"totalTokens"`
	State            any     `json:"state"`
}

var taskArtifactKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

const maxTaskArtifacts = 64

// maxTaskPluginPersistedJSONBytes is the shared ceiling for taskData and plugin state.
const maxTaskPluginPersistedJSONBytes = 1 << 20

type TaskAdaptor struct {
	plugin         *pluginruntime.LoadedPlugin
	info           *relaycommon.RelayInfo
	submit         *requestDescriptor
	routeRequest   *pluginruntime.RouteRequestContext
	requestHeaders map[string]string
	files          []map[string]any
}

func New(plugin *pluginruntime.LoadedPlugin) *TaskAdaptor { return &TaskAdaptor{plugin: plugin} }
func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo)   { a.info = info }

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if pinnedValue, exists := c.Get(pluginruntime.ContextKeyPinnedEndpoint); exists {
		if pinned, ok := pinnedValue.(pluginruntime.PinnedEndpoint); ok && pinned.Plugin == a.plugin {
			if protocolValue, present := c.Get(pluginruntime.ContextKeyProtocolRequest); present {
				if protocolContext, valid := protocolValue.(pluginruntime.ProtocolRequestContext); valid {
					resolvedValue, callErr := a.plugin.Engine.CallPath(context.WithoutCancel(c.Request.Context()), "protocols", []string{pinned.Protocol, "decodeRequest"}, protocolContext.JSValue())
					resolved, resolvedOK := resolvedValue.(map[string]any)
					resolvedModel, modelOK := resolved["model"].(string)
					if callErr != nil || !resolvedOK || !modelOK || resolvedModel != pinned.Model {
						return service.TaskErrorWrapperLocal(fmt.Errorf("final task plugin decoder rejected the pinned model"), "plugin_request_invalid", http.StatusBadRequest)
					}
					if _, forbidden := resolved["renderer"]; forbidden {
						return service.TaskErrorWrapperLocal(fmt.Errorf("decoder must not return renderer"), "plugin_request_invalid", http.StatusBadRequest)
					}
					if body, present := resolved["requestBody"]; present {
						c.Set("task_request", body)
					}
					if action, valid := resolved["action"].(string); valid && strings.TrimSpace(action) != "" {
						c.Set("task_action", action)
						info.Action = action
					}
				}
			}
		}
	}
	if _, exists := c.Get("task_request"); !exists {
		if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, "image_to_video"); taskErr != nil {
			return taskErr
		}
	}
	request, hasRequest := c.Get("task_request")
	hasUsageProfiles := len(a.plugin.Meta.UsageProfiles) > 0
	if hasRequest && !hasUsageProfiles {
		if err := a.validateResolvedUsageRequest(request); err != nil {
			return service.TaskErrorWrapperLocal(err, "plugin_usage_invalid", http.StatusBadRequest)
		}
	}
	if _, err := a.buildSubmit(c, info); err != nil {
		return service.TaskErrorWrapperLocal(err, "plugin_request_invalid", http.StatusBadRequest)
	}
	// The descriptor may rewrite the model. Validate profiled requests against
	// that final model before any quota calculation or upstream submission; an
	// upstream endpoint ID without a profile keeps the client model's profile.
	if hasRequest && hasUsageProfiles {
		if err := a.validateResolvedUsageRequest(request, info.UpstreamModelName, info.OriginModelName); err != nil {
			return service.TaskErrorWrapperLocal(err, "plugin_usage_invalid", http.StatusBadRequest)
		}
	}
	return nil
}

func (a *TaskAdaptor) EstimateBilling(c *gin.Context, info *relaycommon.RelayInfo) map[string]float64 {
	ratios, err := a.EstimateBillingValidated(c, info)
	if err != nil {
		a.logRejectedUsage("extractUsage", err)
		return nil
	}
	return ratios
}

func (a *TaskAdaptor) EstimateBillingValidated(c *gin.Context, info *relaycommon.RelayInfo) (map[string]float64, error) {
	usageContext := a.submitContext(c, info)
	usageContext["usagePurpose"] = "billing_ratios"
	return a.usageRatios(c.Request.Context(), []string{info.UpstreamModelName, info.OriginModelName}, "extractUsage", usageContext)
}

func (a *TaskAdaptor) ExtractUsageFacts(c *gin.Context, info *relaycommon.RelayInfo) map[string]any {
	facts, err := a.ExtractUsageFactsValidated(c, info)
	if err != nil {
		a.logRejectedUsage("extractUsage", err)
		return nil
	}
	return facts
}

func (a *TaskAdaptor) ExtractUsageFactsValidated(c *gin.Context, info *relaycommon.RelayInfo) (map[string]any, error) {
	if !a.hasHook(c.Request.Context(), "extractUsage") {
		return nil, nil
	}
	usageContext := a.submitContext(c, info)
	usageContext["usagePurpose"] = "facts"
	value, err := a.plugin.Engine.Call(c.Request.Context(), "extractUsage", usageContext)
	if err != nil {
		return nil, fmt.Errorf("plugin usage hook failed")
	}
	if value == nil {
		return nil, nil
	}
	facts, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("plugin usage hook must return an object")
	}
	if _, err = a.validatedUsageRatios(facts, info.UpstreamModelName, info.OriginModelName); err != nil {
		return nil, err
	}
	return facts, nil
}

func (a *TaskAdaptor) AdjustBillingOnSubmit(info *relaycommon.RelayInfo, taskData []byte) map[string]float64 {
	var data any
	if err := common.Unmarshal(taskData, &data); err != nil {
		data = string(taskData)
	}
	ratios, err := a.usageRatios(context.Background(), []string{info.UpstreamModelName, info.OriginModelName}, "extractUsageOnSubmit", a.submitContext(nil, info), data)
	if err != nil {
		a.logRejectedUsage("extractUsageOnSubmit", err)
		return nil
	}
	return ratios
}

func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, result *relaycommon.TaskInfo) int {
	if !a.hasHook(context.Background(), "extractUsageOnComplete") {
		return 0
	}
	value, err := a.plugin.Engine.Call(context.Background(), "extractUsageOnComplete", jsonValue(task), jsonValue(result))
	if err != nil {
		return 0
	}
	if task == nil {
		a.applyCompletionUsageFacts(result, value)
		return 0
	}
	a.applyCompletionUsageFacts(result, value, task.Properties.UpstreamModelName, task.Properties.OriginModelName)
	return 0
}

func (a *TaskAdaptor) BuildRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if a.submit == nil {
		return "", fmt.Errorf("plugin submit request was not built")
	}
	return a.submit.URL, pluginruntime.ValidateRequestURL(a.submit.URL, info.ChannelBaseUrl, a.plugin.Meta.AllowedHosts)
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, req *http.Request, _ *relaycommon.RelayInfo) error {
	if a.submit == nil {
		return fmt.Errorf("plugin submit request was not built")
	}
	for name, value := range a.submit.Headers {
		req.Header.Set(name, value)
	}
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	descriptor, err := a.buildSubmit(c, info)
	if err != nil {
		return nil, err
	}
	if descriptor.BodyType == "multipart" {
		form, parseErr := common.ParseMultipartFormReusable(c)
		if parseErr != nil {
			return nil, parseErr
		}
		defer form.RemoveAll()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for _, part := range descriptor.Parts {
			if part.FileRef == "" {
				header := make(textproto.MIMEHeader)
				disposition := mime.FormatMediaType("form-data", map[string]string{"name": part.Name})
				if disposition == "" {
					return nil, fmt.Errorf("invalid multipart name")
				}
				header.Set("Content-Disposition", disposition)
				destination, createErr := writer.CreatePart(header)
				if createErr != nil {
					return nil, createErr
				}
				if _, err = io.WriteString(destination, fmt.Sprint(part.Value)); err != nil {
					return nil, err
				}
				continue
			}
			field, index, validRef := pluginruntime.ParseFileReference(part.FileRef)
			files := form.File[field]
			if !validRef || index >= len(files) {
				return nil, fmt.Errorf("unknown file reference %q", part.FileRef)
			}
			file, openErr := files[index].Open()
			if openErr != nil {
				return nil, openErr
			}
			filename := part.Filename
			if filename == "" {
				filename = files[index].Filename
			}
			header := make(textproto.MIMEHeader)
			disposition := mime.FormatMediaType("form-data", map[string]string{"name": part.Name, "filename": filename})
			if disposition == "" {
				file.Close()
				return nil, fmt.Errorf("invalid multipart name or filename")
			}
			header.Set("Content-Disposition", disposition)
			header.Set("Content-Type", files[index].Header.Get("Content-Type"))
			destination, copyErr := writer.CreatePart(header)
			if copyErr == nil {
				_, copyErr = io.Copy(destination, file)
			}
			file.Close()
			if copyErr != nil {
				return nil, copyErr
			}
		}
		if err = writer.Close(); err != nil {
			return nil, err
		}
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return bytes.NewReader(body.Bytes()), nil
	}
	if descriptor.Body == nil {
		return nil, nil
	}
	if text, ok := descriptor.Body.(string); ok {
		return strings.NewReader(text), nil
	}
	inlined, err := inlineJSONFilePlaceholders(c, descriptor.Body)
	if err != nil {
		return nil, err
	}
	body, err := common.Marshal(inlined)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(body), nil
}

func maxInlineFileBytes() int64 {
	limitMB := constant.MaxFileDownloadMB
	if limitMB <= 0 {
		limitMB = 64
	}
	return int64(limitMB) << 20
}

func inlineJSONFilePlaceholders(c *gin.Context, body any) (any, error) {
	cloned := jsonValue(body)
	var form *multipart.Form
	if c != nil && c.Request != nil && strings.Contains(c.GetHeader("Content-Type"), "multipart/form-data") {
		parsed, parseErr := common.ParseMultipartFormReusable(c)
		if parseErr != nil {
			return nil, parseErr
		}
		form = parsed
		defer form.RemoveAll()
	}
	limit := maxInlineFileBytes()
	var total int64
	return replaceJSONFilePlaceholders(cloned, form, limit, &total)
}

func replaceJSONFilePlaceholders(value any, form *multipart.Form, limit int64, total *int64) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if _, isPlaceholder := typed["__fileRef"]; isPlaceholder {
			return encodeFilePlaceholder(typed, form, limit, total)
		}
		for key, item := range typed {
			replaced, err := replaceJSONFilePlaceholders(item, form, limit, total)
			if err != nil {
				return nil, err
			}
			typed[key] = replaced
		}
		return typed, nil
	case []any:
		for index, item := range typed {
			replaced, err := replaceJSONFilePlaceholders(item, form, limit, total)
			if err != nil {
				return nil, err
			}
			typed[index] = replaced
		}
		return typed, nil
	default:
		return value, nil
	}
}

func encodeFilePlaceholder(placeholder map[string]any, form *multipart.Form, limit int64, total *int64) (string, error) {
	for key := range placeholder {
		switch key {
		case "__fileRef", "encoding", "mimeType", "maxBytes":
		default:
			return "", fmt.Errorf("invalid file placeholder")
		}
	}
	ref, _ := placeholder["__fileRef"].(string)
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("unknown file reference %q", ref)
	}
	encoding, _ := placeholder["encoding"].(string)
	if encoding != "base64" && encoding != "dataUrl" {
		return "", fmt.Errorf("file placeholder encoding must be \"base64\" or \"dataUrl\"")
	}
	if form == nil {
		return "", fmt.Errorf("unknown file reference %q", ref)
	}
	field, index, validRef := pluginruntime.ParseFileReference(ref)
	files := form.File[field]
	if !validRef || index >= len(files) {
		return "", fmt.Errorf("unknown file reference %q", ref)
	}
	header := files[index]
	maxBytes := limit
	if raw, exists := placeholder["maxBytes"]; exists {
		n, ok := usageNumber(raw, false)
		if !ok || n <= 0 || n != math.Trunc(n) {
			return "", fmt.Errorf("invalid file placeholder")
		}
		if int64(n) < maxBytes {
			maxBytes = int64(n)
		}
	}
	if header.Size > maxBytes {
		return "", fmt.Errorf("file %q exceeds the %d byte limit", ref, maxBytes)
	}
	file, openErr := header.Open()
	if openErr != nil {
		return "", openErr
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	file.Close()
	if readErr != nil {
		return "", readErr
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("file %q exceeds the %d byte limit", ref, maxBytes)
	}
	if *total+int64(len(data)) > limit {
		return "", fmt.Errorf("inlined files exceed the %d byte limit", limit)
	}
	*total += int64(len(data))
	encoded := base64.StdEncoding.EncodeToString(data)
	if encoding == "base64" {
		return encoded, nil
	}
	mimeType := "application/octet-stream"
	if override, ok := placeholder["mimeType"].(string); ok && strings.TrimSpace(override) != "" {
		mimeType = override
	} else if contentType := header.Header.Get("Content-Type"); contentType != "" {
		mimeType = contentType
	}
	return "data:" + mimeType + ";base64," + encoded, nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*http.Response, error) {
	wasStream := info.IsStream
	info.IsStream = false
	defer func() { info.IsStream = wasStream }()
	if a.submit != nil && strings.TrimSpace(a.submit.Method) != "" {
		originalMethod := c.Request.Method
		c.Request.Method = strings.ToUpper(a.submit.Method)
		defer func() { c.Request.Method = originalMethod }()
	}
	return channel.DoTaskApiRequest(a, c, info, body)
}

func (a *TaskAdaptor) ParseResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (_ *channel.TaskSubmitResponse, taskErr *dto.TaskError) {
	started := time.Now()
	var responseBody any
	var err error
	streaming := a.submit != nil && a.submit.ResponseType == "sse"
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	acceptedStream := streaming || mediaType == "text/event-stream"
	defer func() {
		if acceptedStream && taskErr != nil {
			taskErr.NoRetry = true
		}
	}()
	if !streaming && acceptedStream {
		return nil, service.TaskErrorWrapperLocal(fmt.Errorf("unexpected SSE response for a JSON submission"), "plugin_submit_response_invalid", http.StatusBadGateway)
	}
	if streaming {
		// Every failure after accepting this stream is non-retryable: the
		// upstream may already have performed billable work.
		defer resp.Body.Close()
		responseBody, err = a.readSubmitEvents(c.Request.Context(), resp, a.submitContext(c, info))
	} else {
		var body []byte
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxTaskPluginPersistedJSONBytes+1))
		if err == nil && len(body) > maxTaskPluginPersistedJSONBytes {
			err = fmt.Errorf("task submit response exceeds size limit")
		}
		responseBody = string(body)
		var decoded any
		if err == nil && common.Unmarshal(body, &decoded) == nil {
			responseBody = decoded
		}
	}
	if err != nil {
		failure := service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusBadGateway)
		failure.NoRetry = streaming
		return nil, failure
	}
	headers := make(map[string][]string, len(resp.Header))
	maps.Copy(headers, resp.Header)
	value, err := a.plugin.Engine.Call(c.Request.Context(), "parseSubmitResponse", a.submitContext(c, info), map[string]any{"statusCode": resp.StatusCode, "headers": headers, "body": responseBody})
	if err != nil {
		logger.LogDebug(
			c,
			"task_plugin subsystem=adaptor event=parse_submit_failed plugin=%q stage=parse_submit_response reason=hook_failed status=%d elapsed_ms=%d",
			a.plugin.Meta.Key,
			resp.StatusCode,
			time.Since(started).Milliseconds(),
		)
		return nil, service.TaskErrorWrapper(err, "plugin_submit_response_failed", http.StatusBadGateway)
	}
	if object, ok := value.(map[string]any); ok {
		if _, forbidden := object["clientResponse"]; forbidden {
			return nil, service.TaskErrorWrapperLocal(fmt.Errorf("parseSubmitResponse must not return clientResponse"), "plugin_submit_response_invalid", http.StatusBadGateway)
		}
	}
	var parsed submitResponse
	if err = convert(value, &parsed); err != nil || strings.TrimSpace(parsed.TaskID) == "" {
		if err == nil {
			err = fmt.Errorf("plugin returned an empty taskId")
		}
		logger.LogDebug(
			c,
			"task_plugin subsystem=adaptor event=parse_submit_failed plugin=%q stage=parse_submit_response reason=invalid_result status=%d elapsed_ms=%d",
			a.plugin.Meta.Key,
			resp.StatusCode,
			time.Since(started).Milliseconds(),
		)
		return nil, service.TaskErrorWrapper(err, "plugin_submit_response_invalid", http.StatusBadGateway)
	}
	var taskData []byte
	if parsed.TaskData != nil {
		taskData, err = common.Marshal(parsed.TaskData)
		if err != nil || len(taskData) > maxTaskPluginPersistedJSONBytes {
			if err == nil {
				err = fmt.Errorf("task data exceeds size limit")
			}
			return nil, service.TaskErrorWrapper(err, "plugin_submit_response_invalid", http.StatusBadGateway)
		}
	}
	pluginState, _ := encodeReturnedPluginState(value)
	if len(pluginState) > maxTaskPluginPersistedJSONBytes {
		logger.LogWarn(c, fmt.Sprintf("task plugin %s rejected oversized submit state (%d bytes)", a.plugin.Meta.Key, len(pluginState)))
		pluginState = nil
	}
	response := &channel.TaskSubmitResponse{
		UpstreamTaskID: parsed.TaskID,
		TaskData:       taskData,
		PluginState:    pluginState,
	}
	logger.LogDebug(c, "task_plugin subsystem=adaptor event=parse_submit_complete plugin=%q status=%d task_data_bytes=%d immediate=%t elapsed_ms=%d",
		a.plugin.Meta.Key, resp.StatusCode, len(taskData), parsed.Immediate != nil, time.Since(started).Milliseconds())
	if parsed.Immediate == nil {
		return response, nil
	}
	immediate := &relaycommon.TaskInfo{
		Code: parsed.Immediate.Code, TaskID: parsed.Immediate.TaskID,
		Status: parsed.Immediate.Status, Progress: parsed.Immediate.Progress,
		Reason: parsed.Immediate.Reason, Url: parsed.Immediate.URL, RemoteUrl: parsed.Immediate.RemoteURL,
		CompletionTokens: positiveInt(parsed.Immediate.CompletionTokens), TotalTokens: positiveInt(parsed.Immediate.TotalTokens),
	}
	response.Immediate = immediate
	if immediate.Status != model.TaskStatusSuccess || !a.hasHook(c.Request.Context(), "extractUsageOnComplete") {
		return response, nil
	}
	// Match the polling hook context without persisting a temporary task.
	task := &model.Task{TaskID: info.PublicTaskID, Action: info.Action, Data: taskData,
		Properties: model.Properties{OriginModelName: info.OriginModelName, UpstreamModelName: info.UpstreamModelName}}
	task.PrivateData.UpstreamTaskID = parsed.TaskID
	task.PrivateData.PluginState = pluginState
	ctx, err := a.queryContext(task, info.ApiKey, info.ChannelBaseUrl, info.ChannelSetting.Proxy)
	if err != nil {
		logger.LogWarn(c, fmt.Sprintf("task plugin %s completion context failed; retaining reserved quota: %v", a.plugin.Meta.Key, err))
		return response, nil
	}
	facts, err := a.plugin.Engine.Call(c.Request.Context(), "extractUsageOnComplete", ctx, jsonValue(immediate), parsed.TaskData)
	if err != nil {
		logger.LogWarn(c, fmt.Sprintf("task plugin %s completion usage failed; retaining reserved quota: %v", a.plugin.Meta.Key, err))
		return response, nil
	}
	if err := a.applyCompletionUsageFacts(immediate, facts, info.UpstreamModelName, info.OriginModelName); err != nil {
		logger.LogWarn(c, fmt.Sprintf("task plugin %s completion usage rejected; retaining reserved quota: %v", a.plugin.Meta.Key, err))
	}
	return response, nil
}

func (a *TaskAdaptor) GetModelList() []string { return append([]string(nil), a.plugin.Meta.Models...) }
func (a *TaskAdaptor) GetChannelName() string { return a.plugin.Meta.Name }
func (a *TaskAdaptor) FetchMode() string      { return a.plugin.Meta.FetchMode }

func (a *TaskAdaptor) FetchBatchTasks(baseURL, key string, tasks []*model.Task, proxy string) (*http.Response, error) {
	taskContexts := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		taskCtx, err := a.queryContext(task, key, baseURL, proxy)
		if err != nil {
			return nil, err
		}
		taskContexts = append(taskContexts, taskCtx)
	}
	ctx, err := a.batchQueryContext(key, baseURL, proxy, taskContexts)
	if err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.Call(context.Background(), "buildBatchQueryRequest", ctx, taskContexts)
	if err != nil {
		return nil, err
	}
	return a.doFetchDescriptor(baseURL, proxy, value)
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, task *model.Task, proxy string) (*http.Response, error) {
	ctx, err := a.queryContext(task, key, baseURL, proxy)
	if err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.Call(context.Background(), "buildQueryRequest", ctx)
	if err != nil {
		return nil, err
	}
	return a.doFetchDescriptor(baseURL, proxy, value)
}

func (a *TaskAdaptor) doFetchDescriptor(baseURL, proxy string, value any) (*http.Response, error) {
	var descriptor requestDescriptor
	if err := convert(value, &descriptor); err != nil {
		return nil, err
	}
	if err := pluginruntime.ValidateRequestURL(descriptor.URL, baseURL, a.plugin.Meta.AllowedHosts); err != nil {
		return nil, err
	}
	var requestBody io.Reader
	if descriptor.Body != nil {
		if bodyText, ok := descriptor.Body.(string); ok {
			requestBody = strings.NewReader(bodyText)
		} else {
			encoded, marshalErr := common.Marshal(descriptor.Body)
			if marshalErr != nil {
				return nil, marshalErr
			}
			requestBody = bytes.NewReader(encoded)
		}
	}
	method := strings.ToUpper(strings.TrimSpace(descriptor.Method))
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, descriptor.URL, requestBody)
	if err != nil {
		return nil, err
	}
	for name, value := range descriptor.Headers {
		req.Header.Set(name, value)
	}
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		logger.LogDebug(
			context.Background(),
			"task_plugin subsystem=adaptor event=query_request_failed plugin=%q method=%q reason=transport_error elapsed_ms=%d",
			a.plugin.Meta.Key,
			method,
			time.Since(started).Milliseconds(),
		)
		return nil, err
	}
	logger.LogDebug(
		context.Background(),
		"task_plugin subsystem=adaptor event=query_response_received plugin=%q method=%q status=%d elapsed_ms=%d",
		a.plugin.Meta.Key,
		method,
		resp.StatusCode,
		time.Since(started).Milliseconds(),
	)
	return resp, nil
}

func (a *TaskAdaptor) ParseBatchResult(tasks []*model.Task, resp *http.Response, body []byte) (map[string]*service.BatchTaskResult, error) {
	started := time.Now()
	input := any(string(body))
	var decoded any
	if common.Unmarshal(body, &decoded) == nil {
		input = decoded
	}
	key, baseURL, proxy := a.queryCredentials()
	taskContexts := make([]map[string]any, 0, len(tasks))
	for _, task := range tasks {
		taskCtx, err := a.queryContext(task, key, baseURL, proxy)
		if err != nil {
			return nil, err
		}
		taskContexts = append(taskContexts, taskCtx)
	}
	ctx, err := a.batchQueryContext(key, baseURL, proxy, taskContexts)
	if err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.Call(context.Background(), "parseBatchResult", ctx, input, hookHTTPResponse(resp))
	if err != nil {
		logger.LogDebug(context.Background(), "task_plugin subsystem=adaptor event=parse_batch_failed plugin=%q reason=hook_failed body_bytes=%d elapsed_ms=%d", a.plugin.Meta.Key, len(body), time.Since(started).Milliseconds())
		return nil, err
	}
	var parsed []struct {
		TaskID     string `json:"taskId"`
		Action     string `json:"action"`
		Status     string `json:"status"`
		Progress   string `json:"progress"`
		Reason     string `json:"reason"`
		URL        string `json:"url"`
		SubmitTime int64  `json:"submitTime"`
		StartTime  int64  `json:"startTime"`
		FinishTime int64  `json:"finishTime"`
		Data       any    `json:"data"`
		State      any    `json:"state"`
	}
	if err = convert(value, &parsed); err != nil {
		logger.LogDebug(context.Background(), "task_plugin subsystem=adaptor event=parse_batch_failed plugin=%q reason=invalid_result body_bytes=%d elapsed_ms=%d", a.plugin.Meta.Key, len(body), time.Since(started).Milliseconds())
		return nil, err
	}
	results := make(map[string]*service.BatchTaskResult, len(parsed))
	hasCompletionUsage := a.hasHook(context.Background(), "extractUsageOnComplete")
	for _, item := range parsed {
		if strings.TrimSpace(item.TaskID) == "" {
			continue
		}
		info := relaycommon.TaskInfo{TaskID: item.TaskID, Status: item.Status, Progress: item.Progress, Reason: item.Reason, Url: item.URL}
		if item.State != nil {
			pluginState, marshalErr := common.Marshal(item.State)
			if marshalErr != nil || len(pluginState) > maxTaskPluginPersistedJSONBytes {
				logger.LogWarn(context.Background(), fmt.Sprintf("task plugin %s rejected invalid or oversized poll state", a.plugin.Meta.Key))
			} else {
				info.PluginState = pluginState
			}
		}
		if hasCompletionUsage {
			usageBody := item.Data
			if usageBody == nil {
				usageBody = jsonValue(item)
			}
			itemCtx := ctx
			for _, taskCtx := range taskContexts {
				if fmt.Sprint(taskCtx["taskId"]) == item.TaskID {
					itemCtx = taskCtx
					break
				}
			}
			facts, hookErr := a.plugin.Engine.Call(context.Background(), "extractUsageOnComplete", itemCtx, jsonValue(&info), usageBody)
			if hookErr == nil {
				upstreamModel, _ := itemCtx["upstreamModel"].(string)
				originModel, _ := itemCtx["model"].(string)
				a.applyCompletionUsageFacts(&info, facts, upstreamModel, originModel)
			}
		}
		results[item.TaskID] = &service.BatchTaskResult{TaskInfo: info, Action: item.Action, SubmitTime: item.SubmitTime, StartTime: item.StartTime, FinishTime: item.FinishTime, Data: item.Data}
	}
	logger.LogDebug(
		context.Background(),
		"task_plugin subsystem=adaptor event=parse_batch_complete plugin=%q body_bytes=%d results=%d completion_usage_hook=%t elapsed_ms=%d",
		a.plugin.Meta.Key,
		len(body),
		len(results),
		hasCompletionUsage,
		time.Since(started).Milliseconds(),
	)
	return results, nil
}

func (a *TaskAdaptor) ParseTaskResult(task *model.Task, resp *http.Response, body []byte) (*relaycommon.TaskInfo, error) {
	started := time.Now()
	input := any(string(body))
	var decoded any
	if common.Unmarshal(body, &decoded) == nil {
		input = decoded
	}
	key, baseURL, proxy := a.queryCredentials()
	if task != nil && task.PrivateData.Key != "" {
		key = task.PrivateData.Key
	}
	ctx, err := a.queryContext(task, key, baseURL, proxy)
	if err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.Call(context.Background(), "parseTaskResult", ctx, input, hookHTTPResponse(resp))
	if err != nil {
		logger.LogDebug(context.Background(), "task_plugin subsystem=adaptor event=parse_task_failed plugin=%q reason=hook_failed body_bytes=%d elapsed_ms=%d", a.plugin.Meta.Key, len(body), time.Since(started).Milliseconds())
		return nil, err
	}
	var parsed taskResult
	if err = convert(value, &parsed); err != nil {
		logger.LogDebug(context.Background(), "task_plugin subsystem=adaptor event=parse_task_failed plugin=%q reason=invalid_result body_bytes=%d elapsed_ms=%d", a.plugin.Meta.Key, len(body), time.Since(started).Milliseconds())
		return nil, err
	}
	result := &relaycommon.TaskInfo{
		Code:             parsed.Code,
		TaskID:           parsed.TaskID,
		Status:           parsed.Status,
		Progress:         parsed.Progress,
		Reason:           parsed.Reason,
		Url:              parsed.URL,
		RemoteUrl:        parsed.RemoteURL,
		CompletionTokens: positiveInt(parsed.CompletionTokens),
		TotalTokens:      positiveInt(parsed.TotalTokens),
	}
	if pluginState, present := encodeReturnedPluginState(value); present {
		if len(pluginState) > maxTaskPluginPersistedJSONBytes {
			logger.LogWarn(context.Background(), fmt.Sprintf("task plugin %s rejected oversized poll state (%d bytes)", a.plugin.Meta.Key, len(pluginState)))
		} else {
			result.PluginState = pluginState
		}
	}
	// The raw polling response only exists at this boundary. Capture upstream
	// units here so the host settlement path can consume them from TaskInfo.
	if a.hasHook(context.Background(), "extractUsageOnComplete") {
		facts, hookErr := a.plugin.Engine.Call(context.Background(), "extractUsageOnComplete", ctx, jsonValue(result), input)
		if hookErr == nil {
			upstreamModel, _ := ctx["upstreamModel"].(string)
			originModel, _ := ctx["model"].(string)
			a.applyCompletionUsageFacts(result, facts, upstreamModel, originModel)
		}
	}
	taskStatus := model.TaskStatus(result.Status)
	logger.LogDebug(
		context.Background(),
		"task_plugin subsystem=adaptor event=parse_task_complete plugin=%q terminal=%t body_bytes=%d elapsed_ms=%d",
		a.plugin.Meta.Key,
		taskStatus == model.TaskStatusSuccess || taskStatus == model.TaskStatusFailure,
		len(body),
		time.Since(started).Milliseconds(),
	)
	return result, nil
}

func (a *TaskAdaptor) applyCompletionUsageFacts(result *relaycommon.TaskInfo, facts any, models ...string) error {
	values, err := a.validatedCompletionUsageFacts(facts, models...)
	if err != nil {
		a.logRejectedUsage("extractUsageOnComplete", err)
		return err
	}
	if len(values) == 0 {
		return nil
	}
	result.UsageFacts = values
	if units := positiveInt(values["upstreamUnits"]); units > 0 {
		result.CompletionTokens = units
		result.TotalTokens = units
		return nil
	}
	if completionTokens, exists := values["completionTokens"]; exists {
		result.CompletionTokens = positiveInt(completionTokens)
	}
	if totalTokens, exists := values["totalTokens"]; exists {
		result.TotalTokens = positiveInt(totalTokens)
	}
	return nil
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	claimed := slices.ContainsFunc(a.plugin.Meta.Protocols, func(claim pluginruntime.ProtocolClaim) bool {
		return claim.Name == "openai_video"
	})
	if !claimed {
		return nil, fmt.Errorf("plugin does not claim openai_video")
	}
	view, err := service.BuildTaskPluginView(task)
	if err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.CallPath(context.Background(), "protocols", []string{"openai_video", "render"}, map[string]any{"protocol": "openai_video", "operation": "retrieve"}, jsonValue(view))
	if err != nil {
		return nil, err
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, err
	}
	var rendered map[string]any
	if err = common.Unmarshal(encoded, &rendered); err != nil || rendered == nil {
		return nil, fmt.Errorf("plugin returned an invalid OpenAI video object")
	}
	// Keep provider extensions intact while the host owns the public task's
	// identity and lifecycle, including completion timestamps after settlement.
	host := task.ToOpenAIVideo()
	rendered["id"] = host.ID
	rendered["object"] = host.Object
	delete(rendered, "task_id")
	rendered["status"] = host.Status
	rendered["progress"] = host.Progress
	rendered["created_at"] = host.CreatedAt
	rendered["model"] = host.Model
	if host.CompletedAt != 0 {
		rendered["completed_at"] = host.CompletedAt
	} else {
		delete(rendered, "completed_at")
	}
	return common.Marshal(rendered)
}

func (a *TaskAdaptor) ListArtifacts(task *model.Task) ([]channel.TaskArtifact, error) {
	if !a.hasHook(context.Background(), "listArtifacts") {
		return nil, nil
	}
	ctx, err := taskArtifactContext(task)
	if err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.Call(context.Background(), "listArtifacts", ctx)
	if err != nil {
		return nil, fmt.Errorf("plugin artifact listing failed")
	}
	return validateTaskArtifacts(value)
}

func (a *TaskAdaptor) BuildContentRequest(task *model.Task, artifactKey string, clientRequest channel.TaskArtifactClientRequest) (*channel.TaskContentRequest, error) {
	if !a.hasHook(context.Background(), "buildContentRequest") {
		return nil, nil
	}
	if a.info == nil {
		return nil, fmt.Errorf("plugin adaptor is not initialized")
	}
	if !taskArtifactKeyPattern.MatchString(artifactKey) {
		return nil, fmt.Errorf("invalid artifact key")
	}
	ctx, err := taskArtifactContext(task)
	if err != nil {
		return nil, err
	}
	ctx["upstreamTaskId"] = task.GetUpstreamTaskID()
	ctx["artifactKey"] = artifactKey
	ctx["baseUrl"] = a.info.ChannelBaseUrl
	ctx["clientRequest"] = jsonValue(clientRequest)
	if err = a.applyUpstreamCredentials(ctx, a.info.ChannelType, a.info.ApiKey, a.info.ChannelSetting.Proxy); err != nil {
		return nil, err
	}
	value, err := a.plugin.Engine.Call(context.Background(), "buildContentRequest", ctx)
	if err != nil {
		return nil, err
	}
	var descriptor requestDescriptor
	if err = convert(value, &descriptor); err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(descriptor.Method))
	if method == "" {
		method = strings.ToUpper(strings.TrimSpace(clientRequest.Method))
	}
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodPost {
		return nil, fmt.Errorf("plugin returned an unsupported artifact request method")
	}
	if descriptor.Credentialless {
		if method != http.MethodGet && method != http.MethodHead {
			return nil, fmt.Errorf("credentialless artifact requests must use GET or HEAD")
		}
		if len(descriptor.Headers) != 0 || descriptor.Body != nil {
			return nil, fmt.Errorf("credentialless artifact requests cannot contain headers or a body")
		}
		parsedURL, parseErr := url.Parse(descriptor.URL)
		if parseErr != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
			return nil, fmt.Errorf("credentialless artifact request URL must be absolute HTTP(S)")
		}
	} else if err = pluginruntime.ValidateRequestURL(descriptor.URL, a.info.ChannelBaseUrl, a.plugin.Meta.AllowedHosts); err != nil {
		return nil, err
	}
	var body []byte
	if descriptor.Body != nil {
		if text, ok := descriptor.Body.(string); ok {
			body = []byte(text)
		} else {
			body, err = common.Marshal(descriptor.Body)
			if err != nil {
				return nil, fmt.Errorf("plugin returned an invalid artifact request body")
			}
		}
	}
	return &channel.TaskContentRequest{
		URL:            descriptor.URL,
		Method:         method,
		Headers:        descriptor.Headers,
		Body:           body,
		Credentialless: descriptor.Credentialless,
	}, nil
}

func taskArtifactContext(task *model.Task) (map[string]any, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	var data any
	if len(task.Data) > 0 {
		if err := common.Unmarshal(task.Data, &data); err != nil {
			return nil, fmt.Errorf("task data is invalid")
		}
	}
	producerVersion := ""
	if task.PrivateData.Execution != nil && task.PrivateData.Execution.TaskPlugin != nil {
		producerVersion = task.PrivateData.Execution.TaskPlugin.Version
	}
	var state any
	if len(task.PrivateData.PluginState) > 0 {
		if err := common.Unmarshal(task.PrivateData.PluginState, &state); err != nil {
			return nil, fmt.Errorf("plugin state is invalid")
		}
	}
	return map[string]any{
		"taskId":          task.TaskID,
		"status":          string(task.Status),
		"action":          task.Action,
		"data":            data,
		"state":           state,
		"producerVersion": producerVersion,
	}, nil
}

func (a *TaskAdaptor) queryContext(task *model.Task, key, baseURL, proxy string) (map[string]any, error) {
	ctx := map[string]any{
		"taskId":        "",
		"publicTaskId":  "",
		"action":        "",
		"model":         "",
		"upstreamModel": "",
		"baseUrl":       baseURL,
		"data":          nil,
		"state":         nil,
	}
	if task != nil {
		originModel := task.Properties.OriginModelName
		upstreamModel := task.Properties.UpstreamModelName
		if upstreamModel == "" {
			upstreamModel = originModel
		}
		ctx["taskId"] = task.GetUpstreamTaskID()
		ctx["publicTaskId"] = task.TaskID
		ctx["action"] = constant.NormalizeTaskAction(task.Action)
		ctx["model"] = originModel
		ctx["upstreamModel"] = upstreamModel
		if len(task.Data) > 0 {
			var data any
			if err := common.Unmarshal(task.Data, &data); err != nil {
				return nil, fmt.Errorf("task data is invalid")
			}
			ctx["data"] = data
		}
		if len(task.PrivateData.PluginState) > 0 {
			var state any
			if err := common.Unmarshal(task.PrivateData.PluginState, &state); err != nil {
				return nil, fmt.Errorf("plugin state is invalid")
			}
			ctx["state"] = state
		}
		if task.PrivateData.Key != "" {
			key = task.PrivateData.Key
		}
	}
	if err := a.applyUpstreamCredentials(ctx, a.channelType(), key, proxy); err != nil {
		return nil, err
	}
	return ctx, nil
}

func (a *TaskAdaptor) batchQueryContext(key, baseURL, proxy string, tasks []map[string]any) (map[string]any, error) {
	ctx := map[string]any{"baseUrl": baseURL, "tasks": tasks}
	if err := a.applyUpstreamCredentials(ctx, a.channelType(), key, proxy); err != nil {
		return nil, err
	}
	return ctx, nil
}

func (a *TaskAdaptor) queryCredentials() (key, baseURL, proxy string) {
	if a.info == nil || !a.info.HasChannelMeta() {
		return "", "", ""
	}
	return a.info.ApiKey, a.info.ChannelBaseUrl, a.info.ChannelSetting.Proxy
}

// channelType is the executing channel's type when polling or content
// fetches run with channel metadata; zero otherwise, which is a vendor upstream.
func (a *TaskAdaptor) channelType() int {
	if a.info == nil || !a.info.HasChannelMeta() {
		return 0
	}
	return a.info.ChannelType
}

// applyUpstreamCredentials sets ctx.upstream together with the credentials
// every driver hook reads. A New API channel (type 60) is another gateway:
// the plugin must address its own prefixed native routes there, and the
// channel key is that gateway's token, so it is sent as a Bearer header for
// every auth type instead of running the plugin's vendor auth scheme.
func (a *TaskAdaptor) applyUpstreamCredentials(ctx map[string]any, channelType int, key, proxy string) error {
	if channelType == constant.ChannelTypeNewAPI {
		ctx["upstream"] = map[string]any{"kind": pluginruntime.UpstreamKindNewAPI}
		ctx["auth"] = map[string]any{"authHeader": "Bearer " + key}
		ctx["authHeader"] = "Bearer " + key
		ctx["apiKey"] = key
		return nil
	}
	ctx["upstream"] = map[string]any{"kind": pluginruntime.UpstreamKindVendor}
	auth, err := resolveAuth(a.plugin.Meta.Auth, key, proxy)
	if err != nil {
		return err
	}
	ctx["auth"] = auth
	ctx["authHeader"] = auth["authHeader"]
	if a.plugin.Meta.Auth.Type == "" || a.plugin.Meta.Auth.Type == "none" || a.plugin.Meta.Auth.Type == "api_key" {
		ctx["apiKey"] = key
	}
	return nil
}

func hookHTTPResponse(resp *http.Response) map[string]any {
	headers := map[string]string{}
	status := 0
	if resp != nil {
		status = resp.StatusCode
		for name, values := range resp.Header {
			if len(values) > 0 {
				headers[name] = values[0]
			}
		}
	}
	return map[string]any{"status": status, "headers": headers}
}

func encodeReturnedPluginState(value any) ([]byte, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	state, exists := object["state"]
	if !exists || state == nil {
		return nil, false
	}
	data, err := common.Marshal(state)
	if err != nil {
		return nil, false
	}
	return data, true
}

func validateTaskArtifacts(value any) ([]channel.TaskArtifact, error) {
	encoded, err := common.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("plugin returned invalid artifacts")
	}
	if common.GetJsonType(encoded) != "array" {
		return nil, fmt.Errorf("plugin listArtifacts must return an array")
	}
	var items []map[string]any
	if err = common.Unmarshal(encoded, &items); err != nil {
		return nil, fmt.Errorf("plugin listArtifacts must return an array")
	}
	if len(items) > maxTaskArtifacts {
		return nil, fmt.Errorf("plugin returned too many artifacts")
	}
	artifacts := make([]channel.TaskArtifact, 0, len(items))
	keys := make(map[string]struct{}, len(items))
	for _, item := range items {
		for field := range item {
			if field != "key" && field != "type" && field != "mimeType" {
				return nil, fmt.Errorf("plugin artifact contains unsupported field %q", field)
			}
		}
		key, keyOK := item["key"].(string)
		artifactType, typeOK := item["type"].(string)
		if !keyOK || !taskArtifactKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("plugin artifact has invalid key")
		}
		if _, exists := keys[key]; exists {
			return nil, fmt.Errorf("plugin artifact keys must be unique")
		}
		keys[key] = struct{}{}
		switch artifactType {
		case "video", "audio", "image", "file":
		default:
			if !typeOK {
				return nil, fmt.Errorf("plugin artifact has invalid type")
			}
			return nil, fmt.Errorf("plugin artifact has unsupported type")
		}
		mimeType := ""
		if rawMimeType, exists := item["mimeType"]; exists {
			var mimeTypeOK bool
			mimeType, mimeTypeOK = rawMimeType.(string)
			if !mimeTypeOK {
				return nil, fmt.Errorf("plugin artifact has invalid mimeType")
			}
		}
		artifacts = append(artifacts, channel.TaskArtifact{
			Key:      key,
			Type:     artifactType,
			MimeType: mimeType,
		})
	}
	return artifacts, nil
}

func (a *TaskAdaptor) buildSubmit(c *gin.Context, info *relaycommon.RelayInfo) (*requestDescriptor, error) {
	if a.submit != nil {
		return a.submit, nil
	}
	started := time.Now()
	value, err := a.plugin.Engine.Call(c.Request.Context(), "buildSubmitRequest", a.submitContext(c, info))
	if err != nil {
		logger.LogDebug(
			c,
			"task_plugin subsystem=adaptor event=build_submit_failed plugin=%q stage=build_submit_request reason=hook_failed elapsed_ms=%d",
			a.plugin.Meta.Key,
			time.Since(started).Milliseconds(),
		)
		return nil, err
	}
	var descriptor requestDescriptor
	if err = convert(value, &descriptor); err != nil {
		logger.LogDebug(
			c,
			"task_plugin subsystem=adaptor event=build_submit_failed plugin=%q stage=build_submit_request reason=invalid_descriptor elapsed_ms=%d",
			a.plugin.Meta.Key,
			time.Since(started).Milliseconds(),
		)
		return nil, err
	}
	if descriptor.ResponseType == "" {
		descriptor.ResponseType = "json"
	}
	allowed := a.plugin.Meta.SubmitResponseTypes
	if len(allowed) == 0 {
		allowed = []string{"json"}
	}
	if !slices.Contains(allowed, descriptor.ResponseType) {
		return nil, fmt.Errorf("plugin does not support submit response type %q", descriptor.ResponseType)
	}
	if strings.TrimSpace(descriptor.URL) == "" {
		logger.LogDebug(c, "task_plugin subsystem=adaptor event=build_submit_failed plugin=%q stage=validate_url reason=empty_url", a.plugin.Meta.Key)
		return nil, fmt.Errorf("plugin returned an empty submit URL")
	}
	if err = pluginruntime.ValidateRequestURL(descriptor.URL, info.ChannelBaseUrl, a.plugin.Meta.AllowedHosts); err != nil {
		logger.LogDebug(c, "task_plugin subsystem=adaptor event=build_submit_failed plugin=%q stage=validate_url reason=url_not_allowed", a.plugin.Meta.Key)
		return nil, err
	}
	if descriptor.Action != "" {
		info.Action = descriptor.Action
	}
	if descriptor.Model != "" {
		if _, pinnedEndpoint := c.Get(pluginruntime.ContextKeyPinnedEndpoint); pinnedEndpoint {
			resolvedModel := c.GetString("resolved_task_model")
			if resolvedModel == "" || descriptor.Model != resolvedModel {
				logger.LogDebug(
					c,
					"task_plugin subsystem=adaptor event=build_submit_failed plugin=%q stage=model_pin reason=model_mismatch",
					a.plugin.Meta.Key,
				)
				return nil, fmt.Errorf("plugin submit model does not match the pinned endpoint model")
			}
		}
		info.OriginModelName = descriptor.Model
	}
	if descriptor.RewriteModel != "" {
		info.UpstreamModelName = descriptor.RewriteModel
	}
	a.submit = &descriptor
	method := strings.ToUpper(strings.TrimSpace(descriptor.Method))
	if method == "" {
		method = http.MethodPost
	}
	logger.LogDebug(
		c,
		"task_plugin subsystem=adaptor event=build_submit_complete plugin=%q method=%q body_type=%q parts=%d model=%q action_present=%t rewrite_model=%t elapsed_ms=%d",
		a.plugin.Meta.Key,
		method,
		descriptor.BodyType,
		len(descriptor.Parts),
		info.OriginModelName,
		info.Action != "",
		descriptor.RewriteModel != "",
		time.Since(started).Milliseconds(),
	)
	return a.submit, nil
}

func (a *TaskAdaptor) submitContext(c *gin.Context, info *relaycommon.RelayInfo) map[string]any {
	routeRequest := pluginruntime.RouteRequestContext{
		Params:      map[string]string{},
		Query:       map[string][]string{},
		RequestBody: map[string]any{},
	}
	requestHeaders := map[string]string{}
	files := make([]map[string]any, 0)
	if a.routeRequest != nil {
		routeRequest = *a.routeRequest
		requestHeaders = make(map[string]string, len(a.requestHeaders))
		maps.Copy(requestHeaders, a.requestHeaders)
		files = append(files, a.files...)
	}
	if c != nil {
		requestHeaders = map[string]string{}
		files = make([]map[string]any, 0)
		if prepared, exists := c.Get(pluginruntime.ContextKeyRouteRequest); exists {
			if canonical, ok := prepared.(pluginruntime.RouteRequestContext); ok {
				routeRequest = canonical
			}
		}
		if taskRequest, exists := c.Get("task_request"); exists {
			routeRequest.RequestBody = jsonValue(taskRequest)
		}
		if c.Request != nil {
			if routeRequest.Path == "" {
				routeRequest.Path = c.Request.URL.Path
			}
			if routeRequest.Method == "" {
				routeRequest.Method = c.Request.Method
			}
			if len(routeRequest.Params) == 0 {
				routeRequest.Params = make(map[string]string, len(c.Params))
				for _, param := range c.Params {
					routeRequest.Params[param.Key] = param.Value
				}
			}
			if len(routeRequest.Query) == 0 {
				routeRequest.Query = make(map[string][]string, len(c.Request.URL.Query()))
				for key, values := range c.Request.URL.Query() {
					routeRequest.Query[key] = append([]string(nil), values...)
				}
			}
			requestHeaders["Content-Type"] = c.GetHeader("Content-Type")
			requestHeaders["Accept"] = c.GetHeader("Accept")
			if strings.Contains(c.GetHeader("Content-Type"), "multipart/form-data") {
				if form, err := common.ParseMultipartFormReusable(c); err == nil {
					defer form.RemoveAll()
					for field, headers := range form.File {
						for index, header := range headers {
							files = append(files, map[string]any{"ref": pluginruntime.FileReference(field, index), "field": field, "filename": header.Filename, "mimeType": header.Header.Get("Content-Type"), "size": header.Size})
						}
					}
				}
			}
		}
		snapshot := routeRequest
		a.routeRequest = &snapshot
		a.requestHeaders = make(map[string]string, len(requestHeaders))
		maps.Copy(a.requestHeaders, requestHeaders)
		a.files = append(a.files[:0], files...)
	}
	ctx := routeRequest.JSValue()
	ctx["requestBody"] = jsonValue(routeRequest.RequestBody)
	ctx["requestHeaders"] = requestHeaders
	ctx["files"] = files
	ctx["action"] = info.Action
	ctx["originTaskId"] = info.OriginTaskID
	if info.TaskRelayInfo != nil && len(info.OriginTasks) > 0 {
		originTasks := make([]map[string]any, 0, len(info.OriginTasks))
		for _, ref := range info.OriginTasks {
			var data any
			if len(ref.Data) > 0 {
				if err := common.Unmarshal(ref.Data, &data); err != nil {
					data = nil
				}
			}
			originTasks = append(originTasks, map[string]any{
				"taskId":         ref.TaskID,
				"upstreamTaskId": ref.UpstreamTaskID,
				"action":         ref.Action,
				"status":         ref.Status,
				"data":           data,
			})
		}
		ctx["originTasks"] = originTasks
	}
	ctx["publicTaskId"] = info.PublicTaskID
	ctx["model"] = info.OriginModelName
	ctx["upstreamModel"] = info.UpstreamModelName
	ctx["baseUrl"] = info.ChannelBaseUrl
	ctx["userSetting"] = info.UserSetting
	if err := a.applyUpstreamCredentials(ctx, info.ChannelType, info.ApiKey, info.ChannelSetting.Proxy); err != nil {
		ctx["authError"] = err.Error()
	}
	return ctx
}

func (a *TaskAdaptor) usageRatios(ctx context.Context, models []string, hook string, args ...any) (map[string]float64, error) {
	if !a.hasHook(ctx, hook) {
		return nil, nil
	}
	started := time.Now()
	value, err := a.plugin.Engine.Call(ctx, hook, args...)
	if err != nil {
		logger.LogDebug(ctx, "task_plugin subsystem=adaptor event=usage_hook_failed plugin=%q hook=%q reason=hook_failed elapsed_ms=%d", a.plugin.Meta.Key, hook, time.Since(started).Milliseconds())
		return nil, fmt.Errorf("plugin usage hook failed")
	}
	if value == nil {
		return nil, nil
	}
	facts, ok := value.(map[string]any)
	if !ok {
		logger.LogDebug(ctx, "task_plugin subsystem=adaptor event=usage_hook_failed plugin=%q hook=%q reason=result_not_object elapsed_ms=%d", a.plugin.Meta.Key, hook, time.Since(started).Milliseconds())
		return nil, fmt.Errorf("plugin usage hook must return an object")
	}
	ratios, err := a.validatedUsageRatios(facts, models...)
	if err != nil {
		logger.LogDebug(ctx, "task_plugin subsystem=adaptor event=usage_hook_failed plugin=%q hook=%q reason=invalid_usage elapsed_ms=%d", a.plugin.Meta.Key, hook, time.Since(started).Milliseconds())
		return nil, err
	}
	logger.LogDebug(
		ctx,
		"task_plugin subsystem=adaptor event=usage_hook_complete plugin=%q hook=%q facts=%d positive_ratios=%d elapsed_ms=%d",
		a.plugin.Meta.Key,
		hook,
		len(facts),
		len(ratios),
		time.Since(started).Milliseconds(),
	)
	return ratios, nil
}

func (a *TaskAdaptor) validateResolvedUsageRequest(request any, models ...string) error {
	schema, _ := a.plugin.Meta.UsageForModels(models...)
	return a.validateResolvedUsageValue(jsonValue(request), schema)
}

func (a *TaskAdaptor) validateResolvedUsageValue(value any, usageSchema map[string]pluginruntime.UsageFieldSchema) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if schema, declared := usageSchema[key]; declared {
				if _, err := validateUsageValue(item, schema, true); err != nil {
					return err
				}
			} else if limit, canonical := canonicalUsageLimit(key); canonical {
				if err := validateUsageLimit(item, limit, true); err != nil {
					return err
				}
			}
			if err := a.validateResolvedUsageValue(item, usageSchema); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := a.validateResolvedUsageValue(item, usageSchema); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *TaskAdaptor) validatedUsageRatios(facts map[string]any, models ...string) (map[string]float64, error) {
	usageSchema, _ := a.plugin.Meta.UsageForModels(models...)
	ratios := make(map[string]float64)
	for key, value := range facts {
		if schema, declared := usageSchema[key]; declared {
			number, err := validateUsageValue(value, schema, false)
			if err != nil {
				return nil, err
			}
			if schema.Type == "number" {
				facts[key] = number
				if number > 0 {
					ratios[key] = number
				}
			}
			continue
		}
		number, numeric := usageNumber(value, false)
		if !numeric {
			continue
		}
		limit, canonical := canonicalUsageLimit(key)
		if !canonical {
			// Undeclared numeric facts remain extensible, but still use the
			// largest canonical task multiplier ceiling so they cannot be
			// unbounded before quota calculation.
			limit = relaycommon.MaxTaskDurationSeconds
		}
		if err := validateUsageNumberLimit(number, limit); err != nil {
			return nil, err
		}
		// Keep numeric facts usable by existing expressions even when the
		// selected profile no longer declares that legacy extension field.
		facts[key] = number
		if number > 0 {
			ratios[key] = number
		}
	}
	return ratios, nil
}

func (a *TaskAdaptor) validatedCompletionUsageFacts(facts any, models ...string) (map[string]any, error) {
	usageSchema, _ := a.plugin.Meta.UsageForModels(models...)
	if facts == nil {
		return nil, nil
	}
	values, ok := facts.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("plugin usage hook must return an object")
	}
	validated := make(map[string]any, len(values))
	for key, value := range values {
		validated[key] = value
		if schema, declared := usageSchema[key]; declared {
			number, err := validateUsageValue(value, schema, false)
			if err != nil {
				return nil, err
			}
			if schema.Type == "number" {
				validated[key] = number
			}
			continue
		}
		if limit, canonical := canonicalUsageLimit(key); canonical {
			number, numeric := usageNumber(value, false)
			if !numeric {
				return nil, fmt.Errorf("plugin usage value must be a number")
			}
			if err := validateUsageNumberLimit(number, limit); err != nil {
				return nil, err
			}
			validated[key] = number
			continue
		}
		switch key {
		case "upstreamUnits", "completionTokens", "totalTokens":
			number, numeric := usageNumber(value, false)
			if !numeric || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
				return nil, fmt.Errorf("plugin usage value must be a finite non-negative number")
			}
			validated[key] = float64(common.QuotaFromFloat(number))
		default:
			if number, numeric := usageNumber(value, false); numeric {
				validated[key] = number
			}
		}
	}
	return validated, nil
}

func validateUsageValue(value any, schema pluginruntime.UsageFieldSchema, allowNumericString bool) (float64, error) {
	if len(schema.Enum) > 0 {
		text, ok := value.(string)
		if !ok {
			return 0, fmt.Errorf("plugin usage enum must be a string")
		}
		if slices.Contains(schema.Enum, text) {
			return 0, nil
		}
		return 0, fmt.Errorf("plugin usage enum is not an allowed value")
	}
	if schema.Type == "boolean" {
		if _, ok := value.(bool); !ok {
			return 0, fmt.Errorf("plugin usage value must be a boolean")
		}
		return 0, nil
	}
	number, ok := usageNumber(value, allowNumericString)
	if !ok {
		return 0, fmt.Errorf("plugin usage value must be a number")
	}
	if schema.Unit == "token" || schema.Unit == "credit" {
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			return 0, fmt.Errorf("plugin usage value must be a finite non-negative number")
		}
		// Bound-check with QuotaFromFloatChecked (int32 saturation) but keep
		// the original fractional part so credit facts like 3.5 survive.
		if quota, clamp := common.QuotaFromFloatChecked(number); clamp != nil {
			return float64(quota), nil
		}
		return number, nil
	}
	limit := relaycommon.MaxTaskDurationSeconds
	if schema.Unit == "count" {
		limit = kitdto.MaxImageN
	}
	if err := validateUsageNumberLimit(number, limit); err != nil {
		return 0, err
	}
	return number, nil
}

func validateUsageLimit(value any, limit int, allowNumericString bool) error {
	number, ok := usageNumber(value, allowNumericString)
	if !ok {
		return fmt.Errorf("plugin usage value must be a number")
	}
	return validateUsageNumberLimit(number, limit)
}

func validateUsageNumberLimit(number float64, limit int) error {
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return fmt.Errorf("plugin usage value must be a finite non-negative number")
	}
	if number > float64(limit) {
		return fmt.Errorf("plugin usage value exceeds the host limit")
	}
	return nil
}

func usageNumber(value any, allowNumericString bool) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int64:
		return float64(number), true
	case int:
		return float64(number), true
	case string:
		if !allowNumericString {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func canonicalUsageLimit(key string) (int, bool) {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	switch normalized {
	case "duration", "durationseconds", "second", "seconds":
		return relaycommon.MaxTaskDurationSeconds, true
	case "n", "count", "imagecount", "samplecount", "batchcount", "numimages":
		return kitdto.MaxImageN, true
	default:
		return 0, false
	}
}

func (a *TaskAdaptor) logRejectedUsage(hook string, _ error) {
	common.SysError(fmt.Sprintf("task plugin %s rejected invalid %s billing facts", a.plugin.Meta.Key, hook))
}

func (a *TaskAdaptor) hasHook(ctx context.Context, hook string) bool {
	has, err := a.plugin.Engine.HasExport(ctx, hook)
	return err == nil && has
}
func convert(value any, target any) error {
	data, err := common.Marshal(value)
	if err != nil {
		return err
	}
	return common.Unmarshal(data, target)
}

func jsonValue(value any) any {
	if normalized, ok := cloneJSONValue(value, 0); ok {
		return normalized
	}
	data, err := common.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if err = common.Unmarshal(data, &normalized); err != nil {
		return value
	}
	return normalized
}

// Already-parsed JSON and Sobek's plain exported values do not need another
// encode/decode pass. Copy containers to isolate hooks, preserving the codec's
// float64 numbers and nulls. Other Go types still use the configured codec.
func cloneJSONValue(value any, depth int) (any, bool) {
	if depth > 64 {
		return nil, false
	}
	switch typed := value.(type) {
	case nil, bool:
		return typed, true
	case string:
		return typed, utf8.ValidString(typed)
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case int64:
		return float64(typed), true
	case int:
		return float64(typed), true
	case map[string]any:
		if typed == nil {
			return nil, true
		}
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			if !utf8.ValidString(key) {
				return nil, false
			}
			child, ok := cloneJSONValue(item, depth+1)
			if !ok {
				return nil, false
			}
			cloned[key] = child
		}
		return cloned, true
	case []any:
		if typed == nil {
			return nil, true
		}
		cloned := make([]any, len(typed))
		for index, item := range typed {
			child, ok := cloneJSONValue(item, depth+1)
			if !ok {
				return nil, false
			}
			cloned[index] = child
		}
		return cloned, true
	default:
		return nil, false
	}
}
func positiveInt(value any) int {
	switch number := value.(type) {
	case int64:
		if number <= 0 {
			return 0
		}
		return common.QuotaFromFloat(float64(number))
	case float64:
		if number <= 0 {
			return 0
		}
		return common.QuotaFromFloat(number)
	default:
		return 0
	}
}

var _ channel.TaskAdaptor = (*TaskAdaptor)(nil)
var _ channel.OpenAIVideoConverter = (*TaskAdaptor)(nil)
var _ channel.TaskArtifactProvider = (*TaskAdaptor)(nil)
var _ channel.TaskContentRequestProvider = (*TaskAdaptor)(nil)
var _ channel.TaskUsageFactsProvider = (*TaskAdaptor)(nil)
var _ channel.TaskValidatedBillingProvider = (*TaskAdaptor)(nil)
var _ channel.TaskValidatedUsageFactsProvider = (*TaskAdaptor)(nil)
