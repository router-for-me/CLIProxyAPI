package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type apiKeyFirstSelector struct{}

func (*apiKeyFirstSelector) Pick(_ context.Context, _ string, _ string, _ coreexecutor.Options, auths []*auth.Auth) (*auth.Auth, error) {
	for _, candidate := range auths {
		if candidate.AuthKind() == auth.AuthKindAPIKey {
			return candidate, nil
		}
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

type captureExecutor struct {
	request      *http.Request
	body         []byte
	selectedAuth *auth.Auth
	responseBody io.ReadCloser
	statusCode   int
}

func (*captureExecutor) Identifier() string { return "codex" }

func (*captureExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (*captureExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (*captureExecutor) Refresh(_ context.Context, credential *auth.Auth) (*auth.Auth, error) {
	return credential, nil
}

func (*captureExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (*captureExecutor) PrepareRequest(req *http.Request, credential *auth.Auth) error {
	token, _ := credential.Metadata["access_token"].(string)
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (e *captureExecutor) HttpRequest(_ context.Context, credential *auth.Auth, req *http.Request) (*http.Response, error) {
	e.request = req.Clone(req.Context())
	e.selectedAuth = credential.Clone()
	body, errRead := io.ReadAll(req.Body)
	if errRead != nil {
		return nil, errRead
	}
	e.body = body
	statusCode := e.statusCode
	if statusCode == 0 {
		statusCode = http.StatusCreated
	}
	return &http.Response{
		StatusCode: statusCode,
		Header: http.Header{
			"Connection":          []string{"X-Connection-Secret"},
			"Content-Type":        []string{"application/sdp"},
			"Location":            []string{"/v1/live/call-123"},
			"Set-Cookie":          []string{"session=secret"},
			"X-Connection-Secret": []string{"secret"},
			"X-Live-Session":      []string{"live-session-123"},
		},
		Body: e.responseBody,
	}, nil
}

type homeDispatcher struct {
	model string
}

func (*homeDispatcher) HeartbeatOK() bool { return true }

func (d *homeDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	d.model = model
	return json.Marshal(map[string]any{
		"model":      model,
		"provider":   "codex",
		"auth_index": "home-codex-live",
		"auth": map[string]any{
			"id":       "home-codex-live",
			"provider": "codex",
			"status":   "active",
			"metadata": map[string]any{"access_token": "home-live-token"},
		},
		"concurrency": map[string]any{
			"accounted":     true,
			"credential_id": "home-codex-live",
			"model":         model,
		},
	})
}

func (*homeDispatcher) AbortAmbiguousDispatch() {}

type failingHTTPWriter struct {
	header http.Header
	status int
}

func (w *failingHTTPWriter) Header() http.Header {
	return w.header
}

func (*failingHTTPWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream write failed")
}

func (w *failingHTTPWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

type trackedResponseBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackedResponseBody) Close() error {
	b.closed.Store(true)
	return nil
}

type fakeMediaRelay struct {
	clientOffer   string
	route         mediaSessionRoute
	upstreamOffer string
	session       *fakeMediaSession
	err           error
}

func (r *fakeMediaRelay) NewSession(_ context.Context, clientOffer string, route mediaSessionRoute) (mediaRelaySession, string, error) {
	r.clientOffer = clientOffer
	r.route = route
	return r.session, r.upstreamOffer, r.err
}

type fakeMediaSession struct {
	upstreamAnswer string
	callIDAtAccept string
	downstreamSDP  string
	closeHandler   func(string)
	callID         string
	closeReason    string
	closed         atomic.Bool
	err            error
}

func (s *fakeMediaSession) AcceptUpstreamAnswer(_ context.Context, answer string) (string, error) {
	s.upstreamAnswer = answer
	s.callIDAtAccept = s.callID
	return s.downstreamSDP, s.err
}

func (s *fakeMediaSession) SetCallID(callID string) {
	s.callID = callID
}

func (s *fakeMediaSession) SetCloseHandler(handler func(string)) {
	s.closeHandler = handler
}

func (s *fakeMediaSession) Close() error {
	return s.CloseWithReason("closed")
}

func (s *fakeMediaSession) CloseWithReason(reason string) error {
	s.closeReason = reason
	s.closed.Store(true)
	return nil
}

func registerCredential(t *testing.T, manager *auth.Manager, credential *auth.Auth) {
	t.Helper()
	if _, errRegister := manager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register %s: %v", credential.ID, errRegister)
	}
}

func multipartBody(boundary, sdp, session string) string {
	body := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"sdp\"\r\n" +
		"Content-Type: application/sdp\r\n\r\n" +
		sdp + "\r\n"
	if session != "" {
		body += "--" + boundary + "\r\n" +
			"Content-Disposition: form-data; name=\"session\"\r\n" +
			"Content-Type: application/json\r\n\r\n" +
			session + "\r\n"
	}
	return body + "--" + boundary + "--\r\n"
}

func TestHandlerRewritesLiveCallAndSchedulesOAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := auth.NewManager(nil, &apiKeyFirstSelector{}, nil)
	responseBody := &trackedResponseBody{Reader: strings.NewReader("v=0\r\na=ice-lite\r\n")}
	executor := &captureExecutor{responseBody: responseBody}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID:         "codex-api-key",
		Provider:   "codex",
		Status:     auth.StatusActive,
		Attributes: map[string]string{auth.AttributeAPIKey: "must-not-be-used"},
	})
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{
			"access_token": "oauth-token",
			"account_id":   "account-123",
		},
	})

	handler := NewHandler(manager, nil)
	router := gin.New()
	router.POST("/v1/live", handler.Handle)

	const boundary = "codex-realtime-call-boundary"
	body := multipartBody(boundary, "v=0\r\na=setup:actpass", `{"model":"gpt-live-1-codex"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer downstream-api-key")
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set("Originator", "Codex Desktop")
	req.Header.Set("Thread-Id", "thread-123")
	req.Header.Set("Session-Id", "session-123")
	req.Header.Set("OpenAI-Alpha", "quicksilver=v2")
	req.Header.Set("X-Oai-Attestation", "attestation-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if executor.request == nil || executor.selectedAuth == nil {
		t.Fatal("Codex executor did not receive a live request")
	}
	if executor.selectedAuth.ID != "codex-oauth" {
		t.Fatalf("selected auth = %q, want codex-oauth", executor.selectedAuth.ID)
	}
	if got := executor.request.URL.String(); got != upstreamCallURL {
		t.Fatalf("upstream URL = %q, want %q", got, upstreamCallURL)
	}
	var upstreamPayload struct {
		SDP     string         `json:"sdp"`
		Session map[string]any `json:"session"`
	}
	if errUnmarshal := json.Unmarshal(executor.body, &upstreamPayload); errUnmarshal != nil {
		t.Fatalf("unmarshal upstream body: %v; body=%s", errUnmarshal, executor.body)
	}
	if upstreamPayload.SDP != "v=0\r\na=setup:actpass" {
		t.Fatalf("upstream sdp = %q", upstreamPayload.SDP)
	}
	if got := upstreamPayload.Session["model"]; got != "gpt-live-1-codex" {
		t.Fatalf("upstream session model = %#v", got)
	}
	if got := executor.request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", got)
	}
	if got := executor.request.Header.Get("Chatgpt-Account-Id"); got != "account-123" {
		t.Fatalf("Chatgpt-Account-Id = %q, want account-123", got)
	}
	for header, want := range map[string]string{
		"OpenAI-Alpha":      "quicksilver=v2",
		"Originator":        "Codex Desktop",
		"Session-Id":        "session-123",
		"Thread-Id":         "thread-123",
		"X-Oai-Attestation": "attestation-token",
	} {
		if got := executor.request.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if got := recorder.Body.String(); got != "v=0\r\na=ice-lite\r\n" {
		t.Fatalf("response body = %q", got)
	}
	if got := recorder.Header().Get("Location"); got != "/v1/live/call-123" {
		t.Fatalf("Location = %q, want live call location", got)
	}
	for _, blocked := range []string{"Connection", "Set-Cookie", "X-Connection-Secret", "X-Live-Session"} {
		if got := recorder.Header().Get(blocked); got != "" {
			t.Errorf("blocked response header %s leaked as %q", blocked, got)
		}
	}
	if !responseBody.closed.Load() {
		t.Fatal("upstream response body was not closed")
	}
	stored, ok := handler.sessions.peek("call-123")
	if !ok || stored.authID != "codex-oauth" || stored.model != "gpt-live-1-codex" {
		t.Fatalf("stored live session = %#v, ok=%t", stored, ok)
	}
}

func TestMediaCredentialNameUsesSafeIdentity(t *testing.T) {
	for name, testCase := range map[string]struct {
		selected *auth.Auth
		index    string
		want     string
	}{
		"label": {
			selected: &auth.Auth{Label: "Voice credential", FileName: "/auths/codex-user.json", ID: "secret-id"},
			index:    "auth-index",
			want:     "Voice credential",
		},
		"file basename": {
			selected: &auth.Auth{FileName: "/auths/codex-user.json", ID: "secret-id"},
			index:    "auth-index",
			want:     "codex-user.json",
		},
		"opaque index": {
			selected: &auth.Auth{ID: "secret-id"},
			index:    "auth-index",
			want:     "auth-index",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := mediaCredentialName(testCase.selected, testCase.index); got != testCase.want {
				t.Fatalf("mediaCredentialName() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestProxyURLForAuthPrefersCredentialOverride(t *testing.T) {
	cfg := &config.Config{}
	cfg.ProxyURL = "http://global.example:8080"
	if got := proxyURLForAuth(cfg, &auth.Auth{ProxyURL: "socks5://credential.example:1080"}); got != "socks5://credential.example:1080" {
		t.Fatalf("effective proxy URL = %q, want credential override", got)
	}
	if got := proxyURLForAuth(cfg, &auth.Auth{}); got != "http://global.example:8080" {
		t.Fatalf("effective proxy URL = %q, want global fallback", got)
	}
	if got := proxyURLForAuth(cfg, &auth.Auth{ProxyURL: "direct"}); got != "direct" {
		t.Fatalf("effective proxy URL = %q, want explicit direct override", got)
	}
}

func TestHandlerRelaysWebRTCMediaSDP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{
		responseBody: &trackedResponseBody{Reader: strings.NewReader("v=0\r\no=upstream-answer\r\n")},
	}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Label:    "Voice credential",
		ProxyURL: "socks5://credential-proxy.example:1080",
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	mediaSession := &fakeMediaSession{downstreamSDP: "v=0\r\no=downstream-answer\r\n"}
	mediaRelay := &fakeMediaRelay{
		upstreamOffer: "v=0\r\no=gateway-offer\r\n",
		session:       mediaSession,
	}
	runtimeConfig := &config.Config{}
	runtimeConfig.ProxyURL = "http://global-proxy.example:8080"
	handler := NewHandler(manager, runtimeConfig)
	handler.mediaRelay = mediaRelay
	router := gin.New()
	router.POST("/v1/live", handler.Handle)

	const boundary = "media-relay-boundary"
	body := multipartBody(boundary, "v=0\r\no=desktop-offer\r\n", `{"model":"gpt-live-1-codex"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if mediaRelay.clientOffer != "v=0\r\no=desktop-offer\r\n" {
		t.Fatalf("media client offer = %q", mediaRelay.clientOffer)
	}
	if mediaRelay.route.proxyURL != "socks5://credential-proxy.example:1080" {
		t.Fatalf("media proxy URL = %q, want credential override", mediaRelay.route.proxyURL)
	}
	if mediaRelay.route.credential != "Voice credential" || mediaRelay.route.authIndex == "" {
		t.Fatalf("media credential route = %#v", mediaRelay.route)
	}
	var upstreamPayload struct {
		SDP string `json:"sdp"`
	}
	if errUnmarshal := json.Unmarshal(executor.body, &upstreamPayload); errUnmarshal != nil {
		t.Fatalf("unmarshal upstream body: %v", errUnmarshal)
	}
	if upstreamPayload.SDP != mediaRelay.upstreamOffer {
		t.Fatalf("upstream SDP = %q, want gateway offer", upstreamPayload.SDP)
	}
	if mediaSession.upstreamAnswer != "v=0\r\no=upstream-answer\r\n" {
		t.Fatalf("accepted upstream answer = %q", mediaSession.upstreamAnswer)
	}
	if mediaSession.callID != "call-123" {
		t.Fatalf("media call ID = %q, want call-123", mediaSession.callID)
	}
	if mediaSession.callIDAtAccept != "call-123" {
		t.Fatalf("media call ID at answer acceptance = %q, want call-123", mediaSession.callIDAtAccept)
	}
	if got := recorder.Body.String(); got != mediaSession.downstreamSDP {
		t.Fatalf("downstream SDP = %q, want %q", got, mediaSession.downstreamSDP)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/sdp" {
		t.Fatalf("Content-Type = %q, want application/sdp", got)
	}
	if mediaSession.closed.Load() {
		t.Fatal("retained media session was closed before session completion")
	}
	if mediaSession.closeHandler == nil {
		t.Fatal("media session close handler was not installed")
	}
	mediaSession.closeHandler("test_closed")
	if !mediaSession.closed.Load() {
		t.Fatal("completed media session was not closed")
	}
	if _, ok := handler.sessions.peek("call-123"); ok {
		t.Fatal("completed media session remained stored")
	}
}

func TestHandlerClosesUnretainedMediaSession(t *testing.T) {
	for name, testCase := range map[string]struct {
		upstreamStatus int
		answerError    error
		wantStatus     int
	}{
		"upstream rejection": {
			upstreamStatus: http.StatusUnauthorized,
			wantStatus:     http.StatusUnauthorized,
		},
		"invalid upstream answer": {
			upstreamStatus: http.StatusCreated,
			answerError:    errors.New("invalid answer"),
			wantStatus:     http.StatusBadGateway,
		},
	} {
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			manager := auth.NewManager(nil, nil, nil)
			executor := &captureExecutor{
				responseBody: &trackedResponseBody{Reader: strings.NewReader("v=0\r\no=upstream-answer\r\n")},
				statusCode:   testCase.upstreamStatus,
			}
			manager.RegisterExecutor(executor)
			registerCredential(t, manager, &auth.Auth{
				ID:       "codex-oauth",
				Provider: "codex",
				Status:   auth.StatusActive,
				Metadata: map[string]any{"access_token": "oauth-token"},
			})
			mediaSession := &fakeMediaSession{
				downstreamSDP: "v=0\r\no=downstream-answer\r\n",
				err:           testCase.answerError,
			}
			handler := NewHandler(manager, nil)
			handler.mediaRelay = &fakeMediaRelay{
				upstreamOffer: "v=0\r\no=gateway-offer\r\n",
				session:       mediaSession,
			}
			router := gin.New()
			router.POST("/v1/live", handler.Handle)

			const boundary = "media-error-boundary"
			body := multipartBody(boundary, "v=0\r\no=desktop-offer\r\n", `{"model":"gpt-live-1-codex"}`)
			req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(body))
			req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			if !mediaSession.closed.Load() {
				t.Fatal("failed request retained its media session")
			}
			if mediaSession.closeReason != "request_not_retained" {
				t.Fatalf("media close reason = %q, want request_not_retained", mediaSession.closeReason)
			}
			if _, ok := handler.sessions.peek("call-123"); ok {
				t.Fatal("failed request stored its media session")
			}
		})
	}
}

