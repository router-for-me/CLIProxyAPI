package responsestools

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CustomFunctionParameter is the single string parameter that carries the
// original custom tool input across the function bridge.
const CustomFunctionParameter = "input"

// CustomAliasPrefix prefixes deterministic aliases minted for custom tools.
const CustomAliasPrefix = "ccb_"

// CustomAliasFor derives the stable wire alias for one custom identity. The
// digest covers namespace, name, and kind so a function and a custom tool
// that share a display name never share an alias.
func CustomAliasFor(namespace, name string) string {
	encoded, _ := json.Marshal([3]string{strings.TrimSpace(namespace), strings.TrimSpace(name), string(ToolKindCustom)})
	sum := sha256.Sum256(encoded)
	return CustomAliasPrefix + fmt.Sprintf("%x", sum[:24])
}

// CustomBridge maps custom identities to their function aliases for one
// attempt. It is built from the request contract (declarations plus the
// alias map) so request, history, response, and stream recovery all share one
// mapping. Unknown names never resolve.
type CustomBridge struct {
	aliasByID map[ToolIdentity]string
	idByAlias map[string]ToolIdentity
	byName    map[string]ToolIdentity
	ambiguous map[string]bool
	grammar   CustomGrammarMode
}

// BuildCustomBridge collects every custom declaration reachable from tools,
// nested namespaces, and additional_tools inputs.
func BuildCustomBridge(value any, grammar CustomGrammarMode) *CustomBridge {
	bridge := &CustomBridge{
		aliasByID: make(map[ToolIdentity]string),
		idByAlias: make(map[string]ToolIdentity),
		byName:    make(map[string]ToolIdentity),
		ambiguous: make(map[string]bool),
		grammar:   grammar,
	}
	collectCustomDeclarations(value, "", bridge)
	return bridge
}

func collectCustomDeclarations(value any, inheritedNamespace string, bridge *CustomBridge) {
	switch typed := value.(type) {
	case map[string]any:
		if tools, ok := typed["tools"].([]any); ok {
			namespace := inheritedNamespace
			if strings.TrimSpace(stringField(typed, "type")) == NamespaceToolType {
				name := strings.TrimSpace(stringField(typed, "name"))
				if namespace != "" && name != "" {
					namespace = JoinNamespace(namespace, name)
				} else if name != "" {
					namespace = name
				}
			}
			for _, rawTool := range tools {
				tool, ok := rawTool.(map[string]any)
				if !ok {
					continue
				}
				if strings.TrimSpace(stringField(tool, "type")) == NamespaceToolType {
					collectCustomDeclarations(tool, namespace, bridge)
					continue
				}
				if !strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), "custom") {
					continue
				}
				name := strings.TrimSpace(stringField(tool, "name"))
				if name == "" {
					continue
				}
				toolNamespace := strings.TrimSpace(stringField(tool, "namespace"))
				if toolNamespace == "" {
					toolNamespace = namespace
				}
				bridge.register(ToolIdentity{Namespace: toolNamespace, Name: name, Kind: ToolKindCustom})
			}
		}
		if input, ok := typed["input"].([]any); ok {
			for _, rawItem := range input {
				item, ok := rawItem.(map[string]any)
				if !ok || stringField(item, "type") != "additional_tools" {
					continue
				}
				collectCustomDeclarations(item, inheritedNamespace, bridge)
			}
		}
	case []any:
		for _, child := range typed {
			collectCustomDeclarations(child, inheritedNamespace, bridge)
		}
	}
}

