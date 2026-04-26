package auth

import (
	"testing"
	"time"
)

func TestCloneAuthForExecution_CodexUsesShallowClone(t *testing.T) {
	state := &ModelState{Status: StatusActive}
	auth := &Auth{
		ID:         "codex-auth",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "k"},
		Metadata:   map[string]any{"account_id": "a"},
		ModelStates: map[string]*ModelState{
			"gpt-5-codex": state,
		},
	}

	cloned := cloneAuthForExecution("codex", auth)
	if cloned == nil {
		t.Fatal("cloneAuthForExecution returned nil")
	}
	if cloned == auth {
		t.Fatal("expected a distinct auth struct copy")
	}
	cloned.Attributes["region"] = "us"
	if auth.Attributes["region"] != "us" {
		t.Fatal("expected codex execution clone to reuse Attributes map")
	}
	cloned.Metadata["plan"] = "plus"
	if auth.Metadata["plan"] != "plus" {
		t.Fatal("expected codex execution clone to reuse Metadata map")
	}
	cloned.ModelStates["gpt-5-codex"] = &ModelState{Status: StatusError}
	if auth.ModelStates["gpt-5-codex"].Status != StatusError {
		t.Fatal("expected codex execution clone to reuse ModelStates map")
	}
}

func TestCloneAuthForExecution_NonCodexDeepClonesMaps(t *testing.T) {
	state := &ModelState{Status: StatusActive}
	auth := &Auth{
		ID:         "gemini-auth",
		Provider:   "gemini",
		Attributes: map[string]string{"priority": "10"},
		Metadata:   map[string]any{"email": "x@example.com"},
		ModelStates: map[string]*ModelState{
			"gemini-2.5-pro": state,
		},
	}

	cloned := cloneAuthForExecution("gemini", auth)
	if cloned == nil {
		t.Fatal("cloneAuthForExecution returned nil")
	}
	cloned.Attributes["priority"] = "20"
	if auth.Attributes["priority"] != "10" {
		t.Fatal("expected non-codex execution clone to deep copy Attributes map")
	}
	cloned.Metadata["email"] = "y@example.com"
	if auth.Metadata["email"] != "x@example.com" {
		t.Fatal("expected non-codex execution clone to deep copy Metadata map")
	}
	cloned.ModelStates["gemini-2.5-pro"] = &ModelState{Status: StatusError}
	if auth.ModelStates["gemini-2.5-pro"] != state {
		t.Fatal("expected non-codex execution clone to deep copy ModelStates map")
	}
	if cloned.ModelStates["gemini-2.5-pro"] == state {
		t.Fatal("expected non-codex execution clone to deep copy model state")
	}
}

func TestAuthCloneForScheduler_MinimizesMutableMaps(t *testing.T) {
	auth := &Auth{
		ID:             "sched-auth",
		Provider:       "gemini-cli",
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: parseMustTime(t, "2026-04-26T12:00:00Z"),
		Attributes: map[string]string{
			"priority":              "10",
			"gemini_virtual_parent": "parent-a",
			"websockets":            "true",
			"api_key":               "secret",
		},
		Metadata: map[string]any{
			"websockets": true,
			"email":      "x@example.com",
		},
		ModelStates: map[string]*ModelState{
			"gemini-2.5-pro": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: parseMustTime(t, "2026-04-26T12:05:00Z"),
				Quota:          QuotaState{Exceeded: true, BackoffLevel: 2},
				LastError:      &Error{Message: "boom"},
			},
		},
		LastError: &Error{Message: "top-level"},
	}

	cloned := auth.CloneForScheduler()
	if cloned == nil {
		t.Fatal("CloneForScheduler returned nil")
	}
	if cloned.Attributes["priority"] != "10" || cloned.Attributes["gemini_virtual_parent"] != "parent-a" || cloned.Attributes["websockets"] != "true" {
		t.Fatalf("CloneForScheduler attributes = %#v", cloned.Attributes)
	}
	if _, ok := cloned.Attributes["api_key"]; ok {
		t.Fatalf("CloneForScheduler should drop unused attributes, got %#v", cloned.Attributes)
	}
	if cloned.Metadata["websockets"] != true {
		t.Fatalf("CloneForScheduler metadata = %#v", cloned.Metadata)
	}
	if _, ok := cloned.Metadata["email"]; ok {
		t.Fatalf("CloneForScheduler should drop unused metadata, got %#v", cloned.Metadata)
	}
	if cloned.LastError != nil {
		t.Fatalf("CloneForScheduler should clear LastError, got %#v", cloned.LastError)
	}
	state := cloned.ModelStates["gemini-2.5-pro"]
	if state == nil {
		t.Fatal("CloneForScheduler lost model state")
	}
	if state.LastError != nil {
		t.Fatalf("CloneForScheduler model state should clear LastError, got %#v", state.LastError)
	}
	state.Quota.BackoffLevel = 9
	if auth.ModelStates["gemini-2.5-pro"].Quota.BackoffLevel != 2 {
		t.Fatal("CloneForScheduler should deep copy model state quota")
	}
}

