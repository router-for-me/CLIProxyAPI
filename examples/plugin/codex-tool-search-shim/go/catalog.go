package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
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
	Kind      string
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
	serverSearch    bool
	searchBridge    bool
	searchAlias     string
	exact           map[string]toolIdentity
	normalized      map[string]toolIdentity
	local           map[string]toolIdentity
	namespaces      map[string]toolIdentity
	namespaceCounts map[string]int
	topLevel        map[string]struct{}
	deferred        map[toolIdentity]bool
	aliasByID       map[toolIdentity]string
	idByAlias       map[string]toolIdentity
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
		deferred:        make(map[toolIdentity]bool),
		aliasByID:       make(map[toolIdentity]string),
		idByAlias:       make(map[string]toolIdentity),
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
	return requestPolicyForModels(kind, sourceFormat, toFormat, "", "")
}

func requestPolicyForModels(kind, sourceFormat, toFormat string, models ...string) requestPolicy {
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
		if bridgeNativeModelMatches(models...) {
			return requestPolicy{bridge: true, prune: true, native: true, bridgeKnown: true}
		}
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
		!bytes.Contains(trimmed, []byte(namespaceToolType)) &&
		!bytes.Contains(trimmed, []byte("defer_loading")) {
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
	if len(catalog.identities) == 0 && len(catalog.topLevel) == 0 && !catalog.clientSearch && !catalog.serverSearch {
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
			if isServerExecutedItem(tool) {
				c.serverSearch = true
			} else {
				c.markClientSearch(tool)
			}
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
				identity := toolIdentity{Namespace: "", Name: name, Kind: normalizeToolKind(stringField(tool, "type"))}
				if source == sourceDiscovered {
					c.addIdentityWithKind(namespace, name, identity.Kind)
					c.addDiscoveredDeclaration(namespace, name, tool)
				} else {
					c.topLevel[name] = struct{}{}
					c.addIdentityWithKind(namespace, name, identity.Kind)
					c.deferred[identity] = deferredLoading(tool)
				}
			} else {
				kind := normalizeToolKind(stringField(tool, "type"))
				identity := toolIdentity{Namespace: namespace, Name: name, Kind: kind}
				c.addIdentityWithKind(namespace, name, kind)
				if source == sourceDeclared {
					c.deferred[identity] = deferredLoading(tool)
				}
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
		Kind:      normalizeToolKind(stringField(tool, "type")),
	}
	if identity.Name == "" {
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
				kind := "function"
				identity := toolIdentity{Namespace: namespace, Name: name, Kind: kind}
				c.addIdentityWithKind(namespace, name, kind)
				c.deferred[identity] = true
			}
		case map[string]any:
			entryNamespace := strings.TrimSpace(stringField(entry, "namespace"))
			if entryNamespace == "" {
				entryNamespace = namespace
			}
			if name := strings.TrimSpace(stringField(entry, "name")); name != "" && entryNamespace != "" {
				kind := normalizeToolKind(stringField(entry, "type"))
				identity := toolIdentity{Namespace: entryNamespace, Name: name, Kind: kind}
				c.addIdentityWithKind(entryNamespace, name, kind)
				c.deferred[identity] = true
			}
		}
	}
}

func (c *toolCatalog) addIdentity(namespace, name string) {
	c.addIdentityWithKind(namespace, name, "function")
}

func (c *toolCatalog) addIdentityWithKind(namespace, name, kind string) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	kind = normalizeToolKind(kind)
	if name == "" {
		return
	}
	for _, existing := range c.identities {
		if existing.Namespace == namespace && existing.Name == name && existing.Kind == kind {
			return
		}
	}
	c.identities = append(c.identities, toolIdentity{Namespace: namespace, Name: name, Kind: kind})
}

func normalizeToolKind(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "custom") {
		return "custom"
	}
	return "function"
}

