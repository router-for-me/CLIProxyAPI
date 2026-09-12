package redisqueue

import (
	"encoding/json"
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
	if err := restarted.ack([]string{"../../other"}); err == nil {
		t.Fatal("unsafe id accepted")
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
