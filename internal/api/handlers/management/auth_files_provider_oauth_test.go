package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

// zcodeFixBaseTransport rewrites the fixed Z.ai CLI base origin to a fake test
// server so the concrete *zcode.ZaiCliLogin can be exercised without the network.
type zcodeFixBaseTransport struct {
	host string // fake server host, e.g. "127.0.0.1:PORT"
}

func (t *zcodeFixBaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	u := *r2.URL
	u.Scheme = "http"
	u.Host = t.host
	r2.URL = &u
	return http.DefaultTransport.RoundTrip(r2)
}

// newZCodeLoginForTest returns a *zcode.ZaiCliLogin wired to the fake server
// with immediate polling (no real sleep).
func newZCodeLoginForTest(fakeHost string) *zcode.ZaiCliLogin {
	return &zcode.ZaiCliLogin{
		HTTP:  &http.Client{Transport: &zcodeFixBaseTransport{host: fakeHost}},
		Sleep: func(time.Duration) {},
	}
}

// newZCodeFakeServer returns a fake Z.ai server serving the CLI login and
// credential resolver endpoints needed to complete a full ZCode login.
func newZCodeFakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	expiresAt := time.Now().Add(time.Hour).Unix()
	mux := http.NewServeMux()

	resp := func(w http.ResponseWriter, payload string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}

	// Z.ai CLI OAuth init.
	mux.HandleFunc("/api/v1/oauth/cli/init", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp(w, fmt.Sprintf(`{"code":0,"data":{"flow_id":"flow-1","authorize_url":"https://auth.example.test/zcode","poll_interval_sec":1,"expires_at":%d},"msg":""}`, expiresAt))
	})
	// Z.ai CLI OAuth poll.
	mux.HandleFunc("/api/v1/oauth/cli/poll/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resp(w, `{"code":0,"data":{"status":"ready","token":"jwt-token","user":{"user_id":"user-1"},"zai":{"access_token":"access-token"}},"msg":""}`)
	})
	// Resolver: exchange OAuth token for a biz access token.
	mux.HandleFunc("/api/auth/z/login", func(w http.ResponseWriter, r *http.Request) {
		resp(w, `{"data":{"access_token":"biz-token"},"msg":""}`)
	})
	// Resolver: customer info with a default org/project.
	mux.HandleFunc("/api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		resp(w, `{"data":{"organizations":[{"organizationId":"org-1","organizationName":"默认机构","projects":[{"projectId":"proj-1","projectName":"默认项目"}]}]},"msg":""}`)
	})
	// Resolver: list API keys (none present -> creation path taken).
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys/copy/", func(w http.ResponseWriter, r *http.Request) {
		resp(w, `{"data":{"secretKey":"secret-zd"},"msg":""}`)
	})
	// Resolver: create API key.
	mux.HandleFunc("/api/biz/v1/organization/org-1/projects/proj-1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			resp(w, `{"data":[]}`)
			return
		}
		resp(w, `{"data":{"apiKey":"api-key-zd"},"msg":""}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestRequestZCodeToken_ReturnsAuthURLAndSavesAuthFile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fake := newZCodeFakeServer(t)
	fakeHost := strings.TrimPrefix(fake.URL, "http://")

	origLogin := newZCodeLogin
	origResolver := newZCodeResolver
	newZCodeLogin = func() *zcode.ZaiCliLogin { return newZCodeLoginForTest(fakeHost) }
	newZCodeResolver = func() *zcode.Resolver { return &zcode.Resolver{TestHost: fake.URL} }
	defer func() {
		newZCodeLogin = origLogin
		newZCodeResolver = origResolver
	}()

	authDir := filepath.Join(t.TempDir(), "auths")
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	h.tokenStore = sdkAuth.NewFileTokenStore()

	router := gin.New()
	router.GET("/zcode-auth-url", h.RequestZCodeToken)

	req := httptest.NewRequest(http.MethodGet, "/zcode-auth-url", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	var payload struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if errDecode := json.Unmarshal(w.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode zcode auth URL response: %v", errDecode)
	}
	if payload.URL == "" {
		t.Fatalf("expected authorize_url in response, body=%s", w.Body.String())
	}
	if payload.State == "" {
		t.Fatalf("expected state in response, body=%s", w.Body.String())
	}

	// Wait for the background poll+resolve to save an auth file.
	deadline := time.Now().Add(5 * time.Second)
	var found string
	for time.Now().Before(deadline) {
		matches, errGlob := filepath.Glob(filepath.Join(authDir, "zcode-*.json"))
		if errGlob == nil && len(matches) > 0 {
			found = matches[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if found == "" {
		t.Fatal("timed out waiting for zcode auth file to be saved")
	}

	raw, errRead := os.ReadFile(found)
	if errRead != nil {
		t.Fatalf("read saved zcode auth file: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(raw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal saved zcode auth file: %v", errUnmarshal)
	}
	if saved["type"] != "zcode" {
		t.Errorf("type = %v, want zcode", saved["type"])
	}
	for _, key := range []string{"api_key", "secret", "jwt", "user_id", "device_mid"} {
		if val, ok := saved[key]; !ok || val == "" {
			t.Errorf("saved[%q] = %v, want non-empty", key, val)
		}
	}
	if saved["api_key"] != "api-key-zd" || saved["secret"] != "secret-zd" {
		t.Errorf("saved api_key/secret = %v/%v, want api-key-zd/secret-zd", saved["api_key"], saved["secret"])
	}
}
