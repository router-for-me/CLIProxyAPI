package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/tidwall/gjson"
)

type fakeAntigravityReasoningReplayKVClient struct {
	mu          sync.Mutex
	values      map[string][]byte
	expireCount int
}

func newFakeAntigravityReasoningReplayKVClient() *fakeAntigravityReasoningReplayKVClient {
	return &fakeAntigravityReasoningReplayKVClient{values: make(map[string][]byte)}
}

func (c *fakeAntigravityReasoningReplayKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	return append([]byte(nil), value...), ok, nil
}

func (c *fakeAntigravityReasoningReplayKVClient) KVSet(_ context.Context, key string, value []byte, _ homekv.KVSetOptions) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeAntigravityReasoningReplayKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, value []byte, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, exists := c.values[key]
	if exists != expectedExists || (exists && !bytes.Equal(current, expected)) {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeAntigravityReasoningReplayKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireCount++
	return true, nil
}

func useFakeAntigravityReasoningReplayKVClient(t *testing.T, client *fakeAntigravityReasoningReplayKVClient, homeMode bool) {
	t.Helper()
	previous := currentAntigravityReasoningReplayKVClient
	currentAntigravityReasoningReplayKVClient = func() (antigravityReasoningReplayKVClient, bool, error) {
		return client, homeMode, nil
	}
	t.Cleanup(func() {
		currentAntigravityReasoningReplayKVClient = previous
	})
}

func antigravityReplayTestItem(signature string) []byte {
	return []byte(`{"type":"thought_signature","contentIndex":1,"partIndex":0,"thoughtSignature":"` + signature + `"}`)
}

func TestAntigravityReasoningReplayConditionalMutationRejectsStaleLocalSnapshot(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	const model, session = "gemini-3.6-flash-high", "stale-local"
	oldItem := antigravityReplayTestItem("old-local-signature-123456")
	newItem := antigravityReplayTestItem("new-local-signature-123456")
	staleItem := antigravityReplayTestItem("stale-local-signature-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{oldItem}) {
		t.Fatal("initial cache write failed")
	}
	_, snapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || !found {
		t.Fatalf("snapshot read failed: found=%v err=%v", found, errGet)
	}
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{newItem}) {
		t.Fatal("newer cache write failed")
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, snapshot, [][]byte{staleItem}); errSwap != nil || swapped {
		t.Fatalf("stale replace = %v, %v; want false, nil", swapped, errSwap)
	}
	if deleted, errDelete := DeleteAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, snapshot); errDelete != nil || deleted {
		t.Fatalf("stale delete = %v, %v; want false, nil", deleted, errDelete)
	}
	items, ok := GetAntigravityReasoningReplayItems(model, session)
	if !ok || len(items) != 1 || !bytes.Contains(items[0], []byte("new-local-signature")) {
		t.Fatalf("newer state was lost: %q, found=%v", items, ok)
	}
}

