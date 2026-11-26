package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSignatureCacheConcurrentDeleteAndInsertNeverLosesLiveSignature(t *testing.T) {
	useFakeSignatureKVClient(t, newFakeSignatureKVClient(), false, nil)
	ClearSignatureCache("")
	t.Cleanup(func() { ClearSignatureCache("") })

	const (
		iterations = 2000
		oldText    = "old thinking text"
		liveText   = "live thinking text"
		oldSig     = "oldSignature12345678901234567890123456789012345678901234"
		liveSig    = "liveSignature1234567890123456789012345678901234567890123"
	)

	for i := 0; i < iterations; i++ {
		ctx := WithSignatureCacheScope(context.Background(), fmt.Sprintf("delete-toctou-scope-%d", i))
		if !CacheSignatureBestEffort(ctx, testModelName, oldText, oldSig) {
			t.Fatalf("iteration %d: failed to seed initial signature", i)
		}

		var ready sync.WaitGroup
		var done sync.WaitGroup
		start := make(chan struct{})
		ready.Add(2)
		done.Add(2)

		var deleteErr error
		var inserted bool

		go func() {
			defer done.Done()
			ready.Done()
			<-start
			deleteErr = DeleteCachedSignatureRequired(ctx, testModelName, oldText)
		}()

		go func() {
			defer done.Done()
			ready.Done()
			<-start
			inserted = CacheSignatureBestEffort(ctx, testModelName, liveText, liveSig)
		}()

		ready.Wait()
		close(start)
		done.Wait()

		if deleteErr != nil {
			t.Fatalf("iteration %d: DeleteCachedSignatureRequired() error = %v", i, deleteErr)
		}
		if !inserted {
			t.Fatalf("iteration %d: CacheSignatureBestEffort() = false, want true", i)
		}

		got, errGet := GetCachedSignatureRequired(ctx, testModelName, liveText)
		if errGet != nil {
			t.Fatalf("iteration %d: GetCachedSignatureRequired() error = %v", i, errGet)
		}
		if got != liveSig {
			t.Fatalf("iteration %d: live signature lost after concurrent delete: got %q, want %q", i, got, liveSig)
		}

		if errCleanup := DeleteCachedSignatureRequired(ctx, testModelName, liveText); errCleanup != nil {
			t.Fatalf("iteration %d: cleanup DeleteCachedSignatureRequired() error = %v", i, errCleanup)
		}
	}
}

func TestSignatureCacheConcurrentPurgeAndInsertNeverLosesLiveSignature(t *testing.T) {
	useFakeSignatureKVClient(t, newFakeSignatureKVClient(), false, nil)
	ClearSignatureCache("")
	t.Cleanup(func() { ClearSignatureCache("") })

	const (
		iterations  = 2000
		expiredText = "expired thinking text"
		liveText    = "live thinking text"
		expiredSig  = "expiredSignature1234567890123456789012345678901234567890"
		liveSig     = "liveSignature1234567890123456789012345678901234567890123"
	)

	for i := 0; i < iterations; i++ {
		scope := fmt.Sprintf("purge-toctou-scope-%d", i)
		ctx := WithSignatureCacheScope(context.Background(), scope)
		if !CacheSignatureBestEffort(ctx, testModelName, expiredText, expiredSig) {
			t.Fatalf("iteration %d: failed to seed expired signature", i)
		}

		groupKey := signatureCacheGroupKey(testModelName, scope)
		val, ok := signatureCache.Load(groupKey)
		if !ok {
			t.Fatalf("iteration %d: expected cache bucket for %q", i, groupKey)
		}
		sc := val.(*groupCache)
		sc.mu.Lock()
		entry := sc.entries[hashText(expiredText)]
		entry.Timestamp = time.Now().Add(-SignatureCacheTTL - time.Minute)
		sc.entries[hashText(expiredText)] = entry
		sc.mu.Unlock()

		var ready sync.WaitGroup
		var done sync.WaitGroup
		start := make(chan struct{})
		ready.Add(2)
		done.Add(2)

		var inserted bool

		go func() {
			defer done.Done()
			ready.Done()
			<-start
			purgeExpiredCaches()
		}()

		go func() {
			defer done.Done()
			ready.Done()
			<-start
			inserted = CacheSignatureBestEffort(ctx, testModelName, liveText, liveSig)
		}()

		ready.Wait()
		close(start)
		done.Wait()

		if !inserted {
			t.Fatalf("iteration %d: CacheSignatureBestEffort() = false, want true", i)
		}

		got, errGet := GetCachedSignatureRequired(ctx, testModelName, liveText)
		if errGet != nil {
			t.Fatalf("iteration %d: GetCachedSignatureRequired() error = %v", i, errGet)
		}
		if got != liveSig {
			t.Fatalf("iteration %d: live signature lost after concurrent purge: got %q, want %q", i, got, liveSig)
		}

		ClearSignatureCache("")
	}
}
