package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestEnsureExecutorsForAuth_AzureDirectProviderBindsAzureExecutor(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "azure-auth-1",
		Provider: "azure-openai",
		Status:   coreauth.StatusActive,
	}

	service.ensureExecutorsForAuth(auth)
	resolved, ok := service.coreManager.Executor("azure-openai")
	if !ok || resolved == nil {
		t.Fatal("expected azure-openai executor after bind")
	}
	if _, isAzure := resolved.(*executor.AzureOpenAIExecutor); !isAzure {
		t.Fatalf("executor type = %T, want *executor.AzureOpenAIExecutor", resolved)
	}
}

func TestEnsureExecutorsForAuth_AzureOpenAICompatBindsProviderKey(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "azure-compat-auth-1",
		Provider: "azure-provider-key",
		Label:    "AzureCompat",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "AzureCompat",
			"provider_key": "azure-provider-key",
			"api_type":     "azure-openai",
		},
	}

	service.ensureExecutorsForAuth(auth)
	resolved, ok := service.coreManager.Executor("azure-provider-key")
	if !ok || resolved == nil {
		t.Fatal("expected Azure executor under OpenAI compatibility provider key")
	}
	if _, isAzure := resolved.(*executor.AzureOpenAIExecutor); !isAzure {
		t.Fatalf("executor type = %T, want *executor.AzureOpenAIExecutor", resolved)
	}
	if _, okGeneric := service.coreManager.Executor("openai-compatibility"); okGeneric {
		t.Fatal("azure compatibility auth must not register the generic compatibility executor")
	}
}

func TestEnsureExecutorsForAuth_OpenAICompatNamedAzureWithoutAPITypeStaysGeneric(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "generic-azure-name-auth-1",
		Provider: "azure-openai",
		Label:    "azure-openai",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "azure-openai",
			"provider_key": "azure-openai",
		},
	}

	service.ensureExecutorsForAuth(auth)
	resolved, ok := service.coreManager.Executor("azure-openai")
	if !ok || resolved == nil {
		t.Fatal("expected generic compatibility executor after bind")
	}
	if _, isGeneric := resolved.(*executor.OpenAICompatExecutor); !isGeneric {
		t.Fatalf("executor type = %T, want *executor.OpenAICompatExecutor", resolved)
	}
}

func TestEnsureExecutorsForAuth_GenericOpenAICompatStillBindsGenericExecutor(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
	}
	auth := &coreauth.Auth{
		ID:       "generic-compat-auth-1",
		Provider: "generic-provider-key",
		Label:    "GenericCompat",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"compat_name":  "GenericCompat",
			"provider_key": "generic-provider-key",
		},
	}

	service.ensureExecutorsForAuth(auth)
	resolved, ok := service.coreManager.Executor("generic-provider-key")
	if !ok || resolved == nil {
		t.Fatal("expected generic compatibility executor after bind")
	}
	if _, isGeneric := resolved.(*executor.OpenAICompatExecutor); !isGeneric {
		t.Fatalf("executor type = %T, want *executor.OpenAICompatExecutor", resolved)
	}
}
