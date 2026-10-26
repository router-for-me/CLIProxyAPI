package kiro

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestKiroSocialLoginExchange(t *testing.T) {
	idPayload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"user@example.com"}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "KiroIDE-") {
			t.Errorf("user agent: %q", r.Header.Get("User-Agent"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["code"] != "auth-code" || body["code_verifier"] == "" || !strings.HasSuffix(body["redirect_uri"], "/oauth/callback?login_option=github") {
			t.Errorf("token body: %v", body)
		}
		_, _ = io.WriteString(w, `{"accessToken":"access","refreshToken":"refresh","expiresIn":3600,"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/p","idToken":"h.`+idPayload+`.s"}`)
	}))
	defer server.Close()

	callbacks := make(chan SocialCallback, 1)
	client := &Client{HTTP: server.Client(), AuthURL: server.URL}
	login, err := client.StartSocialLogin("Github", "state-1", func(callback SocialCallback) { callbacks <- callback })
	if err != nil {
		t.Skipf("no registered callback port available: %v", err)
	}
	defer login.Close()

	authURL, err := url.Parse(login.AuthURL)
	if err != nil {
		t.Fatal(err)
	}
	query := authURL.Query()
	if authURL.Path != "/login" || query.Get("idp") != "Github" || query.Get("state") != "state-1" || query.Get("code_challenge_method") != "S256" || query.Get("redirect_uri") != login.RedirectURI {
		t.Fatalf("auth url: %s", login.AuthURL)
	}

	callbackURL := strings.Replace(login.RedirectURI, "localhost", "127.0.0.1", 1) + "&code=auth-code&state=state-1"
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	callback := <-callbacks
	if callback.Code != "auth-code" || callback.State != "state-1" {
		t.Fatalf("callback: %+v", callback)
	}

	token, err := client.ExchangeSocialCode(context.Background(), login, callback.Code)
	if err != nil {
		t.Fatal(err)
	}
	metadata := SocialTokenMetadata(token, login)
	if metadata["provider"] != "Github" || metadata["auth_method"] != "social" || metadata["email"] != "user@example.com" ||
		metadata["refresh_token"] != "refresh" || metadata["profile_arn"] == nil || metadata["machine_id"] != login.MachineID || String(metadata, "expired") == "" {
		t.Fatalf("metadata: %v", metadata)
	}
	if metadata["client_id"] != nil {
		t.Fatalf("social credential must not carry OIDC client fields")
	}
}

func TestSocialProvider(t *testing.T) {
	for method, want := range map[string]string{"google": "Google", "GitHub": "Github"} {
		if got, ok := SocialProvider(method); !ok || got != want {
			t.Fatalf("%s: %q %v", method, got, ok)
		}
	}
	if _, ok := SocialProvider("builderid"); ok {
		t.Fatal("builderid is not social")
	}
}
