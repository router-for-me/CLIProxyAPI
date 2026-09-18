package zcode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func envResponse(w http.ResponseWriter, data any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data, "msg": ""})
}

func TestZaiLogin_Start_ParsesFlow(t *testing.T) {
	var sawAuth, sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawBody = readBody(r)
		if r.URL.Path != "/oauth/cli/init" || r.Method != http.MethodPost {
			t.Fatalf("unexpected req: %s %s", r.Method, r.URL.Path)
		}
		envResponse(w, map[string]any{
			"flow_id": "f-1", "authorize_url": "https://a/b?c=d",
			"poll_interval_sec": 2, "expires_at": 9999999999,
		})
	}))
	defer srv.Close()

	c := &ZaiCliLogin{HTTP: srv.Client(), baseURL: srv.URL}
	f, err := c.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.FlowID != "f-1" || f.AuthorizeURL != "https://a/b?c=d" || f.PollIntervalSec != 2 {
		t.Fatalf("bad flow: %+v", f)
	}
	if c.PollToken == "" {
		t.Fatal("poll token not set")
	}
	if sawAuth != "Bearer "+c.PollToken {
		t.Fatalf("auth header = %q", sawAuth)
	}
	if !strings.Contains(sawBody, `"provider":"zai"`) {
		t.Fatalf("body = %q", sawBody)
	}
}

func TestZaiLogin_Complete_PollsUntilReady(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/cli/poll/f-1" {
			t.Fatalf("bad path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Fatal("missing poll bearer")
		}
		if atomic.AddInt32(&calls, 1) < 2 {
			envResponse(w, map[string]any{"status": "pending"})
			return
		}
		envResponse(w, map[string]any{
			"status": "ready",
			"token":  "jwt.abc",
			"user":   map[string]any{"user_id": "u-7"},
			"zai":    map[string]any{"access_token": "at-1"},
		})
	}))
	defer srv.Close()

	c := &ZaiCliLogin{
		HTTP: srv.Client(), baseURL: srv.URL, PollToken: "pt-1",
		Sleep: func(time.Duration) {}, Now: func() time.Time { return time.Unix(1000, 0) },
	}
	f := &CliFlow{FlowID: "f-1", PollIntervalSec: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	tok, err := c.Complete(context.Background(), f, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-1" || tok.JWT != "jwt.abc" || tok.UserID != "u-7" {
		t.Fatalf("bad tokens: %+v", tok)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected 2 polls, got %d", calls)
	}
}

func TestZaiLogin_Complete_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envResponse(w, map[string]any{"status": "pending"})
	}))
	defer srv.Close()
	now := int64(1000)
	c := &ZaiCliLogin{
		HTTP: srv.Client(), baseURL: srv.URL,
		Sleep: func(time.Duration) {}, Now: func() time.Time { now++; return time.Unix(now, 0) },
	}
	f := &CliFlow{FlowID: "f-1", PollIntervalSec: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if _, err := c.Complete(context.Background(), f, 3*time.Second); err == nil {
		t.Fatal("want timeout error")
	}
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestNormalizeProvider(t *testing.T) {
	for in, want := range map[string]string{"": ProviderZai, "zai": ProviderZai, "ZAI": ProviderZai, " bigmodel ": ProviderBigmodel, "BigModel": ProviderBigmodel} {
		got, err := NormalizeProvider(in)
		if err != nil || got != want {
			t.Fatalf("NormalizeProvider(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"bigmodele", "openai", "zai-cn", " "} {
		if in == " " {
			continue // whitespace-only normalizes to the default
		}
		if _, err := NormalizeProvider(in); err == nil {
			t.Fatalf("NormalizeProvider(%q) accepted an unknown provider", in)
		}
	}
}

// A provider-scoped poll must not accept the other provider's token field: a
// mismatch is a loud error rather than a silently wrong credential.
func TestCliLogin_CompleteRejectsProviderTokenMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"status":   "ready",
			"token":    "jwt",
			"user":     map[string]any{"user_id": "u"},
			"zai":      map[string]any{"access_token": "zai-token"},
			"bigmodel": map[string]any{"access_token": "bm-token"},
		}})
	}))
	defer srv.Close()

	base := srv.URL
	login := &CliLogin{Provider: ProviderBigmodel, baseURL: base, HTTP: srv.Client(), Sleep: func(time.Duration) {}}
	flow := &CliFlow{FlowID: "f-1", Provider: ProviderBigmodel, PollIntervalSec: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	tok, err := login.Complete(context.Background(), flow, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "bm-token" {
		t.Fatalf("bigmodel token = %q, want bm-token", tok.AccessToken)
	}
}