func (b *CustomBridge) register(identity ToolIdentity) {
	if b == nil || identity.Name == "" {
		return
	}
	if _, exists := b.aliasByID[identity]; exists {
		return
	}
	alias := CustomAliasFor(identity.Namespace, identity.Name)
	base := alias
	for suffix := 1; ; suffix++ {
		owner, exists := b.idByAlias[alias]
		if !exists {
			break
		}
		if owner == identity {
			_ = owner
			b.aliasByID[identity] = alias
			return
		}
		// Deterministic disambiguation for human-chosen collisions; never rely
		// on digest improbability to ignore an adversarial duplicate.
		alias = fmt.Sprintf("%s_%d", base, suffix)
		if len(alias) > 64 {
			alias = base[:64-len(fmt.Sprintf("_%d", suffix))] + fmt.Sprintf("_%d", suffix)
		}
	}
	b.aliasByID[identity] = alias
	b.idByAlias[alias] = identity
	for _, key := range []string{identity.Name, RawQualifiedToolName(identity.Namespace, identity.Name)} {
		if key == "" {
			continue
		}
		if previous, exists := b.byName[key]; exists && previous != identity {
			b.ambiguous[key] = true
			delete(b.byName, key)
			continue
		}
		if !b.ambiguous[key] {
			b.byName[key] = identity
		}
	}
}

// Alias returns the wire alias for one custom identity.
func (b *CustomBridge) Alias(identity ToolIdentity) (string, bool) {
	if b == nil {
		return "", false
	}
	alias, ok := b.aliasByID[identity]
	return alias, ok
}

// Resolve maps one wire alias or display name back to its custom identity.
// Ambiguous display names never resolve; callers must fail rather than guess.
func (b *CustomBridge) Resolve(name string) (ToolIdentity, bool) {
	if b == nil {
		return ToolIdentity{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolIdentity{}, false
	}
	if identity, ok := b.idByAlias[name]; ok {
		return identity, true
	}
	if b.ambiguous[name] {
		return ToolIdentity{}, false
	}
	identity, ok := b.byName[name]
	return identity, ok
}

// RewriteCustomDeclarations wraps every custom declaration in value as a
// single-string function tool and returns the bridge for history, choice,
// response, and stream recovery. Grammar payloads are described, never
// silently dropped, when grammar mode is describe.
func RewriteCustomDeclarations(value any, grammar CustomGrammarMode) (*CustomBridge, bool, error) {
	bridge := BuildCustomBridge(value, grammar)
	changed, err := rewriteCustomDeclarationsValue(value, bridge)
	if err != nil {
		return nil, false, err
	}
	return bridge, changed, nil
}

func rewriteCustomDeclarationsValue(value any, bridge *CustomBridge) (bool, error) {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		if tools, ok := typed["tools"].([]any); ok {
			wrapped, toolChanged, err := wrapCustomToolArray(tools, bridge)
			if err != nil {
				return false, err
			}
			if toolChanged {
				typed["tools"] = wrapped
				changed = true
			}
		}
		if input, ok := typed["input"].([]any); ok {
			for _, rawItem := range input {
				item, ok := rawItem.(map[string]any)
				if !ok {
					continue
				}
				switch stringField(item, "type") {
				case "additional_tools":
					if tools, okTools := item["tools"].([]any); okTools {
						wrapped, toolChanged, err := wrapCustomToolArray(tools, bridge)
						if err != nil {
							return false, err
						}
						if toolChanged {
							item["tools"] = wrapped
							changed = true
						}
					}
				case "custom_tool_call", "custom_tool_call_output":
					if itemChanged, err := rewriteCustomHistoryItem(item, bridge); err != nil {
						return false, err
					} else if itemChanged {
						changed = true
					}
				}
			}
		}
		if choice, ok := typed["tool_choice"].(map[string]any); ok {
			if choiceChanged, err := rewriteCustomChoice(choice, bridge); err != nil {
				return false, err
			} else if choiceChanged {
				changed = true
			}
		}
	case []any:
		for _, child := range typed {
			if childChanged, err := rewriteCustomDeclarationsValue(child, bridge); err != nil {
				return false, err
			} else if childChanged {
				changed = true
			}
		}
	}
	return changed, nil
}

func wrapCustomToolArray(tools []any, bridge *CustomBridge) ([]any, bool, error) {
	return wrapCustomToolArrayInNamespace(tools, "", bridge)
}

