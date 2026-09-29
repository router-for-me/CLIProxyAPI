package jsplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/plugins"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mockPlugin = `
export const meta = {
  apiVersion: 1, key: "mock-task", name: "Mock Task", version: "1.0.0",
  author: {name: "Test"},
  channelTypes: [1001], models: ["mock-v1"], fetchMode: "per_task",
  protocols: ["openai_video"],
  usageSchema: {seconds: {type: "number", unit: "second"}, mode: {enum: ["std", "pro"]}},
};
export function buildSubmitRequest(ctx) {
  if (!ctx.requestBody.prompt) throw new Error("prompt required");
  return { url: ctx.baseUrl + "/submit", method: "POST", headers: {"X-Plugin": "submit"}, body: {prompt: ctx.requestBody.prompt}, action: "text_to_video", model: "mock-v1", rewriteModel: "mock-upstream" };
}

export function parseSubmitResponse(ctx, resp) {
  return {
    taskId: resp.body.id,
    taskData: {accepted: true, status: resp.statusCode},
  };
}
export function extractUsage(ctx) { return {seconds: 5, mode: "pro"}; }
export function extractUsageOnSubmit(ctx, data) { return {seconds: data.seconds || 7}; }
export function extractUsageOnComplete(task, result) { return {upstreamUnits: 23}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl + "/tasks/" + ctx.taskId, method: "GET", headers: {"X-Plugin": "query"}}; }
export function parseTaskResult(ctx, body) { return {taskId: body.id, status: "SUCCESS", progress: "100%", url: body.url}; }
export function listArtifacts() { return []; }
export function buildContentRequest() { throw new Error("artifact_not_found"); }
export const protocols = {openai_video: {
  decodeRequest: function(ctx) { return {kind: "submit", model: ctx.model, requestBody: ctx.body.value}; },
  render: function(ctx, task) { return {id: task.task_id, status: "completed"}; }
}};
`

func TestTaskAdaptorRejectsDeprecatedClientResponse(t *testing.T) {
	source := strings.Replace(mockPlugin, `taskData: {accepted: true, status: resp.statusCode},`, `taskData: {}, clientResponse: {id: ctx.publicTaskId},`, 1)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"}}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"upstream"}`))}

	parsed, taskErr := adaptor.ParseResponse(c, response, info)

	assert.Nil(t, parsed)
	require.NotNil(t, taskErr)
	require.Error(t, taskErr.Error)
	assert.Contains(t, taskErr.Error.Error(), "must not return clientResponse")
}

func TestTaskAdaptorBuildsMultipartFromOpaqueFileReference(t *testing.T) {
	source := `
export const meta = {apiVersion:1,key:"multipart",name:"Multipart",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) { return {url:ctx.baseUrl+"/submit",bodyType:"multipart",parts:[{name:"model",value:"m"},{name:"input_reference",fileRef:ctx.files[0].ref}]}; }
export function parseSubmitResponse(ctx,r){return {taskId:"1"}} export function buildQueryRequest(){return {url:"https://example.com"}} export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	adaptor.Init(info)
	var input bytes.Buffer
	writer := multipart.NewWriter(&input)
	file, err := writer.CreateFormFile("input_reference", "ref.png")
	require.NoError(t, err)
	_, err = file.Write([]byte("image-bytes"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(input.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "p"})
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	requestBytes, err := io.ReadAll(body)
	require.NoError(t, err)
	reader := multipart.NewReader(bytes.NewReader(requestBytes), strings.TrimPrefix(c.GetHeader("Content-Type"), "multipart/form-data; boundary="))
	form, err := reader.ReadForm(1024)
	require.NoError(t, err)
	assert.Equal(t, []string{"m"}, form.Value["model"])
	require.Len(t, form.File["input_reference"], 1)
	opened, err := form.File["input_reference"][0].Open()
	require.NoError(t, err)
	content, err := io.ReadAll(opened)
	require.NoError(t, err)
	assert.Equal(t, "image-bytes", string(content))
}

func TestTaskAdaptorInlinesJSONFilePlaceholders(t *testing.T) {
	const fileBytes = "image-bytes"
	encoded := base64.StdEncoding.EncodeToString([]byte(fileBytes))
	source := `
export const meta = {apiVersion:1,key:"json-inline",name:"JSON Inline",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url:ctx.baseUrl+"/submit",body:{
    prompt:"p",
    image:{__fileRef:ctx.files[0].ref,encoding:"base64"},
    nested:{items:[{__fileRef:ctx.files[0].ref,encoding:"dataUrl",mimeType:"image/png"}]},
    dataUrl:{__fileRef:ctx.files[0].ref,encoding:"dataUrl"},
    refs:ctx.files.map(function(file){return file.ref;})
  }};
}
export function parseSubmitResponse(){return {taskId:"1"}} export function buildQueryRequest(){return {url:"https://example.com"}} export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	adaptor.Init(info)
	c := newMultipartFileContext(t, "input_reference", "ref.png", "image/jpeg", []byte(fileBytes))
	c.Set("task_request", map[string]any{"prompt": "p"})
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	requestBytes, err := io.ReadAll(body)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(requestBytes, &decoded))
	assert.Equal(t, "p", decoded["prompt"])
	assert.Equal(t, encoded, decoded["image"])
	nested := decoded["nested"].(map[string]any)
	items := nested["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "data:image/png;base64,"+encoded, items[0])
	assert.Equal(t, "data:image/jpeg;base64,"+encoded, decoded["dataUrl"])
	assert.Equal(t, []any{"request_file:input_reference"}, decoded["refs"])

	t.Run("repeated field files are addressed by index", func(t *testing.T) {
		second := base64.StdEncoding.EncodeToString([]byte("second-bytes"))
		source := `
export const meta = {apiVersion:1,key:"json-inline-repeat",name:"JSON Inline Repeat",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url:ctx.baseUrl+"/submit",body:{
    refs:ctx.files.map(function(file){return file.ref;}),
    images:ctx.files.map(function(file){return {__fileRef:file.ref,encoding:"base64"};}),
    missing:{__fileRef:"request_file:image[]#2",encoding:"base64"}
  }};
}
export function parseSubmitResponse(){return {taskId:"1"}} export function buildQueryRequest(){return {url:"https://example.com"}} export function parseTaskResult(){return {status:"SUCCESS"}}
`
		plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
		require.NoError(t, err)
		adaptor := New(plugin)
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
		adaptor.Init(info)
		var input bytes.Buffer
		writer := multipart.NewWriter(&input)
		for _, content := range []string{fileBytes, "second-bytes"} {
			file, createErr := writer.CreateFormFile("image[]", "ref.png")
			require.NoError(t, createErr)
			_, err = file.Write([]byte(content))
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(input.Bytes()))
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		c.Set("task_request", map[string]any{"prompt": "p"})
		_, err = adaptor.BuildRequestBody(c, info)
		require.ErrorContains(t, err, `unknown file reference "request_file:image[]#2"`, "an index beyond the uploaded files is rejected")

		source = strings.Replace(source, `,
    missing:{__fileRef:"request_file:image[]#2",encoding:"base64"}`, "", 1)
		plugin, err = pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
		require.NoError(t, err)
		adaptor = New(plugin)
		adaptor.Init(info)
		body, err := adaptor.BuildRequestBody(c, info)
		require.NoError(t, err)
		requestBytes, err := io.ReadAll(body)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, common.Unmarshal(requestBytes, &decoded))
		assert.Equal(t, []any{"request_file:image[]", "request_file:image[]#1"}, decoded["refs"])
		assert.Equal(t, []any{encoded, second}, decoded["images"])
	})
}

func TestTaskAdaptorJSONFilePlaceholderErrors(t *testing.T) {
	tests := []struct {
		name        string
		part        string
		fileSize    int
		globalMB    int
		wantContain string
	}{
		{name: "unknown ref", part: `{__fileRef:"request_file:missing",encoding:"base64"}`, fileSize: 4, wantContain: `unknown file reference "request_file:missing"`},
		{name: "extra key", part: `{__fileRef:"request_file:input_reference",encoding:"base64",extra:true}`, fileSize: 4, wantContain: "invalid file placeholder"},
		{name: "missing encoding", part: `{__fileRef:"request_file:input_reference"}`, fileSize: 4, wantContain: "encoding"},
		{name: "oversize maxBytes", part: `{__fileRef:"request_file:input_reference",encoding:"base64",maxBytes:3}`, fileSize: 4, wantContain: "3 byte limit"},
		{name: "oversize global", part: `{__fileRef:"request_file:input_reference",encoding:"base64"}`, fileSize: 2 << 20, globalMB: 1, wantContain: "1048576 byte limit"},
		{name: "multiple references cap", part: `{a:{__fileRef:"request_file:input_reference",encoding:"base64"},b:{__fileRef:"request_file:input_reference",encoding:"base64"}}`, fileSize: 700 << 10, globalMB: 1, wantContain: "1048576 byte limit"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.globalMB > 0 {
				previous := constant.MaxFileDownloadMB
				constant.MaxFileDownloadMB = testCase.globalMB
				t.Cleanup(func() { constant.MaxFileDownloadMB = previous })
			}
			source := strings.Replace(`
export const meta = {apiVersion:1,key:"json-inline-err",name:"JSON Inline Err",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest() { return {url:"https://provider.example/submit",body:PLACEHOLDER}; }
export function parseSubmitResponse(){return {taskId:"1"}} export function buildQueryRequest(){return {url:"https://example.com"}} export function parseTaskResult(){return {status:"SUCCESS"}}
`, "PLACEHOLDER", testCase.part, 1)
			plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			adaptor := New(plugin)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor.Init(info)
			c := newMultipartFileContext(t, "input_reference", "ref.bin", "application/octet-stream", bytes.Repeat([]byte("x"), testCase.fileSize))
			c.Set("task_request", map[string]any{"prompt": "p"})
			_, err = adaptor.BuildRequestBody(c, info)
			require.Error(t, err)
			assert.Contains(t, err.Error(), testCase.wantContain)
		})
	}
}

func newMultipartFileContext(t *testing.T, field, filename, contentType string, content []byte) *gin.Context {
	t.Helper()
	var input bytes.Buffer
	writer := multipart.NewWriter(&input)
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="` + field + `"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(input.Bytes()))
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	return c
}

