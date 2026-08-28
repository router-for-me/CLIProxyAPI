package config

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	kimiCodingPlanBaseURL         = "https://api.kimi.com/coding"
	kimiOpenPlatformDomesticBase  = "https://api.moonshot.cn"
	kimiOpenPlatformInternational = "https://api.moonshot.ai"
)

// KimiAPIKeyIdentityParts returns the stable identity seed used by synthesizer
// and management auth-index generation. The order must stay unchanged.
func KimiAPIKeyIdentityParts(entry KimiKey) []string {
	return []string{
		strings.TrimSpace(entry.APIKey),
		strings.ToLower(strings.TrimSpace(entry.Service)),
		strings.ToLower(strings.TrimSpace(entry.Region)),
		strings.TrimSpace(entry.ProxyURL),
		strings.TrimSpace(entry.Prefix),
		FormatSortedHeaders(entry.Headers),
	}
}

// HeadersFromAuthAttrs reconstructs configured headers from auth attributes.
func HeadersFromAuthAttrs(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]string)
	for key, value := range attrs {
		if !strings.HasPrefix(key, "header:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(key, "header:"))
		if name == "" {
			continue
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// KimiIndexSeed is the auth-index seed for a Kimi API key. It uses the same
// identity parts as synthesizer IDs so prefix, proxy, and headers stay distinct.
func KimiIndexSeed(apiKey, service, region, prefix, proxyURL string, headers map[string]string) string {
	parts := KimiAPIKeyIdentityParts(KimiKey{
		APIKey:   apiKey,
		Service:  service,
		Region:   region,
		Prefix:   prefix,
		ProxyURL: proxyURL,
		Headers:  headers,
	})
	return "kimi-api-key:" + strings.Join(parts, "\x1e")
}

// KimiBaseURL returns the host root derived from service and region.
func KimiBaseURL(service, region string) string {
	service = strings.ToLower(strings.TrimSpace(service))
	region = strings.ToLower(strings.TrimSpace(region))
	if service == KimiServiceOpenPlatform {
		if region == KimiRegionInternational {
			return kimiOpenPlatformInternational
		}
		return kimiOpenPlatformDomesticBase
	}
	return kimiCodingPlanBaseURL
}

// KimiOpenAIChatCompletionsURL returns the OpenAI chat completions endpoint.
func KimiOpenAIChatCompletionsURL(service, region string) string {
	return strings.TrimRight(KimiBaseURL(service, region), "/") + "/v1/chat/completions"
}

// KimiAnthropicBaseURL returns the Claude executor base URL.
func KimiAnthropicBaseURL(service, region string) string {
	service = strings.ToLower(strings.TrimSpace(service))
	base := strings.TrimRight(KimiBaseURL(service, region), "/")
	if service == KimiServiceOpenPlatform {
		return base + "/anthropic"
	}
	return base
}

// NormalizeKimiKey trims fields and clears region on coding-plan entries.
func NormalizeKimiKey(entry *KimiKey) {
	if entry == nil {
		return
	}
	entry.APIKey = strings.TrimSpace(entry.APIKey)
	entry.Name = strings.TrimSpace(entry.Name)
	entry.Service = strings.ToLower(strings.TrimSpace(entry.Service))
	entry.Region = strings.ToLower(strings.TrimSpace(entry.Region))
	entry.Prefix = normalizeModelPrefix(entry.Prefix)
	entry.ProxyURL = strings.TrimSpace(entry.ProxyURL)
	entry.Headers = NormalizeHeaders(entry.Headers)
	entry.ExcludedModels = NormalizeExcludedModels(entry.ExcludedModels)
	entry.Models = sanitizeKimiModels(entry.Models)
	if entry.Service == KimiServiceCodingPlan {
		entry.Region = ""
	}
}

// ValidateKimiKey reports whether a normalized Kimi API-key entry can be kept.
func ValidateKimiKey(entry KimiKey) error {
	if strings.TrimSpace(entry.APIKey) == "" {
		return fmt.Errorf("kimi-api-key requires api-key")
	}
	service := strings.ToLower(strings.TrimSpace(entry.Service))
	if service != KimiServiceOpenPlatform && service != KimiServiceCodingPlan {
		return fmt.Errorf("kimi-api-key service must be open-platform or coding-plan")
	}
	if service == KimiServiceOpenPlatform {
		region := strings.ToLower(strings.TrimSpace(entry.Region))
		if region != KimiRegionDomestic && region != KimiRegionInternational {
			return fmt.Errorf("kimi-api-key open-platform requires region domestic or international")
		}
	}
	return nil
}

// MatchKimiKey selects a Kimi API-key entry by config index when the identity
// matches, otherwise by api-key + service + region + prefix + proxy-url + headers.
// It does not fall back to api-key only.
func MatchKimiKey(entries []KimiKey, apiKey, service, region, prefix, proxyURL, headerBlob, configIndex string) *KimiKey {
	if len(entries) == 0 {
		return nil
	}
	apiKey = strings.TrimSpace(apiKey)
	service = strings.ToLower(strings.TrimSpace(service))
	region = strings.ToLower(strings.TrimSpace(region))
	prefix = strings.TrimSpace(prefix)
	proxyURL = strings.TrimSpace(proxyURL)
	headerBlob = strings.TrimSpace(headerBlob)
	matchesIdentity := func(entry KimiKey) bool {
		if apiKey == "" {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(entry.APIKey), apiKey) {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(entry.Prefix), prefix) {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(entry.ProxyURL), proxyURL) {
			return false
		}
		return FormatSortedHeaders(entry.Headers) == headerBlob
	}
	matchesService := func(entry KimiKey) bool {
		if service == "" {
			return false
		}
		if strings.ToLower(strings.TrimSpace(entry.Service)) != service {
			return false
		}
		return strings.ToLower(strings.TrimSpace(entry.Region)) == region
	}
	if index, errIndex := strconv.Atoi(strings.TrimSpace(configIndex)); errIndex == nil && index >= 0 && index < len(entries) {
		entry := entries[index]
		if matchesIdentity(entry) && (service == "" || matchesService(entry)) {
			return &entries[index]
		}
	}
	for i := range entries {
		if matchesIdentity(entries[i]) && matchesService(entries[i]) {
			return &entries[i]
		}
	}
	return nil
}
