package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestBuildAWSBedrockConfigModels_UsesAliasAndName(t *testing.T) {
	entry := &config.AWSBedrockKey{
		Models: []config.AWSBedrockModel{
			{
				Name:  "us.deepseek.r1-v1:0",
				Alias: "deepseek-r1",
			},
		},
	}

	models := buildAWSBedrockConfigModels(entry)
	if len(models) != 1 {
		t.Fatalf("models len = %d, want 1", len(models))
	}
	model := models[0]
	if model == nil {
		t.Fatal("model is nil")
	}
	if model.ID != "deepseek-r1" {
		t.Fatalf("id = %q, want %q", model.ID, "deepseek-r1")
	}
	if model.Name != "us.deepseek.r1-v1:0" {
		t.Fatalf("name = %q, want %q", model.Name, "us.deepseek.r1-v1:0")
	}
	if model.OwnedBy != "aws-bedrock" {
		t.Fatalf("owned_by = %q, want %q", model.OwnedBy, "aws-bedrock")
	}
}
