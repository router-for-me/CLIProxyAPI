package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestFileTokenStore_PersistsAndRestoresRuntimeState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	nextRetry := time.Now().UTC().Add(15 * time.Minute).Round(time.Second)
	nextRecover := nextRetry.Add(30 * time.Second)

	auth := &cliproxyauth.Auth{
		ID:          "sample.json",
		FileName:    "sample.json",
		Provider:    "codex",
		Status:      cliproxyauth.StatusError,
		Unavailable: true,
		Quota: cliproxyauth.QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: nextRecover,
			BackoffLevel:  2,
		},
		LastError: &cliproxyauth.Error{
			Code:       "rate_limit",
			Message:    "usage_limit_reached",
			HTTPStatus: 429,
		},
		NextRetryAfter: nextRetry,
		ModelStates: map[string]*cliproxyauth.ModelState{
			"gpt-5.3-codex": {
				Status:         cliproxyauth.StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				LastError: &cliproxyauth.Error{
					Code:       "rate_limit",
					Message:    "usage_limit_reached",
					HTTPStatus: 429,
				},
				Quota: cliproxyauth.QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRecover,
					BackoffLevel:  3,
				},
				UpdatedAt: time.Now().UTC().Round(time.Second),
			},
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "example@example.com",
		},
		Attributes: map[string]string{"path": filepath.Join(dir, "sample.json")},
	}

	savedPath, errSave := store.Save(context.Background(), auth)
	if errSave != nil {
		t.Fatalf("Save() error: %v", errSave)
	}
	if savedPath == "" {
		t.Fatalf("Save() returned empty path")
	}

	raw, errRead := os.ReadFile(savedPath)
	if errRead != nil {
		t.Fatalf("ReadFile() error: %v", errRead)
	}
	var persisted map[string]any
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("unmarshal persisted JSON: %v", errUnmarshal)
	}
	if _, ok := persisted["model_states"]; !ok {
		t.Fatalf("persisted metadata missing model_states")
	}
	if _, ok := persisted["next_retry_after"]; !ok {
		t.Fatalf("persisted metadata missing next_retry_after")
	}
	if _, ok := persisted["quota"]; !ok {
		t.Fatalf("persisted metadata missing quota")
	}

	entries, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error: %v", errList)
	}
	if len(entries) != 1 {
		t.Fatalf("List() entries=%d, want 1", len(entries))
	}

	got := entries[0]
	if got.Status != cliproxyauth.StatusError {
		t.Fatalf("Status=%q, want %q", got.Status, cliproxyauth.StatusError)
	}
	if !got.Unavailable {
		t.Fatalf("Unavailable=false, want true")
	}
	if got.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter is zero, want persisted value")
	}
	if !got.Quota.Exceeded || got.Quota.Reason != "quota" {
		t.Fatalf("Quota=%+v, want exceeded quota", got.Quota)
	}
	state := got.ModelStates["gpt-5.3-codex"]
	if state == nil {
		t.Fatalf("model state missing")
	}
	if !state.Unavailable {
		t.Fatalf("model state unavailable=false, want true")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("model state NextRetryAfter is zero")
	}
	if state.Quota.Reason != "quota" || !state.Quota.Exceeded {
		t.Fatalf("model state quota=%+v, want exceeded quota", state.Quota)
	}
}

