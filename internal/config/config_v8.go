package config

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// Config remains the effective runtime/wire representation. Layout translation is
// confined to YAML boundaries so older SDK and Home consumers keep their contract.
type legacyConfig Config

type configPath struct{ old, current string }

var v8FieldIndexes = make(map[string][]int)
var v8StructPaths []configPath
var v8Paths = buildV8Paths()

// Historical spellings are YAML-boundary aliases, not provider runtime fields.
// The canonical client path wins by presence (including explicit false/null).
// If it is absent, prefer the previous v8 OAuth path, then providers, then codex.
var v8ClientPaths = []configPath{
	{"oauth.providers.codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
	{"providers.codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
	{"codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
}

var v8KeyFamilies = []configPath{
	{"gemini-api-key", "gemini"}, {"interactions-api-key", "interactions"},
	{"vertex-api-key", "vertex"}, {"codex-api-key", "codex"},
	{"claude-api-key", "claude"}, {"xai-api-key", "xai"}, {"meta-api-key", "meta"},
	{"openai-compatibility", "openai-compatibility"},
}

func buildV8Paths() []configPath {
	prefixes := []configPath{
		{"host", "server.host"}, {"port", "server.port"}, {"trusted-proxies", "server.trusted-proxies"},
		{"tls", "server.tls"}, {"commercial-mode", "server.commercial-mode"}, {"discovery", "server.discovery"},
		{"remote-management", "management"}, {"api-keys", "access.api-keys"},
		{"credential-concurrency", "credentials.concurrency"}, {"credential-in-flight", "credentials.in-flight"},
		{"force-model-prefix", "routing.force-model-prefix"},
		{"request-retry", "routing.retry.request-retry"}, {"max-retry-credentials", "routing.retry.max-retry-credentials"},
		{"max-retry-interval", "routing.retry.max-retry-interval"},
		{"disable-cooling", "routing.cooldown.disable-cooling"}, {"save-cooldown-status", "routing.cooldown.save-cooldown-status"},
		{"transient-error-cooldown-seconds", "routing.cooldown.transient-error-cooldown-seconds"},
		{"proxy-url", "requests.proxy-url"}, {"passthrough-headers", "requests.passthrough-headers"},
		{"nonstream-keepalive-interval", "requests.nonstream-keepalive-interval"}, {"streaming", "requests.streaming"}, {"payload", "requests.payload"},
		{"auth-dir", "oauth.auth-dir"}, {"auth-auto-refresh-workers", "oauth.auth-auto-refresh-workers"},
		{"oauth-model-alias", "oauth.model-alias"}, {"oauth-excluded-models", "oauth.excluded-models"},
		{"oauth-request-scoped-errors", "oauth.request-scoped-errors"}, {"oauth-settings", "oauth.settings"}, {"ws-auth", "oauth.providers.aistudio.ws-auth"},
		{"codex", "oauth.providers.codex"}, {"codex-header-defaults", "oauth.providers.codex.header-defaults"},
		{"claude", "oauth.providers.claude"}, {"claude-code", "oauth.providers.claude.claude-code"},
		{"disable-claude-cloak-mode", "oauth.providers.claude.disable-claude-cloak-mode"},
		{"claude-header-defaults", "oauth.providers.claude.header-defaults"},
		{"antigravity", "oauth.providers.antigravity"},
		{"antigravity-signature-cache-enabled", "oauth.providers.antigravity.signature-cache-enabled"},
		{"antigravity-signature-bypass-strict", "oauth.providers.antigravity.signature-bypass-strict"},
		{"quota-exceeded.antigravity-credits", "oauth.providers.antigravity.antigravity-credits"},
		{"xai", "oauth.providers.xai"}, {"devin", "oauth.providers.devin"},
		{"disable-image-generation", "multimedia.disable-image-generation"}, {"gpt-image-2-base-model", "multimedia.gpt-image-2-base-model"},
		{"video-result-auth-cache-ttl", "multimedia.video-result-auth-cache-ttl"},
		{"debug", "observability.logs.debug"}, {"logging-to-file", "observability.logs.logging-to-file"},
		{"logs-max-total-size-mb", "observability.logs.logs-max-total-size-mb"}, {"request-log", "observability.logs.request-log"},
		{"error-logs-max-files", "observability.logs.error-logs-max-files"},
		{"usage-statistics-enabled", "observability.usage.usage-statistics-enabled"},
		{"redis-usage-queue-retention-seconds", "observability.usage.redis-usage-queue-retention-seconds"}, {"pprof", "observability.pprof"},
	}
	var out []configPath
	var walk func(reflect.Type, string, []int)
	walk = func(t reflect.Type, path string, indexes []int) {
		if t.Kind() == reflect.Struct {
			for _, prefix := range prefixes {
				if path == prefix.old || strings.HasPrefix(path, prefix.old+".") {
					v8StructPaths = append(v8StructPaths, configPath{path, prefix.current + strings.TrimPrefix(path, prefix.old)})
					break
				}
			}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
				if tag == "-" || field.PkgPath != "" {
					continue
				}
				fieldIndexes := append(append([]int(nil), indexes...), i)
				if field.Anonymous {
					walk(field.Type, path, fieldIndexes)
					continue
				}
				child := tag
				if path != "" {
					child = path + "." + tag
				}
				walk(field.Type, child, fieldIndexes)
			}
			return
		}
		for _, prefix := range prefixes {
			if path == prefix.old || strings.HasPrefix(path, prefix.old+".") {
				out = append(out, configPath{path, prefix.current + strings.TrimPrefix(path, prefix.old)})
				v8FieldIndexes[path] = indexes
				return
			}
		}
	}
	walk(reflect.TypeOf(Config{}), "", nil)
	return out
}

func yamlPath(root *yaml.Node, path string) *yaml.Node {
	for _, key := range strings.Split(path, ".") {
		idx := findMapKeyIndex(root, key)
		if idx < 0 {
			return nil
		}
		root = root.Content[idx+1]
	}
	return root
}

func setYAMLPath(root *yaml.Node, path string, value *yaml.Node) {
	parts := strings.Split(path, ".")
	for _, key := range parts[:len(parts)-1] {
		root = getOrCreateMapValue(root, key)
	}
	dst := getOrCreateMapValue(root, parts[len(parts)-1])
	*dst = *deepCopyNode(value)
}

// setYAMLPathWithComments attaches leading comments to the destination key;
// yaml.v3 does not render a scalar value's HeadComment.
func setYAMLPathWithComments(root *yaml.Node, path string, value *yaml.Node) {
	setYAMLPath(root, path, value)
	parts := strings.Split(path, ".")
	parent := root
	if len(parts) > 1 {
		parent = yamlPath(root, strings.Join(parts[:len(parts)-1], "."))
	}
	parent.Content[findMapKeyIndex(parent, parts[len(parts)-1])].HeadComment = value.HeadComment
	yamlPath(root, path).HeadComment = ""
}

// copyYAMLPathValue carries a field's key comment when moving its value.
func copyYAMLPathValue(root *yaml.Node, path string) *yaml.Node {
	copy := deepCopyNode(yamlPath(root, path))
	parts := strings.Split(path, ".")
	parent := root
	if len(parts) > 1 {
		parent = yamlPath(root, strings.Join(parts[:len(parts)-1], "."))
	}
	if idx := findMapKeyIndex(parent, parts[len(parts)-1]); idx >= 0 {
		copy.HeadComment = strings.TrimSpace(parent.Content[idx].HeadComment + "\n" + copy.HeadComment)
	}
	return copy
}

func deleteYAMLPath(root *yaml.Node, path string) bool {
	parts := strings.SplitN(path, ".", 2)
	idx := findMapKeyIndex(root, parts[0])
	if idx < 0 {
		return false
	}
	if len(parts) == 2 {
		child := root.Content[idx+1]
		if !deleteYAMLPath(child, parts[1]) {
			return false
		}
		if len(child.Content) != 0 {
			return true
		}
	}
	root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
	return true
}

func legacyPath(root *yaml.Node, path string) *yaml.Node {
	node := yamlPath(root, path)
	if path == "api-keys" && node != nil && node.Kind == yaml.MappingNode {
		return nil
	}
	return node
}

// UnmarshalYAML accepts both layouts, including partially migrated documents.
// Presence, rather than Go zero values, determines which setting wins.
func (cfg *Config) UnmarshalYAML(node *yaml.Node) error {
	root, err := flattenV8(node)
	if err != nil {
		return err
	}
	decoded := legacyConfig(*cfg)
	if err = root.Decode(&decoded); err != nil {
		return err
	}
	*cfg = Config(decoded)
	cfg.OAuthOnlyFields = nil
	source := expandConfigAliases(node)
	for _, path := range v8Paths {
		if strings.HasPrefix(path.current, "oauth.providers.") && yamlPath(source, path.current) != nil {
			if cfg.OAuthOnlyFields == nil {
				cfg.OAuthOnlyFields = make(map[string]bool)
			}
			cfg.OAuthOnlyFields[path.old] = true
		}
	}
	return nil
}

func flattenV8(node *yaml.Node) (*yaml.Node, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config must be a mapping")
	}
	// Decode once before transformation to reject duplicate keys even when a
	// winning v8 value would otherwise hide the malformed legacy subtree.
	var shape map[string]any
	if err := node.Decode(&shape); err != nil {
		return nil, err
	}
	node = expandConfigAliases(node)
	if _, err := normalizeV8PrivateIPAlias(node, true); err != nil {
		return nil, err
	}
	for _, path := range append(append([]configPath(nil), v8Paths...), v8ClientPaths...) {
		parts := strings.Split(path.current, ".")
		for i := 1; i < len(parts); i++ {
			parent := yamlPath(node, strings.Join(parts[:i], "."))
			if parent == nil {
				break
			}
			// Routing is shared with the legacy layout, where null means defaults.
			if i == 1 && parts[0] == "routing" && parent.Tag == "!!null" {
				break
			}
			if parent.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("%s must be a mapping", strings.Join(parts[:i], "."))
			}
		}
	}
	root := deepCopyNode(node)
	for _, path := range v8ClientPaths {
		if yamlPath(root, path.old) != nil {
			if yamlPath(root, path.current) == nil {
				setYAMLPathWithComments(root, path.current, copyYAMLPathValue(root, path.old))
			}
			deleteYAMLPath(root, path.old)
		}
	}
	if version := yamlPath(root, "config-version"); version != nil && (version.Tag != "!!int" || version.Value != "8") {
		return nil, fmt.Errorf("unsupported config-version (expected 8)")
	}
	// The v8 upstream map reuses the legacy client-key field name.
	if keys := yamlPath(root, "api-keys"); keys != nil && keys.Kind == yaml.MappingNode {
		deleteYAMLPath(root, "api-keys")
	}
	for _, path := range v8Paths {
		if value := yamlPath(node, path.current); value != nil {
			deleteYAMLPath(root, path.current)
			setYAMLPath(root, path.old, value)
		}
	}
	for _, family := range v8KeyFamilies {
		if groups := yamlPath(node, "api-keys."+family.current); groups != nil {
			keys, err := expandV8Groups(groups, family.current)
			if err != nil {
				return nil, err
			}
			setYAMLPath(root, family.old, keys)
		}
	}
	return root, nil
}

