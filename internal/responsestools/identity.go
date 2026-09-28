package responsestools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// RawQualifiedToolName flattens one namespaced identity to its dotted form.
// Names that already carry an mcp__ prefix or the namespace keep their shape;
// anything else joins with a double underscore separator.
func RawQualifiedToolName(namespace, name string) string {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" || strings.HasPrefix(name, "mcp__") || isNamespaceQualifiedName(namespace, name) {
		return name
	}
	if strings.HasSuffix(namespace, "__") {
		return namespace + name
	}
	return namespace + "__" + name
}

// isNamespaceQualifiedName reports whether name already carries namespace as
// its prefix. A shared prefix alone ("fs_read" under namespace "fs") is not
// qualification; only the exact namespace or its separator-delimited form
// counts, so the guard never requires a name no translator emits.
func isNamespaceQualifiedName(namespace, name string) bool {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return false
	}
	if name == namespace {
		return true
	}
	if strings.HasSuffix(namespace, "__") {
		return strings.HasPrefix(name, namespace)
	}
	return strings.HasPrefix(name, namespace+"__")
}

// CapToolName keeps a flattened name within the 64-character upstream limit
// by trimming from the left.
func CapToolName(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	truncated := name[len(name)-limit:]
	trimmed := strings.TrimLeft(truncated, "_-")
	if trimmed == "" {
		return truncated
	}
	return trimmed
}

// NormalizeToolName folds one name for fuzzy matching: lowercase with every
// non-alphanumeric run collapsed to a single dot.
func NormalizeToolName(name string) string {
	var builder strings.Builder
	lastSeparator := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			lastSeparator = false
			continue
		}
		if !lastSeparator {
			builder.WriteByte('.')
			lastSeparator = true
		}
	}
	return strings.Trim(builder.String(), ".")
}

// JoinNamespace joins nested namespace segments with the same separator used
// for flattened tool names.
func JoinNamespace(parent, child string) string {
	parent = strings.TrimSpace(parent)
	child = strings.TrimSpace(child)
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	if strings.HasSuffix(parent, "__") {
		return parent + child
	}
	return parent + "__" + child
}

// HashedToolAlias derives a stable, collision-resistant alias from the full
// identity triple. It is deterministic across rounds so multi-turn discovery
// stays stable, and it avoids depending on hash-collision improbability for
// human-chosen duplicate names: deliberate conflicts still get suffixed in
// buildActivationAliases.
func HashedToolAlias(identity ToolIdentity) string {
	encoded, _ := json.Marshal([3]string{identity.Namespace, identity.Name, string(identity.Kind)})
	sum := sha256.Sum256(encoded)
	return "cts_" + fmt.Sprintf("%x", sum[:24])
}

// IsValidFunctionName reports whether name fits the upstream function-name
// grammar used for alias selection.
func IsValidFunctionName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '_' && index > 0:
		default:
			return false
		}
	}
	return true
}

// IsPromotedToolName reports whether name looks like a discovery alias minted
// by this package, so replays can drop stale promotions before reinjecting
// the current activation set.
func IsPromotedToolName(name string) bool {
	const prefix = "cts_"
	const hashLength = 48
	if len(name) != len(prefix)+hashLength || !strings.HasPrefix(name, prefix) {
		return false
	}
	for _, char := range name[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func stringField(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}

// IsServerExecutedItem reports whether an item is owned by an upstream that
// implements tool search itself. Items that omit execution are treated as
// client-executed, which is what Responses-compatible clients emit.
func IsServerExecutedItem(item map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(stringField(item, "execution")), "server")
}

func itoa(value int) string {
	return "_" + strconv.Itoa(value)
}
