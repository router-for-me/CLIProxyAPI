package config

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding"
	textunicode "golang.org/x/text/encoding/unicode"
	"gopkg.in/yaml.v3"
)

// DecodeFirstConfigDocument validates the complete YAML stream before returning
// its first document. Runtime settings come only from the first document.
func DecodeFirstConfigDocument(data []byte) (*yaml.Node, error) {
	docs, err := decodeConfigStream(data)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, fmt.Errorf("empty config")
	}
	return docs[0], nil
}

// ReplaceFirstConfigDocument encodes the edited document and copies the rest
// of the original YAML stream byte-for-byte.
func ReplaceFirstConfigDocument(original []byte, doc *yaml.Node) ([]byte, error) {
	docs, err := decodeConfigStream(original)
	if err != nil {
		return nil, err
	}
	return encodeConfigDocument(doc, original, docs)
}

// Decode the entire stream, including alias and mapping validation in documents
// not used by the runtime. Keep them out of the encoder so their bytes survive.
func decodeConfigStream(data []byte) ([]*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			return docs, nil
		}
		if err == nil {
			err = validateConfigAnchors(&doc, make(map[string]*yaml.Node))
		}
		if err == nil {
			err = validateConfigYAMLShape(&doc)
		}
		if err != nil {
			return docs, fmt.Errorf("parse config document %d: %w", len(docs)+1, err)
		}
		docs = append(docs, &doc)
	}
}

// yaml.v3's stream decoder retains its anchor lookup between documents, but
// YAML aliases are scoped to a single document, with the most recent definition.
func validateConfigAnchors(node *yaml.Node, anchors map[string]*yaml.Node) error {
	if node.Anchor != "" {
		anchors[node.Anchor] = node
	}
	if node.Kind == yaml.AliasNode && anchors[node.Value] != node.Alias {
		return fmt.Errorf("alias on line %d has no matching anchor in this document", node.Line)
	}
	for _, child := range node.Content {
		if errChild := validateConfigAnchors(child, anchors); errChild != nil {
			return errChild
		}
	}
	return nil
}

func validateConfigYAMLShape(root *yaml.Node) error {
	if err := validateConfigAliasGraph(root); err != nil {
		return err
	}
	var shape any
	if !hasEncodedConfigKey(root) {
		return root.Decode(&shape)
	}
	// yaml.v3 compares raw key kind/value for duplicates. Resolve scalar aliases
	// and binary names in a private copy to compare the decoded keys instead.
	copy := deepCopyNode(root)
	var resolve func(*yaml.Node)
	resolve = func(node *yaml.Node) {
		for i, child := range node.Content {
			if node.Kind == yaml.MappingNode && i%2 == 0 && isEncodedConfigKey(child) {
				key := *derefConfigNode(child)
				key.Anchor = ""
				normalizeConfigKey(&key, child.Kind == yaml.AliasNode)
				// An anchor declared on a key can still be used by other aliases.
				node.Content[i] = &key
			}
			resolve(child)
		}
	}
	resolve(copy)
	return copy.Decode(&shape)
}

func hasEncodedConfigKey(node *yaml.Node) bool {
	for i, child := range node.Content {
		if node.Kind == yaml.MappingNode && i%2 == 0 && isEncodedConfigKey(child) {
			return true
		}
		if hasEncodedConfigKey(child) {
			return true
		}
	}
	return false
}

func isEncodedConfigKey(key *yaml.Node) bool {
	resolved := derefConfigNode(key)
	return resolved.Kind == yaml.ScalarNode && (key.Kind == yaml.AliasNode || resolved.ShortTag() == "!!binary")
}

func normalizeConfigKey(key *yaml.Node, aliased bool) {
	if key.Kind == yaml.ScalarNode && key.ShortTag() == "!!binary" {
		key.Value = configScalarString(key)
		key.Tag, key.Style = "!!str", yaml.DoubleQuotedStyle
	} else if aliased && isConfigMergeKey(key) {
		key.Tag, key.Style = "!!str", yaml.SingleQuotedStyle
	}
}

