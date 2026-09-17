package zcode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func biz(w http.ResponseWriter, body any) { _ = json.NewEncoder(w).Encode(body) }

func TestResolver_ResolveZaiCredential(t *testing.T) {
	var gotToken string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/z/login", func(w http.ResponseWriter, r *http.Request) {
		gotToken = readBody(r)
		biz(w, map[string]any{"access_token": "biz-token"})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		biz(w, map[string]any{"data": map[string]any{
			"organizations": []any{map[string]any{
				"organizationId": "org-1",
				"projects":       []any{map[string]any{"projectId": "proj-1"}},
			}},
		}})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			biz(w, []any{})
			return
		}
		biz(w, map[string]any{"apiKey": "k-1"})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys/copy/", func(w http.ResponseWriter, r *http.Request) {
		biz(w, map[string]any{"secretKey": "s-1"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &Resolver{HTTP: srv.Client(), TestHost: srv.URL}
	cred, err := r.ResolveZaiCredential(context.Background(), "at-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotToken, `"token":"at-1"`) {
		t.Fatalf("z/login token = %q", gotToken)
	}
	if cred.FullKey() != "k-1.s-1" || cred.APIKey != "k-1" {
		t.Fatalf("bad credential: %+v", cred)
	}
}

func TestResolver_ResolveBigmodelCredential(t *testing.T) {
	// Bigmodel does NOT call z/login: the OAuth access token is the Authorization
	// value verbatim (no "Bearer " scheme) for the business API.
	var gotAuth string
	var zLoginCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/z/login", func(w http.ResponseWriter, r *http.Request) {
		zLoginCalled = true
		biz(w, map[string]any{"access_token": "unexpected"})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		biz(w, map[string]any{"data": map[string]any{
			"organizations": []any{map[string]any{
				"organizationId": "org-b",
				"projects":       []any{map[string]any{"projectId": "proj-b"}},
			}},
		}})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-b/projects/proj-b/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			biz(w, []any{})
			return
		}
		biz(w, map[string]any{"apiKey": "bm-key"})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-b/projects/proj-b/api_keys/copy/", func(w http.ResponseWriter, r *http.Request) {
		biz(w, map[string]any{"secretKey": "bm-secret"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &Resolver{HTTP: srv.Client(), TestHost: srv.URL}
	cred, err := r.ResolveCredential(context.Background(), "bm-oauth-token", "bigmodel")
	if err != nil {
		t.Fatal(err)
	}
	if zLoginCalled {
		t.Fatal("bigmodel resolution must not call z/login")
	}
	if gotAuth != "bm-oauth-token" {
		t.Fatalf("Authorization = %q, want the raw access token", gotAuth)
	}
	if cred.FullKey() != "bm-key.bm-secret" {
		t.Fatalf("credential = %+v", cred)
	}
}

func TestResolver_ZaiAuthUsesBearerScheme(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/z/login", func(w http.ResponseWriter, r *http.Request) {
		biz(w, map[string]any{"access_token": "biz-token"})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		biz(w, map[string]any{"data": map[string]any{
			"organizations": []any{map[string]any{
				"organizationId": "org-1",
				"projects":       []any{map[string]any{"projectId": "proj-1"}},
			}},
		}})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			biz(w, []any{})
			return
		}
		biz(w, map[string]any{"apiKey": "k-1"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &Resolver{HTTP: srv.Client(), TestHost: srv.URL}
	if _, err := r.ResolveCredential(context.Background(), "at-1", "zai"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer biz-token" {
		t.Fatalf("Authorization = %q, want \"Bearer biz-token\"", gotAuth)
	}
}

func TestResolver_UsesExistingApiKey(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/z/login", func(w http.ResponseWriter, r *http.Request) {
		biz(w, map[string]any{"access_token": "biz-token"})
	})
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		biz(w, map[string]any{"data": map[string]any{
			"organizations": []any{map[string]any{
				"organizationId": "org-1",
				"projects":       []any{map[string]any{"projectId": "proj-1"}},
			}},
		}})
	})
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		biz(w, []any{map[string]any{"name": "zcode-api-key", "apiKey": "existing"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := &Resolver{HTTP: srv.Client(), TestHost: srv.URL}
	cred, err := r.ResolveZaiCredential(context.Background(), "at-1")
	if err != nil {
		t.Fatal(err)
	}
	if cred.APIKey != "existing" {
		t.Fatalf("expected existing key, got %+v", cred)
	}
}
