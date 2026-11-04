package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type siwcExchangeInput struct {
	code, clientID, redirect, deviceID string
	pkce                               *codex.PKCECodes
}

type testSIWCOAuth struct {
	calls   atomic.Int32
	inputs  chan siwcExchangeInput
	release chan struct{}
	err     error
}

func (s *testSIWCOAuth) GenerateSIWCAuthURL(state, nonce, deviceID, redirect string, pkce *codex.PKCECodes) (string, error) {
	return codex.NewCodexAuth(nil).GenerateSIWCAuthURL(state, nonce, deviceID, redirect, pkce)
}

func (s *testSIWCOAuth) ExchangeSIWCCodeForTokens(ctx context.Context, code, clientID, redirect, deviceID string, pkce *codex.PKCECodes) (*codex.CodexSIWCTokenData, error) {
	s.calls.Add(1)
	s.inputs <- siwcExchangeInput{code, clientID, redirect, deviceID, pkce}
	if s.release != nil {
		// Deliberately ignore cancellation to prove the commit gate rejects a
		// successful but late upstream result after cancellation or expiration.
		<-s.release
	}
	if s.err != nil {
		return nil, s.err
	}
	return &codex.CodexSIWCTokenData{AccessToken: "new-siwc", RefreshToken: "new-siwc-refresh", ClientID: clientID, DeviceID: deviceID, Expire: "2035-01-01T00:00:00Z", ExpiresAt: "2035-01-01T00:00:00Z"}, nil
}

type siwcFixture struct {
	h       *Handler
	router  *gin.Engine
	manager *coreauth.Manager
	auth    *coreauth.Auth
	path    string
	service *testSIWCOAuth
	store   *oauthSessionStore
	unblock func()
}

func setupSIWCFlow(t *testing.T, blocked bool) *siwcFixture {
	t.Helper()
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	metadata := map[string]any{"type": "codex", "access_token": "outer", "refresh_token": "outer-refresh", "account_id": "account", "note": "keep", "siwc": map[string]any{"device_id": "e61bbe28-07ef-466d-8e5d-a344f94ab305"}}
	if errWrite := codex.WriteJSONFileAtomic(path, metadata); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg := &config.Config{AuthDir: dir}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "codex.json", FileName: "codex.json", Provider: "codex", Metadata: metadata, Attributes: map[string]string{"path": path}, Status: coreauth.StatusActive})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	service := &testSIWCOAuth{inputs: make(chan siwcExchangeInput, 4)}
	if blocked {
		service.release = make(chan struct{})
	}
	var once sync.Once
	unblock := func() {
		once.Do(func() {
			if service.release != nil {
				close(service.release)
			}
		})
	}
	h := &Handler{cfg: cfg, authManager: manager, siwcOAuthFactory: func(*config.Config) siwcOAuthService { return service }}
	router := gin.New()
	router.POST("/bind", h.StartSIWCBind)
	router.GET("/siwc-status", h.GetSIWCStatus)
	router.POST("/callback", h.PostOAuthCallbackV8)
	router.GET("/callback", h.GetOAuthCallbackV8)
	router.POST("/legacy-callback", h.PostOAuthCallback)
	router.GET("/status", h.GetAuthStatus)
	router.DELETE("/session", h.CancelAuthSession)
	t.Cleanup(func() {
		unblock()
		store.mu.Lock()
		var flows []*siwcBindSession
		for _, session := range store.sessions {
			if session.SIWC != nil {
				flows = append(flows, session.SIWC)
			}
		}
		store.mu.Unlock()
		for _, flow := range flows {
			flow.cancel()
			flow.mu.Lock()
			done := flow.exchangeDone
			flow.mu.Unlock()
			if done != nil {
				<-done
			}
		}
	})
	return &siwcFixture{h, router, manager, auth, path, service, store, unblock}
}

func siwcRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, errJSON := json.Marshal(body)
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func (f *siwcFixture) start(t *testing.T) (string, *siwcBindSession) {
	t.Helper()
	w := siwcRequest(t, f.router, http.MethodPost, "/bind", map[string]any{"name": f.auth.FileName})
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		URL, State, Flow string
		ExpiresIn        int `json:"expires_in"`
	}
	if errJSON := json.Unmarshal(w.Body.Bytes(), &response); errJSON != nil {
		t.Fatal(errJSON)
	}
	if response.Flow != "siwc_bind" || response.ExpiresIn != 300 {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
	u, errURL := url.Parse(response.URL)
	if errURL != nil {
		t.Fatal(errURL)
	}
	q := u.Query()
	if q.Get("client_id") != codex.SIWCDynamicClientID || q.Get("redirect_uri") != codex.SIWCRedirectURI || q.Get("nonce") == "" || q.Get("code_challenge") == "" {
		t.Fatal("missing SIWC authorization parameters")
	}
	session, ok := f.store.Get(response.State)
	if !ok || session.SIWC == nil {
		t.Fatal("missing SIWC session")
	}
	if strings.Contains(w.Body.String(), session.SIWC.pkce.CodeVerifier) {
		t.Fatal("PKCE verifier exposed")
	}
	return response.State, session.SIWC
}

func siwcCallbackURL(state string) string {
	return codex.SIWCRedirectURI + "?" + url.Values{"state": {state}, "code": {"issued-code"}, "client_id": {"oaiapp_issued"}}.Encode()
}

func waitSIWCExchange(t *testing.T, flow *siwcBindSession) {
	t.Helper()
	flow.mu.Lock()
	done := flow.exchangeDone
	flow.mu.Unlock()
	if done == nil {
		t.Fatal("exchange did not start")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("exchange did not finish")
	}
}

func TestSIWCBindCallbackFormsAndSummary(t *testing.T) {
	for _, form := range []string{"redirect", "json", "get"} {
		t.Run(form, func(t *testing.T) {
			f := setupSIWCFlow(t, false)
			state, flow := f.start(t)
			var w *httptest.ResponseRecorder
			switch form {
			case "redirect":
				w = siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"provider": "codex", "redirect_url": siwcCallbackURL(state)})
			case "json":
				w = siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"state": state, "code": "issued-code", "client_id": "oaiapp_issued"})
			case "get":
				w = siwcRequest(t, f.router, http.MethodGet, "/callback?"+url.Values{"state": {state}, "code": {"issued-code"}, "client_id": {"oaiapp_issued"}}.Encode(), nil)
			}
			if w.Code != http.StatusOK {
				t.Fatalf("callback: %d %s", w.Code, w.Body.String())
			}
			waitSIWCExchange(t, flow)
			input := <-f.service.inputs
			if input.clientID != "oaiapp_issued" || input.code != "issued-code" || input.redirect != codex.SIWCRedirectURI || input.deviceID != "e61bbe28-07ef-466d-8e5d-a344f94ab305" || input.pkce == nil {
				t.Fatal("exchange lost server context or dynamic client_id")
			}
			if status := performOAuthStatusRequest(t, f.router, state); status.Status != "ok" {
				t.Fatalf("status: %#v", status)
			}
			got, _ := f.manager.GetByID(f.auth.ID)
			if got.Metadata["access_token"] != "outer" || got.Metadata["note"] != "keep" || got.Metadata["use_siwc"] != true {
				t.Fatal("binding corrupted outer credential")
			}
			entry := f.h.buildAuthFileEntry(got, authFileQuotaContext{v8: true})
			if entry["siwc_bound"] != true || entry["use_siwc"] != true || entry["siwc_status"] != "valid" || entry["siwc_bind_supported"] != true || entry["siwc_expires_at"] != "2035-01-01T00:00:00Z" {
				t.Fatalf("missing summary: %#v", entry)
			}
			encoded, _ := json.Marshal(entry)
			if strings.Contains(string(encoded), "new-siwc") || strings.Contains(string(encoded), "oaiapp_issued") {
				t.Fatal("credential secrets exposed in summary")
			}
			legacy := f.h.buildAuthFileEntry(got, authFileQuotaContext{})
			if _, exists := legacy["siwc_bound"]; exists {
				t.Fatal("v0 response changed")
			}
			if CancelOAuthSession(state) {
				t.Fatal("completed binding cancelled")
			}
		})
	}
}