func TestHandlerReleasesHomeSelectionWhenMediaSetupFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(&homeDispatcher{}, registry, 1)
	manager.RegisterExecutor(&captureExecutor{})
	handler := NewHandler(manager, nil)
	handler.mediaRelay = &fakeMediaRelay{err: errors.New("media setup failed")}
	router := gin.New()
	router.POST("/v1/live", handler.Handle)

	const boundary = "home-media-error-boundary"
	body := multipartBody(boundary, "v=0\r\no=desktop-offer\r\n", `{"model":"gpt-live-1-codex"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	if got := len(registry.FreezeInFlight(time.Now()).Executions); got != 0 {
		t.Fatalf("active Home executions = %d, want 0", got)
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

func TestHandlerClosesMediaWhenResponseWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{
		responseBody: &trackedResponseBody{Reader: strings.NewReader("v=0\r\no=upstream-answer\r\n")},
	})
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	mediaSession := &fakeMediaSession{downstreamSDP: "v=0\r\no=downstream-answer\r\n"}
	handler := NewHandler(manager, nil)
	handler.mediaRelay = &fakeMediaRelay{
		upstreamOffer: "v=0\r\no=gateway-offer\r\n",
		session:       mediaSession,
	}
	router := gin.New()
	router.POST("/v1/live", handler.Handle)

	const boundary = "response-write-error-boundary"
	body := multipartBody(boundary, "v=0\r\no=desktop-offer\r\n", `{"model":"gpt-live-1-codex"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	writer := &failingHTTPWriter{header: make(http.Header)}
	router.ServeHTTP(writer, req)

	if writer.status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", writer.status, http.StatusCreated)
	}
	if !mediaSession.closed.Load() {
		t.Fatal("response write failure retained its media session")
	}
	if mediaSession.closeReason != "response_write_failed" {
		t.Fatalf("media close reason = %q, want response_write_failed", mediaSession.closeReason)
	}
	if _, ok := handler.sessions.peek("call-123"); ok {
		t.Fatal("response write failure retained a stored session")
	}
}

func TestHandlerUsesLiveModelForHomeDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	dispatcher := &homeDispatcher{}
	registry := executionregistry.New()
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	responseBody := &trackedResponseBody{Reader: strings.NewReader("v=0\r\n")}
	executor := &captureExecutor{responseBody: responseBody}
	manager.RegisterExecutor(executor)

	handler := NewHandler(manager, nil)
	router := gin.New()
	router.POST("/v1/live", handler.Handle)

	const boundary = "home-live-boundary"
	body := multipartBody(boundary, "v=0", `{"model":"future-live-model"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if dispatcher.model != "future-live-model" {
		t.Fatalf("Home dispatch model = %q, want future-live-model", dispatcher.model)
	}
	if executor.selectedAuth == nil || executor.selectedAuth.ID != "home-codex-live" {
		t.Fatalf("selected Home auth = %#v", executor.selectedAuth)
	}
	if !responseBody.closed.Load() {
		t.Fatal("Home upstream response body was not closed")
	}
	stored, ok := handler.sessions.peek("call-123")
	if !ok || stored.homeSelection == nil || !stored.homeSelection.Retained() || !stored.homeSelection.Active() {
		t.Fatalf("stored Home live session = %#v, ok=%t", stored, ok)
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
	if stored.homeSelection.Active() {
		t.Fatal("Home live selection remained active after drain")
	}
}

func TestHomeLiveSessionExpiryReleasesSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	registry := executionregistry.New()
	manager.PublishHomeDispatch(&homeDispatcher{}, registry, 1)
	manager.RegisterExecutor(&captureExecutor{
		responseBody: &trackedResponseBody{Reader: strings.NewReader("v=0\r\n")},
	})

	handler := NewHandler(manager, nil)
	handler.sessions.lifetime = 20 * time.Millisecond
	router := gin.New()
	router.POST("/v1/live", handler.Handle)

	const boundary = "expiring-home-live-boundary"
	body := multipartBody(boundary, "v=0", `{"model":"gpt-live-1-codex"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	stored, ok := handler.sessions.peek("call-123")
	if !ok || stored.homeSelection == nil || !stored.homeSelection.Active() {
		t.Fatalf("stored Home live session = %#v, ok=%t", stored, ok)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, stillStored := handler.sessions.peek("call-123")
		if !stillStored && !stored.homeSelection.Active() {
			if errDrain := registry.Drain(context.Background()); errDrain != nil {
				t.Fatalf("Drain() error = %v", errDrain)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("expired Home live session remained active")
}

func TestHandleSidebandPinsAuthAndRelaysBidirectionally(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamHeaders := make(chan http.Header, 1)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, errUpgrade := upgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		upstreamHeaders <- request.Header.Clone()
		messageType, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			return
		}
		_ = conn.WriteMessage(messageType, append([]byte("echo:"), payload...))
	}))
	defer upstreamServer.Close()

	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID:       "other-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "other-token", "account_id": "other-account"},
	})
	registerCredential(t, manager, &auth.Auth{
		ID:       "pinned-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "pinned-token", "account_id": "pinned-account"},
	})

	handler := NewHandler(manager, nil)
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstreamServer.URL, "http") + "/v1"
	handler.sessions.put("call-sideband", liveSession{authID: "pinned-oauth", model: defaultLiveModel})
	router := gin.New()
	router.GET("/v1/live/:call_id", handler.HandleSideband)
	downstreamServer := httptest.NewServer(router)
	defer downstreamServer.Close()

	wsURL := "ws" + strings.TrimPrefix(downstreamServer.URL, "http") + "/v1/live/call-sideband"
	headers := http.Header{
		"OpenAI-Alpha":      []string{"quicksilver=v2"},
		"X-Oai-Attestation": []string{"attestation-token"},
	}
	client, response, errDial := websocket.DefaultDialer.Dial(wsURL, headers)
	if errDial != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		t.Fatalf("dial downstream sideband: %v", errDial)
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	defer func() { _ = client.Close() }()
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte("ping")); errWrite != nil {
		t.Fatalf("write sideband message: %v", errWrite)
	}
	_, payload, errRead := client.ReadMessage()
	if errRead != nil {
		t.Fatalf("read sideband message: %v", errRead)
	}
	if got := string(payload); got != "echo:ping" {
		t.Fatalf("sideband payload = %q, want echo:ping", got)
	}

	select {
	case captured := <-upstreamHeaders:
		if got := captured.Get("Authorization"); got != "Bearer pinned-token" {
			t.Fatalf("upstream Authorization = %q, want pinned OAuth token", got)
		}
		if got := captured.Get("Chatgpt-Account-Id"); got != "pinned-account" {
			t.Fatalf("upstream Chatgpt-Account-Id = %q, want pinned-account", got)
		}
		if got := captured.Get("OpenAI-Alpha"); got != "quicksilver=v2" {
			t.Fatalf("upstream OpenAI-Alpha = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream sideband headers were not captured")
	}
}

