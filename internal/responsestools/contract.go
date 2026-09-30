package responsestools

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// Protocol field and item names of the Responses tool contract.
const (
	// ToolSearchName is the ordinary function alias used for client-executed
	// tool search on the wire to chat-shaped upstreams.
	ToolSearchName = "tool_search"
	// NamespaceToolType groups tool declarations under a shared namespace.
	NamespaceToolType = "namespace"
	// DeferredToolsKey carries pruned deferred entries on namespace containers.
	DeferredToolsKey = "codex_deferred_tools"
)

// IsToolDeclarationInput identifies the protocol input containers whose tools
// arrays are declarations. Business payloads are never declaration scopes.
func IsToolDeclarationInput(item map[string]any) bool {
	switch stringField(item, "type") {
	case "additional_tools", "tool_search_output":
		return true
	default:
		return false
	}
}

// searchAliasOwner reserves the bridge's search entry point while activation
// aliases are minted. It is deliberately not a zero ToolIdentity: the
// top-level escape hatch that lets an eager function keep its own name must
// not let a real tool steal the search alias.
var searchAliasOwner = ToolIdentity{Name: "\x00tool_search", Kind: ToolKindFunction}

// ToolSource records whether a declaration was stated upfront or learned from
// a tool_search_output discovery round.
type ToolSource uint8

const (
	// SourceDeclared marks upfront declarations (tools/additional_tools).
	SourceDeclared ToolSource = iota
	// SourceDiscovered marks tools learned from tool_search_output items.
	SourceDiscovered
)

// ToolContract is the single parsed view of every tool declaration carried by
// one client request: top-level tools, nested namespaces, additional_tools,
// and all discovery rounds, plus the activation aliases derived from them.
type ToolContract struct {
	Identities           []ToolIdentity
	Declarations         map[ToolIdentity]json.RawMessage
	Discovered           map[ToolIdentity]struct{}
	DiscoveredRound      map[ToolIdentity]int
	LatestRound          int
	PendingRound         int
	ClientSearch         bool
	SearchBridged        bool
	ServerSearch         bool
	SearchAlias          string
	SearchSyntheticNulls map[string]struct{}
	searchSyntheticSeen  bool
	Exact                map[string]ToolIdentity
	Normalized           map[string]ToolIdentity
	Local                map[string]ToolIdentity
	Namespaces           map[string]ToolIdentity
	NamespaceCounts      map[string]int
	TopLevel             map[string]struct{}
	Deferred             map[ToolIdentity]bool
	AliasByID            map[ToolIdentity]string
	IDByAlias            map[string]ToolIdentity
	discoveryRoundSeen   map[ToolIdentity]json.RawMessage
	discoveryRound       int
	discoveryConflict    bool
}

// NewToolContract returns an empty contract ready for collection.
func NewToolContract() *ToolContract {
	return &ToolContract{
		Declarations:         make(map[ToolIdentity]json.RawMessage),
		Discovered:           make(map[ToolIdentity]struct{}),
		DiscoveredRound:      make(map[ToolIdentity]int),
		Exact:                make(map[string]ToolIdentity),
		Normalized:           make(map[string]ToolIdentity),
		Local:                make(map[string]ToolIdentity),
		Namespaces:           make(map[string]ToolIdentity),
		NamespaceCounts:      make(map[string]int),
		TopLevel:             make(map[string]struct{}),
		Deferred:             make(map[ToolIdentity]bool),
		AliasByID:            make(map[ToolIdentity]string),
		IDByAlias:            make(map[string]ToolIdentity),
		SearchSyntheticNulls: make(map[string]struct{}),
		discoveryRoundSeen:   make(map[ToolIdentity]json.RawMessage),
	}
}

