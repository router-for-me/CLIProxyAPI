package cliproxy

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// A Mistral credential with no openai-compatibility entry in config.yaml still
// has to advertise models, otherwise the auth registers zero models and the
// imported key is unusable. The list must track the model catalog so a catalog
// refresh reaches imported credentials.
func TestDefaultMistralCompatModels(t *testing.T) {
	catalog := registry.GetMistralModels()
	if len(catalog) == 0 {
		t.Fatal("expected Mistral definitions in the model catalog")
	}
	wantIDs := make(map[string]bool, len(catalog))
	catalogByID := make(map[string]*ModelInfo, len(catalog))
	for _, model := range catalog {
		wantIDs[model.ID] = false
		catalogByID[model.ID] = model
	}

	models := defaultMistralCompatModels()
	if len(models) != len(catalog) {
		t.Fatalf("model count = %d, want %d (one per catalog entry)", len(models), len(catalog))
	}

	for _, model := range models {
		if model == nil {
			t.Fatal("unexpected nil model")
		}
		seen, ok := wantIDs[model.ID]
		if !ok {
			t.Errorf("model %q is not in the catalog", model.ID)
			continue
		}
		if seen {
			t.Errorf("duplicate model %q", model.ID)
		}
		wantIDs[model.ID] = true

		if model.MetadataModelID != model.ID {
			t.Errorf("%s: MetadataModelID = %q, want the upstream ID", model.ID, model.MetadataModelID)
		}
		if model.Type != "openai-compatibility" {
			t.Errorf("%s: Type = %q, want openai-compatibility", model.ID, model.Type)
		}
		if model.OwnedBy != util.MistralProvider {
			t.Errorf("%s: OwnedBy = %q, want %s", model.ID, model.OwnedBy, util.MistralProvider)
		}
		if model.Thinking == nil || len(model.Thinking.Levels) == 0 {
			t.Errorf("%s: expected the compat thinking defaults, got %+v", model.ID, model.Thinking)
		}
		// Catalog metadata must survive registration so catalog updates
		// (descriptions, context lengths, limits) reach served models.
		source := catalogByID[model.ID]
		if model.Description != source.Description || model.Created != source.Created ||
			model.ContextLength != source.ContextLength || model.MaxCompletionTokens != source.MaxCompletionTokens {
			t.Errorf("%s: catalog metadata not preserved: got description=%q created=%d context=%d completion=%d, want %q %d %d %d",
				model.ID, model.Description, model.Created, model.ContextLength, model.MaxCompletionTokens,
				source.Description, source.Created, source.ContextLength, source.MaxCompletionTokens)
		}
	}

	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("catalog model %q is not served", id)
		}
	}
}

// newMistralFileAuth mirrors the auth the file synthesizer builds from a
// mistral-import credential.
func newMistralFileAuth(id, compatName string) *coreauth.Auth {
	providerKey := util.OpenAICompatibleProviderKey(compatName)
	return &coreauth.Auth{
		ID:       id,
		Provider: providerKey,
		Attributes: map[string]string{
			coreauth.AttributeSource:        "/auths/" + id,
			coreauth.AttributePath:          "/auths/" + id,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			"api_key":                       "sk-mistral-test",
			"base_url":                      util.MistralDefaultBaseURL,
			"compat_name":                   compatName,
			"provider_key":                  providerKey,
		},
		Metadata: map[string]any{"type": util.MistralProvider},
	}
}

