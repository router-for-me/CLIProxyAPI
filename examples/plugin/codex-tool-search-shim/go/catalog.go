package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"unicode"
)

const (
	deferredToolsKey  = "codex_deferred_tools"
	namespaceToolType = "namespace"
)

type toolIdentity struct {
	Namespace string
	Name      string
}

type toolSource uint8

const (
	sourceDeclared toolSource = iota
	sourceDiscovered
)

type toolCatalog struct {
	identities      []toolIdentity
	declarations    map[toolIdentity]json.RawMessage
	discovered      map[toolIdentity]struct{}
	clientSearch    bool
	exact           map[string]toolIdentity
	normalized      map[string]toolIdentity
	local           map[string]toolIdentity
	namespaces      map[string]toolIdentity
	namespaceCounts map[string]int
	topLevel        map[string]struct{}
	finalizeOnce    sync.Once
}

type requestPolicy struct {
	bridge      bool
	prune       bool
	native      bool
	bridgeKnown bool
}

func newToolCatalog() *toolCatalog {
	return &toolCatalog{
		declarations:    make(map[toolIdentity]json.RawMessage),
		discovered:      make(map[toolIdentity]struct{}),
		exact:           make(map[string]toolIdentity),
		normalized:      make(map[string]toolIdentity),
		local:           make(map[string]toolIdentity),
		namespaces:      make(map[string]toolIdentity),
		namespaceCounts: make(map[string]int),
		topLevel:        make(map[string]struct{}),
	}
}

// isResponsesFormat reports whether a protocol identifier belongs to the OpenAI
// Responses / Codex family. That family is the only one that carries
// client-executed tool_search declarations, tool_search_call items and
// tool_search_output items.
func isResponsesFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai-response", "openai-responses", "responses", "response", "codex":
		return true
	}
	return configuredExtraSourceFormat(format)
}

// requestPolicyFor decides how one interception phase treats a request.
//
// Bridging requires both sides to disagree: the client must speak Responses
// (otherwise it has no tool_search protocol to translate) and the upstream must
// not. Anything else is left byte-for-byte untouched, so unrelated clients hit
// by this plugin cannot be rewritten.
func requestPolicyFor(kind, sourceFormat, toFormat string) requestPolicy {
	kind = strings.TrimSpace(kind)
	to := strings.ToLower(strings.TrimSpace(toFormat))

	// Before-auth has no selected upstream format. Do not rewrite until the
	// after-auth hook has identified a non-Responses upstream.
	if kind == "request_before" && to == "" {
		return requestPolicy{bridgeKnown: false}
	}
	if !isResponsesFormat(sourceFormat) {
		return requestPolicy{bridgeKnown: true}
	}
	if isResponsesFormat(toFormat) {
		return requestPolicy{native: true, bridgeKnown: true}
	}
	return requestPolicy{bridge: true, prune: true, bridgeKnown: true}
}

func extractToolCatalog(body []byte) *toolCatalog {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}
	// Deferred tool metadata is the only thing this catalog describes, so skip
	// the parse entirely for requests that cannot carry any.
	if !bytes.Contains(trimmed, []byte(toolSearchName)) &&
		!bytes.Contains(trimmed, []byte(namespaceToolType)) {
		return nil
	}
	value, okValue := decodeJSONValue(body)
	if !okValue {
		return nil
	}
	catalog := newToolCatalog()
	switch root := value.(type) {
	case map[string]any:
		catalog.collectToolArray(root["tools"], "", sourceDeclared)
		if input, ok := root["input"].([]any); ok {
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				switch stringField(item, "type") {
				case "additional_tools":
					catalog.collectToolArray(item["tools"], "", sourceDeclared)
				case "tool_search_output":
					catalog.collectToolArray(item["tools"], "", sourceDiscovered)
					catalog.markClientSearch(item)
				case "tool_search_call":
					catalog.markClientSearch(item)
				}
			}
		}
	case []any:
		catalog.collectToolArray(root, "", sourceDeclared)
	}
	catalog.finalize()
	if len(catalog.identities) == 0 && len(catalog.topLevel) == 0 {
		return nil
	}
	return catalog
}