var sharedKeyFields = map[string]bool{
	"priority": true, "prefix": true, "proxy-url": true, "headers": true,
	"models": true, "excluded-models": true, "disable-cooling": true,
	"request-retry": true, "request-scoped-errors": true,
}

func expandV8Groups(groups *yaml.Node, provider string) (*yaml.Node, error) {
	if groups.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("api-keys.%s must be a list", provider)
	}
	out := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for index, group := range groups.Content {
		if group.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("api-keys.%s[%d] must be a mapping", provider, index)
		}
		keys := yamlPath(group, "keys")
		if keys == nil || keys.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("api-keys.%s[%d].keys must be a list", provider, index)
		}
		if err := validateWeightSequenceNode(keys, "api-keys."+provider+".keys"); err != nil {
			return nil, err
		}
		if provider == "openai-compatibility" {
			item := deepCopyNode(group)
			deleteYAMLPath(item, "keys")
			setYAMLPath(item, "api-key-entries", keys)
			out.Content = append(out.Content, item)
			continue
		}
		for i := 0; i < len(group.Content); i += 2 {
			field := group.Content[i].Value
			if field != "name" && field != "base-url" && field != "keys" && !sharedKeyFields[field] {
				return nil, fmt.Errorf("api-keys.%s: unsupported group field %s", provider, field)
			}
		}
		for _, key := range keys.Content {
			if key.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("api-keys.%s key must be a mapping", provider)
			}
			if yamlPath(key, "base-url") != nil {
				return nil, fmt.Errorf("api-keys.%s: base-url belongs to the group", provider)
			}
			item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for i := 0; i < len(group.Content); i += 2 {
				field := group.Content[i].Value
				if field == "base-url" || sharedKeyFields[field] {
					setYAMLPath(item, field, group.Content[i+1])
				}
			}
			for i := 0; i < len(key.Content); i += 2 {
				if key.Content[i+1].Tag != "!!null" {
					setYAMLPath(item, key.Content[i].Value, key.Content[i+1])
				}
			}
			out.Content = append(out.Content, item)
		}
	}
	return out, nil
}

