package auth

import (
	"encoding/json"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var responseModelAliasPaths = []string{
	"message.model",
	"model",
	"modelVersion",
	"response.model",
	"response.modelVersion",
}

func (m *Manager) requestedResponseModelAlias(auth *Auth, opts cliproxyexecutor.Options, routeModel, upstreamModel string) string {
	requestedModel := strings.TrimSpace(requestedModelAliasFromOptions(opts, routeModel))
	upstreamModel = strings.TrimSpace(upstreamModel)
	if m == nil || auth == nil || requestedModel == "" || upstreamModel == "" {
		return ""
	}
	if canonicalModelKey(requestedModel) == canonicalModelKey(upstreamModel) {
		return ""
	}
	if !m.shouldEchoRequestedModelAlias(auth, requestedModel, upstreamModel) {
		return ""
	}
	return requestedModel
}

func (m *Manager) shouldEchoRequestedModelAlias(auth *Auth, requestedModel, upstreamModel string) bool {
	requestedModel = strings.TrimSpace(requestedModel)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if m == nil || auth == nil || requestedModel == "" || upstreamModel == "" {
		return false
	}
	if modelAliasPoolContains(m.resolveOAuthUpstreamModelPool(auth, requestedModel), upstreamModel) {
		return true
	}
	if resolved := strings.TrimSpace(m.applyOAuthModelAlias(auth, requestedModel)); explicitAliasMatchesUpstream(requestedModel, resolved, upstreamModel) {
		return true
	}
	if modelAliasPoolContains(m.resolveAPIKeyUpstreamModelPool(auth, requestedModel), upstreamModel) {
		return true
	}
	if resolved := strings.TrimSpace(m.lookupAPIKeyUpstreamModel(auth.ID, requestedModel)); explicitAliasMatchesUpstream(requestedModel, resolved, upstreamModel) {
		return true
	}
	return false
}

func explicitAliasMatchesUpstream(requestedModel, resolvedModel, upstreamModel string) bool {
	requestedKey := canonicalModelKey(requestedModel)
	resolvedKey := canonicalModelKey(resolvedModel)
	upstreamKey := canonicalModelKey(upstreamModel)
	if requestedKey == "" || resolvedKey == "" || upstreamKey == "" {
		return false
	}
	if requestedKey == resolvedKey {
		return false
	}
	return resolvedKey == upstreamKey
}

func modelAliasPoolContains(pool []string, upstreamModel string) bool {
	upstreamKey := canonicalModelKey(upstreamModel)
	if upstreamKey == "" {
		return false
	}
	for _, candidate := range pool {
		if canonicalModelKey(candidate) == upstreamKey {
			return true
		}
	}
	return false
}

func rewriteResponsePayloadModelAlias(payload []byte, alias string) []byte {
	alias = strings.TrimSpace(alias)
	if alias == "" || len(payload) == 0 {
		return payload
	}
	if rewritten, changed := rewriteResponseJSONModelAlias(payload, alias); changed {
		return rewritten
	}
	if rewritten, changed := rewriteResponseSSEModelAlias(payload, alias); changed {
		return rewritten
	}
	return payload
}

func rewriteResponseJSONModelAlias(payload []byte, alias string) ([]byte, bool) {
	if !json.Valid(payload) {
		return nil, false
	}
	rewritten := payload
	changed := false
	for _, path := range responseModelAliasPaths {
		value := gjson.GetBytes(rewritten, path)
		if !value.Exists() || value.Type != gjson.String {
			continue
		}
		if strings.TrimSpace(value.String()) == "" || strings.EqualFold(strings.TrimSpace(value.String()), alias) {
			continue
		}
		updated, err := sjson.SetBytes(rewritten, path, alias)
		if err != nil {
			continue
		}
		rewritten = updated
		changed = true
	}
	return rewritten, changed
}

func rewriteResponseSSEModelAlias(payload []byte, alias string) ([]byte, bool) {
	lines := strings.Split(string(payload), "\n")
	changed := false
	for i, line := range lines {
		trimmedLine := strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(trimmedLine, "data:") {
			continue
		}
		rest := trimmedLine[len("data:"):]
		spacePrefixLen := 0
		for spacePrefixLen < len(rest) && (rest[spacePrefixLen] == ' ' || rest[spacePrefixLen] == '\t') {
			spacePrefixLen++
		}
		content := strings.TrimSpace(rest)
		if content == "" || strings.EqualFold(content, "[DONE]") {
			continue
		}
		rewrittenJSON, ok := rewriteResponseJSONModelAlias([]byte(content), alias)
		if !ok {
			continue
		}
		rebuilt := "data:" + rest[:spacePrefixLen] + string(rewrittenJSON)
		if strings.HasSuffix(line, "\r") {
			rebuilt += "\r"
		}
		lines[i] = rebuilt
		changed = true
	}
	if !changed {
		return nil, false
	}
	return []byte(strings.Join(lines, "\n")), true
}