// decodeJSONValue decodes JSON without converting numbers to float64, so large
// integers survive an unchanged round trip.
func decodeJSONValue(body []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, false
	}
	return value, true
}

// markClientSearch records that the request participates in the client-executed
// tool_search protocol. Items without an explicit execution field default to
// client, which several Responses-compatible clients rely on.
func (c *toolCatalog) markClientSearch(item map[string]any) {
	if !strings.EqualFold(strings.TrimSpace(stringField(item, "execution")), "server") {
		c.clientSearch = true
	}
}

func (c *toolCatalog) collectToolArray(value any, inheritedNamespace string, source toolSource) {
	tools, ok := value.([]any)
	if !ok {
		return
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		c.collectDeferredMetadata(tool)
		switch strings.TrimSpace(stringField(tool, "type")) {
		case namespaceToolType:
			namespace := strings.TrimSpace(stringField(tool, "name"))
			if inheritedNamespace != "" && namespace != "" {
				namespace = joinNamespace(inheritedNamespace, namespace)
			}
			c.collectToolArray(tool["tools"], namespace, source)
			continue
		case "tool_search":
			c.markClientSearch(tool)
			continue
		case "function", "custom", "":
			name := strings.TrimSpace(stringField(tool, "name"))
			if name == "" {
				continue
			}
			namespace := strings.TrimSpace(stringField(tool, "namespace"))
			if namespace == "" {
				namespace = inheritedNamespace
			}
			if namespace == "" {
				c.topLevel[name] = struct{}{}
			} else {
				c.addIdentity(namespace, name)
				if source == sourceDiscovered {
					c.addDiscoveredDeclaration(namespace, name, tool)
				}
			}
		}
	}
}

func (c *toolCatalog) addDiscoveredDeclaration(namespace, name string, tool map[string]any) {
	identity := toolIdentity{
		Namespace: strings.TrimSpace(namespace),
		Name:      strings.TrimSpace(name),
	}
	if identity.Namespace == "" || identity.Name == "" {
		return
	}
	raw, errMarshal := json.Marshal(tool)
	if errMarshal != nil {
		return
	}
	c.declarations[identity] = raw
	c.discovered[identity] = struct{}{}
}

func (c *toolCatalog) collectDeferredMetadata(tool map[string]any) {
	entries, ok := tool[deferredToolsKey].([]any)
	if !ok {
		return
	}
	namespace := strings.TrimSpace(stringField(tool, "namespace"))
	if namespace == "" {
		namespace = strings.TrimSpace(stringField(tool, "name"))
	}
	for _, rawEntry := range entries {
		switch entry := rawEntry.(type) {
		case string:
			if name := strings.TrimSpace(entry); name != "" && namespace != "" {
				c.addIdentity(namespace, name)
			}
		case map[string]any:
			entryNamespace := strings.TrimSpace(stringField(entry, "namespace"))
			if entryNamespace == "" {
				entryNamespace = namespace
			}
			if name := strings.TrimSpace(stringField(entry, "name")); name != "" && entryNamespace != "" {
				c.addIdentity(entryNamespace, name)
			}
		}
	}
}

func (c *toolCatalog) addIdentity(namespace, name string) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" {
		return
	}
	for _, existing := range c.identities {
		if existing.Namespace == namespace && existing.Name == name {
			return
		}
	}
	c.identities = append(c.identities, toolIdentity{Namespace: namespace, Name: name})
}

