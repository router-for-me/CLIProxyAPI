package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
)

type fakeAntigravityReasoningReplayKVClient struct {
	values map[string][]byte
	setErr error
	delErr error
}

func newFakeAntigravityReasoningReplayKVClient() *fakeAntigravityReasoningReplayKVClient {
	return &fakeAntigravityReasoningReplayKVClient{values: make(map[string][]byte)}
}

func (c *fakeAntigravityReasoningReplayKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *fakeAntigravityReasoningReplayKVClient) KVSet(_ context.Context, key string, value []byte, _ homekv.KVSetOptions) (bool, error) {
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeAntigravityReasoningReplayKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	if c.delErr != nil {
		return 0, c.delErr
	}
	var deleted int64
	for _, key := range keys {
		if _, ok := c.values[key]; ok {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

func (c *fakeAntigravityReasoningReplayKVClient) KVExpire(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func useFakeAntigravityReasoningReplayKVClient(t *testing.T, client *fakeAntigravityReasoningReplayKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentAntigravityReasoningReplayKVClient
	currentAntigravityReasoningReplayKVClient = func() (antigravityReasoningReplayKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentAntigravityReasoningReplayKVClient = previous
	})
}

func validAntigravityReasoningReplayItemForTest() []byte {
	return []byte(`{"type":"thought_signature","thoughtSignature":"VALID_REPLAY_SIGNATURE_XXXXXXXXXX","contentIndex":1,"partIndex":0}`)
}

func TestStoreAntigravityReasoningReplayItemsStatus(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)

	if status := StoreAntigravityReasoningReplayItems(context.Background(), "", "session", [][]byte{validAntigravityReasoningReplayItemForTest()}); status != AntigravityReasoningReplayStoreInvalidArgs {
		t.Fatalf("empty model status = %v, want InvalidArgs", status)
	}
	if status := StoreAntigravityReasoningReplayItems(context.Background(), "gemini-3-flash-agent", "session", [][]byte{[]byte(`{"type":"message"}`)}); status != AntigravityReasoningReplayNoReplayableState {
		t.Fatalf("non-replayable status = %v, want NoReplayableState", status)
	}

	client := newFakeAntigravityReasoningReplayKVClient()
	client.setErr = errors.New("set failed")
	useFakeAntigravityReasoningReplayKVClient(t, client, true, nil)
	if status := StoreAntigravityReasoningReplayItems(context.Background(), "gemini-3-flash-agent", "session-home", [][]byte{validAntigravityReasoningReplayItemForTest()}); status != AntigravityReasoningReplayStoreBackendError {
		t.Fatalf("backend error status = %v, want StoreBackendError", status)
	}

	useFakeAntigravityReasoningReplayKVClient(t, newFakeAntigravityReasoningReplayKVClient(), false, nil)
	if status := StoreAntigravityReasoningReplayItems(context.Background(), "gemini-3-flash-agent", "session-local", [][]byte{validAntigravityReasoningReplayItemForTest()}); status != AntigravityReasoningReplayStored {
		t.Fatalf("local store status = %v, want Stored", status)
	}
}
