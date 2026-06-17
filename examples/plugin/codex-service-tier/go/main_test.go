package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestShouldSetPriorityServiceTier(t *testing.T) {
	cases := []struct {
		name     string
		fast     bool
		toFormat string
		model    string
		want     bool
	}{
		{
			name:     "gpt-5.4 with codex fast",
			fast:     true,
			toFormat: "codex",
			model:    "gpt-5.4",
			want:     true,
		},
		{
			name:     "gpt-5.5 with codex fast",
			fast:     true,
			toFormat: "codex",
			model:    "gpt-5.5",
			want:     true,
		},
		{
			name:     "other model with codex fast",
			fast:     true,
			toFormat: "codex",
			model:    "gpt-4",
			want:     false,
		},
		{
			name:     "gpt-5.5 with non-codex fast",
			fast:     true,
			toFormat: "openai",
			model:    "gpt-5.5",
			want:     false,
		},
		{
			name:     "gpt-5.4 with codex but fast disabled",
			fast:     false,
			toFormat: "codex",
			model:    "gpt-5.4",
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fastEnabled.Store(tc.fast)
			req := pluginapi.RequestTransformRequest{
				ToFormat: tc.toFormat,
				Model:    tc.model,
			}
			if got := shouldSetPriorityServiceTier(req); got != tc.want {
				t.Fatalf("shouldSetPriorityServiceTier() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeRequestInjectsPriorityServiceTierForBothModelsWhenFastEnabled(t *testing.T) {
	fastEnabled.Store(true)
	body := []byte(`{"input": "hello"}`)

	for _, model := range []string{"gpt-5.4", "gpt-5.5"} {
		t.Run(model, func(t *testing.T) {
			req := pluginapi.RequestTransformRequest{
				ToFormat: "codex",
				Model:    model,
				Body:     body,
			}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}

			respRaw, err := normalizeRequest(raw)
			if err != nil {
				t.Fatalf("normalizeRequest: %v", err)
			}

			var resp struct {
				OK     bool                      `json:"ok"`
				Result pluginapi.PayloadResponse `json:"result"`
			}
			if err := json.Unmarshal(respRaw, &resp); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if !resp.OK {
				t.Fatal("normalizeRequest should return ok response")
			}

			var updated map[string]any
			if err := json.Unmarshal(resp.Result.Body, &updated); err != nil {
				t.Fatalf("unmarshal updated body: %v", err)
			}
			if got := updated["service_tier"]; got != "priority" {
				t.Fatalf("service_tier = %v, want priority", got)
			}
		})
	}
}