// The generic decoder can skip overridden merge operands. Inspect all graph
// edges independently, including in documents that are not used at runtime.
func validateConfigAliasGraph(root *yaml.Node) error {
	const (
		maxAliasDepth = 10000
		maxAliasNodes = 100000
		maxAliasBytes = 64 << 20
	)
	type cost struct {
		nodes, bytes, depth int
	}
	seen := make(map[*yaml.Node]cost)
	var addedNodes, addedBytes int
	var visit func(*yaml.Node, int) (cost, error)
	visit = func(node *yaml.Node, depth int) (cost, error) {
		if depth > maxAliasDepth {
			return cost{}, fmt.Errorf("config YAML exceeds maximum alias expansion depth")
		}
		if prior, ok := seen[node]; ok {
			if prior.nodes == 0 {
				return cost{}, fmt.Errorf("cyclic YAML alias at line %d", node.Line)
			}
			return prior, nil
		}
		seen[node] = cost{}
		result := cost{nodes: 1, bytes: len(node.Value) + len(node.Tag) + len(node.HeadComment) + len(node.LineComment) + len(node.FootComment), depth: 1}
		if node.Kind == yaml.AliasNode {
			if node.Alias == nil {
				return cost{}, fmt.Errorf("missing YAML alias target at line %d", node.Line)
			}
			target, err := visit(node.Alias, depth+1)
			if err != nil {
				return cost{}, err
			}
			addedNodes += target.nodes
			addedBytes += target.bytes
			if addedNodes > maxAliasNodes || addedBytes > maxAliasBytes {
				return cost{}, fmt.Errorf("config YAML exceeds maximum alias expansion size")
			}
			result.nodes += target.nodes
			result.bytes += target.bytes
			result.depth += target.depth
		}
		for _, child := range node.Content {
			childCost, err := visit(child, depth+1)
			if err != nil {
				return cost{}, err
			}
			result.nodes += childCost.nodes
			result.bytes += childCost.bytes
			result.depth = max(result.depth, childCost.depth+1)
		}
		if depth+result.depth-1 > maxAliasDepth {
			return cost{}, fmt.Errorf("config YAML exceeds maximum alias expansion depth")
		}
		if node.Kind == yaml.MappingNode {
			// Check physical keys here: decoding can skip values hidden by a merge override.
			type keyIdentity struct {
				kind  yaml.Kind
				value string
			}
			keys := make(map[keyIdentity]int, len(node.Content)/2)
			for i := 0; i+1 < len(node.Content); i += 2 {
				key := node.Content[i]
				id := keyIdentity{kind: key.Kind, value: key.Value}
				if resolved := derefConfigNode(key); resolved.Kind == yaml.ScalarNode {
					id = keyIdentity{kind: resolved.Kind, value: configScalarString(resolved)}
				}
				if previous, exists := keys[id]; exists {
					return cost{}, fmt.Errorf("mapping key on line %d already defined at line %d", key.Line, previous)
				}
				keys[id] = key.Line
				if !isConfigMergeKey(key) {
					continue
				}
				merged := derefConfigNode(node.Content[i+1])
				if merged.Kind == yaml.SequenceNode {
					for _, item := range merged.Content {
						if derefConfigNode(item).Kind != yaml.MappingNode {
							return cost{}, fmt.Errorf("invalid YAML merge value at line %d", item.Line)
						}
					}
				} else if merged.Kind != yaml.MappingNode {
					return cost{}, fmt.Errorf("invalid YAML merge value at line %d", merged.Line)
				}
			}
		}
		if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!binary" {
			var value string
			if err := node.Decode(&value); err != nil {
				return cost{}, err
			}
		}
		seen[node] = result
		return result, nil
	}
	_, err := visit(root, 1)
	return err
}

// documentSuffix finds the end marker of the first document, or the start
// marker of the second. YAML recognizes these only at column one followed by
// separation; a mapping's nested block scalar content cannot start there.
func documentSuffixUTF8(data []byte, docs []*yaml.Node) []byte {
	line := 1
	for rest := data; len(rest) != 0; line++ {
		marker := rest
		if line == 1 {
			marker = bytes.TrimPrefix(marker, []byte("\xef\xbb\xbf"))
		}
		if yamlDocumentMarker(marker, "...") || (len(docs) > 1 && line == docs[1].Line && yamlDocumentMarker(marker, "---")) {
			return rest
		}
		i := bytes.IndexAny(rest, "\r\n\u0085\u2028\u2029")
		if i < 0 {
			break
		}
		width := 1
		switch rest[i] {
		case '\r':
			if i+1 < len(rest) && rest[i+1] == '\n' {
				width = 2
			}
		case 0xc2:
			width = 2
		case 0xe2:
			width = 3
		}
		rest = rest[i+width:]
	}
	return nil
}

