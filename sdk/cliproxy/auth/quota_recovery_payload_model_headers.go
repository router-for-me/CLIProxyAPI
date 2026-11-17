package auth

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// quotaRecoveryPayloadModels includes possible finalized model identities. The
// recovery snapshot has no request body, source format, or client headers, so a
// matching rule's request predicates cannot exclude its destination here.
func quotaRecoveryPayloadModels(cfg *internalconfig.Config, model string, requested ...string) ([]string, bool) {
	models := []string{model}
	if cfg == nil {
		return models, true
	}
	names := []string{model}
	for _, name := range requested {
		names = append(names, name, thinking.ParseSuffix(name).ModelName)
	}
	for _, group := range []struct {
		rules []internalconfig.PayloadRule
		raw   bool
	}{{cfg.Payload.Override, false}, {cfg.Payload.OverrideRaw, true}} {
		for _, rule := range group.rules {
			possible := false
			for _, match := range rule.Models {
				if p := strings.TrimSpace(match.Protocol); p != "" && !strings.EqualFold(p, "codex") {
					continue
				}
				for _, name := range names {
					if quotaRecoveryPayloadPatternMatches(match.Name, name) {
						possible = true
						break
					}
				}
				if possible {
					break
				}
			}
			if !possible {
				continue
			}
			for path, value := range rule.Params {
				path = strings.TrimSpace(path)
				if path == "" {
					continue
				}
				// Use the same document writer as the finalizer, rather than interpreting
				// escaped or nested JSON paths as a second path language.
				probe := ojson.NewDocString(`{}`)
				if err := probe.Set("model", model); err != nil {
					return nil, false
				}
				var err error
				if group.raw {
					switch raw := value.(type) {
					case nil:
						continue
					case string:
						err = probe.SetRaw(path, raw)
					case []byte:
						err = probe.SetRawBytes(path, raw)
					default:
						err = probe.Set(path, value)
					}
				} else {
					err = probe.Set(path, value)
				}
				if err != nil {
					continue
				} // Invalid writes are skipped by the finalizer.
				result := probe.Get("model")
				if result.Type != ojson.String {
					return nil, false
				}
				candidate := strings.TrimSpace(result.String())
				if candidate != "" {
					models = append(models, candidate)
				}
			}
		}
	}
	// Codex seeds model in both the original and prepared document, so defaults
	// cannot replace it. Filters only remove values and add no header identity.
	return models, true
}

// Payload names use a case-sensitive '*' wildcard, not filepath glob syntax.
func quotaRecoveryPayloadPatternMatches(pattern, name string) bool {
	pattern, name = strings.TrimSpace(pattern), strings.TrimSpace(name)
	if pattern == "" {
		return false
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return name == pattern
	}
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = strings.TrimPrefix(name, parts[0])
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(name, part)
		if index < 0 {
			return false
		}
		name = name[index+len(part):]
	}
	return strings.HasSuffix(name, parts[len(parts)-1])
}
