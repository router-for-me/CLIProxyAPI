package auth

import "testing"

func TestHomeForceMappingAliasResult(t *testing.T) {
	auth := &Auth{
		Provider: "xai",
		Attributes: map[string]string{
			homeUpstreamModelAttributeKey: "grok-4.5",
			homeForceMappingAttributeKey:  "true",
			homeOriginalAliasAttributeKey: "grok-latest",
		},
	}

	result := homeForceMappingAliasResult(auth, "grok-latest")
	if result.UpstreamModel != "grok-4.5" || !result.ForceMapping || result.OriginalAlias != "grok-latest" {
		t.Fatalf("homeForceMappingAliasResult() = %+v", result)
	}
}

func TestHomeForceMappingAliasResultRequiresExplicitFlag(t *testing.T) {
	auth := &Auth{
		Provider: "xai",
		Attributes: map[string]string{
			homeUpstreamModelAttributeKey: "grok-4.5",
			homeOriginalAliasAttributeKey: "grok-latest",
		},
	}

	result := homeForceMappingAliasResult(auth, "grok-latest")
	if result.ForceMapping || result.OriginalAlias != "" {
		t.Fatalf("homeForceMappingAliasResult() = %+v, want no force mapping", result)
	}
}
