package responses

import (
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
)

type responsesToolIdentity struct {
	namespace string
	name      string
}

// responsesToolIndex is scoped to one request. History and declarations are
// scanned once; replayed calls and streaming events only consult these maps.
type responsesToolIndex struct {
	declarations []responsesToolDeclaration
	byChat       map[string]responsesToolDeclaration
	byIdentity   map[responsesToolIdentity]string
	byRaw        map[string]string
	byLocal      map[string]string // Empty means multiple distinct emitted tools.
	custom       map[string]struct{}
}

func newResponsesToolIndex(root gjson.Result) *responsesToolIndex {
	idx := &responsesToolIndex{
		byChat:     make(map[string]responsesToolDeclaration),
		byIdentity: make(map[responsesToolIdentity]string),
		byRaw:      make(map[string]string),
		byLocal:    make(map[string]string),
		custom:     make(map[string]struct{}),
	}
	walkResponsesToolDeclarations(root, func(d responsesToolDeclaration) bool {
		idx.declarations = append(idx.declarations, d)
		identity := responsesToolIdentity{d.namespace, d.localName}
		if _, exists := idx.byIdentity[identity]; !exists {
			idx.byIdentity[identity] = d.chatName
		}
		raw := rawResponsesNamespaceQualifiedName(d.namespace, d.localName)
		if _, exists := idx.byRaw[raw]; !exists {
			idx.byRaw[raw] = d.chatName
		}
		if _, exists := idx.byChat[d.chatName]; exists {
			return true
		}
		idx.byChat[d.chatName] = d
		if _, exists := idx.byLocal[d.localName]; exists {
			idx.byLocal[d.localName] = ""
		} else {
			idx.byLocal[d.localName] = d.chatName
		}
		if d.custom {
			idx.custom[d.chatName] = struct{}{}
		}
		return true
	})
	return idx
}

func (idx *responsesToolIndex) namespaceName(namespace, name string) string {
	if chatName, ok := idx.byIdentity[responsesToolIdentity{namespace, name}]; ok {
		return chatName
	}
	return idx.avoidAlias(qualifyResponsesNamespaceToolName(namespace, name))
}

func (idx *responsesToolIndex) canonicalName(name string) string {
	if _, ok := idx.byChat[name]; ok {
		return name
	}
	if chatName, ok := idx.byRaw[name]; ok {
		return chatName
	}
	if chatName := idx.byLocal[name]; chatName != "" {
		return chatName
	}
	return idx.avoidAlias(capResponsesChatToolName(name))
}

func (idx *responsesToolIndex) avoidAlias(candidate string) string {
	if _, taken := idx.byChat[candidate]; !taken {
		return candidate
	}
	for suffix := 1; ; suffix++ {
		variant := capResponsesChatToolName(candidate + "_" + strconv.Itoa(suffix))
		if _, taken := idx.byChat[variant]; !taken {
			return variant
		}
	}
}

func (idx *responsesToolIndex) applyIdentity(item []byte, qualifiedName, itemPath string) []byte {
	name, namespace := strings.TrimSpace(qualifiedName), ""
	if d, ok := idx.byChat[name]; ok {
		name, namespace = d.localName, d.namespace
	}
	return translatorcommon.SetResponsesToolCallIdentity(item, name, namespace, itemPath)
}

func (idx *responsesToolIndex) singleCustomName() (string, bool) {
	if len(idx.custom) == 1 {
		for name := range idx.custom {
			return name, len(idx.byChat) == 1
		}
	}
	return "", false
}

func (idx *responsesToolIndex) chatTools() [][]byte {
	var merged [][]byte
	seen := make(map[string]struct{})
	for _, d := range idx.declarations {
		if _, exists := seen[d.chatName]; exists {
			continue
		}
		convert := convertResponsesFunctionToolToOpenAIChat
		if d.custom {
			convert = convertResponsesCustomToolToOpenAIChat
		}
		if tool, ok := convert(d.tool, d.chatName); ok {
			merged = append(merged, tool)
			seen[d.chatName] = struct{}{}
		}
	}
	return merged
}
