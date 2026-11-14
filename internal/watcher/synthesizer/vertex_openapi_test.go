package synthesizer

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSynthesizeVertexOpenAPICredentialSettings(t *testing.T) {
	for _, raw := range []any{true, false, "true", "false", "1", "0"} {
		for _, locationKey := range []string{"openapi_location", "openapi-location"} {
			data, errMarshal := json.Marshal(map[string]any{"type": "vertex", "openapi": raw, locationKey: " global "})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			auths, errSynthesize := SynthesizeAuthFile(&SynthesisContext{Now: time.Unix(1700000000, 0)}, "vertex.json", data)
			if errSynthesize != nil || len(auths) != 1 {
				t.Fatalf("SynthesizeAuthFile = %v, %v", auths, errSynthesize)
			}
			enabled := raw == true || raw == "true" || raw == "1"
			if auths[0].Attributes["openapi"] != strconv.FormatBool(enabled) || auths[0].Attributes["openapi_location"] != "global" {
				t.Fatalf("credential attributes = %v", auths[0].Attributes)
			}
		}
	}
	for _, enabled := range []bool{true, false} {
		auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
			Config:      &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "test-key", OpenAPI: &enabled, OpenAPILocation: " global "}}},
			IDGenerator: NewStableIDGenerator(), Now: time.Unix(1700000000, 0),
		})
		if errSynthesize != nil || len(auths) != 1 {
			t.Fatalf("Synthesize config = %v, %v", auths, errSynthesize)
		}
		if auths[0].Attributes["openapi"] != strconv.FormatBool(enabled) || auths[0].Attributes["openapi_location"] != "global" {
			t.Fatalf("API key attributes = %v", auths[0].Attributes)
		}
	}
}

func TestSynthesizeVertexOpenAPIModelSettingsPreservedWithoutMetadata(t *testing.T) {
	for _, key := range []string{"models", "model_aliases", "model-aliases"} {
		for _, withAlias := range []bool{true, false} {
			for _, enabled := range []bool{true, false} {
				t.Run(key+"/alias="+strconv.FormatBool(withAlias)+"/openapi="+strconv.FormatBool(enabled), func(t *testing.T) {
					name := "custom-model"
					if !enabled {
						name = "partner/model"
					}
					rule := map[string]any{"name": name, "openapi": enabled}
					requested := name
					if withAlias {
						rule["alias"] = "public-model"
						requested = "public-model"
					}
					data, errMarshal := json.Marshal(map[string]any{"type": "vertex", "openapi": !enabled, key: []any{"unrelated-model", rule}})
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					auths, errSynthesize := SynthesizeAuthFile(&SynthesisContext{Now: time.Unix(1700000000, 0)}, "vertex.json", data)
					if errSynthesize != nil || len(auths) != 1 {
						t.Fatalf("SynthesizeAuthFile = %v, %v", auths, errSynthesize)
					}
					auth := &coreauth.Auth{Provider: "vertex", Attributes: auths[0].Attributes}
					if auth.Attributes["vertex_model_openapi"] == "" {
						t.Fatal("model OpenAPI settings were not preserved in attributes")
					}
					opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: requested}}
					if got := helps.ShouldExecuteVertexOpenAPIForRequest(nil, auth, name, opts); got != enabled {
						t.Fatalf("attribute-only model setting = %v, want %v", got, enabled)
					}
				})
			}
		}
	}
}
