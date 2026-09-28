package jevclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCallSendsExpectedRequestAndParsesChoice(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		decodeJSON(t, r, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model": "jev-1.13.0",
			"answers": {"tier": {
				"type": "choice", "choice": "complex",
				"probabilities": {"simple":0.05,"medium":0.18,"complex":0.60,"reasoning":0.17},
				"confidence": 0.72
			}},
			"usage": {"input_tokens": 1480, "output_tokens": 0}
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "sk-ts-test", srv.Client())
	resp, err := c.Call(context.Background(), "jev-1.13.0",
		map[string]any{"latest_user_message": "refactor this"},
		map[string]Question{"tier": {Type: "choice", Instructions: "pick a tier"}})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if gotPath != "/v1/systemone" {
		t.Errorf("path = %q, want /v1/systemone", gotPath)
	}
	if gotAuth != "Bearer sk-ts-test" {
		t.Errorf("auth = %q, want Bearer sk-ts-test", gotAuth)
	}
	if gotBody["model"] != "jev-1.13.0" {
		t.Errorf("model = %v, want jev-1.13.0", gotBody["model"])
	}
	if _, ok := gotBody["questions"].(map[string]any)["tier"]; !ok {
		t.Errorf("questions missing tier key: %v", gotBody["questions"])
	}

	if resp.Answers["tier"].Choice != "complex" {
		t.Errorf("choice = %q, want complex", resp.Answers["tier"].Choice)
	}
	if resp.Answers["tier"].Confidence != 0.72 {
		t.Errorf("confidence = %v, want 0.72", resp.Answers["tier"].Confidence)
	}
	if resp.Usage.InputTokens != 1480 {
		t.Errorf("input_tokens = %d, want 1480", resp.Usage.InputTokens)
	}
}

func TestCallReturnsStatusErrorOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "nope", srv.Client())
	_, err := c.Call(context.Background(), "jev-1.13.0", "state", nil)
	if err == nil {
		t.Fatal("Call: want error, got nil")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error type = %T, want *StatusError", err)
	}
	if se.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", se.StatusCode)
	}
}

func TestNewDefaultsBaseURL(t *testing.T) {
	c := New("", "k", nil)
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
}

func decodeJSON(t *testing.T, r *http.Request, into any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(into); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
}

// A key rotated at runtime must be the one the next request carries, and the
// rotation must be safe against concurrent calls under -race.
func TestSetAPIKeyRotatesCredential(t *testing.T) {
	var gotAuth []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{},"usage":{}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "first-key", srv.Client())
	if got := c.APIKey(); got != "first-key" {
		t.Fatalf("APIKey() = %q, want first-key", got)
	}
	if _, errFirst := c.Call(context.Background(), "jev-1.13.0", nil, nil); errFirst != nil {
		t.Fatalf("first call: %v", errFirst)
	}

	c.SetAPIKey("second-key")
	if _, errSecond := c.Call(context.Background(), "jev-1.13.0", nil, nil); errSecond != nil {
		t.Fatalf("second call: %v", errSecond)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"Bearer first-key", "Bearer second-key"}
	if len(gotAuth) != 2 || gotAuth[0] != want[0] || gotAuth[1] != want[1] {
		t.Errorf("authorization headers = %v, want %v", gotAuth, want)
	}
}

func TestSetAPIKeyNilClientIsSafe(t *testing.T) {
	var c *Client
	c.SetAPIKey("ignored") // must not panic
	if got := c.APIKey(); got != "" {
		t.Errorf("APIKey() on a nil client = %q, want empty", got)
	}
}
