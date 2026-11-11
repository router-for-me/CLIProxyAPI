package helps

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func resetSessionIDCache() {
	sessionIDCacheMu.Lock()
	sessionIDCache = make(map[string]sessionIDCacheEntry)
	sessionIDCacheMu.Unlock()
}

type fakeClaudeIDKVClient struct {
	values        map[string][]byte
	getErr        error
	setErr        error
	expireErr     error
	setNoPersist  bool
	getCount      int
	setCount      int
	expireCount   int
	lastSetTTL    time.Duration
	lastExpireTTL time.Duration
}

func newFakeClaudeIDKVClient() *fakeClaudeIDKVClient {
	return &fakeClaudeIDKVClient{values: make(map[string][]byte)}
}

func (c *fakeClaudeIDKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.getCount++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *fakeClaudeIDKVClient) KVSetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	c.setCount++
	c.lastSetTTL = ttl
	if c.setErr != nil {
		return false, c.setErr
	}
	if _, ok := c.values[key]; ok {
		return false, nil
	}
	if !c.setNoPersist {
		c.values[key] = append([]byte(nil), value...)
	}
	return true, nil
}

func (c *fakeClaudeIDKVClient) KVExpire(_ context.Context, _ string, ttl time.Duration) (bool, error) {
	c.expireCount++
	c.lastExpireTTL = ttl
	if c.expireErr != nil {
		return false, c.expireErr
	}
	return true, nil
}

func useFakeClaudeIDKVClient(t *testing.T, client *fakeClaudeIDKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentClaudeIDKVClient
	currentClaudeIDKVClient = func() (claudeIDKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentClaudeIDKVClient = previous
	})
}

func TestCachedSessionIDRequiredHomeReusesKVAcrossLocalCacheReset(t *testing.T) {
	resetSessionIDCache()
	client := newFakeClaudeIDKVClient()
	useFakeClaudeIDKVClient(t, client, true, nil)

	first, errFirst := CachedSessionIDRequired(context.Background(), "api-key-1")
	if errFirst != nil {
		t.Fatalf("CachedSessionIDRequired() first error = %v", errFirst)
	}
	resetSessionIDCache()
	second, errSecond := CachedSessionIDRequired(context.Background(), "api-key-1")
	if errSecond != nil {
		t.Fatalf("CachedSessionIDRequired() second error = %v", errSecond)
	}
	if first != second {
		t.Fatalf("session id = %q then %q, want same Home KV value", first, second)
	}
	if _, errParse := uuid.Parse(first); errParse != nil {
		t.Fatalf("session id %q is not a UUID: %v", first, errParse)
	}
	if client.setCount != 1 {
		t.Fatalf("KVSetNX count = %d, want 1", client.setCount)
	}
	if client.expireCount != 1 || client.lastExpireTTL != sessionIDTTL {
		t.Fatalf("KVExpire count/ttl = %d/%v, want 1/%v", client.expireCount, client.lastExpireTTL, sessionIDTTL)
	}
	if client.lastSetTTL != sessionIDTTL {
		t.Fatalf("KVSetNX ttl = %v, want %v", client.lastSetTTL, sessionIDTTL)
	}
}

func TestCachedSessionIDRequiredEmptyAPIKeyDoesNotUseHomeKV(t *testing.T) {
	client := newFakeClaudeIDKVClient()
	useFakeClaudeIDKVClient(t, client, true, nil)

	value, errValue := CachedSessionIDRequired(context.Background(), "")
	if errValue != nil {
		t.Fatalf("CachedSessionIDRequired(empty) error = %v", errValue)
	}
	if _, errParse := uuid.Parse(value); errParse != nil {
		t.Fatalf("session id %q is not a UUID: %v", value, errParse)
	}
	if client.getCount != 0 || client.setCount != 0 || client.expireCount != 0 {
		t.Fatalf("KV calls = get %d set %d expire %d, want all zero", client.getCount, client.setCount, client.expireCount)
	}
}

func TestCachedSessionIDRequiredHomeKVFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client *fakeClaudeIDKVClient
	}{
		{name: "get", client: &fakeClaudeIDKVClient{values: make(map[string][]byte), getErr: errors.New("get failed")}},
		{name: "set", client: &fakeClaudeIDKVClient{values: make(map[string][]byte), setErr: errors.New("set failed")}},
		{name: "expire", client: &fakeClaudeIDKVClient{values: map[string][]byte{
			claudeSessionIDKVKey("api-key-1"): []byte(uuid.New().String()),
		}, expireErr: errors.New("expire failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFakeClaudeIDKVClient(t, tc.client, true, nil)
			if _, errValue := CachedSessionIDRequired(context.Background(), "api-key-1"); errValue == nil {
				t.Fatalf("CachedSessionIDRequired() error = nil, want error")
			}
		})
	}
}

func TestCachedSessionIDRequiredHomeRequiresReadAfterSet(t *testing.T) {
	client := newFakeClaudeIDKVClient()
	client.setNoPersist = true
	useFakeClaudeIDKVClient(t, client, true, nil)

	if _, errValue := CachedSessionIDRequired(context.Background(), "api-key-1"); errValue == nil {
		t.Fatalf("CachedSessionIDRequired() error = nil, want missing-after-set error")
	}
}

func TestCachedSessionIDRequiredNonHomeModeUsesLocalMap(t *testing.T) {
	resetSessionIDCache()
	client := newFakeClaudeIDKVClient()
	useFakeClaudeIDKVClient(t, client, false, nil)

	first, errFirst := CachedSessionIDRequired(context.Background(), "api-key-1")
	if errFirst != nil {
		t.Fatalf("CachedSessionIDRequired() first error = %v", errFirst)
	}
	second, errSecond := CachedSessionIDRequired(context.Background(), "api-key-1")
	if errSecond != nil {
		t.Fatalf("CachedSessionIDRequired() second error = %v", errSecond)
	}
	if first != second {
		t.Fatalf("session id = %q then %q, want local cache reuse", first, second)
	}
	if client.getCount != 0 || client.setCount != 0 || client.expireCount != 0 {
		t.Fatalf("KV calls = get %d set %d expire %d, want all zero", client.getCount, client.setCount, client.expireCount)
	}
}
