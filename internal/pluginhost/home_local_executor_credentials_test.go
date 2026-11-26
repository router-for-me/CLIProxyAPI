package pluginhost

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHomeLocalExecutorSDKCredentials(t *testing.T) {
	const pluginID = "sdk-credentials-plugin"
	const model = "sdk-credentials-model"
	const alias = "acp/" + model
	const pluginYAML = "enabled: true\npriority: 100\n"
	configDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(pluginYAML)))
	type authorizationRequest struct {
		Type    string            `json:"type"`
		Model   string            `json:"model"`
		Headers map[string]string `json:"headers"`
		Query   map[string]string `json:"query"`
	}
	authorizations := make(chan authorizationRequest, 64)
	client := newHomeExecutorTestClient(t, func(command, argument string) []byte {
		var req authorizationRequest
		if errDecode := json.Unmarshal([]byte(argument), &req); errDecode != nil {
			t.Errorf("decode Home authorization request: %v", errDecode)
			return []byte(`{"error":{"type":"error","message":"invalid authorization request"}}`)
		}
		if command != "GET" || req.Type != "local-plugin-dispatch" || req.Model != alias {
			t.Errorf("unexpected Home request: command=%s type=%s model=%s", command, req.Type, req.Model)
			return []byte(`{"error":{"type":"error","message":"unexpected authorization request"}}`)
		}
		authorizations <- req
		if req.Headers["authorization"] == "Bearer denied-key" || req.Query["key"] == "denied-key" {
			return []byte(`{"error":{"type":"invalid_credential","message":"credential rejected"}}`)
		}
		if req.Headers["authorization"] == "" && req.Query["key"] == "" {
			return []byte(`{"error":{"type":"no_credentials","message":"credential required"}}`)
		}
		return []byte(fmt.Sprintf(`{"kind":"local-plugin","plugin":%q,"version":"1.0.0","config_digest":%q,"model":%q,"principal":"sdk-caller","access_provider":"config-api-key"}`, pluginID, configDigest, model))
	})
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(client, executionregistry.New(), 1)

	executions := make(chan pluginapi.ExecutorRequest, 64)
	executor := &fakeExecutor{
		identifier: pluginID,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			executions <- req
			return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
		},
		executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			executions <- req
			chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
			chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")}
			close(chunks)
			return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
		},
	}
	record := normalizeTestCapabilityRecord(capabilityRecord{
		id: pluginID, version: "1.0.0",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: executor, ExecutorModelScope: pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats: []string{"openai"}, ExecutorOutputFormats: []string{"openai"},
		}},
	})
	record.loaded = &loadedPlugin{id: record.id, path: record.path, version: record.version, configYAML: []byte(pluginYAML)}
	host := newHostWithRecords(record)
	host.loaded[record.id] = record.loaded
	handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler.SetModelRouterHost(host)

	for _, operation := range []string{"model", "protocol", "model-stream", "protocol-stream"} {
		for _, tc := range []struct {
			name        string
			withGin     bool
			headers     http.Header
			query       url.Values
			wantHeaders map[string]string
			wantQuery   map[string]string
			wantStatus  int
		}{
			{
				name:        "explicit header without Gin",
				headers:     http.Header{"Authorization": {"Bearer sdk-key"}},
				wantHeaders: map[string]string{"authorization": "Bearer sdk-key"},
			},
			{
				name:      "explicit query without Gin",
				query:     url.Values{"key": {"sdk-key"}},
				wantQuery: map[string]string{"key": "sdk-key"},
			},
			{
				name: "explicit values override Gin", withGin: true,
				headers: http.Header{"Authorization": {"Bearer sdk-key"}}, query: url.Values{"key": {"sdk-key"}},
				wantHeaders: map[string]string{"authorization": "Bearer sdk-key"}, wantQuery: map[string]string{"key": "sdk-key"},
			},
			{
				name: "absent values fall back to Gin", withGin: true,
				wantHeaders: map[string]string{"authorization": "Bearer context-key"}, wantQuery: map[string]string{"key": "context-key"},
			},
			{
				name: "empty values match execution fallback", withGin: true,
				headers: http.Header{}, query: url.Values{},
				wantHeaders: map[string]string{"authorization": "Bearer context-key"}, wantQuery: map[string]string{"key": "context-key"},
			},
			{
				name: "header and query fall back independently", withGin: true,
				headers:     http.Header{"Authorization": {"Bearer sdk-key"}},
				wantHeaders: map[string]string{"authorization": "Bearer sdk-key"}, wantQuery: map[string]string{"key": "context-key"},
			},
			{
				name: "query and header fall back independently", withGin: true,
				query:       url.Values{"key": {"sdk-key"}},
				wantHeaders: map[string]string{"authorization": "Bearer context-key"}, wantQuery: map[string]string{"key": "sdk-key"},
			},
			{
				name: "rejected explicit credentials do not retry with Gin", withGin: true,
				headers: http.Header{"Authorization": {"Bearer denied-key"}}, query: url.Values{"key": {"denied-key"}},
				wantHeaders: map[string]string{"authorization": "Bearer denied-key"}, wantQuery: map[string]string{"key": "denied-key"},
				wantStatus: http.StatusUnauthorized,
			},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				if tc.withGin {
					ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions?key=context-key", nil)
					ginCtx.Request.Header.Set("Authorization", "Bearer context-key")
					ctx = context.WithValue(ctx, "gin", ginCtx)
				}
				body := []byte(`{"model":"` + alias + `","messages":[{"role":"user","content":"hello"}]}`)
				modelRequest := handlers.ModelExecutionRequest{
					EntryProtocol: "openai", ExitProtocol: "openai", Model: alias,
					Body: body, Headers: tc.headers, Query: tc.query,
				}
				protocolRequest := handlers.ProtocolExecutionRequest{
					EntryProtocol: "openai", ExitProtocol: "openai", Model: alias,
					Body: body, Headers: tc.headers, Query: tc.query,
				}
				status := http.StatusOK
				switch operation {
				case "model":
					if _, errMsg := handler.ExecuteModel(ctx, modelRequest); errMsg != nil {
						status = errMsg.StatusCode
					}
				case "protocol":
					if _, errMsg := handler.ExecuteProtocolWithAuthManager(ctx, protocolRequest); errMsg != nil {
						status = errMsg.StatusCode
					}
				case "model-stream", "protocol-stream":
					var stream handlers.ModelExecutionStream
					if operation == "model-stream" {
						modelRequest.Stream = true
						result, errMsg := handler.ExecuteModelStream(ctx, modelRequest)
						if errMsg != nil {
							status = errMsg.StatusCode
						} else {
							stream = result
						}
					} else {
						protocolRequest.Stream = true
						result, errMsg := handler.ExecuteProtocolStreamWithAuthManager(ctx, protocolRequest)
						if errMsg != nil {
							status = errMsg.StatusCode
						} else {
							stream = result
						}
					}
					if status == http.StatusOK {
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream error: %v", chunk.Err)
							}
						}
					}
				}
				wantStatus := tc.wantStatus
				if wantStatus == 0 {
					wantStatus = http.StatusOK
				}
				if status != wantStatus {
					t.Fatalf("status = %d, want %d", status, wantStatus)
				}
				select {
				case req := <-authorizations:
					if !maps.Equal(req.Headers, tc.wantHeaders) || !maps.Equal(req.Query, tc.wantQuery) {
						t.Fatalf("Home inputs = headers %v query %v, want headers %v query %v", req.Headers, req.Query, tc.wantHeaders, tc.wantQuery)
					}
				default:
					t.Fatal("Home did not receive an authorization request")
				}
				select {
				case req := <-executions:
					if wantStatus != http.StatusOK {
						t.Fatal("plugin executed after authorization rejection")
					}
					if req.Headers.Get("Authorization") != tc.wantHeaders["authorization"] || req.Query.Get("key") != tc.wantQuery["key"] {
						t.Fatal("authorization and execution selected different credentials")
					}
				default:
					if wantStatus == http.StatusOK {
						t.Fatal("authorized request did not reach the plugin")
					}
				}
			})
		}
	}
}
