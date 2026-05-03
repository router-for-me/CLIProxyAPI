package cliproxy

import (
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

// TestService_ConfigSnapshot_RaceFree pins the BLOCKER #1 invariant from
// the Codex Phase C round-6 review: helpers that read s.cfg
// (ensureExecutorsForAuthWithMode, registerModelsForAuth,
// resolveConfig*Key, oauthExcludedModels) must observe a consistent
// snapshot even while buildReloadCallback's cfgMu.Lock writer races
// them. Phase C #23 routed mgmt commits through buildReloadCallback so
// this race is now triggered per-mgmt-PUT, not just on rare file-watcher
// events.
//
// The test runs under go test -race and exercises the helpers
// concurrently with a writer that swaps s.cfg under cfgMu.Lock. The
// helpers each go through configSnapshot() (cfgMu.RLock) at function
// entry so the race detector should report no races. Without the
// snapshot pattern this would surface a write-vs-direct-read race on
// s.cfg.
func TestService_ConfigSnapshot_RaceFree(t *testing.T) {
	s := &Service{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "key-A", BaseURL: "https://a.example.com"},
			},
			GeminiKey: []config.GeminiKey{
				{APIKey: "gem-A", BaseURL: "https://gem-a.example.com"},
			},
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "vc-A", BaseURL: "https://vc-a.example.com"},
			},
			CodexKey: []config.CodexKey{
				{APIKey: "cx-A", BaseURL: "https://cx-a.example.com"},
			},
			OAuthExcludedModels: map[string][]string{
				"claude": {"claude-old-model"},
			},
		},
	}

	auth := &coreauth.Auth{
		ID:       "auth-id",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "key-A",
			"base_url":  "https://a.example.com",
		},
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Reader pool: drive each helper that previously read s.cfg directly.
	// Each helper takes a configSnapshot() at entry and operates on the
	// snapshot, so concurrent writers are race-free.
	readers := []func(){
		func() { _ = resolveConfigClaudeKey(s.configSnapshot(), auth) },
		func() { _ = resolveConfigGeminiKey(s.configSnapshot(), auth) },
		func() { _ = resolveConfigVertexCompatKey(s.configSnapshot(), auth) },
		func() { _ = resolveConfigCodexKey(s.configSnapshot(), auth) },
		func() { _ = s.oauthExcludedModels(s.configSnapshot(), "claude", "oauth") },
	}
	for i := 0; i < 4; i++ {
		for _, fn := range readers {
			wg.Add(1)
			f := fn
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						f()
					}
				}
			}()
		}
	}

	// Writer: swap s.cfg under cfgMu.Lock (mirrors buildReloadCallback's
	// final write at line 525-527 of service.go after a hot-reload).
	wg.Add(1)
	go func() {
		defer wg.Done()
		flip := false
		for {
			select {
			case <-stop:
				return
			default:
				flip = !flip
				suffix := "A"
				if flip {
					suffix = "B"
				}
				newCfg := &config.Config{
					ClaudeKey: []config.ClaudeKey{
						{APIKey: "key-" + suffix, BaseURL: "https://" + suffix + ".example.com"},
					},
					GeminiKey: []config.GeminiKey{
						{APIKey: "gem-" + suffix, BaseURL: "https://gem-" + suffix + ".example.com"},
					},
					VertexCompatAPIKey: []config.VertexCompatKey{
						{APIKey: "vc-" + suffix, BaseURL: "https://vc-" + suffix + ".example.com"},
					},
					CodexKey: []config.CodexKey{
						{APIKey: "cx-" + suffix, BaseURL: "https://cx-" + suffix + ".example.com"},
					},
					OAuthExcludedModels: map[string][]string{
						"claude": {"claude-" + suffix + "-model"},
					},
				}
				s.cfgMu.Lock()
				s.cfg = newCfg
				s.cfgMu.Unlock()
			}
		}
	}()

	time.Sleep(80 * time.Millisecond)
	close(stop)
	wg.Wait()
}