func registeredModelIDs(t *testing.T, authID string) []string {
	t.Helper()
	models := GlobalModelRegistry().GetModelsForClient(authID)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestRegisterModelsForMistralAuthUsesCatalogDefaults(t *testing.T) {
	auth := newMistralFileAuth("mistral-default-catalog.json", util.MistralProvider)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	svc := &Service{cfg: &config.Config{}}
	svc.registerModelsForAuth(context.Background(), auth)

	want := make([]string, 0)
	for _, model := range registry.GetMistralModels() {
		want = append(want, model.ID)
	}
	sort.Strings(want)
	if got := registeredModelIDs(t, auth.ID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("registered models = %v, want the catalog %v", got, want)
	}
}

// Declaring an openai-compatibility entry named "mistral" replaces the catalog
// defaults for an imported credential, as config.example.yaml documents.
func TestRegisterModelsForMistralAuthConfigEntryOverridesDefaults(t *testing.T) {
	auth := newMistralFileAuth("mistral-config-override.json", util.MistralProvider)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	svc := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "mistral",
		BaseURL: util.MistralDefaultBaseURL,
		Models:  []config.OpenAICompatibilityModel{{Name: "magistral-medium-latest"}},
	}}}}
	svc.registerModelsForAuth(context.Background(), auth)

	if got := registeredModelIDs(t, auth.ID); strings.Join(got, ",") != "magistral-medium-latest" {
		t.Fatalf("registered models = %v, want only the config-declared model", got)
	}
}

// A config entry that declares no models overrides the defaults with nothing,
// so the credential is unregistered rather than silently keeping the catalog.
func TestRegisterModelsForMistralAuthEmptyConfigEntryUnregisters(t *testing.T) {
	auth := newMistralFileAuth("mistral-config-empty.json", util.MistralProvider)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	svc := &Service{cfg: &config.Config{}}
	svc.registerModelsForAuth(context.Background(), auth)
	if len(registeredModelIDs(t, auth.ID)) == 0 {
		t.Fatal("precondition: expected catalog defaults before the override")
	}

	svc.cfg = &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "mistral",
		BaseURL: util.MistralDefaultBaseURL,
	}}}
	svc.registerModelsForAuth(context.Background(), auth)
	if got := registeredModelIDs(t, auth.ID); len(got) != 0 {
		t.Fatalf("registered models = %v, want none", got)
	}
}

// Only compat_name "mistral" has built-in defaults; another name without a
// same-named config entry serves nothing.
func TestRegisterModelsForMistralAuthUnknownCompatNameServesNothing(t *testing.T) {
	auth := newMistralFileAuth("mistral-unknown-compat.json", "mistral-eu")
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	svc := &Service{cfg: &config.Config{}}
	svc.registerModelsForAuth(context.Background(), auth)
	if got := registeredModelIDs(t, auth.ID); len(got) != 0 {
		t.Fatalf("registered models = %v, want none", got)
	}
}

// A catalog refresh names sections by provider ("mistral"), but an imported
// credential is keyed "openai-compatible-mistral"; the refresh must still reach it.
func TestModelRefreshAffectsAuth(t *testing.T) {
	changed := func(providers ...string) map[string]bool {
		set := make(map[string]bool, len(providers))
		for _, provider := range providers {
			set[provider] = true
		}
		return set
	}
	mistralFile := newMistralFileAuth("mistral-refresh.json", util.MistralProvider)

	withoutCompatName := newMistralFileAuth("mistral-refresh-prefix.json", util.MistralProvider)
	delete(withoutCompatName.Attributes, "compat_name")

	configEntry := &coreauth.Auth{
		ID:       "config-mistral",
		Provider: util.OpenAICompatibleProviderKey(util.MistralProvider),
		Attributes: map[string]string{
			coreauth.AttributeSource: "config:mistral[token]",
			"compat_name":            "mistral",
			"provider_key":           util.OpenAICompatibleProviderKey(util.MistralProvider),
		},
	}
	native := &coreauth.Auth{ID: "meta-oauth.json", Provider: "meta"}

	cases := []struct {
		name    string
		auth    *coreauth.Auth
		changed map[string]bool
		want    bool
	}{
		{"imported credential on mistral change", mistralFile, changed("mistral"), true},
		{"imported credential falls back to the provider key prefix", withoutCompatName, changed("mistral"), true},
		{"imported credential ignores other providers", mistralFile, changed("meta", "xai"), false},
		{"config entry takes models from config, not the catalog", configEntry, changed("mistral"), false},
		{"native provider still matches by provider", native, changed("meta"), true},
		{"nil auth", nil, changed("mistral"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelRefreshAffectsAuth(tc.auth, tc.changed); got != tc.want {
				t.Fatalf("modelRefreshAffectsAuth = %v, want %v", got, tc.want)
			}
		})
	}
}
