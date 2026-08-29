package auth

import "testing"

func TestMergeExistingAuthMetadataPreservesDisabledState(t *testing.T) {
	target := &Auth{Metadata: map[string]any{"type": "claude"}}
	MergeExistingAuthMetadata(target, map[string]any{"disabled": true, "prefix": "team"})

	if !target.Disabled {
		t.Fatal("Disabled = false, want true")
	}
	if disabled, _ := target.Metadata["disabled"].(bool); !disabled {
		t.Fatalf("metadata disabled = %v, want true", target.Metadata["disabled"])
	}
	if target.Metadata["prefix"] != "team" {
		t.Fatalf("prefix = %v, want team", target.Metadata["prefix"])
	}
}

func TestMergeExistingAuthMetadataKeepsExplicitDisabledState(t *testing.T) {
	target := &Auth{Metadata: map[string]any{"disabled": false}}
	MergeExistingAuthMetadata(target, map[string]any{"disabled": true})

	if target.Disabled {
		t.Fatal("Disabled = true, want explicit false state")
	}
	if disabled, _ := target.Metadata["disabled"].(bool); disabled {
		t.Fatalf("metadata disabled = %v, want false", target.Metadata["disabled"])
	}
}