func TestTaskAdaptorDoesNotEmitInjectedMultipartDispositionHeaders(t *testing.T) {
	tests := []struct {
		name string
		part string
	}{
		{name: "part name", part: `{name:"prompt\r\nX-Injected: yes",value:"hello"}`},
		{name: "filename", part: `{name:"input_reference",fileRef:"request_file:input_reference",filename:"safe.png\r\nX-Injected: yes"}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			source := strings.Replace(`
export const meta = {apiVersion:1,key:"multipart-safe",name:"Multipart Safe",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) { return {url:ctx.baseUrl+"/submit",bodyType:"multipart",parts:[PART]}; }
export function parseSubmitResponse(){return {taskId:"1"}} export function buildQueryRequest(){return {}} export function parseTaskResult(){return {status:"SUCCESS"}}
`, "PART", testCase.part, 1)
			plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			adaptor := New(plugin)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor.Init(info)
			var input bytes.Buffer
			writer := multipart.NewWriter(&input)
			file, err := writer.CreateFormFile("input_reference", "input.png")
			require.NoError(t, err)
			_, err = file.Write([]byte("image"))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewReader(input.Bytes()))
			c.Request.Header.Set("Content-Type", writer.FormDataContentType())
			c.Set("task_request", map[string]any{"model": "m"})

			body, buildErr := adaptor.BuildRequestBody(c, info)
			if buildErr != nil {
				assert.Contains(t, buildErr.Error(), "multipart")
				return
			}
			encoded, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "\r\nX-Injected: yes")
		})
	}
}

func TestTaskAdaptorRejectsPostDistributionEndpointModelDrift(t *testing.T) {
	source := `
export const meta = {apiVersion:1,key:"endpoint-drift",name:"Endpoint Drift",version:"1.0.0",author:{name:"Test"},models:["claimed-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url:ctx.baseUrl+"/submit",method:"POST",model:"outside-model",rewriteModel:"allowed-upstream-rewrite"};
}

export function parseSubmitResponse(){return {taskId:"1"}}
export function buildQueryRequest(){return {url:"https://example.com"}}
export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
		OriginModelName: "claimed-model",
	}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("task_request", map[string]any{"model": "claimed-model"})
	c.Set("resolved_task_model", "claimed-model")
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{
		Plugin: plugin,
	})

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, "claimed-model", info.OriginModelName)
	assert.Contains(t, taskErr.Message, "does not match")
}

func TestTaskAdaptorReDecodesFinalCandidateAndRejectsModelDrift(t *testing.T) {
	source := `
export const meta = {apiVersion:1,key:"redecode",name:"Redecode",version:"1.0.0",author:{name:"Test"},models:["claimed-model"],fetchMode:"per_task",protocols:[{name:"openai_responses",supports:["sync","background"]}]};
let calls = 0;
export const protocols = {openai_responses:{decodeRequest:function(ctx){calls++;return {kind:"submit",model:calls === 1 ? ctx.model : "drifted-model",requestBody:ctx.body.value};},renderFinal:function(){return {};}}};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit"}} export function parseSubmitResponse(){return {taskId:"one"}} export function buildQueryRequest(){return {}} export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	protocolContext := pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{Body: map[string]any{"kind": "json", "value": map[string]any{"model": "claimed-model"}}, RequestBody: map[string]any{"model": "claimed-model"}},
		Protocol:            "openai_responses", Model: "claimed-model",
	}
	_, err = plugin.Engine.CallPath(context.Background(), "protocols", []string{"openai_responses", "decodeRequest"}, protocolContext.JSValue())
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Plugin: plugin, Protocol: "openai_responses", Model: "claimed-model"})
	c.Set(pluginruntime.ContextKeyProtocolRequest, protocolContext)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}, OriginModelName: "claimed-model"}
	adaptor := New(plugin)
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
	assert.Contains(t, taskErr.Message, "pinned model")
}

func TestTaskAdaptorRejectsRendererFromFinalProtocolDecoder(t *testing.T) {
	source := `
export const meta = {apiVersion:1,key:"renderer-reject",name:"Renderer Reject",version:"1.0.0",author:{name:"Test"},models:["claimed-model"],fetchMode:"per_task",protocols:[{name:"openai_responses",supports:["sync","background"]}]};
export const protocols = {openai_responses:{decodeRequest:function(ctx){return {kind:"submit",model:ctx.model,requestBody:ctx.body.value,renderer:"legacy"};},renderFinal:function(){return {};}}};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit"}} export function parseSubmitResponse(){return {taskId:"one"}} export function buildQueryRequest(){return {}} export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	protocolContext := pluginruntime.ProtocolRequestContext{
		RouteRequestContext: pluginruntime.RouteRequestContext{Body: map[string]any{"kind": "json", "value": map[string]any{"model": "claimed-model"}}},
		Protocol:            "openai_responses", Model: "claimed-model",
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(pluginruntime.ContextKeyPinnedEndpoint, pluginruntime.PinnedEndpoint{Plugin: plugin, Protocol: "openai_responses", Model: "claimed-model"})
	c.Set(pluginruntime.ContextKeyProtocolRequest, protocolContext)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}, OriginModelName: "claimed-model"}
	adaptor := New(plugin)
	adaptor.Init(info)

	taskErr := adaptor.ValidateRequestAndSetAction(c, info)

	require.NotNil(t, taskErr)
	assert.Equal(t, http.StatusBadRequest, taskErr.StatusCode)
	assert.Contains(t, taskErr.Message, "must not return renderer")
}

func TestTaskAdaptorBuildContentRequestHookAndMissingFallback(t *testing.T) {
	source := strings.Replace(mockPlugin, `export function listArtifacts() { return []; }
export function buildContentRequest() { throw new Error("artifact_not_found"); }`, `export function listArtifacts(task) { return [{key: "video", type: "video", mimeType: "video/mp4"}]; }
export function buildContentRequest(ctx) {
  if (ctx.data.id !== "raw-upstream" || ctx.upstreamTaskId !== "upstream-task" || ctx.producerVersion !== "0.9.0") throw new Error("bad task context");
  return {url: ctx.baseUrl + "/content/" + ctx.artifactKey, method: ctx.clientRequest.method, headers: {"X-Content": "plugin"}};
}`, 1)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example", ApiKey: "key"}})
	taskData, err := common.Marshal(map[string]any{"id": "raw-upstream"})
	require.NoError(t, err)
	task := &model.Task{
		TaskID: "task-public", Status: model.TaskStatusSuccess, Data: taskData,
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "upstream-task",
			Execution: &model.TaskExecutionSnapshot{TaskPlugin: &model.TaskPluginSnapshot{
				Version: "0.9.0",
			}},
		},
	}
	artifacts, err := adaptor.ListArtifacts(task)
	require.NoError(t, err)
	require.Equal(t, []channel.TaskArtifact{{Key: "video", Type: "video", MimeType: "video/mp4"}}, artifacts)
	descriptor, err := adaptor.BuildContentRequest(task, "video", channel.TaskArtifactClientRequest{Method: http.MethodHead})
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	assert.Equal(t, "https://provider.example/content/video", descriptor.URL)
	assert.Equal(t, http.MethodHead, descriptor.Method)
	assert.Equal(t, "plugin", descriptor.Headers["X-Content"])

	withoutHook, err := pluginruntime.NewRegistry().Register(`
export const meta = {apiVersion:1,key:"no-artifacts",name:"No Artifacts",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest(){return {url:"https://provider.example"};}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`, pluginruntime.Options{})
	require.NoError(t, err)
	fallback := New(withoutHook)
	fallback.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}})
	artifacts, err = fallback.ListArtifacts(&model.Task{})
	require.NoError(t, err)
	assert.Nil(t, artifacts)
	descriptor, err = fallback.BuildContentRequest(&model.Task{}, "video", channel.TaskArtifactClientRequest{Method: http.MethodGet})
	require.NoError(t, err)
	assert.Nil(t, descriptor)
}

func TestTaskAdaptorRejectsInvalidArtifactProjection(t *testing.T) {
	testCases := []struct {
		name       string
		projection string
	}{
		{name: "duplicate key", projection: `[{key:"video",type:"video"},{key:"video",type:"video"}]`},
		{name: "array index identity", projection: `[{key:"video",type:"video",index:0}]`},
		{name: "upstream url", projection: `[{key:"video",type:"video",url:"https://cdn.example/video.mp4"}]`},
		{name: "invalid key", projection: `[{key:"video/0",type:"video"}]`},
		{name: "unsupported type", projection: `[{key:"video",type:"text"}]`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			source := strings.Replace(mockPlugin, `export function listArtifacts() { return []; }
export function buildContentRequest() { throw new Error("artifact_not_found"); }`, `export function listArtifacts() { return `+testCase.projection+`; }
export function buildContentRequest(ctx) { return {url:ctx.baseUrl+"/content",method:"GET"}; }
`, 1)
			plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			adaptor := New(plugin)
			_, err = adaptor.ListArtifacts(&model.Task{TaskID: "task", Status: model.TaskStatusSuccess, Data: []byte(`{}`)})
			require.Error(t, err)
		})
	}
}

func TestTaskAdaptorAllowsExplicitCredentiallessCDNRequest(t *testing.T) {
	source := strings.Replace(mockPlugin, `export function listArtifacts() { return []; }
export function buildContentRequest() { throw new Error("artifact_not_found"); }`, `export function listArtifacts() { return [{key:"video",type:"video"}]; }
export function buildContentRequest(ctx) { return {url:"https://cdn.example/video.mp4",method:ctx.clientRequest.method,credentialless:true}; }
`, 1)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}})
	descriptor, err := adaptor.BuildContentRequest(
		&model.Task{TaskID: "task", Data: []byte(`{}`)},
		"video",
		channel.TaskArtifactClientRequest{Method: http.MethodGet},
	)
	require.NoError(t, err)
	require.NotNil(t, descriptor)
	assert.True(t, descriptor.Credentialless)
	assert.Equal(t, "https://cdn.example/video.mp4", descriptor.URL)
}

func TestTaskAdaptorMapsJSContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/submit":
			assert.Equal(t, "submit", r.Header.Get("X-Plugin"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			assert.JSONEq(t, `{"prompt":"hello"}`, string(body))
			_, _ = w.Write([]byte(`{"id":"upstream-1"}`))
		case "/tasks/upstream-1":
			assert.Equal(t, "query", r.Header.Get("X-Plugin"))
			_, _ = w.Write([]byte(`{"id":"upstream-1","url":"https://cdn.example/video.mp4"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registry := pluginruntime.NewRegistry()
	plugin, err := registry.Register(mockPlugin, pluginruntime.Options{Key: "mock-task", Version: "1.0.0"})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "secret"}, OriginModelName: "client-model", TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_public"}}
	adaptor.Init(info)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "hello"})
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	assert.Equal(t, "text_to_video", info.Action)
	assert.Equal(t, "mock-v1", info.OriginModelName)
	assert.Equal(t, "mock-upstream", info.UpstreamModelName)
	assert.Equal(t, []string{"mock-v1"}, adaptor.GetModelList())
	assert.Equal(t, "Mock Task", adaptor.GetChannelName())
	assert.Equal(t, map[string]float64{"seconds": 5}, adaptor.EstimateBilling(c, info))

	requestBody, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, server.URL+"/submit", url)
	req := httptest.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, adaptor.BuildRequestHeader(c, req, info))
	assert.Equal(t, "submit", req.Header.Get("X-Plugin"))

	resp, err := adaptor.DoRequest(c, info, requestBody)
	require.NoError(t, err)
	parsed, taskErr := adaptor.ParseResponse(c, resp, info)
	require.Nil(t, taskErr)
	require.NotNil(t, parsed)
	assert.Equal(t, "upstream-1", parsed.UpstreamTaskID)
	assert.JSONEq(t, `{"accepted":true,"status":200}`, string(parsed.TaskData))
	assert.Nil(t, parsed.ClientResponse)
	assert.Empty(t, recorder.Body.String(), "response parsing must not write before the durable task barrier")
	assert.Equal(t, map[string]float64{"seconds": 7}, adaptor.AdjustBillingOnSubmit(info, []byte(`{"seconds":7}`)))

	queryResp, err := adaptor.FetchTask(server.URL, "secret", &model.Task{
		Action:      info.Action,
		PrivateData: model.TaskPrivateData{UpstreamTaskID: parsed.UpstreamTaskID},
	}, "")
	require.NoError(t, err)
	queryBody, err := io.ReadAll(queryResp.Body)
	require.NoError(t, err)
	require.NoError(t, queryResp.Body.Close())
	result, err := adaptor.ParseTaskResult(&model.Task{}, queryResp, queryBody)
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", result.Status)
	assert.Equal(t, "https://cdn.example/video.mp4", result.Url)
	assert.Zero(t, adaptor.AdjustBillingOnComplete(&model.Task{}, result))
	assert.Equal(t, 23, result.TotalTokens)

	rendered, err := adaptor.ConvertToOpenAIVideo(&model.Task{TaskID: "task_public", Status: model.TaskStatusSuccess})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id":"task_public",
		"object":"video",
		"model":"",
		"status":"completed",
		"progress":0,
		"created_at":0
	}`, string(rendered))
	_, err = plugin.Engine.Export(context.Background(), "meta")
	require.NoError(t, err)
}

func TestTaskAdaptorPreservesSoraVideoResponseFields(t *testing.T) {
	source, err := plugins.Source("sora")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)

	for _, tc := range []struct {
		status model.TaskStatus
		want   string
	}{
		{model.TaskStatusInProgress, "in_progress"},
		{model.TaskStatusSuccess, "completed"},
		{model.TaskStatusFailure, "failed"},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			task := &model.Task{
				TaskID:      "task_public",
				Status:      tc.status,
				Progress:    "42%",
				CreatedAt:   100,
				FinishTime:  200,
				Properties:  model.Properties{OriginModelName: "origin-model"},
				PrivateData: model.TaskPrivateData{UpstreamTaskID: "upstream-task-id"},
				Data: []byte(`{
					"id":"upstream-task-id","task_id":"upstream-task-id",
					"object":"provider-video","model":"provider-model",
					"status":"completed","progress":100,"created_at":10,"completed_at":20,
					"url":"https://cdn.example/video.mp4",
					"metadata":{"url":"https://cdn.example/video.mp4","URL":"https://cdn.example/uppercase.mp4"},
					"provider_payload":{"task_id":"upstream-task-id","items":[{"enabled":false,"count":0,"value":null}]},
					"seconds":8,"resolution":"720p","aspect_ratio":"16:9",
					"reference_images":["https://cdn.example/reference.png"],
					"error":{"code":"provider_error","message":"provider rejected request","detail":{"retryable":false}}
				}`),
			}
			rendered, err := adaptor.ConvertToOpenAIVideo(task)
			require.NoError(t, err)

			var fields map[string]any
			require.NoError(t, common.Unmarshal(rendered, &fields))
			assert.Equal(t, "task_public", fields["id"])
			assert.NotContains(t, fields, "task_id")
			assert.Equal(t, "video", fields["object"])
			assert.Equal(t, "origin-model", fields["model"])
			assert.Equal(t, tc.want, fields["status"])
			assert.Equal(t, float64(42), fields["progress"])
			assert.Equal(t, float64(100), fields["created_at"])
			if tc.status == model.TaskStatusSuccess {
				assert.Equal(t, float64(200), fields["completed_at"])
			} else {
				assert.NotContains(t, fields, "completed_at")
			}
			assert.Equal(t, "https://cdn.example/video.mp4", fields["url"])
			assert.Equal(t, map[string]any{
				"url": "https://cdn.example/video.mp4",
				"URL": "https://cdn.example/uppercase.mp4",
			}, fields["metadata"])
			assert.Equal(t, map[string]any{
				"task_id": "task_public",
				"items":   []any{map[string]any{"enabled": false, "count": float64(0), "value": nil}},
			}, fields["provider_payload"])
			assert.Equal(t, float64(8), fields["seconds"])
			assert.Equal(t, "720p", fields["resolution"])
			assert.Equal(t, "16:9", fields["aspect_ratio"])
			assert.Equal(t, []any{"https://cdn.example/reference.png"}, fields["reference_images"])
			assert.Equal(t, map[string]any{
				"code": "provider_error", "message": "provider rejected request",
				"detail": map[string]any{"retryable": false},
			}, fields["error"])
		})
	}
}

func TestTaskAdaptorRejectsNonObjectOpenAIVideoRendererOutput(t *testing.T) {
	for _, value := range []string{"null", "[]", `"video"`, "42", "false"} {
		t.Run(value, func(t *testing.T) {
			source := strings.Replace(mockPlugin, `return {id: task.task_id, status: "completed"};`, "return "+value+";", 1)
			plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			_, err = New(plugin).ConvertToOpenAIVideo(&model.Task{TaskID: "task_public"})
			require.ErrorContains(t, err, "invalid OpenAI video object")
		})
	}
}

func TestTaskAdaptorPreservesOpenAIVideoFailureSlotsAndOwnsLifecycle(t *testing.T) {
	source := strings.Replace(mockPlugin, `render: function(ctx, task) { return {id: task.task_id, status: "completed"}; }`, `render: function() { return {id:"provider", object:"provider", model:"provider-model", status:"completed", progress:100, created_at:99, completed_at:20, error:{code:"provider_error",message:"provider rejected request"}}; }`, 1)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	task := &model.Task{
		TaskID:     "task_public",
		Status:     model.TaskStatusFailure,
		FailReason: "provider secret",
		CreatedAt:  10,
		UpdatedAt:  20,
		Properties: model.Properties{OriginModelName: "origin-model"},
	}

	rendered, err := adaptor.ConvertToOpenAIVideo(task)

	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"task_public","object":"video","model":"origin-model","status":"failed","progress":0,"created_at":10,"error":{"message":"provider rejected request","code":"provider_error"}}`, string(rendered))
}

func TestTaskAdaptorBoundsNativeUsageBeforeQuotaCalculation(t *testing.T) {
	source := `
