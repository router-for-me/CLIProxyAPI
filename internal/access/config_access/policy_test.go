package configaccess

import (
	"bytes"
	"context"
	"github.com/klauspost/compress/zstd"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"gopkg.in/yaml.v3"
)

func TestClientKeyModelPolicies(t *testing.T) {
	var cfg sdkconfig.Config
	if err := yaml.Unmarshal([]byte(`access:
  api-keys: [restricted, legacy]
  api-key-policies:
    - api-key: restricted
      name: test-client
      allowed-models: ["claude/*", "paid-*"]
      blocked-models: ["paid-*"]
`), &cfg); err != nil {
		t.Fatal(err)
	}
	Register(&cfg.SDKConfig)
	t.Cleanup(func() { Register(nil) })
	p := sdkaccess.RegisteredProviders()[0]
	tests := []struct {
		name, method, path, body, key string
		denied                        bool
	}{
		{"allowed prefix", "POST", "/v1/messages", `{"model":"claude/sonnet"}`, "restricted", false},
		{"denied model", "POST", "/v1/messages", `{"model":"codex/gpt"}`, "restricted", true},
		{"block overrides allow", "POST", "/v1/messages", `{"model":"paid-sonnet"}`, "restricted", true},
		{"legacy unchanged", "POST", "/v1/messages", `{"model":"paid-sonnet"}`, "legacy", false},
		{"Gemini path", "POST", "/v1beta/models/paid-sonnet:generateContent", `{}`, "restricted", true},
		{"websocket turn", "POST", "/v1/responses", `{"type":"response.create","model":"paid-sonnet"}`, "restricted", true},
		{"websocket upgrade", "GET", "/v1/responses", "", "restricted", false},
		{"missing model fails closed", "POST", "/v1/messages", `{}`, "restricted", true},
		{"model catalog accessible", "GET", "/v1/models", "", "restricted", false},
		{"realtime nested model", "POST", "/v1/realtime/client_secrets", `{"session":{"model":"paid-sonnet"}}`, "restricted", true},
		{"duplicate models fail closed", "POST", "/v1/messages", `{"model":"claude/sonnet","model":"paid-sonnet"}`, "restricted", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			r.Header.Set("Authorization", "Bearer "+tt.key)
			result, err := p.Authenticate(context.Background(), r)
			if tt.denied {
				if err == nil || err.HTTPStatusCode() != http.StatusForbidden {
					t.Fatalf("want 403, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.key == "restricted" && result.Metadata["name"] != "test-client" {
				t.Fatal("client name missing")
			}
			restored, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if string(restored) != tt.body {
				t.Fatal("request body changed")
			}
		})
	}
}

func TestPolicyRejectsAmbiguousSelectors(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "key", AllowedModels: []string{"allowed"}, BlockedModels: []string{"secret"}}}}
	p := newConfiguredProvider("test", []string{"key"}, cfg)
	for _, tt := range []struct{ path, body string }{
		{"/v1/images/generations", `{"Model":"allowed"}`},
		{"/v1/images/generations", `{"agent":"allowed"}`},
		{"/v1/realtime/client_secrets", `{"model":"allowed","session":{}}`},
		{"/v1/realtime/sessions", `{"session":{"model":"allowed"},"model":"secret"}`},
		{"/v1beta/interactions", `{"model":"models/secret"}`},
		{"/v1/realtime/client_secrets", `{"session":{"model":"secret"},"model":"allowed"}`},
	} {
		r := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
		r.Header.Set("X-Api-Key", "key")
		if _, err := p.Authenticate(context.Background(), r); err == nil || err.HTTPStatusCode() != 403 {
			t.Errorf("path %s: expected 403", tt.path)
		}
	}
}

