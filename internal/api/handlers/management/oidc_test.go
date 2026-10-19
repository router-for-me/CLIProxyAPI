package management

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/oauth2"
)

func testManagementOIDC() *managementOIDC {
	return &managementOIDC{
		issuer:   "https://issuer.example",
		clientID: "management",
		redirect: "https://admin.example/v8/management/auth/oidc/callback",
		origin:   "https://admin.example",
		subjects: map[string]struct{}{"owner": {}},
		flows:    make(map[string]oidcFlow),
		sessions: make(map[string]oidcSession),
	}
}

func managementOIDCRequest(handler gin.HandlerFunc, method, target string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(method, target, nil)
	ctx.Request.RemoteAddr = "127.0.0.1:1234"
	for _, cookie := range cookies {
		ctx.Request.AddCookie(cookie)
	}
	for key, value := range headers {
		ctx.Request.Header.Set(key, value)
	}
	handler(ctx)
	ctx.Writer.WriteHeaderNow()
	return response
}

func TestManagementOIDCAuthenticationBoundary(t *testing.T) {
	o := testManagementOIDC()
	o.sessions["valid"] = oidcSession{subject: "owner", expires: time.Now().Add(time.Hour)}
	h := &Handler{oidc: o, cfg: &config.Config{}, failedAttempts: make(map[string]*attemptInfo)}
	cookie := []*http.Cookie{{Name: managementOIDCSessionCookie, Value: "valid"}}
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		cookies []*http.Cookie
		headers map[string]string
		status  int
	}{
		{name: "same-origin v8 request", method: http.MethodGet, path: "/v8/management/config", cookies: cookie, headers: map[string]string{"X-Management-CSRF": "1", "Origin": o.origin}, status: http.StatusOK},
		{name: "missing csrf header", method: http.MethodGet, path: "/v8/management/config", cookies: cookie, status: http.StatusForbidden},
		{name: "foreign origin", method: http.MethodPost, path: "/v8/management/config", cookies: cookie, headers: map[string]string{"X-Management-CSRF": "1", "Origin": "https://evil.example"}, status: http.StatusForbidden},
		{name: "cross-site fetch", method: http.MethodGet, path: "/v8/management/config", cookies: cookie, headers: map[string]string{"X-Management-CSRF": "1", "Origin": o.origin, "Sec-Fetch-Site": "cross-site"}, status: http.StatusForbidden},
		{name: "spoofed identity header", method: http.MethodGet, path: "/v8/management/config", headers: map[string]string{"X-Authentik-Uid": "owner"}, status: http.StatusUnauthorized},
		{name: "unknown cookie", method: http.MethodGet, path: "/v8/management/config", cookies: []*http.Cookie{{Name: managementOIDCSessionCookie, Value: "fake"}}, headers: map[string]string{"X-Management-CSRF": "1"}, status: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := managementOIDCRequest(h.Middleware(), tc.method, "https://admin.example"+tc.path, tc.cookies, tc.headers)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
		})
	}

	// The OIDC cookie applies only to v8. Explicit management keys keep their existing behavior.
	h.envSecret = "key"
	h.allowRemoteOverride = true
	for _, tc := range []struct {
		name, key string
		path      string
		status    int
	}{
		{name: "valid key", key: "key", path: "/v8/management/config", status: http.StatusOK},
		{name: "invalid key does not fall back to cookie", key: "wrong", path: "/v8/management/config", status: http.StatusUnauthorized},
		{name: "v0 does not accept oidc cookie", path: "/v0/management/config", status: http.StatusUnauthorized},
	} {
		response := managementOIDCRequest(h.Middleware(), http.MethodGet, "https://admin.example"+tc.path, cookie, map[string]string{"Authorization": "Bearer " + tc.key})
		if response.Code != tc.status {
			t.Fatalf("%s: status=%d want=%d body=%s", tc.name, response.Code, tc.status, response.Body.String())
		}
	}

	o.sessions["expired"] = oidcSession{subject: "owner", expires: time.Now().Add(-time.Second)}
	response := managementOIDCRequest(h.ManagementOIDCSession, http.MethodGet, "/", []*http.Cookie{{Name: managementOIDCSessionCookie, Value: "expired"}}, nil)
	if !strings.Contains(response.Body.String(), `"authenticated":false`) {
		t.Fatalf("expired session was accepted: %s", response.Body.String())
	}
}

