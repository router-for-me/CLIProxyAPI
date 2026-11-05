package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// LoadConfig reads a YAML configuration file from the given path,
// unmarshals it into a Config struct, applies environment variable overrides,
// and returns it.
//
// Parameters:
//   - configFile: The path to the YAML configuration file
//
// Returns:
//   - *Config: The loaded configuration
//   - error: An error if the configuration could not be loaded
func LoadConfig(configFile string) (*Config, error) {
	return LoadConfigOptional(configFile, false)
}

// ComputeSourceHash returns the Config.SourceHash value for config file bytes.
func ComputeSourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PrepareConfigFileForUpload applies the file rewrites of a startup load: a plaintext
// management secret is replaced with its bcrypt hash and conflicting legacy fields are
// removed. Stores that seed remote storage from configFile before the startup load call
// it first, so the plaintext secret never leaves the machine and the local and remote
// copies stay identical. When the plaintext cannot be removed it returns an error and
// leaves configFile untouched, so every later attempt refuses the file again.
func PrepareConfigFileForUpload(configFile string) error {
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	data, errRead := os.ReadFile(configFile)
	if errRead != nil {
		return errRead
	}
	prepared, errPrepare := prepareConfigForUpload(data)
	if errPrepare != nil {
		return errPrepare
	}
	if bytes.Equal(prepared, data) {
		return nil
	}
	if errWrite := os.WriteFile(configFile, prepared, 0600); errWrite != nil {
		return fmt.Errorf("failed to write prepared config file: %w", errWrite)
	}
	return nil
}

// prepareConfigForUpload returns data with the file rewrites of a startup load applied.
// It fails when the result would still hold a plaintext management secret: hashing
// replaces only the effective key and the layout cleanup only drops an overridden legacy
// key, so a YAML anchor or a copy elsewhere keeps the plaintext, and rewriting those could
// change other settings that share them.
func prepareConfigForUpload(data []byte) ([]byte, error) {
	var cfg Config
	if errParse := yaml.Unmarshal(data, &cfg); errParse != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", errParse)
	}
	prepared := data
	if secret := cfg.RemoteManagement.SecretKey; secret != "" && !looksLikeBcrypt(secret) {
		hashed, errHash := hashSecret(secret)
		if errHash != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", errHash)
		}
		updated, errUpdate := updateNestedScalarYAML(data, managementSecretKeyPath(data), hashed)
		if errUpdate != nil {
			return nil, fmt.Errorf("failed to update remote management key: %w", errUpdate)
		}
		prepared = updated
	}
	cleaned, _, errLayout := NormalizeConfigLayout(prepared, false)
	if errLayout != nil {
		return nil, errLayout
	}
	if plaintexts := PlaintextManagementSecrets(data); len(plaintexts) > 0 {
		var doc yaml.Node
		if errParse := yaml.Unmarshal(cleaned, &doc); errParse != nil {
			return nil, fmt.Errorf("failed to parse prepared config: %w", errParse)
		}
		for _, plaintext := range plaintexts {
			if yamlScalarExists(&doc, plaintext) || (len(doc.Content) > 0 && secretEntryCommentHolds(doc.Content[0], plaintext)) {
				return nil, errors.New("plaintext remote management key is still present elsewhere in the config file, for example through a YAML anchor, as another setting's value or in a comment")
			}
		}
	}
	return cleaned, nil
}

// plaintextManagementSecrets returns the plaintext management secrets of data: the
// effective one and the values of the legacy and v8 secret keys, since an overridden
// legacy key still holds its own. YAML anchors, aliases and merge keys are resolved, so
// data must already decode as a Config, which rejects cyclic aliases.
func plaintextManagementSecrets(data []byte, effective string) []string {
	candidates := []string{effective}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) == nil && len(doc.Content) > 0 {
		root := expandConfigAliases(doc.Content[0])
		for _, path := range []string{"remote-management.secret-key", "management.secret-key"} {
			if node := yamlPath(root, path); node != nil && node.Kind == yaml.ScalarNode && node.ShortTag() != "!!null" {
				candidates = append(candidates, yamlScalarText(node))
			}
		}
	}
	var plaintexts []string
	for _, candidate := range candidates {
		if candidate != "" && !looksLikeBcrypt(candidate) {
			plaintexts = append(plaintexts, candidate)
		}
	}
	return plaintexts
}

