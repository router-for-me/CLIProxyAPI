package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientAPIKeyProfilesAndPatchPayload(t *testing.T) {
	var patchBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/api-key-profiles":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"api-key-profiles":[{"index":1,"id":"team-a","alias":"Production","disabled":true,"masked_key":"abcd***wxyz"}]}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/api-key-profiles":
			if err := json.NewDecoder(r.Body).Decode(&patchBody); err != nil {
				t.Errorf("decode patch body: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, http: server.Client()}
	profiles, err := client.GetAPIKeyProfiles()
	if err != nil {
		t.Fatalf("GetAPIKeyProfiles: %v", err)
	}
	if len(profiles) != 1 || profiles[0].Index != 1 || profiles[0].ID != "team-a" || profiles[0].Alias != "Production" || !profiles[0].Disabled || profiles[0].MaskedKey != "abcd***wxyz" {
		t.Fatalf("profiles = %#v", profiles)
	}

	alias := ""
	disabled := false
	if err = client.PatchAPIKeyProfile(1, nil, &alias, &disabled); err != nil {
		t.Fatalf("PatchAPIKeyProfile: %v", err)
	}
	if got := patchBody["index"]; got != float64(1) {
		t.Fatalf("patch index = %#v, want 1", got)
	}
	if got, exists := patchBody["alias"]; !exists || got != "" {
		t.Fatalf("patch alias = %#v, exists=%v", got, exists)
	}
	if got, exists := patchBody["disabled"]; !exists || got != false {
		t.Fatalf("patch disabled = %#v, exists=%v", got, exists)
	}
	if _, exists := patchBody["id"]; exists {
		t.Fatalf("patch unexpectedly included id: %#v", patchBody)
	}

	alias = "Updated"
	if err = client.PatchAPIKeyProfileExpected(1, "team-a", nil, &alias, nil); err != nil {
		t.Fatalf("PatchAPIKeyProfileExpected: %v", err)
	}
	if got := patchBody["expected_id"]; got != "team-a" {
		t.Fatalf("expected_id = %#v, want team-a", got)
	}
}

func TestClientPutAPIKeyProfilesOmitsMaskedKey(t *testing.T) {
	var body []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode PUT body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, http: server.Client()}
	err := client.PutAPIKeyProfiles([]APIKeyProfile{{Index: 0, ID: "team-a", Alias: "Production", Disabled: true, MaskedKey: "abcd***wxyz"}})
	if err != nil {
		t.Fatalf("PutAPIKeyProfiles: %v", err)
	}
	if len(body) != 1 || body[0]["id"] != "team-a" || body[0]["alias"] != "Production" || body[0]["disabled"] != true {
		t.Fatalf("PUT body = %#v", body)
	}
	if _, exists := body[0]["masked_key"]; exists {
		t.Fatalf("PUT body included masked_key: %#v", body)
	}
}

func TestClientGetClientKeyUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/client-key-usage" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"currency":"USD","currencies":["USD"],"keys":[{"key_id":"team-a","summary":{"attempts":2,"tokens":{"total_tokens":30},"estimated_cost_micros_by_currency":{"USD":15}}}]}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, http: server.Client()}
	report, err := client.GetClientKeyUsage()
	if err != nil {
		t.Fatalf("GetClientKeyUsage: %v", err)
	}
	if !report.Enabled || len(report.Keys) != 1 || report.Keys[0].KeyID != "team-a" || report.Keys[0].Summary.Attempts != 2 || report.Keys[0].Summary.Tokens.TotalTokens != 30 || report.Keys[0].Summary.EstimatedCostMicrosByCurrency["USD"] != 15 {
		t.Fatalf("report = %#v", report)
	}
}

func TestClientGetsKeysAndProfilesFromOneSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api-keys":["raw-key"],"api-key-profiles":[{"index":0,"id":"team-a","alias":"Production","masked_key":"****"}]}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, http: server.Client()}
	keys, profiles, err := client.GetAPIKeysAndProfiles()
	if err != nil {
		t.Fatalf("GetAPIKeysAndProfiles: %v", err)
	}
	if len(keys) != 1 || keys[0] != "raw-key" || len(profiles) != 1 || profiles[0].ID != "team-a" {
		t.Fatalf("keys=%#v profiles=%#v", keys, profiles)
	}
}

func TestClientSendsKeyRevisionForRawKeyMutations(t *testing.T) {
	var patchBody map[string]any
	var deleteQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			if err := json.NewDecoder(r.Body).Decode(&patchBody); err != nil {
				t.Errorf("decode patch body: %v", err)
			}
		case http.MethodDelete:
			deleteQuery = r.URL.RawQuery
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, http: server.Client()}
	if err := client.EditAPIKeyExpectedRevision(2, "team-a", "revision-a", "new-key"); err != nil {
		t.Fatalf("EditAPIKeyExpectedRevision: %v", err)
	}
	if patchBody["expected_id"] != "team-a" || patchBody["expected_key_revision"] != "revision-a" {
		t.Fatalf("patch body = %#v", patchBody)
	}
	if err := client.DeleteAPIKeyExpectedRevision(2, "team-a", "revision-a"); err != nil {
		t.Fatalf("DeleteAPIKeyExpectedRevision: %v", err)
	}
	if !strings.Contains(deleteQuery, "expected_id=team-a") || !strings.Contains(deleteQuery, "expected_key_revision=revision-a") {
		t.Fatalf("delete query = %q", deleteQuery)
	}
}