// groupLegacyKeys deliberately keeps one group per legacy entry: equal endpoints
// do not imply equal routing, headers, models, or credential policies.
func groupLegacyKeys(keys *yaml.Node, provider string) *yaml.Node {
	out := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for index, entry := range keys.Content {
		group := deepCopyNode(entry)
		if provider == "openai-compatibility" {
			entries := yamlPath(group, "api-key-entries")
			if entries == nil {
				entries = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			}
			setYAMLPath(group, "keys", entries)
			deleteYAMLPath(group, "api-key-entries")
		} else {
			group = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setYAMLPath(group, "name", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprintf("%s-%d", provider, index+1)})
			key := deepCopyNode(entry)
			for i := 0; i < len(entry.Content); i += 2 {
				field := entry.Content[i].Value
				if field == "base-url" || sharedKeyFields[field] {
					setYAMLPath(group, field, entry.Content[i+1])
					deleteYAMLPath(key, field)
				}
			}
			setYAMLPath(group, "keys", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{key}})
		}
		out.Content = append(out.Content, group)
	}
	return out
}

// NormalizeConfigLayout removes conflicting legacy fields. migrate also moves
// legacy-only fields; callers use it only for an explicit v8 configuration write.
func NormalizeConfigLayout(data []byte, migrate bool) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, err
	}
	if len(doc.Content) == 0 {
		if !migrate {
			return data, false, nil
		}
		return nil, false, fmt.Errorf("empty config")
	}
	root := doc.Content[0]
	if _, err := flattenV8(root); err != nil {
		return nil, false, err
	}
	root = expandConfigAliases(root)
	doc.Content[0] = root
	changed, err := normalizeV8PrivateIPAlias(root, migrate)
	if err != nil {
		return nil, false, err
	}
	// Empty legacy structs have no leaf fields to move. Preserve them as empty v8
	// mappings; null structs also mean defaults. User-owned maps are not included.
	paths := append(append([]configPath(nil), v8ClientPaths...), v8Paths...)
	for _, path := range v8StructPaths {
		old := yamlPath(root, path.old)
		if old == nil || (!migrate && yamlPath(root, path.current) == nil) {
			continue
		}
		if old.Tag == "!!null" {
			old.Kind, old.Tag, old.Value = yaml.MappingNode, "!!map", ""
		} else if old.Kind != yaml.MappingNode || len(old.Content) != 0 {
			continue
		}
		paths = append(paths, path)
	}
	for _, path := range paths {
		old := legacyPath(root, path.old)
		if old == nil {
			continue
		}
		current := yamlPath(root, path.current)
		if current == nil && !migrate {
			continue
		}
		copy := copyYAMLPathValue(root, path.old)
		deleteYAMLPath(root, path.old)
		if current == nil {
			setYAMLPathWithComments(root, path.current, copy)
		}
		changed = true
	}
	for _, family := range v8KeyFamilies {
		old := yamlPath(root, family.old)
		if old == nil {
			continue
		}
		path := "api-keys." + family.current
		if yamlPath(root, path) == nil {
			if !migrate {
				continue
			}
			setYAMLPath(root, path, groupLegacyKeys(old, family.current))
		}
		deleteYAMLPath(root, family.old)
		changed = true
	}
	if migrate {
		// Existing unknown fields are ignored by the runtime. Retain their
		// contents as comments while keeping new v8 writes strictly validated.
		if err := commentUnknownV8Sections(root); err != nil {
			return nil, false, err
		}
		setYAMLPath(root, "config-version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "8"})
		changed = true
	}
	if !changed {
		return data, false, nil
	}
	out, err := yaml.Marshal(&doc)
	return out, true, err
}

