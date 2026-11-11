package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexreset"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexResetPolicyConsentAndPendingJournal(t *testing.T) {
	if errConfigure := kvstore.ConfigureLocal(true, filepath.Join(t.TempDir(), "kv.db")); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	manager := coreauth.NewManager(nil, nil, nil)
	claims := `{"sub":"user","https://api.openai.com/auth":{"chatgpt_account_id":"workspace"}}`
	auth := &coreauth.Auth{ID: "codex", Provider: "codex", Metadata: map[string]any{
		"account_id": "workspace", "id_token": "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig",
	}}
	index := auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	request := func(method, body string) int {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(method, "/v8/management/routing/codex-reset-policy?auth_index="+index, strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.CodexResetPolicy(c)
		return recorder.Code
	}
	if code := request(http.MethodGet, ""); code != 200 {
		t.Fatalf("read: %d", code)
	}
	if code := request(http.MethodPut, `{"account_key":"stale","enabled":true,"before_expiry_seconds":3600}`); code != 409 {
		t.Fatalf("stale identity: %d", code)
	}
	if code := request(http.MethodPut, `{"enabled":true,"before_expiry_seconds":1}`); code != 400 {
		t.Fatalf("bad lead time: %d", code)
	}
	key := codexreset.AccountKey(auth)
	store, _, _ := kvstore.CurrentKVClient()
	state, raw, exists, _ := codexreset.Read(context.Background(), store, key)
	state.Pending, state.RequestID, state.CreditID = true, "original-request", "selected-credit"
	if saved, errSave := codexreset.Save(context.Background(), store, state, raw, exists); !saved || errSave != nil {
		t.Fatal("seed journal")
	}
	if code := request(http.MethodPut, `{"account_key":"`+key+`","enabled":false,"before_expiry_seconds":3600,"pending":false,"request_id":"overwrite"}`); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	state, _, _, _ = codexreset.Read(context.Background(), store, key)
	if state.Enabled || !state.Pending || state.RequestID != "original-request" || state.CreditID != "selected-credit" {
		t.Fatalf("journal changed: %+v", state)
	}
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	if code := request(http.MethodGet, ""); code != 503 {
		t.Fatalf("volatile fallback: %d", code)
	}
}