func TestSIWCBindRejectsInvalidCallbacksWithoutExchange(t *testing.T) {
	f := setupSIWCFlow(t, false)
	state, _ := f.start(t)
	cases := []map[string]any{
		{"state": state, "code": "code"},
		{"state": state, "client_id": "client"},
		{"state": state, "code": "code", "client_id": "client", "provider": "claude"},
		{"state": state, "redirect_url": siwcCallbackURL("other-state")},
		{"state": state, "redirect_url": strings.Replace(siwcCallbackURL(state), "127.0.0.1", "evil.example", 1)},
		{"redirect_url": siwcCallbackURL(state) + "&client_id=other"},
		{"state": state, "client_id": "other", "redirect_url": siwcCallbackURL(state)},
	}
	for _, body := range cases {
		w := siwcRequest(t, f.router, http.MethodPost, "/callback", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid callback: %d %s", w.Code, w.Body.String())
		}
	}
	w := siwcRequest(t, f.router, http.MethodGet, "/callback?state="+state+"&code=code&client_id=first&client_id=second", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatal("duplicate GET callback parameters accepted")
	}
	if f.service.calls.Load() != 0 || !f.store.IsPending(state, "codex") {
		t.Fatal("invalid callback consumed pending session")
	}
}

func TestSIWCBindDuplicateAndCancellation(t *testing.T) {
	f := setupSIWCFlow(t, true)
	state, flow := f.start(t)
	duplicate := siwcRequest(t, f.router, http.MethodPost, "/bind", map[string]any{"name": f.auth.FileName})
	if duplicate.Code != http.StatusConflict {
		t.Fatal("parallel binding accepted for same target")
	}
	body := map[string]any{"redirect_url": siwcCallbackURL(state)}
	if w := siwcRequest(t, f.router, http.MethodPost, "/callback", body); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	<-f.service.inputs
	if status := performOAuthStatusRequest(t, f.router, state); status.Status != "wait" {
		t.Fatal("callback acceptance reported success before persistence")
	}
	if w := siwcRequest(t, f.router, http.MethodPost, "/callback", body); w.Code != http.StatusConflict {
		t.Fatal("duplicate callback accepted")
	}
	if !CancelOAuthSession(state) {
		t.Fatal("inflight binding could not be cancelled")
	}
	f.unblock()
	waitSIWCExchange(t, flow)
	got, _ := f.manager.GetByID(f.auth.ID)
	if got.Metadata["use_siwc"] == true || f.service.calls.Load() != 1 {
		t.Fatal("cancelled or duplicate exchange committed")
	}
	if w := siwcRequest(t, f.router, http.MethodPost, "/callback", body); w.Code != http.StatusNotFound {
		t.Fatal("cancelled callback accepted")
	}
}

func TestSIWCBindRejectsLateResults(t *testing.T) {
	for _, change := range []string{"expire", "delete", "reimport", "exchange-error"} {
		t.Run(change, func(t *testing.T) {
			f := setupSIWCFlow(t, true)
			if change == "exchange-error" {
				f.service.err = errors.New("upstream contains secret-token")
			}
			state, flow := f.start(t)
			w := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"redirect_url": siwcCallbackURL(state)})
			if w.Code != http.StatusOK {
				t.Fatal(w.Body.String())
			}
			<-f.service.inputs
			switch change {
			case "expire":
				f.store.mu.Lock()
				session := f.store.sessions[state]
				session.ExpiresAt = time.Now().Add(-time.Hour)
				f.store.sessions[state] = session
				f.store.mu.Unlock()
			case "delete":
				if errRemove := os.Remove(f.path); errRemove != nil {
					t.Fatal(errRemove)
				}
			case "reimport":
				f.manager.Remove(context.Background(), f.auth.ID)
				if _, errRegister := f.manager.Register(context.Background(), f.auth.Clone()); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			f.unblock()
			waitSIWCExchange(t, flow)
			got, _ := f.manager.GetByID(f.auth.ID)
			if got.Metadata["use_siwc"] == true {
				t.Fatal("late binding committed")
			}
			status := performOAuthStatusRequest(t, f.router, state)
			if status.Status != "error" || strings.Contains(status.Error, "secret-token") {
				t.Fatalf("unsafe status: %#v", status)
			}
		})
	}
}