func (c *toolCatalog) finalize() {
	// A catalog is shared by every stream chunk of one request, so the index
	// build has to be safe to trigger from several goroutines at once.
	c.finalizeOnce.Do(func() {
		if c.exact == nil {
			c.exact = make(map[string]toolIdentity)
		}
		if c.normalized == nil {
			c.normalized = make(map[string]toolIdentity)
		}
		if c.local == nil {
			c.local = make(map[string]toolIdentity)
		}
		if c.namespaces == nil {
			c.namespaces = make(map[string]toolIdentity)
		}
		if c.namespaceCounts == nil {
			c.namespaceCounts = make(map[string]int)
		}
		localAmbiguous := make(map[string]bool)
		for _, identity := range c.identities {
			raw := rawQualifiedToolName(identity.Namespace, identity.Name)
			alias := capToolName(raw)
			c.exact[raw] = identity
			c.exact[alias] = identity
			c.normalized[normalizeToolName(raw)] = identity
			if previous, exists := c.local[identity.Name]; exists && previous != identity {
				localAmbiguous[identity.Name] = true
			} else {
				c.local[identity.Name] = identity
			}
			c.namespaceCounts[identity.Namespace]++
			c.namespaces[identity.Namespace] = identity
		}
		for name, ambiguous := range localAmbiguous {
			if ambiguous {
				delete(c.local, name)
			}
		}
		for namespace, count := range c.namespaceCounts {
			if count != 1 {
				delete(c.namespaces, namespace)
			}
		}
	})
}

func (c *toolCatalog) resolve(name string) (toolIdentity, bool) {
	if c == nil {
		return toolIdentity{}, false
	}
	c.finalize()
	name = strings.TrimSpace(name)
	if name == "" || name == toolSearchName {
		return toolIdentity{}, false
	}
	if _, exists := c.topLevel[name]; exists {
		return toolIdentity{}, false
	}
	if identity, exists := c.exact[name]; exists {
		return identity, true
	}
	if identity, exists := c.namespaces[name]; exists {
		return identity, true
	}
	if identity, exists := c.local[name]; exists {
		return identity, true
	}
	if identity, exists := c.normalized[normalizeToolName(name)]; exists {
		return identity, true
	}
	return toolIdentity{}, false
}

func restoreFunctionCallIdentity(item map[string]any, catalog *toolCatalog) bool {
	if stringField(item, "type") != "function_call" || catalog == nil {
		return false
	}
	if strings.TrimSpace(stringField(item, "namespace")) != "" {
		return false
	}
	name := strings.TrimSpace(stringField(item, "name"))
	identity, ok := catalog.resolve(name)
	if !ok {
		return false
	}
	item["name"] = identity.Name
	if identity.Namespace != "" {
		item["namespace"] = identity.Namespace
	}
	return true
}

// ensureTopLevelToolSearch advertises the ordinary tool_search function the
// upstream can call. It only runs when the request has something to search:
// either the client already speaks the tool_search protocol or it declares
// namespaced children that this rewrite is about to move into the index.
func ensureTopLevelToolSearch(root map[string]any, catalog *toolCatalog) bool {
	if catalog == nil {
		return false
	}
	if !catalog.clientSearch && len(catalog.identities) == 0 {
		return false
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if toolType := stringField(tool, "type"); toolType == "tool_search" {
			return false
		}
		if toolType := stringField(tool, "type"); toolType == "function" && stringField(tool, "name") == toolSearchName {
			return false
		}
	}
	root["tools"] = append(tools, map[string]any{
		"type":        "function",
		"name":        toolSearchName,
		"description": "Search deferred tools by keyword before calling one.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []any{"query"},
		},
	})
	return true
}

func hasNamespaceChildren(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if stringField(typed, "type") == "namespace" {
			if children, ok := typed["tools"].([]any); ok && len(children) > 0 {
				return true
			}
		}
		for _, child := range typed {
			if hasNamespaceChildren(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasNamespaceChildren(child) {
				return true
			}
		}
	}
	return false
}

