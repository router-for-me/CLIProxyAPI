package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// stubJevStore is the minimal surface the handler needs, so the settings
// handlers can be tested without a database.
type stubJevStore struct {
	set    store.JevSettings
	seen   *string
	lastAt time.Time
}

func (s *stubJevStore) Get(_ context.Context) (store.JevSettings, error) {
	return s.set, nil
}

func (s *stubJevStore) Upsert(_ context.Context, set store.JevSettings, apiKey *string) (store.JevSettings, error) {
	s.set.Enabled = set.Enabled
	s.set.Model = set.Model
	s.seen = apiKey
	if apiKey != nil {
		if *apiKey == "" {
			s.set.APIKeySet = false
			s.set.APIKeyPrefix = ""
		} else {
			key := *apiKey
			s.set.APIKeySet = true
			if len(key) > 4 {
				s.set.APIKeyPrefix = "sk-ts-…" + key[len(key)-4:]
			}
		}
	}
	s.lastAt = time.Now()
	return s.set, nil
}

// newJevTestContext builds a gin context with the given JSON body.
func newJevTestContext(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func TestPutJevSettingsNeverEchoesKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubJevStore{}
	h := &Handler{pgJev: stub}

	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings",
		`{"enabled":true,"api_key":"sk-ts-secretvalue1234","model":"jev-1.13.0"}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("secretvalue")) {
		t.Fatalf("response leaked the API key: %s", rec.Body.String())
	}
	var got struct {
		Settings store.JevSettings `json:"settings"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &got); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if !got.Settings.Enabled {
		t.Error("enabled did not round-trip")
	}
	if !got.Settings.APIKeySet {
		t.Error("api_key_set must be true after supplying a key")
	}
	if got.Settings.Model != "jev-1.13.0" {
		t.Errorf("model = %q", got.Settings.Model)
	}
	// The store must receive the raw key so it can seal it.
	if stub.seen == nil || *stub.seen != "sk-ts-secretvalue1234" {
		t.Errorf("store did not receive the raw key: %v", stub.seen)
	}
}

func TestPutJevSettingsPreservesKeyWhenOmitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubJevStore{set: store.JevSettings{Enabled: true, APIKeySet: true, APIKeyPrefix: "sk-ts-…1234", Model: "jev-1.13.0"}}
	h := &Handler{pgJev: stub}

	// Toggling off must pass a nil key so the credential survives.
	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"enabled":false}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.seen != nil {
		t.Errorf("omitted api_key must pass nil, got %v", *stub.seen)
	}
	if !stub.set.APIKeySet {
		t.Error("the stored key must survive a toggle without api_key")
	}
	if stub.set.Enabled {
		t.Error("enabled must be updated")
	}
}

func TestPutJevSettingsEmptyKeyClears(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubJevStore{set: store.JevSettings{APIKeySet: true, APIKeyPrefix: "sk-ts-…1234", Model: "jev-1.13.0"}}
	h := &Handler{pgJev: stub}

	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"api_key":""}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.seen == nil || *stub.seen != "" {
		t.Errorf("empty api_key must reach the store as an explicit clear")
	}
	if stub.set.APIKeySet {
		t.Error("api_key_set must be false after a clear")
	}
	if stub.set.APIKeyPrefix != "" {
		t.Errorf("prefix must be cleared, got %q", stub.set.APIKeyPrefix)
	}
}

func TestGetJevSettingsReturnsMaskedPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubJevStore{set: store.JevSettings{
		Enabled: true, APIKeySet: true, APIKeyPrefix: "sk-ts-…1234", Model: "jev-1.13.0",
	}}
	h := &Handler{pgJev: stub}

	c, rec := newJevTestContext(http.MethodGet, "/v0/management/jev/settings", "")
	h.GetJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Settings struct {
			APIKeySet    bool   `json:"api_key_set"`
			APIKeyPrefix string `json:"api_key_prefix"`
			Enabled      bool   `json:"enabled"`
			Model        string `json:"model"`
		} `json:"settings"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &got); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if !got.Settings.Enabled || !got.Settings.APIKeySet {
		t.Errorf("settings = %+v", got.Settings)
	}
	if got.Settings.APIKeyPrefix != "sk-ts-…1234" {
		t.Errorf("prefix = %q", got.Settings.APIKeyPrefix)
	}
}

func TestJevSettingsRoutes503WithoutPG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}

	for name, call := range map[string]func(*gin.Context){
		"get": h.GetJevSettings,
		"put": h.PutJevSettings,
	} {
		c, rec := newJevTestContext(http.MethodGet, "/v0/management/jev/settings", `{"enabled":true}`)
		call(c)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", name, rec.Code)
		}
	}
}

func TestPutJevSettingsRejectsMalformedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{pgJev: &stubJevStore{}}

	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"enabled":"not-a-bool"}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body = %s)", rec.Code, rec.Body.String())
	}
}

// TestStubJevStoreMatchesStoreInterface guards against the stub drifting from
// the handler's expected surface.
func TestStubJevStoreMatchesStoreInterface(t *testing.T) {
	var _ jevSettingsStore = (*stubJevStore)(nil)
}