func yamlDocumentMarker(data []byte, marker string) bool {
	if !bytes.HasPrefix(data, []byte(marker)) {
		return false
	}
	rest := data[len(marker):]
	return len(rest) == 0 || bytes.IndexAny(rest, " \t\r\n\u0085\u2028\u2029") == 0
}

func encodeConfigDocument(doc *yaml.Node, original []byte, docs []*yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if errEncode := encoder.Encode(doc); errEncode != nil {
		_ = encoder.Close()
		return nil, errEncode
	}
	if errClose := encoder.Close(); errClose != nil {
		return nil, errClose
	}
	first := buf.Bytes()
	view := original
	encoded := configUTF16Encoding(original)
	if bytes.HasPrefix(original, []byte{0xef, 0xbb, 0xbf}) {
		first = append([]byte{0xef, 0xbb, 0xbf}, first...)
	}
	if encoded != nil {
		var errCodec error
		view, errCodec = encoded.NewDecoder().Bytes(original)
		if errCodec != nil {
			return nil, errCodec
		}
		first, errCodec = encoded.NewEncoder().Bytes(first)
		if errCodec != nil {
			return nil, errCodec
		}
	}
	suffix := documentSuffixUTF8(view, docs)
	if len(suffix) == 0 {
		if len(docs) > 1 {
			return nil, fmt.Errorf("cannot preserve subsequent config documents")
		}
		return first, nil
	}
	if encoded != nil {
		// Convert the offset, not the suffix: keep all its original bytes.
		prefix := view[:len(view)-len(suffix)]
		offset := 2 + 2*len(utf16.Encode([]rune(string(prefix))))
		suffix = original[offset:]
	}
	return append(first, suffix...), nil
}

func configUTF16Encoding(data []byte) encoding.Encoding {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		return textunicode.UTF16(textunicode.LittleEndian, textunicode.UseBOM)
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		return textunicode.UTF16(textunicode.BigEndian, textunicode.UseBOM)
	default:
		return nil
	}
}

func derefConfigNode(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func isConfigMergeKey(key *yaml.Node) bool {
	return key.Kind == yaml.ScalarNode && key.Value == "<<" && key.ShortTag() == "!!merge"
}

func configScalarString(node *yaml.Node) string {
	if node.ShortTag() != "!!binary" {
		return node.Value
	}
	var value string
	_ = node.Decode(&value) // Stream validation rejects invalid binary scalars.
	return value
}

func directConfigKey(mapping *yaml.Node, name string) int {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if matchesConfigKey(mapping.Content[i], name) {
			return i
		}
	}
	return -1
}

func matchesConfigKey(key *yaml.Node, name string) bool {
	resolved := derefConfigNode(key)
	return !isConfigMergeKey(key) && resolved != nil && resolved.Kind == yaml.ScalarNode && configScalarString(resolved) == name
}

// visitConfigEntries visits direct and merged definitions in effective precedence
// order. Callers can continue past the first match to inspect shadowed entries.
func visitConfigEntries(node *yaml.Node, name string, yield func(key, value *yaml.Node) bool) bool {
	node = derefConfigNode(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return true
	}
	if i := directConfigKey(node, name); i >= 0 && !yield(node.Content[i], node.Content[i+1]) {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if !isConfigMergeKey(node.Content[i]) {
			continue
		}
		merged := derefConfigNode(node.Content[i+1])
		if merged.Kind == yaml.SequenceNode {
			for _, child := range merged.Content {
				if !visitConfigEntries(child, name, yield) {
					return false
				}
			}
		} else if !visitConfigEntries(merged, name, yield) {
			return false
		}
	}
	return true
}

func effectiveConfigValue(root *yaml.Node, path ...string) *yaml.Node {
	for _, field := range path {
		var value *yaml.Node
		visitConfigEntries(root, field, func(_, found *yaml.Node) bool {
			value = derefConfigNode(found)
			return false
		})
		root = value
	}
	return root
}