func v8AllowedRoots() map[string]bool {
	allowed := map[string]bool{"config-version": true, "api-keys": true, "plugins": true, "quota-exceeded": true, "client": true}
	for _, path := range v8Paths {
		section, _, _ := strings.Cut(path.current, ".")
		allowed[section] = true
	}
	return allowed
}

var (
	v8WarnMu   sync.RWMutex
	v8WarnFunc func(section, msg string)
)

// SetV8MigrationWarnFunc sets a custom warning handler (e.g. from logging package).
func SetV8MigrationWarnFunc(fn func(section, msg string)) {
	v8WarnMu.Lock()
	defer v8WarnMu.Unlock()
	v8WarnFunc = fn
}

func warnUnrecognizedV8Section(section string) {
	msg := fmt.Sprintf("unrecognized configuration section %q commented out during v8 migration", section)
	v8WarnMu.RLock()
	fn := v8WarnFunc
	v8WarnMu.RUnlock()
	if fn != nil {
		fn(section, msg)
		return
	}
	log.Warn(msg)
}

func commentUnknownV8Sections(root *yaml.Node) error {
	if err := commentUnknownV8Fields(root, root, v8AllowedRoots(), ""); err != nil {
		return err
	}

	children := make(map[string]map[string]bool)
	for _, path := range append(append([]configPath(nil), v8Paths...), v8StructPaths...) {
		parts := strings.Split(path.current, ".")
		for i := 1; i < len(parts); i++ {
			parent := strings.Join(parts[:i], ".")
			if children[parent] == nil {
				children[parent] = make(map[string]bool)
			}
			children[parent][parts[i]] = true
		}
	}
	// Some structs already live at their v8 path and therefore have no remap entry.
	allowedRoots := v8AllowedRoots()
	var includeNative func(reflect.Type, string)
	includeNative = func(t reflect.Type, path string) {
		if t.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if tag == "-" || field.PkgPath != "" {
				continue
			}
			if field.Anonymous {
				includeNative(field.Type, path)
				continue
			}
			if path == "" && !allowedRoots[tag] {
				continue
			}
			if children[path] == nil {
				children[path] = make(map[string]bool)
			}
			children[path][tag] = true
			child := tag
			if path != "" {
				child = path + "." + tag
			}
			includeNative(field.Type, child)
		}
	}
	includeNative(reflect.TypeOf(Config{}), "")
	var walk func(*yaml.Node, string) error
	walk = func(node *yaml.Node, path string) error {
		if node == nil || node.Kind != yaml.MappingNode || children[path] == nil {
			return nil
		}
		if err := commentUnknownV8Fields(node, root, children[path], path); err != nil {
			return err
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			child := path + "." + node.Content[i].Value
			if err := walk(node.Content[i+1], child); err != nil {
				return err
			}
		}
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if err := walk(root.Content[i+1], root.Content[i].Value); err != nil {
			return err
		}
	}
	return nil
}