func TestPrepareCallRequestRewritesMultipart(t *testing.T) {
	const boundary = "live-model-boundary"
	body := multipartBody(boundary, "v=0-offer", `{"model":"future-live-model","instructions":"hi"}`)

	encoded, contentType, model, errPrepare := prepareCallRequest([]byte(body), "multipart/form-data; boundary="+boundary)
	if errPrepare != nil {
		t.Fatalf("prepareCallRequest() error = %v", errPrepare)
	}
	if contentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", contentType)
	}
	if model != "future-live-model" {
		t.Fatalf("model = %q, want future-live-model", model)
	}
	var payload struct {
		SDP     string         `json:"sdp"`
		Session map[string]any `json:"session"`
	}
	if errUnmarshal := json.Unmarshal(encoded, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal encoded body: %v", errUnmarshal)
	}
	if payload.SDP != "v=0-offer" || payload.Session["instructions"] != "hi" {
		t.Fatalf("encoded payload = %#v", payload)
	}
}

func TestPrepareCallRequestPreservesRawSDPWhenRelayDisabled(t *testing.T) {
	body := []byte("v=0\r\no=raw-offer\r\n")
	prepared, contentType, model, errPrepare := prepareCallRequest(body, "application/sdp")
	if errPrepare != nil {
		t.Fatalf("prepareCallRequest() error = %v", errPrepare)
	}
	if string(prepared) != string(body) {
		t.Fatalf("prepared SDP = %q, want original body", prepared)
	}
	if contentType != "application/sdp" {
		t.Fatalf("content type = %q, want application/sdp", contentType)
	}
	if model != defaultLiveModel {
		t.Fatalf("model = %q, want %q", model, defaultLiveModel)
	}
}