func deferredLoading(tool map[string]any) bool {
	deferred, _ := tool["defer_loading"].(bool)
	return deferred
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
		exactAmbiguous := make(map[string]bool)
		normalizedAmbiguous := make(map[string]bool)
		localAmbiguous := make(map[string]bool)
		registerExact := func(name string, identity toolIdentity) {
			name = strings.TrimSpace(name)
			if name == "" {
				return
			}
			if previous, exists := c.exact[name]; exists && previous != identity {
				exactAmbiguous[name] = true
				return
			}
			c.exact[name] = identity
		}
		registerNormalized := func(name string, identity toolIdentity) {
			name = normalizeToolName(name)
			if name == "" {
				return
			}
			if previous, exists := c.normalized[name]; exists && previous != identity {
				normalizedAmbiguous[name] = true
				return
			}
			c.normalized[name] = identity
		}
		for _, identity := range c.identities {
			raw := rawQualifiedToolName(identity.Namespace, identity.Name)
			alias := capToolName(raw)
			registerExact(raw, identity)
			registerExact(alias, identity)
			registerNormalized(raw, identity)
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
		for name := range exactAmbiguous {
			delete(c.exact, name)
		}
		for name := range normalizedAmbiguous {
			delete(c.normalized, name)
		}
		for namespace, count := range c.namespaceCounts {
			if count != 1 {
				delete(c.namespaces, namespace)
			}
		}
		_, ordinarySearchExists := c.topLevel[toolSearchName]
		if c.clientSearch && ordinarySearchExists {
			c.searchAlias = "cts_" + toolSearchName
		} else {
			c.searchAlias = toolSearchName
		}
		c.buildActivationAliases()
	})
}

func (c *toolCatalog) buildActivationAliases() {
	if len(c.declarations) == 0 {
		return
	}
	identities := make([]toolIdentity, 0, len(c.declarations))
	for identity := range c.declarations {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(left, right int) bool {
		if identities[left].Namespace != identities[right].Namespace {
			return identities[left].Namespace < identities[right].Namespace
		}
		if identities[left].Name != identities[right].Name {
			return identities[left].Name < identities[right].Name
		}
		return identities[left].Kind < identities[right].Kind
	})

	used := make(map[string]toolIdentity, len(identities)+len(c.topLevel))
	for name := range c.topLevel {
		used[name] = toolIdentity{}
	}
	if c.searchAlias != toolSearchName {
		used[c.searchAlias] = toolIdentity{}
	}
	for _, identity := range identities {
		raw := rawQualifiedToolName(identity.Namespace, identity.Name)
		candidate := raw
		if identity.Namespace != "" || !isValidFunctionName(candidate) {
			candidate = hashedToolAlias(identity)
		}
		alias := candidate
		for suffix := 1; ; suffix++ {
			owner, exists := used[alias]
			if !exists || owner == identity || (identity.Namespace == "" && owner == (toolIdentity{})) {
				break
			}
			suffixText := fmt.Sprintf("_%d", suffix)
			baseLimit := 64 - len(suffixText)
			if len(candidate) > baseLimit {
				candidate = hashedToolAlias(identity)
				if len(candidate) > baseLimit {
					candidate = candidate[:baseLimit]
				}
			}
			alias = candidate + suffixText
		}
		if len(alias) > 64 {
			alias = hashedToolAlias(identity)
		}
		used[alias] = identity
		c.aliasByID[identity] = alias
		c.idByAlias[alias] = identity
	}
}

func hashedToolAlias(identity toolIdentity) string {
	encoded, _ := json.Marshal([3]string{identity.Namespace, identity.Name, identity.Kind})
	sum := sha256.Sum256(encoded)
	return "cts_" + fmt.Sprintf("%x", sum[:24])
}