func commentUnknownV8Fields(node *yaml.Node, archive *yaml.Node, allowed map[string]bool, path string) error {
	var comments []string
	for i := 0; i+1 < len(node.Content); {
		key := node.Content[i].Value
		if allowed[key] {
			i += 2
			continue
		}
		section := key
		if path != "" {
			section = path + "." + key
		}
		warnUnrecognizedV8Section(section)
		keyNode := *node.Content[i]
		keyNode.Value = section
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{&keyNode, node.Content[i+1]}}
		data, errMarshal := yaml.Marshal(entry)
		if errMarshal != nil {
			return errMarshal
		}
		text := strings.TrimSuffix(string(data), "\n")
		comments = append(comments, "# "+strings.ReplaceAll(text, "\n", "\n# "))
		node.Content = append(node.Content[:i], node.Content[i+2:]...)
	}
	// Archive at the root so deleting a neighboring nested field cannot drop it.
	if len(comments) > 0 {
		archive.FootComment = strings.TrimSpace(archive.FootComment + "\n" + strings.Join(comments, "\n"))
	}
	return nil
}

// The deprecated allow flag is the inverse of disable-private-remote-ips.
// Resolve it before applying v8 precedence so both names cannot reach the runtime
// decoder together. Legacy-only files are left untouched until a migration/save.
func normalizeV8PrivateIPAlias(root *yaml.Node, migrate bool) (bool, error) {
	const old = "codex.live-media-relay.allow-private-remote-ips"
	const canonical = "codex.live-media-relay.disable-private-remote-ips"
	value := yamlPath(root, old)
	if value == nil {
		return false, nil
	}
	if yamlPath(root, "oauth.providers."+canonical) != nil {
		return deleteYAMLPath(root, old), nil
	}
	if !migrate || yamlPath(root, canonical) != nil {
		return false, nil
	}
	var allow bool
	if err := value.Decode(&allow); err != nil {
		return false, fmt.Errorf("decode %s: %w", old, err)
	}
	replacement := deepCopyNode(value)
	if err := replacement.Encode(!allow); err != nil {
		return false, err
	}
	setYAMLPath(root, canonical, replacement)
	deleteYAMLPath(root, old)
	return true, nil
}