// yamlScalarExists reports whether node or any value below it is a non-null scalar equal
// to value, by its raw spelling or by the value a tag decodes it to. Mapping keys only
// name settings and are skipped, as are alias nodes, whose anchors are visited where
// they are defined.
func yamlScalarExists(node *yaml.Node, value string) bool {
	if node.Kind == yaml.ScalarNode {
		return node.ShortTag() != "!!null" && (node.Value == value || yamlScalarText(node) == value)
	}
	first, step := 0, 1
	if node.Kind == yaml.MappingNode {
		first, step = 1, 2
	}
	for i := first; i < len(node.Content); i += step {
		if yamlScalarExists(node.Content[i], value) {
			return true
		}
	}
	return false
}

// secretEntryCommentHolds reports whether a comment on a management secret entry (the
// secret-key key or its value, in either section) holds value. The hashed write-back
// rewrites that value in place and keeps those comments, so `secret-key: s3cret # s3cret`
// would still carry the plaintext. A comment right after the section's last entry is
// the section key's foot comment and counts too. Comments elsewhere are left alone: a
// short secret would match unrelated words in them.
func secretEntryCommentHolds(root *yaml.Node, value string) bool {
	if root.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(root.Content); i += 2 {
			switch yamlScalarText(resolveYAMLAlias(root.Content[i])) {
			case "management", "remote-management":
				if strings.Contains(root.Content[i].FootComment, value) {
					return true
				}
			}
		}
	}
	for _, section := range []string{"management", "remote-management"} {
		for _, mapping := range yamlKeyValues(root, section, map[*yaml.Node]bool{}) {
			if mapping.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(mapping.Content); i += 2 {
				key, val := mapping.Content[i], mapping.Content[i+1]
				if yamlScalarText(resolveYAMLAlias(key)) == "secret-key" && (yamlCommentHolds(key, value) || yamlCommentHolds(val, value)) {
					return true
				}
			}
		}
	}
	return false
}

// yamlCommentHolds reports whether a comment attached to node contains value.
func yamlCommentHolds(node *yaml.Node, value string) bool {
	return strings.Contains(node.HeadComment, value) || strings.Contains(node.LineComment, value) || strings.Contains(node.FootComment, value)
}

// managementSecretKeyPath returns the management secret key path used by data: the v8
// path when the document defines it, the legacy path otherwise.
func managementSecretKeyPath(data []byte) []string {
	var source yaml.Node
	if yaml.Unmarshal(data, &source) == nil && len(source.Content) > 0 && yamlPath(expandConfigAliases(source.Content[0]), "management.secret-key") != nil {
		return []string{"management", "secret-key"}
	}
	return []string{"remote-management", "secret-key"}
}