func TestSIWCBindCommitSurvivesSessionExpiry(t *testing.T) {
	f := setupSIWCFlow(t, false)
	state, flow := f.start(t)
	if !f.store.claimSIWCCommit(state, flow) {
		t.Fatal("commit could not be claimed")
	}
	f.store.mu.Lock()
	session := f.store.sessions[state]
	session.ExpiresAt = time.Now().Add(-time.Hour)
	f.store.sessions[state] = session
	f.store.mu.Unlock()
	if _, ok := f.store.Get(state); !ok {
		t.Fatal("in-progress commit expired before reporting its outcome")
	}
	if f.store.CompleteProvider("codex", oauthSessionSourceBuiltin) != 0 {
		t.Fatal("ordinary provider completion consumed SIWC binding")
	}
	f.store.Complete(state)
	if status := performOAuthStatusRequest(t, f.router, state); status.Status != "ok" {
		t.Fatalf("committed session lost outcome: %#v", status)
	}
	if session, ok := f.store.Get(state); !ok || session.SIWCCommitting {
		t.Fatal("commit claim was not released")
	}
}

func TestSIWCBindOAuthErrorAndEligibility(t *testing.T) {
	f := setupSIWCFlow(t, false)
	for _, kind := range []string{"config", "runtime", "api-key", "missing-outer", "other-provider"} {
		candidate := f.auth.Clone()
		switch kind {
		case "config":
			candidate.Attributes["source_backend"] = "config"
		case "runtime":
			candidate.Attributes["runtime_only"] = "true"
		case "api-key":
			candidate.Attributes["api_key"] = "key"
		case "missing-outer":
			delete(candidate.Metadata, "refresh_token")
		case "other-provider":
			candidate.Provider = "claude"
		}
		if siwcBindEligible(candidate) {
			t.Fatalf("%s unexpectedly eligible", kind)
		}
	}
	state, _ := f.start(t)
	w := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"state": state, "error": "access_denied: secret-data"})
	if w.Code != http.StatusOK || f.service.calls.Load() != 0 {
		t.Fatalf("error callback triggered exchange: %d", w.Code)
	}
	if status := performOAuthStatusRequest(t, f.router, state); status.Status != "error" || strings.Contains(status.Error, "secret-data") {
		t.Fatalf("unsafe OAuth error: %#v", status)
	}
}

func TestSIWCBindRejectsLegacyCallback(t *testing.T) {
	f := setupSIWCFlow(t, false)
	state, flow := f.start(t)
	w := siwcRequest(t, f.router, http.MethodPost, "/legacy-callback", map[string]any{"state": state, "code": "issued-code"})
	if w.Code != http.StatusConflict {
		t.Fatalf("legacy callback: %d %s", w.Code, w.Body.String())
	}
	path := filepath.Join(filepath.Dir(f.path), ".oauth-codex-"+state+".oauth")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy callback created a file: %v", err)
	}
	if !f.store.IsPending(state, "codex") || f.service.calls.Load() != 0 {
		t.Fatal("legacy callback consumed the SIWC session")
	}
	w = siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"redirect_url": siwcCallbackURL(state)})
	if w.Code != http.StatusOK {
		t.Fatalf("v8 retry: %d %s", w.Code, w.Body.String())
	}
	waitSIWCExchange(t, flow)
}