// restoreV8Layout lets v0 handlers update the effective runtime fields without
// reintroducing their legacy spellings into an existing v8 document.
func restoreV8Layout(root, layout *yaml.Node, original []byte, generated *yaml.Node) error {
	for _, path := range v8Paths {
		upstreams := yamlPath(layout, "api-keys")
		clientKeyCollision := path.old == "api-keys" && upstreams != nil && upstreams.Kind == yaml.MappingNode
		if yamlPath(layout, path.current) == nil && !clientKeyCollision {
			continue
		}
		value := legacyPath(root, path.old)
		if value == nil {
			continue
		}
		copy := deepCopyNode(value)
		deleteYAMLPath(root, path.old)
		setYAMLPath(root, path.current, copy)
	}
	var baseline *yaml.Node
	for _, family := range v8KeyFamilies {
		groups := yamlPath(layout, "api-keys."+family.current)
		if groups == nil {
			continue
		}
		if baseline == nil {
			cfg, err := ParseConfigBytes(original)
			if err != nil {
				return err
			}
			baseline = new(yaml.Node)
			if err = baseline.Encode((*legacyConfig)(cfg)); err != nil {
				return err
			}
		}
		var before, after any
		if value := yamlPath(baseline, family.old); value != nil {
			if err := value.Decode(&before); err != nil {
				return err
			}
		}
		if value := yamlPath(generated, family.old); value != nil {
			if err := value.Decode(&after); err != nil {
				return err
			}
		}
		if !reflect.DeepEqual(before, after) {
			keys := yamlPath(root, family.old)
			if keys == nil {
				keys = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			}
			groups = groupLegacyKeys(keys, family.current)
		}
		deleteYAMLPath(root, family.old)
		setYAMLPath(root, "api-keys."+family.current, groups)
	}
	preserveV8Comments(root, layout)
	return nil
}

