package pluginhost

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHomeLocalExecutorSameModelDelegationHonorsRouterExclusion(t *testing.T) {
	const originPlugin = "local-origin-plugin"
	const otherPlugin = "local-other-plugin"
	const model = "same-model"
	const alias = "acp/" + model
	const pluginYAML = "enabled: true\npriority: 100\n"
	configDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(pluginYAML)))
	body := []byte(`{"model":"` + alias + `","messages":[{"role":"user","content":"hello"}]}`)

	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			selected   string
			excluded   string
			wantReject bool
		}{
			{"excluded origin", originPlugin, originPlugin, true},
			{"trimmed exclusion", originPlugin, " " + originPlugin + " ", true},
			{"different executor", otherPlugin, originPlugin, false},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				var grants, originCalls, otherCalls atomic.Int32
				client := newHomeExecutorTestClient(t, func(command, argument string) []byte {
					var req struct {
						Type  string `json:"type"`
						Model string `json:"model"`
					}
					if errDecode := json.Unmarshal([]byte(argument), &req); errDecode != nil {
						t.Errorf("decode Home request: %v", errDecode)
						return []byte(`{"error":{"type":"error","message":"invalid request"}}`)
					}
					if command != "GET" || req.Type != "local-plugin-dispatch" || req.Model != alias {
						t.Errorf("unexpected Home request: %s %+v", command, req)
						return []byte(`{"error":{"type":"error","message":"unexpected dispatch"}}`)
					}
					selected := originPlugin
					if grants.Add(1) > 1 {
						selected = tc.selected
					}
					return []byte(fmt.Sprintf(`{"kind":"local-plugin","plugin":%q,"version":"1.0.0","config_digest":%q,"model":%q,"principal":"caller","access_provider":"config-api-key"}`, selected, configDigest, model))
				})
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
				manager.PublishHomeDispatch(client, executionregistry.New(), 1)
				handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)

				origin := &fakeExecutor{
					identifier: originPlugin,
					execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						if originCalls.Add(1) > 1 {
							return pluginapi.ExecutorResponse{}, errors.New("origin executor re-entered")
						}
						resp, errMsg := handler.ExecuteModel(ctx, handlers.ModelExecutionRequest{
							EntryProtocol: "openai", ExitProtocol: "openai", Model: alias,
							Body: body, Headers: req.Headers, SkipRouterPluginID: tc.excluded,
						})
						if errMsg != nil {
							return pluginapi.ExecutorResponse{}, errMsg.Error
						}
						return pluginapi.ExecutorResponse{Payload: resp.Body}, nil
					},
					executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
						if originCalls.Add(1) > 1 {
							return pluginapi.ExecutorStreamResponse{}, errors.New("origin executor re-entered")
						}
						resp, errMsg := handler.ExecuteModelStream(ctx, handlers.ModelExecutionRequest{
							EntryProtocol: "openai", ExitProtocol: "openai", Model: alias, Stream: true,
							Body: body, Headers: req.Headers, SkipRouterPluginID: tc.excluded,
						})
						if errMsg != nil {
							return pluginapi.ExecutorStreamResponse{}, errMsg.Error
						}
						chunks := make(chan pluginapi.ExecutorStreamChunk)
						go func() {
							defer close(chunks)
							for chunk := range resp.Chunks {
								var errChunk error
								if chunk.Err != nil {
									errChunk = chunk.Err
								}
								select {
								case chunks <- pluginapi.ExecutorStreamChunk{Payload: chunk.Payload, Err: errChunk}:
								case <-ctx.Done():
									return
								}
							}
						}()
						return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
					},
				}
				other := &fakeExecutor{
					identifier: otherPlugin,
					execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						otherCalls.Add(1)
						return pluginapi.ExecutorResponse{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
					},
					executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
						otherCalls.Add(1)
						chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
						chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")}
						close(chunks)
						return pluginapi.ExecutorStreamResponse{Chunks: chunks}, nil
					},
				}
				var records []capabilityRecord
				for _, executor := range []*fakeExecutor{origin, other} {
					record := normalizeTestCapabilityRecord(capabilityRecord{
						id: executor.identifier, version: "1.0.0",
						plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
							Executor: executor, ExecutorModelScope: pluginapi.ExecutorModelScopeBoth,
							ExecutorInputFormats: []string{"openai"}, ExecutorOutputFormats: []string{"openai"},
						}},
					})
					record.loaded = &loadedPlugin{id: record.id, path: record.path, version: record.version, configYAML: []byte(pluginYAML)}
					records = append(records, record)
				}
				host := newHostWithRecords(records...)
				for _, record := range records {
					host.loaded[record.id] = record.loaded
				}
				handler.SetModelRouterHost(host)
				req := handlers.ProtocolExecutionRequest{
					EntryProtocol: "openai", ExitProtocol: "openai", Model: alias, Stream: stream,
					Body: body, Headers: http.Header{"Authorization": {"Bearer caller"}},
				}
				var errExecute error
				var status int
				if stream {
					resp, errMsg := handler.ExecuteProtocolStreamWithAuthManager(t.Context(), req)
					if errMsg != nil {
						errExecute, status = errMsg.Error, errMsg.StatusCode
					} else {
						for chunk := range resp.Chunks {
							if chunk.Err != nil {
								t.Fatalf("unexpected stream error: %v", chunk.Err)
							}
						}
					}
				} else if _, errMsg := handler.ExecuteProtocolWithAuthManager(t.Context(), req); errMsg != nil {
					errExecute, status = errMsg.Error, errMsg.StatusCode
				}
				if originCalls.Load() != 1 || grants.Load() != 2 {
					t.Fatalf("origin executions=%d Home grants=%d, want 1 execution and 2 fresh grants", originCalls.Load(), grants.Load())
				}
				if tc.wantReject {
					var coded interface{ ErrorCode() string }
					if !errors.As(errExecute, &coded) || coded.ErrorCode() != "local_executor_excluded" || status != http.StatusBadRequest {
						t.Fatalf("error=%v status=%d, want local_executor_excluded with HTTP 400", errExecute, status)
					}
					if otherCalls.Load() != 0 {
						t.Fatal("rejected route fell back to another executor")
					}
				} else if errExecute != nil || otherCalls.Load() != 1 {
					t.Fatalf("error=%v other executions=%d, want successful delegation", errExecute, otherCalls.Load())
				}
			})
		}
	}
}
