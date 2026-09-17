package configsnapshot

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// scalarKeys is the explicit Phase-1 projection of top-level Config fields
// that the snapshot round-trips through YAML. This is intentionally a small
// runtime-settings subset: server binding, TLS, logging, usage/quotas,
// cooling/retry, routing, ws-auth, antigravity switches, xai/codex/claude
// provider-wide blocks, claude/codex header defaults, oauth-excluded-models,
// oauth-model-alias, payload, debug, pprof, branding, commercial-mode,
// credential-concurrency, credential-in-flight, plugins, remote-management.
//
// Provider credential arrays (claude-api-key, codex-api-key, xai-api-key,
// gemini-api-key, interactions-api-key, openai-compatibility, opencode-go,
// vertex-api-key) and other fields belonging to normalised resource tables
// (api_keys, upstream_providers, proxy_pools, model_groups, internal_users,
// auto_routers) are deliberately absent here. They travel through the
// normalised resource tables and ResourceRefs, not through Snapshot
// Settings; unknown fields fall into Extra until a future phase introduces a
// dedicated section. Do not expand this list to encode credential arrays —
// that work belongs to MapResources in Task 8 and its successors, not in the
// scalar projection.
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

// errNonStringMapKey is the sentinel that normalizeYAMLValue returns to
// callers via *nonStringMapKeyError so the error chain can be inspected
// programmatically if future callers want to distinguish lossy-key
// rejections from other decode failures.
type nonStringMapKeyError struct {
	parent any
	key    any
}

func (e *nonStringMapKeyError) Error() string {
	return fmt.Sprintf("configsnapshot: non-string map key %v at parent %T; nested mapping keys must be strings so the round-trip is lossless", e.key, e.parent)
}

// normalizeYAMLValue recursively walks a value decoded by yaml.v3 and
// converts map[any]any containers into map[string]any. yaml.v3 decodes
// into map[string]any when the target is map[string]any, but when the
// target is interface{} the inner maps come back as map[any]any, which
// MarshalYAML cannot emit back to YAML without a lossy key coercion.
//
// Non-string mapping keys are rejected with *nonStringMapKeyError rather
// than silently converted to "%v": coercing them would change the
// canonical identity of the value and break the deterministic checksum,
// so callers must fix the upstream payload (yaml.v3 itself refuses to
// encode non-string mapping keys). String-keyed nested maps, slices, and
// scalars pass through unchanged.
func normalizeYAMLValue(v any) (any, error) {
	switch t := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			ks, ok := k.(string)
			if !ok {
				return nil, &nonStringMapKeyError{parent: t, key: k}
			}
			nv, err := normalizeYAMLValue(val)
			if err != nil {
				return nil, err
			}
			out[ks] = nv
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nv, err := normalizeYAMLValue(val)
			if err != nil {
				return nil, err
			}
			out[k] = nv
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			nv, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = nv
		}
		return out, nil
	default:
		return v, nil
	}
}

// extraEnvelope is the reserved sentinel under which forward-compatible
// fields are grouped when eminemitted to YAML. Programmatic callers must
// not place "extra" under Settings: that would emit an ambiguous "extra"
// key at the top level which UnmarshalYAML would re-divert into Extra, a
// loss-lossy that flattens Settings vs Extra semantics.
const extraEnvelope = "__extra"

// ErrEmptyYAML is returned by SnapshotFromConfig when the projected config
// marshals to empty/whitespace-only YAML. The bootstrap projector treats the
// empty case as a benign no-op and returns a NewEmpty snapshot without
// erroring the import; explicit UnmarshalYAML callers continue to fail
// loudly via the same wrapped error.
var ErrEmptyYAML = errors.New("configsnapshot: empty yaml payload")

// buildRoot composes the ordered root map that MarshalYAML hands to the
// yaml.v3 encoder. Known Settings come first, in scalarKeys order; unknown
// Extra keys follow in sorted order under the reserved __extra envelope so
// a future parser can detect forward-compatible fields without losing them.
// The result is a deterministic Go map. The yaml.v3 encoder sorts map
// keys at the encoding boundary, so emission is stable.
//
// A Snapshot whose Settings contains a top-level __extra key is rejected:
// the round-trip would be lossy (Settings vs Extra semantics collapse)
// and there is no clean way to disambiguate the two channels at parse time.
func buildRoot(snap *Snapshot) (map[string]any, error) {
	if snap == nil {
		return nil, errors.New("configsnapshot: nil snapshot")
	}
	if _, ok := snap.Settings[extraEnvelope]; ok {
		return nil, fmt.Errorf("configsnapshot: %s is a reserved envelope key and must not appear under Settings; place forward-compatible fields under Snapshot.Extra instead", extraEnvelope)
	}
	root := make(map[string]any, len(snap.Settings)+1)
	for _, k := range scalarKeys {
		if v, ok := snap.Settings[k]; ok {
			nv, err := normalizeYAMLValue(v)
			if err != nil {
				return nil, err
			}
			root[k] = nv
		}
	}
	if len(snap.Extra) > 0 {
		extra := make(map[string]any, len(snap.Extra))
		for _, k := range sortedKeys(snap.Extra) {
			if k == extraEnvelope {
				// Drop nested __extra entries: the envelope is owned by
				// MarshalYAML itself. Preserving them would emit
				// "extra:\n  extra: ..." which UnmarshalYAML cannot
				// reliably unwind.
				continue
			}
			nv, err := normalizeYAMLValue(snap.Extra[k])
			if err != nil {
				return nil, err
			}
			extra[k] = nv
		}
		if len(extra) > 0 {
			root[extraEnvelope] = extra
		}
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
// The function normalises yaml.v3's map[any]any shapes into map[string]any
// so subsequent MarshalYAML calls can encode them, and refuses non-string
// nested mapping keys with a contextual error rather than coercing them.
//
// The reserved __extra envelope must map to another mapping; scalar or
// sequence values are rejected so forward-compatible fields cannot be
// hidden behind a wrong-shape envelope.
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
	normalized, err := normalizeYAMLValue(raw)
	if err != nil {
		return err
	}
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
		if k == extraEnvelope {
			extra, ok := v.(map[string]any)
			if !ok {
				return fmt.Errorf("configsnapshot: %s envelope must be a mapping; got %T", extraEnvelope, v)
			}
			for ek, ev := range extra {
				// The envelope may not contain a nested __extra key:
				// nesting would create the same ambiguity that the
				// top-level guard prevents.
				if ek == extraEnvelope {
					return fmt.Errorf("configsnapshot: nested %s key inside %s envelope is reserved", extraEnvelope, extraEnvelope)
				}
				snap.Extra[ek] = ev
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
