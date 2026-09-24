package responses

import (
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// responsesToolDeclaration is one Responses tool declaration paired with the
// Chat Completions function name it produces. Namespace children carry both
// their declared name and the owning namespace, so reverse translation can
// restore the split identity.
type responsesToolDeclaration struct {
	tool      gjson.Result
	chatName  string
	localName string
	namespace string
	custom    bool
}

// walkResponsesToolDeclarations visits the tool declarations of a Responses
// request in one canonical order: the top-level "tools" field first, then
// Codex Desktop (Responses Lite) "additional_tools" input items, namespace
// children in declaration order. Declarations that produce no Chat Completions
// tool are skipped. Visiting stops early once visit returns false.
//
// The emitted chatName is namespace-qualified, capped to the Chat Completions
// function-name limit, and disambiguated when two distinct declarations flatten
// onto the same name. Request conversion, reverse name resolution and freeform
// tool classification all traverse through here, so they cannot disagree about
// which declaration backs a given Chat Completions tool name.
func walkResponsesToolDeclarations(root gjson.Result, visit func(responsesToolDeclaration) bool) {
	var declarations []responsesToolDeclaration
	emit := func(tool gjson.Result, namespaceName string) {
		var custom bool
		switch strings.TrimSpace(tool.Get("type").String()) {
		case "", "function":
		case "custom":
			custom = true
		default:
			return
		}
		localName := responsesToolName(tool)
		if localName == "" {
			return
		}
		declarations = append(declarations, responsesToolDeclaration{
			tool:      tool,
			chatName:  qualifyResponsesNamespaceToolName(namespaceName, localName),
			localName: localName,
			namespace: namespaceName,
			custom:    custom,
		})
	}
	scan := func(tools gjson.Result) {
		if !tools.Exists() || !tools.IsArray() {
			return
		}
		tools.ForEach(func(_, tool gjson.Result) bool {
			if strings.TrimSpace(tool.Get("type").String()) == "namespace" {
				if children := tool.Get("tools"); children.Exists() && children.IsArray() {
					namespaceName := strings.TrimSpace(tool.Get("name").String())
					children.ForEach(func(_, child gjson.Result) bool {
						emit(child, namespaceName)
						return true
					})
				}
				return true
			}
			emit(tool, "")
			return true
		})
	}

	scan(root.Get("tools"))
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				scan(item.Get("tools"))
			}
			return true
		})
	}

	disambiguateResponsesChatToolNames(declarations)

	proceed := true
	for _, declaration := range declarations {
		if !proceed {
			break
		}
		proceed = visit(declaration)
	}
}

