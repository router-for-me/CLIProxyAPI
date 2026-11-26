package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func startAntigravityMockHomeRefreshServer(t *testing.T, reply func(command, key string) []byte) config.HomeConfig {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("net.Listen() error = %v", errListen)
	}
	var kvMu sync.Mutex
	kvStore := make(map[string][]byte)
	var serverWG sync.WaitGroup
	serverWG.Add(1)
	go func() {
		defer serverWG.Done()
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			serverWG.Add(1)
			go func(c net.Conn) {
				defer serverWG.Done()
				defer func() {
					_ = c.Close()
				}()
				reader := bufio.NewReader(c)
				for {
					var count int
					if _, errRead := fmt.Fscanf(reader, "*%d\n", &count); errRead != nil || count < 1 {
						return
					}
					args := make([]string, count)
					for i := range args {
						var length int
						if _, errRead := fmt.Fscanf(reader, "$%d\n", &length); errRead != nil || length < 0 {
							return
						}
						raw := make([]byte, length+2)
						if _, errRead := io.ReadFull(reader, raw); errRead != nil {
							return
						}
						args[i] = string(raw[:length])
					}
					response := "+OK\r\n"
					switch command := strings.ToUpper(args[0]); command {
					case "HELLO":
						response = "%2\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n"
					case "SUBSCRIBE":
						response = ">3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
					case "PING":
						response = "+PONG\r\n"
					case "SET":
						if len(args) >= 3 {
							kvMu.Lock()
							kvStore[args[1]] = []byte(args[2])
							kvMu.Unlock()
						}
						response = "+OK\r\n"
					case "EXPIRE", "PEXPIRE":
						response = ":1\r\n"
					case "GET":
						var payload []byte
						if args[1] == "config" {
							payload = []byte("{}")
						} else if strings.HasPrefix(strings.TrimSpace(args[1]), "{") {
							payload = reply(command, args[1])
						} else {
							kvMu.Lock()
							if v, ok := kvStore[args[1]]; ok {
								payload = append([]byte(nil), v...)
							}
							kvMu.Unlock()
						}
						response = "$-1\r\n"
						if payload != nil {
							response = fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
						}
					}
					if _, errWrite := io.WriteString(c, response); errWrite != nil {
						return
					}
				}
			}(conn)
		}
	}()

	homeCfg := config.HomeConfig{
		Enabled:                 true,
		Host:                    "127.0.0.1",
		Port:                    listener.Addr().(*net.TCPAddr).Port,
		DisableClusterDiscovery: true,
	}
	client := home.New(homeCfg)
	previous := home.Current()
	home.SetCurrent(client)

	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- client.RunConfigSubscriberLifetime(ctx, func([]byte) error { return nil }, func() { close(ready) })
	}()
	t.Cleanup(func() {
		cancel()
		client.Close()
		home.SetCurrent(previous)
		_ = listener.Close()
		serverWG.Wait()
	})
	select {
	case <-ready:
		return homeCfg
	case errClient := <-done:
		t.Fatalf("mock Home subscriber failed: %v", errClient)
	case <-time.After(10 * time.Second):
		t.Fatal("mock Home subscriber was not ready")
	}
	return homeCfg
}

