package executor

import (
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestVertexCredsADCMode verifies that a vertex auth carrying the ADC marker
// (synthesized from a vertex-adc config entry) resolves project/location and
// yields no service account JSON, routing token resolution to Application
// Default Credentials.
func TestVertexCredsADCMode(t *testing.T) {
	a := &cliproxyauth.Auth{
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id": "proj-1",
			"location":   "global",
			"adc":        true,
		},
	}
	projectID, location, saJSON, err := vertexCreds(a)
	if err != nil {
		t.Fatalf("vertexCreds(adc) unexpected error: %v", err)
	}
	if projectID != "proj-1" {
		t.Errorf("projectID = %q, want %q", projectID, "proj-1")
	}
	if location != "global" {
		t.Errorf("location = %q, want %q", location, "global")
	}
	if len(saJSON) != 0 {
		t.Errorf("service account JSON = %q, want empty in ADC mode", saJSON)
	}
}

// TestVertexCredsADCDefaultsLocationToUsCentral1 verifies the location default
// when a vertex-adc entry omits it.
func TestVertexCredsADCDefaultsLocationToUsCentral1(t *testing.T) {
	a := &cliproxyauth.Auth{
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id": "proj-1",
			"adc":        true,
		},
	}
	_, location, _, err := vertexCreds(a)
	if err != nil {
		t.Fatalf("vertexCreds(adc, no location) unexpected error: %v", err)
	}
	if location != "us-central1" {
		t.Errorf("location = %q, want default %q", location, "us-central1")
	}
}

// TestVertexCredsMissingServiceAccountWithoutADCFlag preserves the historical
// fail-loud behavior: a vertex auth without a service account AND without the
// explicit ADC marker is a misconfiguration, not an implicit ADC fallback.
func TestVertexCredsMissingServiceAccountWithoutADCFlag(t *testing.T) {
	a := &cliproxyauth.Auth{
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id": "proj-1",
		},
	}
	_, _, _, err := vertexCreds(a)
	if err == nil {
		t.Fatal("expected error for missing service_account without adc marker")
	}
	if !strings.Contains(err.Error(), "missing service_account") {
		t.Errorf("error = %v, want it to mention missing service_account", err)
	}
}

// TestVertexCredsADCStillRequiresProject verifies project_id remains required
// in ADC mode instead of failing later at request time with an opaque 404.
func TestVertexCredsADCStillRequiresProject(t *testing.T) {
	a := &cliproxyauth.Auth{
		Provider: "vertex",
		Metadata: map[string]any{
			"location": "global",
			"adc":      true,
		},
	}
	_, _, _, err := vertexCreds(a)
	if err == nil {
		t.Fatal("expected error for missing project_id in ADC mode")
	}
	if !strings.Contains(err.Error(), "missing project_id") {
		t.Errorf("error = %v, want it to mention missing project_id", err)
	}
}

// TestADCQuotaProject verifies quota_project_id extraction from raw ADC
// credentials JSON: user credentials carry it, metadata-server credentials
// (no JSON) do not, and malformed JSON yields none.
func TestADCQuotaProject(t *testing.T) {
	userCreds := []byte(`{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"rt","quota_project_id":"quota-proj"}`)
	if got := adcQuotaProject(userCreds); got != "quota-proj" {
		t.Errorf("adcQuotaProject(user creds) = %q, want quota-proj", got)
	}
	if got := adcQuotaProject(nil); got != "" {
		t.Errorf("adcQuotaProject(nil) = %q, want empty", got)
	}
	if got := adcQuotaProject([]byte(`{"type":"authorized_user"}`)); got != "" {
		t.Errorf("adcQuotaProject(no quota field) = %q, want empty", got)
	}
	if got := adcQuotaProject([]byte(`{not-json`)); got != "" {
		t.Errorf("adcQuotaProject(malformed) = %q, want empty", got)
	}
}

// TestApplyVertexAuthHeaderQuotaProject verifies the quota project is sent as
// x-goog-user-project only when present, alongside the bearer token.
func TestApplyVertexAuthHeaderQuotaProject(t *testing.T) {
	h := http.Header{}
	applyVertexAuthHeader(h, "tok", "quota-proj")
	if got := h.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", got)
	}
	if got := h.Get("x-goog-user-project"); got != "quota-proj" {
		t.Errorf("x-goog-user-project = %q, want quota-proj", got)
	}

	h = http.Header{}
	applyVertexAuthHeader(h, "tok", "")
	if got := h.Get("x-goog-user-project"); got != "" {
		t.Errorf("x-goog-user-project = %q, want empty for project-bound credentials", got)
	}
}