export const meta = {
  apiVersion: 1, key: "bounded-usage", name: "Bounded Usage", version: "1.0.0",
  author: {name: "Test"},
  models: ["model"], fetchMode: "per_task",
  usageSchema: {
    duration: {type: "number", unit: "second"},
    count: {type: "number", unit: "count"},
    tokens: {type: "number", unit: "token"},
    mode: {enum: ["std", "pro"]},
  },
  usageExamples: [{label: "std · 1s", facts: {duration: 1, count: 1, tokens: 1, mode: "std"}}],
};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl + "/submit", method: "POST", body: {}};
}
export function parseSubmitResponse() { return {taskId: "1"}; }
export function buildQueryRequest() { return {url: "https://example.com"}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
export function extractUsage(ctx) {
  const entries = (ctx.requestBody || {}).hookUsageEntries || [];
  const facts = {};
  entries.forEach(function(entry) { facts[entry.name] = entry.value; });
  return facts;
}
export function extractUsageOnSubmit(ctx, data) { return (data || {}).usage || {}; }
export function extractUsageOnComplete(task, result, body) { return (body || {}).completionUsage || {}; }
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)

	newRequest := func(t *testing.T, requestBody map[string]any) (*TaskAdaptor, *gin.Context, *relaycommon.RelayInfo) {
		t.Helper()
		adaptor := New(plugin)
		info := &relaycommon.RelayInfo{
			ChannelMeta:   &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"},
			TaskRelayInfo: &relaycommon.TaskRelayInfo{},
		}
		adaptor.Init(info)
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = httptest.NewRequest(http.MethodPost, "/native/submit", nil)
		context.Set("task_request", requestBody)
		return adaptor, context, info
	}

	requestTests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "duration in resolved metadata",
			body: map[string]any{"metadata": map[string]any{"duration": relaycommon.MaxTaskDurationSeconds + 1}},
		},
		{
			name: "count in resolved metadata",
			body: map[string]any{"metadata": map[string]any{"count": dto.MaxImageN + 1}},
		},
		{
			name: "declared enum in resolved metadata",
			body: map[string]any{"metadata": map[string]any{"mode": "turbo"}},
		},
		{
			name: "implicit duration key without declaration",
			body: map[string]any{"durationSeconds": relaycommon.MaxTaskDurationSeconds + 1},
		},
		{
			name: "implicit count key without declaration",
			body: map[string]any{"image_count": dto.MaxImageN + 1},
		},
		{
			name: "negative resolved duration",
			body: map[string]any{"duration": -1},
		},
		{
			name: "non-finite resolved duration",
			body: map[string]any{"duration": math.Inf(1)},
		},
		{
			name: "metadata cannot hide behind valid top-level duration",
			body: map[string]any{
				"duration": relaycommon.MaxTaskDurationSeconds,
				"metadata": map[string]any{"duration": relaycommon.MaxTaskDurationSeconds + 1},
			},
		},
		{
			name: "nested passthrough duration",
			body: map[string]any{
				"metadata": map[string]any{
					"parameters": map[string]any{"duration": relaycommon.MaxTaskDurationSeconds + 1},
				},
			},
		},
	}
	for _, testCase := range requestTests {
		t.Run(testCase.name, func(t *testing.T) {
			adaptor, context, info := newRequest(t, testCase.body)
			taskErr := adaptor.ValidateRequestAndSetAction(context, info)
			require.NotNil(t, taskErr)
			assert.Equal(t, "plugin_usage_invalid", taskErr.Code)
		})
	}

	hookTests := []struct {
		name  string
		usage map[string]any
	}{
		{
			name:  "duration returned only by extractUsage",
			usage: map[string]any{"duration": float64(relaycommon.MaxTaskDurationSeconds + 1)},
		},
		{
			name:  "count returned only by extractUsage",
			usage: map[string]any{"count": float64(dto.MaxImageN + 1)},
		},
		{
			name:  "enum returned only by extractUsage",
			usage: map[string]any{"mode": "turbo"},
		},
		{
			name:  "undeclared numeric ratio uses conservative host ceiling",
			usage: map[string]any{"custom_ratio": float64(relaycommon.MaxTaskDurationSeconds + 1)},
		},
		{
			name:  "negative hook ratio",
			usage: map[string]any{"custom_ratio": -1.0},
		},
		{
			name:  "non-finite hook ratio",
			usage: map[string]any{"custom_ratio": math.NaN()},
		},
	}
	for _, testCase := range hookTests {
		t.Run(testCase.name, func(t *testing.T) {
			entries := make([]any, 0, len(testCase.usage))
			for key, value := range testCase.usage {
				entries = append(entries, map[string]any{"name": key, "value": value})
			}
			adaptor, context, info := newRequest(t, map[string]any{"hookUsageEntries": entries})
			require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
			ratios, err := adaptor.EstimateBillingValidated(context, info)
			require.Error(t, err)
			assert.Nil(t, ratios)
		})
	}

	t.Run("numeric strings remain valid in vendor request fields", func(t *testing.T) {
		adaptor, context, info := newRequest(t, map[string]any{
			"metadata": map[string]any{
				"duration": "5",
				"count":    "2",
				"mode":     "std",
			},
		})
		assert.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
	})

	t.Run("numeric strings from usage hooks are rejected", func(t *testing.T) {
		adaptor, context, info := newRequest(t, map[string]any{
			"hookUsageEntries": []any{map[string]any{"name": "duration", "value": "5"}},
		})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
		ratios, err := adaptor.EstimateBillingValidated(context, info)
		require.Error(t, err)
		assert.Nil(t, ratios)
	})

	t.Run("declared token facts use int32 saturation instead of duration cap", func(t *testing.T) {
		adaptor, context, info := newRequest(t, map[string]any{
			"hookUsageEntries": []any{
				map[string]any{"name": "tokens", "value": float64(500000)},
			},
		})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
		facts, err := adaptor.ExtractUsageFactsValidated(context, info)
		require.NoError(t, err)
		assert.EqualValues(t, 500000, facts["tokens"])

		ratios, err := adaptor.EstimateBillingValidated(context, info)
		require.NoError(t, err)
		assert.Equal(t, 500000.0, ratios["tokens"])
	})

	t.Run("declared token facts saturate at the int32 quota bound", func(t *testing.T) {
		adaptor, context, info := newRequest(t, map[string]any{
			"hookUsageEntries": []any{
				map[string]any{"name": "tokens", "value": float64(common.MaxQuota) + 1},
			},
		})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
		facts, err := adaptor.ExtractUsageFactsValidated(context, info)
		require.NoError(t, err)
		assert.EqualValues(t, common.MaxQuota, facts["tokens"])
	})

	t.Run("canonical maxima and enum are accepted", func(t *testing.T) {
		adaptor, context, info := newRequest(t, map[string]any{
			"duration": relaycommon.MaxTaskDurationSeconds,
			"count":    dto.MaxImageN,
			"mode":     "std",
			"hookUsageEntries": []any{
				map[string]any{"name": "duration", "value": float64(relaycommon.MaxTaskDurationSeconds)},
				map[string]any{"name": "count", "value": float64(dto.MaxImageN)},
				map[string]any{"name": "mode", "value": "pro"},
			},
		})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
		ratios, err := adaptor.EstimateBillingValidated(context, info)
		require.NoError(t, err)
		assert.Equal(t, map[string]float64{
			"duration": relaycommon.MaxTaskDurationSeconds,
			"count":    dto.MaxImageN,
		}, ratios)
	})

	t.Run("runtime error does not expose plugin-controlled usage key", func(t *testing.T) {
		adaptor, context, info := newRequest(t, map[string]any{
			"hookUsageEntries": []any{
				map[string]any{
					"name":  "https://private.invalid/?token=secret",
					"value": float64(relaycommon.MaxTaskDurationSeconds + 1),
				},
			},
		})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))
		_, err := adaptor.EstimateBillingValidated(context, info)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "private.invalid")
		assert.NotContains(t, err.Error(), "secret")
	})

	for _, testCase := range []struct {
		name  string
		usage map[string]any
	}{
		{
			name:  "oversized completion duration is discarded",
			usage: map[string]any{"duration": relaycommon.MaxTaskDurationSeconds + 1},
		},
		{
			name:  "oversized completion count is discarded",
			usage: map[string]any{"count": dto.MaxImageN + 1},
		},
		{
			name:  "completion hook numeric string is discarded",
			usage: map[string]any{"duration": "5"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			adaptor, _, _ := newRequest(t, map[string]any{})
			body, marshalErr := common.Marshal(map[string]any{"completionUsage": testCase.usage})
			require.NoError(t, marshalErr)
			result, parseErr := adaptor.ParseTaskResult(&model.Task{}, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, body)
			require.NoError(t, parseErr)
			assert.Nil(t, result.UsageFacts)
			assert.Zero(t, result.TotalTokens)
		})
	}

	t.Run("declared completion token unit is saturated instead of discarded", func(t *testing.T) {
		adaptor, _, _ := newRequest(t, map[string]any{})
		body, err := common.Marshal(map[string]any{"completionUsage": map[string]any{"tokens": 500000}})
		require.NoError(t, err)
		result, err := adaptor.ParseTaskResult(&model.Task{}, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, body)
		require.NoError(t, err)
		assert.EqualValues(t, 500000, result.UsageFacts["tokens"])
	})

	t.Run("declared credit facts keep sub-integer precision", func(t *testing.T) {
		source := `
export const meta = {
  apiVersion: 1, key: "credit-decimals", name: "Credit Decimals", version: "1.0.0",
  author: {name: "Test"}, models: ["model"], fetchMode: "per_task",
  usageSchema: {units: {type: "number", unit: "credit"}},
  usageExamples: [{label: "3.5 credits", facts: {units: 3.5}}],
};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl + "/submit"}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest() { return {url: "https://example.com"}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
export function extractUsage() { return {units: 3.5}; }
export function extractUsageOnComplete() { return {units: 3.5}; }
`
		plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
		require.NoError(t, err)
		adaptor := New(plugin)
		info := &relaycommon.RelayInfo{
			ChannelMeta:   &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"},
			TaskRelayInfo: &relaycommon.TaskRelayInfo{},
		}
		adaptor.Init(info)
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = httptest.NewRequest(http.MethodPost, "/native/submit", nil)
		context.Set("task_request", map[string]any{"model": "model"})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))

		facts, err := adaptor.ExtractUsageFactsValidated(context, info)
		require.NoError(t, err)
		assert.Equal(t, 3.5, facts["units"])

		body, err := common.Marshal(map[string]any{})
		require.NoError(t, err)
		result, err := adaptor.ParseTaskResult(&model.Task{}, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, body)
		require.NoError(t, err)
		assert.Equal(t, 3.5, result.UsageFacts["units"])
	})

	t.Run("completion token facts are saturated instead of duration-capped", func(t *testing.T) {
		adaptor, _, _ := newRequest(t, map[string]any{})
		body, err := common.Marshal(map[string]any{"completionUsage": map[string]any{"upstreamUnits": 5000}})
		require.NoError(t, err)
		result, err := adaptor.ParseTaskResult(&model.Task{}, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, body)
		require.NoError(t, err)
		assert.Equal(t, 5000, result.TotalTokens)
		assert.EqualValues(t, 5000, result.UsageFacts["upstreamUnits"])
	})

	t.Run("invalid post-submit adjustment is discarded before recalculation", func(t *testing.T) {
		adaptor, _, info := newRequest(t, map[string]any{})
		ratios := adaptor.AdjustBillingOnSubmit(info, []byte(`{"usage":{"duration":1000000000000000}}`))
		assert.Nil(t, ratios)
	})
}

func TestTaskAdaptorSeparatesExpressionFactsFromLegacyBillingRatios(t *testing.T) {
	source := `
export const meta = {
  apiVersion: 1, key: "usage-purpose", name: "Usage Purpose", version: "1.0.0",
  author: {name: "Test"}, models: ["usage-model"], fetchMode: "per_task",
  usageSchema: {seconds: {type: "number", unit: "second"}},
};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl + "/submit"}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl + "/query"}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
export function extractUsage(ctx) {
  return ctx.usagePurpose === "billing_ratios" ? {legacy_multiplier: 2} : {seconds: 5};
}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}
	adaptor.Init(info)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/submit", nil)
	context.Set("task_request", map[string]any{"model": "usage-model"})
	require.Nil(t, adaptor.ValidateRequestAndSetAction(context, info))

	facts, err := adaptor.ExtractUsageFactsValidated(context, info)
	require.NoError(t, err)
	assert.EqualValues(t, 5, facts["seconds"])
	assert.Len(t, facts, 1)

	ratios, err := adaptor.EstimateBillingValidated(context, info)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"legacy_multiplier": 2}, ratios)
}

func TestTaskAdaptorAcceptsNormalizedLegacyTokenCounters(t *testing.T) {
	source := `
export const meta = {apiVersion:1,key:"normalized-tokens",name:"Normalized Tokens",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl + "/submit"}; }
export function parseSubmitResponse() { return {taskId: "task"}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl + "/query"}; }
export function parseTaskResult(ctx, body) { return {status: "SUCCESS", completionTokens: body.completion, totalTokens: body.total}; }
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)

	result, err := adaptor.ParseTaskResult(&model.Task{}, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, []byte(`{"completion":13,"total":17}`))
	require.NoError(t, err)
	assert.Equal(t, 13, result.CompletionTokens)
	assert.Equal(t, 17, result.TotalTokens)
	assert.Nil(t, result.UsageFacts)
}