func TestManagementOIDCSameOriginNormalizesHostAndDefaultPort(t *testing.T) {
	o := testManagementOIDC()
	response := managementOIDCRequest(gin.HandlerFunc(func(c *gin.Context) {
		if !o.sameOrigin(c) {
			c.Status(http.StatusForbidden)
			return
		}
		c.Status(http.StatusNoContent)
	}), http.MethodGet, "https://admin.example/v8/management/config", nil, map[string]string{
		"Origin": "https://ADMIN.example:443", "X-Management-CSRF": "1",
	})
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-origin request with canonicalized Host/default port was rejected: %d", response.Code)
	}
}

func TestManagementOIDCConfiguration(t *testing.T) {
	t.Setenv("MANAGEMENT_OIDC_ISSUER", "https://issuer.example/realms/ducktape")
	t.Setenv("MANAGEMENT_OIDC_CLIENT_ID", "management")
	t.Setenv("MANAGEMENT_OIDC_CLIENT_SECRET", "")
	t.Setenv("MANAGEMENT_OIDC_REDIRECT_URL", "https://admin.example/v8/management/auth/oidc/callback")
	t.Setenv("MANAGEMENT_OIDC_ALLOWED_SUBJECTS", " owner,second-owner ")
	o, err := newManagementOIDC()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := o.subjects["owner"]; !ok || len(o.subjects) != 2 {
		t.Fatalf("unexpected subject allowlist: %#v", o.subjects)
	}

	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "issuer scheme", key: "MANAGEMENT_OIDC_ISSUER", value: "http://issuer.example"},
		{name: "client id", key: "MANAGEMENT_OIDC_CLIENT_ID", value: " "},
		{name: "redirect path", key: "MANAGEMENT_OIDC_REDIRECT_URL", value: "https://admin.example/wrong"},
		{name: "redirect credentials", key: "MANAGEMENT_OIDC_REDIRECT_URL", value: "https://user:pass@admin.example/v8/management/auth/oidc/callback"},
		{name: "empty subject allowlist", key: "MANAGEMENT_OIDC_ALLOWED_SUBJECTS", value: " , "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := newManagementOIDC(); err == nil {
				t.Fatal("accepted invalid OIDC configuration")
			}
		})
	}
}

