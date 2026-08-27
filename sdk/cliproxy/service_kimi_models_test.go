package cliproxy

import (
	"context"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuthKimiAPIKeyUsesConfiguredModelsOnly(t *testing.T) {
	catalog := internalregistry.GetKimiModels()
	if len(catalog) == 0 {
		t.Fatal("expected built-in Kimi catalog")
	}

	authID := "kimi-apikey-configured-models"
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	service := &Service{cfg: &config.Config{KimiKey: []config.KimiKey{{
		APIKey:  "sk-code",
		Service: internalconfig.KimiServiceCodingPlan,
		Models: []config.KimiModel{{
			Name:  "k3",
			Alias: "kimi-k3",
		}},
	}}}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "kimi",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "sk-code",
			coreauth.AttributeConfigIndex: "0",
			coreauth.AttributeSource:      "config:kimi:test",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)
	got := modelRegistry.GetModelsForClient(authID)
	if len(got) != 1 || got[0] == nil || got[0].ID != "kimi-k3" {
		t.Fatalf("registered models = %#v, want [kimi-k3]", got)
	}
	for _, model := range got {
		if model != nil && model.ID == catalog[0].ID {
			t.Fatalf("API key registered built-in catalog model %q", model.ID)
		}
	}
}

func TestRegisterModelsForAuthKimiAPIKeyEmptyModelsFallsBackToCatalog(t *testing.T) {
	catalog := internalregistry.GetKimiModels()
	if len(catalog) == 0 {
		t.Fatal("expected built-in Kimi catalog")
	}

	authID := "kimi-apikey-empty-models"
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	service := &Service{cfg: &config.Config{KimiKey: []config.KimiKey{{
		APIKey:  "sk-empty",
		Service: internalconfig.KimiServiceCodingPlan,
	}}}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "kimi",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "sk-empty",
			coreauth.AttributeConfigIndex: "0",
			coreauth.AttributeSource:      "config:kimi:test",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)
	got := modelRegistry.GetModelsForClient(authID)
	if len(got) != len(catalog) {
		t.Fatalf("empty API key models = %d, want catalog %d", len(got), len(catalog))
	}
}

func TestRegisterModelsForAuthKimiOpenPlatformEmptyModelsExcludesCodingIDs(t *testing.T) {
	authID := "kimi-apikey-open-empty-models"
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	service := &Service{cfg: &config.Config{KimiKey: []config.KimiKey{{
		APIKey:  "sk-open-empty",
		Service: internalconfig.KimiServiceOpenPlatform,
		Region:  internalconfig.KimiRegionDomestic,
	}}}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "kimi",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "sk-open-empty",
			coreauth.AttributeConfigIndex: "0",
			coreauth.AttributeSource:      "config:kimi:test",
			"service":                     internalconfig.KimiServiceOpenPlatform,
			"region":                      internalconfig.KimiRegionDomestic,
		},
	}

	service.registerModelsForAuth(context.Background(), auth)
	got := modelRegistry.GetModelsForClient(authID)
	if len(got) == 0 {
		t.Fatal("expected open-platform catalog models")
	}
	for _, model := range got {
		id := strings.ToLower(model.ID)
		if strings.Contains(id, "k2.7-code") || strings.Contains(id, "for-coding") {
			t.Fatalf("open-platform catalog still includes coding model %q", model.ID)
		}
	}
}

func TestRegisterModelsForAuthKimiOAuthKeepsCatalog(t *testing.T) {
	catalog := internalregistry.GetKimiModels()
	if len(catalog) == 0 {
		t.Fatal("expected built-in Kimi catalog")
	}

	authID := "kimi-oauth-catalog"
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "kimi",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
		},
	}

	service.registerModelsForAuth(context.Background(), auth)
	got := modelRegistry.GetModelsForClient(authID)
	if len(got) != len(catalog) {
		t.Fatalf("oauth model count = %d, want %d", len(got), len(catalog))
	}
}

func TestResolveConfigKimiKeyUsesConfigIndex(t *testing.T) {
	service := &Service{cfg: &config.Config{KimiKey: []config.KimiKey{
		{APIKey: "shared-key", Service: internalconfig.KimiServiceCodingPlan, Models: []config.KimiModel{{Name: "first"}}},
		{APIKey: "shared-key", Service: internalconfig.KimiServiceOpenPlatform, Region: internalconfig.KimiRegionDomestic, Models: []config.KimiModel{{Name: "second"}}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "shared-key",
		coreauth.AttributeSource:      "config:kimi[token-1]",
		coreauth.AttributeConfigIndex: "1",
	}}

	entry := service.resolveConfigKimiKey(auth)
	if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
		t.Fatalf("resolved config entry = %+v, want second entry", entry)
	}
}

func TestResolveConfigKimiKeyMatchUsesServiceNotAPIKeyOnly(t *testing.T) {
	service := &Service{cfg: &config.Config{KimiKey: []config.KimiKey{
		{APIKey: "shared-key", Service: internalconfig.KimiServiceCodingPlan, Models: []config.KimiModel{{Name: "code"}}},
		{APIKey: "shared-key", Service: internalconfig.KimiServiceOpenPlatform, Region: internalconfig.KimiRegionDomestic, Models: []config.KimiModel{{Name: "open"}}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "shared-key",
		"service":                     internalconfig.KimiServiceOpenPlatform,
		"region":                      internalconfig.KimiRegionDomestic,
		coreauth.AttributeConfigIndex: "99",
	}}

	entry := service.resolveConfigKimiKey(auth)
	if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "open" {
		t.Fatalf("resolved config entry = %+v, want open-platform entry", entry)
	}
}

func TestResolveConfigKimiKeyDoesNotTrustStaleIndex(t *testing.T) {
	service := &Service{cfg: &config.Config{KimiKey: []config.KimiKey{
		{APIKey: "shared-key", Service: internalconfig.KimiServiceCodingPlan, Prefix: "a", Models: []config.KimiModel{{Name: "first"}}},
		{APIKey: "shared-key", Service: internalconfig.KimiServiceCodingPlan, Prefix: "b", Models: []config.KimiModel{{Name: "second"}}},
	}}}
	auth := &coreauth.Auth{
		Prefix: "b",
		Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			"service":                     internalconfig.KimiServiceCodingPlan,
			coreauth.AttributeConfigIndex: "0",
		},
	}

	entry := service.resolveConfigKimiKey(auth)
	if entry == nil || entry.Prefix != "b" || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
		t.Fatalf("resolved config entry = %+v, want prefix b", entry)
	}
}
