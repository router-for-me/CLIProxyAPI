package configsnapshot

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// scalarKeys is the Phase-1 projection of top-level Config fields that the
// snapshot can round-trip through YAML. Unknown top-level keys fall through
// to Snapshot.Extra so the projection can widen in Phase 2 without breaking
// users that added their own fields. Keep this list aligned with the yaml
// tags declared on internal/config.Config and its nested structs; it is a
// hand-maintained allowlist, not a reflection over internal/config (which
// would couple this package to a different module dependency).
var scalarKeys = []string{
	// Server binding.
	"host",
	"port",
	"tls",

	// Logging.
	"logging-to-file",
	"logs-max-total-size-mb",
	"error-logs-max-files",

	// Usage / quotas.
	"usage-statistics-enabled",
	"redis-usage-queue-retention-seconds",

	// Cooling / retry.
	"disable-cooling",
	"save-cooldown-status",
	"transient-error-cooldown-seconds",
	"auth-auto-refresh-workers",
	"request-retry",
	"max-retry-credentials",
	"max-retry-interval",

	// Quota / routing.
	"quota-exceeded",
	"routing",
	"ws-auth",

	// Antigravity.
	"antigravity-signature-cache-enabled",
	"antigravity-signature-bypass-strict",
	"antigravity",

	// XAI / Codex / Claude.
	"xai",
	"codex",
	"codex-header-defaults",
	"claude-header-defaults",
	"disable-claude-cloak-mode",

	// OAuth.
	"oauth-excluded-models",
	"oauth-model-alias",

	// Payload / debug / pprof.
	"payload",
	"debug",
	"pprof",

	// Surface.
	"commercial-mode",
	"branding",

	// Concurrency.
	"credential-concurrency",
	"credential-in-flight",

	// Plugins / management.
	"plugins",
	"remote-management",
}

// scalarKeySet is the O(1) lookup index built once at init time.
var scalarKeySet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(scalarKeys))
	for _, k := range scalarKeys {
		m[k] = struct{}{}
	}
	return m
}()

// sortedKeys returns the keys of m in lexicographic order so YAML output
// does not depend on Go map iteration order.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// normalizeYAMLValue recursively walks a value decoded by yaml.v3 and
// converts any map[any]any or yaml.MapSlice containers into map[string]any.
// yaml.v3 decodes into map[string]any when the target is map[string]any,
// but when the target is interface{} the inner maps come back as
// map[any]any, which MarshalYAML cannot emit back to YAML.
func normalizeYAMLValue(v any) any {
	switch t := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			ks, ok := k.(string)
			if !ok {
				ks = fmt.Sprintf("%v", k)
			}
			out[ks] = normalizeYAMLValue(val)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeYAMLValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalizeYAMLValue(item)
		}
		return out
	default:
		return v
	}
}

// buildRoot composes the ordered root map that MarshalYAML hands to the
// yaml.v3 encoder. Known Settings come first, in scalarKeys order; unknown
// Extra keys follow in sorted order under a synthetic __extra envelope so
// a future parser can detect forward-compatible fields without losing them.
// The result is a deterministic Go map. The yaml.v3 encoder sorts map
// keys at the encoding boundary, so emission is stable.
func buildRoot(snap *Snapshot) (map[string]any, error) {
	if snap == nil {
		return nil, errors.New("configsnapshot: nil snapshot")
	}
	root := make(map[string]any, len(snap.Settings)+1)
	for _, k := range scalarKeys {
		if v, ok := snap.Settings[k]; ok {
			root[k] = normalizeYAMLValue(v)
		}
	}
	if len(snap.Extra) > 0 {
		extra := make(map[string]any, len(snap.Extra))
		for _, k := range sortedKeys(snap.Extra) {
			extra[k] = normalizeYAMLValue(snap.Extra[k])
		}
		root["__extra"] = extra
	}
	return root, nil
}

// MarshalYAML serialises snap to YAML, emitting only the keys the Phase-1
// projection recognises plus a __extra envelope for unknown fields. Output
// is deterministic: known keys appear in scalarKeys order, Extra keys are
// sorted, and yaml.v3 sorts map keys at the encoding boundary.
//
// Empty snapshots still emit a trailing newline so the output is a valid
// text file. Nil maps are treated as empty (no key emission).
func MarshalYAML(snap *Snapshot) ([]byte, error) {
	root, err := buildRoot(snap)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("configsnapshot: encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("configsnapshot: close yaml encoder: %w", err)
	}
	if buf.Len() == 0 {
		buf.WriteByte('\n')
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte{'\n'}) {
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// UnmarshalYAML populates snap from a YAML payload. Top-level keys that
// appear in scalarKeys are copied into snap.Settings; everything else
// (including top-level keys under a __extra envelope) lands in
// snap.Extra. snap must be non-nil; its Settings and Extra are
// initialised to non-nil maps when they are nil. Empty or whitespace-only
// input is rejected to match internal/config.ParseConfigBytes.
//
// The function normalises yaml.v3's map[any]any / yaml.MapSlice shapes into
// map[string]any so subsequent MarshalYAML calls can encode them.
func UnmarshalYAML(data []byte, snap *Snapshot) error {
	if snap == nil {
		return errors.New("configsnapshot: nil snapshot target")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return errors.New("configsnapshot: empty yaml payload")
	}
	var raw map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("configsnapshot: decode yaml: %w", err)
	}
	normalized := normalizeYAMLValue(raw)
	nm, ok := normalized.(map[string]any)
	if !ok {
		return fmt.Errorf("configsnapshot: top-level yaml must decode to a mapping, got %T", normalized)
	}
	if snap.Settings == nil {
		snap.Settings = map[string]any{}
	}
	if snap.Extra == nil {
		snap.Extra = map[string]any{}
	}
	for _, k := range scalarKeys {
		if v, ok := nm[k]; ok {
			snap.Settings[k] = v
		}
	}
	for k, v := range nm {
		if k == "__extra" {
			if extra, ok := v.(map[string]any); ok {
				for ek, ev := range extra {
					snap.Extra[ek] = ev
				}
			}
			continue
		}
		if _, known := scalarKeySet[k]; known {
			continue
		}
		snap.Extra[k] = v
	}
	return nil
}
