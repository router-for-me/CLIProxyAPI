package executor

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type copilotHomeDispatcher struct{ qualified bool }

func (copilotHomeDispatcher) HeartbeatOK() bool       { return true }
func (copilotHomeDispatcher) AbortAmbiguousDispatch() {}

func (d copilotHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	doc := ojson.NewDocString(`{
		"provider":"copilot","model":"gpt-5.3-codex","auth_index":"copilot-home-mock",
		"request_retry":0,"selection_id":"copilot-home-test-selection",
		"auth":{"id":"copilot-home-mock","provider":"copilot","metadata":{
			"base_url":"https://api.enterprise.githubcopilot.com","access_token":"mock-short-token"
		}},
		"model_info":{"id":"gpt-5.3-codex","type":"copilot","context_length":32768,"thinking":{"levels":["low","medium"]}}
	}`)
	if d.qualified {
		for _, path := range []string{"model", "model_info.id"} {
			if errSet := doc.Set(path, "copilot/gpt-5.3-codex"); errSet != nil {
				return nil, errSet
			}
		}
	}
	return doc.Bytes(), nil
}

func TestCopilotHomeExecutionWithoutLocalModel(t *testing.T) {
	const model = "gpt-5.3-codex"
	modelDir, err := filepath.Abs("../../registry/models")
	if err != nil {
		t.Fatal(err)
	}
	original := registry.CatalogSources{
		Catalog:      filepath.Join(modelDir, "models.json"),
		CodexCatalog: filepath.Join(modelDir, "codex_client_models.json"),
		DevinCatalog: filepath.Join(modelDir, "devin_models.json"),
	}
	raw, err := os.ReadFile(original.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	doc := ojson.NewDoc(raw)
	if errSet := doc.Set("copilot", []string{}); errSet != nil {
		t.Fatal(errSet)
	}
	withoutCopilot := original
	withoutCopilot.Catalog = filepath.Join(t.TempDir(), "models.json")
	if errWrite := os.WriteFile(withoutCopilot.Catalog, doc.Bytes(), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	t.Cleanup(func() {
		if _, errRefresh := registry.RefreshModelCatalogsWithPolicy(context.Background(), original, false); errRefresh != nil {
			t.Error(errRefresh)
		}
	})
	if _, errRefresh := registry.RefreshModelCatalogsWithPolicy(t.Context(), withoutCopilot, false); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if registry.LookupStaticModelInfoByChannel(model, "copilot") != nil {
		t.Fatal("test model still present in the local Copilot catalog")
	}

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.PublishHomeDispatch(copilotHomeDispatcher{}, executionregistry.New(), 1)
	manager.RegisterExecutor(NewCopilotExecutor(cfg))
	previousEnabled, previousStats := redisqueue.Enabled(), redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	defer func() {
		redisqueue.SetUsageStatisticsEnabled(previousStats)
		redisqueue.SetEnabled(previousEnabled)
	}()
	usage, unsubscribe := redisqueue.SubscribeUsage()
	defer unsubscribe()

	for _, test := range []struct {
		name         string
		stream       bool
		qualified    bool
		countPayload string
		status       int
	}{
		{name: "ordinary", status: 200},
		{name: "stream", stream: true, status: 200},
		{name: "qualified ordinary", qualified: true, status: 200},
		{name: "qualified stream", qualified: true, stream: true, status: 200},
		{name: "qualified count", qualified: true, countPayload: `{"input":"Hello"}`, status: 200},
		{name: "qualified rate limited", qualified: true, status: 429},
		{name: "count/string", countPayload: `{"input":"Hello"}`, status: 200},
		{name: "count/message string", countPayload: `{"input":[{"type":"message","role":"user","content":"Hello"}]}`, status: 200},
		{name: "count/role string", countPayload: `{"input":[{"role":"user","content":"Hello"}]}`, status: 200},
		{name: "count/message parts", countPayload: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`, status: 200},
		{name: "count/role parts", countPayload: `{"input":[{"role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`, status: 200},
		{name: "rate limited", status: 429},
		{name: "unavailable", status: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager.PublishHomeDispatch(copilotHomeDispatcher{qualified: test.qualified}, executionregistry.New(), 1)
			executionModel := model
			if test.qualified {
				executionModel = "copilot/" + model
			}
			calls := 0
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.String() != "https://api.enterprise.githubcopilot.com/responses" ||
					req.Header.Get("Authorization") != "Bearer mock-short-token" {
					t.Error("upstream request did not use the dispatched credential and compat route")
				}
				raw, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				if ojson.GetBytesNoCopy(raw, "model").String() != model || ojson.GetBytesNoCopy(raw, "stream").Bool() != test.stream {
					t.Error("unexpected upstream model or stream setting")
				}
				body := `{"id":"resp-1","model":"gpt-5.3-codex","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1}}`
				if test.stream {
					body = "data: " + `{"type":"response.output_text.delta","delta":"OK"}` + "\n\n" +
						"data: " + `{"type":"response.completed","response":{"id":"resp-1","status":"completed","output":[]}}` + "\n\n"
				}
				if test.status != 200 {
					body = `{"error":{"message":"mock upstream failure"}}`
				}
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			intercepted := false
			opts := cliproxyexecutor.Options{
				SourceFormat: tr.FormatOpenAIResponse, Stream: test.stream,
				RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					intercepted = true
					plan, ok := req.Metadata[copilotRouteKey].(copilotAttemptRoute)
					if !ok || plan.route.Info == nil || plan.route.Info.ContextLength != 32768 ||
						req.ToFormat != tr.FormatOpenAIResponse || req.Model != executionModel {
						t.Error("route planner did not see the Home model information before interception")
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			}
			req := cliproxyexecutor.Request{Model: "copilot/home-alias", Payload: []byte(`{"input":"Hello"}`)}
			providers := []string{"copilot"}
			switch {
			case test.countPayload != "":
				req.Payload = []byte(test.countPayload)
				res, errCount := manager.ExecuteCount(ctx, providers, req, opts)
				if errCount != nil {
					t.Fatal(errCount)
				}
				if got := ojson.GetBytesNoCopy(res.Payload, "input_tokens").Int(); got != 1 {
					t.Fatalf("input_tokens = %d, want 1 for Hello", got)
				}
				if got := res.Headers.Get("X-Cpa-Token-Count"); got != "estimated" {
					t.Fatalf("X-Cpa-Token-Count = %q, want estimated", got)
				}
			case test.stream:
				stream, errStream := manager.ExecuteStream(ctx, providers, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				var output strings.Builder
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					output.Write(chunk.Payload)
				}
				if !strings.Contains(output.String(), "response.completed") {
					t.Fatal("missing stream completion")
				}
			default:
				res, errExecute := manager.Execute(ctx, providers, req, opts)
				if test.status == 200 {
					if errExecute != nil || ojson.GetBytesNoCopy(res.Payload, "status").String() != "completed" {
						t.Fatalf("unexpected execution result: %v", errExecute)
					}
				} else if errExecute == nil {
					t.Fatal("expected upstream failure")
				}
			}
			wantCalls := 1
			if test.countPayload != "" {
				wantCalls = 0
			}
			if calls != wantCalls || !intercepted || len(manager.List()) != 0 {
				t.Fatalf("calls=%d, want %d, intercept=%t, local credentials=%d", calls, wantCalls, intercepted, len(manager.List()))
			}
			if test.status != 200 {
				waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				for {
					select {
					case <-waitCtx.Done():
						t.Fatal("missing Copilot failure usage")
					case payload := <-usage:
						record := ojson.ParseBytes(payload)
						if record.Get("fail.status_code").Int() != int64(test.status) {
							continue
						}
						if record.Get("provider").String() != "copilot" || record.Get("model").String() != executionModel ||
							record.Get("auth_index").String() != "copilot-home-mock" {
							t.Fatal("failure usage is missing the dispatched credential or native model")
						}
						return
					}
				}
			}
		})
	}
}
