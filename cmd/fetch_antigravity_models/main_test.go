package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDefaultAntigravityFetchBaseURLs(t *testing.T) {
	want := []string{
		antigravityBaseURLDaily,
		antigravityBaseURLProd,
		antigravitySandboxBaseURLDaily,
	}

	got := defaultAntigravityFetchBaseURLs()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaultAntigravityFetchBaseURLs() = %#v, want %#v", got, want)
	}
}

func TestFetchModelsRetryPerEndpoint(t *testing.T) {
	var endpoint1Calls atomic.Int32
	var endpoint2Calls atomic.Int32

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := endpoint1Calls.Add(1)
		if call == 1 {
			http.Error(w, `{"error":"temporary server error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"models": {
				"gemini-3.6-flash": {
					"displayName": "Gemini 3.6 Flash",
					"maxTokens": 1048576,
					"maxOutputTokens": 8192
				}
			}
		}`))
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint2Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"models": {
				"gemini-1.5-pro": {
					"displayName": "Gemini 1.5 Pro"
				}
			}
		}`))
	}))
	defer server2.Close()

	auth := &coreauth.Auth{
		Metadata: map[string]interface{}{
			"access_token": "test-token",
			"project_id":   "test-project",
		},
	}

	// Case 1: First endpoint fails on attempt 1, succeeds on attempt 2.
	// It should succeed without calling endpoint 2, and endpoint 1 should have been called 2 times.
	models := fetchModelsFromBaseURLs(context.Background(), auth, []string{server1.URL, server2.URL}, server1.Client())
	if len(models) != 1 || models[0].ID != "gemini-3.6-flash" {
		t.Fatalf("expected 1 model (gemini-3.6-flash), got: %#v", models)
	}
	if calls := endpoint1Calls.Load(); calls != 2 {
		t.Fatalf("expected endpoint 1 to be called 2 times, got %d", calls)
	}
	if calls := endpoint2Calls.Load(); calls != 0 {
		t.Fatalf("expected endpoint 2 not to be called, got %d", calls)
	}
}

func TestFetchModelsFallbackAfterTwoAttempts(t *testing.T) {
	var endpoint1Calls atomic.Int32
	var endpoint2Calls atomic.Int32

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint1Calls.Add(1)
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint2Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"models": {
				"gemini-2.5-flash": {
					"displayName": "Gemini 2.5 Flash"
				}
			}
		}`))
	}))
	defer server2.Close()

	auth := &coreauth.Auth{
		Metadata: map[string]interface{}{
			"access_token": "test-token",
		},
	}

	// Case 2: First endpoint fails all 2 attempts, then falls back to endpoint 2.
	models := fetchModelsFromBaseURLs(context.Background(), auth, []string{server1.URL, server2.URL}, server1.Client())
	if len(models) != 1 || models[0].ID != "gemini-2.5-flash" {
		t.Fatalf("expected 1 model (gemini-2.5-flash), got: %#v", models)
	}
	if calls := endpoint1Calls.Load(); calls != 2 {
		t.Fatalf("expected endpoint 1 to be called 2 times before fallback, got %d", calls)
	}
	if calls := endpoint2Calls.Load(); calls != 1 {
		t.Fatalf("expected endpoint 2 to be called 1 time, got %d", calls)
	}
}

func TestFetchModelsSetsUserProjectHeader(t *testing.T) {
	cases := []struct {
		name       string
		projectID  string
		wantHeader string
		wantExists bool
	}{
		{name: "sets trimmed project_id header", projectID: "  test-project  ", wantHeader: "test-project", wantExists: true},
		{name: "omits header when project_id absent", projectID: "", wantHeader: "", wantExists: false},
		{name: "omits header when project_id whitespace", projectID: "   ", wantHeader: "", wantExists: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotHeaders http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeaders = r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{"displayName":"Gemini 3 Flash"}}}`))
			}))
			defer server.Close()

			meta := map[string]interface{}{
				"access_token": "test-token",
			}
			if tc.projectID != "" {
				meta["project_id"] = tc.projectID
			}
			auth := &coreauth.Auth{Metadata: meta}

			models := fetchModelsFromBaseURLs(context.Background(), auth, []string{server.URL}, server.Client())
			if len(models) != 1 {
				t.Fatalf("expected 1 model, got %d", len(models))
			}
			_, exists := gotHeaders["X-Goog-User-Project"]
			if exists != tc.wantExists {
				t.Fatalf("X-Goog-User-Project exists = %v, want %v (headers=%v)", exists, tc.wantExists, gotHeaders)
			}
			if got := gotHeaders.Get("X-Goog-User-Project"); got != tc.wantHeader {
				t.Fatalf("X-Goog-User-Project = %q, want %q", got, tc.wantHeader)
			}
		})
	}

	t.Run("does not follow cross-origin redirect and falls back without leaking header", func(t *testing.T) {
		var leakedHits atomic.Int32
		redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			leakedHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":{"leaked-model":{"displayName":"Leaked"}}}`))
		}))
		defer redirectTarget.Close()

		redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectTarget.URL+"/steal", http.StatusFound)
		}))
		defer redirectSource.Close()

		fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{"displayName":"Gemini 3 Flash"}}}`))
		}))
		defer fallback.Close()

		auth := &coreauth.Auth{
			Metadata: map[string]interface{}{
				"access_token": "test-token",
				"project_id":   "test-project",
			},
		}

		models := fetchModelsFromBaseURLs(context.Background(), auth, []string{redirectSource.URL, fallback.URL}, redirectSource.Client())
		if got := leakedHits.Load(); got != 0 {
			t.Fatalf("cross-origin redirect target hits = %d, want 0", got)
		}
		if len(models) != 1 || models[0].ID != "gemini-3-flash" {
			t.Fatalf("models = %#v, want fallback gemini-3-flash", models)
		}
	})
}
