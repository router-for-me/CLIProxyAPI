package multiagentv2

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PrepareCodexCollaborationPlaintext changes only known collaboration tools'
// string message declarations with encrypted:true. It does not prepare the
// OAuth optimizer, rewrite descriptions/namespaces, or decrypt conversation data.
func PrepareCodexCollaborationPlaintext(ctx context.Context, headers http.Header, payload []byte, enabled bool) []byte {
	if !codexMultiAgentV2ClientEnabled(ctx, headers, enabled) || !gjson.ValidBytes(payload) {
		return payload
	}
	var paths []string
	collectPlaintextCollaborationPaths(gjson.GetBytes(payload, "tools"), "tools", "", &paths)
	for index, item := range gjson.GetBytes(payload, "input").Array() {
		if item.Get("type").String() == "additional_tools" {
			collectPlaintextCollaborationPaths(item.Get("tools"), fmt.Sprintf("input.%d.tools", index), "", &paths)
		}
	}
	updated := payload
	for _, path := range paths {
		var err error
		updated, err = sjson.DeleteBytes(updated, path+".parameters.properties.message.encrypted")
		if err != nil {
			return payload
		}
	}
	return updated
}

func collectPlaintextCollaborationPaths(tools gjson.Result, path, namespace string, paths *[]string) {
	if !tools.IsArray() {
		return
	}
	for index, tool := range tools.Array() {
		toolPath := fmt.Sprintf("%s.%d", path, index)
		name := tool.Get("name").String()
		switch tool.Get("type").String() {
		case "namespace":
			if namespace == "" && knownPlaintextCollaborationNamespace(name) {
				collectPlaintextCollaborationPaths(tool.Get("tools"), toolPath+".tools", name, paths)
			}
		case "function":
			if !knownPlaintextCollaborationTool(name, namespace) {
				continue
			}
			message := tool.Get("parameters.properties.message")
			if message.Get("type").String() == "string" && message.Get("encrypted").Type == gjson.True {
				*paths = append(*paths, toolPath)
			}
		}
	}
}

func knownPlaintextCollaborationNamespace(name string) bool {
	return name == "agents" || name == "collaboration" || name == codexOptimizedCollaborationNamespace
}

func knownPlaintextCollaborationTool(name, namespace string) bool {
	if namespace != "" {
		_, ok := codexCollaborationMessageTools[name]
		return ok
	}
	if _, ok := codexCollaborationMessageTools[name]; ok {
		return true
	}
	for _, prefix := range []string{"agents", "collaboration", codexOptimizedCollaborationNamespace} {
		for _, separator := range []string{".", "__"} {
			if suffix, ok := strings.CutPrefix(name, prefix+separator); ok {
				_, known := codexCollaborationMessageTools[suffix]
				return known
			}
		}
	}
	return false
}
