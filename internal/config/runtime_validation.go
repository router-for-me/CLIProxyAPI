package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
)

type runtimeConfigValidationError struct {
	domain string
	err    error
}

func (e *runtimeConfigValidationError) Error() string { return e.err.Error() }
func (e *runtimeConfigValidationError) Unwrap() error { return e.err }

// RuntimeConfigValidationDomain identifies which canonical config contract failed.
func RuntimeConfigValidationDomain(err error) string {
	var validationError *runtimeConfigValidationError
	if errors.As(err, &validationError) {
		return validationError.domain
	}
	return "runtime config"
}

// ValidateRuntimeConfig validates contracts required by every config ingress.
func (cfg *Config) ValidateRuntimeConfig() error {
	if errValidate := cfg.ValidateCredentialWeights(); errValidate != nil {
		return &runtimeConfigValidationError{domain: "credential weights", err: errValidate}
	}
	if cfg != nil && cfg.ModelRouting != nil {
		if errValidate := cfg.ModelRouting.Validate(); errValidate != nil {
			return &runtimeConfigValidationError{domain: "model routing", err: errValidate}
		}
	}
	if errValidate := cfg.validateOpenAICompatibilityInventoryFacts(); errValidate != nil {
		return &runtimeConfigValidationError{domain: "model inventory", err: errValidate}
	}
	return nil
}

func (cfg *Config) validateOpenAICompatibilityInventoryFacts() error {
	if cfg == nil {
		return nil
	}
	routeChannels := make(map[string]int, len(cfg.OpenAICompatibility))
	providerNames := make(map[string]int, len(cfg.OpenAICompatibility))
	allowedProtocols := map[string]struct{}{
		modelrouting.ProtocolOpenAIChat:        {},
		modelrouting.ProtocolOpenAIResponses:   {},
		modelrouting.ProtocolAnthropicMessages: {},
	}
	for providerIndex := range cfg.OpenAICompatibility {
		provider := &cfg.OpenAICompatibility[providerIndex]
		if provider.Disabled {
			continue
		}
		providerPath := fmt.Sprintf("openai-compatibility[%d]", providerIndex)
		routeChannel := strings.TrimSpace(provider.RouteChannel)
		if routeChannel == "" {
			routeChannel = canonicalRouteChannel(provider.Name)
		}
		for _, fact := range []struct {
			name  string
			value string
		}{
			{name: "name", value: provider.Name},
			{name: "base-url", value: provider.BaseURL},
			{name: "route-channel", value: routeChannel},
		} {
			if errValidate := validateCanonicalInventoryFact(providerPath+"."+fact.name, fact.value); errValidate != nil {
				return errValidate
			}
		}
		if routeChannel != strings.ToLower(routeChannel) {
			return fmt.Errorf("%s.route-channel: must be lower-case", providerPath)
		}
		providerName := strings.ToLower(provider.Name)
		if previousIndex, exists := providerNames[providerName]; exists {
			return fmt.Errorf("%s.name: duplicates openai-compatibility[%d].name", providerPath, previousIndex)
		}
		providerNames[providerName] = providerIndex
		baseURL, errURL := url.Parse(provider.BaseURL)
		if errURL != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") {
			return fmt.Errorf("%s.base-url: must be an absolute HTTP(S) URL", providerPath)
		}
		if previousIndex, exists := routeChannels[routeChannel]; exists {
			return fmt.Errorf("%s.route-channel: duplicates openai-compatibility[%d].route-channel", providerPath, previousIndex)
		}
		routeChannels[routeChannel] = providerIndex
		if len(provider.Models) == 0 {
			return fmt.Errorf("%s.models: must contain at least one model", providerPath)
		}
		// A keyless group is valid: the provider may receive credentials through
		// auth files or the keyless management projection.
		for credentialIndex := range provider.APIKeyEntries {
			credentialPath := fmt.Sprintf("%s.api-key-entries[%d]", providerPath, credentialIndex)
			credential := &provider.APIKeyEntries[credentialIndex]
			if errValidate := validateCanonicalInventoryFact(credentialPath+".api-key", credential.APIKey); errValidate != nil {
				return errValidate
			}
			if errValidate := validateCanonicalInventoryFact(credentialPath+".quota-domain", credential.QuotaDomain); errValidate != nil {
				return errValidate
			}
		}
		for modelIndex := range provider.Models {
			model := &provider.Models[modelIndex]
			path := fmt.Sprintf("openai-compatibility[%d].models[%d]", providerIndex, modelIndex)
			if errValidate := validateCanonicalInventoryFact(path+".name", model.Name); errValidate != nil {
				return errValidate
			}
			if errValidate := validateCanonicalInventoryFact(path+".alias", model.Alias); errValidate != nil {
				return errValidate
			}
			// Duplicate aliases are valid: an ordered pool maps one client alias
			// to several upstream models for sequential failover.
			facts := []struct {
				name  string
				value string
			}{
				{name: "catalog-provider-id", value: model.CatalogProviderID},
				{name: "catalog-model-id", value: model.CatalogModelID},
				{name: "catalog-route-provider-id", value: model.CatalogRouteProviderID},
				{name: "catalog-route-model-id", value: model.CatalogRouteModelID},
			}
			for _, fact := range facts {
				if errValidate := validateCanonicalInventoryFact(path+"."+fact.name, fact.value); errValidate != nil {
					return errValidate
				}
			}
			if model.VariantID != "" {
				if errValidate := validateCanonicalInventoryFact(path+".variant-id", model.VariantID); errValidate != nil {
					return errValidate
				}
			}
			if len(model.Protocols) == 0 {
				return fmt.Errorf("%s.protocols: must contain at least one explicit protocol", path)
			}
			seenProtocols := make(map[string]struct{}, len(model.Protocols))
			previous := ""
			for protocolIndex, protocol := range model.Protocols {
				protocolPath := fmt.Sprintf("%s.protocols[%d]", path, protocolIndex)
				if errValidate := validateCanonicalInventoryFact(protocolPath, protocol); errValidate != nil {
					return errValidate
				}
				if _, exists := seenProtocols[protocol]; exists {
					return fmt.Errorf("%s: duplicate protocol %q", protocolPath, protocol)
				}
				if _, supported := allowedProtocols[protocol]; !supported {
					return fmt.Errorf("%s: protocol %q has no runtime executor", protocolPath, protocol)
				}
				if previous != "" && protocol < previous {
					return fmt.Errorf("%s.protocols: must be sorted", path)
				}
				seenProtocols[protocol] = struct{}{}
				previous = protocol
			}
		}
	}
	return nil
}

func validateCanonicalInventoryFact(path, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s: must be a non-empty canonical string", path)
	}
	return nil
}
