package home

import (
	"encoding/json"
	"testing"
)

// Home versions that predate the structured session object read session_id,
// so both representations have to travel together.
func TestAuthDispatchRequestCarriesStructuredSessionAndLegacyID(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", DispatchSession{
		ID:             "pck:cache-1",
		Aliases:        []string{"conv:conv_1"},
		Source:         "prompt_cache_key",
		Confidence:     "medium",
		Scope:          "session",
		ClientType:     "codex",
		ThreadID:       "thread-1",
		ParentThreadID: "thread-0",
		ClientProvided: true,
	}, nil, 1)

	raw, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal auth dispatch request: %v", err)
	}
	var payload struct {
		SessionID string `json:"session_id"`
		Session   *struct {
			ID             string   `json:"id"`
			Aliases        []string `json:"aliases"`
			Source         string   `json:"source"`
			Confidence     string   `json:"confidence"`
			Scope          string   `json:"scope"`
			ClientType     string   `json:"client_type"`
			ThreadID       string   `json:"thread_id"`
			ParentThreadID string   `json:"parent_thread_id"`
			ClientProvided bool     `json:"client_provided"`
		} `json:"session"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal auth dispatch request: %v", errUnmarshal)
	}

	if payload.SessionID != "pck:cache-1" {
		t.Fatalf("session_id = %q, want pck:cache-1", payload.SessionID)
	}
	if payload.Session == nil {
		t.Fatal("session object is missing")
	}
	if payload.Session.ID != "pck:cache-1" {
		t.Fatalf("session.id = %q, want pck:cache-1", payload.Session.ID)
	}
	if len(payload.Session.Aliases) != 1 || payload.Session.Aliases[0] != "conv:conv_1" {
		t.Fatalf("session.aliases = %#v, want [conv:conv_1]", payload.Session.Aliases)
	}
	if payload.Session.Source != "prompt_cache_key" || payload.Session.Confidence != "medium" ||
		payload.Session.Scope != "session" || payload.Session.ClientType != "codex" {
		t.Fatalf("session classification = %#v, want the identity CPA resolved", payload.Session)
	}
	if payload.Session.ThreadID != "thread-1" || payload.Session.ParentThreadID != "thread-0" {
		t.Fatalf("session thread fields = %#v, want thread-1/thread-0", payload.Session)
	}
	if !payload.Session.ClientProvided {
		t.Fatal("session.client_provided = false, want true")
	}
}

func TestAuthDispatchRequestOmitsSessionWhenUnresolved(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", DispatchSession{ClientType: "openai-sdk"}, nil, 1)

	raw, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal auth dispatch request: %v", err)
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal auth dispatch request: %v", errUnmarshal)
	}
	if _, exists := payload["session"]; exists {
		t.Fatalf("session object was sent without a resolved session: %v", payload["session"])
	}
	if _, exists := payload["session_id"]; exists {
		t.Fatalf("session_id was sent without a resolved session: %v", payload["session_id"])
	}
}
