package redisqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageJournalReplayRequiresAcknowledgement(t *testing.T) {
	j := &usageJournal{dir: t.TempDir()}
	if err := j.append([]byte(`{"event_id":"a","request_id":"same"}`)); err != nil {
		t.Fatal(err)
	}
	if err := j.append([]byte(`{"event_id":"b","request_id":"same"}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		items, err := j.read(100)
		if err != nil || len(items) != 2 {
			t.Fatalf("read %d: %d %v", i, len(items), err)
		}
	}
	restarted := &usageJournal{dir: j.dir}
	items, err := restarted.read(100)
	if err != nil || len(items) != 2 {
		t.Fatalf("restart: %d %v", len(items), err)
	}
	var event map[string]any
	json.Unmarshal(items[0], &event)
	if err := restarted.ack([]string{event["event_id"].(string)}); err != nil {
		t.Fatal(err)
	}
	items, err = restarted.read(100)
	if err != nil || len(items) != 1 {
		t.Fatalf("ack: %d %v", len(items), err)
	}
	if err := restarted.ack([]string{""}); err == nil {
		t.Fatal("empty event ID accepted")
	}
}

func TestUsageJournalOpaqueEventIDsRoundTrip(t *testing.T) {
	root := t.TempDir()
	j := &usageJournal{dir: filepath.Join(root, "journal")}
	outside := filepath.Join(root, "outside.json")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ids := []string{"usage:123", "evt.123", "../outside", `C:\event\123`, "Case", "case", "事件/123", "event\x00suffix", "CON", "NUL", "COM1", "LPT1", strings.Repeat("long-id:", 100)}
	for _, id := range ids {
		payload, err := json.Marshal(map[string]string{"event_id": id, "request_id": "same"})
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := j.append(payload); err != nil {
				t.Fatalf("opaque ID %q rejected: %v", id, err)
			}
		}
	}
	restarted := &usageJournal{dir: j.dir}
	items, err := restarted.read(100)
	if err != nil || len(items) != len(ids) {
		t.Fatalf("replay: got %d items, want %d: %v", len(items), len(ids), err)
	}
	seen := make(map[string]bool)
	for _, payload := range items {
		var event struct {
			EventID string `json:"event_id"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Fatal(err)
		}
		seen[event.EventID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("public ID changed: %q", id)
		}
		if err := restarted.ack([]string{id}); err != nil {
			t.Fatalf("ACK %q failed: %v", id, err)
		}
	}
	if items, err := restarted.read(100); err != nil || len(items) != 0 {
		t.Fatalf("ACK left %d items: %v", len(items), err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("opaque traversal ID changed an outside file: %q %v", data, err)
	}
}

func TestUsageJournalLegacyFilesRemainReplayableAndAcknowledged(t *testing.T) {
	j := &usageJournal{dir: t.TempDir()}
	legacy := []byte(`{"event_id":"Legacy-ID","request_id":"legacy"}`)
	if err := os.WriteFile(filepath.Join(j.dir, "Legacy-ID.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.append(legacy); err != nil {
		t.Fatal(err)
	}
	if items, err := j.read(10); err != nil || len(items) != 1 {
		t.Fatalf("legacy replay duplicated: %d %v", len(items), err)
	}
	if err := j.append([]byte(`{"event_id":"legacy-id","request_id":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if items, err := j.read(10); err != nil || len(items) != 2 {
		t.Fatalf("distinct case-sensitive public IDs collided: %d %v", len(items), err)
	}
	if err := j.ack([]string{"legacy-id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(j.dir, "Legacy-ID.json")); err != nil {
		t.Fatalf("ACK removed a different legacy ID: %v", err)
	}
	if err := j.ack([]string{"Legacy-ID"}); err != nil {
		t.Fatal(err)
	}
	if items, err := j.read(10); err != nil || len(items) != 0 {
		t.Fatalf("legacy ACK left %d items: %v", len(items), err)
	}
}

func TestUsageJournalHashedNamesCannotCollideWithLegacyIDs(t *testing.T) {
	j := &usageJournal{dir: t.TempDir()}
	sum := sha256.Sum256([]byte("usage:123"))
	legacyID := hex.EncodeToString(sum[:])
	legacy, _ := json.Marshal(map[string]string{"event_id": legacyID})
	if err := os.WriteFile(filepath.Join(j.dir, legacyID+".json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.append([]byte(`{"event_id":"usage:123"}`)); err != nil {
		t.Fatal(err)
	}
	if items, err := j.read(10); err != nil || len(items) != 2 {
		t.Fatalf("hash collided with legacy file: %d %v", len(items), err)
	}
	if err := j.ack([]string{"usage:123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(j.dir, legacyID+".json")); err != nil {
		t.Fatalf("hashed ACK removed legacy event: %v", err)
	}
}

func TestUsageJournalDoesNotPersistPlaintextAPIKeys(t *testing.T) {
	j := &usageJournal{dir: t.TempDir()}
	if err := j.append([]byte(`{"event_id":"secret-test","request_id":"r","api_key":"not-a-real-key"}`)); err != nil {
		t.Fatal(err)
	}
	items, err := j.read(10)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(items[0], &value); err != nil {
		t.Fatal(err)
	}
	if _, exists := value["api_key"]; exists {
		t.Fatal("durable journal contains API key")
	}
	if value["api_key_hash"] == nil {
		t.Fatal("key attribution was lost")
	}
}

func TestUsageJournalFingerprintsCredentialSource(t *testing.T) {
	j := &usageJournal{dir: t.TempDir()}
	if err := j.append([]byte(`{"event_id":"source-test","source":"upstream-secret","auth_type":"api_key"}`)); err != nil {
		t.Fatal(err)
	}
	items, err := j.read(10)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(items[0], &value); err != nil {
		t.Fatal(err)
	}
	if value["source"] == "upstream-secret" {
		t.Fatal("upstream credential persisted in source")
	}
}

func TestUsageJournalStripsFailureBodyWithoutChangingLegacyQueue(t *testing.T) {
	withEnabledQueue(t, func() {
		ConfigureUsageJournal(t.TempDir())
		defer ConfigureUsageJournal("")
		body := strings.Repeat("private-prompt-and-echoed-credential", 1000)
		(&usageQueuePlugin{}).HandleUsage(context.Background(), coreusage.Record{
			EventID: "failure-redaction", Provider: "codex", Model: "test", Failed: true,
			Fail:   coreusage.Failure{StatusCode: 401, Body: body},
			Detail: coreusage.Detail{InputTokens: 10, TotalTokens: 10, UsageObserved: true},
		})
		legacy := popSinglePayload(t)
		var legacyFail failDetail
		if err := json.Unmarshal(legacy["fail"], &legacyFail); err != nil {
			t.Fatal(err)
		}
		if legacyFail.Body != body {
			t.Fatal("legacy failure diagnostics were changed")
		}
		items, err := ReadUsageJournal(10)
		if err != nil || len(items) != 1 {
			t.Fatalf("journal read: count=%d err=%v", len(items), err)
		}
		var durable struct {
			EventID string                     `json:"event_id"`
			Failed  bool                       `json:"failed"`
			Fail    map[string]json.RawMessage `json:"fail"`
			Tokens  tokenStats                 `json:"tokens"`
		}
		if err := json.Unmarshal(items[0], &durable); err != nil {
			t.Fatal(err)
		}
		if _, exists := durable.Fail["body"]; exists || strings.Contains(string(items[0]), "private-prompt") {
			t.Fatal("durable journal retained sensitive upstream failure body")
		}
		if durable.EventID != "failure-redaction" || !durable.Failed || string(durable.Fail["status_code"]) != "401" || durable.Tokens.TotalTokens != 10 {
			t.Fatalf("failure attribution or usage was lost: %+v", durable)
		}
	})
}