func TestMediaRelayWrapsRawSDPForCodexBackend(t *testing.T) {
	body := []byte("v=0\r\no=raw-offer\r\n")
	clientOffer, errSDP := callRequestSDP(body, "application/sdp")
	if errSDP != nil {
		t.Fatalf("callRequestSDP() error = %v", errSDP)
	}
	if clientOffer != string(body) {
		t.Fatalf("client offer = %q, want original body", clientOffer)
	}
	prepared, contentType, errReplace := replaceCallRequestSDP(body, "application/sdp", "v=0\r\no=gateway-offer\r\n")
	if errReplace != nil {
		t.Fatalf("replaceCallRequestSDP() error = %v", errReplace)
	}
	if contentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", contentType)
	}
	var payload struct {
		SDP string `json:"sdp"`
	}
	if errUnmarshal := json.Unmarshal(prepared, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal prepared request: %v", errUnmarshal)
	}
	if payload.SDP != "v=0\r\no=gateway-offer\r\n" {
		t.Fatalf("upstream SDP = %q", payload.SDP)
	}
}

func TestHandlerUpdatesMediaRelayConfig(t *testing.T) {
	handler := NewHandler(nil, nil)
	if relay, errRelay := handler.currentMediaRelay(); relay != nil || errRelay != nil {
		t.Fatalf("initial media relay = %#v, error = %v", relay, errRelay)
	}
	enabled := &config.Config{Codex: config.CodexConfig{LiveMediaRelay: config.CodexLiveMediaRelayConfig{
		Enabled:                 true,
		MaxSessions:             1,
		DisablePrivateRemoteIPs: false,
	}}}
	if errUpdate := handler.UpdateConfig(enabled); errUpdate != nil {
		t.Fatalf("enable media relay: %v", errUpdate)
	}
	enabledRelay, errRelay := handler.currentMediaRelay()
	if enabledRelay == nil || errRelay != nil {
		t.Fatalf("enabled media relay = %#v, error = %v", enabledRelay, errRelay)
	}
	unchanged := *enabled
	unchanged.Debug = true
	unchanged.ProxyURL = "http://new-proxy.example"
	if errUpdate := handler.UpdateConfig(&unchanged); errUpdate != nil {
		t.Fatalf("apply unrelated config change: %v", errUpdate)
	}
	unchangedRelay, errRelay := handler.currentMediaRelay()
	if unchangedRelay != enabledRelay || errRelay != nil {
		t.Fatalf("unrelated config change rebuilt media relay: before=%#v after=%#v error=%v", enabledRelay, unchangedRelay, errRelay)
	}
	if current := handler.currentConfig(); current == nil || current.ProxyURL != "http://new-proxy.example" {
		t.Fatalf("runtime config was not updated: %#v", current)
	}
	changed := *enabled
	changed.Codex.LiveMediaRelay.MaxSessions = 2
	if errUpdate := handler.UpdateConfig(&changed); errUpdate != nil {
		t.Fatalf("reload media relay: %v", errUpdate)
	}
	changedRelay, errRelay := handler.currentMediaRelay()
	if changedRelay == nil || changedRelay == enabledRelay || errRelay != nil {
		t.Fatalf("changed media relay = %#v, previous=%#v error=%v", changedRelay, enabledRelay, errRelay)
	}
	if errUpdate := handler.UpdateConfig(&config.Config{}); errUpdate != nil {
		t.Fatalf("disable media relay: %v", errUpdate)
	}
	if relay, errRelay := handler.currentMediaRelay(); relay != nil || errRelay != nil {
		t.Fatalf("disabled media relay = %#v, error = %v", relay, errRelay)
	}
}