func TestSIWCBindRejectsCredentialSymlinkAtStart(t *testing.T) {
	f := setupSIWCFlow(t, false)
	target := filepath.Join(filepath.Dir(f.path), "target.json")
	if err := os.Rename(f.path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w := siwcRequest(t, f.router, http.MethodPost, "/bind", map[string]any{"name": f.auth.FileName})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("symlink accepted: %d %s", w.Code, w.Body.String())
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if len(f.store.sessions) != 0 || f.service.calls.Load() != 0 {
		t.Fatal("symlink rejection created an OAuth session or exchanged tokens")
	}
}

func TestSIWCBindTargetValidationAndLegacyCallback(t *testing.T) {
	f := setupSIWCFlow(t, false)
	for _, name := range []string{"", "../codex.json", "missing.json", f.path} {
		w := siwcRequest(t, f.router, http.MethodPost, "/bind", map[string]any{"name": name})
		if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Fatalf("invalid name accepted: %q %d", name, w.Code)
		}
	}
	RegisterOAuthSession("ordinary", "codex")
	w := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"state": "ordinary", "code": "ordinary-code"})
	if w.Code != http.StatusOK {
		t.Fatalf("ordinary v8 callback regressed: %s", w.Body.String())
	}
	data, errRead := os.ReadFile(filepath.Join(filepath.Dir(f.path), ".oauth-codex-ordinary.oauth"))
	if errRead != nil || !strings.Contains(string(data), "ordinary-code") {
		t.Fatal("ordinary callback file missing")
	}
}

func TestSIWCBindRelativeAuthDirCommit(t *testing.T) {
	t.Chdir(t.TempDir())
	if errMkdir := os.Mkdir("auths", 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	f := setupSIWCFlow(t, false)
	relPath := filepath.Join("auths", "codex.json")
	raw, errRead := os.ReadFile(f.path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errWrite := os.WriteFile(relPath, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	f.h.cfg.AuthDir = "./auths"
	f.manager.SetConfig(f.h.cfg)
	f.auth.Attributes["path"] = relPath
	f.auth.Attributes["source"] = relPath
	if _, errUpdate := f.manager.Update(context.Background(), f.auth); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	state, flow := f.start(t)
	w := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"provider": "codex", "redirect_url": siwcCallbackURL(state)})
	if w.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	waitSIWCExchange(t, flow)
	if status := performOAuthStatusRequest(t, f.router, state); status.Status != "ok" {
		t.Fatalf("status: %#v", status)
	}
}

func TestSIWCBindRejectsReplacedAccount(t *testing.T) {
	for _, mode := range []string{"legacy-upload", "watcher-update"} {
		t.Run(mode, func(t *testing.T) {
			f := setupSIWCFlow(t, false)
			f.router.POST("/legacy", f.h.UploadAuthFile)
			state, flow := f.start(t)
			replacement := map[string]any{
				"type":          "codex",
				"access_token":  "outer-b",
				"refresh_token": "outer-refresh-b",
				"account_id":    "account-b",
			}
			switch mode {
			case "legacy-upload":
				wUpload := siwcRequest(t, f.router, http.MethodPost, "/legacy?name=codex.json", replacement)
				if wUpload.Code != http.StatusOK {
					t.Fatalf("legacy upload: %d %s", wUpload.Code, wUpload.Body.String())
				}
			case "watcher-update":
				if errWrite := codex.WriteJSONFileAtomic(f.path, replacement); errWrite != nil {
					t.Fatal(errWrite)
				}
				replaced := f.auth.Clone()
				replaced.RegistrationEpoch = 0
				replaced.Metadata = replacement
				if _, errUpdate := f.manager.Update(context.Background(), replaced); errUpdate != nil {
					t.Fatal(errUpdate)
				}
			}
			w := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"provider": "codex", "redirect_url": siwcCallbackURL(state)})
			if w.Code != http.StatusOK {
				t.Fatalf("callback: %d %s", w.Code, w.Body.String())
			}
			waitSIWCExchange(t, flow)
			if status := performOAuthStatusRequest(t, f.router, state); status.Status != "error" {
				t.Fatalf("expected error status for replaced account, got %#v", status)
			}
			got, _ := f.manager.GetByID(f.auth.ID)
			if got.Metadata["account_id"] != "account-b" || got.Metadata["use_siwc"] == true || got.Metadata["siwc"] != nil {
				t.Fatalf("binding mixed SIWC tokens into replaced account in memory: %#v", got.Metadata)
			}
			diskData, errRead := os.ReadFile(f.path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			var disk map[string]any
			if errJSON := json.Unmarshal(diskData, &disk); errJSON != nil {
				t.Fatal(errJSON)
			}
			if disk["account_id"] != "account-b" || disk["use_siwc"] == true || disk["siwc"] != nil {
				t.Fatalf("binding mixed SIWC tokens into replaced account on disk: %#v", disk)
			}
		})
	}
}