// ParseContract extracts the tool contract from one raw client request body.
// It returns nil when the body cannot carry any tool metadata, so callers on
// unmatched routes pay no JSON parse for ordinary requests.
func ParseContract(body []byte) *ToolContract {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil
	}
	// Deferred tool metadata is the only thing this contract describes, so skip
	// the parse entirely for requests that cannot carry any.
	if !bytes.Contains(trimmed, []byte(ToolSearchName)) &&
		!bytes.Contains(trimmed, []byte(NamespaceToolType)) &&
		!bytes.Contains(trimmed, []byte("defer_loading")) &&
		!bytes.Contains(trimmed, []byte("\"custom\"")) {
		return nil
	}
	value, ok := decodeValue(body)
	if !ok {
		return nil
	}
	contract := NewToolContract()
	switch root := value.(type) {
	case map[string]any:
		contract.collectToolArray(root["tools"], "", SourceDeclared)
		if input, ok := root["input"].([]any); ok {
			for inputIndex, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				switch stringField(item, "type") {
				case "additional_tools":
					contract.collectToolArray(item["tools"], "", SourceDeclared)
				case "tool_search_output":
					contract.PendingRound = inputIndex + 1
					contract.beginDiscoveryRound(contract.PendingRound)
					contract.collectToolArray(item["tools"], "", SourceDiscovered)
					if IsServerExecutedItem(item) {
						contract.ServerSearch = true
					} else {
						contract.markClientSearch(item)
					}
				case "tool_search_call":
					if IsServerExecutedItem(item) {
						contract.ServerSearch = true
					} else {
						contract.markClientSearch(item)
					}
				}
			}
		}
	case []any:
		contract.collectToolArray(root, "", SourceDeclared)
	}
	contract.finalize()
	contract.discoveryRoundSeen = nil
	if len(contract.Identities) == 0 && len(contract.TopLevel) == 0 && !contract.ClientSearch && !contract.ServerSearch {
		return nil
	}
	return contract
}

func (c *ToolContract) beginDiscoveryRound(round int) {
	if c == nil {
		return
	}
	c.discoveryRound = round
	c.discoveryRoundSeen = make(map[ToolIdentity]json.RawMessage)
}

// decodeValue decodes JSON while preserving number precision, so large
// integers survive an unchanged round trip.
func decodeValue(body []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	return value, true
}

// MarkClientSearch records that the request participates in the
// client-executed tool_search protocol. Items without an explicit execution
// field default to client, which several Responses-compatible clients rely on.
func (c *ToolContract) markClientSearch(item map[string]any) {
	if !strings.EqualFold(strings.TrimSpace(stringField(item, "execution")), "server") {
		c.ClientSearch = true
	}
}

// PayloadUsesToolProtocol reports whether a client payload carries tool
// protocol state the core Responses tools layer can actually rewrite: a
// client-executed tool_search declaration or history item, or custom tool
// declarations and history. Ordinary tool metadata such as a namespace
// container or a plain function declaration is not enough, because Prepare
// passes those turns through byte-for-byte. Callers that must decide whether a
// turn needs rewriting (for example the WebSocket replay gate) use this
// instead of testing for tool metadata.
func PayloadUsesToolProtocol(body []byte) bool {
	contract := ParseContract(body)
	if contract != nil && contract.ClientSearch {
		return true
	}
	return hasCustomDeclarations(body) || hasCustomHistory(body)
}

func (c *ToolContract) collectToolArray(value any, inheritedNamespace string, source ToolSource) {
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
		case NamespaceToolType:
			namespace := strings.TrimSpace(stringField(tool, "name"))
			if inheritedNamespace != "" && namespace != "" {
				namespace = JoinNamespace(inheritedNamespace, namespace)
			} else if inheritedNamespace != "" {
				namespace = inheritedNamespace
			}
			c.collectToolArray(tool["tools"], namespace, source)
			continue
		case "tool_search":
			if IsServerExecutedItem(tool) {
				c.ServerSearch = true
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
			kind := NormalizeToolKind(stringField(tool, "type"))
			identity := ToolIdentity{Namespace: namespace, Name: name, Kind: kind}
			c.addIdentityWithKind(namespace, name, kind)
			if source == SourceDiscovered {
				c.addDiscoveredDeclaration(namespace, name, kind, tool)
			} else if namespace == "" {
				c.TopLevel[name] = struct{}{}
				c.Deferred[identity] = deferredLoading(tool)
			} else {
				c.Deferred[identity] = deferredLoading(tool)
			}
		}
	}
}

