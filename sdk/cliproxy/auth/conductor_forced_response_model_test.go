package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestApplyForcedResponseModel(t *testing.T) {
	base := func() OAuthModelAliasResult {
		return OAuthModelAliasResult{UpstreamModel: "deepseek-chat", ForceMapping: false, OriginalAlias: ""}
	}

	t.Run("overrides when metadata present", func(t *testing.T) {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.ForcedResponseModelMetadataKey: "glm",
		}}
		r := base()
		applyForcedResponseModel(opts, &r)
		if !r.ForceMapping {
			t.Fatal("ForceMapping = false, want true")
		}
		if r.OriginalAlias != "glm" {
			t.Fatalf("OriginalAlias = %q, want %q", r.OriginalAlias, "glm")
		}
		if r.UpstreamModel != "deepseek-chat" {
			t.Fatalf("UpstreamModel = %q, want unchanged", r.UpstreamModel)
		}
	})

	t.Run("overrides existing force-mapping", func(t *testing.T) {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.ForcedResponseModelMetadataKey: "glm",
		}}
		r := OAuthModelAliasResult{ForceMapping: true, OriginalAlias: "deepseek-alias"}
		applyForcedResponseModel(opts, &r)
		if !r.ForceMapping || r.OriginalAlias != "glm" {
			t.Fatalf("expected forced override to glm, got ForceMapping=%v OriginalAlias=%q", r.ForceMapping, r.OriginalAlias)
		}
	})

	t.Run("no-op when metadata absent", func(t *testing.T) {
		r := base()
		applyForcedResponseModel(cliproxyexecutor.Options{}, &r)
		if r.ForceMapping || r.OriginalAlias != "" {
			t.Fatalf("expected no-op, got ForceMapping=%v OriginalAlias=%q", r.ForceMapping, r.OriginalAlias)
		}
	})

	t.Run("no-op when value empty", func(t *testing.T) {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.ForcedResponseModelMetadataKey: "   ",
		}}
		r := base()
		applyForcedResponseModel(opts, &r)
		if r.ForceMapping {
			t.Fatalf("expected no-op for empty model, got ForceMapping=%v", r.ForceMapping)
		}
	})

	t.Run("supports []byte value", func(t *testing.T) {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.ForcedResponseModelMetadataKey: []byte("glm"),
		}}
		r := base()
		applyForcedResponseModel(opts, &r)
		if !r.ForceMapping || r.OriginalAlias != "glm" {
			t.Fatalf("expected []byte to apply, got ForceMapping=%v OriginalAlias=%q", r.ForceMapping, r.OriginalAlias)
		}
	})

	t.Run("nil aliasResult does not panic", func(t *testing.T) {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.ForcedResponseModelMetadataKey: "glm",
		}}
		applyForcedResponseModel(opts, nil)
	})
}