func pruneDeferredNamespaceTools(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		if stringField(typed, "type") == namespaceToolType {
			if children, ok := typed["tools"].([]any); ok {
				entries := deferredNamespaceEntries(children, strings.TrimSpace(stringField(typed, "name")))
				if len(entries) > 0 {
					typed[deferredToolsKey] = entries
					delete(typed, "tools")
					changed = true
				}
			}
		}
		for key, child := range typed {
			if key == deferredToolsKey {
				continue
			}
			if pruneDeferredNamespaceTools(child) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if pruneDeferredNamespaceTools(child) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

// deferredNamespaceEntries flattens a namespace's children into name-only index
// entries. Nested namespaces join their parent prefix so every flattened tool
// stays addressable by the same qualified name the upstream emits.
func deferredNamespaceEntries(children []any, namespace string) []any {
	entries := make([]any, 0, len(children))
	for _, rawChild := range children {
		child, ok := rawChild.(map[string]any)
		if !ok {
			continue
		}
		if stringField(child, "type") == namespaceToolType {
			nested, okNested := child["tools"].([]any)
			if !okNested {
				continue
			}
			entries = append(entries, deferredNamespaceEntries(nested, joinNamespace(namespace, stringField(child, "name")))...)
			continue
		}
		switch childType := strings.TrimSpace(stringField(child, "type")); childType {
		case "function", "custom", "":
		default:
			continue
		}
		name := strings.TrimSpace(stringField(child, "name"))
		if name == "" {
			continue
		}
		entries = append(entries, map[string]any{
			"namespace": namespace,
			"name":      name,
		})
	}
	return entries
}

func pruneRequestDeclarations(root map[string]any) bool {
	changed := false
	if hasNamespaceChildren(root["tools"]) {
		if pruneDeferredNamespaceTools(root["tools"]) {
			changed = true
		}
	}
	if input, ok := root["input"].([]any); ok {
		for _, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok || stringField(item, "type") != "additional_tools" {
				continue
			}
			if hasNamespaceChildren(item["tools"]) && pruneDeferredNamespaceTools(item["tools"]) {
				changed = true
			}
		}
	}
	return changed
}

func applyDeferredToolPolicyWithCatalog(root map[string]any, catalog *toolCatalog) bool {
	if catalog == nil {
		return false
	}
	changed := ensureTopLevelToolSearch(root, catalog)
	if pruneRequestDeclarations(root) {
		changed = true
	}
	if injectDiscoveredTools(root, catalog) {
		changed = true
	}
	return changed
}

func injectDiscoveredTools(root map[string]any, catalog *toolCatalog) bool {
	if catalog == nil || len(catalog.discovered) == 0 {
		return false
	}
	tools, _ := root["tools"].([]any)
	existing := make(map[string]struct{}, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(stringField(tool, "type")) {
		case "function", "custom", "":
			if name := strings.TrimSpace(stringField(tool, "name")); name != "" {
				existing[name] = struct{}{}
			}
		}
	}
	changed := false
	for _, identity := range catalog.identities {
		raw, ok := catalog.declarations[identity]
		if !ok {
			continue
		}
		name := rawQualifiedToolName(identity.Namespace, identity.Name)
		if name == "" || name == toolSearchName {
			continue
		}
		if _, exists := existing[name]; exists {
			continue
		}
		var tool map[string]any
		if errUnmarshal := json.Unmarshal(raw, &tool); errUnmarshal != nil {
			continue
		}
		if strings.TrimSpace(stringField(tool, "type")) == "" {
			tool["type"] = "function"
		}
		tool["name"] = name
		delete(tool, "namespace")
		delete(tool, "defer_loading")
		tools = append(tools, tool)
		existing[name] = struct{}{}
		changed = true
	}
	if changed {
		root["tools"] = tools
	}
	return changed
}

func rawQualifiedToolName(namespace, name string) string {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" || strings.HasPrefix(name, "mcp__") || strings.HasPrefix(name, namespace) {
		return name
	}
	if strings.HasSuffix(namespace, "__") {
		return namespace + name
	}
	return namespace + "__" + name
}

func capToolName(name string) string {
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

func normalizeToolName(name string) string {
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

func joinNamespace(parent, child string) string {
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
