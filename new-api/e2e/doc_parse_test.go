package e2e

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const docParsePluginSource = `export const meta = {apiVersion:1,key:"doc-parse",name:"Document Parser",version:"1.0.0",author:{name:"Test"},models:["doc-parse-v1"],fetchMode:"batch"};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit",method:"POST",headers:{"Content-Type":"application/json"},body:ctx.requestBody,action:"parse_document"};}
export function parseSubmitResponse(ctx,resp){if(!resp.body.id)throw new Error("missing id");return {taskId:resp.body.id,taskData:resp.body};}
export function buildBatchQueryRequest(ctx,taskIds){return {url:ctx.baseUrl+"/batch",method:"POST",headers:{"Content-Type":"application/json"},body:{ids:taskIds}};}
export function parseBatchResult(ctx,body){return body.tasks.map((task)=>({taskId:task.id,status:task.status,progress:"100%",data:task}));}
export function parseTaskResult(ctx,body){return {taskId:body.id,status:body.status};}
export function listArtifacts(task){return task.status==="SUCCESS"?(task.data.artifacts||[]).map((item)=>({key:item.key,type:"file",mimeType:item.mimeType})):[];}
export function buildContentRequest(ctx){const item=(ctx.data.artifacts||[]).find((artifact)=>artifact.key===ctx.artifactKey);if(!item)throw new Error("artifact_not_found");return {url:item.url,method:ctx.clientRequest.method,credentialless:true};}
`

