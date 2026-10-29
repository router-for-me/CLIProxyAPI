package synthesizer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAuthRefreshHandledPolicyErrorCannotFallBackToBuiltin(t *testing.T) {
	failure := errors.New("unsupported plugin refresh policy")
	dir := t.TempDir()
	ctx := &SynthesisContext{Config: &config.Config{}, AuthDir: dir, Now: time.Now(), PluginAuthParser: multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		return nil, true, failure
	})}
	auths, err := SynthesizeAuthFile(ctx, filepath.Join(dir, "codex.json"), []byte(`{"type":"codex","access_token":"synthetic","expired":"2000-01-01T00:00:00Z"}`))
	if !errors.Is(err, failure) || len(auths) != 0 {
		t.Fatalf("invalid plugin policy enabled fallback auth: err=%v count=%d", err, len(auths))
	}
}
