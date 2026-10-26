package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A remote browser cannot reach the server's localhost listener, so the pasted
// redirect URL must complete the login through the shared callback endpoint.
func TestKiroSocialLoginCompletesFromSubmittedCallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/oauth/token" || body["code"] != "pasted-code" {
			t.Errorf("unexpected token request %s %v", r.URL.Path, body)
		}
		_, _ = io.WriteString(w, `{"accessToken":"access","refreshToken":"refresh","expiresIn":3600}`)
	}))
	defer upstream.Close()
	original := kiroAuthURL
	kiroAuthURL = upstream.URL
	t.Cleanup(func() { kiroAuthURL = original })

	dir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, nil)
	saved := make(chan *coreauth.Auth, 1)
	h.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error { saved <- auth; return nil })
	router := gin.New()
	router.GET("/oauth/auth-url", h.StartOAuthV8)
	router.POST("/oauth/callback", h.PostOAuthCallback)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/oauth/auth-url?provider=kiro&method=google", nil))
	if w.Code != http.StatusOK {
		t.Skipf("social login could not start (callback port in use?): %d %s", w.Code, w.Body.String())
	}
	var start struct{ URL, State, Flow string }
	if errDecode := json.Unmarshal(w.Body.Bytes(), &start); errDecode != nil {
		t.Fatal(errDecode)
	}
	t.Cleanup(func() { CancelOAuthSession(start.State) })
	if start.Flow != "social" || !strings.Contains(start.URL, "idp=Google") {
		t.Fatalf("start response: %s", w.Body.String())
	}

	body := `{"provider":"kiro","redirect_url":"http://localhost:3128/oauth/callback?login_option=google&code=pasted-code&state=` + start.State + `"}`
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/oauth/callback", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	var auth *coreauth.Auth
	select {
	case auth = <-saved:
	case <-time.After(5 * time.Second):
		t.Fatal("credential was not saved")
	}
	data, errRead := os.ReadFile(filepath.Join(dir, auth.FileName))
	if errRead != nil {
		t.Fatal(errRead)
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(data, &stored); errDecode != nil {
		t.Fatal(errDecode)
	}
	if stored["type"] != "kiro" || stored["provider"] != "Google" || stored["auth_method"] != "social" || stored["refresh_token"] != "refresh" {
		t.Fatalf("stored credential: %v", stored)
	}
}
