package management

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxAccountConfigYAMLBodyBytes int64 = 2 * 1024 * 1024

var errAccountProtectedConfigPath = errors.New("protected config path")

func accountSanitizeJSONConfig(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if accountConfigKeyProtected(key) {
				continue
			}
			out[key] = accountSanitizeJSONConfig(child)
		}
		accountSanitizeJSONRouting(out)
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, accountSanitizeJSONConfig(child))
		}
		return out
	default:
		return value
	}
}

func accountSanitizeJSONRouting(root map[string]any) {
	rawRouting, ok := root["routing"]
	if !ok {
		return
	}
	routing, ok := rawRouting.(map[string]any)
	if !ok {
		return
	}
	if strategy, _ := routing["strategy"].(string); strings.EqualFold(strings.TrimSpace(strategy), "quota-aware") {
		delete(routing, "strategy")
	}
}

func accountConfigKeyProtected(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "remote-management" || normalized == "home" || normalized == "auth-dir" || normalized == "pprof" {
		return true
	}
	return strings.Contains(normalized, "billing") || strings.Contains(normalized, "quota")
}

func decodeAccountConfigYAML(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return accountEmptyDocument(), nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	} else if err == nil && !accountYAMLDocumentEmpty(&extra) {
		return nil, fmt.Errorf("multiple yaml documents are not supported")
	}
	if accountYAMLDocumentEmpty(&doc) {
		return accountEmptyDocument(), nil
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0] == nil {
		return nil, fmt.Errorf("invalid yaml document")
	}
	if err := validateAccountYAMLNode(doc.Content[0]); err != nil {
		return nil, err
	}
	return &doc, nil
}

func accountEmptyDocument() *yaml.Node {
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
}

func accountYAMLDocumentEmpty(doc *yaml.Node) bool {
	return doc == nil || doc.Kind == 0 || (doc.Kind == yaml.DocumentNode && len(doc.Content) == 0)
}

func validateAccountYAMLNode(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("yaml aliases are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key == nil || key.Kind != yaml.ScalarNode {
				return fmt.Errorf("yaml mapping keys must be scalars")
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("duplicate yaml key")
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateAccountYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeAccountConfigYAMLDocument(doc *yaml.Node) *yaml.Node {
	if doc == nil || len(doc.Content) == 0 || doc.Content[0] == nil {
		return accountEmptyDocument()
	}
	out := deepCopyYAMLNode(doc)
	out.Content[0] = sanitizeAccountConfigYAMLNode(out.Content[0], nil, doc.Content[0])
	if out.Content[0] == nil {
		out.Content[0] = &yaml.Node{Kind: yaml.MappingNode}
	}
	return out
}

func sanitizeAccountConfigYAMLNode(node *yaml.Node, path []string, root *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		out := *node
		out.Content = nil
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]
			if key == nil {
				continue
			}
			childPath := append(append([]string(nil), path...), key.Value)
			if accountYAMLPathProtected(childPath, value, root, nil) {
				continue
			}
			child := sanitizeAccountConfigYAMLNode(value, childPath, root)
			if child == nil {
				continue
			}
			out.Content = append(out.Content, deepCopyYAMLNode(key), child)
		}
		return &out
	}
	out := *node
	out.Content = nil
	for _, child := range node.Content {
		out.Content = append(out.Content, sanitizeAccountConfigYAMLNode(child, path, root))
	}
	return &out
}

func mergeAccountConfigYAMLWithProtectedOriginal(originalDoc, requestedDoc *yaml.Node) (*yaml.Node, error) {
	if originalDoc == nil || len(originalDoc.Content) == 0 || originalDoc.Content[0] == nil {
		originalDoc = accountEmptyDocument()
	}
	if requestedDoc == nil || len(requestedDoc.Content) == 0 || requestedDoc.Content[0] == nil {
		requestedDoc = accountEmptyDocument()
	}
	originalRoot := originalDoc.Content[0]
	requestedRoot := requestedDoc.Content[0]
	if err := rejectChangedAccountProtectedYAML(requestedRoot, originalRoot, nil, requestedRoot); err != nil {
		return nil, err
	}
	merged := sanitizeAccountConfigYAMLDocument(requestedDoc)
	restoreAccountProtectedYAMLNodes(merged.Content[0], originalRoot, nil, originalRoot, requestedRoot)
	return merged, nil
}

func rejectChangedAccountProtectedYAML(requested, original *yaml.Node, path []string, requestedRoot *yaml.Node) error {
	if requested == nil {
		return nil
	}
	if requested.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(requested.Content); i += 2 {
		key := requested.Content[i]
		value := requested.Content[i+1]
		if key == nil {
			continue
		}
		childPath := append(append([]string(nil), path...), key.Value)
		if accountYAMLPathProtected(childPath, value, requestedRoot, original) {
			originalValue := accountYAMLLookupPath(original, childPath)
			if originalValue == nil || !accountYAMLNodesSemanticallyEqual(value, originalValue) {
				return errAccountProtectedConfigPath
			}
			continue
		}
		if err := rejectChangedAccountProtectedYAML(value, original, childPath, requestedRoot); err != nil {
			return err
		}
	}
	return nil
}

func restoreAccountProtectedYAMLNodes(target, original *yaml.Node, path []string, originalRoot, requestedRoot *yaml.Node) {
	if original == nil || original.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(original.Content); i += 2 {
		key := original.Content[i]
		value := original.Content[i+1]
		if key == nil {
			continue
		}
		childPath := append(append([]string(nil), path...), key.Value)
		if accountYAMLPathProtected(childPath, value, originalRoot, requestedRoot) {
			accountYAMLSetPath(target, childPath, value)
			continue
		}
		restoreAccountProtectedYAMLNodes(target, value, childPath, originalRoot, requestedRoot)
	}
}