func TestAuthCloneForScheduler_CodexRetainsExecutionFields(t *testing.T) {
	auth := &Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":           "sk-test",
			"base_url":          "https://chatgpt.com/backend-api/codex",
			"plan_type":         "plus",
			"header:X-Test":     "1",
			"installation_id":   "install-1",
			"header:Originator": "codex_vscode",
			"header:User-Agent": "codex_vscode/1.0.0",
			"websockets":        "true",
		},
		Metadata: map[string]any{
			"access_token":    "oauth-token",
			"refresh_token":   "refresh-token",
			"account_id":      "acct-1",
			"installation_id": "install-1",
			"user_agent":      "codex_vscode/1.0.0",
			"originator":      "codex_vscode",
			"websockets":      true,
		},
		ModelStates: map[string]*ModelState{
			"gpt-5-codex": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: parseMustTime(t, "2026-04-26T12:05:00Z"),
				Quota:          QuotaState{Exceeded: true, BackoffLevel: 2},
				LastError:      &Error{Message: "boom"},
			},
		},
		LastError: &Error{Message: "top-level"},
	}

	cloned := auth.CloneForScheduler()
	if cloned == nil {
		t.Fatal("CloneForScheduler returned nil")
	}
	if cloned.Attributes["api_key"] != "sk-test" || cloned.Attributes["plan_type"] != "plus" || cloned.Attributes["header:X-Test"] != "1" {
		t.Fatalf("CloneForScheduler codex attributes = %#v", cloned.Attributes)
	}
	if cloned.Metadata["access_token"] != "oauth-token" || cloned.Metadata["account_id"] != "acct-1" || cloned.Metadata["websockets"] != true {
		t.Fatalf("CloneForScheduler codex metadata = %#v", cloned.Metadata)
	}
	if cloned.LastError != nil {
		t.Fatalf("CloneForScheduler should clear LastError, got %#v", cloned.LastError)
	}
	state := cloned.ModelStates["gpt-5-codex"]
	if state == nil {
		t.Fatal("CloneForScheduler lost codex model state")
	}
	if state.LastError != nil {
		t.Fatalf("CloneForScheduler codex model state should clear LastError, got %#v", state.LastError)
	}
	cloned.Attributes["api_key"] = "mutated"
	if auth.Attributes["api_key"] != "sk-test" {
		t.Fatal("CloneForScheduler should deep copy codex attributes")
	}
	cloned.Metadata["access_token"] = "mutated"
	if auth.Metadata["access_token"] != "oauth-token" {
		t.Fatal("CloneForScheduler should deep copy codex metadata")
	}
}

func BenchmarkCloneAuthForExecutionCodex(b *testing.B) {
	auth := &Auth{
		ID:         "codex-auth",
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "k", "base_url": "https://chatgpt.com/backend-api/codex"},
		Metadata: map[string]any{
			"account_id":   "acct",
			"originator":   "cli",
			"installation": "install",
		},
		ModelStates: map[string]*ModelState{
			"gpt-5-codex": {Status: StatusActive},
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cloned := cloneAuthForExecution("codex", auth)
		if cloned == nil {
			b.Fatal("cloneAuthForExecution returned nil")
		}
	}
}

func BenchmarkCloneAuthForScheduler(b *testing.B) {
	auth := &Auth{
		ID:          "sched-auth",
		Provider:    "gemini-cli",
		Status:      StatusError,
		Unavailable: true,
		Attributes: map[string]string{
			"priority":              "10",
			"gemini_virtual_parent": "parent-a",
			"websockets":            "true",
			"api_key":               "secret",
		},
		Metadata: map[string]any{
			"websockets": true,
			"email":      "x@example.com",
		},
		ModelStates: map[string]*ModelState{
			"gemini-2.5-pro": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: parseMustTime(b, "2026-04-26T12:05:00Z"),
				Quota:          QuotaState{Exceeded: true, BackoffLevel: 2},
				LastError:      &Error{Message: "boom"},
			},
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cloned := auth.CloneForScheduler()
		if cloned == nil {
			b.Fatal("CloneForScheduler returned nil")
		}
	}
}

type timeParserTB interface {
	Fatalf(string, ...any)
	Helper()
}

func parseMustTime(tb timeParserTB, raw string) time.Time {
	tb.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		tb.Fatalf("time.Parse(%q) error = %v", raw, err)
	}
	return parsed
}

func BenchmarkCloneAuthForExecutionGemini(b *testing.B) {
	auth := &Auth{
		ID:         "gemini-auth",
		Provider:   "gemini",
		Attributes: map[string]string{"priority": "10", "proxy_url": "http://127.0.0.1:7890"},
		Metadata: map[string]any{
			"email":      "x@example.com",
			"websockets": true,
		},
		ModelStates: map[string]*ModelState{
			"gemini-2.5-pro": {Status: StatusActive},
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cloned := cloneAuthForExecution("gemini", auth)
		if cloned == nil {
			b.Fatal("cloneAuthForExecution returned nil")
		}
	}
}
