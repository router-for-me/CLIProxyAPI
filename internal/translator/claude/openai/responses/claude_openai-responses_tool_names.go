package responses

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

var claudeToolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// claudeToolNames maps qualified Responses tool identities to unique Claude
// tool names for one request, and back. The request and response translators
// build it from the same Responses JSON, so both sides agree on every name.
type claudeToolNames struct {
	toClaude   map[string]string
	fromClaude map[string]string
}

// buildClaudeToolNames allocates Claude names in a fixed order so the result
// does not depend on declaration order:
//  1. Passthrough tools (for example web_search) keep their names.
//  2. Declared function and custom tools with a valid name keep it.
//  3. A declared name that must change gets the plain sanitized name when no
//     other changed name sanitizes to the same value and it is free.
//  4. All other changed names, in sorted order, get a hashed name.
//
// Names found only in history are allocated last with the same rules, so they
// can never change the name of a declared tool.
func buildClaudeToolNames(root gjson.Result) claudeToolNames {
	m := claudeToolNames{toClaude: map[string]string{}, fromClaude: map[string]string{}}
	taken := map[string]bool{}
	winners := responsesToolWinners(root)
	var declared []string
	for _, descriptor := range responsesToolDescriptors(root) {
		if winner, ok := winners[descriptor.name]; !ok || winner.order != descriptor.order {
			continue
		}
		switch descriptor.toolType {
		case "function", "custom":
			declared = append(declared, descriptor.name)
		default:
			m.assign(descriptor.name, descriptor.name, taken)
		}
	}
	m.allocate(declared, taken)
	m.allocate(responsesHistoryToolIdentities(root), taken)
	return m
}

// claudeName returns the Claude name for a qualified Responses identity.
func (m claudeToolNames) claudeName(identity string) string {
	if name, ok := m.toClaude[identity]; ok {
		return name
	}
	return util.SanitizeClaudeFunctionName(identity)
}

// identity returns the qualified Responses identity for a Claude name. Names
// that are not in the map are returned unchanged.
func (m claudeToolNames) identity(claudeName string) string {
	if identity, ok := m.fromClaude[claudeName]; ok {
		return identity
	}
	return claudeName
}

func (m claudeToolNames) assign(identity, name string, taken map[string]bool) {
	m.toClaude[identity] = name
	m.fromClaude[name] = identity
	taken[name] = true
}

func (m claudeToolNames) allocate(identities []string, taken map[string]bool) {
	var changed []string
	seen := map[string]bool{}
	for _, identity := range identities {
		if _, done := m.toClaude[identity]; done || identity == "" || seen[identity] {
			continue
		}
		seen[identity] = true
		if claudeToolNamePattern.MatchString(identity) && !taken[identity] {
			m.assign(identity, identity, taken)
			continue
		}
		changed = append(changed, identity)
	}

	count := map[string]int{}
	for _, identity := range changed {
		count[util.SanitizeClaudeFunctionName(identity)]++
	}
	var hashed []string
	for _, identity := range changed {
		if base := util.SanitizeClaudeFunctionName(identity); count[base] == 1 && !taken[base] {
			m.assign(identity, base, taken)
		} else {
			hashed = append(hashed, identity)
		}
	}

	sort.Strings(hashed)
	for _, identity := range hashed {
		base := util.SanitizeClaudeFunctionName(identity)
		if len(base) > 53 {
			base = base[:53]
		}
		for attempt := 0; ; attempt++ {
			seed := identity
			if attempt > 0 {
				seed += "\x00" + strconv.Itoa(attempt)
			}
			sum := sha256.Sum256([]byte(seed))
			if name := base + "_" + hex.EncodeToString(sum[:])[:10]; !taken[name] {
				m.assign(identity, name, taken)
				break
			}
		}
	}
}

// responsesHistoryToolIdentities lists the qualified names of function_call and
// custom_tool_call items in input, in order.
func responsesHistoryToolIdentities(root gjson.Result) []string {
	var identities []string
	root.Get("input").ForEach(func(_, item gjson.Result) bool {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			name := item.Get("name").String()
			if namespaceName := strings.TrimSpace(item.Get("namespace").String()); namespaceName != "" {
				name = qualifyResponsesNamespaceToolName(namespaceName, name)
			}
			identities = append(identities, name)
		}
		return true
	})
	return identities
}
