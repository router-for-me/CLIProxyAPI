package jsplugin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relay/channel"
	vertexcore "github.com/QuantumNous/new-api/relay/channel/vertex"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuth2JWTAuthCachesAndRefreshes(t *testing.T) {
	pluginAuthCache = sync.Map{}
	original := acquireAccessToken
	t.Cleanup(func() { acquireAccessToken = original; pluginAuthCache = sync.Map{} })
	calls := 0
	acquireAccessToken = func(_ vertexcore.Credentials, _ string) (string, error) {
		calls++
		return fmt.Sprintf("token-%d", calls), nil
	}
	credentials, err := common.Marshal(vertexcore.Credentials{ProjectID: "project", ClientEmail: "a@example.com", PrivateKey: "secret"})
	require.NoError(t, err)
	meta := pluginruntime.AuthMeta{Type: "oauth2_jwt"}
	first, err := resolveAuth(meta, string(credentials), "")
	require.NoError(t, err)
	second, err := resolveAuth(meta, string(credentials), "")
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-1", first["authHeader"])
	assert.Equal(t, first, second)
	assert.Equal(t, 1, calls)
	pluginAuthCache.Store(string(credentials)+"\x00", cachedAuth{expiresAt: time.Now().Add(-time.Second)})
	refreshed, err := resolveAuth(meta, string(credentials), "")
	require.NoError(t, err)
	assert.Equal(t, "Bearer token-2", refreshed["authHeader"])
	assert.Equal(t, 2, calls)
}

func TestOAuth2JWTContextDoesNotExposeServiceAccountKey(t *testing.T) {
	pluginAuthCache = sync.Map{}
	original := acquireAccessToken
	t.Cleanup(func() { acquireAccessToken = original; pluginAuthCache = sync.Map{} })
	acquireAccessToken = func(_ vertexcore.Credentials, _ string) (string, error) {
		return "access-token", nil
	}
	credentials, err := common.Marshal(vertexcore.Credentials{ProjectID: "project", ClientEmail: "a@example.com", PrivateKey: "secret"})
	require.NoError(t, err)
	source := `
export const meta = {apiVersion:1,key:"oauth",name:"OAuth",version:"1.0.0",author:{name:"Test"},models:["m"],fetchMode:"per_task",auth:{type:"oauth2_jwt"}};
export function buildSubmitRequest(ctx) {
  if (ctx.apiKey !== undefined) throw new Error("raw key exposed");
  return {url:ctx.baseUrl+"/submit",headers:{Authorization:ctx.authHeader}};
}
export function parseSubmitResponse(){return {taskId:"1"}}
export function buildQueryRequest(){return {url:"https://example.com"}}
export function parseTaskResult(){return {status:"SUCCESS"}}
`
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "https://provider.example", ApiKey: string(credentials)}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
	adaptor.Init(info)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Set("task_request", relaycommon.TaskSubmitReq{Prompt: "p"})
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	req := httptest.NewRequest(http.MethodPost, "https://provider.example/submit", nil)
	require.NoError(t, adaptor.BuildRequestHeader(c, req, info))
	assert.Equal(t, "Bearer access-token", req.Header.Get("Authorization"))
}

