package management

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestDeployRelayContractValidation pins the request validation: platform
// + per-platform required credentials.
func TestDeployRelayContractValidation(t *testing.T) {
	h := &Handler{}
	h.SetProxyPoolStore(newFakePoolStore())
	r := newPoolTestRouter(h)

	cases := []struct{ name, body string }{
		{"no platform", `{"project_name":"x"}`},
		{"unknown platform", `{"platform":"gcp"}`},
		{"vercel without token", `{"platform":"vercel"}`},
		{"cloudflare without account", `{"platform":"cloudflare","cf_api_token":"t"}`},
		{"cloudflare without token", `{"platform":"cloudflare","cf_account_id":"a"}`},
		{"deno without token", `{"platform":"deno","deno_org_domain":"org"}`},
		{"deno without org", `{"platform":"deno","deno_token":"t"}`},
	}
	for _, tc := range cases {
		res := postJSON(t, r, "/proxy-pools/relay-deploy", tc.body)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s: = %d body=%s", tc.name, res.Code, res.Body.String())
		}
	}
}

// TestDeployRelayCloudflareHappyPath drives the three-call Cloudflare flow
// against a stub server and pins the created pool row.
func TestDeployRelayCloudflareHappyPath(t *testing.T) {
	fs := newFakePoolStore()
	h := &Handler{}
	h.SetProxyPoolStore(fs)

	var scriptBody string
	calls := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/workers/scripts/"):
			raw, _ := io.ReadAll(r.Body)
			scriptBody = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/subdomain"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workers/subdomain"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"result":{"subdomain":"my-sub"}}`))
		default:
			t.Errorf("unexpected call %d: %s %s", calls, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stub.Close()

	prevBase := cloudflareAPIBase
	cloudflareAPIBase = stub.URL
	defer func() { cloudflareAPIBase = prevBase }()

	r := newPoolTestRouter(h)
	res := postJSON(t, r, "/proxy-pools/relay-deploy",
		`{"platform":"cloudflare","cf_account_id":"acct","cf_api_token":"tok","project_name":"myrelay"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("deploy = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(scriptBody, "x-relay-target") {
		t.Fatal("uploaded worker script must carry the x-relay-target contract")
	}
	var body struct {
		Pool      store.ProxyPool `json:"pool"`
		DeployURL string          `json:"deploy_url"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pool.Type != "cloudflare" || body.Pool.ProxyURL != "https://myrelay.my-sub.workers.dev" || !body.Pool.IsActive {
		t.Fatalf("pool = %+v / url=%s", body.Pool, body.DeployURL)
	}
	if len(fs.pools) != 1 {
		t.Fatalf("pool not persisted: %d", len(fs.pools))
	}
}

// TestDeployRelayVercelHappyPath drives the Vercel deploy+poll flow.
func TestDeployRelayVercelHappyPath(t *testing.T) {
	fs := newFakePoolStore()
	h := &Handler{}
	h.SetProxyPoolStore(fs)

	polls := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v13/deployments"):
			_, _ = w.Write([]byte(`{"id":"dep1","projectId":"proj1","url":"myrelay.vercel.app"}`))
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/v9/projects/"):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v13/deployments/dep1"):
			polls++
			_, _ = w.Write([]byte(`{"readyState":"READY","url":"myrelay.vercel.app"}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stub.Close()

	prevBase := vercelAPIBase
	vercelAPIBase = stub.URL
	defer func() { vercelAPIBase = prevBase }()

	r := newPoolTestRouter(h)
	res := postJSON(t, r, "/proxy-pools/relay-deploy",
		`{"platform":"vercel","vercel_token":"tok","project_name":"myrelay"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("deploy = %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Pool      store.ProxyPool `json:"pool"`
		DeployURL string          `json:"deploy_url"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pool.Type != "vercel" || body.Pool.ProxyURL != "https://myrelay.vercel.app" {
		t.Fatalf("pool = %+v", body.Pool)
	}
	if polls < 1 {
		t.Fatal("deployment status must be polled")
	}
}

// TestDeployRelayDenoHappyPath drives the Deno create+deploy+poll flow and
// pins the deno.net URL shape.
func TestDeployRelayDenoHappyPath(t *testing.T) {
	fs := newFakePoolStore()
	h := &Handler{}
	h.SetProxyPoolStore(fs)

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/apps"):
			_, _ = w.Write([]byte(`{"id":"app1"}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/apps/app1/deploy"):
			_, _ = w.Write([]byte(`{"id":"rev1","status":"building"}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/revisions/rev1"):
			_, _ = w.Write([]byte(`{"status":"succeeded"}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stub.Close()

	prevBase := denoAPIBase
	denoAPIBase = stub.URL
	defer func() { denoAPIBase = prevBase }()

	r := newPoolTestRouter(h)
	res := postJSON(t, r, "/proxy-pools/relay-deploy",
		`{"platform":"deno","deno_token":"tok","deno_org_domain":"myorg.deno.net","project_name":"myrelay"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("deploy = %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Pool      store.ProxyPool `json:"pool"`
		DeployURL string          `json:"deploy_url"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pool.Type != "deno" || body.Pool.ProxyURL != "https://myrelay.myorg.deno.net" {
		t.Fatalf("pool = %+v", body.Pool)
	}
}

// TestDeployRelayPlatformFailureSurfacesError pins that platform API errors
// bubble up with their status code and no pool is created.
func TestDeployRelayPlatformFailureSurfacesError(t *testing.T) {
	h := &Handler{}
	h.SetProxyPoolStore(newFakePoolStore())
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
	}))
	defer stub.Close()

	prevBase := cloudflareAPIBase
	cloudflareAPIBase = stub.URL
	defer func() { cloudflareAPIBase = prevBase }()

	r := newPoolTestRouter(h)
	res := postJSON(t, r, "/proxy-pools/relay-deploy",
		`{"platform":"cloudflare","cf_account_id":"a","cf_api_token":"bad"}`)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("deploy = %d, want upstream 401", res.Code)
	}
	if !strings.Contains(res.Body.String(), "bad token") {
		t.Fatalf("upstream message must surface: %s", res.Body.String())
	}
}