func TestPrepareCallRequestRejectsInvalidMultipart(t *testing.T) {
	const boundary = "invalid-live-boundary"
	body := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"session\"\r\n\r\n" +
		`{"model":"gpt-live-1-codex"}` + "\r\n" +
		"--" + boundary + "--\r\n"

	if _, _, _, errPrepare := prepareCallRequest([]byte(body), "multipart/form-data; boundary="+boundary); errPrepare == nil {
		t.Fatal("prepareCallRequest() accepted multipart body without sdp")
	}
}

func TestHeadersForLoggingRedactsAttestation(t *testing.T) {
	source := http.Header{
		"Authorization":     []string{"Bearer oauth-token"},
		"X-Oai-Attestation": []string{"attestation-token"},
	}

	got := headersForLogging(source)
	if value := got.Get("X-Oai-Attestation"); value != "[REDACTED]" {
		t.Fatalf("logged X-Oai-Attestation = %q, want redacted", value)
	}
	if value := source.Get("X-Oai-Attestation"); value != "attestation-token" {
		t.Fatalf("source X-Oai-Attestation changed to %q", value)
	}
}

func TestSessionStoreClaimsAndExpiresSessions(t *testing.T) {
	store := newSessionStore()
	store.lifetime = 20 * time.Millisecond
	store.put("call-claim", liveSession{authID: "auth-1", model: defaultLiveModel})

	session, claim := store.claim("call-claim")
	if claim != sessionClaimAcquired {
		t.Fatalf("first claim = %v, want acquired", claim)
	}
	if _, duplicateClaim := store.claim("call-claim"); duplicateClaim != sessionClaimBusy {
		t.Fatalf("duplicate claim = %v, want busy", duplicateClaim)
	}
	store.release(session)
	if _, retryClaim := store.claim("call-claim"); retryClaim != sessionClaimAcquired {
		t.Fatalf("retry claim = %v, want acquired", retryClaim)
	}
	store.release(session)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := store.peek("call-claim"); !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("released live session did not expire")
}