// LoadConfigOptional reads YAML from configFile.
// If optional is true and the file is missing, it returns an empty Config.
// If optional is true and the file is empty or invalid, it returns an empty Config.
func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	// Read the entire configuration file into memory.
	data, err := os.ReadFile(configFile)
	if err != nil {
		if optional {
			if os.IsNotExist(err) || errors.Is(err, syscall.EISDIR) {
				// Missing and optional: return empty config (cloud deploy standby).
				cfg := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
				cfg.NormalizePluginsConfig()
				cfg.NormalizeClaudeConfig()
				return cfg, nil
			}
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	return loadConfigData(configFile, data, optional, true)
}

// LoadConfigData parses config YAML that was already read from configFile, so a
// caller that hashed those bytes loads exactly them. A plaintext management secret
// is hashed and written back only while configFile still holds data, so a newer
// save is never overwritten; conflicting legacy fields are left for startup.
func LoadConfigData(configFile string, data []byte) (*Config, error) {
	return loadConfigData(configFile, data, false, false)
}

// HasPlaintextSecretKey reports whether config YAML holds a management secret that
// is not bcrypt-hashed yet at either path the loader reads one from:
// management.secret-key or the legacy remote-management.secret-key, even when the
// loader would use the other one. Aliases and merge keys are followed to the node
// that defines each value, so a plaintext anchor a secret path reads counts too,
// as does a merged value a direct key shadows. YAML that does not parse reports
// false.
func HasPlaintextSecretKey(data []byte) bool {
	var root yaml.Node
	if yaml.Unmarshal(data, &root) != nil || len(root.Content) == 0 {
		return false
	}
	for _, secret := range managementSecretNodes(root.Content[0]) {
		if !emptyOrHashedSecret(secret) {
			return true
		}
	}
	return false
}

// managementSecretNodes returns every value either secret path can read in the
// document root: own and merged-in values, aliases resolved, including merged
// values a direct key shadows.
func managementSecretNodes(root *yaml.Node) []*yaml.Node {
	var secrets []*yaml.Node
	for _, path := range [][]string{{"management", "secret-key"}, {"remote-management", "secret-key"}} {
		nodes := []*yaml.Node{root}
		for _, key := range path {
			var next []*yaml.Node
			for _, node := range nodes {
				next = append(next, yamlKeyValues(node, key, map[*yaml.Node]bool{})...)
			}
			nodes = next
		}
		secrets = append(secrets, nodes...)
	}
	return secrets
}

// emptyOrHashedSecret reports whether a secret value node holds no plaintext: an
// empty value, an explicit YAML null (`null`, `~`, which decodes as empty) or a
// bcrypt hash. A quoted 'null' is a string and holds plaintext.
func emptyOrHashedSecret(secret *yaml.Node) bool {
	if secret.Kind != yaml.ScalarNode {
		return false
	}
	// A tagged value (`!!binary <base64 of the hash>`) is classified as it decodes.
	text := yamlScalarText(secret)
	return secret.ShortTag() == "!!null" || text == "" || looksLikeBcrypt(text)
}

// PlaintextManagementSecrets returns the plaintext management secrets config YAML
// holds: the effective one and the values at both secret paths, with aliases and
// merge keys resolved, plus every raw scalar either path can read, including a
// merged value a direct key shadows. Hashing replaces only the effective key and
// the rewrite can drop the merge, leaving a shadowed source as a plain setting, so
// a caller that must keep the plaintext out of a remote mirror records these
// before the hashed write-back and checks the rewritten file with
// HoldsAnyScalarValue. YAML that does not decode as a Config returns none.
func PlaintextManagementSecrets(data []byte) []string {
	var cfg Config
	if yaml.Unmarshal(data, &cfg) != nil {
		return nil
	}
	secrets := plaintextManagementSecrets(data, cfg.RemoteManagement.SecretKey)
	var root yaml.Node
	if yaml.Unmarshal(data, &root) == nil && len(root.Content) > 0 {
		for _, secret := range managementSecretNodes(root.Content[0]) {
			if secret.Kind == yaml.ScalarNode && !emptyOrHashedSecret(secret) {
				secrets = append(secrets, secret.Value)
			}
		}
	}
	return secrets
}

// HoldsAnyScalarValue reports whether config YAML holds any of values as a scalar
// anywhere, such as at a YAML anchor a hashed key no longer refers to. Mapping keys
// count too: an anchor on a key (`&pw plaintext: x` with `secret-key: *pw`) leaves
// the plaintext as that key. So do comments, which the write-back keeps, when they
// hold a value as a whole word. YAML that does not parse reports true, so unreadable
// bytes never count as clean.
func HoldsAnyScalarValue(data []byte, values []string) bool {
	if len(values) == 0 {
		return false
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil {
		return true
	}
	for _, value := range values {
		if yamlScalarOrKeyExists(&doc, value) {
			return true
		}
	}
	return false
}

// yamlScalarOrKeyExists is yamlScalarExists with mapping keys and comments
// included: the hashed write-back keeps every comment, so a comment that repeats
// the plaintext as a word (`secret-key: s3cret # s3cret`) still holds it. A tagged
// scalar matches by its raw spelling and by the value it decodes to.
func yamlScalarOrKeyExists(node *yaml.Node, value string) bool {
	if yamlCommentHoldsWord(node, value) {
		return true
	}
	if node.Kind == yaml.ScalarNode {
		return node.ShortTag() != "!!null" && (node.Value == value || yamlScalarText(node) == value)
	}
	for _, child := range node.Content {
		if yamlScalarOrKeyExists(child, value) {
			return true
		}
	}
	return false
}

// yamlKeyValues returns the values key has in mapping, its own and those merged
// in, each resolved through aliases to the node that defines it. Key names are
// resolved too: `*k : value` with `k` anchoring "secret-key" sets secret-key, as
// the loader decodes it, and so are tagged ones (`!!binary c2VjcmV0LWtleQ==`).
// seen stops a merge that refers back to a mapping being searched.
func yamlKeyValues(mapping *yaml.Node, key string, seen map[*yaml.Node]bool) []*yaml.Node {
	mapping = resolveYAMLAlias(mapping)
	if mapping == nil || mapping.Kind != yaml.MappingNode || seen[mapping] {
		return nil
	}
	seen[mapping] = true
	var values []*yaml.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		name, value := resolveYAMLAlias(mapping.Content[i]), resolveYAMLAlias(mapping.Content[i+1])
		if name == nil {
			continue
		}
		if name.Tag != "!!merge" {
			if yamlScalarText(name) == key && value != nil {
				values = append(values, value)
			}
			continue
		}
		sources := []*yaml.Node{value}
		if value != nil && value.Kind == yaml.SequenceNode {
			sources = value.Content
		}
		for _, source := range sources {
			values = append(values, yamlKeyValues(source, key, seen)...)
		}
	}
	return values
}

// yamlScalarText returns a scalar as the loader decodes it, so a tagged spelling
// such as `!!binary c2VjcmV0LWtleQ==` reads as secret-key. A node that does not
// decode as a string keeps its raw value.
func yamlScalarText(node *yaml.Node) string {
	if node.Kind == yaml.ScalarNode {
		var decoded string
		if node.Decode(&decoded) == nil {
			return decoded
		}
	}
	return node.Value
}

func resolveYAMLAlias(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

// loadConfigData parses data. persist allows startup writes to configFile: an
// unchecked secret write-back and removal of conflicting legacy fields. Without
// it, the secret write-back is checked against data.
func loadConfigData(configFile string, data []byte, optional, persist bool) (*Config, error) {
	// In cloud deploy mode (optional=true), if file is empty or contains only whitespace, return empty config.
	if optional && len(bytes.TrimSpace(data)) == 0 {
		cfg := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
		cfg.NormalizePluginsConfig()
		cfg.NormalizeClaudeConfig()
		return cfg, nil
	}

	if errValidate := validateCredentialWeightYAML(data); errValidate != nil {
		if optional {
			cfgOptional := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
			cfgOptional.NormalizePluginsConfig()
			cfgOptional.NormalizeClaudeConfig()
			return cfgOptional, nil
		}
		return nil, errValidate
	}

	// Unmarshal the YAML data into the Config struct.
	var cfg Config
	// Set defaults before unmarshal so that absent keys keep defaults.
	cfg.Host = "" // Default empty: binds to all interfaces (IPv4 + IPv6)
	cfg.LoggingToFile = false
	cfg.LogsMaxTotalSizeMB = 0
	cfg.ErrorLogsMaxFiles = 10
	cfg.UsageStatisticsEnabled = false
	cfg.RedisUsageQueueRetentionSeconds = 60
	cfg.DisableCooling = false
	cfg.SaveCooldownStatus = false
	cfg.TransientErrorCooldownSeconds = 0
	cfg.DisableImageGeneration = DisableImageGenerationOff
	cfg.WebsocketAuth = true
	cfg.Pprof.Enable = false
	cfg.Pprof.Addr = DefaultPprofAddr
	cfg.Discovery.Enabled = false
	cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages", "_generate-content", "_interactions"}
	cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	cfg.CredentialInFlight = DefaultCredentialInFlightConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		if optional {
			// In cloud deploy mode, if YAML parsing fails, return empty config instead of error.
			cfgOptional := &Config{CredentialInFlight: DefaultCredentialInFlightConfig()}
			cfgOptional.NormalizePluginsConfig()
			cfgOptional.NormalizeClaudeConfig()
			return cfgOptional, nil
		}
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	if errValidate := validateTrustedProxies(cfg.TrustedProxies); errValidate != nil {
		return nil, errValidate
	}

	cfg.CredentialConcurrency = cfg.CredentialConcurrency.WithDefaults()
	if errValidate := cfg.CredentialInFlight.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if cfg.Discovery.ServiceType == "" {
		cfg.Discovery.ServiceType = DefaultDiscoveryServiceType
	}
	if len(cfg.Discovery.Subtypes) == 0 {
		cfg.Discovery.Subtypes = []string{"_chat-completions", "_responses", "_messages", "_generate-content", "_interactions"}
	}
	if errValidate := cfg.Codex.LiveMediaRelay.Validate(); errValidate != nil {
		return nil, errValidate
	}
	if errValidate := cfg.ValidateCredentialWeights(); errValidate != nil {
		return nil, errValidate
	}

	// Hash remote management key if plaintext is detected (nested)
	// We consider a value to be already hashed if it looks like a bcrypt hash ($2a$, $2b$, or $2y$ prefix).
	if cfg.RemoteManagement.SecretKey != "" && !looksLikeBcrypt(cfg.RemoteManagement.SecretKey) {
		hashed, errHash := hashSecret(cfg.RemoteManagement.SecretKey)
		if errHash != nil {
			return nil, fmt.Errorf("failed to hash remote management key: %w", errHash)
		}
		cfg.RemoteManagement.SecretKey = hashed

		// Persist the hashed value back to the config file to avoid re-hashing on next startup
		// and to keep the plaintext out of remote store mirrors. Preserve YAML comments and
		// ordering; update only the nested key.
		// A hot reload may parse bytes a newer save (perhaps with a rotated secret)
		// already replaced, so it writes back only over the bytes it parsed.
		var expected []byte
		if !persist {
			expected = data
		}
		configWriteMu.Lock()
		errWriteBack := updateNestedScalarLocked(configFile, expected, managementSecretKeyPath(data), hashed)
		configWriteMu.Unlock()
		if errWriteBack != nil {
			// Remote stores refuse to mirror the plaintext, so they keep the previous
			// config until the file can be written.
			log.WithError(errWriteBack).Warnf("failed to write the hashed management secret back to %s", configFile)
		}
	}

	cfg.RemoteManagement.PanelGitHubRepository = strings.TrimSpace(cfg.RemoteManagement.PanelGitHubRepository)
	if cfg.RemoteManagement.PanelGitHubRepository == "" {
		cfg.RemoteManagement.PanelGitHubRepository = DefaultPanelGitHubRepository
	}

	cfg.Pprof.Addr = strings.TrimSpace(cfg.Pprof.Addr)
	if cfg.Pprof.Addr == "" {
		cfg.Pprof.Addr = DefaultPprofAddr
	}

	cfg.KVStore.Path = strings.TrimSpace(cfg.KVStore.Path)

	if cfg.LogsMaxTotalSizeMB < 0 {
		cfg.LogsMaxTotalSizeMB = 0
	}

	if cfg.ErrorLogsMaxFiles < 0 {
		cfg.ErrorLogsMaxFiles = 10
	}

	if cfg.RedisUsageQueueRetentionSeconds <= 0 {
		cfg.RedisUsageQueueRetentionSeconds = 60
	} else if cfg.RedisUsageQueueRetentionSeconds > 3600 {
		log.WithField("value", cfg.RedisUsageQueueRetentionSeconds).Warn("redis-usage-queue-retention-seconds too large; clamping to 3600")
		cfg.RedisUsageQueueRetentionSeconds = 3600
	}

	if cfg.MaxRetryCredentials < 0 {
		cfg.MaxRetryCredentials = 0
	}

	cfg.NormalizePluginsConfig()
	cfg.NormalizeClaudeConfig()
	if errResolvePluginsDir := cfg.ResolvePluginsDir(); errResolvePluginsDir != nil && cfg.Plugins.Enabled {
		return nil, errResolvePluginsDir
	}

	// Sanitize Gemini API key configuration and migrate legacy entries.
	cfg.SanitizeGeminiKeys()

	// Sanitize native Interactions API key configuration.
	cfg.SanitizeInteractionsKeys()

	// Sanitize Vertex-compatible API keys.
	cfg.SanitizeVertexCompatKeys()

	// Sanitize Codex keys: drop entries without base-url
	cfg.SanitizeCodexKeys()

	// Sanitize xAI keys: drop entries without base-url
	cfg.SanitizeXAIKeys()

	// Sanitize Meta keys.
	cfg.SanitizeMetaKeys()

	// Sanitize Codex header defaults.
	cfg.SanitizeCodexHeaderDefaults()

	// Sanitize Claude header defaults.
	cfg.SanitizeClaudeHeaderDefaults()

	// Sanitize Claude key headers
	cfg.SanitizeClaudeKeys()

	// Sanitize OpenAI compatibility providers: drop entries without base-url
	cfg.SanitizeOpenAICompatibility()

	// Normalize OAuth provider model exclusion map.
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)

	// Normalize global OAuth model name aliases.
	cfg.SanitizeOAuthModelAlias()

	// Normalize global OAuth model settings.
	cfg.SanitizeOAuthSettings()

	// Normalize global OAuth request-scoped error rules.
	cfg.SanitizeOAuthRequestScopedErrors()

	// Validate raw payload rules and drop invalid entries.
	cfg.SanitizePayloadRules()

	// Record the exact bytes this config was parsed from, so a consumer can tell
	// it apart from a newer version of the file.
	cfg.SourceHash = ComputeSourceHash(data)

	if !persist {
		return &cfg, nil
	}

	// Only conflicting legacy fields are removed on load. A legacy-only document
	// stays legacy until a v8 configuration write explicitly migrates it.
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	current, errRead := os.ReadFile(configFile)
	if errRead != nil {
		return nil, errRead
	}
	cleaned, changed, errLayout := NormalizeConfigLayout(current, false)
	if errLayout != nil {
		return nil, errLayout
	}
	if changed {
		if errWrite := os.WriteFile(configFile, cleaned, 0600); errWrite != nil {
			return nil, fmt.Errorf("clean conflicting config fields: %w", errWrite)
		}
	}

	// Return the populated configuration struct.
	return &cfg, nil
}

// yamlCommentHoldsWord reports whether a comment attached to node contains value as
// a whole word, with no letter or digit right before or after it. Every comment
// counts, since the write-back keeps them all; a short secret no longer matches
// inside ordinary words ("a" in "Management"), while `key=s3cret` and
// `# s3cret note` still do.
func yamlCommentHoldsWord(node *yaml.Node, value string) bool {
	for _, comment := range []string{node.HeadComment, node.LineComment, node.FootComment} {
		if containsWord(comment, value) {
			return true
		}
	}
	return false
}

// containsWord reports whether s contains word at a position with no letter or
// digit adjacent to either end.
func containsWord(s, word string) bool {
	if word == "" {
		return false
	}
	for start := 0; start <= len(s)-len(word); {
		i := strings.Index(s[start:], word)
		if i < 0 {
			return false
		}
		i += start
		before, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[i+len(word):])
		if !isWordRune(before) && !isWordRune(after) {
			return true
		}
		start = i + 1
	}
	return false
}

// isWordRune reports whether r is a letter or digit; a missing rune is not.
func isWordRune(r rune) bool {
	return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