func TestUpstreamContextsUseGatewayBearerOnNewAPIChannels(t *testing.T) {
	pluginAuthCache = sync.Map{}
	original := acquireAccessToken
	exchanges := 0
	acquireAccessToken = func(_ vertexcore.Credentials, _ string) (string, error) {
		exchanges++
		return "access-token", nil
	}
	t.Cleanup(func() { acquireAccessToken = original; pluginAuthCache = sync.Map{} })
	credentials, err := common.Marshal(vertexcore.Credentials{ProjectID: "project", ClientEmail: "a@example.com", PrivateKey: "secret"})
	require.NoError(t, err)
	const base = "https://upstream.example"

	testCases := []struct {
		name           string
		auth           string
		channelType    int
		key            string
		wantKind       string
		wantAuthHeader string
		wantAPIKey     any // nil when the raw key must stay hidden from the plugin
	}{
		{"api key plugin on a New API channel", "", constant.ChannelTypeNewAPI, "sk-gateway", pluginruntime.UpstreamKindNewAPI, "Bearer sk-gateway", "sk-gateway"},
		{"api key plugin on a Task Plugin channel", "", constant.ChannelTypeTaskPlugin, "vendor-key", pluginruntime.UpstreamKindVendor, "vendor-key", "vendor-key"},
		{"api key plugin on a legacy channel", "", 1002, "vendor-key", pluginruntime.UpstreamKindVendor, "vendor-key", "vendor-key"},
		{"oauth2_jwt plugin on a New API channel", `,auth:{type:"oauth2_jwt"}`, constant.ChannelTypeNewAPI, "sk-gateway", pluginruntime.UpstreamKindNewAPI, "Bearer sk-gateway", "sk-gateway"},
		{"oauth2_jwt plugin on a Task Plugin channel", `,auth:{type:"oauth2_jwt"}`, constant.ChannelTypeTaskPlugin, string(credentials), pluginruntime.UpstreamKindVendor, "Bearer access-token", nil},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			source := `
export const meta = {apiVersion:1,key:"upstream-probe",name:"Upstream Probe",version:"1.0.0",author:{name:"Test"},channelTypes:[1002],models:["probe"],fetchMode:"per_task",upstreams:["vendor","new_api"]` + tc.auth + `};
export function buildSubmitRequest(ctx){return {url:ctx.baseUrl+"/submit"}}
export function parseSubmitResponse(){return {taskId:"1"}}
export function buildQueryRequest(ctx){return {url:ctx.baseUrl+"/tasks/"+ctx.taskId}}
export function parseTaskResult(){return {status:"SUCCESS"}}
export function listArtifacts(){return [{key:"video",type:"video"}]}
export function buildContentRequest(ctx){return {url:ctx.baseUrl+"/content/"+ctx.upstream.kind,headers:{Authorization:ctx.authHeader,"X-Api-Key":String(ctx.apiKey)}}}
`
			plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			adaptor := New(plugin)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: tc.channelType, ChannelBaseUrl: base, ApiKey: tc.key}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
			task := &model.Task{PrivateData: model.TaskPrivateData{UpstreamTaskID: "t1"}}

			submit := adaptor.submitContext(c, info)
			query, err := adaptor.queryContext(task, tc.key, base, "")
			require.NoError(t, err)
			batch, err := adaptor.batchQueryContext(tc.key, base, "", nil)
			require.NoError(t, err)
			assert.NotContains(t, submit, "authError")
			for name, ctx := range map[string]map[string]any{"submit": submit, "query": query, "batch": batch} {
				assert.Equal(t, map[string]any{"kind": tc.wantKind}, ctx["upstream"], name)
				assert.Equal(t, tc.wantAuthHeader, ctx["authHeader"], name)
				assert.Equal(t, tc.wantAPIKey, ctx["apiKey"], name)
				assert.Equal(t, tc.wantAuthHeader, ctx["auth"].(map[string]any)["authHeader"], name)
			}

			content, err := adaptor.BuildContentRequest(task, "video", channel.TaskArtifactClientRequest{Method: http.MethodGet})
			require.NoError(t, err)
			assert.Equal(t, base+"/content/"+tc.wantKind, content.URL)
			assert.Equal(t, tc.wantAuthHeader, content.Headers["Authorization"])
			if tc.wantAPIKey == nil {
				assert.Equal(t, "undefined", content.Headers["X-Api-Key"])
			} else {
				assert.Equal(t, tc.wantAPIKey, content.Headers["X-Api-Key"])
			}
		})
	}
	assert.Equal(t, 1, exchanges, "the vendor token exchange runs only for the oauth2_jwt vendor channel, never for a New API channel")
}
