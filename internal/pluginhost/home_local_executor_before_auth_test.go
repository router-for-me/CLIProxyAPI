package pluginhost

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type homeBeforeAuthInterceptorFunc func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error)

func (f homeBeforeAuthInterceptorFunc) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return f(ctx, req)
}

func (homeBeforeAuthInterceptorFunc) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return pluginapi.RequestInterceptResponse{}, nil
}

func TestHomeLocalExecutorBeforeAuth(t *testing.T) {
	const pluginYAML = "enabled: true\n"
	const provider = "before-auth-test-provider"
	const replacement = "Bearer intercepted-key"
	configDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(pluginYAML)))
	for _, route := range []string{"local", "credential", "legacy"} {
		for _, operation := range []string{"execute", "stream", "count"} {
			for _, behavior := range []string{"replace", "reject", "terminate", "clear"} {
				t.Run(route+"/"+operation+"/"+behavior, func(t *testing.T) {
					var interceptions, authorizations, credentialPops, executions atomic.Int32
					credentialReply := homeExecutorDispatchJSON(t, coreauth.Auth{
						ID: homePluginAuth, Provider: provider, Status: coreauth.StatusActive,
						Attributes: map[string]string{"source": "home"},
					}, homePluginID)
					client := newHomeExecutorTestClient(t, func(command, argument string) []byte {
						doc := ojson.NewDocString(argument)
						if command == "RPOP" {
							credentialPops.Add(1)
						} else if command == "GET" && doc.Get("type").String() == "local-plugin-dispatch" {
							authorizations.Add(1)
						} else {
							t.Errorf("unexpected Home command %s", command)
							return []byte(`{"error":{"type":"error","message":"unexpected request"}}`)
						}
						if doc.Get("model").String() != homePluginAlias || interceptions.Load() != 1 {
							t.Error("Home authorization did not follow exactly one before-auth interception")
						}
						wantHeader := replacement
						if behavior == "reject" {
							wantHeader = "Bearer rejected-intercepted-key"
						} else if behavior == "clear" {
							wantHeader = ""
						}
						if doc.Get("headers.authorization").String() != wantHeader {
							t.Error("Home did not receive the final intercepted authorization header")
						}
						if doc.Get("headers.authorization").String() != replacement {
							return []byte(`{"error":{"type":"invalid_credential","message":"credential rejected"}}`)
						}
						if command == "RPOP" {
							return credentialReply
						}
						switch route {
						case "credential":
							return []byte(`{"kind":"credential"}`)
						case "legacy":
							return []byte(`{"error":{"type":"error","message":"unsupported type"}}`)
						default:
							return []byte(fmt.Sprintf(`{"kind":"local-plugin","plugin":%q,"version":"1.0.0","config_digest":%q,"model":%q,"principal":"before-auth-caller","access_provider":"config-api-key"}`, homePluginID, configDigest, homePluginModel))
						}
					})
					manager := coreauth.NewManager(nil, nil, nil)
					manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
					manager.PublishHomeDispatch(client, executionregistry.New(), 1)
					checkRequest := func(req pluginapi.ExecutorRequest) {
						executions.Add(1)
						if req.Headers.Get("Authorization") != replacement || ojson.GetBytesNoCopy(req.Payload, "user").String() != "intercepted" ||
							ojson.GetBytesNoCopy(req.OriginalRequest, "user").String() != "intercepted" {
							t.Error("execution lost intercepted headers or body")
						}
						if route == "local" && (req.AuthID != "" || len(req.StorageJSON) != 0) {
							t.Error("local executor received an upstream credential")
						}
						if route != "local" && req.AuthID != homePluginAuth {
							t.Error("credential fallback lost the Home-selected upstream credential")
						}
					}
					respond := func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						checkRequest(req)
						return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}],"total_tokens":3}`)}, nil
					}
					executor := &fakeExecutor{identifier: homePluginID, execute: respond, countTokens: respond,
						executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
							checkRequest(req)
							chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
							chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")}
							close(chunks)
							return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
						}}
					record := normalizeTestCapabilityRecord(capabilityRecord{id: homePluginID, version: "1.0.0",
						plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
							Executor: executor, ExecutorModelScope: pluginapi.ExecutorModelScopeBoth,
							ExecutorAuthProviders: []string{provider},
							ExecutorInputFormats:  []string{"openai"}, ExecutorOutputFormats: []string{"openai"},
							RequestInterceptor: homeBeforeAuthInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
								interceptions.Add(1)
								wantKey := "Bearer explicit-key"
								if operation == "count" {
									wantKey = "Bearer context-key"
								}
								if req.Headers.Get("Authorization") != wantKey || req.Model != homePluginAlias {
									t.Error("before-auth interceptor did not receive the original caller request")
								}
								if behavior == "terminate" {
									return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: http.StatusForbidden}, nil
								}
								if behavior == "clear" {
									return pluginapi.RequestInterceptResponse{ClearHeaders: []string{"Authorization"}}, nil
								}
								key := replacement
								if behavior == "reject" {
									key = "Bearer rejected-intercepted-key"
								}
								doc := ojson.NewDoc(req.Body)
								if errSet := doc.Set("user", "intercepted"); errSet != nil {
									return pluginapi.RequestInterceptResponse{}, errSet
								}
								return pluginapi.RequestInterceptResponse{Headers: http.Header{"Authorization": {key}}, Body: doc.Bytes()}, nil
							}),
						}},
					})
					record.loaded = &loadedPlugin{id: record.id, path: record.path, version: record.version, configYAML: []byte(pluginYAML)}
					host := newHostWithRecords(record)
					host.loaded[record.id] = record.loaded
					manager.SetHomePluginExecutorResolver(host)
					handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
					handler.SetModelRouterHost(host)
					handler.SetPluginHost(host)

					ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
					ginCtx.Request.Header.Set("Authorization", "Bearer context-key")
					ctx := context.WithValue(t.Context(), "gin", ginCtx)
					body := []byte(`{"model":"` + homePluginAlias + `","messages":[{"role":"user","content":"hi"}]}`)
					req := handlers.ModelExecutionRequest{
						EntryProtocol: "openai", Model: homePluginAlias, Body: body,
						Headers: http.Header{"Authorization": {"Bearer explicit-key"}},
					}
					var errMsg *interfaces.ErrorMessage
					switch operation {
					case "execute":
						_, errMsg = handler.ExecuteModel(ctx, req)
					case "stream":
						req.Stream = true
						var stream handlers.ModelExecutionStream
						stream, errMsg = handler.ExecuteModelStream(ctx, req)
						if errMsg == nil {
							for chunk := range stream.Chunks {
								if chunk.Err != nil {
									t.Fatalf("stream error: %v", chunk.Err)
								}
							}
						}
					case "count":
						_, _, errMsg = handler.ExecuteCountWithAuthManager(ctx, "openai", homePluginAlias, body, "")
					}
					wantStatus, wantExecutions, wantAuthorizations := http.StatusOK, int32(1), int32(1)
					if behavior != "replace" {
						wantStatus, wantExecutions = http.StatusUnauthorized, 0
					}
					if behavior == "terminate" {
						wantStatus, wantAuthorizations = http.StatusForbidden, 0
					}
					status := http.StatusOK
					if errMsg != nil {
						status = errMsg.StatusCode
					}
					if status != wantStatus || interceptions.Load() != 1 || authorizations.Load() != wantAuthorizations || executions.Load() != wantExecutions {
						t.Fatalf("status=%d interceptions=%d authorizations=%d executions=%d, want %d/1/%d/%d; error=%v", status, interceptions.Load(), authorizations.Load(), executions.Load(), wantStatus, wantAuthorizations, wantExecutions, errMsg)
					}
					wantPops := int32(0)
					if route != "local" && behavior == "replace" {
						wantPops = 1
					}
					if credentialPops.Load() != wantPops {
						t.Fatalf("credential dispatches = %d, want %d", credentialPops.Load(), wantPops)
					}
				})
			}
		}
	}
}