func isValidFunctionName(name string) bool {
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
	if identity, exists := c.idByAlias[name]; exists {
		return identity, true
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
	if identity.Kind == "custom" && stringField(item, "type") == "function_call" {
		item["type"] = "custom_tool_call"
	}
	return true
}

// ensureTopLevelToolSearch advertises the ordinary tool_search function the
// upstream can call. It only runs when the request has something to search:
// either the client already speaks the tool_search protocol or it declares
// namespaced children that this rewrite is about to move into the index.
func ensureTopLevelToolSearch(root map[string]any, catalog *toolCatalog) bool {
	if catalog == nil || !catalog.clientSearch {
		return false
	}
	tools, ok := root["tools"].([]any)
	if !ok {
		if _, exists := root["tools"]; exists {
			return false
		}
		tools = make([]any, 0, 1)
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if toolType := stringField(tool, "type"); toolType == "tool_search" && !isServerExecutedItem(tool) {
			return false
		}
		if toolType := stringField(tool, "type"); toolType == "function" && stringField(tool, "name") == catalog.searchAlias {
			return false
		}
	}
	root["tools"] = append(tools, map[string]any{
		"type":        "function",
		"name":        catalog.searchAlias,
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

func pruneDeferredNamespaceTools(value any, catalog *toolCatalog) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		if stringField(typed, "type") == namespaceToolType {
			if children, ok := typed["tools"].([]any); ok {
				retained, entries := pruneNamespaceChildren(children, strings.TrimSpace(stringField(typed, "name")), catalog)
				if len(entries) > 0 {
					typed[deferredToolsKey] = appendExistingDeferredEntries(typed[deferredToolsKey], entries)
					if len(retained) > 0 {
						typed["tools"] = retained
					} else {
						delete(typed, "tools")
					}
					changed = true
				}
			}
		}
		for key, child := range typed {
			if key == deferredToolsKey {
				continue
			}
			if pruneDeferredNamespaceTools(child, catalog) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if pruneDeferredNamespaceTools(child, catalog) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func pruneNamespaceChildren(children []any, namespace string, catalog *toolCatalog) ([]any, []any) {
	retained := make([]any, 0, len(children))
	entries := make([]any, 0)
	for _, rawChild := range children {
		child, ok := rawChild.(map[string]any)
		if !ok {
			retained = append(retained, rawChild)
			continue
		}
		if stringField(child, "type") == namespaceToolType {
			nested, okNested := child["tools"].([]any)
			if !okNested {
				retained = append(retained, rawChild)
				continue
			}
			nestedNamespace := joinNamespace(namespace, stringField(child, "name"))
			nestedRetained, nestedEntries := pruneNamespaceChildren(nested, nestedNamespace, catalog)
			entries = append(entries, nestedEntries...)
			if len(nestedRetained) > 0 {
				if len(nestedEntries) > 0 {
					child["tools"] = nestedRetained
					child[deferredToolsKey] = appendExistingDeferredEntries(child[deferredToolsKey], nestedEntries)
				}
				retained = append(retained, child)
			} else if len(nestedEntries) == 0 {
				retained = append(retained, child)
			}
			continue
		}
		switch childType := strings.TrimSpace(stringField(child, "type")); childType {
		case "function", "custom", "":
		default:
			continue
		}
		name := strings.TrimSpace(stringField(child, "name"))
		if name == "" {
			retained = append(retained, rawChild)
			continue
		}
		if catalog == nil || !catalog.clientSearch || !deferredLoading(child) {
			retained = append(retained, rawChild)
			continue
		}
		entries = append(entries, map[string]any{
			"namespace": namespace,
			"name":      name,
		})
	}
	return retained, entries
}

func appendExistingDeferredEntries(existing any, additions []any) []any {
	out := make([]any, 0)
	if values, ok := existing.([]any); ok {
		out = append(out, values...)
	}
	return append(out, additions...)
}

func pruneRequestDeclarations(root map[string]any, catalog *toolCatalog) bool {
	if catalog == nil || !catalog.clientSearch {
		return false
	}
	changed := false
	if tools, ok := root["tools"].([]any); ok {
		retained := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, okTool := rawTool.(map[string]any)
			if okTool && deferredLoading(tool) && toolTypeIsCallable(stringField(tool, "type")) {
				changed = true
				continue
			}
			retained = append(retained, rawTool)
		}
		if len(retained) != len(tools) {
			if len(retained) > 0 {
				root["tools"] = retained
			} else {
				delete(root, "tools")
			}
		}
		if pruneDeferredNamespaceTools(root["tools"], catalog) {
			changed = true
		}
	}
	if input, ok := root["input"].([]any); ok {
		for _, rawItem := range input {
			item, ok := rawItem.(map[string]any)
			if !ok || stringField(item, "type") != "additional_tools" {
				continue
			}
			if tools, okTools := item["tools"].([]any); okTools {
				retained := make([]any, 0, len(tools))
				for _, rawTool := range tools {
					tool, okTool := rawTool.(map[string]any)
					if okTool && deferredLoading(tool) && toolTypeIsCallable(stringField(tool, "type")) {
						changed = true
						continue
					}
					if okTool && stringField(tool, "type") == "function" && stringField(tool, "name") == catalog.searchAlias {
						changed = true
						continue
					}
					retained = append(retained, rawTool)
				}
				if len(retained) != len(tools) {
					if len(retained) > 0 {
						item["tools"] = retained
					} else {
						delete(item, "tools")
					}
				}
			}
			if pruneDeferredNamespaceTools(item["tools"], catalog) {
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
	if pruneRequestDeclarations(root, catalog) {
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
		name := catalog.aliasByID[identity]
		if name == "" {
			name = rawQualifiedToolName(identity.Namespace, identity.Name)
		}
		rawName := rawQualifiedToolName(identity.Namespace, identity.Name)
		if name == "" || name == toolSearchName {
			continue
		}
		if _, exists := existing[name]; exists {
			continue
		}
		if _, exists := existing[rawName]; exists {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var tool map[string]any
		if errDecode := decoder.Decode(&tool); errDecode != nil {
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

func toolTypeIsCallable(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "function", "custom":
		return true
	default:
		return false
	}
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