func TestAntigravityHomeRefreshPreservesSignatureCacheScope(t *testing.T) {
	cache.ClearSignatureCache("")
	t.Cleanup(func() { cache.ClearSignatureCache("") })

	var mu sync.Mutex
	var replyPayload string
	homeCfg := startAntigravityMockHomeRefreshServer(t, func(_, _ string) []byte {
		mu.Lock()
		defer mu.Unlock()
		return []byte(replyPayload)
	})
	setReply := func(payload string) {
		mu.Lock()
		replyPayload = payload
		mu.Unlock()
	}

	exec := NewAntigravityExecutor(&config.Config{Home: homeCfg})
	expired := time.Now().Add(-time.Minute).Format(time.RFC3339)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)

	t.Run("same refresh token preserves scope across Refresh and ensureAccessTokenWithOptions and repeated rotations", func(t *testing.T) {
		preAuth := &cliproxyauth.Auth{
			ID:       "ag-home-sig",
			Provider: "antigravity",
			Index:    "idx-1",
			Metadata: map[string]any{
				"access_token":  "at-initial-1",
				"refresh_token": "rt-same",
				"project_id":    "proj-1",
				"expired":       expired,
			},
		}
		scopeBefore := antigravitySignatureCacheScope(preAuth)
		const model = "claude-sonnet-4-6"
		const thinkingText = "cached before home refresh"
		sig := signaturetest.AntigravityCAQSWithChannel(37)
		if !cache.CacheSignatureBestEffort(cache.WithSignatureCacheScope(context.Background(), scopeBefore), model, thinkingText, sig) {
			t.Fatal("failed to seed pre-refresh signature cache")
		}

		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-sig","provider":"antigravity","status":"active","metadata":{"access_token":"at-rotated-2","refresh_token":"rt-same","project_id":"proj-1","expires_in":3600,"expired":%q}},"auth_index":"idx-1"}`, future))

		refreshed, errRefresh := exec.Refresh(context.Background(), preAuth)
		if errRefresh != nil {
			t.Fatalf("Refresh() error = %v", errRefresh)
		}
		if gotScope := antigravitySignatureCacheScope(refreshed); gotScope != scopeBefore {
			t.Fatalf("Refresh() via Home changed antigravitySignatureCacheScope: before=%q after=%q (grant=%q)", scopeBefore, gotScope, refreshed.Metadata[antigravitySignatureGrantScopeMetadataKey])
		}
		gotSig, errSig := cache.GetCachedSignatureRequired(cache.WithSignatureCacheScope(context.Background(), antigravitySignatureCacheScope(refreshed)), model, thinkingText)
		if errSig != nil || gotSig != sig {
			t.Fatalf("GetCachedSignatureRequired() after Refresh() = %q, %v; want %q", gotSig, errSig, sig)
		}

		// Verify MergeRefreshedAuth preserves the rebound grant metadata.
		merged := cliproxyauth.MergeRefreshedAuth(preAuth, preAuth, refreshed)
		if gotScope := antigravitySignatureCacheScope(merged); gotScope != scopeBefore {
			t.Fatalf("MergeRefreshedAuth() changed antigravitySignatureCacheScope: before=%q after=%q", scopeBefore, gotScope)
		}

		// Verify ensureAccessTokenWithOptions also preserves the scope.
		_, updatedEnsure, _, errEnsure := exec.ensureAccessTokenWithOptions(context.Background(), preAuth, false)
		if errEnsure != nil {
			t.Fatalf("ensureAccessTokenWithOptions() error = %v", errEnsure)
		}
		if gotScope := antigravitySignatureCacheScope(updatedEnsure); gotScope != scopeBefore {
			t.Fatalf("ensureAccessTokenWithOptions() via Home changed antigravitySignatureCacheScope: before=%q after=%q (grant=%q)", scopeBefore, gotScope, updatedEnsure.Metadata[antigravitySignatureGrantScopeMetadataKey])
		}

		// Verify a second consecutive rotation preserves the original scope.
		merged.Metadata["expired"] = expired
		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-sig","provider":"antigravity","status":"active","metadata":{"access_token":"at-rotated-3","refresh_token":"rt-same","project_id":"proj-1","expires_in":3600,"expired":%q}},"auth_index":"idx-1"}`, future))
		rotatedAgain, errAgain := exec.Refresh(context.Background(), merged)
		if errAgain != nil {
			t.Fatalf("second Refresh() error = %v", errAgain)
		}
		if gotScope := antigravitySignatureCacheScope(rotatedAgain); gotScope != scopeBefore {
			t.Fatalf("second Refresh() via Home changed antigravitySignatureCacheScope: before=%q after=%q", scopeBefore, gotScope)
		}
	})

	t.Run("sanitized home response omitting refresh_token preserves scope for same email or account_id", func(t *testing.T) {
		preAuth := &cliproxyauth.Auth{
			ID:       "ag-home-sanitized",
			Provider: "antigravity",
			Index:    "idx-2",
			Metadata: map[string]any{
				"access_token": "at-san-1",
				"project_id":   "proj-san",
				"email":        "user@example.com",
				"expired":      expired,
			},
		}
		scopeBefore := antigravitySignatureCacheScope(preAuth)
		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-sanitized","provider":"antigravity","status":"active","metadata":{"access_token":"at-san-2","project_id":"proj-san","email":"user@example.com","expires_in":3600,"expired":%q}},"auth_index":"idx-2"}`, future))

		refreshed, errRefresh := exec.Refresh(context.Background(), preAuth)
		if errRefresh != nil {
			t.Fatalf("Refresh() error = %v", errRefresh)
		}
		if gotScope := antigravitySignatureCacheScope(refreshed); gotScope != scopeBefore {
			t.Fatalf("sanitized Refresh() changed antigravitySignatureCacheScope: before=%q after=%q", scopeBefore, gotScope)
		}

		_, updatedEnsure, _, errEnsure := exec.ensureAccessTokenWithOptions(context.Background(), preAuth, false)
		if errEnsure != nil {
			t.Fatalf("ensureAccessTokenWithOptions() error = %v", errEnsure)
		}
		if gotScope := antigravitySignatureCacheScope(updatedEnsure); gotScope != scopeBefore {
			t.Fatalf("sanitized ensureAccessTokenWithOptions() changed antigravitySignatureCacheScope: before=%q after=%q", scopeBefore, gotScope)
		}

		preAccountID := &cliproxyauth.Auth{
			ID:       "ag-home-sanitized-acct",
			Provider: "antigravity",
			Index:    "idx-2b",
			Metadata: map[string]any{
				"access_token": "at-acct-1",
				"project_id":   "proj-san",
				"account_id":   "acct-123",
				"expired":      expired,
			},
		}
		scopeAccountBefore := antigravitySignatureCacheScope(preAccountID)
		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-sanitized-acct","provider":"antigravity","status":"active","metadata":{"access_token":"at-acct-2","project_id":"proj-san","account_id":"acct-123","expires_in":3600,"expired":%q}},"auth_index":"idx-2b"}`, future))
		refreshedAcct, errAcct := exec.Refresh(context.Background(), preAccountID)
		if errAcct != nil {
			t.Fatalf("account_id Refresh() error = %v", errAcct)
		}
		if gotScope := antigravitySignatureCacheScope(refreshedAcct); gotScope != scopeAccountBefore {
			t.Fatalf("account_id Refresh() changed antigravitySignatureCacheScope: before=%q after=%q", scopeAccountBefore, gotScope)
		}
	})

	t.Run("shared project_id without account identity does not leak cached signature across replacement", func(t *testing.T) {
		accountA := &cliproxyauth.Auth{
			ID:       "ag-home-shared-proj",
			Provider: "antigravity",
			Index:    "idx-shared",
			Metadata: map[string]any{
				"access_token": "at-account-a",
				"project_id":   "shared-proj",
				"expired":      expired,
			},
		}
		scopeA := antigravitySignatureCacheScope(accountA)
		const model = "claude-sonnet-4-6"
		const thinkingText = "account A private thinking block"
		sigA := signaturetest.AntigravityCAQSWithChannel(41)
		if !cache.CacheSignatureBestEffort(cache.WithSignatureCacheScope(context.Background(), scopeA), model, thinkingText, sigA) {
			t.Fatal("failed to seed account A signature cache")
		}

		// Home returns a different account token under the same ID and shared project_id,
		// without refresh_token, email, account_id, enum scope, or authoritative grant binding.
		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-shared-proj","provider":"antigravity","status":"active","metadata":{"access_token":"at-account-b","project_id":"shared-proj","expires_in":3600,"expired":%q}},"auth_index":"idx-shared"}`, future))

		refreshedB, errRefresh := exec.Refresh(context.Background(), accountA)
		if errRefresh != nil {
			t.Fatalf("Refresh() error = %v", errRefresh)
		}
		scopeB := antigravitySignatureCacheScope(refreshedB)
		if scopeB == scopeA {
			t.Fatalf("shared project_id without account identity reused signature scope: %q", scopeB)
		}
		gotSig, errSig := cache.GetCachedSignatureRequired(cache.WithSignatureCacheScope(context.Background(), scopeB), model, thinkingText)
		if errSig != nil {
			t.Fatalf("GetCachedSignatureRequired() error = %v", errSig)
		}
		if gotSig != "" {
			t.Fatalf("account B unexpectedly read account A cached signature: %q", gotSig)
		}

		_, ensureB, _, errEnsure := exec.ensureAccessTokenWithOptions(context.Background(), accountA, false)
		if errEnsure != nil {
			t.Fatalf("ensureAccessTokenWithOptions() error = %v", errEnsure)
		}
		if gotScope := antigravitySignatureCacheScope(ensureB); gotScope == scopeA {
			t.Fatalf("ensureAccessTokenWithOptions() leaked scope across shared project_id without account identity: %q", gotScope)
		}
	})

	t.Run("different refresh_token or account metadata does not carry over grant", func(t *testing.T) {
		preAuth := &cliproxyauth.Auth{
			ID:       "ag-home-replaced",
			Provider: "antigravity",
			Index:    "idx-3",
			Metadata: map[string]any{
				"access_token":  "at-old",
				"refresh_token": "rt-account-a",
				"project_id":    "proj-a",
				"email":         "a@example.com",
				"expired":       expired,
			},
		}
		scopeBefore := antigravitySignatureCacheScope(preAuth)

		// Different refresh_token must isolate even if project_id matches.
		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-replaced","provider":"antigravity","status":"active","metadata":{"access_token":"at-new","refresh_token":"rt-account-b","project_id":"proj-a","email":"a@example.com","expires_in":3600,"expired":%q}},"auth_index":"idx-3"}`, future))
		refreshed, errRefresh := exec.Refresh(context.Background(), preAuth)
		if errRefresh != nil {
			t.Fatalf("Refresh() error = %v", errRefresh)
		}
		if gotScope := antigravitySignatureCacheScope(refreshed); gotScope == scopeBefore {
			t.Fatalf("Refresh() leaked signature scope across different refresh_token: %q", gotScope)
		}

		// Omitted refresh_token with different email must also isolate.
		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-replaced","provider":"antigravity","status":"active","metadata":{"access_token":"at-new-2","project_id":"proj-a","email":"b@example.com","expires_in":3600,"expired":%q}},"auth_index":"idx-3"}`, future))
		_, updatedEnsure, _, errEnsure := exec.ensureAccessTokenWithOptions(context.Background(), preAuth, false)
		if errEnsure != nil {
			t.Fatalf("ensureAccessTokenWithOptions() error = %v", errEnsure)
		}
		if gotScope := antigravitySignatureCacheScope(updatedEnsure); gotScope == scopeBefore {
			t.Fatalf("ensureAccessTokenWithOptions() leaked signature scope across different email: %q", gotScope)
		}
	})

	t.Run("home authoritative bound grant on refreshed auth is preserved without overwriting", func(t *testing.T) {
		preAuth := &cliproxyauth.Auth{
			ID:       "ag-home-authoritative",
			Provider: "antigravity",
			Index:    "idx-4",
			Metadata: map[string]any{
				"access_token": "at-auth-1",
				"project_id":   "proj-auth",
				"expired":      expired,
				antigravitySignatureGrantScopeMetadataKey:   "grant:home-old-account",
				antigravitySignatureGrantBindingMetadataKey: antigravityCredentialScope("grant:", "\x00at-auth-1"),
			},
		}
		newBinding := antigravityCredentialScope("grant:", "\x00at-auth-2")
		setReply(fmt.Sprintf(`{"auth":{"id":"ag-home-authoritative","provider":"antigravity","status":"active","metadata":{"access_token":"at-auth-2","project_id":"proj-auth","expires_in":3600,"expired":%q,%q:"grant:home-new-account",%q:%q}},"auth_index":"idx-4"}`, future, antigravitySignatureGrantScopeMetadataKey, antigravitySignatureGrantBindingMetadataKey, newBinding))

		refreshed, errRefresh := exec.Refresh(context.Background(), preAuth)
		if errRefresh != nil {
			t.Fatalf("Refresh() error = %v", errRefresh)
		}
		if got := refreshed.Metadata[antigravitySignatureGrantScopeMetadataKey]; got != "grant:home-new-account" {
			t.Fatalf("authoritative Home grant = %v, want grant:home-new-account", got)
		}
		if antigravitySignatureCacheScope(refreshed) == antigravitySignatureCacheScope(preAuth) {
			t.Fatal("authoritative Home grant change reused previous scope")
		}
	})
}