func (c *ToolContract) mergeSearchSyntheticNulls(candidates map[string]struct{}, seen bool) {
	if c == nil || !seen {
		return
	}
	if !c.searchSyntheticSeen {
		c.SearchSyntheticNulls = make(map[string]struct{}, len(candidates))
		for path := range candidates {
			c.SearchSyntheticNulls[path] = struct{}{}
		}
		c.searchSyntheticSeen = true
		return
	}
	for path := range c.SearchSyntheticNulls {
		if _, exists := candidates[path]; !exists {
			delete(c.SearchSyntheticNulls, path)
		}
	}
}

func (c *ToolContract) addDiscoveredDeclaration(namespace, name string, kind ToolKind, tool map[string]any) {
	identity := ToolIdentity{
		Namespace: strings.TrimSpace(namespace),
		Name:      strings.TrimSpace(name),
		Kind:      kind,
	}
	if identity.Name == "" {
		return
	}
	raw, err := json.Marshal(tool)
	if err != nil {
		return
	}
	if c.PendingRound > 0 {
		if c.discoveryRound != c.PendingRound {
			c.beginDiscoveryRound(c.PendingRound)
		}
		if previous, exists := c.discoveryRoundSeen[identity]; exists {
			if !bytes.Equal(previous, raw) {
				c.discoveryConflict = true
				return
			}
		} else {
			c.discoveryRoundSeen[identity] = append(json.RawMessage(nil), raw...)
		}
		c.Declarations[identity] = raw
		c.Discovered[identity] = struct{}{}
		c.DiscoveredRound[identity] = c.PendingRound
		if c.PendingRound > c.LatestRound {
			c.LatestRound = c.PendingRound
		}
		return
	}
	c.Declarations[identity] = raw
	c.Discovered[identity] = struct{}{}
}

func (c *ToolContract) collectDeferredMetadata(tool map[string]any) {
	entries, ok := tool[DeferredToolsKey].([]any)
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
				identity := ToolIdentity{Namespace: namespace, Name: name, Kind: ToolKindFunction}
				c.addIdentityWithKind(namespace, name, ToolKindFunction)
				c.Deferred[identity] = true
			}
		case map[string]any:
			entryNamespace := strings.TrimSpace(stringField(entry, "namespace"))
			if entryNamespace == "" {
				entryNamespace = namespace
			}
			if name := strings.TrimSpace(stringField(entry, "name")); name != "" && entryNamespace != "" {
				kind := NormalizeToolKind(stringField(entry, "type"))
				identity := ToolIdentity{Namespace: entryNamespace, Name: name, Kind: kind}
				c.addIdentityWithKind(entryNamespace, name, kind)
				c.Deferred[identity] = true
			}
		}
	}
}

func (c *ToolContract) addIdentityWithKind(namespace, name string, kind ToolKind) {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	for _, existing := range c.Identities {
		if existing.Namespace == namespace && existing.Name == name && existing.Kind == kind {
			return
		}
	}
	c.Identities = append(c.Identities, ToolIdentity{Namespace: namespace, Name: name, Kind: kind})
}

// NormalizeToolKind maps a declared tool type to its canonical kind.
// Declaration and call items spell the same tool differently ("custom" versus
// "custom_tool_call"), so both are recognized; anything else is an ordinary
// function.
func NormalizeToolKind(value string) ToolKind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "custom", "custom_tool_call":
		return ToolKindCustom
	}
	return ToolKindFunction
}

func deferredLoading(tool map[string]any) bool {
	deferred, _ := tool["defer_loading"].(bool)
	return deferred
}

