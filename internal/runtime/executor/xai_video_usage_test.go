package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestXAIExecutorVideoBillingWithoutTokens(t *testing.T) {
	for _, tc := range []struct {
		name, path, payload, response, billingID, cost string
		observed                                       bool
	}{
		{name: "create", path: "/v1/videos/generations", payload: `{"model":"grok-imagine-video"}`, response: `{"request_id":"vid_123"}`, billingID: "vid_123"},
		{name: "edit", path: "/v1/videos/edits", payload: `{"model":"grok-imagine-video"}`, response: `{"request_id":"vid_123"}`, billingID: "vid_123"},
		{name: "extend", path: "/v1/videos/extensions", payload: `{"model":"grok-imagine-video"}`, response: `{"request_id":"vid_123"}`, billingID: "vid_123"},
		{name: "poll pending", payload: `{"request_id":"vid_123"}`, response: `{"status":"pending"}`, billingID: "vid_123"},
		{name: "poll done", payload: `{"request_id":"vid_123"}`, response: `{"status":"done","video":{"url":"https://example.com/video.mp4"}}`, billingID: "vid_123"},
		{name: "response identity wins", payload: `{"request_id":"vid_123"}`, response: `{"request_id":"vid_456","status":"done"}`, billingID: "vid_456"},
		{name: "create cost only", path: "/v1/videos/generations", payload: `{"model":"grok-imagine-video"}`, response: `{"request_id":"vid_123","usage":{"cost_in_usd_ticks":250000}}`, billingID: "vid_123", cost: "0.0000250000", observed: true},
		{name: "poll cost only", payload: `{"request_id":"vid_123"}`, response: `{"status":"done","usage":{"cost_in_usd_ticks":250000}}`, billingID: "vid_123", cost: "0.0000250000", observed: true},
		{name: "reported zero cost", payload: `{"request_id":"vid_123"}`, response: `{"status":"done","usage":{"cost_in_usd_ticks":0}}`, billingID: "vid_123", cost: "0.0000000000", observed: true},
		{name: "unknown identity and cost", payload: `{"model":"grok-imagine-video"}`, response: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &websocketUsageCapture{authID: t.Name()}
			usage.RegisterNamedPlugin(t.Name(), capture)
			defer usage.RegisterNamedPlugin(t.Name(), &websocketUsageCapture{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.response)
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "xai", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test"}}
			_, err := NewXAIExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model: "grok-imagine-video", Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-video"), Metadata: map[string]any{cliproxyexecutor.RequestPathMetadataKey: tc.path}})
			if err != nil {
				t.Fatal(err)
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if len(capture.records) != 1 {
				t.Fatalf("got %d events, want 1", len(capture.records))
			}
			record := capture.records[0]
			detail := record.Detail
			wantID, wantScope := "", ""
			if tc.billingID != "" {
				wantID, wantScope = "xai-video/"+tc.billingID, "operation"
			}
			if record.Failed || detail.BillingID != wantID || detail.CostScope != wantScope {
				t.Errorf("failed=%v, billing ID=%q, scope=%q; want false, %q, %q", record.Failed, detail.BillingID, detail.CostScope, wantID, wantScope)
			}
			if detail.UsageObserved != tc.observed || detail.InputTokens != 0 || detail.OutputTokens != 0 || detail.TotalTokens != 0 {
				t.Errorf("unexpected token usage: %+v", detail)
			}
			if tc.cost == "" {
				if detail.CostUSD != nil {
					t.Errorf("missing cost became reported cost %q", *detail.CostUSD)
				}
			} else if detail.CostUSD == nil || *detail.CostUSD != tc.cost {
				t.Errorf("cost = %v, want %q", detail.CostUSD, tc.cost)
			}
		})
	}
}

func TestXAIExecutorVideoPollsShareBillingIdentityAndKeepDistinctEvents(t *testing.T) {
	capture := &websocketUsageCapture{authID: t.Name()}
	usage.RegisterNamedPlugin(t.Name(), capture)
	defer usage.RegisterNamedPlugin(t.Name(), &websocketUsageCapture{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_, _ = fmt.Fprint(w, `{"request_id":"vid_123"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"done","usage":{"cost_in_usd_ticks":250000}}`)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "xai", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test"}}
	executor := NewXAIExecutor(&config.Config{})
	for _, payload := range []string{`{"model":"grok-imagine-video"}`, `{"request_id":"vid_123"}`, `{"request_id":"vid_123"}`} {
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "grok-imagine-video", Payload: []byte(payload)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-video")})
		if err != nil {
			t.Fatal(err)
		}
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.records) != 3 {
		t.Fatalf("got %d events, want 3 HTTP attempts", len(capture.records))
	}
	ids := make(map[string]bool)
	for _, record := range capture.records {
		if record.EventID == "" || ids[record.EventID] {
			t.Errorf("missing or duplicate event ID %q", record.EventID)
		}
		ids[record.EventID] = true
		if record.Detail.BillingID != "xai-video/vid_123" || record.Detail.CostScope != "operation" {
			t.Errorf("lost shared operation identity: %+v", record.Detail)
		}
	}
}
