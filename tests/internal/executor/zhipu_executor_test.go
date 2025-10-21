package executor_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestZhipuExecutor_IdentifierAndErrors(t *testing.T) {
	exec := executor.NewZhipuExecutor(&config.Config{})
	if exec.Identifier() != "zhipu" {
		t.Fatalf("identifier mismatch")
	}
	ctx := context.Background()
	_, err := exec.Execute(ctx, &coreauth.Auth{Attributes: map[string]string{"api_key":"k","base_url":"u"}}, sdkexec.Request{Model: "glm-4.5"}, sdkexec.Options{})
	if err == nil {
		t.Fatalf("expected error in alpha placeholder")
	}
}