func (c *ToolContract) finalize() {
	exactAmbiguous := make(map[string]bool)
	normalizedAmbiguous := make(map[string]bool)
	localAmbiguous := make(map[string]bool)
	registerExact := func(name string, identity ToolIdentity) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if previous, exists := c.Exact[name]; exists && previous != identity {
			exactAmbiguous[name] = true
			return
		}
		c.Exact[name] = identity
	}
	registerNormalized := func(name string, identity ToolIdentity) {
		name = NormalizeToolName(name)
		if name == "" {
			return
		}
		if previous, exists := c.Normalized[name]; exists && previous != identity {
			normalizedAmbiguous[name] = true
			return
		}
		c.Normalized[name] = identity
	}
	for _, identity := range c.Identities {
		raw := RawQualifiedToolName(identity.Namespace, identity.Name)
		alias := CapToolName(raw)
		registerExact(raw, identity)
		registerExact(alias, identity)
		registerNormalized(raw, identity)
		if previous, exists := c.Local[identity.Name]; exists && previous != identity {
			localAmbiguous[identity.Name] = true
		} else {
			c.Local[identity.Name] = identity
		}
		c.NamespaceCounts[identity.Namespace]++
		c.Namespaces[identity.Namespace] = identity
	}
	for name, ambiguous := range localAmbiguous {
		if ambiguous {
			delete(c.Local, name)
		}
	}
	for name := range exactAmbiguous {
		delete(c.Exact, name)
	}
	for name := range normalizedAmbiguous {
		delete(c.Normalized, name)
	}
	for namespace, count := range c.NamespaceCounts {
		if count != 1 {
			delete(c.Namespaces, namespace)
		}
	}
	c.SearchAlias = c.uniqueSearchAlias()
	c.buildActivationAliases()
}

func (c *ToolContract) uniqueSearchAlias() string {
	if !c.ClientSearch {
		return ToolSearchName
	}
	if _, exists := c.TopLevel[ToolSearchName]; !exists {
		return ToolSearchName
	}
	base := "cts_" + ToolSearchName
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate += itoa(suffix)
		}
		if _, exists := c.TopLevel[candidate]; !exists {
			return candidate
		}
	}
}

func (c *ToolContract) buildActivationAliases() {
	if len(c.Declarations) == 0 {
		return
	}
	identities := make([]ToolIdentity, 0, len(c.Declarations))
	for identity := range c.Declarations {
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

	used := make(map[string]ToolIdentity, len(identities)+len(c.TopLevel))
	for name := range c.TopLevel {
		used[name] = ToolIdentity{}
	}
	// The search entry point owns its alias even when it keeps the default
	// "tool_search" spelling, so a discovered or deferred tool that shares
	// that name is suffixed instead of colliding with the entry point.
	if c.ClientSearch {
		used[c.SearchAlias] = searchAliasOwner
	}
	for _, identity := range identities {
		raw := RawQualifiedToolName(identity.Namespace, identity.Name)
		candidate := raw
		if identity.Namespace != "" || !IsValidFunctionName(candidate) {
			candidate = HashedToolAlias(identity)
		}
		alias := candidate
		for suffix := 1; ; suffix++ {
			owner, exists := used[alias]
			if !exists || owner == identity || (identity.Namespace == "" && owner == (ToolIdentity{})) {
				break
			}
			suffixText := itoa(suffix)
			baseLimit := 64 - len(suffixText)
			if len(candidate) > baseLimit {
				candidate = HashedToolAlias(identity)
				if len(candidate) > baseLimit {
					candidate = candidate[:baseLimit]
				}
			}
			alias = candidate + suffixText
		}
		if len(alias) > 64 {
			alias = HashedToolAlias(identity)
		}
		used[alias] = identity
		c.AliasByID[identity] = alias
		c.IDByAlias[alias] = identity
	}
}

// Resolve maps one upstream function name back to its canonical identity.
// The tool_search alias, eager top-level tools, and ambiguous names never
// resolve: only explicitly deferred or discovered identities restore.
func (c *ToolContract) Resolve(name string) (ToolIdentity, bool) {
	if c == nil {
		return ToolIdentity{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" || name == ToolSearchName {
		return ToolIdentity{}, false
	}
	if _, exists := c.TopLevel[name]; exists {
		return ToolIdentity{}, false
	}
	if identity, exists := c.IDByAlias[name]; exists {
		return identity, true
	}
	if identity, exists := c.Exact[name]; exists {
		return identity, true
	}
	if identity, exists := c.Namespaces[name]; exists {
		return identity, true
	}
	if identity, exists := c.Local[name]; exists {
		return identity, true
	}
	if identity, exists := c.Normalized[NormalizeToolName(name)]; exists {
		return identity, true
	}
	return ToolIdentity{}, false
}