type siwcCommitWindowContext struct {
	context.Context
	errCalls atomic.Int32
	onCall   int32
	hook     func()
}

func (c *siwcCommitWindowContext) Err() error {
	if c.errCalls.Add(1) == c.onCall && c.hook != nil {
		c.hook()
	}
	return c.Context.Err()
}

type siwcUploadProgressContext struct {
	context.Context
	valueOnce sync.Once
	valueCh   chan struct{}
	doneOnce  sync.Once
	doneCh    chan struct{}
}

func (c *siwcUploadProgressContext) Value(key any) any {
	c.valueOnce.Do(func() {
		close(c.valueCh)
	})
	return c.Context.Value(key)
}

func (c *siwcUploadProgressContext) Done() <-chan struct{} {
	c.doneOnce.Do(func() {
		close(c.doneCh)
	})
	return c.Context.Done()
}

func TestSIWCBindLegacyUploadDuringCommitWindow(t *testing.T) {
	f := setupSIWCFlow(t, false)
	f.router.POST("/legacy", f.h.UploadAuthFile)
	state, flow := f.start(t)

	uploadedBody := map[string]any{
		"type":          "codex",
		"access_token":  "uploaded-outer",
		"refresh_token": "uploaded-refresh",
		"account_id":    "account",
		"note":          "uploaded-during-commit",
	}
	rawBody, errMarshal := json.Marshal(uploadedBody)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}

	reachedMutation := make(chan struct{})
	reachedUpsert := make(chan struct{})
	uploadDone := make(chan *httptest.ResponseRecorder, 1)

	flow.mu.Lock()
	flow.ctx = &siwcCommitWindowContext{
		Context: flow.ctx,
		onCall:  6,
		hook: func() {
			go func() {
				req := httptest.NewRequest(http.MethodPost, "/legacy?name=codex.json", strings.NewReader(string(rawBody)))
				req.Header.Set("Content-Type", "application/json")
				req = req.WithContext(&siwcUploadProgressContext{
					Context: req.Context(),
					valueCh: reachedMutation,
					doneCh:  reachedUpsert,
				})
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, req)
				uploadDone <- w
			}()
			<-reachedMutation
			for range 500 {
				select {
				case <-reachedUpsert:
					return
				default:
					runtime.Gosched()
				}
			}
		},
	}
	flow.mu.Unlock()

	w := siwcRequest(t, f.router, http.MethodPost, "/callback", map[string]any{"provider": "codex", "redirect_url": siwcCallbackURL(state)})
	if w.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	waitSIWCExchange(t, flow)
	if status := performOAuthStatusRequest(t, f.router, state); status.Status != "ok" {
		t.Fatalf("expected bind commit to finish before locked upload replaced file, got %#v", status)
	}

	var wUpload *httptest.ResponseRecorder
	select {
	case wUpload = <-uploadDone:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy upload did not finish")
	}
	if wUpload.Code != http.StatusOK {
		t.Fatalf("legacy upload status = %d, body = %s", wUpload.Code, wUpload.Body.String())
	}

	diskData, errRead := os.ReadFile(f.path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var disk map[string]any
	if errJSON := json.Unmarshal(diskData, &disk); errJSON != nil {
		t.Fatal(errJSON)
	}
	got, ok := f.manager.GetByID(f.auth.ID)
	if !ok || got == nil {
		t.Fatal("missing auth in manager after upload")
	}
	if disk["note"] != "uploaded-during-commit" || disk["access_token"] != "uploaded-outer" {
		t.Fatalf("SIWC bind overwrote legacy upload on disk: %#v", disk)
	}
	if got.Metadata["note"] != "uploaded-during-commit" || got.Metadata["access_token"] != "uploaded-outer" {
		t.Fatalf("unexpected in-memory metadata after upload: %#v", got.Metadata)
	}
	if disk["use_siwc"] != got.Metadata["use_siwc"] {
		t.Fatalf("disk and memory disagree on use_siwc: disk=%v memory=%v", disk["use_siwc"], got.Metadata["use_siwc"])
	}
}