// disambiguateResponsesChatToolNames rewrites flattened names in place when
// distinct declarations collapse onto the same capped Chat Completions name.
// Identity is the pre-cap qualified name: declarations that qualified to the
// same name before the cap (one tool delivered through both "tools" and
// "additional_tools", or a flat tool colliding with a namespace child) are
// the same upstream tool and keep the shared first-wins name, while distinct
// names that only collide through truncation get "_1"-style suffixes so the
// deduplication downstream never silently drops a real tool.
//
// Qualified names that fit the cap unchanged are claimed before any
// truncation alias is assigned (equal raw names are one identity, so those
// claims cannot conflict). Local names that fit the cap are reserved the
// same way: a replayed call or tool_choice that omits the namespace carries
// the local name, and local-name recovery resolves it to the declaration,
// so a capped alias occupying that name would win the earlier
// exact-emitted-alias match and attribute those calls to the wrong tool. A
// long declaration whose capped tail lands on any reserved name therefore
// takes the suffix itself. Suffixed variants stay within the name cap, and
// every variant is claimed in the same pass so a later declaration cannot
// resurrect a collision.
//
// A local name carried by more than one distinct identity is ambiguous: no
// namespace-less call naming it can be resolved, so the name is burned
// instead of being awarded to whichever declaration came first. Burning
// matters even when the name is also a declaration's capped alias — that
// alias would be emitted verbatim, win the exact-emitted-alias match, and
// silently route the other namespace's calls to the first declaration.
func disambiguateResponsesChatToolNames(declarations []responsesToolDeclaration) {
	claimed := make(map[string]string, len(declarations))
	claim := func(candidate, identity string) bool {
		if ownerClaim, taken := claimed[candidate]; !taken {
			claimed[candidate] = identity
			return true
		} else {
			return ownerClaim == identity
		}
	}
	longDeclarations := make([]int, 0)
	identities := make([]string, len(declarations))
	// localName → the single identity that declares it, or "" once a second,
	// distinct identity shows the name is ambiguous.
	localOwners := make(map[string]string)
	ambiguousLocalNames := make(map[string]struct{})
	for i := range declarations {
		identity := rawResponsesNamespaceQualifiedName(declarations[i].namespace, declarations[i].localName)
		identities[i] = identity
		if len(identity) > responsesChatToolNameLimit {
			longDeclarations = append(longDeclarations, i)
		} else {
			claim(identity, identity)
		}
		local := declarations[i].localName
		if local == "" || local == identity || len(local) > responsesChatToolNameLimit {
			continue
		}
		if ownerLocal, seen := localOwners[local]; !seen {
			localOwners[local] = identity
		} else if ownerLocal != "" && ownerLocal != identity {
			localOwners[local] = ""
		}
	}
	for local, ownerLocal := range localOwners {
		// Reserving under any identity keeps the name out of every later
		// truncation alias; ambiguous names additionally never get emitted.
		claim(local, ownerLocal)
		if ownerLocal == "" {
			ambiguousLocalNames[local] = struct{}{}
		}
	}
	isAmbiguous := func(name string) bool {
		_, ambiguous := ambiguousLocalNames[name]
		return ambiguous
	}
	for _, i := range longDeclarations {
		identity := identities[i]
		name := declarations[i].chatName
		if !isAmbiguous(name) && claim(name, identity) {
			continue
		}
		for suffix := 1; ; suffix++ {
			candidate := capResponsesChatToolName(name + "_" + strconv.Itoa(suffix))
			if isAmbiguous(candidate) {
				continue
			}
			if claim(candidate, identity) {
				declarations[i].chatName = candidate
				break
			}
		}
	}
}

// mergeResponsesRequestChatTools converts every tool declaration in a Responses
// request into Chat Completions form, merging the top-level "tools" field with
// Codex Desktop (Responses Lite) "additional_tools" input items.
//
// Codex clients may deliver the same tool through both channels, and namespace
// qualification can collapse distinct declarations onto one Chat Completions
// name, so entries are deduplicated by function name. The first occurrence
// wins, which keeps the top-level "tools" definition authoritative over the
// "additional_tools" copy. Chat Completions requires tool names to be unique;
// strict upstreams reject the whole request otherwise.
func mergeResponsesRequestChatTools(root gjson.Result) [][]byte {
	return newResponsesToolIndex(root).chatTools()
}

// convertResponsesCustomToolToOpenAIChat maps a Responses freeform ("custom")
// tool onto a Chat Completions function tool with a single freeform "input"
// string, mirroring the function-based shape Codex uses for apply_patch.
func convertResponsesCustomToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}
	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	return chatTool, true
}

func convertResponsesFunctionToolToOpenAIChat(tool gjson.Result, overrideName string) ([]byte, bool) {
	name := strings.TrimSpace(overrideName)
	if name == "" {
		name = responsesToolName(tool)
	}
	if name == "" {
		return nil, false
	}

	chatTool := []byte(`{"type":"function","function":{"name":"","description":"","parameters":{}}}`)
	chatTool, _ = sjson.SetBytes(chatTool, "function.name", name)
	if description := responsesToolDescription(tool); description != "" {
		chatTool, _ = sjson.SetBytes(chatTool, "function.description", description)
	}
	if parameters := responsesToolParameters(tool); parameters.Exists() {
		chatTool, _ = sjson.SetRawBytes(chatTool, "function.parameters", []byte(parameters.Raw))
	}
	return chatTool, true
}

