package zai

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestBigModelStartFlowBuildsLocalhostAuthorizeURL verifies the BigModel login
// builds a bigmodel.cn authorize URL with a localhost redirect and the zcode
// appId, since BigModel rejects the server-mediated CLI callback.
func TestBigModelStartFlowBuildsLocalhostAuthorizeURL(t *testing.T) {
	a := NewZAIAuth(nil, ProviderBigModel, "", 0)
	init, err := a.StartFlow(context.Background())
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer a.shutdownBigModelServer()

	u, err := url.Parse(init.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if u.Host != "bigmodel.cn" {
		t.Fatalf("authorize host = %q, want bigmodel.cn", u.Host)
	}
	q := u.Query()
	if q.Get("appId") != bigModelAppID {
		t.Fatalf("appId = %q, want %q", q.Get("appId"), bigModelAppID)
	}
	if q.Get("state") == "" {
		t.Fatal("state missing from authorize url")
	}
	redirect := q.Get("redirect")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, "/callback") {
		t.Fatalf("redirect = %q, want a localhost /callback URL", redirect)
	}
}

// TestBigModelStartFlowHonorsCallbackPort verifies that an explicit callback port
// (from --oauth-callback-port) is used for the local server and redirect URI,
// matching the other OAuth providers.
func TestBigModelStartFlowHonorsCallbackPort(t *testing.T) {
	// Reserve a free port, release it, then assert the override binds to it.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	a := NewZAIAuth(nil, ProviderBigModel, "", port)
	init, err := a.StartFlow(context.Background())
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	defer a.shutdownBigModelServer()

	u, err := url.Parse(init.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	if got := u.Query().Get("redirect"); !strings.Contains(got, fmt.Sprintf(":%d/", port)) {
		t.Fatalf("redirect = %q, want override port %d", got, port)
	}
}

// TestBigModelCallbackCapturesCode verifies the local callback handler extracts
// the authCode and state from the redirect query.
func TestBigModelCallbackCapturesCode(t *testing.T) {
	a := NewZAIAuth(nil, ProviderBigModel, "", 0)
	a.bmState = "state-123"
	a.bmResult = make(chan bmCallback, 1)

	req := httptest.NewRequest("GET", "/callback?authCode=the-code&state=state-123", nil)
	a.handleBigModelCallback(httptest.NewRecorder(), req)

	cb := <-a.bmResult
	if cb.err != nil {
		t.Fatalf("unexpected callback error: %v", cb.err)
	}
	if cb.code != "the-code" {
		t.Fatalf("code = %q, want the-code", cb.code)
	}
	if cb.state != "state-123" {
		t.Fatalf("state = %q, want state-123", cb.state)
	}
}

// TestBigModelInjectCallback verifies a manually supplied authorization code is
// delivered to a pending flow tagged with the flow's own OAuth state.
func TestBigModelInjectCallback(t *testing.T) {
	a := NewZAIAuth(nil, ProviderBigModel, "", 0)
	a.bmState = "flow-state"
	a.bmResult = make(chan bmCallback, 1)

	a.InjectCallback("code-x")
	select {
	case cb := <-a.bmResult:
		if cb.code != "code-x" || cb.state != "flow-state" {
			t.Fatalf("unexpected callback: %+v", cb)
		}
	default:
		t.Fatal("InjectCallback should deliver the code with the flow state")
	}

	// Empty code is a no-op; an inactive flow must not panic.
	a.InjectCallback("")
	(&ZAIAuth{}).InjectCallback("x")
}

// TestBigModelInjectError fails a pending flow with the provider error instead of
// leaving it to hang until the authorization timeout.
func TestBigModelInjectError(t *testing.T) {
	a := NewZAIAuth(nil, ProviderBigModel, "", 0)
	a.bmState = "flow-state"
	a.bmResult = make(chan bmCallback, 1)

	a.InjectError("access_denied")
	select {
	case cb := <-a.bmResult:
		if cb.err == nil || !strings.Contains(cb.err.Error(), "access_denied") {
			t.Fatalf("expected an error carrying the provider message, got %+v", cb)
		}
	default:
		t.Fatal("InjectError should have delivered an error to the flow")
	}

	// Empty message falls back to a default; an inactive flow must not panic.
	a.InjectError("")
	(&ZAIAuth{}).InjectError("x")
}

// TestBigModelCallbackSurfacesError verifies a denied/errored loopback callback is
// reported with the provider's error rather than a generic missing-code message.
func TestBigModelCallbackSurfacesError(t *testing.T) {
	a := NewZAIAuth(nil, ProviderBigModel, "", 0)
	a.bmResult = make(chan bmCallback, 1)

	req := httptest.NewRequest("GET", "/callback?error=access_denied&state=s", nil)
	a.handleBigModelCallback(httptest.NewRecorder(), req)

	cb := <-a.bmResult
	if cb.err == nil || !strings.Contains(cb.err.Error(), "access_denied") {
		t.Fatalf("expected the callback to surface the OAuth error, got %+v", cb)
	}
}

// TestBigModelCallbackRejectsMissingCode verifies a callback without an authCode
// is reported as an error rather than silently accepted.
func TestBigModelCallbackRejectsMissingCode(t *testing.T) {
	a := NewZAIAuth(nil, ProviderBigModel, "", 0)
	a.bmResult = make(chan bmCallback, 1)

	req := httptest.NewRequest("GET", "/callback?state=only-state", nil)
	a.handleBigModelCallback(httptest.NewRecorder(), req)

	cb := <-a.bmResult
	if cb.err == nil {
		t.Fatal("expected an error for a callback missing authCode")
	}
}
