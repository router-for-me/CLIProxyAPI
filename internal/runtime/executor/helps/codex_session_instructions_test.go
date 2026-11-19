package helps

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

type sessionInstructionTestKV struct {
	kvstore.KVClient
	mu           sync.Mutex
	values       map[string][]byte
	gets, writes int
	ttl          time.Duration
	err          error
}

func (kv *sessionInstructionTestKV) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	kv.gets++
	value, ok := kv.values[key]
	return value, ok, kv.err
}

func (kv *sessionInstructionTestKV) KVSetNX(_ context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	kv.writes++
	kv.ttl = ttl
	if kv.err != nil {
		return false, kv.err
	}
	if _, ok := kv.values[key]; ok {
		return false, nil
	}
	kv.values[key] = value
	return true, nil
}

func TestCodexSessionInstructionStore(t *testing.T) {
	now := time.Unix(12345, 0)
	kv := &sessionInstructionTestKV{values: make(map[string][]byte)}
	mode := false
	store := codexSessionInstructionStore{
		now:     func() time.Time { return now },
		current: func() (kvstore.KVClient, bool, error) { return kv, mode, nil },
	}
	const key = "session"
	load := func(candidate string) (string, error) { return store.load(t.Context(), key, candidate) }
	if got, err := load(" A "); err != nil || got != " A " {
		t.Fatalf("start: %q %v", got, err)
	}
	now = now.Add(23 * time.Hour)
	if got, err := load("B"); err != nil || got != " A " {
		t.Fatalf("fixed baseline: %q %v", got, err)
	}
	now = now.Add(time.Hour)
	if got, err := load(""); got != "" || !errors.Is(err, errCodexSessionInstructionMissing) {
		t.Fatalf("expired reuse: %q %v", got, err)
	}
	if got, err := load("B"); err != nil || got != "B" {
		t.Fatalf("new start: %q %v", got, err)
	}
	mode = true
	kv.err = errors.New("sensitive error")
	if got, err := load(""); got != "" || !errors.Is(err, errCodexSessionInstructionStore) {
		t.Fatalf("enabled KV failure fell back to memory: %q %v", got, err)
	}
	if got, err := load("candidate"); got != "" || !errors.Is(err, errCodexSessionInstructionStore) {
		t.Fatalf("failed SET NX fell back to memory: %q %v", got, err)
	}
	kv.err = nil
	if got, err := load(""); got != "" || !errors.Is(err, errCodexSessionInstructionMissing) || kv.writes != 1 {
		t.Fatalf("reuse wrote or fell back to memory: %q %v", got, err)
	}
	const workers = 20
	var wg sync.WaitGroup
	results := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Go(func() {
			got, err := load(fmt.Sprint(i))
			if err != nil {
				t.Error(err)
			}
			results <- got
		})
	}
	wg.Wait()
	close(results)
	winner := string(kv.values[key])
	for got := range results {
		if got == "" || got != winner {
			t.Errorf("concurrent start got %q, want winner %q", got, winner)
		}
	}
	if kv.writes != workers+1 || kv.ttl != 24*time.Hour {
		t.Fatalf("SET NX count=%d ttl=%v", kv.writes, kv.ttl)
	}
}
