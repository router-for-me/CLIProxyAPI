package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type homeSelectedFormatExecutor struct {
	forceMappingAliasChangeExecutor
	seenAuthID string
}

func (e *homeSelectedFormatExecutor) RequestToFormatWithAuth(auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	if auth == nil {
		return ""
	}
	e.seenAuthID = auth.ID
	return sdktranslator.FormatOpenAIResponse
}

func TestHomeAfterAuthFormatResolverReceivesRuntimeAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(&accountedAliasTargetDispatcher{}, executionregistry.New(), 1)
	executor := &homeSelectedFormatExecutor{}
	manager.RegisterExecutor(executor)
	t.Cleanup(func() { manager.CloseExecutionSession(t.Name()) })
	var reported sdktranslator.Format
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: t.Name(), cliproxyexecutor.PinnedAuthMetadataKey: "non-force-alias-auth"},
		RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			reported = req.ToFormat
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(t.Context())
	if _, err := manager.Execute(ctx, []string{"force-mapping"}, cliproxyexecutor.Request{Model: "alias-a"}, opts); err != nil {
		t.Fatal(err)
	}
	if executor.seenAuthID != "non-force-alias-auth" || reported != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("resolver auth = %q, interceptor format = %q", executor.seenAuthID, reported)
	}
}
