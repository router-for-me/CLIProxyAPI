package responsestools

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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
	reserved  map[string]struct{}
	grammar   CustomGrammarMode
}

// BuildCustomBridge collects every custom declaration reachable from tools,
// nested namespaces, and additional_tools inputs.
func BuildCustomBridge(value any, grammar CustomGrammarMode) *CustomBridge {
	return BuildCustomBridgeWithReserved(value, grammar, nil)
}

// BuildCustomBridgeWithReserved collects custom identities before request
// rewriting and avoids every name already assigned to a non-custom function.
func BuildCustomBridgeWithReserved(value any, grammar CustomGrammarMode, reserved []string) *CustomBridge {
	bridge := &CustomBridge{
		aliasByID: make(map[ToolIdentity]string),
		idByAlias: make(map[string]ToolIdentity),
		byName:    make(map[string]ToolIdentity),
		ambiguous: make(map[string]bool),
		reserved:  make(map[string]struct{}, len(reserved)),
		grammar:   grammar,
	}
	for _, name := range reserved {
		if name = strings.TrimSpace(name); name != "" {
			bridge.reserved[name] = struct{}{}
		}
	}
	collectReservedFunctionNames(value, bridge.reserved)
	collectCustomDeclarations(value, "", bridge)
	return bridge
}

func collectReservedFunctionNames(value any, reserved map[string]struct{}) {
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if tools, ok := typed["tools"].([]any); ok {
				for _, rawTool := range tools {
					tool, okTool := rawTool.(map[string]any)
					if !okTool {
						continue
					}
					if stringField(tool, "type") == "function" {
						if name := strings.TrimSpace(stringField(tool, "name")); name != "" {
							reserved[name] = struct{}{}
						}
					}
					visit(tool["tools"])
				}
			}
			if input, ok := typed["input"].([]any); ok {
				for _, rawItem := range input {
					item, okItem := rawItem.(map[string]any)
					if okItem && IsToolDeclarationInput(item) {
						visit(item)
					}
				}
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
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
				if !ok || !IsToolDeclarationInput(item) {
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
		_, reserved := b.reserved[alias]
		if !exists && !reserved {
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

// IsAmbiguous reports whether a display or qualified name resolves to multiple
// custom identities and therefore cannot be safely registered as history-only.
func (b *CustomBridge) IsAmbiguous(name string) bool {
	if b == nil {
		return false
	}
	return b.ambiguous[strings.TrimSpace(name)]
}

// ResolveWireAlias resolves only generated wire aliases. Display names are
// useful while translating client history, but are not unique enough to
// classify an upstream function_call without risking an ordinary function
// being mistaken for a custom tool.
func (b *CustomBridge) ResolveWireAlias(name string) (ToolIdentity, bool) {
	if b == nil {
		return ToolIdentity{}, false
	}
	identity, ok := b.idByAlias[strings.TrimSpace(name)]
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
				case "additional_tools", "tool_search_output":
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
	if len(format) > 0 {
		switch stringField(format, "type") {
		case "text":
			if len(format) != 1 {
				return nil, unprocessableError(ReasonUnsupportedGrammar, fmt.Errorf("custom tool %q has unsupported text format fields", name))
			}
		case "grammar":
			if bridge.grammar == CustomGrammarReject {
				return nil, unprocessableError(ReasonUnsupportedGrammar, fmt.Errorf("custom tool %q requires grammar support", name))
			}
			described := describeCustomFormat(format)
			if description != "" {
				description += "\n\n"
			}
			description += described
		default:
			return nil, unprocessableError(ReasonUnsupportedGrammar, fmt.Errorf("custom tool %q has an unsupported format", name))
		}
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
	if deferred, exists := tool["defer_loading"]; exists {
		wrapped["defer_loading"] = deferred
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
		namespace := strings.TrimSpace(stringField(choice, "namespace"))
		identity, ok := resolveCustomIdentity(bridge, namespace, name)
		if !ok {
			return false, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("custom choice %q is ambiguous or unknown", name))
		}
		alias, _ := bridge.Alias(identity)
		choice["type"] = "function"
		choice["name"] = alias
		delete(choice, "namespace")
		changed = true
	}
	if tools, ok := choice["tools"].([]any); ok {
		for _, rawTool := range tools {
			tool, okTool := rawTool.(map[string]any)
			if !okTool {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(stringField(tool, "type")), "custom") {
				// Function entries are already explicit ordinary function
				// identities. Never infer that one is custom from a matching
				// display name; custom entries must carry type=custom.
				continue
			}
			name := strings.TrimSpace(stringField(tool, "name"))
			namespace := strings.TrimSpace(stringField(tool, "namespace"))
			identity, okResolve := resolveCustomIdentity(bridge, namespace, name)
			if !okResolve {
				return false, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("custom choice entry %q is ambiguous or unknown", name))
			}
			alias, _ := bridge.Alias(identity)
			tool["type"] = "function"
			tool["name"] = alias
			delete(tool, "namespace")
			changed = true
		}
	}
	if auto := strings.TrimSpace(stringField(choice, "type")); auto == "auto" || auto == "none" || auto == "required" {
		return changed, nil
	}
	return changed, nil
}

func resolveCustomIdentity(bridge *CustomBridge, namespace, name string) (ToolIdentity, bool) {
	name = strings.TrimSpace(name)
	namespace = strings.TrimSpace(namespace)
	if bridge == nil || name == "" {
		return ToolIdentity{}, false
	}
	if namespace != "" {
		identity := ToolIdentity{Namespace: namespace, Name: name, Kind: ToolKindCustom}
		_, ok := bridge.Alias(identity)
		return identity, ok
	}
	identity, ok := bridge.Resolve(name)
	return identity, ok && identity.Kind == ToolKindCustom
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
		var identity ToolIdentity
		var okResolve bool
		if namespace != "" {
			identity, okResolve = resolveCustomIdentity(bridge, namespace, name)
		} else {
			identity, okResolve = resolveCustomIdentity(bridge, "", name)
		}
		if !okResolve {
			if bridge.IsAmbiguous(name) {
				return false, unprocessableError(ReasonAmbiguousIdentity, fmt.Errorf("custom history name %q is ambiguous without a namespace", name))
			}
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
		if _, err := ReidentifyItem(item, "function_call"); err != nil {
			return false, unprocessableError(ReasonHistoryLink, err)
		}
		item["name"] = alias
		item["arguments"] = string(arguments)
		delete(item, "input")
		delete(item, "namespace")
		return true, nil
	case "custom_tool_call_output":
		if _, err := ReidentifyItem(item, "function_call_output"); err != nil {
			return false, unprocessableError(ReasonHistoryLink, err)
		}
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
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return "", unprocessableError(ReasonInvalidCustomInput, err)
	}
	count := 0
	seen := make(map[string]struct{}, 1)
	var input string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", unprocessableError(ReasonInvalidCustomInput, err)
		}
		key, ok := token.(string)
		if !ok {
			return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom arguments contain a non-string key"))
		}
		if _, duplicate := seen[key]; duplicate {
			return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom arguments contain duplicate fields"))
		}
		seen[key] = struct{}{}
		count++
		var value any
		if err := decoder.Decode(&value); err != nil {
			return "", unprocessableError(ReasonInvalidCustomInput, err)
		}
		if key == CustomFunctionParameter {
			var okInput bool
			input, okInput = value.(string)
			if !okInput {
				return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom input is not a string"))
			}
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom arguments object is incomplete"))
	}
	if count != 1 {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom arguments must carry exactly one field"))
	}
	if _, ok := seen[CustomFunctionParameter]; !ok {
		return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("custom arguments miss input field"))
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", unprocessableError(ReasonInvalidCustomInput, fmt.Errorf("trailing value after custom arguments"))
		}
		return "", unprocessableError(ReasonInvalidCustomInput, err)
	}
	return input, nil
}