func TestSubmitContextExposesOriginTasks(t *testing.T) {
	plugin, err := pluginruntime.NewRegistry().Register(mockPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example", ApiKey: "secret"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			OriginTasks: []relaycommon.OriginTaskRef{{
				TaskID:         "task_pub_1",
				UpstreamTaskID: "cgt-upstream-1",
				Action:         "text_to_video",
				Status:         "SUCCESS",
				Data:           []byte(`{"id":"cgt-upstream-1"}`),
			}},
		},
	}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)

	ctx := adaptor.submitContext(c, info)

	originTasks, ok := ctx["originTasks"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, originTasks, 1)
	assert.Equal(t, "task_pub_1", originTasks[0]["taskId"])
	assert.Equal(t, "cgt-upstream-1", originTasks[0]["upstreamTaskId"])
	assert.Equal(t, "text_to_video", originTasks[0]["action"])
	assert.Equal(t, "SUCCESS", originTasks[0]["status"])
	assert.Equal(t, map[string]any{"id": "cgt-upstream-1"}, originTasks[0]["data"])
}

func TestSubmitContextOmitsOriginTasksWhenEmpty(t *testing.T) {
	plugin, err := pluginruntime.NewRegistry().Register(mockPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example", ApiKey: "secret"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{},
	}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)

	ctx := adaptor.submitContext(c, info)

	_, ok := ctx["originTasks"]
	assert.False(t, ok)
}

func TestSubmitContextOriginTasksNilDataOnInvalidJSON(t *testing.T) {
	plugin, err := pluginruntime.NewRegistry().Register(mockPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example", ApiKey: "secret"},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{
			OriginTasks: []relaycommon.OriginTaskRef{{
				TaskID:         "task_pub_1",
				UpstreamTaskID: "cgt-upstream-1",
				Action:         "text_to_video",
				Status:         "SUCCESS",
				Data:           []byte("not-json"),
			}},
		},
	}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)

	ctx := adaptor.submitContext(c, info)

	originTasks, ok := ctx["originTasks"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, originTasks, 1)
	assert.Nil(t, originTasks[0]["data"])
}

func TestTaskAdaptorRejectsRequestHostOverride(t *testing.T) {
	source := strings.Replace(mockPlugin, `ctx.baseUrl + "/submit"`, `"https://attacker.example/steal"`, 1)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{Key: "mock-task", Version: "1.0.0"})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "hello"})
	taskErr := adaptor.ValidateRequestAndSetAction(c, info)
	require.NotNil(t, taskErr)
	assert.Contains(t, taskErr.Message, "not allowed")
}

const batchMockPlugin = `
export const meta = { apiVersion: 1, key: "mock-batch", name: "Mock Batch", version: "1.0.0", author: {name: "Test"}, channelTypes: [1002], models: ["batch-v1"], fetchMode: "batch" };
export function buildSubmitRequest(ctx) { return { url: ctx.baseUrl + "/submit", method: "POST", body: {} }; }
export function parseSubmitResponse(ctx, resp) { return { taskId: resp.body.id }; }
export function buildQueryRequest(ctx) { return { url: ctx.baseUrl + "/tasks/" + ctx.taskId }; }
export function parseTaskResult(ctx, body) { return { taskId: body.id, status: "SUCCESS" }; }
export function buildBatchQueryRequest(ctx, tasks) { return { url: ctx.baseUrl + "/batch", method: "POST", headers: { "X-Plugin": "batch" }, body: { ids: (tasks || []).map(function (task) { return task.taskId; }) } }; }
export function parseBatchResult(ctx, body) {
  return body.items.map(function (item) {
    return { taskId: item.id, action: item.action, status: item.status, progress: item.progress, url: (item.urls || [])[0] || "", finishTime: item.finish || 0, data: item };
  });
}
export function extractUsageOnComplete(task, result, body) { return {upstreamUnits: body.usage || 0}; }
`

// Covers the bridge half of the batch contract: FetchBatchTasks must build the
// upstream request from the plugin descriptor, and ParseBatchResult must key
// results by taskId, preserve the explicit result URL, and skip entries without
// a task id.
func TestTaskAdaptorBatchBridge(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/batch", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "batch", r.Header.Get("X-Plugin"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"ids":["task-a","task-b"]}`, string(body))
		_, _ = w.Write([]byte(`{"items":[
			{"id":"task-a","action":"music","status":"SUCCESS","progress":"100%","urls":["https://cdn.example/a1.mp3","https://cdn.example/a2.mp3"],"finish":1700000000,"usage":23},
			{"id":"task-b","status":"IN_PROGRESS","progress":"40%"},
			{"id":"","status":"SUCCESS"}
		]}`))
	}))
	defer server.Close()

	plugin, err := pluginruntime.NewRegistry().Register(batchMockPlugin, pluginruntime.Options{Key: "mock-batch", Version: "1.0.0"})
	require.NoError(t, err)
	adaptor := New(plugin)
	require.Equal(t, "batch", adaptor.FetchMode())

	tasks := []*model.Task{
		{PrivateData: model.TaskPrivateData{UpstreamTaskID: "task-a"}},
		{PrivateData: model.TaskPrivateData{UpstreamTaskID: "task-b"}},
	}
	resp, err := adaptor.FetchBatchTasks(server.URL, "secret", tasks, "")
	require.NoError(t, err)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	results, err := adaptor.ParseBatchResult(tasks, resp, payload)
	require.NoError(t, err)
	require.Len(t, results, 2, "entry without taskId must be skipped")

	done := results["task-a"]
	require.NotNil(t, done)
	assert.Equal(t, "music", done.Action)
	assert.Equal(t, "SUCCESS", done.TaskInfo.Status)
	assert.Equal(t, "100%", done.TaskInfo.Progress)
	assert.Equal(t, "https://cdn.example/a1.mp3", done.TaskInfo.Url)
	assert.Equal(t, int64(1700000000), done.FinishTime)
	assert.EqualValues(t, 23, done.TaskInfo.UsageFacts["upstreamUnits"])
	assert.Equal(t, 23, done.TaskInfo.TotalTokens)
	require.NotNil(t, done.Data)

	pending := results["task-b"]
	require.NotNil(t, pending)
	assert.Equal(t, "IN_PROGRESS", pending.TaskInfo.Status)
	assert.Equal(t, "40%", pending.TaskInfo.Progress)
	assert.Empty(t, pending.TaskInfo.Url)
}

const mappingOrderAdaptorPlugin = `
export const meta = {apiVersion:1,key:"map-order-adaptor",name:"Map Order Adaptor",version:"1.0.0",author:{name:"Test"},models:["declared-model"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx) {
  return {url: ctx.baseUrl+"/submit", method:"POST", body:{upstreamModel: ctx.upstreamModel, model: ctx.model}};
}
export function parseSubmitResponse(){return {taskId:"1"};}
export function buildQueryRequest(){return {url:"https://provider.example"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`

func mappingOrderSubmitBody(t *testing.T, origin, mapping string) []byte {
	t.Helper()
	plugin, err := pluginruntime.NewRegistry().Register(mappingOrderAdaptorPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example"},
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{},
		OriginModelName: origin,
	}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	if mapping != "" {
		c.Set("model_mapping", mapping)
	}
	c.Set("task_request", map[string]any{"prompt": "p"})
	info.UpstreamModelName = info.OriginModelName
	require.NoError(t, helper.ModelMappedHelper(c, info, nil))
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	return raw
}

func TestTaskAdaptorBuildSubmitReceivesMappedUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mapped := mappingOrderSubmitBody(t, "alias-model", `{"alias-model":"mid-model","mid-model":"declared-model"}`)
	var decoded map[string]any
	require.NoError(t, common.Unmarshal(mapped, &decoded))
	assert.Equal(t, "declared-model", decoded["upstreamModel"])
	assert.Equal(t, "alias-model", decoded["model"])

	withoutMapping := mappingOrderSubmitBody(t, "declared-model", "")
	emptyMapping := mappingOrderSubmitBody(t, "declared-model", "{}")
	assert.Equal(t, withoutMapping, emptyMapping)
	require.NoError(t, common.Unmarshal(withoutMapping, &decoded))
	assert.Equal(t, "declared-model", decoded["upstreamModel"])
	assert.Equal(t, "declared-model", decoded["model"])
}

// Polling has no relay info, so query hooks can only branch on the model when
// the host forwards the persisted task identities from the fetch body.
func TestTaskAdaptorFetchTaskExposesModelIdentities(t *testing.T) {
	service.InitHttpClient()
	var requested string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	source := `
export const meta = {apiVersion:1,key:"query-model",name:"Query Model",version:"1.0.0",author:{name:"Test"},models:["alias"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit"}}
export function parseSubmitResponse(){return {taskId:"1"}}
export function buildQueryRequest(ctx){return {url:ctx.baseUrl+"/tasks/"+ctx.model+"/"+ctx.upstreamModel+"/"+ctx.taskId,method:"GET"}}
export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)

	testCases := []struct {
		name string
		task *model.Task
		want string
	}{
		{
			name: "mapped model",
			task: &model.Task{
				Properties:  model.Properties{OriginModelName: "alias", UpstreamModelName: "declared-model"},
				PrivateData: model.TaskPrivateData{UpstreamTaskID: "t1"},
			},
			want: "/tasks/alias/declared-model/t1",
		},
		{
			name: "unmapped model falls back to the origin name",
			task: &model.Task{
				Properties:  model.Properties{OriginModelName: "alias"},
				PrivateData: model.TaskPrivateData{UpstreamTaskID: "t1"},
			},
			want: "/tasks/alias/alias/t1",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resp, fetchErr := adaptor.FetchTask(server.URL, "secret", testCase.task, "")
			require.NoError(t, fetchErr)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, testCase.want, requested)
		})
	}
}

func TestTaskAdaptorQueryContextOmitsRequestBody(t *testing.T) {
	service.InitHttpClient()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, common.DecodeJson(r.Body, &captured))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	source := `
export const meta = {apiVersion:1,key:"query-ctx",name:"Query Ctx",version:"1.0.0",author:{name:"Test"},models:["alias"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit"}}
export function parseSubmitResponse(){return {taskId:"1",state:{req_key:"from-submit"}}}
export function buildQueryRequest(ctx){
  return {url:ctx.baseUrl+"/query",method:"POST",body:{
    keys: Object.keys(ctx).sort(),
    taskId: ctx.taskId,
    publicTaskId: ctx.publicTaskId,
    action: ctx.action,
    model: ctx.model,
    upstreamModel: ctx.upstreamModel,
    data: ctx.data,
    state: ctx.state,
    hasRequestBody: Object.prototype.hasOwnProperty.call(ctx, "requestBody")
  }};
}
export function parseTaskResult(ctx, body, response){
  return {status:"IN_PROGRESS",reason:String(response && response.status),url:ctx.taskId,state:{round:"poll"}};
}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "secret"}})

	task := &model.Task{
		TaskID: "task_public",
		Action: constant.TaskActionImageToVideo,
		Properties: model.Properties{
			OriginModelName:   "alias",
			UpstreamModelName: "declared",
		},
		Data: []byte(`{"snapshot":true}`),
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: "upstream-1",
			PluginState:    []byte(`{"req_key":"kept"}`),
		},
	}
	resp, err := adaptor.FetchTask(server.URL, "secret", task, "")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, "upstream-1", captured["taskId"])
	assert.Equal(t, "task_public", captured["publicTaskId"])
	assert.Equal(t, constant.NormalizeTaskAction(constant.TaskActionImageToVideo), captured["action"])
	assert.Equal(t, "alias", captured["model"])
	assert.Equal(t, "declared", captured["upstreamModel"])
	assert.Equal(t, map[string]any{"snapshot": true}, captured["data"])
	assert.Equal(t, map[string]any{"req_key": "kept"}, captured["state"])
	assert.Equal(t, false, captured["hasRequestBody"])
	keys, ok := captured["keys"].([]any)
	require.True(t, ok)
	assert.NotContains(t, keys, "requestBody")

	result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusTeapot, Header: make(http.Header)}, []byte(`{"ok":true}`))
	require.NoError(t, err)
	assert.Equal(t, "IN_PROGRESS", result.Status)
	assert.Equal(t, "418", result.Reason)
	assert.Equal(t, "upstream-1", result.Url)
	assert.JSONEq(t, `{"round":"poll"}`, string(result.PluginState))
}

