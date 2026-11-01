package config

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// keyPayloadLayout identifies key entries and their nested collection fields.
type keyPayloadLayout struct {
	path     string
	keysPath string
	fields   []reflect.StructField
}

// keyPayloadFields discovers collections containing keys with local filter policies.
func keyPayloadFields(valueType reflect.Type) []reflect.StructField {
	var fields []reflect.StructField
	for index := range valueType.NumField() {
		field := valueType.Field(index)
		if field.PkgPath != "" || strings.Split(field.Tag.Get("yaml"), ",")[0] == "-" || field.Type.Kind() != reflect.Slice || field.Type.Elem().Kind() != reflect.Struct {
			continue
		}
		if !reflect.PointerTo(field.Type.Elem()).Implements(reflect.TypeFor[PayloadFilterSource]()) && len(keyPayloadFields(field.Type.Elem())) == 0 {
			continue
		}
		fields = append(fields, field)
	}
	return fields
}

// keyPayloadPath resolves a collection's serialized name through existing layout mappings.
func keyPayloadPath(field reflect.StructField) configPath {
	name := strings.Split(field.Tag.Get("yaml"), ",")[0]
	path := configPath{old: name, current: name}
	for _, family := range v8KeyFamilies {
		if family.old != name {
			continue
		}
		path = family
	}
	return path
}

// KeyPayloadFilters projects nonempty local policies by flattened diagnostic path.
func (cfg *Config) KeyPayloadFilters() map[string][]PayloadFilterRule {
	if cfg == nil {
		return nil
	}
	filters := make(map[string][]PayloadFilterRule)
	collectKeyPayloadFilters(reflect.ValueOf(cfg).Elem(), "", filters)
	return filters
}

// collectKeyPayloadFilters follows policy-bearing collections without copying credentials.
func collectKeyPayloadFilters(value reflect.Value, prefix string, filters map[string][]PayloadFilterRule) {
	for _, field := range keyPayloadFields(value.Type()) {
		collection := value.FieldByIndex(field.Index)
		fields := keyPayloadFields(field.Type.Elem())
		for index := range collection.Len() {
			entry := collection.Index(index)
			if len(fields) == 0 && len(PayloadFiltersFor(entry.Addr().Interface().(PayloadFilterSource))) == 0 {
				continue
			}
			path := fmt.Sprintf("%s%s[%d]", prefix, keyPayloadPath(field).current, index)
			if len(fields) != 0 {
				collectKeyPayloadFilters(entry, path+".", filters)
			} else {
				filters[path+".payload.filter"] = PayloadFiltersFor(entry.Addr().Interface().(PayloadFilterSource))
			}
		}
	}
}

// validateKeyPayloads checks local policies without losing their source indexes.
func validateKeyPayloads(root *yaml.Node) error {
	for _, field := range keyPayloadFields(reflect.TypeFor[Config]()) {
		family := keyPayloadPath(field)
		for _, layout := range []keyPayloadLayout{
			{path: family.old, fields: keyPayloadFields(field.Type.Elem())},
			{path: "api-keys." + family.current, keysPath: "keys"},
		} {
			if err := validateKeyPayloadSequence(yamlPath(root, layout.path), layout, field); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateKeyPayloadSequence traverses individual keys and schema-derived nested lists.
func validateKeyPayloadSequence(node *yaml.Node, layout keyPayloadLayout, scope reflect.StructField) error {
	if node == nil || node.Tag == "!!null" || node.Kind != yaml.SequenceNode {
		return nil
	}
	for index, entry := range node.Content {
		entryLayout := layout
		entryLayout.path = fmt.Sprintf("%s[%d]", layout.path, index)
		if err := validateKeyPayloadEntry(entry, entryLayout, scope); err != nil {
			return err
		}
	}
	return nil
}

// validateKeyPayloadEntry enforces individual-key ownership before validating its policy.
func validateKeyPayloadEntry(node *yaml.Node, layout keyPayloadLayout, scope reflect.StructField) error {
	if layout.keysPath == "" && len(layout.fields) == 0 {
		return validateKeyPayloadPolicy(yamlPath(node, "payload"), layout.path+".payload", scope)
	}
	if err := validateKeyPayloadGroup(node, layout.path, keyPayloadPath(scope).current); err != nil {
		return err
	}
	if layout.keysPath != "" {
		return validateKeyPayloadSequence(yamlPath(node, layout.keysPath), keyPayloadLayout{path: layout.path + "." + layout.keysPath}, scope)
	}
	for _, field := range layout.fields {
		path := keyPayloadPath(field).old
		child := keyPayloadLayout{path: layout.path + "." + path, fields: keyPayloadFields(field.Type.Elem())}
		if err := validateKeyPayloadSequence(yamlPath(node, path), child, scope); err != nil {
			return err
		}
	}
	return nil
}

// validateKeyPayloadGroup rejects policies outside an individual credential definition.
func validateKeyPayloadGroup(node *yaml.Node, path, scope string) error {
	if yamlPath(node, "payload") == nil {
		return nil
	}
	return fmt.Errorf("%s.payload: payload belongs to an individual %s key, not its group", path, scope)
}

// validateKeyPayloadPolicy decodes the existing grammar and checks parent destination scope.
func validateKeyPayloadPolicy(node *yaml.Node, path string, scope reflect.StructField) error {
	if node == nil || node.Tag == "!!null" {
		return nil
	}
	if err := validateKeyPayloadShape(node, reflect.TypeOf(KeyPayloadConfig{}), path); err != nil {
		return err
	}
	var policy KeyPayloadConfig
	if err := node.Decode(&policy); err != nil {
		return fmt.Errorf("%s: invalid key filter values", path)
	}
	destinations := strings.Split(scope.Tag.Get("payload-protocols"), ",")
	for ruleIndex, rule := range policy.Filter {
		for modelIndex, model := range rule.Models {
			protocol := strings.TrimSpace(model.Protocol)
			supported := protocol == ""
			for _, destination := range destinations {
				supported = supported || strings.EqualFold(protocol, destination)
			}
			if !supported {
				return fmt.Errorf("%s.filter[%d].models[%d].protocol: destination %q conflicts with parent %s key scope; expected %s", path, ruleIndex, modelIndex, model.Protocol, keyPayloadPath(scope).current, strings.Join(destinations, ", "))
			}
		}
	}
	return nil
}

// validateKeyPayloadShape derives field names and shapes from production rule types.
func validateKeyPayloadShape(node *yaml.Node, valueType reflect.Type, path string) error {
	if node.Tag == "!!null" && (valueType.Kind() == reflect.Slice || valueType.Kind() == reflect.Map || valueType.Kind() == reflect.Interface) {
		return nil
	}
	switch valueType.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: expected a mapping", path)
		}
		fields := make(map[string]reflect.Type, valueType.NumField())
		for index := range valueType.NumField() {
			field := valueType.Field(index)
			fields[field.Tag.Get("yaml")] = field.Type
		}
		for index := 0; index < len(node.Content); index += 2 {
			field := node.Content[index].Value
			fieldType, ok := fields[field]
			if !ok {
				return fmt.Errorf("%s.%s: unsupported field in key payload", path, field)
			}
			if err := validateKeyPayloadShape(node.Content[index+1], fieldType, path+"."+field); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if node.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s: expected a list", path)
		}
		for index, entry := range node.Content {
			if err := validateKeyPayloadShape(entry, valueType.Elem(), fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return fmt.Errorf("%s: expected a mapping", path)
		}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("%s: expected string mapping keys", path)
			}
			if err := validateKeyPayloadShape(node.Content[index+1], valueType.Elem(), path+"."+key.Value); err != nil {
				return err
			}
		}
	case reflect.String:
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return fmt.Errorf("%s: expected a string", path)
		}
	case reflect.Interface:
		// Preserve arbitrary condition values for decoding by the production rule type.
		return nil
	default:
		return fmt.Errorf("%s: unsupported key-payload type %s", path, valueType)
	}
	return nil
}

