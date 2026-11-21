package main

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func fixtureModels() map[string]ModelTier {
	return map[string]ModelTier{
		"claude-opus-4-8":   {Family: "claude", Tier: 75},
		"claude-sonnet-4-6": {Family: "claude", Tier: 15},
		"gpt-5.6-sol":       {Family: "gpt", Tier: 30},
	}
}

func TestConsent(t *testing.T) {
	for _, header := range []string{"", "0", "true", " 1", "1 ", "1,1", "1"} {
		t.Run(header, func(t *testing.T) {
			d := Intercept("claude-opus-4-8", "gpt-5.6-sol", header, fixtureModels(), 0, func(_, _ string) bool { return true })
			want := header == "1"
			if d.Stopped == want || d.CrossFamilyConsent != want {
				t.Fatalf("header=%q: %+v", header, d)
			}
		})
	}
}

func TestInFamilyDefault(t *testing.T) {
	asks := 0
	d := Intercept("claude-opus-4-8", "claude-sonnet-4-6", "", fixtureModels(), 0, func(_, _ string) bool { asks++; return true })
	if d.Stopped || d.Model != "claude-sonnet-4-6" || asks != 1 {
		t.Fatalf("expected an asked in-family downgrade, got %+v asks=%d", d, asks)
	}
}

func TestNeverPromote(t *testing.T) {
	asks := 0
	d := Intercept("claude-sonnet-4-6", "claude-opus-4-8", "1", fixtureModels(), 0, func(_, _ string) bool { asks++; return true })
	if !d.Stopped || d.Reason != "upward_tier" || asks != 0 {
		t.Fatalf("promotion was not blocked: %+v asks=%d", d, asks)
	}
}

func TestMissingAndDeniedAsk(t *testing.T) {
	for _, ask := range []Ask{nil, func(_, _ string) bool { return false }} {
		d := Intercept("claude-opus-4-8", "claude-sonnet-4-6", "", fixtureModels(), 0, ask)
		if !d.Stopped || d.Model != "claude-opus-4-8" || d.Line != "No confirmed lower-tier model in the same family. Stopped on claude-opus-4-8." {
			t.Fatalf("expected visible stop: %+v", d)
		}
	}
}

func TestRetryAndTierLookup(t *testing.T) {
	models := fixtureModels()
	allow := func(_, _ string) bool { return true }
	if d := Intercept("claude-opus-4-8", "claude-opus-4-8", "", models, 0, allow); d.Stopped {
		t.Fatalf("first asked retry blocked: %+v", d)
	}
	if d := Intercept("claude-opus-4-8", "claude-opus-4-8", "", models, 1, allow); !d.Stopped {
		t.Fatal("second same-model retry allowed")
	}
	for _, candidate := range []string{"missing", "invalid"} {
		models["invalid"] = ModelTier{Family: "claude", Tier: math.NaN()}
		if d := Intercept("claude-opus-4-8", candidate, "", models, 0, allow); !d.Stopped {
			t.Fatalf("unknown or invalid tier was allowed: %+v", d)
		}
	}
}

func TestNormalization(t *testing.T) {
	for _, value := range []string{"cpa-base/claude-opus-4-8-high", "cpa-fast:claude-opus-4-8/low", "cpa-deep/claude-opus-4-8(max)"} {
		if got := normalizeModel(value); got != "claude-opus-4-8" {
			t.Fatalf("normalize %q = %q", value, got)
		}
	}
}

func TestInitialRequestPassesUnchanged(t *testing.T) {
	r, err := interceptRequest(pluginapi.RequestInterceptRequest{Model: "claude-opus-4-8", Body: []byte(`{"model":"claude-opus-4-8"}`)})
	if err != nil || r.Terminate || len(r.Body) != 0 || len(r.Headers) != 0 {
		t.Fatalf("initial request changed: %+v %v", r, err)
	}
}

func TestNativeInterceptorRequiresExactAskApproval(t *testing.T) {
	for _, proposed := range []string{"claude-sonnet-4-6", "claude-opus-4-8", "gpt-5.6-sol"} {
		t.Run(proposed, func(t *testing.T) {
			r, err := interceptRequest(pluginapi.RequestInterceptRequest{
				Model: proposed,
				Metadata: map[string]any{"family_alias_ask_gate": gateContext{
					Requested: "claude-opus-4-8", Models: fixtureModels(),
					ApprovedModel: "claude-sonnet-4-6", AskAnswer: "allow",
				}},
			})
			if err != nil || r.Terminate != (proposed != "claude-sonnet-4-6") {
				t.Fatalf("candidate=%s response=%+v err=%v", proposed, r, err)
			}
		})
	}
}

func TestDuplicateConsentHeaderDenied(t *testing.T) {
	h := http.Header{"X-Open-Design-Cross-Family": {"1"}, "x-open-design-cross-family": {"1"}}
	if got := consentHeader(h); got != "" {
		t.Fatalf("duplicate header accepted: %q", got)
	}
}

func TestRegistrationAndInterceptRPC(t *testing.T) {
	registration, err := handleMethod("plugin.register", []byte(`{}`))
	if err != nil || !strings.Contains(string(registration), `"request_interceptor":true`) {
		t.Fatalf("registration failed: %s %v", registration, err)
	}
	req, err := json.Marshal(pluginapi.RequestInterceptRequest{
		Model: "gpt-5.6-sol", RequestedModel: "claude-opus-4-8",
		Metadata: map[string]any{"family_alias_ask_gate": gateContext{Requested: "claude-opus-4-8", Models: fixtureModels()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handleMethod("request.intercept_before", req)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result pluginapi.RequestInterceptResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Result.Terminate || reply.Result.StatusCode != http.StatusConflict || !strings.Contains(string(reply.Result.ResponseBody), "Stopped on claude-opus-4-8") {
		t.Fatalf("RPC did not terminate visibly: %s", raw)
	}
}