func TestTaskAdaptorParseSubmitResponsePersistsState(t *testing.T) {
	service.InitHttpClient()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"upstream-1"}`))
	}))
	defer server.Close()

	source := `
export const meta = {apiVersion:1,key:"submit-state",name:"Submit State",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task"};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit",method:"POST",body:{}}}
export function parseSubmitResponse(){return {taskId:"upstream-1",state:{req_key:"from-submit"}}}
export function buildQueryRequest(ctx){return {url:ctx.baseUrl+"/query"}}
export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "secret"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "hello"})
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	resp, err := adaptor.DoRequest(c, info, body)
	require.NoError(t, err)
	parsed, taskErr := adaptor.ParseResponse(c, resp, info)
	require.Nil(t, taskErr)
	require.NotNil(t, parsed)
	assert.JSONEq(t, `{"req_key":"from-submit"}`, string(parsed.PluginState))
}

func TestTaskAdaptorBatchQueryReceivesTaskObjects(t *testing.T) {
	service.InitHttpClient()
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, common.DecodeJson(r.Body, &captured))
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	source := `
export const meta = {apiVersion:1,key:"batch-ctx",name:"Batch Ctx",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"batch"};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit"}}
export function parseSubmitResponse(){return {taskId:"1"}}
export function buildQueryRequest(ctx){return {url:ctx.baseUrl+"/q"}}
export function parseTaskResult(){return {status:"SUCCESS"}}
export function buildBatchQueryRequest(ctx, tasks){
  return {url:ctx.baseUrl+"/batch",method:"POST",body:{
    ids: (tasks||[]).map(function(task){return task.taskId;}),
    models: (tasks||[]).map(function(task){return task.model;}),
    hasRequestBody: (tasks||[]).some(function(task){return Object.prototype.hasOwnProperty.call(task,"requestBody");})
  }};
}
export function parseBatchResult(){return [];}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	tasks := []*model.Task{
		{Properties: model.Properties{OriginModelName: "model-a"}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "task-a"}},
		{Properties: model.Properties{OriginModelName: "model-b"}, PrivateData: model.TaskPrivateData{UpstreamTaskID: "task-b"}},
	}
	resp, err := adaptor.FetchBatchTasks(server.URL, "secret", tasks, "")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, []any{"task-a", "task-b"}, captured["ids"])
	assert.Equal(t, []any{"model-a", "model-b"}, captured["models"])
	assert.Equal(t, false, captured["hasRequestBody"])
}

func TestTaskAdaptorUsageProfilesFollowExecutionModel(t *testing.T) {
	const source = `
export const meta = {
  apiVersion:1, key:"profile-usage", name:"Profile Usage", version:"1.0.0", author:{name:"Test"},
  models:["image", "video"], fetchMode:"batch",
  usageSchema:{units:{type:"number",unit:"second"}},
  usageProfiles:[
    {models:["image"],schema:{units:{type:"number",unit:"count"},mode:{enum:["image"]}}},
    {models:["video"],schema:{units:{type:"number",unit:"token"},mode:{enum:["video"]}},
     examples:[{label:"video",facts:{units:1,mode:"video"}}]}
  ]
};
export function buildSubmitRequest(ctx) {
  return {url:ctx.baseUrl+"/submit",rewriteModel:ctx.requestBody.rewriteTo || ""};
}
export function parseSubmitResponse(){return {taskId:"task"};}
export function buildQueryRequest(){return {url:"https://provider.example/task"};}
export function buildBatchQueryRequest(){return {url:"https://provider.example/tasks"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
export function parseBatchResult(ctx,body){return body.items;}
export function extractUsage(ctx){return {units:ctx.requestBody.hookUnits,mode:ctx.requestBody.hookMode,legacyRatio:2};}
export function extractUsageOnSubmit(ctx,body){return body.usage;}
export function extractUsageOnComplete(ctx,result,body){return body.usage;}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	newRequest := func(t *testing.T, origin, upstream string, body map[string]any) (*TaskAdaptor, *gin.Context, *relaycommon.RelayInfo) {
		t.Helper()
		info := &relaycommon.RelayInfo{
			OriginModelName: origin,
			ChannelMeta: &relaycommon.ChannelMeta{
				UpstreamModelName: upstream, ChannelBaseUrl: "https://provider.example",
			},
			TaskRelayInfo: &relaycommon.TaskRelayInfo{},
		}
		adaptor := New(plugin)
		adaptor.Init(info)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/native/submit", nil)
		c.Set("task_request", body)
		return adaptor, c, info
	}

	for _, tc := range []struct {
		name, origin, upstream, rewrite, mode string
		units                                 float64
		wantError                             bool
	}{
		{"mapped image", "public-alias", "image", "", "image", 2, false},
		{"mapped video token units", "public-alias", "video", "", "video", 500000, false},
		{"rewritten model", "public-alias", "image", "video", "video", 500000, false},
		{"image count ceiling", "public-alias", "image", "", "image", float64(dto.MaxImageN + 1), true},
		{"profile enum", "public-alias", "image", "", "video", 2, true},
		{"endpoint upstream keeps the declared origin profile", "image", "ep-endpoint", "", "image", 2, false},
		{"endpoint upstream enforces the origin count ceiling", "image", "ep-endpoint", "", "image", float64(dto.MaxImageN + 1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor, c, info := newRequest(t, tc.origin, tc.upstream, map[string]any{
				"hookUnits": tc.units, "hookMode": tc.mode, "rewriteTo": tc.rewrite,
			})
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			facts, err := adaptor.ExtractUsageFactsValidated(c, info)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, map[string]any{"units": tc.units, "mode": tc.mode, "legacyRatio": 2.0}, facts)
			ratios, err := adaptor.EstimateBillingValidated(c, info)
			require.NoError(t, err)
			assert.Equal(t, map[string]float64{"units": tc.units, "legacyRatio": 2}, ratios)
			body, err := common.Marshal(map[string]any{"usage": facts})
			require.NoError(t, err)
			assert.Equal(t, ratios, adaptor.AdjustBillingOnSubmit(info, body))
		})
	}

	t.Run("request revalidated after model rewrite", func(t *testing.T) {
		adaptor, c, info := newRequest(t, "public-alias", "video", map[string]any{
			"rewriteTo": "image", "metadata": map[string]any{"units": dto.MaxImageN + 1},
		})
		taskErr := adaptor.ValidateRequestAndSetAction(c, info)
		require.NotNil(t, taskErr)
		assert.Equal(t, "plugin_usage_invalid", taskErr.Code)
	})

	t.Run("request enum belongs to the rewritten model", func(t *testing.T) {
		adaptor, c, info := newRequest(t, "public-alias", "video", map[string]any{
			"rewriteTo": "image", "mode": "image", "units": 2,
		})
		require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
		assert.Equal(t, "image", info.UpstreamModelName)
	})

	t.Run("completion selects each persisted model including legacy origin fallback", func(t *testing.T) {
		adaptor, _, _ := newRequest(t, "public-alias", "image", map[string]any{})
		for _, tc := range []struct {
			upstream, origin, mode string
			units                  float64
			accepted               bool
		}{
			{"video", "public-alias", "video", 500000, true},
			{"image", "public-alias", "image", 2, true},
			{"", "video", "video", 500000, true},
			{"image", "public-alias", "image", float64(dto.MaxImageN + 1), false},
			{"video", "public-alias", "image", 1, false},
			{"ep-endpoint", "image", "image", 2, true},
			{"ep-endpoint", "image", "image", float64(dto.MaxImageN + 1), false},
		} {
			task := &model.Task{Properties: model.Properties{OriginModelName: tc.origin, UpstreamModelName: tc.upstream}}
			body, err := common.Marshal(map[string]any{"usage": map[string]any{"units": tc.units, "mode": tc.mode}})
			require.NoError(t, err)
			result, err := adaptor.ParseTaskResult(task, &http.Response{StatusCode: http.StatusOK}, body)
			require.NoError(t, err)
			if tc.accepted {
				assert.Equal(t, map[string]any{"units": tc.units, "mode": tc.mode}, result.UsageFacts)
			} else {
				assert.Nil(t, result.UsageFacts)
			}
		}
	})

	t.Run("legacy completion facts remain usable by stored expressions", func(t *testing.T) {
		updated, err := pluginruntime.NewRegistry().Register(strings.Replace(source,
			`export function extractUsageOnComplete(ctx,result,body){return body.usage;}`,
			`export function extractUsageOnComplete(){return {legacyRatio:3};}`, 1), pluginruntime.Options{})
		require.NoError(t, err)
		_, _, info := newRequest(t, "public-alias", "image", map[string]any{})
		adaptor := New(updated)
		adaptor.Init(info)
		result, err := adaptor.ParseTaskResult(&model.Task{Properties: model.Properties{UpstreamModelName: "image"}},
			&http.Response{StatusCode: http.StatusOK}, []byte(`{}`))
		require.NoError(t, err)
		cost, _, err := billingexpr.RunExprWithRequest(`tier("legacy", u("legacyRatio") * 2)`,
			billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: result.UsageFacts})
		require.NoError(t, err)
		assert.Equal(t, 6.0, cost)
	})

	t.Run("mixed batch selects schema by task rather than adaptor", func(t *testing.T) {
		adaptor, _, _ := newRequest(t, "public-alias", "image", map[string]any{})
		tasks := []*model.Task{
			{TaskID: "video-task", Properties: model.Properties{UpstreamModelName: "video"}},
			{TaskID: "image-task", Properties: model.Properties{UpstreamModelName: "image"}},
			{TaskID: "invalid-image", Properties: model.Properties{UpstreamModelName: "image"}},
			{TaskID: "endpoint-image", Properties: model.Properties{OriginModelName: "image", UpstreamModelName: "ep-endpoint"}},
		}
		body := []byte(`{"items":[
{"taskId":"image-task","status":"SUCCESS","data":{"usage":{"units":2,"mode":"image"}}},
{"taskId":"video-task","status":"SUCCESS","data":{"usage":{"units":500000,"mode":"video"}}},
{"taskId":"invalid-image","status":"SUCCESS","data":{"usage":{"units":129,"mode":"image"}}},
{"taskId":"endpoint-image","status":"SUCCESS","data":{"usage":{"units":129,"mode":"image"}}}
]}`)
		results, err := adaptor.ParseBatchResult(tasks, &http.Response{StatusCode: http.StatusOK}, body)
		require.NoError(t, err)
		require.Len(t, results, 4)
		assert.Nil(t, results["endpoint-image"].TaskInfo.UsageFacts, "endpoint upstream must keep the origin profile's count ceiling")
		assert.Equal(t, map[string]any{"units": 500000.0, "mode": "video"}, results["video-task"].TaskInfo.UsageFacts)
		assert.Equal(t, map[string]any{"units": 2.0, "mode": "image"}, results["image-task"].TaskInfo.UsageFacts)
		assert.Nil(t, results["invalid-image"].TaskInfo.UsageFacts)
	})
}