// SanitizePayloadRules drops invalid rules and skips invalid regex entries individually.
func (cfg *Config) SanitizePayloadRules() {
	if cfg == nil {
		return
	}
	cfg.Payload.DefaultRaw = sanitizePayloadRawRules(cfg.Payload.DefaultRaw, "default-raw")
	cfg.Payload.OverrideRaw = sanitizePayloadRawRules(cfg.Payload.OverrideRaw, "override-raw")
	if len(cfg.Payload.Replace) > 0 {
		valid := make([]PayloadReplaceRule, 0, len(cfg.Payload.Replace))
		for i, rule := range cfg.Payload.Replace {
			if errValidate := rule.Validate(); errValidate != nil {
				log.Warnf("payload.replace rule %d dropped: invalid rule", i+1)
				continue
			}
			replacements := make([]PayloadReplacement, 0, len(rule.Replacements))
			for j, replacement := range rule.Replacements {
				// Validation guarantees that two-key entries are regex replacements.
				if len(replacement) == 2 {
					pattern := replacement["pattern"]
					if _, errCompile := regexp.Compile(pattern); pattern == "" || errCompile != nil {
						log.Warnf("payload.replace rule %d replacement %d skipped: invalid regex", i+1, j+1)
						continue
					}
				}
				replacements = append(replacements, replacement)
			}
			if len(replacements) == 0 {
				continue
			}
			rule.Replacements = replacements
			valid = append(valid, rule)
		}
		cfg.Payload.Replace = valid
	}
}

func sanitizePayloadRawRules(rules []PayloadRule, section string) []PayloadRule {
	if len(rules) == 0 {
		return rules
	}
	out := make([]PayloadRule, 0, len(rules))
	for i := range rules {
		rule := rules[i]
		if len(rule.Params) == 0 {
			continue
		}
		invalid := false
		for path, value := range rule.Params {
			raw, ok := payloadRawString(value)
			if !ok {
				continue
			}
			trimmed := bytes.TrimSpace(raw)
			if len(trimmed) == 0 || !json.Valid(trimmed) {
				log.WithFields(log.Fields{
					"section":    section,
					"rule_index": i + 1,
					"param":      path,
				}).Warn("payload rule dropped: invalid raw JSON")
				invalid = true
				break
			}
		}
		if invalid {
			continue
		}
		out = append(out, rule)
	}
	return out
}

func payloadRawString(value any) ([]byte, bool) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		return nil, false
	}
}

// looksLikeBcrypt recognizes supported bcrypt encodings without treating an
// arbitrary plaintext string with a bcrypt prefix as safe to upload.
func looksLikeBcrypt(s string) bool {
	if len(s) != 60 || (s[:4] != "$2a$" && s[:4] != "$2b$" && s[:4] != "$2y$") || s[6] != '$' {
		return false
	}
	for _, c := range s[7:] {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '/' {
			return false
		}
	}
	_, errCost := bcrypt.Cost([]byte(s))
	return errCost == nil
}

// hashSecret hashes the given secret using bcrypt.
func hashSecret(secret string) (string, error) {
	// Use default cost for simplicity.
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashedBytes), nil
}
