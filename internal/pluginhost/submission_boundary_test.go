package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestPluginSubmissionGenerationAndAuxiliaryBoundaries(t *testing.T) {
	for _, profile := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, auxiliary := range []bool{false, true} {
				t.Run(fmt.Sprintf("profile=%t/stream=%t/auxiliary=%t", profile, stream, auxiliary), func(t *testing.T) {
					var tokens, generations atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						_, _ = io.Copy(io.Discard, r.Body)
						if r.URL.Path == "/token" {
							tokens.Add(1)
							_, _ = io.WriteString(w, `{"access_token":"synthetic"}`)
							return
						}
						count := generations.Add(1)
						if auxiliary && count == 1 {
							w.WriteHeader(http.StatusTooManyRequests)
							return
						}
						_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"safe-fallback"}}]}`)
					}))
					defer upstream.Close()
					const provider = "plugin-submission-boundary"
					const model = "plugin-submission-model"
					record := normalizeTestCapabilityRecord(capabilityRecord{id: "plugin-submission-" + t.Name()})
					host := newHostWithRecords(record)
					exec := &fakeExecutor{identifier: provider, execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
						if auxiliary {
							doc := ojson.NewDocString(`{}`)
							prefix := ""
							if profile {
								prefix = "request."
							}
							if err := doc.Set(prefix+"method", http.MethodPost); err != nil {
								return pluginapi.ExecutorResponse{}, err
							}
							if err := doc.Set(prefix+"auxiliary", true); err != nil {
								return pluginapi.ExecutorResponse{}, err
							}
							if err := doc.Set(prefix+"url", upstream.URL+"/token"); err != nil {
								return pluginapi.ExecutorResponse{}, err
							}
							tokenReq, errDecode := decodeHostHTTPRequest(doc.Bytes())
							if errDecode != nil {
								return pluginapi.ExecutorResponse{}, errDecode
							}
							if _, err := req.HTTPClient.Do(ctx, tokenReq); err != nil {
								return pluginapi.ExecutorResponse{}, err
							}
						}
						generation := pluginapi.HTTPRequest{Method: http.MethodPost, URL: upstream.URL + "/generate", Body: []byte(`{}`)}
						if profile {
							generation.WireProfile = &pluginapi.HTTPWireProfile{HTTP1Only: true}
						}
						var resp pluginapi.HTTPResponse
						var err error
						if stream {
							var streamed pluginapi.HTTPStreamResponse
							streamed, err = req.HTTPClient.DoStream(ctx, generation)
							resp.StatusCode = streamed.StatusCode
							if err == nil {
								for chunk := range streamed.Chunks {
									resp.Body = append(resp.Body, chunk.Payload...)
									if chunk.Err != nil {
										err = chunk.Err
									}
								}
							}
						} else {
							resp, err = req.HTTPClient.Do(ctx, generation)
						}
						if err != nil {
							return pluginapi.ExecutorResponse{}, err
						}
						if auxiliary && resp.StatusCode == 200 {
							return pluginapi.ExecutorResponse{Payload: resp.Body}, nil
						}
						status := 503
						if auxiliary {
							status = resp.StatusCode
						}
						return pluginapi.ExecutorResponse{}, testHostStatusError{error: errors.New("synthetic plugin failure"), status: status}
					}}
					adapter := newExecutorAdapterForRecordForTest(host, record, exec, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})
					adapter.provider = provider
					manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
					manager.SetRetryConfig(0, 0, 0)
					manager.RegisterExecutor(adapter)
					for _, suffix := range []string{"a", "b"} {
						auth := &coreauth.Auth{ID: t.Name() + suffix, Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct"}
						if _, err := manager.Register(context.Background(), auth); err != nil {
							t.Fatal(err)
						}
						registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					}
					_, err := manager.Execute(context.Background(), []string{provider}, coreexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
					t.Logf("token POSTs=%d generation POSTs=%d error=%v", tokens.Load(), generations.Load(), err)
					if auxiliary {
						if err != nil || tokens.Load() != 2 || generations.Load() != 2 {
							t.Fatalf("auxiliary token prevented generation failover: %v", err)
						}
					} else if generations.Load() != 1 || !httptransport.IsOutcomeUnknown(err) {
						t.Fatalf("plugin replayed an accepted local failure: %v", err)
					}
				})
			}
		}
	}
}

func TestPluginSubmissionCredentialHTTPIsAuxiliaryByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"access_token":"synthetic"}`)
	}))
	defer server.Close()
	ctx := httptransport.WithSubmissionTracking(context.Background())
	_, err := New().newHTTPClient(nil).Do(ctx, pluginapi.HTTPRequest{Method: http.MethodPost, URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	local := errors.New("model was not sent")
	if httptransport.ClassifySubmissionError(ctx, local) != local {
		t.Fatal("credential HTTP accepted a generation")
	}
}