func preserveV8Comments(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	dst.HeadComment, dst.LineComment, dst.FootComment = src.HeadComment, src.LineComment, src.FootComment
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(src.Content); i += 2 {
		if index := findMapKeyIndex(dst, src.Content[i].Value); index >= 0 {
			preserveV8Comments(dst.Content[index], src.Content[i])
			preserveV8Comments(dst.Content[index+1], src.Content[i+1])
		}
	}
}

// Expand aliases and YAML merge keys before moving paths. Keeping an alias to a
// removed legacy anchor would produce an unreadable file, and updating a shared
// anchor in place could unintentionally change an unrelated setting.
// Callers first decode the document so yaml.v3 rejects cyclic/invalid aliases.
func expandConfigAliases(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return expandConfigAliases(node.Alias)
	}
	copy := *node
	copy.Anchor = ""
	copy.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		copy.Content[i] = expandConfigAliases(child)
	}
	if copy.Kind != yaml.MappingNode {
		return &copy
	}
	for i := 0; i < len(copy.Content); i += 2 {
		if copy.Content[i].Tag != "!!merge" {
			continue
		}
		merge := copy.Content[i+1]
		copy.Content = append(copy.Content[:i], copy.Content[i+2:]...)
		mappings := []*yaml.Node{merge}
		if merge.Kind == yaml.SequenceNode {
			mappings = merge.Content
		}
		for _, mapping := range mappings {
			for j := 0; j < len(mapping.Content); j += 2 {
				if findMapKeyIndex(&copy, mapping.Content[j].Value) < 0 {
					copy.Content = append(copy.Content, mapping.Content[j], mapping.Content[j+1])
				}
			}
		}
		i -= 2
	}
	return &copy
}

// ValidateV8Config accepts only the v8 layout in management writes.
// Plugin-owned configuration remains extensible through its existing decoder.
func ValidateV8Config(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("empty config")
	}
	root := doc.Content[0]
	flat, err := flattenV8(root)
	if err != nil {
		return err
	}
	root = expandConfigAliases(root)
	allowedRoots := v8AllowedRoots()
	for _, path := range append(append([]configPath(nil), v8Paths...), v8ClientPaths...) {
		if legacyPath(root, path.old) != nil {
			return fmt.Errorf("legacy field %s is not accepted by v8; use %s", path.old, path.current)
		}
	}
	for i := 0; i < len(root.Content); i += 2 {
		if key := root.Content[i].Value; !allowedRoots[key] {
			return fmt.Errorf("unknown v8 configuration section %s", key)
		}
	}
	if groups := yamlPath(root, "api-keys"); groups != nil && groups.Kind == yaml.MappingNode {
		for i := 0; i < len(groups.Content); i += 2 {
			found := false
			for _, family := range v8KeyFamilies {
				if groups.Content[i].Value == family.current {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("unknown API-key provider %s", groups.Content[i].Value)
			}
		}
	}
	deleteYAMLPath(flat, "config-version")
	// Empty struct containers are valid replacements. Only strip known structural
	// paths; empty user maps (headers, aliases, plugin options) carry real values.
	for _, path := range v8Paths {
		parts := strings.Split(path.current, ".")
		for end := len(parts) - 1; end > 0; end-- {
			container := strings.Join(parts[:end], ".")
			if value := yamlPath(flat, container); value != nil && value.Kind == yaml.MappingNode && len(value.Content) == 0 {
				deleteYAMLPath(flat, container)
			}
		}
	}
	encoded, err := yaml.Marshal(flat)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	var cfg legacyConfig
	return decoder.Decode(&cfg)
}