const documentStreamPlugin = `
export const meta = {apiVersion:1,key:"document-stream",name:"Document stream",version:"1.0.0",author:{name:"Test"},models:["document"],fetchMode:"per_task",submitResponseTypes:["json","sse"],usageSchema:{units:{type:"number",unit:"count"}}};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/compile",responseType:ctx.requestBody.responseType || "sse"};}
export function parseSubmitEvent(ctx,event,previous) {
  const chunk = JSON.parse(event.data);
  if (chunk.error) throw new Error("provider stream failure");
  if (chunk.badState) return {state:null};
  if (chunk.largeState) return {state:{document:"x".repeat(1048577)},done:chunk.complete === true};
  if (chunk.escapedState) return {state:{document:"<".repeat(200000)},done:true};
  if (chunk.resetState) return {state:{document:"",units:0},done:true};
  const state = Object.assign({}, previous || {document:"",units:0});
  state.document += chunk.part || "";
  if (chunk.units !== undefined) state.units = chunk.units;
  state.event = event.event; state.id = event.id;
  return {state:state,done:chunk.complete === true};
}
export function parseSubmitResponse(ctx,response){return {taskId:"vendor-document",taskData:response.body,immediate:{status:"SUCCESS"},state:{revision:1}};}
export function parseTaskResult(){return {status:"SUCCESS"};}
export function buildQueryRequest(ctx){return {url:ctx.baseUrl+"/query"};}
export function extractUsageOnComplete(ctx,result,body){
  if(ctx.model!=="alias" || ctx.upstreamModel!=="document" || ctx.taskId!=="vendor-document" || ctx.publicTaskId!=="public-document" || ctx.state.revision!==1 || ctx.data.document!==body.document) throw new Error("invalid completion context");
  return {units:body.units};
}
`

func TestTaskSubmitStreamContract(t *testing.T) {
	plugin, err := pluginruntime.CompilePlugin(documentStreamPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body, contentType string
		valid                   bool
	}{
		{"multiline and CRLF", ": heartbeat\r\nid: doc-1\r\nevent: update\r\ndata: {\"part\":\r\ndata: \"hello\",\"units\":2}\r\n\r\ndata: {\"part\":\"world\",\"units\":3,\"complete\":true}\n\n", "text/event-stream; charset=utf-8", true},
		{"zero actual units", "data: {\"part\":\"free\",\"units\":0,\"complete\":true}\n\n", "text/event-stream", true},
		{"premature EOF", "data: {\"part\":\"partial\"}\n\n", "text/event-stream", false},
		{"unterminated event", "data: {\"complete\":true}", "text/event-stream", false},
		{"provider error", "data: {\"error\":true}\n\n", "text/event-stream", false},
		{"invalid hook result", "data: {\"badState\":true}\n\n", "text/event-stream", false},
		{"state limit", "data: {\"largeState\":true}\n\n", "text/event-stream", false},
		{"intermediate state limit", "data: {\"largeState\":true}\n\ndata: {\"resetState\":true}\n\n", "text/event-stream", false},
		{"encoded state limit", "data: {\"escapedState\":true}\n\n", "text/event-stream", false},
		{"event limit", "data: " + strings.Repeat("x", maxTaskPluginPersistedJSONBytes) + "\n\n", "text/event-stream", false},
		{"wrong response type", "{}", "application/json", false},
		{"undeclared stream", "data: {}\n\n", "text/event-stream", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &relaycommon.RelayInfo{OriginModelName: "alias", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "document", ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "public-document"}}
			adaptor := New(plugin)
			adaptor.Init(info)
			c, recorder := gin.CreateTestContext(httptest.NewRecorder())
			_ = recorder
			c.Request = httptest.NewRequest(http.MethodPost, "/compile", nil)
			requestBody := map[string]any{}
			if tc.name == "undeclared stream" {
				requestBody["responseType"] = "json"
			}
			c.Set("task_request", requestBody)
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			parsed, taskErr := adaptor.ParseResponse(c, response, info)
			assert.False(t, c.Writer.Written())
			if !tc.valid {
				require.NotNil(t, taskErr)
				assert.True(t, taskErr.NoRetry)
				return
			}
			require.Nil(t, taskErr)
			require.NotNil(t, parsed.Immediate)
			if tc.name == "zero actual units" {
				assert.Equal(t, map[string]any{"units": float64(0)}, parsed.Immediate.UsageFacts)
				return
			}
			assert.Equal(t, map[string]any{"units": float64(3)}, parsed.Immediate.UsageFacts)
			assert.JSONEq(t, `{"document":"helloworld","units":3,"event":"message","id":"doc-1"}`, string(parsed.TaskData))
		})
	}
}