func TestAntigravityReasoningReplayNonPrefixReplaceRotatesLocalBranch(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	const model, session = "gemini-3.6-flash-high", "non-prefix-local"
	oldItem := antigravityReplayTestItem("non-prefix-old-signature-123456")
	newItem := antigravityReplayTestItem("non-prefix-new-signature-123456")
	latestItem := antigravityReplayTestItem("non-prefix-latest-signature-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{oldItem}) {
		t.Fatal("old local write failed")
	}
	_, firstSnapshot, _, errFirstGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	_, staleSnapshot, _, errStaleGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errFirstGet != nil || errStaleGet != nil {
		t.Fatalf("snapshot reads failed: %v, %v", errFirstGet, errStaleGet)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, firstSnapshot, [][]byte{newItem}); errSwap != nil || !swapped {
		t.Fatalf("non-prefix local replace = %v, %v", swapped, errSwap)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{newItem, latestItem}); errSwap != nil || swapped {
		t.Fatalf("stale local descendant crossed non-prefix reset: swapped=%v err=%v", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayConditionalReplaceAcceptsDescendantLocalChain(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	const model, session = "gemini-3.6-flash-high", "descendant-local"
	prefix := antigravityReplayTestItem("descendant-prefix-signature-123456")
	middle := antigravityReplayTestItem("descendant-middle-signature-123456")
	latest := antigravityReplayTestItem("descendant-latest-signature-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{prefix}) {
		t.Fatal("prefix write failed")
	}
	_, staleSnapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || !found {
		t.Fatalf("prefix snapshot failed: found=%v err=%v", found, errGet)
	}
	_, firstSnapshot, _, errFirstGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errFirstGet != nil {
		t.Fatal(errFirstGet)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, firstSnapshot, [][]byte{prefix, middle}); errSwap != nil || !swapped {
		t.Fatalf("middle conditional write = %v, %v", swapped, errSwap)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{prefix, middle, latest}); errSwap != nil || !swapped {
		t.Fatalf("descendant local replace = %v, %v; want true, nil", swapped, errSwap)
	}
	items, ok := GetAntigravityReasoningReplayItems(model, session)
	if !ok || len(items) != 3 {
		t.Fatalf("descendant local chain = %d items, found=%v", len(items), ok)
	}
}

func TestAntigravityReasoningReplayDescendantMergeRejectsResetBranchABA(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	const model, session = "gemini-3.6-flash-high", "descendant-reset-aba"
	prefix := antigravityReplayTestItem("reset-prefix-signature-123456")
	middle := antigravityReplayTestItem("reset-middle-signature-123456")
	staleLatest := antigravityReplayTestItem("reset-stale-signature-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{prefix}) {
		t.Fatal("prefix write failed")
	}
	_, staleSnapshot, _, errStaleGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	_, firstSnapshot, _, errFirstGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errStaleGet != nil || errFirstGet != nil {
		t.Fatalf("snapshot reads failed: %v, %v", errStaleGet, errFirstGet)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, firstSnapshot, [][]byte{prefix, middle}); errSwap != nil || !swapped {
		t.Fatalf("middle write = %v, %v", swapped, errSwap)
	}
	_, currentSnapshot, _, errCurrentGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errCurrentGet != nil {
		t.Fatal(errCurrentGet)
	}
	if deleted, errDelete := DeleteAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, currentSnapshot); errDelete != nil || !deleted {
		t.Fatalf("branch reset = %v, %v", deleted, errDelete)
	}
	_, resetSnapshot, _, errResetGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errResetGet != nil {
		t.Fatal(errResetGet)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, resetSnapshot, [][]byte{prefix}); errSwap != nil || !swapped {
		t.Fatalf("new branch prefix write = %v, %v", swapped, errSwap)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{prefix, staleLatest}); errSwap != nil || swapped {
		t.Fatalf("stale descendant crossed reset branch: swapped=%v err=%v", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayConditionalDeleteTombstoneBlocksStaleFirstWriter(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	const model, session = "gemini-3.6-flash-high", "stale-first-writer"
	_, staleSnapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || found {
		t.Fatalf("initial absent snapshot = found %v, err %v", found, errGet)
	}
	_, clearSnapshot, _, errClearGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errClearGet != nil {
		t.Fatal(errClearGet)
	}
	if deleted, errDelete := DeleteAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, clearSnapshot); errDelete != nil || !deleted {
		t.Fatalf("conditional empty clear = %v, %v; want true, nil", deleted, errDelete)
	}
	staleItem := antigravityReplayTestItem("stale-first-writer-signature-123456")
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{staleItem}); errSwap != nil || swapped {
		t.Fatalf("stale first write = %v, %v; want false, nil", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayEvictedTombstoneStillBlocksStaleFirstWriter(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	const model, session = "gemini-3.6-flash-high", "evicted-stale-first-writer"
	_, staleSnapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || found {
		t.Fatalf("initial absent snapshot = found %v, err %v", found, errGet)
	}
	_, clearSnapshot, _, errClearGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errClearGet != nil {
		t.Fatal(errClearGet)
	}
	if deleted, errDelete := DeleteAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, clearSnapshot); errDelete != nil || !deleted {
		t.Fatalf("conditional clear = %v, %v", deleted, errDelete)
	}
	antigravityReasoningReplayMu.Lock()
	evictOldestAntigravityReasoningReplayEntries(1)
	antigravityReasoningReplayMu.Unlock()
	staleItem := antigravityReplayTestItem("evicted-stale-first-writer-signature-123456")
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{staleItem}); errSwap != nil || swapped {
		t.Fatalf("stale first writer crossed tombstone eviction: swapped=%v err=%v", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayEvictedLiveEntryStillBlocksStaleAbsentSnapshot(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	const model = "gemini-3.6-flash-high"
	_, staleSnapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, "absent-before-live-eviction")
	if errGet != nil || found {
		t.Fatalf("initial absent snapshot = found %v, err %v", found, errGet)
	}
	liveItem := antigravityReplayTestItem("evicted-live-signature-123456")
	if !CacheAntigravityReasoningReplayItems(model, "live-entry", [][]byte{liveItem}) {
		t.Fatal("live entry write failed")
	}
	antigravityReasoningReplayMu.Lock()
	evictOldestAntigravityReasoningReplayEntries(1)
	antigravityReasoningReplayMu.Unlock()
	staleItem := antigravityReplayTestItem("stale-after-live-eviction-123456")
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, "absent-before-live-eviction", staleSnapshot, [][]byte{staleItem}); errSwap != nil || swapped {
		t.Fatalf("stale absent snapshot crossed live-entry eviction: swapped=%v err=%v", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayHomeAbsentSnapshotIsFenced(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "home-absent-fence"
	_, snapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || found || !snapshot.found || len(snapshot.raw) == 0 {
		t.Fatalf("fenced Home miss = found %v snapshotFound %v raw %d err %v", found, snapshot.found, len(snapshot.raw), errGet)
	}
	key := antigravityReasoningReplayKVKey(model, session)
	client.mu.Lock()
	client.values[key] = []byte(`[[123]]`)
	delete(client.values, key)
	client.mu.Unlock()
	item := antigravityReplayTestItem("home-absent-stale-signature-123456")
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, snapshot, [][]byte{item}); errSwap != nil || swapped {
		t.Fatalf("stale Home absent snapshot crossed value expiry: swapped=%v err=%v", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayConditionalMutationRejectsStaleHomeSnapshot(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "stale-home"
	oldItem := antigravityReplayTestItem("old-home-signature-123456")
	newItem := antigravityReplayTestItem("new-home-signature-123456")
	staleItem := antigravityReplayTestItem("stale-home-signature-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{oldItem}) {
		t.Fatal("initial Home write failed")
	}
	_, snapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || !found {
		t.Fatalf("Home snapshot read failed: found=%v err=%v", found, errGet)
	}
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{newItem}) {
		t.Fatal("newer Home write failed")
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, snapshot, [][]byte{staleItem}); errSwap != nil || swapped {
		t.Fatalf("stale Home replace = %v, %v; want false, nil", swapped, errSwap)
	}
	if deleted, errDelete := DeleteAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, snapshot); errDelete != nil || deleted {
		t.Fatalf("stale Home delete = %v, %v; want false, nil", deleted, errDelete)
	}
	items, ok := GetAntigravityReasoningReplayItems(model, session)
	if !ok || len(items) != 1 || !bytes.Contains(items[0], []byte("new-home-signature")) {
		t.Fatalf("newer Home state was lost: %q, found=%v", items, ok)
	}
}

func TestAntigravityReasoningReplayNonPrefixReplaceRotatesHomeBranch(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "non-prefix-home"
	oldItem := antigravityReplayTestItem("non-prefix-home-old-123456")
	newItem := antigravityReplayTestItem("non-prefix-home-new-123456")
	latestItem := antigravityReplayTestItem("non-prefix-home-latest-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{oldItem}) {
		t.Fatal("old Home write failed")
	}
	_, firstSnapshot, _, errFirstGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	_, staleSnapshot, _, errStaleGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errFirstGet != nil || errStaleGet != nil {
		t.Fatalf("Home snapshot reads failed: %v, %v", errFirstGet, errStaleGet)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, firstSnapshot, [][]byte{newItem}); errSwap != nil || !swapped {
		t.Fatalf("non-prefix Home replace = %v, %v", swapped, errSwap)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{newItem, latestItem}); errSwap != nil || swapped {
		t.Fatalf("stale Home descendant crossed non-prefix reset: swapped=%v err=%v", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayConditionalReplaceAcceptsDescendantHomeChain(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "descendant-home"
	prefix := antigravityReplayTestItem("home-descendant-prefix-123456")
	middle := antigravityReplayTestItem("home-descendant-middle-123456")
	latest := antigravityReplayTestItem("home-descendant-latest-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{prefix}) {
		t.Fatal("Home prefix write failed")
	}
	_, staleSnapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || !found {
		t.Fatalf("Home prefix snapshot failed: found=%v err=%v", found, errGet)
	}
	_, firstSnapshot, _, errFirstGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errFirstGet != nil {
		t.Fatal(errFirstGet)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, firstSnapshot, [][]byte{prefix, middle}); errSwap != nil || !swapped {
		t.Fatalf("Home middle conditional write = %v, %v", swapped, errSwap)
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{prefix, middle, latest}); errSwap != nil || !swapped {
		t.Fatalf("descendant Home replace = %v, %v; want true, nil", swapped, errSwap)
	}
	items, ok := GetAntigravityReasoningReplayItems(model, session)
	if !ok || len(items) != 3 {
		t.Fatalf("descendant Home chain = %d items, found=%v", len(items), ok)
	}
}

func TestAntigravityReasoningReplayHomeGenerationRejectsSuccessfulValueABA(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "home-aba"
	itemA := antigravityReplayTestItem("home-aba-signature-a-123456")
	itemB := antigravityReplayTestItem("home-aba-signature-b-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{itemA}) {
		t.Fatal("initial A write failed")
	}
	_, staleSnapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || !found {
		t.Fatalf("A snapshot read failed: found=%v err=%v", found, errGet)
	}
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{itemB}) || !CacheAntigravityReasoningReplayItems(model, session, [][]byte{itemA}) {
		t.Fatal("B to A rewrite failed")
	}
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, staleSnapshot, [][]byte{itemB}); errSwap != nil || swapped {
		t.Fatalf("stale A snapshot passed Home ABA guard: swapped=%v err=%v", swapped, errSwap)
	}
}

func TestAntigravityReasoningReplayHomeCASRetryRejectsOversizedValue(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "oversized-home-cas"
	prefix := antigravityReplayTestItem("oversized-home-prefix-123456")
	latest := antigravityReplayTestItem("oversized-home-latest-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{prefix}) {
		t.Fatal("Home prefix write failed")
	}
	_, snapshot, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || !found {
		t.Fatalf("Home snapshot read failed: found=%v err=%v", found, errGet)
	}
	oversized, errMarshal := marshalAntigravityReasoningReplayHomeValue([][]byte{prefix}, snapshot.branch)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	oversized = append(oversized, bytes.Repeat([]byte(" "), antigravityReasoningReplayCacheMaxSerializedBytes-len(oversized)+1)...)
	key := antigravityReasoningReplayKVKey(model, session)
	client.values[key] = oversized
	if swapped, errSwap := ReplaceAntigravityReasoningReplayItemsIfUnchanged(context.Background(), model, session, snapshot, [][]byte{prefix, latest}); errSwap != nil || swapped {
		t.Fatalf("oversized Home CAS retry = swapped %v, err %v; want false, nil", swapped, errSwap)
	}
	if got := len(client.values[key]); got <= antigravityReasoningReplayCacheMaxSerializedBytes {
		t.Fatalf("oversized value was unexpectedly replaced: %d", got)
	}
}

func TestAntigravityReasoningReplayLocalTombstonesStayWithinEntryBound(t *testing.T) {
	ClearAntigravityReasoningReplayCache()
	t.Cleanup(ClearAntigravityReasoningReplayCache)
	for index := 0; index <= AntigravityReasoningReplayCacheMaxEntries; index++ {
		if errDelete := DeleteAntigravityReasoningReplayItemRequired(context.Background(), "gemini-3.6-flash-high", fmt.Sprintf("tombstone-%d", index)); errDelete != nil {
			t.Fatal(errDelete)
		}
	}
	antigravityReasoningReplayMu.Lock()
	entryCount := len(antigravityReasoningReplayEntries)
	antigravityReasoningReplayMu.Unlock()
	if entryCount > AntigravityReasoningReplayCacheMaxEntries {
		t.Fatalf("local tombstone count = %d, max %d", entryCount, AntigravityReasoningReplayCacheMaxEntries)
	}
}

func TestAntigravityReasoningReplayHomeWritesRemainLegacyArrayReadable(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "home-legacy-readable"
	item := antigravityReplayTestItem("legacy-readable-signature-123456")
	if !CacheAntigravityReasoningReplayItems(model, session, [][]byte{item}) {
		t.Fatal("Home write failed")
	}
	raw := client.values[antigravityReasoningReplayKVKey(model, session)]
	var legacyItems [][]byte
	if errUnmarshal := json.Unmarshal(raw, &legacyItems); errUnmarshal != nil {
		t.Fatalf("new Home value is not readable as legacy [][]byte: %v", errUnmarshal)
	}
	if len(legacyItems) != 2 || gjson.GetBytes(legacyItems[0], "type").String() != antigravityReasoningReplayGenerationItemType || !bytes.Contains(legacyItems[1], []byte("legacy-readable-signature")) {
		t.Fatalf("legacy-readable Home array malformed: %q", legacyItems)
	}
}

func TestAntigravityReasoningReplayHomeReadNormalizesAndRejectsMixedInvalidChain(t *testing.T) {
	client := newFakeAntigravityReasoningReplayKVClient()
	useFakeAntigravityReasoningReplayKVClient(t, client, true)
	const model, session = "gemini-3.6-flash-high", "home-validation"
	key := antigravityReasoningReplayKVKey(model, session)
	valid := []byte(`{"type":"function_call_part","name":"run","args":{"b":2,"a":1},"targetOccurrence":1,"thoughtSignature":"valid-home-signature-123456"}`)
	raw, errMarshal := json.Marshal([][]byte{valid})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	client.values[key] = raw
	items, _, found, errGet := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session)
	if errGet != nil || !found || len(items) != 1 {
		t.Fatalf("valid Home read = %q, found=%v err=%v", items, found, errGet)
	}
	if !bytes.Contains(items[0], []byte(`"targetOccurrence":1`)) {
		t.Fatalf("target occurrence was not normalized: %s", items[0])
	}
	if client.expireCount != 1 {
		t.Fatalf("valid Home read expire count = %d, want 1", client.expireCount)
	}

	invalidRaw, errInvalidMarshal := json.Marshal([][]byte{valid, []byte(`{"type":"unknown"}`)})
	if errInvalidMarshal != nil {
		t.Fatal(errInvalidMarshal)
	}
	client.values[key] = invalidRaw
	if _, _, foundInvalid, errInvalid := GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, session); errInvalid != nil || foundInvalid {
		t.Fatalf("mixed invalid Home chain = found %v, err %v; want false, nil", foundInvalid, errInvalid)
	}
	if client.expireCount != 1 {
		t.Fatalf("invalid Home read refreshed TTL: count=%d", client.expireCount)
	}
}