func TestSessionStoreCloseAllReleasesMediaAndResources(t *testing.T) {
	store := newSessionStore()
	mediaSession := &fakeMediaSession{}
	stored := store.put("call-close-all", liveSession{media: mediaSession})
	var resourceClosed atomic.Bool
	stored.resources.add(func() error {
		resourceClosed.Store(true)
		return nil
	})

	store.closeAll("test_shutdown")

	if !mediaSession.closed.Load() {
		t.Fatal("closeAll() did not close the media session")
	}
	if !resourceClosed.Load() {
		t.Fatal("closeAll() did not close session resources")
	}
	if _, ok := store.peek("call-close-all"); ok {
		t.Fatal("closeAll() retained a session")
	}
}

func TestSidebandURLShapes(t *testing.T) {
	if got := buildSidebandURL(defaultSidebandAPIBaseURL, sidebandFrameless, "rtc_1"); got != "wss://api.openai.com/v1/live/rtc_1" {
		t.Fatalf("Frameless sideband URL = %q", got)
	}
	if got := buildSidebandURL(defaultSidebandAPIBaseURL, sidebandRealtimeCalls, "rtc_1"); got != "wss://api.openai.com/v1/realtime/calls/rtc_1" {
		t.Fatalf("Realtime calls sideband URL = %q", got)
	}
	if got := buildSidebandURL(defaultSidebandAPIBaseURL, sidebandRealtimeQuery, "rtc_2"); got != "wss://api.openai.com/v1/realtime?intent=quicksilver&call_id=rtc_2" {
		t.Fatalf("Realtime query sideband URL = %q", got)
	}
	for location, want := range map[string]string{
		"/v1/live/rtc_1":                                "rtc_1",
		"/v1/realtime/calls/rtc_2":                      "rtc_2",
		"/v1/realtime?intent=quicksilver&call_id=rtc_3": "rtc_3",
	} {
		if got := callIDFromLocation(location); got != want {
			t.Errorf("callIDFromLocation(%q) = %q, want %q", location, got, want)
		}
	}
}