func responsesToolName(tool gjson.Result) string {
	if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
		return name
	}
	return strings.TrimSpace(tool.Get("function.name").String())
}

func responsesToolDescription(tool gjson.Result) string {
	if description := tool.Get("description").String(); description != "" {
		return description
	}
	return tool.Get("function.description").String()
}

func responsesToolParameters(tool gjson.Result) gjson.Result {
	for _, path := range []string{
		"parameters",
		"parametersJsonSchema",
		"input_schema",
		"function.parameters",
		"function.parametersJsonSchema",
	} {
		if parameters := tool.Get(path); parameters.Exists() {
			return parameters
		}
	}
	return gjson.Result{}
}

// responsesToolOutputText flattens a tool output value that may be a plain
// string or an array of content parts ({"type":"input_text","text":...}) into
// a single text payload for a Chat Completions tool message.
func responsesToolOutputText(output gjson.Result) string {
	if output.Type == gjson.String {
		return output.String()
	}
	if output.IsArray() {
		var b strings.Builder
		output.ForEach(func(_, part gjson.Result) bool {
			if part.Type == gjson.String {
				b.WriteString(part.String())
				return true
			}
			if text := part.Get("text"); text.Exists() {
				b.WriteString(text.String())
			}
			return true
		})
		return b.String()
	}
	if output.Exists() {
		return output.Raw
	}
	return ""
}

// responsesCustomToolNames collects the Chat Completions names of the freeform
// ("custom") tools that survive the merge, so response translation only unwraps
// freeform arguments for calls whose winning declaration really was freeform.
//
// Declaration types may differ across the two delivery channels: a top-level
// function and an "additional_tools" custom tool can flatten to the same name.
// Classification therefore follows the same first-wins rule as the merge —
// a discarded custom declaration must not turn a surviving ordinary function
// into a custom_tool_call.
func responsesCustomToolNames(requestRawJSON []byte) map[string]struct{} {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).custom
}

func responsesSingleCustomToolName(requestRawJSON []byte) (string, bool) {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).singleCustomName()
}

// unwrapCustomToolInput extracts the freeform input from the {"input": "..."}
// function-call arguments produced for a converted custom tool; it falls back
// to the raw arguments when the wrapper is absent.
func unwrapCustomToolInput(arguments string) string {
	if v := gjson.Get(arguments, "input"); v.Exists() {
		if v.Type == gjson.String {
			return v.String()
		}
		return v.Raw
	}
	return arguments
}

// responsesChatToolNameLimit is the Chat Completions function name limit enforced
// by strict upstreams (e.g. z-ai/glm). Responses namespace tools routinely
// flatten to names longer than this.
const responsesChatToolNameLimit = 64

func qualifyResponsesNamespaceToolName(namespaceName, childName string) string {
	return capResponsesChatToolName(rawResponsesNamespaceQualifiedName(namespaceName, childName))
}

// rawResponsesNamespaceQualifiedName is qualifyResponsesNamespaceToolName
// without the length cap, so disambiguation can tell a genuine name apart
// from a truncation-induced collision.
func rawResponsesNamespaceQualifiedName(namespaceName, childName string) string {
	childName = strings.TrimSpace(childName)
	if childName == "" || namespaceName == "" || strings.HasPrefix(childName, "mcp__") {
		return childName
	}
	if strings.HasPrefix(childName, namespaceName) {
		return childName
	}
	if strings.HasSuffix(namespaceName, "__") {
		return namespaceName + childName
	}
	return namespaceName + "__" + childName
}