func TestDocumentPluginRunsGenericBatchArtifactChain(t *testing.T) {
	service.InitHttpClient()
	originalDB := model.DB
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&model.TaskPlugin{}, &model.Channel{}, &model.Task{}))
	model.DB = database
	t.Cleanup(func() { model.DB = originalDB; jsplugin.DefaultRegistry.Unregister("doc-parse") })

	source := docParsePluginSource
	uploadBody, err := common.Marshal(map[string]any{"source": source, "remark": "phase 4 acceptance"})
	require.NoError(t, err)
	uploadRecorder := httptest.NewRecorder()
	uploadContext, _ := gin.CreateTestContext(uploadRecorder)
	uploadContext.Request = httptest.NewRequest(http.MethodPost, "/api/plugin/task", bytes.NewReader(uploadBody))
	uploadContext.Request.Header.Set("Content-Type", "application/json")
	controller.UploadTaskPlugin(uploadContext)
	require.Equal(t, http.StatusOK, uploadRecorder.Code, uploadRecorder.Body.String())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/submit":
			_, _ = io.WriteString(w, `{"id":"doc-upstream-1"}`)
		case "/batch":
			_, _ = io.WriteString(w, `{"tasks":[{"id":"doc-upstream-1","status":"SUCCESS","artifacts":[{"key":"text","url":"`+"http://"+r.Host+`/artifact/text","mimeType":"text/plain"},{"key":"json","url":"`+"http://"+r.Host+`/artifact/json","mimeType":"application/json"}]}]}`)
		case "/artifact/text":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "parsed text")
		case "/artifact/json":
			_, _ = io.WriteString(w, `{"pages":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	setting := dto.ChannelSettings{TaskPluginKey: "doc-parse"}
	channel := model.Channel{Type: constant.ChannelTypeTaskPlugin, Name: "documents", Key: "unused", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Models: "doc-parse-v1", Group: "default"}
	channel.SetSetting(setting)
	require.NoError(t, database.Create(&channel).Error)

	adaptor := relay.GetTaskAdaptor("doc-parse")
	require.NotNil(t, adaptor)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channel.Type, ChannelBaseUrl: upstream.URL, ApiKey: channel.Key, ChannelSetting: setting, UpstreamModelName: "doc-parse-v1"}, OriginModelName: "doc-parse-v1", TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_doc_parse"}}
	adaptor.Init(info)
	submitRecorder := httptest.NewRecorder()
	submitContext, _ := gin.CreateTestContext(submitRecorder)
	submitContext.Request = httptest.NewRequest(http.MethodPost, "/v1/tasks/doc-parse", bytes.NewBufferString(`{"model":"doc-parse-v1","document":"opaque-ref"}`))
	submitContext.Request.Header.Set("Content-Type", "application/json")
	submitContext.Params = gin.Params{{Key: "key", Value: "doc-parse"}}
	middleware.PrepareTaskPluginSubmit()(submitContext)
	require.Empty(t, submitRecorder.Body.String())
	require.Equal(t, "doc-parse-v1", submitContext.GetString("resolved_task_model"))
	require.Nil(t, adaptor.ValidateRequestAndSetAction(submitContext, info))
	require.Equal(t, "parse_document", info.Action)
	requestBody, err := adaptor.BuildRequestBody(submitContext, info)
	require.NoError(t, err)
	response, err := adaptor.DoRequest(submitContext, info, requestBody)
	require.NoError(t, err)
	parsed, taskErr := adaptor.ParseResponse(submitContext, response, info)
	require.Nil(t, taskErr)
	require.NotNil(t, parsed)
	require.Equal(t, "doc-upstream-1", parsed.UpstreamTaskID)
	task := model.Task{
		TaskID: info.PublicTaskID, Platform: "doc-parse", UserId: 7, ChannelId: channel.Id,
		Status: model.TaskStatusInProgress, Data: parsed.TaskData,
		PrivateData: model.TaskPrivateData{
			UpstreamTaskID: parsed.UpstreamTaskID,
			Execution: &model.TaskExecutionSnapshot{TaskPlugin: &model.TaskPluginSnapshot{
				Key: "doc-parse", Name: "Document Parser", Version: "1.0.0",
				Author: &model.TaskPluginAuthorSnapshot{Name: "Test"}, APIVersion: 1,
			}},
		},
	}
	require.NoError(t, database.Create(&task).Error)

	originalFactory := service.GetTaskAdaptorFunc
	service.GetTaskAdaptorFunc = func(platform constant.TaskPlatform) service.TaskPollingAdaptor { return relay.GetTaskAdaptor(platform) }
	t.Cleanup(func() { service.GetTaskAdaptorFunc = originalFactory })
	service.DispatchPlatformUpdate(context.Background(), "doc-parse", map[int][]string{channel.Id: {parsed.UpstreamTaskID}}, map[string]*model.Task{parsed.UpstreamTaskID: &task})
	require.NoError(t, database.First(&task, task.ID).Error)
	assert.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)

	queryRecorder := httptest.NewRecorder()
	queryContext, _ := gin.CreateTestContext(queryRecorder)
	queryContext.Set("id", 7)
	queryContext.Params = gin.Params{{Key: "key", Value: task.TaskID}}
	queryContext.Request = httptest.NewRequest(http.MethodGet, "/v1/tasks/"+task.TaskID+"/artifacts", nil)
	controller.GetTaskArtifacts(queryContext)
	require.Equal(t, http.StatusOK, queryRecorder.Code)
	var query struct {
		Artifacts []map[string]any `json:"artifacts"`
	}
	require.NoError(t, common.Unmarshal(queryRecorder.Body.Bytes(), &query))
	require.Len(t, query.Artifacts, 2)

	originalFetch := *system_setting.GetFetchSetting()
	system_setting.GetFetchSetting().EnableSSRFProtection = true
	system_setting.GetFetchSetting().AllowPrivateIp = true
	system_setting.GetFetchSetting().AllowedPorts = []string{"1-65535"}
	t.Cleanup(func() { *system_setting.GetFetchSetting() = originalFetch })
	contentRecorder := httptest.NewRecorder()
	contentContext, _ := gin.CreateTestContext(contentRecorder)
	contentContext.Set("id", 7)
	contentContext.Params = gin.Params{{Key: "key", Value: task.TaskID}, {Key: "artifact_key", Value: "text"}}
	contentContext.Request = httptest.NewRequest(http.MethodGet, "/v1/tasks/"+task.TaskID+"/artifacts/text/content", nil)
	controller.TaskArtifactContent(contentContext)
	assert.Equal(t, http.StatusOK, contentRecorder.Code)
	assert.Equal(t, "parsed text", contentRecorder.Body.String())
}