// RestoreCustomResponseItem converts one upstream function item back to its
// custom identity when the alias belongs to this bridge.
func (b *CustomBridge) RestoreCustomResponseItem(item map[string]any) bool {
	changed, _ := b.RestoreCustomResponseItemChecked(item)
	return changed
}

// RestoreCustomResponseItemChecked is the production form: it propagates a
// malformed custom payload or an unmigratable id instead of leaving the caller
// with a half-rewritten item. On error nothing on the item has changed.
func (b *CustomBridge) RestoreCustomResponseItemChecked(item map[string]any) (bool, error) {
	if b == nil || stringField(item, "type") != "function_call" {
		return false, nil
	}
	identity, ok := b.ResolveWireAlias(stringField(item, "name"))
	if !ok || identity.Kind != ToolKindCustom {
		return false, nil
	}
	return b.restoreCustomResponseItemChecked(item, identity)
}

// restoreCustomResponseItemChecked performs the conversion for an alias this
// bridge already resolved. Every failure is an upstream contract violation: the
// upstream answered a bridged alias with a payload the client cannot execute.
func (b *CustomBridge) restoreCustomResponseItemChecked(item map[string]any, identity ToolIdentity) (bool, error) {
	arguments, okArgs := item["arguments"].(string)
	if !okArgs {
		return false, upstreamError(ReasonUpstreamContract, fmt.Errorf("custom function_call arguments are not a string"))
	}
	input, err := UnpackCustomArguments(arguments)
	if err != nil {
		return false, upstreamError(ReasonUpstreamContract, fmt.Errorf("invalid custom function_call arguments: %w", err))
	}
	if err := requireBridgedItemID(item); err != nil {
		return false, err
	}
	if _, err := ReidentifyItem(item, "custom_tool_call"); err != nil {
		return false, upstreamError(ReasonUpstreamContract, err)
	}
	item["name"] = identity.Name
	if identity.Namespace != "" {
		item["namespace"] = identity.Namespace
	} else {
		delete(item, "namespace")
	}
	item["input"] = input
	delete(item, "arguments")
	return true, nil
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
