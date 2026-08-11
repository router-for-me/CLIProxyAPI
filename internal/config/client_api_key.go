package config

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

const (
	maxConfiguredClientAPIKeyIDLength    = 64
	maxConfiguredClientAPIKeyAliasLength = 128
)

var configuredClientAPIKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SanitizeAPIKeyMetadata normalizes client API key metadata without changing
// the legacy APIKeys list representation.
func (cfg *Config) SanitizeAPIKeyMetadata() {
	if cfg == nil {
		return
	}

	configuredKeys := make(map[string]struct{}, len(cfg.APIKeys))
	orderedKeys := make([]string, 0, len(cfg.APIKeys))
	for _, rawKey := range cfg.APIKeys {
		apiKey := strings.TrimSpace(rawKey)
		if apiKey == "" {
			continue
		}
		if _, exists := configuredKeys[apiKey]; exists {
			continue
		}
		configuredKeys[apiKey] = struct{}{}
		orderedKeys = append(orderedKeys, apiKey)
	}
	replacements := make([]string, 0, len(configuredKeys)*2)
	for apiKey := range configuredKeys {
		replacements = append(replacements, apiKey, "")
	}
	credentialRedactor := strings.NewReplacer(replacements...)

	metadataKeys := make([]string, 0, len(cfg.APIKeyMetadata))
	for apiKey := range cfg.APIKeyMetadata {
		metadataKeys = append(metadataKeys, apiKey)
	}
	sort.Slice(metadataKeys, func(i, j int) bool {
		trimmedI := strings.TrimSpace(metadataKeys[i])
		trimmedJ := strings.TrimSpace(metadataKeys[j])
		if trimmedI != trimmedJ {
			return trimmedI < trimmedJ
		}
		exactI := metadataKeys[i] == trimmedI
		exactJ := metadataKeys[j] == trimmedJ
		if exactI != exactJ {
			return exactI
		}
		return metadataKeys[i] < metadataKeys[j]
	})

	normalized := make(map[string]ClientAPIKeyMetadata, len(cfg.APIKeyMetadata))
	for _, rawKey := range metadataKeys {
		apiKey := strings.TrimSpace(rawKey)
		if _, configured := configuredKeys[apiKey]; !configured {
			continue
		}
		if _, exists := normalized[apiKey]; exists {
			continue
		}
		metadata := cfg.APIKeyMetadata[rawKey]
		metadata.ID = strings.TrimSpace(metadata.ID)
		metadata.Alias = strings.TrimSpace(metadata.Alias)
		metadata.Alias = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, metadata.Alias)
		if metadata.ID != "" && (len([]rune(metadata.ID)) > maxConfiguredClientAPIKeyIDLength ||
			!configuredClientAPIKeyIDPattern.MatchString(metadata.ID) || credentialRedactor.Replace(metadata.ID) != metadata.ID) {
			metadata.ID = sdkaccess.FallbackClientKeyID(apiKey)
			log.Warn("invalid or sensitive client API key metadata id detected; using a deterministic fallback id")
		}
		if metadata.Alias != "" && credentialRedactor.Replace(metadata.Alias) != metadata.Alias {
			metadata.Alias = ""
			log.Warn("client API key alias contained credential material; clearing alias")
		}
		aliasRunes := []rune(metadata.Alias)
		if len(aliasRunes) > maxConfiguredClientAPIKeyAliasLength {
			metadata.Alias = string(aliasRunes[:maxConfiguredClientAPIKeyAliasLength])
		}
		normalized[apiKey] = metadata
	}

	seenIDs := make(map[string]struct{}, len(orderedKeys))
	for _, apiKey := range orderedKeys {
		metadata, exists := normalized[apiKey]
		explicitID := metadata.ID
		resolvedID := explicitID
		fallbackID := sdkaccess.FallbackClientKeyID(apiKey)
		if resolvedID == "" {
			resolvedID = fallbackID
		}
		if _, duplicate := seenIDs[resolvedID]; duplicate {
			resolvedID = fallbackID
			if _, fallbackDuplicate := seenIDs[resolvedID]; fallbackDuplicate {
				resolvedID = uniqueClientKeyID(resolvedID, seenIDs)
			}
			if explicitID != "" {
				log.Warn("duplicate client API key metadata id detected; using a deterministic fallback id")
			} else {
				log.Warn("client API key fallback id collision detected; using a disambiguated id")
			}
		}
		if exists || resolvedID != fallbackID {
			metadata.ID = resolvedID
			normalized[apiKey] = metadata
		}
		seenIDs[resolvedID] = struct{}{}
	}

	if len(normalized) == 0 {
		cfg.APIKeyMetadata = nil
		return
	}
	cfg.APIKeyMetadata = normalized
}

func uniqueClientKeyID(base string, seen map[string]struct{}) string {
	for suffix := 2; ; suffix++ {
		candidate := base + "_" + strconv.Itoa(suffix)
		if _, exists := seen[candidate]; !exists {
			return candidate
		}
	}
}
