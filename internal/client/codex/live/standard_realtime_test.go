package live

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newStandardRealtimeHandler(t *testing.T) (*Handler, *captureExecutor, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{responseBody: io.NopCloser(strings.NewReader("v=0\r\no=upstream-answer\r\n"))}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	handler := NewHandler(manager, nil)
	router := gin.New()
	router.POST("/v1/realtime", handler.Handle)
	router.POST("/v1/realtime/calls", handler.Handle)
	router.POST("/v1/live", handler.Handle)
	return handler, executor, router
}

func TestIsStandardRealtimeModel(t *testing.T) {
	for model, want := range map[string]bool{
		"gpt-realtime":                 true,
		"gpt-realtime-2.1":             true,
		"GPT-Realtime ":                true,
		"gpt-4o-realtime-preview":      true,
		"gpt-4o-realtime-preview-2024": true,
		"":                             false,
		"gpt-realtimefoo":              false,
		"gpt-live-1-codex":             false,
		"codex-mini-latest":            false,
	} {
		if got := isStandardRealtimeModel(model); got != want {
			t.Errorf("isStandardRealtimeModel(%q) = %t, want %t", model, got, want)
		}
	}
}

func TestCodexRealtimeModelMapsOnlyStandardModels(t *testing.T) {
	for model, want := range map[string]string{
		"":                        defaultLiveModel,
		"gpt-realtime":            defaultLiveModel,
		"gpt-realtime-2.1":        defaultLiveModel,
		"gpt-4o-realtime-preview": defaultLiveModel,
		"gpt-live-1-codex":        "gpt-live-1-codex",
		"future-live-model":       "future-live-model",
	} {
		if got := codexRealtimeModel(model); got != want {
			t.Errorf("codexRealtimeModel(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestStandardRealtimeCallURL(t *testing.T) {
	handler := NewHandler(nil, nil)
	if got := handler.standardRealtimeCallURL("gpt-realtime-2.1"); got != "https://api.openai.com/v1/realtime?model=gpt-realtime-2.1" {
		t.Fatalf("standardRealtimeCallURL() = %q", got)
	}
	if got := handler.standardRealtimeCallURL("model with spaces"); got != "https://api.openai.com/v1/realtime?model=model+with+spaces" {
		t.Fatalf("standardRealtimeCallURL() = %q", got)
	}
}

func TestStandardRealtimeCallPreservesRequestedModel(t *testing.T) {
	_, executor, router := newStandardRealtimeHandler(t)

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime", strings.NewReader(`{"model":"gpt-realtime-2.1","sdp":"v=0\r\no=client-offer\r\n"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer downstream-api-key")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if executor.request == nil || executor.selectedAuth == nil {
		t.Fatal("Codex executor did not receive a realtime request")
	}
	if got := executor.request.URL.String(); got != "https://api.openai.com/v1/realtime?model=gpt-realtime-2.1" {
		t.Fatalf("upstream URL = %q, want the requested model without substitution", got)
	}
	if got := executor.request.Header.Get("Content-Type"); got != "application/sdp" {
		t.Fatalf("Content-Type = %q, want application/sdp", got)
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", got)
	}
	if got := executor.request.Header.Get("OpenAI-Alpha"); got != "" {
		t.Fatalf("OpenAI-Alpha = %q, want it stripped for the standard Realtime upstream", got)
	}
	if got := string(executor.body); got != "v=0\r\no=client-offer\r\n" {
		t.Fatalf("upstream body = %q, want the raw SDP offer", got)
	}
	if strings.Contains(string(executor.body), defaultLiveModel) {
		t.Fatalf("upstream body substitutes the Codex Live model: %s", executor.body)
	}
	if got := recorder.Header().Get("Location"); got != "" {
		t.Fatalf("Location = %q, want none for standard Realtime WebRTC", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/sdp" {
		t.Fatalf("response Content-Type = %q", got)
	}
	if got := recorder.Body.String(); got != "v=0\r\no=upstream-answer\r\n" {
		t.Fatalf("response body = %q", got)
	}
}

func TestStandardRealtimeCallUsesQueryModelFallback(t *testing.T) {
	_, executor, router := newStandardRealtimeHandler(t)

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime?model=gpt-realtime-2.1", strings.NewReader("v=0\r\no=browser-offer\r\n"))
	request.Header.Set("Content-Type", "application/sdp")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if got := executor.request.URL.String(); got != "https://api.openai.com/v1/realtime?model=gpt-realtime-2.1" {
		t.Fatalf("upstream URL = %q, want the query model", got)
	}
	if got := string(executor.body); got != "v=0\r\no=browser-offer\r\n" {
		t.Fatalf("upstream body = %q", got)
	}
}

func TestLiveCallWithoutModelStillUsesQuicksilver(t *testing.T) {
	_, executor, router := newStandardRealtimeHandler(t)

	request := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader("v=0\r\no=desktop-offer\r\n"))
	request.Header.Set("Content-Type", "application/sdp")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if got := executor.request.URL.String(); got != upstreamCallURL {
		t.Fatalf("upstream URL = %q, want %q", got, upstreamCallURL)
	}
}

func TestStandardRealtimeCallRelaysMediaAndQueuesSessionUpdate(t *testing.T) {
	handler, executor, router := newStandardRealtimeHandler(t)
	mediaSession := &fakeMediaSession{downstreamSDP: "v=0\r\no=downstream-answer\r\n"}
	mediaRelay := &fakeMediaRelay{
		upstreamOffer: "v=0\r\no=gateway-offer\r\n",
		session:       mediaSession,
	}
	handler.mediaRelay = mediaRelay

	body := `{"model":"gpt-realtime-2.1","sdp":"v=0\r\no=client-offer\r\n","session":{"type":"realtime","model":"gpt-realtime-2.1","voice":"marin","instructions":"be brief"}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if mediaRelay.clientOffer != "v=0\r\no=client-offer\r\n" {
		t.Fatalf("media client offer = %q", mediaRelay.clientOffer)
	}
	if got := string(executor.body); got != "v=0\r\no=gateway-offer\r\n" {
		t.Fatalf("upstream body = %q, want the relay gateway offer", got)
	}
	if mediaSession.upstreamAnswer != "v=0\r\no=upstream-answer\r\n" {
		t.Fatalf("accepted upstream answer = %q", mediaSession.upstreamAnswer)
	}
	if got := recorder.Body.String(); got != "v=0\r\no=downstream-answer\r\n" {
		t.Fatalf("response body = %q, want the downstream answer", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/sdp" {
		t.Fatalf("response Content-Type = %q, want application/sdp", got)
	}
	if mediaSession.callID == "" {
		t.Fatal("media session call ID was not set")
	}
	if mediaSession.closed.Load() {
		t.Fatalf("media session was closed with reason %q, want it retained for the call", mediaSession.closeReason)
	}
	stored, ok := handler.sessions.peek(mediaSession.callID)
	if !ok || stored.authID != "codex-oauth" || stored.model != "gpt-realtime-2.1" {
		t.Fatalf("stored standard realtime session = %#v, ok=%t", stored, ok)
	}
	if len(mediaSession.upstreamTexts) != 1 {
		t.Fatalf("upstream texts = %#v, want one queued session.update", mediaSession.upstreamTexts)
	}
	var update struct {
		Type    string         `json:"type"`
		Session map[string]any `json:"session"`
	}
	if errUnmarshal := json.Unmarshal([]byte(mediaSession.upstreamTexts[0]), &update); errUnmarshal != nil {
		t.Fatalf("unmarshal queued update: %v; text=%s", errUnmarshal, mediaSession.upstreamTexts[0])
	}
	if update.Type != "session.update" || update.Session["voice"] != "marin" || update.Session["instructions"] != "be brief" {
		t.Fatalf("queued update = %#v", update)
	}
	if _, hasModel := update.Session["model"]; hasModel {
		t.Fatalf("queued update keeps a model field: %#v", update.Session)
	}
}

func TestStandardRealtimeCallClientSecretSessionDrivesModel(t *testing.T) {
	handler, executor, router := newStandardRealtimeHandler(t)
	mediaSession := &fakeMediaSession{downstreamSDP: "v=0\r\no=downstream-answer\r\n"}
	mediaRelay := &fakeMediaRelay{
		upstreamOffer: "v=0\r\no=gateway-offer\r\n",
		session:       mediaSession,
	}
	handler.mediaRelay = mediaRelay
	token, _, _, errCreate := handler.clientSecrets.create(
		json.RawMessage(`{"type":"realtime","model":"gpt-realtime-2.1","voice":"cedar"}`),
		time.Minute, "issuer-key", "static",
	)
	if errCreate != nil {
		t.Fatalf("create client secret: %v", errCreate)
	}

	router.POST("/v1/realtime/secret", func(c *gin.Context) {
		authorization, matched, errAuthenticate := handler.AuthenticateClientSecret(c.Request)
		if errAuthenticate != nil || !matched {
			c.Status(http.StatusUnauthorized)
			c.Abort()
			return
		}
		c.Set(ClientSecretSessionContextKey, authorization.Session)
		c.Set(ClientSecretPrincipalContextKey, authorization.Principal)
		c.Next()
	}, handler.Handle)

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/secret", strings.NewReader("v=0\r\no=browser-offer\r\n"))
	request.Header.Set("Content-Type", "application/sdp")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if got := executor.request.URL.String(); got != "https://api.openai.com/v1/realtime?model=gpt-realtime-2.1" {
		t.Fatalf("upstream URL = %q, want the client secret model without substitution", got)
	}
	if len(mediaSession.upstreamTexts) != 1 {
		t.Fatalf("upstream texts = %#v, want the client secret session queued as session.update", mediaSession.upstreamTexts)
	}
	var update struct {
		Type    string         `json:"type"`
		Session map[string]any `json:"session"`
	}
	if errUnmarshal := json.Unmarshal([]byte(mediaSession.upstreamTexts[0]), &update); errUnmarshal != nil {
		t.Fatalf("unmarshal queued update: %v", errUnmarshal)
	}
	if update.Type != "session.update" || update.Session["voice"] != "cedar" {
		t.Fatalf("queued update = %#v", update)
	}
	if stored, ok := handler.sessions.peek(mediaSession.callID); !ok || stored.clientSecretPrincipal == "" {
		t.Fatalf("stored session = %#v, ok=%t, want the client secret principal recorded", stored, ok)
	}
}

func TestStandardRealtimeCallPassesUpstreamErrorThrough(t *testing.T) {
	_, executor, router := newStandardRealtimeHandler(t)
	executor.statusCode = http.StatusBadRequest
	executor.responseBody = io.NopCloser(strings.NewReader(`{"error":{"message":"Unknown model: gpt-realtime-2.1"}}`))

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime?model=gpt-realtime-2.1", strings.NewReader("v=0\r\no=client-offer\r\n"))
	request.Header.Set("Content-Type", "application/sdp")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "Unknown model") {
		t.Fatalf("response body = %q, want the upstream error passthrough", recorder.Body.String())
	}
}
