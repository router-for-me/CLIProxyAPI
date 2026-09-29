package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestNeuralwattRequestInjectsServiceTier(t *testing.T) {
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[]}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"service_tier": "flex"}}

	got := applyNeuralwattServiceTier(payload, auth)
	if gjson.GetBytes(got, "service_tier").String() != "flex" {
		t.Fatalf("service_tier = %q, want flex", gjson.GetBytes(got, "service_tier").String())
	}
}

func TestNeuralwattRequestLeavesTierUnsetWhenNotConfigured(t *testing.T) {
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[]}`)
	got := applyNeuralwattServiceTier(payload, &cliproxyauth.Auth{})
	if gjson.GetBytes(got, "service_tier").Exists() {
		t.Fatal("service_tier must not be injected when the credential sets no tier")
	}
}

func TestNeuralwattRequestRejectsValuesOutsideWhitelist(t *testing.T) {
	for _, tier := range []string{"", "standard", "FLEX", "Default", "flexx", " priority"} {
		t.Run("tier="+tier, func(t *testing.T) {
			payload := []byte(`{"model":"deepseek-v4-pro","messages":[]}`)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"service_tier": tier}}

			got := applyNeuralwattServiceTier(payload, auth)
			if gjson.GetBytes(got, "service_tier").Exists() {
				t.Fatalf("service_tier = %q must not be injected", tier)
			}
		})
	}
}

func TestNeuralwattRequestInjectsDefaultTier(t *testing.T) {
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[]}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"service_tier": "default"}}

	got := applyNeuralwattServiceTier(payload, auth)
	if gjson.GetBytes(got, "service_tier").String() != "default" {
		t.Fatalf("service_tier = %q, want default", gjson.GetBytes(got, "service_tier").String())
	}
}

func TestNeuralwattRequestDoesNotMutateCallerPayload(t *testing.T) {
	payload := []byte(`{"model":"deepseek-v4-pro","messages":[]}`)
	before := string(payload)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"service_tier": "flex"}}

	_ = applyNeuralwattServiceTier(payload, auth)
	if string(payload) != before {
		t.Fatalf("caller payload mutated: %s", payload)
	}
}

func TestNeuralwattRequestDoesNotFabricateForEmptyOrMalformedPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{name: "nil", payload: nil},
		{name: "empty", payload: []byte("")},
		{name: "garbage", payload: []byte("not json")},
		{name: "truncated", payload: []byte("{")},
		{name: "unterminated string", payload: []byte(`{"model":"oops`)},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"service_tier": "flex"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyNeuralwattServiceTier(tc.payload, auth)
			if string(got) != string(tc.payload) {
				t.Fatalf("expected passthrough %q, got %q — helper fabricated a document", string(tc.payload), string(got))
			}
		})
	}
}

func TestNeuralwattRequestNilPayloadStaysNil(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"service_tier": "flex"}}
	got := applyNeuralwattServiceTier(nil, auth)
	if got != nil {
		t.Fatalf("expected nil, got %q — sjson would otherwise fabricate {\"service_tier\":\"flex\"}", string(got))
	}
}
