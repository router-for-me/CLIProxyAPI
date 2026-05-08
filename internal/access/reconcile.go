package access

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	configaccess "github.com/router-for-me/CLIProxyAPI/v6/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	log "github.com/sirupsen/logrus"
)

// ReconcileProviders builds the desired provider list by reusing existing providers when possible
// and creating or removing providers only when their configuration changed. It returns the final
// ordered provider slice along with the identifiers of providers that were added, updated, or
// removed compared to the previous configuration.
func ReconcileProviders(oldCfg, newCfg *config.Config, existing []sdkaccess.Provider) (result []sdkaccess.Provider, added, updated, removed []string, err error) {
	_ = oldCfg
	if newCfg == nil {
		return nil, nil, nil, nil, nil
	}

	result = sdkaccess.RegisteredProviders()

	existingMap := make(map[string]sdkaccess.Provider, len(existing))
	for _, provider := range existing {
		providerID := identifierFromProvider(provider)
		if providerID == "" {
			continue
		}
		existingMap[providerID] = provider
	}

	finalIDs := make(map[string]struct{}, len(result))

	isInlineProvider := func(id string) bool {
		return strings.EqualFold(id, sdkaccess.DefaultAccessProviderName)
	}
	appendChange := func(list *[]string, id string) {
		if isInlineProvider(id) {
			return
		}
		*list = append(*list, id)
	}

	for _, provider := range result {
		providerID := identifierFromProvider(provider)
		if providerID == "" {
			continue
		}
		finalIDs[providerID] = struct{}{}

		existingProvider, exists := existingMap[providerID]
		if !exists {
			appendChange(&added, providerID)
			continue
		}
		if !providerInstanceEqual(existingProvider, provider) {
			appendChange(&updated, providerID)
		}
	}

	for providerID := range existingMap {
		if _, exists := finalIDs[providerID]; exists {
			continue
		}
		appendChange(&removed, providerID)
	}

	sort.Strings(added)
	sort.Strings(updated)
	sort.Strings(removed)

	return result, added, updated, removed, nil
}

// ApplyAccessProviders reconciles the configured access providers against the
// currently registered providers and updates the manager. It logs a concise
// summary of the detected changes and returns whether any provider changed.
func ApplyAccessProviders(manager *sdkaccess.Manager, oldCfg, newCfg *config.Config) (bool, error) {
	if manager == nil || newCfg == nil {
		return false, nil
	}

	existing := manager.Providers()
	configaccess.Register(&newCfg.SDKConfig)
	providers, added, updated, removed, err := ReconcileProviders(oldCfg, newCfg, existing)
	if err != nil {
		log.Errorf("failed to reconcile request auth providers: %v", err)
		return false, fmt.Errorf("reconciling access providers: %w", err)
	}

	manager.SetProviders(providers)

	if len(added)+len(updated)+len(removed) > 0 {
		log.Debugf("auth providers reconciled (added=%d updated=%d removed=%d)", len(added), len(updated), len(removed))
		log.Debugf("auth providers changes details - added=%v updated=%v removed=%v", added, updated, removed)
		return true, nil
	}

	log.Debug("auth providers unchanged after config update")
	return false, nil
}

func identifierFromProvider(provider sdkaccess.Provider) string {
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.Identifier())
}

func providerInstanceEqual(a, b sdkaccess.Provider) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	valueA := reflect.ValueOf(a)
	valueB := reflect.ValueOf(b)
	if valueA.Kind() == reflect.Pointer && valueB.Kind() == reflect.Pointer {
		return valueA.Pointer() == valueB.Pointer()
	}
	return reflect.DeepEqual(a, b)
}
