package session

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

func TestClaudeThreadAuthRecoversFromKVWithCallerIsolation(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	path := filepath.Join(t.TempDir(), "kv.db")
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(func() {
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	})
	scope := t.Name()
	RememberClaudeThreadAuth(scope, "message-a", "credential-a")
	key := claudeThreadAuthKey(scope, "message-a")
	claudeThreadAuthCache.Lock()
	delete(claudeThreadAuthCache.entries, key)
	claudeThreadAuthCache.Unlock()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	if errConfigure := kvstore.ConfigureLocal(true, path); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	authID, found, errLookup := ClaudeThreadAuth(context.Background(), scope, "message-a")
	if errLookup != nil || !found || authID != "credential-a" {
		t.Fatalf("restored owner=%q, found=%v, error=%v", authID, found, errLookup)
	}
	if _, foreign, errForeign := ClaudeThreadAuth(context.Background(), scope+"-other", "message-a"); errForeign != nil || foreign {
		t.Fatalf("owner crossed caller scopes: found=%v, error=%v", foreign, errForeign)
	}
}

func TestClaudeThreadAuthMemoryIsBounded(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	scope := t.Name()
	for i := 0; i <= claudeThreadAuthLimit; i++ {
		RememberClaudeThreadAuth(scope, "message-"+strconv.Itoa(i), "credential-a")
	}
	if _, found, errLookup := ClaudeThreadAuth(context.Background(), scope, "message-0"); errLookup != nil || found {
		t.Fatalf("oldest owner was not evicted: found=%v, error=%v", found, errLookup)
	}
	if authID, found, errLookup := ClaudeThreadAuth(context.Background(), scope, "message-"+strconv.Itoa(claudeThreadAuthLimit)); errLookup != nil || !found || authID != "credential-a" {
		t.Fatalf("newest owner was lost: auth=%q, found=%v, error=%v", authID, found, errLookup)
	}
}

func TestClaudeThreadAuthMemoryExpiration(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	scope := t.Name()
	RememberClaudeThreadAuth(scope, "message-a", "credential-a")
	key := claudeThreadAuthKey(scope, "message-a")
	claudeThreadAuthCache.Lock()
	entry := claudeThreadAuthCache.entries[key]
	entry.expiresAt = time.Unix(0, 0)
	claudeThreadAuthCache.entries[key] = entry
	claudeThreadAuthCache.Unlock()
	if _, found, errLookup := ClaudeThreadAuth(context.Background(), scope, "message-a"); errLookup != nil || found {
		t.Fatalf("expired owner remained routable: found=%v, error=%v", found, errLookup)
	}
}
