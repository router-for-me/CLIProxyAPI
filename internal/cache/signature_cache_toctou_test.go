package cache

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSignatureCacheConcurrentDeleteAndInsertNeverLosesLiveSignature(t *testing.T) {
	const (
		model      = "claude-sonnet-4-5"
		iterations = 500
	)
	staleSig := strings.Repeat("s", MinValidSignatureLen)
	liveSig := strings.Repeat("l", MinValidSignatureLen)

	for i := 0; i < iterations; i++ {
		ctx := WithSignatureCacheScope(context.Background(), fmt.Sprintf("toctou-delete-%d", i))
		staleText := fmt.Sprintf("stale-%d", i)
		liveText := fmt.Sprintf("live-%d", i)

		if ok := CacheSignatureBestEffort(ctx, model, staleText, staleSig); !ok {
			t.Fatalf("iteration %d: failed to seed stale signature", i)
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)

		var errDel error
		go func() {
			defer wg.Done()
			<-start
			errDel = DeleteCachedSignatureRequired(ctx, model, staleText)
		}()

		var wrote bool
		go func() {
			defer wg.Done()
			<-start
			wrote = CacheSignatureBestEffort(ctx, model, liveText, liveSig)
		}()

		close(start)
		wg.Wait()

		if errDel != nil {
			t.Fatalf("iteration %d: DeleteCachedSignatureRequired returned error: %v", i, errDel)
		}
		if !wrote {
			t.Fatalf("iteration %d: CacheSignatureBestEffort returned false", i)
		}

		got, errGet := GetCachedSignatureRequired(ctx, model, liveText)
		if errGet != nil {
			t.Fatalf("iteration %d: GetCachedSignatureRequired returned error: %v", i, errGet)
		}
		if got != liveSig {
			t.Fatalf("iteration %d: live signature lost after concurrent delete: got %q, want %q", i, got, liveSig)
		}
	}
	ClearSignatureCache(model)
}

func TestSignatureCacheConcurrentPurgeAndInsertNeverLosesLiveSignature(t *testing.T) {
	const (
		model      = "claude-sonnet-4-5"
		iterations = 500
	)
	expiredSig := strings.Repeat("e", MinValidSignatureLen)
	liveSig := strings.Repeat("p", MinValidSignatureLen)

	for i := 0; i < iterations; i++ {
		scope := fmt.Sprintf("toctou-purge-%d", i)
		ctx := WithSignatureCacheScope(context.Background(), scope)
		expiredText := fmt.Sprintf("expired-%d", i)
		liveText := fmt.Sprintf("live-%d", i)

		groupKey := signatureCacheGroupKey(model, scope)
		sc := getOrCreateGroupCache(groupKey)
		sc.mu.Lock()
		sc.entries[hashText(expiredText)] = SignatureEntry{
			Signature: expiredSig,
			Timestamp: time.Now().Add(-SignatureCacheTTL - time.Minute),
		}
		sc.mu.Unlock()

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			purgeExpiredCaches()
		}()

		var wrote bool
		go func() {
			defer wg.Done()
			<-start
			wrote = CacheSignatureBestEffort(ctx, model, liveText, liveSig)
		}()

		close(start)
		wg.Wait()

		if !wrote {
			t.Fatalf("iteration %d: CacheSignatureBestEffort returned false", i)
		}

		got, errGet := GetCachedSignatureRequired(ctx, model, liveText)
		if errGet != nil {
			t.Fatalf("iteration %d: GetCachedSignatureRequired returned error: %v", i, errGet)
		}
		if got != liveSig {
			t.Fatalf("iteration %d: live signature lost after concurrent purge: got %q, want %q", i, got, liveSig)
		}
	}
	ClearSignatureCache(model)
}