func TestAntigravityHomeDispatchEnumScopeStabilizesSignatureCacheScope(t *testing.T) {
	scopeA := strings.Repeat("a", 64)
	scopeB := strings.Repeat("b", 64)

	dispatch1 := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeA,
		},
		Metadata: map[string]any{
			"access_token": "at-dispatch-1",
			"project_id":   "proj-dispatch",
		},
	}
	dispatch2 := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeA,
		},
		Metadata: map[string]any{
			"access_token": "at-dispatch-2",
			"project_id":   "proj-dispatch",
		},
	}
	replaced := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeB,
		},
		Metadata: map[string]any{
			"access_token": "at-dispatch-3",
			"project_id":   "proj-dispatch",
		},
	}

	s1 := antigravitySignatureCacheScope(dispatch1)
	s2 := antigravitySignatureCacheScope(dispatch2)
	s3 := antigravitySignatureCacheScope(replaced)
	if s1 == "" || s1 != s2 {
		t.Fatalf("Home dispatch access_token rotation changed signature scope: s1=%q s2=%q", s1, s2)
	}
	if s3 == s1 {
		t.Fatalf("Home dispatch with different antigravity_model_enum_scope reused signature scope: %q", s3)
	}

	// Rolling-upgrade compatibility: old Home (enum scope only) and new Home
	// (enum scope + authoritative bound home-grant) must yield the exact same
	// signature cache scope in both directions (old->new and new->old), including
	// when old Home forwards a stale DB binding written by a new Home node.
	cache.ClearSignatureCache("")
	t.Cleanup(func() { cache.ClearSignatureCache("") })
	homeGrant := antigravityCredentialScope("home-grant:", scopeA)
	newHomeDispatch := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeA,
		},
		Metadata: map[string]any{
			"access_token": "at-dispatch-1",
			"project_id":   "proj-dispatch",
			antigravitySignatureGrantScopeMetadataKey:   homeGrant,
			antigravitySignatureGrantBindingMetadataKey: antigravityCredentialScope("grant:", "\x00at-dispatch-1"),
		},
	}
	newHomeRotated := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeA,
		},
		Metadata: map[string]any{
			"access_token": "at-dispatch-2",
			"project_id":   "proj-dispatch",
			antigravitySignatureGrantScopeMetadataKey:   homeGrant,
			antigravitySignatureGrantBindingMetadataKey: antigravityCredentialScope("grant:", "\x00at-dispatch-2"),
		},
	}
	oldHomeWithStaleDBBinding := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeA,
		},
		Metadata: map[string]any{
			"access_token": "at-dispatch-2",
			"project_id":   "proj-dispatch",
			antigravitySignatureGrantScopeMetadataKey:   homeGrant,
			antigravitySignatureGrantBindingMetadataKey: antigravityCredentialScope("grant:", "rt-1\x00at-dispatch-2"),
		},
	}

	sNew := antigravitySignatureCacheScope(newHomeDispatch)
	sNewRotated := antigravitySignatureCacheScope(newHomeRotated)
	sOldStaleBinding := antigravitySignatureCacheScope(oldHomeWithStaleDBBinding)
	if s1 != sNew || sNew != sNewRotated || sNewRotated != sOldStaleBinding {
		t.Fatalf("rolling-upgrade scopes diverged: old=%q new=%q newRotated=%q oldStaleBinding=%q", s1, sNew, sNewRotated, sOldStaleBinding)
	}

	const model = "claude-sonnet-4-6"
	sigOldToNew := signaturetest.AntigravityCAQSWithChannel(43)
	if !cache.CacheSignatureBestEffort(cache.WithSignatureCacheScope(context.Background(), s1), model, "old-to-new", sigOldToNew) {
		t.Fatal("failed to seed old->new signature cache")
	}
	if got, errGet := cache.GetCachedSignatureRequired(cache.WithSignatureCacheScope(context.Background(), sNewRotated), model, "old-to-new"); errGet != nil || got != sigOldToNew {
		t.Fatalf("old->new signature lookup = %q, %v; want %q", got, errGet, sigOldToNew)
	}

	sigNewToOld := signaturetest.AntigravityCAQSWithChannel(47)
	if !cache.CacheSignatureBestEffort(cache.WithSignatureCacheScope(context.Background(), sNew), model, "new-to-old", sigNewToOld) {
		t.Fatal("failed to seed new->old signature cache")
	}
	if got, errGet := cache.GetCachedSignatureRequired(cache.WithSignatureCacheScope(context.Background(), sOldStaleBinding), model, "new-to-old"); errGet != nil || got != sigNewToOld {
		t.Fatalf("new->old signature lookup = %q, %v; want %q", got, errGet, sigNewToOld)
	}

	// Rolling-upgrade after a verified refresh that also rotates refresh_token (rt-1 -> rt-2):
	// Both new and old Home read the exact same post-refresh DB row where the enum scope
	// changed from scopeA (rt-1) to scopeB (rt-2), while the DB retains homeGrant (from rt-1)
	// plus cpa_signature_cache_grant_home_binding bound to scopeB.
	newHomeAfterRTRotation := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeB,
		},
		Metadata: map[string]any{
			"access_token": "at-rt-rotated",
			"project_id":   "proj-dispatch",
			antigravitySignatureGrantScopeMetadataKey:   homeGrant,
			antigravitySignatureGrantBindingMetadataKey: antigravityCredentialScope("grant:", "\x00at-rt-rotated"),
		},
	}
	oldHomeAfterRTRotation := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeB,
		},
		Metadata: map[string]any{
			"access_token": "at-rt-rotated",
			"project_id":   "proj-dispatch",
			antigravitySignatureGrantScopeMetadataKey:       homeGrant,
			antigravitySignatureGrantBindingMetadataKey:     antigravityCredentialScope("grant:", "rt-2\x00at-rt-rotated"),
			antigravitySignatureGrantHomeBindingMetadataKey: antigravityCredentialScope("home-bind:", scopeB),
		},
	}
	sNewAfterRT := antigravitySignatureCacheScope(newHomeAfterRTRotation)
	sOldAfterRT := antigravitySignatureCacheScope(oldHomeAfterRTRotation)
	if sNewAfterRT != s1 || sOldAfterRT != s1 {
		t.Fatalf("post-rt-rotation rolling-upgrade scopes diverged: initial=%q new=%q old=%q", s1, sNewAfterRT, sOldAfterRT)
	}

	sigRTOldToNew := signaturetest.AntigravityCAQSWithChannel(53)
	if !cache.CacheSignatureBestEffort(cache.WithSignatureCacheScope(context.Background(), sOldAfterRT), model, "rt-old-to-new", sigRTOldToNew) {
		t.Fatal("failed to seed rt-rotation old->new signature cache")
	}
	if got, errGet := cache.GetCachedSignatureRequired(cache.WithSignatureCacheScope(context.Background(), sNewAfterRT), model, "rt-old-to-new"); errGet != nil || got != sigRTOldToNew {
		t.Fatalf("rt-rotation old->new signature lookup = %q, %v; want %q", got, errGet, sigRTOldToNew)
	}

	sigRTNewToOld := signaturetest.AntigravityCAQSWithChannel(59)
	if !cache.CacheSignatureBestEffort(cache.WithSignatureCacheScope(context.Background(), sNewAfterRT), model, "rt-new-to-old", sigRTNewToOld) {
		t.Fatal("failed to seed rt-rotation new->old signature cache")
	}
	if got, errGet := cache.GetCachedSignatureRequired(cache.WithSignatureCacheScope(context.Background(), sOldAfterRT), model, "rt-new-to-old"); errGet != nil || got != sigRTNewToOld {
		t.Fatalf("rt-rotation new->old signature lookup = %q, %v; want %q", got, errGet, sigRTNewToOld)
	}

	// Stale home_binding from scopeB must fail closed when the credential is replaced (scopeC).
	scopeC := strings.Repeat("c", 64)
	replacedWithStaleHomeBinding := &cliproxyauth.Auth{
		ID:       "ag-home-dispatch",
		Provider: "antigravity",
		Attributes: map[string]string{
			cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scopeC,
		},
		Metadata: map[string]any{
			"access_token": "at-replaced",
			"project_id":   "proj-dispatch",
			antigravitySignatureGrantScopeMetadataKey:       homeGrant,
			antigravitySignatureGrantBindingMetadataKey:     antigravityCredentialScope("grant:", "rt-2\x00at-rt-rotated"),
			antigravitySignatureGrantHomeBindingMetadataKey: antigravityCredentialScope("home-bind:", scopeB),
		},
	}
	sReplaced := antigravitySignatureCacheScope(replacedWithStaleHomeBinding)
	if sReplaced == s1 {
		t.Fatalf("stale home_binding leaked previous scope after credential replacement: %q", sReplaced)
	}
	if got, errGet := cache.GetCachedSignatureRequired(cache.WithSignatureCacheScope(context.Background(), sReplaced), model, "rt-new-to-old"); errGet != nil || got != "" {
		t.Fatalf("replaced credential read previous signature = %q, %v; want miss", got, errGet)
	}
}