func wrapCustomToolArrayInNamespace(tools []any, namespace string, bridge *CustomBridge) ([]any, bool, error) {
	changed := false
	for index, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringField(tool, "type")) == NamespaceToolType {
			childNamespace := JoinNamespace(namespace, stringField(tool, "name"))
			if children, okChildren := tool["tools"].([]any); okChildren {
				wrapped, childChanged, err := wrapCustomToolArrayInNamespace(children, childNamespace, bridge)
				if err != nil {
					return tools, false, err
				}
				if childChanged {
					if len(wrapped) == 0 {
						delete(tool, "tools")
					} else {
						tool["tools"] = wrapped
					}
					changed = true
				}
			}
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), "custom") {
			continue
		}
		wrapped, err := wrapCustomDeclarationInNamespace(tool, namespace, bridge)
		if err != nil {
			return tools, false, err
		}
		tools[index] = wrapped
		changed = true
	}
	return tools, changed, nil
}

func wrapCustomDeclaration(tool map[string]any, bridge *CustomBridge) (map[string]any, error) {
	return wrapCustomDeclarationInNamespace(tool, "", bridge)
}

func wrapCustomDeclarationInNamespace(tool map[string]any, inheritedNamespace string, bridge *CustomBridge) (map[string]any, error) {
	name := strings.TrimSpace(stringField(tool, "name"))
	namespace := strings.TrimSpace(stringField(tool, "namespace"))
	if namespace == "" {
		namespace = strings.TrimSpace(inheritedNamespace)
	}
	identity := ToolIdentity{Namespace: namespace, Name: name, Kind: ToolKindCustom}
	alias, ok := bridge.Alias(identity)
	if !ok {
		return nil, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("custom tool %q has no bridge alias", name))
	}
	description := strings.TrimSpace(stringField(tool, "description"))
	format, _ := tool["format"].(map[string]any)
	if len(format) > 0 && bridge.grammar == CustomGrammarReject {
		return nil, unprocessableError(ReasonUnsupportedGrammar, fmt.Errorf("custom tool %q requires grammar support", name))
	}
	if len(format) > 0 {
		described := describeCustomFormat(format)
		if description != "" {
			description += "\n\n"
		}
		description += described
	}
	if description == "" {
		description = fmt.Sprintf("Call the %s custom tool with its exact input string.", name)
	} else {
		description += fmt.Sprintf(" Pass the exact custom tool input as the %q string.", CustomFunctionParameter)
	}
	wrapped := map[string]any{
		"type":        "function",
		"name":        alias,
		"description": description,
		"strict":      true,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				CustomFunctionParameter: map[string]any{"type": "string"},
			},
			"required":             []any{CustomFunctionParameter},
			"additionalProperties": false,
		},
	}
	return wrapped, nil
}

func describeCustomFormat(format map[string]any) string {
	keys := make([]string, 0, len(format))
	for key := range format {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("Original custom tool format (descriptive only, constraints not enforced upstream):")
	for _, key := range keys {
		encoded, err := json.Marshal(format[key])
		if err != nil {
			continue
		}
		builder.WriteString(fmt.Sprintf("\n%s: %s", key, string(encoded)))
	}
	return builder.String()
}

func rewriteCustomChoice(choice map[string]any, bridge *CustomBridge) (bool, error) {
	changed := false
	if strings.EqualFold(strings.TrimSpace(stringField(choice, "type")), "custom") {
		name := strings.TrimSpace(stringField(choice, "name"))
		identity, ok := bridge.Resolve(name)
		if !ok {
			return false, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("custom choice %q is ambiguous or unknown", name))
		}
		alias, _ := bridge.Alias(identity)
		choice["type"] = "function"
		choice["name"] = alias
		changed = true
	}
	if tools, ok := choice["tools"].([]any); ok {
		for _, rawTool := range tools {
			tool, okTool := rawTool.(map[string]any)
			if !okTool {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), "custom") {
				name := strings.TrimSpace(stringField(tool, "name"))
				if name == "" {
					continue
				}
				if identity, okResolve := bridge.Resolve(name); okResolve {
					alias, _ := bridge.Alias(identity)
					tool["type"] = "function"
					tool["name"] = alias
					changed = true
				}
				continue
			}
			name := strings.TrimSpace(stringField(tool, "name"))
			identity, okResolve := bridge.Resolve(name)
			if !okResolve {
				return false, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("custom choice entry %q is ambiguous or unknown", name))
			}
			alias, _ := bridge.Alias(identity)
			tool["type"] = "function"
			tool["name"] = alias
			changed = true
		}
	}
	if auto := strings.TrimSpace(stringField(choice, "type")); auto == "auto" || auto == "none" || auto == "required" {
		return changed, nil
	}
	return changed, nil
}

