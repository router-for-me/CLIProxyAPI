package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyCodexIdentityConfuseBodyDoesNotCopyEntireDocumentPerField(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation budget")
	}

	const transcriptSize = 2 << 20
	body := largeCodexIdentityBody(transcriptSize)
	cfg := &config.Config{
		Routing: config.RoutingConfig{SessionAffinity: true},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}

	result := testing.Benchmark(func(b *testing.B) {
		b.SetBytes(int64(len(body)))
		b.ReportAllocs()
		for b.Loop() {
			updated, _ := applyCodexIdentityConfuseBody(cfg, auth, body, body)
			if len(updated) < transcriptSize {
				b.Fatalf("confused body too small: %d", len(updated))
			}
		}
	})

	// One rewritten document is expected. Four sjson.SetBytes calls on a 2MiB
	// payload (prompt_cache_key, installation-id, turn-metadata, window-id)
	// were the live heap in production pprof (sjson.appendRawPaths).
	maxAllocated := int64(len(body)) * 2
	t.Logf("identity confuse allocated %d bytes per op for %d input bytes (%.2fx)", result.AllocedBytesPerOp(), len(body), float64(result.AllocedBytesPerOp())/float64(len(body)))
	if allocated := result.AllocedBytesPerOp(); allocated > maxAllocated {
		t.Fatalf("applyCodexIdentityConfuseBody allocated %d bytes for a %d-byte body (want <= %d). Each sjson.SetBytes copies the whole document.", allocated, len(body), maxAllocated)
	}
}

func largeCodexIdentityBody(transcriptSize int) []byte {
	content := strings.Repeat("x", transcriptSize)
	return []byte(`{"model":"gpt-5-codex","stream":true,"prompt_cache_key":"cache-1","client_metadata":{"x-codex-installation-id":"install-1","x-codex-turn-metadata":"{\"prompt_cache_key\":\"cache-1\",\"turn_id\":\"turn-1\",\"window_id\":\"cache-1:0\"}","x-codex-window-id":"cache-1:0"},"input":[{"type":"message","id":"msg-1","role":"user","content":"` + content + `"}]}`)
}
