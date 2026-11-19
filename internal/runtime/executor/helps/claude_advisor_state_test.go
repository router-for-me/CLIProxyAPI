package helps

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

func TestClaudeAdvisorOwnerStore_FirstWriterWinsSlidingTTLAndBounds(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	store := NewClaudeAdvisorOwnerStore()
	store.nowFunc = func() time.Time { return now }

	// First writer wins while entry is still live.
	store.Remember("claude", "srvtoolu_1", "id:cred-a")
	store.Remember("claude", "srvtoolu_1", "id:cred-b")
	owner, ok := store.Lookup(context.Background(), "claude", "srvtoolu_1")
	if !ok || owner != "id:cred-a" {
		t.Fatalf("Lookup() = (%q, %v), want (id:cred-a, true)", owner, ok)
	}

	// Scope isolation.
	if foreignOwner, found := store.Lookup(context.Background(), "other-provider", "srvtoolu_1"); found {
		t.Fatalf("Lookup() crossed scopes: (%q, %v)", foreignOwner, found)
	}

	// Active session sliding TTL: hitting Lookup at +20h keeps the entry alive past
	// the original 24h mark (at +30h total).
	now = now.Add(20 * time.Hour)
	owner, ok = store.Lookup(context.Background(), "claude", "srvtoolu_1")
	if !ok || owner != "id:cred-a" {
		t.Fatalf("active Lookup() at +20h = (%q, %v), want (id:cred-a, true)", owner, ok)
	}
	now = now.Add(10 * time.Hour) // 30h since initial registration, 10h since last hit
	owner, ok = store.Lookup(context.Background(), "claude", "srvtoolu_1")
	if !ok || owner != "id:cred-a" {
		t.Fatalf("active Lookup() at +30h (10h idle) = (%q, %v), want (id:cred-a, true)", owner, ok)
	}

	// Idle entry expires once untouched for >24h.
	now = now.Add(ClaudeAdvisorOwnerTTL + time.Second)
	if expiredOwner, found := store.Lookup(context.Background(), "claude", "srvtoolu_1"); found {
		t.Fatalf("expired entry remained live: (%q, %v)", expiredOwner, found)
	}

	// Re-inserting an expired key keeps order length accurate.
	store.Remember("claude", "srvtoolu_1", "id:cred-b")
	owner, ok = store.Lookup(context.Background(), "claude", "srvtoolu_1")
	if !ok || owner != "id:cred-b" {
		t.Fatalf("refreshed Lookup() = (%q, %v), want (id:cred-b, true)", owner, ok)
	}
	if store.order.Len() != 1 {
		t.Fatalf("order len = %d after refresh, want 1", store.order.Len())
	}

	// Bounded LRU eviction: exceeding claudeAdvisorOwnerLimit evicts the oldest idle entry.
	for i := 0; i <= claudeAdvisorOwnerLimit; i++ {
		store.Remember("claude", "srvtoolu_bound_"+strconv.Itoa(i), "id:cred-a")
	}
	if _, found := store.Lookup(context.Background(), "claude", "srvtoolu_1"); found {
		t.Fatal("oldest entry was not evicted when exceeding store limit")
	}
	if got, found := store.Lookup(context.Background(), "claude", "srvtoolu_bound_"+strconv.Itoa(claudeAdvisorOwnerLimit)); !found || got != "id:cred-a" {
		t.Fatalf("newest entry lost: (%q, %v)", got, found)
	}
}

func TestClaudeAdvisorOwnerStore_SharedKVFirstWriterConsistency(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	kvPath := filepath.Join(t.TempDir(), "kv.db")
	if errCfg := kvstore.ConfigureLocal(true, kvPath); errCfg != nil {
		t.Fatal(errCfg)
	}
	t.Cleanup(func() {
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	})

	store1 := NewClaudeAdvisorOwnerStore()
	store2 := NewClaudeAdvisorOwnerStore()

	// store1 claims ownership first in shared KV; store2 attempts a conflicting claim.
	store1.Remember("claude", "srvtoolu_shared_kv", "id:cred-a")
	store2.Remember("claude", "srvtoolu_shared_kv", "id:cred-b")

	owner1, ok1 := store1.Lookup(context.Background(), "claude", "srvtoolu_shared_kv")
	owner2, ok2 := store2.Lookup(context.Background(), "claude", "srvtoolu_shared_kv")
	if !ok1 || owner1 != "id:cred-a" {
		t.Fatalf("store1 Lookup() = (%q, %v), want (id:cred-a, true)", owner1, ok1)
	}
	if !ok2 || owner2 != "id:cred-a" {
		t.Fatalf("store2 Lookup() = (%q, %v), want authoritative KV winner (id:cred-a, true)", owner2, ok2)
	}

	// Active Lookup past the KV refresh interval must renew the backing KV TTL.
	now := time.Now()
	store1.nowFunc = func() time.Time { return now }
	client, kvMode, errClient := kvstore.CurrentKVClient()
	if !kvMode || errClient != nil || client == nil {
		t.Fatalf("CurrentKVClient() = (%v, %v, %v), want active local KV", client, kvMode, errClient)
	}
	key := claudeAdvisorOwnerKVKey("claude", "srvtoolu_shared_kv")
	if _, errExp := client.KVExpire(context.Background(), key, 30*time.Second); errExp != nil {
		t.Fatalf("KVExpire() error = %v", errExp)
	}
	now = now.Add(2 * time.Hour)
	if got, found := store1.Lookup(context.Background(), "claude", "srvtoolu_shared_kv"); !found || got != "id:cred-a" {
		t.Fatalf("active Lookup() = (%q, %v), want (id:cred-a, true)", got, found)
	}
	remaining, hasTTL, errTTL := client.KVTTL(context.Background(), key)
	if errTTL != nil || !hasTTL || remaining < 23*time.Hour {
		t.Fatalf("backing KV TTL after active Lookup = (%v, hasTTL=%v, err=%v), want >= 23h", remaining, hasTTL, errTTL)
	}
}