func TestTaskSubmitStreamCancellationClosesReader(t *testing.T) {
	plugin, err := pluginruntime.CompilePlugin(documentStreamPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = New(plugin).readSubmitEvents(ctx, &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, err = writer.Write([]byte("data: {}\n\n"))
	require.Error(t, err)
}

func TestTaskSubmitStreamIdleTimeout(t *testing.T) {
	previous := constant.StreamingTimeout
	constant.StreamingTimeout = 1
	t.Cleanup(func() { constant.StreamingTimeout = previous })
	plugin, err := pluginruntime.CompilePlugin(documentStreamPlugin, pluginruntime.Options{})
	require.NoError(t, err)
	reader, writer := io.Pipe()
	defer writer.Close()
	_, err = New(plugin).readSubmitEvents(t.Context(), &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil)
	require.ErrorContains(t, err, "idle timeout")
}

func TestPluginJSONValuesPreserveCodecNormalizationAndIsolation(t *testing.T) {
	for _, value := range []any{
		map[string]any{"units": int64(3), "nested": []any{true, "<image> / 图像", math.Copysign(0, -1), nil}},
		map[string]any{"empty": []any{}, "null": []any(nil), "object": map[string]any(nil)},
		map[string]any{"large": int64(math.MaxInt64), "invalid UTF-8": string([]byte{0xff, 0xfe})},
		map[string]any{string([]byte{0xff}): "invalid key"},
		map[string]any{"bytes": []byte{0, 1, 255}, "number": json.Number("9007199254740993")},
		json.RawMessage(`{"units":2,"enabled":false}`),
		struct {
			Units int `json:"units"`
		}{Units: 0},
	} {
		encoded, err := common.Marshal(value)
		require.NoError(t, err)
		var expected any
		require.NoError(t, common.Unmarshal(encoded, &expected))
		assert.Equal(t, expected, jsonValue(value))
	}
	source := map[string]any{"items": []any{map[string]any{"label": "original"}}}
	copy := jsonValue(source).(map[string]any)
	copy["items"].([]any)[0].(map[string]any)["label"] = "changed"
	assert.Equal(t, "original", source["items"].([]any)[0].(map[string]any)["label"])
}

func TestTaskSubmitHooksReceiveIndependentRequestSnapshots(t *testing.T) {
	source := `
export const meta={apiVersion:1,key:"request-copy",name:"Request copy",version:"1.0.0",author:{name:"Test"},models:["copy"],fetchMode:"per_task",usageSchema:{units:{type:"number",unit:"count"}}};
export function buildSubmitRequest(ctx){ctx.requestBody.units=3;ctx.requestBody.nested.label="built";return {url:ctx.baseUrl+"/submit",body:ctx.requestBody};}
export function extractUsage(ctx){if(ctx.requestBody.nested.label!=="original")throw new Error("request changed");return {units:ctx.requestBody.units};}
export function parseSubmitResponse(){return {taskId:"copy"};}
export function buildQueryRequest(ctx){return {url:ctx.baseUrl+"/query"};}
export function parseTaskResult(){return {status:"SUCCESS"};}
`
	plugin, err := pluginruntime.CompilePlugin(source, pluginruntime.Options{})
	require.NoError(t, err)
	request := map[string]any{"units": int64(2), "nested": map[string]any{"label": "original"}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/submit", nil)
	c.Set("task_request", request)
	info := &relaycommon.RelayInfo{OriginModelName: "copy", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "copy", ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	adaptor := New(plugin)
	adaptor.Init(info)
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	facts, err := adaptor.ExtractUsageFactsValidated(c, info)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"units": float64(2)}, facts)
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	encoded, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"units":3,"nested":{"label":"built"}}`, string(encoded))
	assert.Equal(t, int64(2), request["units"])
	assert.Equal(t, "original", request["nested"].(map[string]any)["label"])
}

func TestTaskSubmitDeltaStreamContract(t *testing.T) {
	source := strings.Replace(documentStreamPlugin, `submitResponseTypes:["json","sse"],`, `submitResponseTypes:["json","sse"],requiredCapabilities:["submit-sse-delta@1"],`, 1) + `
export function parseSubmitEventDelta(ctx,event,previous) {
  if(previous && previous.document !== undefined) throw new Error("full result leaked into control state");
  const chunk=JSON.parse(event.data);
  let changes=chunk.changes;
  if(chunk.largeResult) changes=[{op:"set",path:[],value:{document:"<".repeat(200000)}}];
  if(chunk.resetAfterLarge) changes.push({op:"set",path:[],value:{document:"",units:0}});
  const result={changes:changes,state:{events:(previous ? previous.events : 0)+1},done:chunk.complete===true};
  if(chunk.largeControl) result.state={text:"x".repeat(65537)};
  if(chunk.extra) result.extra=true;
  return result;
}`
	plugin, err := pluginruntime.CompilePlugin(source, pluginruntime.Options{})
	require.NoError(t, err)
	const first = `{"changes":[{"op":"set","path":[],"value":{"document":"hello","units":2}}]}`
	const last = `{"changes":[{"op":"appendText","path":["document"],"value":"world"},{"op":"set","path":["units"],"value":0}],"complete":true}`
	for _, tc := range []struct {
		name   string
		frames []string
		valid  bool
	}{
		{"control state and zero usage", []string{first, last}, true},
		{"oversized intermediate result", []string{`{"largeResult":true}`, last}, false},
		{"oversized operation before reset", []string{`{"largeResult":true,"resetAfterLarge":true,"complete":true}`}, false},
		{"control state limit", []string{first, `{"changes":[],"largeControl":true,"complete":true}`}, false},
		{"missing changes", []string{`{"complete":true}`}, false},
		{"extra result fields", []string{first, `{"changes":[],"extra":true,"complete":true}`}, false},
		{"unfinished delta stream", []string{first}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stream strings.Builder
			for _, frame := range tc.frames {
				stream.WriteString("data: " + frame + "\n\n")
			}
			info := &relaycommon.RelayInfo{OriginModelName: "alias", ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "document", ChannelBaseUrl: "https://provider.example"}, TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "public-document"}}
			adaptor := New(plugin)
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/compile", nil)
			c.Set("task_request", map[string]any{})
			require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
			response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream.String()))}
			parsed, taskErr := adaptor.ParseResponse(c, response, info)
			assert.False(t, c.Writer.Written())
			if !tc.valid {
				require.NotNil(t, taskErr)
				assert.True(t, taskErr.NoRetry)
				return
			}
			require.Nil(t, taskErr)
			require.NotNil(t, parsed.Immediate)
			assert.JSONEq(t, `{"document":"helloworld","units":0}`, string(parsed.TaskData))
			assert.Equal(t, map[string]any{"units": float64(0)}, parsed.Immediate.UsageFacts)
		})
	}
}

func TestAlibabaSubmitDeltaDoesNotMutateControlState(t *testing.T) {
	source, err := plugins.Source("alibaba")
	require.NoError(t, err)
	plugin, err := pluginruntime.CompilePlugin(source, pluginruntime.Options{})
	require.NoError(t, err)
	const before = `{"request_id":"old","usage":{"image_count":1},"output":{"finished":false,"choices":[{"message":{"role":"assistant","content":[{"text":"hello"}]}}]}}`
	const control = `{"choices":[{"count":1,"lastText":true,"finishReason":""}],"hasUsage":true}`
	var previous, initial any
	require.NoError(t, common.UnmarshalJsonStr(control, &previous))
	require.NoError(t, common.UnmarshalJsonStr(before, &initial))
	accumulated := pluginruntime.NewJSONState(maxTaskPluginPersistedJSONBytes)
	require.NoError(t, accumulated.Apply(t.Context(), []any{map[string]any{"op": "set", "path": []any{}, "value": initial}}))
	value, err := plugin.Engine.Call(t.Context(), "parseSubmitEventDelta", map[string]any{}, map[string]any{
		"event": "message", "data": `{"request_id":"new","usage":{"image_count":2},"output":{"choices":[{"finish_reason":"stop","message":{"content":[{"text":" world"},{"image":"https://cdn.example/image.png"}]}}]}}`,
	}, previous)
	require.NoError(t, err)
	unchanged, err := common.Marshal(previous)
	require.NoError(t, err)
	assert.JSONEq(t, control, string(unchanged))
	result := value.(map[string]any)
	assert.Equal(t, true, result["done"])
	require.NoError(t, accumulated.Apply(t.Context(), result["changes"]))
	final, err := accumulated.Value()
	require.NoError(t, err)
	encoded, err := common.Marshal(final)
	require.NoError(t, err)
	assert.JSONEq(t, `{"request_id":"new","usage":{"image_count":2},"output":{"finished":true,"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":[{"text":"hello world"},{"image":"https://cdn.example/image.png"}]}}]}}`, string(encoded))
	encoded, err = common.Marshal(result["state"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"choices":[{"count":2,"lastText":false,"finishReason":"stop"}],"hasUsage":true}`, string(encoded))
}

// newAPIGateway emulates the upstream New API instance a type-60 channel
// points at: it answers only on the plugin's prefixed native routes, requires
// the gateway token as a Bearer header, and renders what its own presenters
// render (the public task id already substituted into the id fields).
func newAPIGateway(t *testing.T, routes map[string]string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer sk-gateway" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestTaskAdaptorChainsDoubaoThroughNewAPIUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	var seen []string
	gateway := newAPIGateway(t, map[string]string{
		"POST /doubao/api/v3/contents/generations/tasks":               `{"id":"task_up_public"}`,
		"GET /doubao/api/v3/contents/generations/tasks/task_up_public": `{"id":"task_up_public","status":"succeeded","content":{"video_url":"https://cdn.example/v.mp4"},"usage":{"completion_tokens":1200,"total_tokens":1200}}`,
	}, &seen)
	defer gateway.Close()

	source, err := plugins.Source("doubao")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().RegisterFactory(source, pluginruntime.Options{Key: "doubao"})
	require.NoError(t, err)
	adaptor := New(plugin)
	const modelName = "doubao-seedance-1-0-pro-250528"
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNewAPI, ChannelBaseUrl: gateway.URL, ApiKey: "sk-gateway", UpstreamModelName: modelName},
		OriginModelName: modelName,
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_local"},
	}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "a cat", Model: modelName})
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

	requestBody, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	submitURL, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, gateway.URL+"/doubao/api/v3/contents/generations/tasks", submitURL)
	resp, err := adaptor.DoRequest(c, info, requestBody)
	require.NoError(t, err)
	parsed, taskErr := adaptor.ParseResponse(c, resp, info)
	require.Nil(t, taskErr)
	assert.Equal(t, "task_up_public", parsed.UpstreamTaskID, "the gateway's presenter emits its public id in the id field doubao reads")

	task := &model.Task{
		Action:      info.Action,
		Properties:  model.Properties{OriginModelName: modelName, UpstreamModelName: modelName},
		PrivateData: model.TaskPrivateData{UpstreamTaskID: parsed.UpstreamTaskID},
	}
	queryResp, err := adaptor.FetchTask(gateway.URL, "sk-gateway", task, "")
	require.NoError(t, err)
	queryBody, err := io.ReadAll(queryResp.Body)
	require.NoError(t, err)
	require.NoError(t, queryResp.Body.Close())
	result, err := adaptor.ParseTaskResult(task, queryResp, queryBody)
	require.NoError(t, err)
	assert.Equal(t, "SUCCESS", result.Status)
	assert.Equal(t, "https://cdn.example/v.mp4", result.Url)
	assert.Equal(t, 1200, result.TotalTokens)
	assert.Equal(t, []string{
		"POST /doubao/api/v3/contents/generations/tasks",
		"GET /doubao/api/v3/contents/generations/tasks/task_up_public",
	}, seen)
}

func TestTaskAdaptorChainsSunoBatchFetchThroughNewAPIUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	var seen []string
	gateway := newAPIGateway(t, map[string]string{
		"POST /suno/submit/MUSIC": `{"code":"success","message":"","data":"task_up_public"}`,
		"POST /suno/fetch":        `{"code":"success","message":"","data":[{"task_id":"task_up_public","status":"SUCCESS","progress":"100%","data":[{"id":"clip-1","status":"complete","audio_url":"https://cdn.example/a.mp3"}]}]}`,
	}, &seen)
	defer gateway.Close()

	source, err := plugins.Source("sunoapi")
	require.NoError(t, err)
	plugin, err := pluginruntime.NewRegistry().RegisterFactory(source, pluginruntime.Options{Key: "sunoapi"})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeNewAPI, ChannelBaseUrl: gateway.URL, ApiKey: "sk-gateway", UpstreamModelName: "suno_music"},
		OriginModelName: "suno_music",
		TaskRelayInfo:   &relaycommon.TaskRelayInfo{PublicTaskID: "task_local", Action: "MUSIC"},
	}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/suno/submit/music", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "a song about cats"})
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))

	requestBody, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	submitURL, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	assert.Equal(t, gateway.URL+"/suno/submit/MUSIC", submitURL)
	resp, err := adaptor.DoRequest(c, info, requestBody)
	require.NoError(t, err)
	parsed, taskErr := adaptor.ParseResponse(c, resp, info)
	require.Nil(t, taskErr)
	assert.Equal(t, "task_up_public", parsed.UpstreamTaskID)

	task := &model.Task{
		Action:      "MUSIC",
		Properties:  model.Properties{OriginModelName: "suno_music", UpstreamModelName: "suno_music"},
		PrivateData: model.TaskPrivateData{UpstreamTaskID: parsed.UpstreamTaskID},
	}
	batchResp, err := adaptor.FetchBatchTasks(gateway.URL, "sk-gateway", []*model.Task{task}, "")
	require.NoError(t, err)
	batchBody, err := io.ReadAll(batchResp.Body)
	require.NoError(t, err)
	require.NoError(t, batchResp.Body.Close())
	results, err := adaptor.ParseBatchResult([]*model.Task{task}, batchResp, batchBody)
	require.NoError(t, err)
	require.Contains(t, results, "task_up_public")
	assert.Equal(t, "SUCCESS", results["task_up_public"].TaskInfo.Status)
	assert.Equal(t, []string{"POST /suno/submit/MUSIC", "POST /suno/fetch"}, seen)
}