func TestManagementOIDCLoginAndCallback(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var issuer, rawToken, expectedVerifier string
	tokenCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
				Key: &key.PublicKey, Algorithm: "RS256", Use: "sig", KeyID: "test-key",
			}}})
		case "/token":
			tokenCalls++
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("code_verifier") != expectedVerifier {
				t.Error("token exchange did not include the PKCE verifier")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "unused", "token_type": "Bearer", "id_token": rawToken,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	provider, err := oidc.NewProvider(context.Background(), issuer)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, subject, nonce, audience, tokenIssuer, state string
		expired                                            bool
		status                                             int
	}{
		{name: "authorized", subject: "owner", nonce: "nonce", audience: "management", tokenIssuer: issuer, state: "state", status: http.StatusSeeOther},
		{name: "invalid signature", subject: "owner", nonce: "nonce", audience: "management", tokenIssuer: issuer, state: "state", status: http.StatusUnauthorized},
		{name: "subject is not allowlisted", subject: "other", nonce: "nonce", audience: "management", tokenIssuer: issuer, state: "state", status: http.StatusForbidden},
		{name: "wrong nonce", subject: "owner", nonce: "wrong", audience: "management", tokenIssuer: issuer, state: "state", status: http.StatusUnauthorized},
		{name: "wrong audience", subject: "owner", nonce: "nonce", audience: "other", tokenIssuer: issuer, state: "state", status: http.StatusUnauthorized},
		{name: "wrong issuer", subject: "owner", nonce: "nonce", audience: "management", tokenIssuer: "https://evil.example", state: "state", status: http.StatusUnauthorized},
		{name: "expired token", subject: "owner", nonce: "nonce", audience: "management", tokenIssuer: issuer, state: "state", expired: true, status: http.StatusUnauthorized},
		{name: "wrong state", subject: "owner", nonce: "nonce", audience: "management", tokenIssuer: issuer, state: "other", status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testManagementOIDC()
			o.provider = provider
			o.oauth = oauth2.Config{ClientID: o.clientID, Endpoint: provider.Endpoint(), RedirectURL: o.redirect}
			expectedVerifier = oauth2.GenerateVerifier()
			o.flows["binding"] = oidcFlow{state: "state", nonce: "nonce", verifier: expectedVerifier, expires: time.Now().Add(time.Minute)}
			expires := time.Now().Add(time.Hour)
			if tc.expired {
				expires = time.Now().Add(-time.Hour)
			}
			payload, err := json.Marshal(map[string]any{
				"iss": tc.tokenIssuer, "sub": tc.subject, "aud": tc.audience,
				"exp": expires.Unix(), "iat": time.Now().Unix(), "nonce": tc.nonce,
			})
			if err != nil {
				t.Fatal(err)
			}
			signed, err := signer.Sign(payload)
			if err != nil {
				t.Fatal(err)
			}
			rawToken, err = signed.CompactSerialize()
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "invalid signature" {
				rawToken += "x"
			}

			h := &Handler{oidc: o}
			cookie := []*http.Cookie{{Name: managementOIDCFlowCookie, Value: "binding"}}
			before := tokenCalls
			response := managementOIDCRequest(h.ManagementOIDCCallback, http.MethodGet, "https://admin.example/v8/management/auth/oidc/callback?state="+tc.state+"&code=code", cookie, nil)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
			}
			if tc.state != "state" && tokenCalls != before {
				t.Fatal("exchanged a code before checking state")
			}
			replay := managementOIDCRequest(h.ManagementOIDCCallback, http.MethodGet, "https://admin.example/v8/management/auth/oidc/callback?state=state&code=code", cookie, nil)
			if replay.Code != http.StatusBadRequest {
				t.Fatalf("accepted callback replay: %d", replay.Code)
			}

			if tc.status == http.StatusSeeOther {
				var sessionCookie *http.Cookie
				for _, responseCookie := range response.Result().Cookies() {
					if responseCookie.Name == managementOIDCSessionCookie {
						sessionCookie = responseCookie
					}
				}
				if sessionCookie == nil || !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.Path != "/" || sessionCookie.Domain != "" {
					t.Fatal("session cookie does not use the required host-only security attributes")
				}
				blocked := managementOIDCRequest(h.ManagementOIDCLogout, http.MethodPost, "/", []*http.Cookie{sessionCookie}, nil)
				if blocked.Code != http.StatusForbidden || len(o.sessions) != 1 {
					t.Fatal("logout accepted a cross-site request")
				}
				logout := managementOIDCRequest(h.ManagementOIDCLogout, http.MethodPost, "/", []*http.Cookie{sessionCookie}, map[string]string{
					"X-Management-CSRF": "1", "Origin": o.origin,
				})
				if logout.Code != http.StatusNoContent || len(o.sessions) != 0 {
					t.Fatal("logout did not revoke the browser session")
				}
			} else if len(o.sessions) != 0 {
				t.Fatal("unauthorized login created a session")
			}
		})
	}

	o := testManagementOIDC()
	o.provider = provider
	o.oauth = oauth2.Config{ClientID: o.clientID, Endpoint: provider.Endpoint(), RedirectURL: o.redirect}
	h := &Handler{oidc: o}
	response := managementOIDCRequest(h.ManagementOIDCLogin, http.MethodGet, "https://admin.example/v8/management/auth/oidc/login", nil, nil)
	redirect, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusFound || redirect.Query().Get("code_challenge_method") != "S256" || redirect.Query().Get("nonce") == "" || redirect.Query().Get("state") == "" {
		t.Fatal("login redirect is missing PKCE, state, or nonce")
	}
	var flowCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == managementOIDCFlowCookie && cookie.Secure && cookie.HttpOnly && cookie.SameSite == http.SameSiteLaxMode {
			flowCookie = cookie
		}
	}
	if flowCookie == nil {
		t.Fatal("login binding cookie does not use secure attributes")
	}

	response = managementOIDCRequest(h.ManagementOIDCLogin, http.MethodGet, "https://admin.example/v8/management/auth/oidc/login", []*http.Cookie{flowCookie}, nil)
	if response.Code != http.StatusFound || len(o.flows) != 1 {
		t.Fatal("restarting login did not replace the browser's previous transaction")
	}
	if _, ok := o.flows[flowCookie.Value]; ok {
		t.Fatal("restarting login retained the superseded transaction")
	}

	// Model a store filled with abandoned unauthenticated requests. Admission must
	// stay bounded, evict the oldest attempt, and let a fresh browser finish login.
	clear(o.flows)
	now := time.Now()
	for i := 0; i < managementOIDCMaxEntries; i++ {
		o.flows[fmt.Sprintf("flow-%d", i)] = oidcFlow{expires: now.Add(time.Minute + time.Duration(i))}
	}
	for i := 0; i < 3; i++ {
		response = managementOIDCRequest(h.ManagementOIDCLogin, http.MethodGet, "https://admin.example/v8/management/auth/oidc/login", nil, nil)
		if response.Code != http.StatusFound || len(o.flows) != managementOIDCMaxEntries {
			t.Fatalf("saturated login status=%d entries=%d", response.Code, len(o.flows))
		}
		if _, ok := o.flows[fmt.Sprintf("flow-%d", i)]; ok {
			t.Fatal("saturated login did not evict the oldest pending transaction")
		}
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == managementOIDCFlowCookie {
			flowCookie = cookie
		}
	}
	flow, ok := o.flows[flowCookie.Value]
	if !ok {
		t.Fatal("fresh login transaction was not retained")
	}
	expectedVerifier = flow.verifier
	payload, err := json.Marshal(map[string]any{
		"iss": issuer, "sub": "owner", "aud": o.clientID,
		"exp": time.Now().Add(time.Hour).Unix(), "nonce": flow.nonce,
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	rawToken, err = signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	response = managementOIDCRequest(h.ManagementOIDCCallback, http.MethodGet, o.redirect+"?state="+flow.state+"&code=code", []*http.Cookie{flowCookie}, nil)
	if response.Code != http.StatusSeeOther || len(o.sessions) != 1 {
		t.Fatalf("fresh login could not complete after saturation: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManagementOIDCCallbackRequiresBrowserBinding(t *testing.T) {
	for _, tc := range []struct {
		name, cookie string
		expired      bool
	}{
		{name: "missing cookie"},
		{name: "tampered cookie", cookie: "other"},
		{name: "expired transaction", cookie: "binding", expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testManagementOIDC()
			expires := time.Now().Add(time.Minute)
			if tc.expired {
				expires = time.Now().Add(-time.Minute)
			}
			o.flows["binding"] = oidcFlow{state: "state", expires: expires}
			h := &Handler{oidc: o}
			response := managementOIDCRequest(h.ManagementOIDCCallback, http.MethodGet, "/v8/management/auth/oidc/callback?state=state&code=code", []*http.Cookie{{Name: managementOIDCFlowCookie, Value: tc.cookie}}, nil)
			if response.Code != http.StatusBadRequest || len(o.sessions) != 0 {
				t.Fatal("accepted an unbound or expired callback")
			}
		})
	}
}