func detachedConfigNode(node *yaml.Node) *yaml.Node {
	resolved := derefConfigNode(node)
	copy := *resolved
	copy.Anchor = ""
	if node != resolved {
		copy.HeadComment, copy.LineComment, copy.FootComment = node.HeadComment, node.LineComment, node.FootComment
	}
	copy.Content = make([]*yaml.Node, len(resolved.Content))
	for i, child := range resolved.Content {
		copy.Content[i] = detachedConfigNode(child)
		if resolved.Kind == yaml.MappingNode && i%2 == 0 && child.Kind == yaml.AliasNode && isConfigMergeKey(copy.Content[i]) {
			copy.Content[i].Tag = "!!str"
			copy.Content[i].Style = yaml.SingleQuotedStyle
		}
	}
	return &copy
}

// Keep aliases to altered definitions independent of an edited setting. Match
// target pointers, not names: an anchor may be redefined later in the document.
func protectConfigAliases(root *yaml.Node, target *yaml.Node, subtree bool) {
	targets := map[*yaml.Node]bool{target: true}
	var gather func(*yaml.Node)
	gather = func(node *yaml.Node) {
		for _, child := range node.Content {
			targets[child] = true
			gather(child)
		}
	}
	if subtree {
		gather(target)
	}
	var walk func(*yaml.Node)
	walk = func(node *yaml.Node) {
		for i, child := range node.Content {
			if child.Kind == yaml.AliasNode && targets[child.Alias] {
				clone := detachedConfigNode(child)
				if node.Kind == yaml.MappingNode && i%2 == 0 && isConfigMergeKey(clone) {
					clone.Tag, clone.Style = "!!str", yaml.SingleQuotedStyle
				}
				node.Content[i] = clone
			} else {
				walk(child)
			}
		}
	}
	walk(root)
}

func setConfigScalar(root *yaml.Node, path []string, value string) {
	node := root
	for _, key := range path[:len(path)-1] {
		idx := directConfigKey(node, key)
		if idx < 0 {
			child := effectiveConfigValue(node, key)
			if child != nil && child.Kind == yaml.MappingNode {
				child = detachedConfigNode(child)
			} else {
				child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
			node = child
			continue
		}
		child := node.Content[idx+1]
		switch {
		case child.Kind == yaml.MappingNode:
			if child.Anchor != "" {
				protectConfigAliases(root, child, false)
			}
		case child.Kind == yaml.AliasNode && child.Alias.Kind == yaml.MappingNode:
			// Override inside a private mapping; keep the original alias in place
			// as a merge source, including its binding to any redefined anchor.
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", LineComment: child.LineComment, Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"},
				{Kind: yaml.AliasNode, Value: child.Value, Alias: child.Alias},
			}}
			node.Content[idx+1] = child
		default:
			protectConfigAliases(root, child, true)
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", LineComment: child.LineComment}
			node.Content[idx+1] = child
		}
		node = child
	}
	key := path[len(path)-1]
	if idx := directConfigKey(node, key); idx >= 0 {
		child := node.Content[idx+1]
		protectConfigAliases(root, child, true)
		child.Kind, child.Tag, child.Value, child.Alias, child.Content = yaml.ScalarNode, "!!str", value, nil, nil
		child.Style &= yaml.SingleQuotedStyle | yaml.DoubleQuotedStyle | yaml.TaggedStyle
		return
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// Only remove physically present keys. A legacy value inside an inherited
// mapping can be overridden without changing the merge source's other users.
func removeConfigPath(root *yaml.Node, path string, allowEmpty bool) bool {
	parts := strings.Split(path, ".")
	current := root
	var parents []*yaml.Node
	for i, part := range parts {
		idx := directConfigKey(current, part)
		if idx < 0 {
			return false
		}
		child := current.Content[idx+1]
		if i == len(parts)-1 {
			if allowEmpty && child.ShortTag() != "!!null" && (child.Kind != yaml.MappingNode || len(child.Content) != 0) {
				return false
			}
			for _, parent := range append(parents, current) {
				if parent.Anchor != "" {
					protectConfigAliases(root, parent, false)
				}
			}
			protectConfigAliases(root, current.Content[idx], true)
			protectConfigAliases(root, child, true)
			current.Content = append(current.Content[:idx], current.Content[idx+2:]...)
			// Prune empty unanchored ancestors, as legacy normalization did.
			for j := len(parents) - 1; j >= 0 && len(current.Content) == 0 && current.Anchor == ""; j-- {
				current = parents[j]
				k := directConfigKey(current, parts[j])
				protectConfigAliases(root, current.Content[k], true)
				current.Content = append(current.Content[:k], current.Content[k+2:]...)
			}
			return true
		}
		parents = append(parents, current)
		current = child
	}
	return false
}