func TestPolicyMultipartPreservesUploadAndSessionPrecedence(t *testing.T) {
	for _, tt := range []struct {
		path, session string
		denied        bool
	}{
		{"/v1/images/edits", "", false},
		{"/v1/realtime/calls", `{"model":"secret"}`, true},
		{"/v1/realtime/calls", `{}`, true},
	} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if tt.session != "" {
			if err := writer.WriteField("session", tt.session); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.WriteField("model", "allowed"); err != nil {
			t.Fatal(err)
		}
		part, err := writer.CreateFormFile("image", "image.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte("image-bytes")); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		cfg := &sdkconfig.SDKConfig{APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "key", AllowedModels: []string{"allowed"}}}}
		p := newConfiguredProvider("test", []string{"key"}, cfg)
		r := httptest.NewRequest("POST", tt.path, bytes.NewReader(body.Bytes()))
		r.Header.Set("X-Api-Key", "key")
		r.Header.Set("Content-Type", writer.FormDataContentType())
		_, authErr := p.Authenticate(context.Background(), r)
		if tt.denied && (authErr == nil || authErr.HTTPStatusCode() != 403) {
			t.Error("nested session model bypassed policy")
		}
		if !tt.denied && authErr != nil {
			t.Fatal(authErr)
		}
		restored, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(restored, body.Bytes()) {
			t.Error("upload body changed")
		}
		upload, ok := r.Body.(*policyUploadBody)
		if !ok {
			t.Fatal("upload not spooled")
		}
		name := upload.File.Name()
		if err = r.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(name); !os.IsNotExist(err) {
			t.Fatal("upload spool not removed")
		}
	}
}

func TestPolicyZstdBody(t *testing.T) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	cfg := &sdkconfig.SDKConfig{APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "key", AllowedModels: []string{"allowed"}}}}
	p := newConfiguredProvider("test", []string{"key"}, cfg)
	for _, model := range []string{"allowed", "blocked"} {
		raw := encoder.EncodeAll([]byte(`{"model":"`+model+`"}`), nil)
		r := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(raw))
		r.Header.Set("X-Api-Key", "key")
		r.Header.Set("Content-Encoding", "zstd")
		_, authErr := p.Authenticate(context.Background(), r)
		if (authErr != nil) != (model == "blocked") {
			t.Errorf("model %s: unexpected auth result", model)
		}
		restored, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if !bytes.Equal(restored, raw) {
			t.Fatal("compressed body changed")
		}
	}
}

func TestPolicyEndpointDefaultsAndFormModels(t *testing.T) {
	for _, tt := range []struct{ path, body, contentType, model string }{
		{"/v1/images/generations", `{}`, "application/json", "gpt-image-2"},
		{"/v1/videos/generations", `{}`, "application/json", "grok-imagine-video"},
		{"/openai/v1/videos", `{}`, "application/json", "sora-2"},
		{"/openai/v1/videos", "model=allowed", "application/x-www-form-urlencoded", "allowed"},
		{"/v1/realtime/client_secrets", `{"session":{}}`, "application/json", "gpt-realtime"},
		{"/v1beta/interactions", `{"model":"models/allowed"}`, "application/json", "allowed"},
	} {
		cfg := &sdkconfig.SDKConfig{APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "key", AllowedModels: []string{tt.model}}}}
		p := newConfiguredProvider("test", []string{"key"}, cfg)
		r := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
		r.Header.Set("X-Api-Key", "key")
		r.Header.Set("Content-Type", tt.contentType)
		if _, err := p.Authenticate(context.Background(), r); err != nil {
			t.Errorf("%s: %v", tt.path, err)
		}
	}
}

func TestPolicyAutoAndBodyLimit(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "key", BlockedModels: []string{"paid-*"}}}}
	p := newConfiguredProvider("test", []string{"key"}, cfg)
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"auto"}`))
	r.Header.Set("X-Api-Key", "key")
	if _, err := p.Authenticate(context.Background(), r); err == nil || err.HTTPStatusCode() != 403 {
		t.Fatal("dynamic model must not bypass policy")
	}
	r = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"allowed"}`))
	r.Header.Set("X-Api-Key", "key")
	r.Body = http.MaxBytesReader(nil, r.Body, 8)
	if _, err := p.Authenticate(context.Background(), r); err == nil || err.HTTPStatusCode() != 413 {
		t.Fatal("body limit must preserve 413")
	}
}

func TestPolicySnapshotsAreIndependent(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "key", BlockedModels: []string{"paid-*"}}}}
	previous := newConfiguredProvider("test", []string{"key"}, cfg)
	cfg.APIKeyPolicies[0].BlockedModels[0] = "other-*"
	updated := newConfiguredProvider("test", []string{"key"}, cfg)
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"paid-model"}`))
		r.Header.Set("X-Api-Key", "key")
		return r
	}
	if _, err := previous.Authenticate(context.Background(), request()); err == nil {
		t.Fatal("existing policy snapshot changed")
	}
	if _, err := updated.Authenticate(context.Background(), request()); err != nil {
		t.Fatal("new snapshot did not apply changed policy")
	}
}