// rewriteCustomHistoryItem converts one custom history item to its function
// equivalent: custom_tool_call becomes a function_call whose arguments carry
// exactly {"input": original}, and custom_tool_call_output becomes a
// function_call_output with the payload preserved.
func rewriteCustomHistoryItem(item map[string]any, bridge *CustomBridge) (bool, error) {
	switch stringField(item, "type") {
	case "custom_tool_call":
		toolInput, ok := item["input"].(string)
		if !ok {
			return false, unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom history input is not a string"))
		}
		name := strings.TrimSpace(stringField(item, "name"))
		namespace := strings.TrimSpace(stringField(item, "namespace"))
		identity, okResolve := bridge.Resolve(name)
		if !okResolve && namespace != "" {
			identity, okResolve = bridge.Resolve(RawQualifiedToolName(namespace, name))
		}
		if !okResolve {
			// History may reference a custom tool declared only in a previous
			// turn; register it so the round trip stays stable.
			identity = ToolIdentity{Namespace: namespace, Name: name, Kind: ToolKindCustom}
			bridge.register(identity)
		}
		alias, _ := bridge.Alias(identity)
		arguments, err := json.Marshal(map[string]string{CustomFunctionParameter: toolInput})
		if err != nil {
			return false, unprocessableError(ReasonInvalidCustomInput, err)
		}
		item["type"] = "function_call"
		item["name"] = alias
		item["arguments"] = string(arguments)
		delete(item, "input")
		delete(item, "namespace")
		return true, nil
	case "custom_tool_call_output":
		item["type"] = "function_call_output"
		return true, nil
	default:
		return false, nil
	}
}

// UnpackCustomArguments strictly decodes one bridged function arguments
// string back to the original custom input. The payload must be exactly one
// JSON object with a single string "input" field: duplicate keys, extra
// fields, trailing values, and non-string inputs are rejected so a malformed
// model output can never silently become a different custom input.
func UnpackCustomArguments(arguments string) (string, error) {
	trimmed := bytes.TrimSpace([]byte(arguments))
	if len(trimmed) == 0 {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("empty custom arguments"))
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return "", unprocessableError(ReasonInvalidCustomInput, err)
	}
	if decoder.More() {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("trailing data after custom arguments"))
	}
	if len(value) != 1 {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom arguments must carry exactly one field"))
	}
	raw, ok := value[CustomFunctionParameter]
	if !ok {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom arguments miss input field"))
	}
	input, ok := raw.(string)
	if !ok {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom input is not a string"))
	}
	return input, nil
}

// RestoreCustomResponseItem converts one upstream function item back to its
// custom identity when the alias belongs to this bridge.
func (b *CustomBridge) RestoreCustomResponseItem(item map[string]any) bool {
	if b == nil || stringField(item, "type") != "function_call" {
		return false
	}
	identity, ok := b.Resolve(stringField(item, "name"))
	if !ok || identity.Kind != ToolKindCustom {
		return false
	}
	arguments, okArgs := item["arguments"].(string)
	if !okArgs {
		return false
	}
	input, err := UnpackCustomArguments(arguments)
	if err != nil {
		return false
	}
	item["type"] = "custom_tool_call"
	item["name"] = identity.Name
	if identity.Namespace != "" {
		item["namespace"] = identity.Namespace
	} else {
		delete(item, "namespace")
	}
	item["input"] = input
	delete(item, "arguments")
	return true
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

// Aliases returns every wire alias minted by this bridge in stable order.
func (b *CustomBridge) Aliases() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.aliasByID))
	for _, alias := range b.aliasByID {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}