// capResponsesChatToolName truncates a flattened Responses tool name to the
// Chat Completions limit while keeping the tail, which carries the most
// identifying part of the name (the tool's local name). Namespace-qualified
// names share a long "mcp__<server>" prefix, so keeping the tail preserves more
// usable signal than keeping the head. Truncation can leave a partial "_"/"-"
// run at the start; leading separators are stripped because some strict
// upstreams reject names that do not begin with an alphanumeric character.
// This is a pure function of the input name, so every path that derives a
// chat function name (declarations, replayed calls, tool_choice, reverse
// resolution) stays consistent with every other.
func capResponsesChatToolName(name string) string {
	if len(name) <= responsesChatToolNameLimit {
		return name
	}
	truncated := name[len(name)-responsesChatToolNameLimit:]
	if trimmed := strings.TrimLeft(truncated, "_-"); trimmed != "" {
		return trimmed
	}
	return truncated
}

// resolveResponsesQualifiedToolIdentity maps an emitted Chat Completions
// function name back to the Responses declaration that produced it.
//
// Declarations are walked in the same order mergeResponsesRequestChatTools
// uses, and the first one producing the name wins, so reverse translation
// reports the identity of the declaration that actually survived the merge. A
// flat top-level tool named "editor__apply_patch" therefore stays flat even
// when a later namespace declares a child qualifying to the same name.
func resolveResponsesQualifiedToolIdentity(root gjson.Result, qualifiedName string) (name, namespace string, found bool) {
	d, found := newResponsesToolIndex(root).byChat[qualifiedName]
	return d.localName, d.namespace, found
}

// chatNameForResponsesNamespaceToolCall returns the Chat Completions name the
// request's declarations assign to the (namespace, localName) identity of a
// replayed or forced tool call. Declaration-derived names carry the same
// 64-character cap and disambiguation as the outgoing tools array, so replayed
// calls stay consistent with their declarations even when a collision renamed
// the tool. Unknown identities fall back to plain namespace qualification.
func chatNameForResponsesNamespaceToolCall(requestRawJSON []byte, namespace, localName string) string {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).namespaceName(namespace, localName)
}

// canonicalResponsesToolName resolves a name carried by a replayed call or
// tool_choice that omits the namespace. Exact emitted names win, then the
// declarations' uncapped qualified names are checked, then an omitted namespace
// is restored only when the current request declares exactly one matching local
// name. Qualified-name equality is direct provenance — the name can only have
// been generated from that declaration — so it outranks local-name recovery,
// which merely guesses at a namespace and can otherwise hijack a name that is
// also another namespace's declared child. Ambiguous names remain unresolved
// rather than being dispatched to another tool.
func canonicalResponsesToolName(requestRawJSON []byte, name string) string {
	return newResponsesToolIndex(gjson.ParseBytes(requestRawJSON)).canonicalName(name)
}

// avoidResponsesDeclaredChatAliases keeps a fallback name (the blind cap of an
// unresolved or ambiguous name) from colliding with any alias the request's
// declarations actually emit. Dispatching such a call to a real declaration
// would silently invoke the wrong tool; a distinct name keeps the identity
// unresolved instead, which upstreams and clients can surface properly.
func avoidResponsesDeclaredChatAliases(root gjson.Result, candidate string) string {
	return newResponsesToolIndex(root).avoidAlias(candidate)
}

func splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON []byte, qualifiedName string) (name, namespace string) {
	qualifiedName = strings.TrimSpace(qualifiedName)
	if qualifiedName == "" {
		return "", ""
	}

	if resolvedName, resolvedNamespace, ok := resolveResponsesQualifiedToolIdentity(gjson.ParseBytes(requestRawJSON), qualifiedName); ok {
		return resolvedName, resolvedNamespace
	}
	return qualifiedName, ""
}

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func applyResponsesFunctionCallNamespaceFields(item []byte, requestRawJSON []byte, qualifiedName string, itemPath string) []byte {
	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(requestRawJSON, qualifiedName)
	return translatorcommon.SetResponsesToolCallIdentity(item, name, namespace, itemPath)
}
