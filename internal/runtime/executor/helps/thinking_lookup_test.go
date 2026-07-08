package helps

import (
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestThinkingLookupModelForConfigAPIKeyAuth(t *testing.T) {
	const provider = "claude"
	const requestedModel = "team-a/claude-sonnet"
	const upstreamModel = "claude-sonnet"

	reg := registry.GetGlobalRegistry()
	clientID := fmt.Sprintf("thinking-lookup-config-apikey-%d", time.Now().UnixNano())
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{
		{
			ID:          requestedModel,
			Object:      "model",
			Created:     time.Now().Unix(),
			OwnedBy:     "anthropic",
			Type:        "claude",
			Thinking:    &registry.ThinkingSupport{Levels: []string{"low", "xhigh"}},
			UserDefined: true,
		},
	})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ThinkingLookupModelMetadataKey: requestedModel,
		},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
		cliproxyauth.AttributeAPIKey:   "test-key",
		cliproxyauth.AttributeSource:   "config:claude[test-key]",
	}}

	if got := ThinkingLookupModelForAuth(auth, provider, upstreamModel, opts); got != requestedModel {
		t.Fatalf("ThinkingLookupModelForAuth() = %q, want %q", got, requestedModel)
	}

	auth.Attributes[cliproxyauth.AttributeAuthKind] = cliproxyauth.AuthKindOAuth
	if got := ThinkingLookupModelForAuth(auth, provider, upstreamModel, opts); got != upstreamModel {
		t.Fatalf("oauth auth lookup = %q, want upstream model %q", got, upstreamModel)
	}
}

func TestThinkingLookupModelForKeylessConfigAuth(t *testing.T) {
	const provider = "openai-compatibility"
	const requestedModel = "team-a/model"
	const upstreamModel = "model"

	reg := registry.GetGlobalRegistry()
	clientID := fmt.Sprintf("thinking-lookup-keyless-%d", time.Now().UnixNano())
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: requestedModel, Object: "model", Created: time.Now().Unix(), OwnedBy: "test", Type: "openai"}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeSource: "config:openai-compatibility[test]",
	}}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ThinkingLookupModelMetadataKey: requestedModel,
		},
	}

	if got := ThinkingLookupModelForAuth(auth, provider, upstreamModel, opts); got != requestedModel {
		t.Fatalf("ThinkingLookupModelForAuth() = %q, want keyless config lookup model", got)
	}
}

func TestThinkingLookupModelTrimsLookupModelBeforeRegistryLookup(t *testing.T) {
	const provider = "openai-compatibility"
	const requestedModel = "team-a/model"
	const upstreamModel = "model"

	reg := registry.GetGlobalRegistry()
	clientID := fmt.Sprintf("thinking-lookup-trim-%d", time.Now().UnixNano())
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: requestedModel, Object: "model", Created: time.Now().Unix(), OwnedBy: "test", Type: "openai"}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
		cliproxyauth.AttributeAPIKey:   "test-key",
		cliproxyauth.AttributeSource:   "config:openai-compatibility[test-key]",
	}}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ThinkingLookupModelMetadataKey: "  " + requestedModel + "  ",
		},
	}

	if got := ThinkingLookupModelForAuth(auth, provider, upstreamModel, opts); got != requestedModel {
		t.Fatalf("ThinkingLookupModelForAuth() = %q, want trimmed lookup model", got)
	}
}

func TestThinkingLookupModelIgnoresPreRouterRequestedModel(t *testing.T) {
	const provider = "openai-compatibility"
	const originalRequestedModel = "team-a/original-model"
	const routedUpstreamModel = "routed-model"

	reg := registry.GetGlobalRegistry()
	clientID := fmt.Sprintf("thinking-lookup-router-%d", time.Now().UnixNano())
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: originalRequestedModel, Object: "model", Created: time.Now().Unix(), OwnedBy: "test", Type: "openai"}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
		cliproxyauth.AttributeAPIKey:   "test-key",
		cliproxyauth.AttributeSource:   "config:openai-compatibility[test-key]",
	}}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: originalRequestedModel,
		},
	}

	if got := ThinkingLookupModelForAuth(auth, provider, routedUpstreamModel, opts); got != routedUpstreamModel {
		t.Fatalf("ThinkingLookupModelForAuth() = %q, want routed upstream model", got)
	}
}
