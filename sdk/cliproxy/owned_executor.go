package cliproxy

import (
	"errors"
	"reflect"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type ownedProviderExecutor struct {
	provider string
	executor coreauth.ProviderExecutor
}

// WithOwnedProviderExecutor assigns a custom provider to an external executor for
// the lifetime of the built Service. The original executor is installed without
// wrapping, and service configuration reloads must not replace it with a stock
// executor. Registrations are validated together by Build before manager changes.
//
// The provider is normalized to lower case without surrounding whitespace and
// must match the executor's stable, canonical Identifier. Native provider keys and
// the stock OpenAI-compatible namespace cannot be owned through this option.
// Executors must be non-nil and compare equal to themselves so Manager can
// recognize repeated registration of the same executor.
// Duplicate keys are rejected; subsequent builder changes do not alter a Service.
//
// The external owner is responsible for custom endpoint/configuration changes,
// concurrency and resource retirement. This option is not a factory: stock config
// reload does not rebuild the executor. Keep it usable until all admitted work has
// ended and Service shutdown has completed, then release externally owned resources.
// Do not replace or unregister its manager key during the Service lifetime.
func (b *Builder) WithOwnedProviderExecutor(provider string, executor coreauth.ProviderExecutor) *Builder {
	b.ownedProviderExecutors = append(b.ownedProviderExecutors, ownedProviderExecutor{provider, executor})
	return b
}

func validateOwnedProviderExecutors(registrations []ownedProviderExecutor) (map[string]coreauth.ProviderExecutor, error) {
	if len(registrations) == 0 {
		return nil, nil
	}
	owned := make(map[string]coreauth.ProviderExecutor, len(registrations))
	for _, registration := range registrations {
		provider := strings.ToLower(strings.TrimSpace(registration.provider))
		if provider == "" || reservedOwnedProvider(provider) {
			return nil, errors.New("cliproxy: owned executor requires a custom provider key")
		}
		executor := registration.executor
		if executor == nil {
			return nil, errors.New("cliproxy: owned executor is nil")
		}
		value := reflect.ValueOf(executor)
		if !value.Comparable() || nilOwnedExecutor(value) {
			return nil, errors.New("cliproxy: owned executor must be non-nil and comparable")
		}
		if executor != executor {
			return nil, errors.New("cliproxy: owned executor must compare equal to itself")
		}
		if executor.Identifier() != provider {
			return nil, errors.New("cliproxy: owned executor identifier does not match provider")
		}
		if _, duplicate := owned[provider]; duplicate {
			return nil, errors.New("cliproxy: duplicate owned executor provider")
		}
		owned[provider] = executor
	}
	return owned, nil
}

func nilOwnedExecutor(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func reservedOwnedProvider(provider string) bool {
	// Reuse the service's native registrations and stock compatibility key rules.
	for _, auth := range baselineExecutorAuths() {
		if auth.Provider == provider {
			return true
		}
	}
	// The native Kimi switch also accepts this alias, which has no baseline auth.
	return provider == "kimi.com" || util.OpenAICompatibleProviderKey(provider) == provider
}