func accountYAMLPathProtected(path []string, value, root, comparisonRoot *yaml.Node) bool {
	if len(path) == 0 {
		return false
	}
	// Generic YAML edits cannot bypass protected plugin lifecycle restrictions.
	if path[0] == "plugins" {
		var scope []string
		if len(path) == 2 && (path[1] == "enabled" || path[1] == "dir") {
			scope = []string{"plugins", "configs"}
		}
		if len(path) == 4 && path[1] == "configs" && path[3] == "enabled" {
			scope = path[:3]
		}
		if scope != nil && (accountPluginConfigContainsProtectedFields(accountYAMLLookupPath(root, scope)) || accountPluginConfigContainsProtectedFields(accountYAMLLookupPath(comparisonRoot, scope))) {
			return true
		}
	}
	for _, segment := range path {
		if accountConfigKeyProtected(segment) {
			return true
		}
	}
	if accountYAMLSequenceContainsProtectedKey(value) {
		return true
	}
	if comparisonValue := accountYAMLLookupPath(comparisonRoot, path); accountYAMLSequenceContainsProtectedKey(comparisonValue) {
		return true
	}
	if len(path) == 2 && path[0] == "routing" && path[1] == "strategy" {
		if accountYAMLScalarIsQuotaAware(value) {
			return true
		}
		if rootStrategy := accountYAMLLookupPath(root, []string{"routing", "strategy"}); accountYAMLScalarIsQuotaAware(rootStrategy) {
			return true
		}
		if comparisonStrategy := accountYAMLLookupPath(comparisonRoot, []string{"routing", "strategy"}); accountYAMLScalarIsQuotaAware(comparisonStrategy) {
			return true
		}
	}
	return false
}

func accountYAMLScalarIsQuotaAware(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && strings.EqualFold(strings.TrimSpace(node.Value), "quota-aware")
}

func accountYAMLSequenceContainsProtectedKey(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.SequenceNode {
		return accountYAMLSubtreeContainsProtectedKey(node)
	}
	for _, child := range node.Content {
		if accountYAMLSequenceContainsProtectedKey(child) {
			return true
		}
	}
	return false
}

func accountYAMLSubtreeContainsProtectedKey(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key != nil && accountConfigKeyProtected(key.Value) {
				return true
			}
			if accountYAMLSubtreeContainsProtectedKey(node.Content[i+1]) {
				return true
			}
		}
		return false
	}
	for _, child := range node.Content {
		if accountYAMLSubtreeContainsProtectedKey(child) {
			return true
		}
	}
	return false
}

func accountYAMLLookupPath(root *yaml.Node, path []string) *yaml.Node {
	current := root
	for _, segment := range path {
		if current == nil || current.Kind != yaml.MappingNode {
			return nil
		}
		found := false
		for i := 0; i+1 < len(current.Content); i += 2 {
			key := current.Content[i]
			if key != nil && key.Value == segment {
				current = current.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return current
}

func accountYAMLSetPath(root *yaml.Node, path []string, value *yaml.Node) {
	if root == nil || len(path) == 0 {
		return
	}
	if root.Kind != yaml.MappingNode {
		root.Kind = yaml.MappingNode
		root.Content = nil
	}
	current := root
	for _, segment := range path[:len(path)-1] {
		var next *yaml.Node
		for i := 0; i+1 < len(current.Content); i += 2 {
			key := current.Content[i]
			if key != nil && key.Value == segment {
				next = current.Content[i+1]
				if next == nil || next.Kind != yaml.MappingNode {
					next = &yaml.Node{Kind: yaml.MappingNode}
					current.Content[i+1] = next
				}
				break
			}
		}
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode}
			current.Content = append(current.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: segment}, next)
		}
		current = next
	}
	leaf := path[len(path)-1]
	for i := 0; i+1 < len(current.Content); i += 2 {
		key := current.Content[i]
		if key != nil && key.Value == leaf {
			current.Content[i+1] = deepCopyYAMLNode(value)
			return
		}
	}
	current.Content = append(current.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: leaf}, deepCopyYAMLNode(value))
}

func accountYAMLNodesSemanticallyEqual(a, b *yaml.Node) bool {
	av, aerr := accountYAMLNodeToComparable(a)
	bv, berr := accountYAMLNodeToComparable(b)
	if aerr != nil || berr != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func accountYAMLNodeToComparable(node *yaml.Node) (any, error) {
	if node == nil {
		return nil, nil
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return nil, err
	}
	return canonicalizeAccountYAMLValue(value), nil
}

func canonicalizeAccountYAMLValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make([]any, 0, len(keys)*2)
		for _, key := range keys {
			out = append(out, key, canonicalizeAccountYAMLValue(typed[key]))
		}
		return out
	case map[any]any:
		keys := make([]string, 0, len(typed))
		values := make(map[string]any, len(typed))
		for key, child := range typed {
			text := fmt.Sprint(key)
			keys = append(keys, text)
			values[text] = child
		}
		sort.Strings(keys)
		out := make([]any, 0, len(keys)*2)
		for _, key := range keys {
			out = append(out, key, canonicalizeAccountYAMLValue(values[key]))
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, canonicalizeAccountYAMLValue(child))
		}
		return out
	default:
		return value
	}
}

func deepCopyYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	out := *node
	if len(node.Content) > 0 {
		out.Content = make([]*yaml.Node, 0, len(node.Content))
		for _, child := range node.Content {
			out.Content = append(out.Content, deepCopyYAMLNode(child))
		}
	}
	if node.Alias != nil {
		out.Alias = deepCopyYAMLNode(node.Alias)
	}
	return &out
}

func encodeAccountConfigYAML(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		_ = encoder.Close()
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
