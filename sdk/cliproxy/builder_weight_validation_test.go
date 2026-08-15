package cliproxy

import (
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuilderBuildRejectsInvalidWithConfigCredentialWeight(t *testing.T) {
	invalidWeight := internalconfig.MaxCredentialWeight + 1
	cfg := &internalconfig.Config{
		ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey: "claude-key",
			Weight: &invalidWeight,
		}},
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(t.TempDir() + "/config.yaml").
		Build()
	if errBuild == nil {
		t.Fatal("Build() accepted an invalid credential weight")
	}
	if service != nil {
		t.Fatal("Build() returned a service for an invalid credential weight")
	}
	if !strings.Contains(errBuild.Error(), "cliproxy: validate credential weights: claude-api-key[0].weight") {
		t.Fatalf("Build() error = %q, want contextual credential weight path", errBuild)
	}
}

func TestBuilderBuildRejectsInvalidWithConfigVertexADC(t *testing.T) {
	cfg := &internalconfig.Config{
		VertexADC: []internalconfig.VertexADCConfig{{
			ProjectID: "  ",
			Location:  "global",
		}},
	}

	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(t.TempDir() + "/config.yaml").
		Build()
	if errBuild == nil {
		t.Fatal("Build() accepted a vertex-adc entry without project-id")
	}
	if service != nil {
		t.Fatal("Build() returned a service for an invalid vertex-adc entry")
	}
	if !strings.Contains(errBuild.Error(), "cliproxy: validate vertex-adc entries: vertex-adc[0]: project-id is required") {
		t.Fatalf("Build() error = %q, want contextual vertex-adc validation path", errBuild)
	}
}