func TestWriteAuthFileWaitsForCodexFileLock(t *testing.T) {
	for _, tc := range []struct {
		name         string
		useSymlink   bool
		uploadedBody map[string]any
	}{
		{
			name:       "codex_upload",
			useSymlink: false,
			uploadedBody: map[string]any{
				"type":          "codex",
				"access_token":  "uploaded-outer",
				"refresh_token": "uploaded-refresh",
				"account_id":    "account",
				"note":          "after-lock",
			},
		},
		{
			name:       "non_codex_replacing_existing_codex",
			useSymlink: false,
			uploadedBody: map[string]any{
				"type":          "claude",
				"access_token":  "claude-access",
				"refresh_token": "claude-refresh",
				"note":          "replaced-provider",
			},
		},
		{
			name:       "symlinked_auth_dir_alias",
			useSymlink: true,
			uploadedBody: map[string]any{
				"type":          "codex",
				"access_token":  "uploaded-outer",
				"refresh_token": "uploaded-refresh",
				"account_id":    "account",
				"note":          "via-symlink-dir",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupSIWCFlow(t, false)
			f.router.POST("/legacy", f.h.UploadAuthFile)

			lockPath := f.path
			if tc.useSymlink {
				aliasDir := filepath.Join(t.TempDir(), "auths-link")
				if errSymlink := os.Symlink(filepath.Dir(f.path), aliasDir); errSymlink != nil {
					t.Skipf("symlinks unavailable: %v", errSymlink)
				}
				f.h.cfg.AuthDir = aliasDir
				if realPath, errEval := filepath.EvalSymlinks(f.path); errEval == nil {
					lockPath = realPath
				}
			}

			rawBody, errMarshal := json.Marshal(tc.uploadedBody)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}

			locked := make(chan struct{})
			releaseLock := make(chan struct{})
			var releaseOnce sync.Once
			release := func() {
				releaseOnce.Do(func() {
					close(releaseLock)
				})
			}
			t.Cleanup(release)
			lockDone := make(chan error, 1)
			go func() {
				lockDone <- codex.WithFileLock(lockPath, func() error {
					close(locked)
					<-releaseLock
					return nil
				})
			}()
			<-locked

			reachedMutation := make(chan struct{})
			reachedUpsert := make(chan struct{})
			uploadDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest(http.MethodPost, "/legacy?name=codex.json", strings.NewReader(string(rawBody)))
				req.Header.Set("Content-Type", "application/json")
				req = req.WithContext(&siwcUploadProgressContext{
					Context: req.Context(),
					valueCh: reachedMutation,
					doneCh:  reachedUpsert,
				})
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, req)
				uploadDone <- w
			}()
			<-reachedMutation
			for range 200 {
				runtime.Gosched()
			}
			select {
			case <-reachedUpsert:
				t.Fatal("writeAuthFile bypassed codex.WithFileLock and reached upsertAuthRecord while lock was held")
			default:
			}
			diskWhileLocked, errRead := os.ReadFile(f.path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if strings.Contains(string(diskWhileLocked), tc.uploadedBody["note"].(string)) {
				t.Fatalf("writeAuthFile modified disk while codex.WithFileLock was held: %s", string(diskWhileLocked))
			}

			release()
			select {
			case errLock := <-lockDone:
				if errLock != nil {
					t.Fatal(errLock)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("file lock holder did not finish")
			}
			var wUpload *httptest.ResponseRecorder
			select {
			case wUpload = <-uploadDone:
			case <-time.After(5 * time.Second):
				t.Fatal("legacy upload did not finish")
			}
			if wUpload.Code != http.StatusOK {
				t.Fatalf("legacy upload status = %d, body = %s", wUpload.Code, wUpload.Body.String())
			}
			diskAfter, errReadAfter := os.ReadFile(f.path)
			if errReadAfter != nil {
				t.Fatal(errReadAfter)
			}
			if !strings.Contains(string(diskAfter), tc.uploadedBody["note"].(string)) {
				t.Fatalf("expected uploaded content after lock release, got: %s", string(diskAfter))
			}
		})
	}
}