type stubConflictAdvisorKVClient struct {
	kvstore.KVClient
	getErr    error
	getVal    []byte
	getFound  bool
	setNXDone bool
}

func (c *stubConflictAdvisorKVClient) KVSetNX(_ context.Context, _ string, _ []byte, _ time.Duration) (bool, error) {
	c.setNXDone = true
	return false, nil
}

func (c *stubConflictAdvisorKVClient) KVGet(_ context.Context, _ string) ([]byte, bool, error) {
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	return c.getVal, c.getFound, nil
}

func (c *stubConflictAdvisorKVClient) KVExpire(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func TestClaudeAdvisorOwnerStore_KVConflictReadErrorDoesNotCacheLoser(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	stub := &stubConflictAdvisorKVClient{getErr: errors.New("transient kv read failure")}
	kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) {
		return stub, true, nil
	})
	t.Cleanup(func() {
		kvstore.RegisterHomeProvider(nil)
	})

	store := NewClaudeAdvisorOwnerStore()
	// KVSetNX returns false (lost conflict) and follow-up KVGet fails -> losing
	// candidate "id:cred-b" must NOT be cached locally.
	store.Remember("claude", "srvtoolu_conflict_err", "id:cred-b")
	if !stub.setNXDone {
		t.Fatal("expected KVSetNX to be called")
	}

	// Once KV recovers, Lookup must fetch the true winner "id:cred-a" rather than
	// returning the losing candidate "id:cred-b".
	stub.getErr = nil
	stub.getVal = []byte("id:cred-a")
	stub.getFound = true
	owner, ok := store.Lookup(context.Background(), "claude", "srvtoolu_conflict_err")
	if !ok || owner != "id:cred-a" {
		t.Fatalf("Lookup() after conflict read recovery = (%q, %v), want (id:cred-a, true)", owner, ok)
	}
}

func TestStripForeignAdvisorBlocksDoc_EdgeCases(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	store := NewClaudeAdvisorOwnerStore()
	store.Remember("claude", "toolu_ord", "id:cred-a")
	store.Remember("claude", "srvtoolu_obj", "id:cred-a")
	store.Remember("claude", "srvtoolu_mixed_srv", "id:cred-a")

	doc := ojson.NewDocString(`{
		"messages": [
			{"role": "user", "content": "hi"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_ord", "name": "normal_tool", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_ord",
						"content": [
							{"type": "advisor_redacted_result", "encrypted_content": "enc_ord"}
						]
					}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "server_tool_use", "id": "srvtoolu_mixed_srv", "name": "advisor", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "srvtoolu_mixed_srv",
						"content": [
							{"type": "text", "text": "wrapper text"},
							{"type": "advisor_redacted_result", "encrypted_content": "enc_mixed_srv"}
						]
					}
				]
			},
			{
				"role": "user",
				"content": {
					"type": "advisor_tool_result",
					"tool_use_id": "srvtoolu_obj",
					"content": {"type": "advisor_redacted_result", "encrypted_content": "enc_obj"}
				}
			},
			{"role": "user", "content": "final"}
		]
	}`)

	_, stripped := store.StripForeignAdvisorBlocksDoc(context.Background(), doc, "claude", "id:cred-b")
	if stripped != 4 {
		t.Fatalf("stripped = %d, want 4", stripped)
	}
	out := doc.String()
	if strings.Contains(out, "enc_ord") || strings.Contains(out, "enc_obj") || strings.Contains(out, "srvtoolu_obj") || strings.Contains(out, "srvtoolu_mixed_srv") || strings.Contains(out, "enc_mixed_srv") {
		t.Fatalf("foreign advisor content or dangling server_tool_use tool_result remained: %s", out)
	}
	// Ordinary tool_use must keep its paired tool_result with cleared content instead of orphaning it.
	if got := doc.Get("messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.2.content.0.type = %q, want tool_result: %s", got, out)
	}
	if got := doc.Get("messages.2.content.0.content").String(); got != "" {
		t.Fatalf("messages.2.content.0.content = %q, want empty string: %s", got, out)
	}
	// Both advisor-only turns (server_tool_use + mixed tool_result for dropped server_tool_use)
	// and the object-content advisor message must be dropped completely.
	if got := doc.Get("messages.#").Int(); got != 4 {
		t.Fatalf("messages count = %d, want 4: %s", got, out)
	}
}
