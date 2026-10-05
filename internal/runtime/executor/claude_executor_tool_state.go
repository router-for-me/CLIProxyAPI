package executor

import (
	"net/http"
	"sync"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const claudeOAuthToolAliasStateLimit = 1024

// claudeOAuthToolAliasStoreInitMu guards lazy allocation for executors built
// without a constructor. It lives outside ClaudeExecutor because ForAPIKey
// copies the executor by value, and a mutex field would be copied with it.
var claudeOAuthToolAliasStoreInitMu sync.Mutex

type claudeOAuthToolAliasStore struct {
	mu      sync.Mutex
	entries map[string]map[string]string
	order   []string
}

func (s *claudeOAuthToolAliasStore) load(keys []string) (map[string]string, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		aliases, ok := s.entries[key]
		if !ok {
			continue
		}
		return cloneClaudeOAuthToolAliasMap(aliases), true
	}
	return nil, false
}

func (s *claudeOAuthToolAliasStore) save(keys []string, aliases map[string]string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]map[string]string)
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, exists := s.entries[key]; !exists {
			s.order = append(s.order, key)
		}
		s.entries[key] = cloneClaudeOAuthToolAliasMap(aliases)
	}
	for len(s.order) > claudeOAuthToolAliasStateLimit {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
}

func cloneClaudeOAuthToolAliasMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func (e *ClaudeExecutor) claudeOAuthToolAliasStore() *claudeOAuthToolAliasStore {
	claudeOAuthToolAliasStoreInitMu.Lock()
	defer claudeOAuthToolAliasStoreInitMu.Unlock()
	if e.oauthToolAliases == nil {
		e.oauthToolAliases = &claudeOAuthToolAliasStore{}
	}
	return e.oauthToolAliases
}

func claudeOAuthToolAliasKeys(payload []byte, messageID string) []string {
	keys := make([]string, 0, 2)
	if previousMessageID := gjson.GetBytes(payload, "thread.previous_message_id").String(); previousMessageID != "" {
		keys = append(keys, "message:"+previousMessageID)
	}
	if messageID != "" {
		keys = append(keys, "message:"+messageID)
	}
	return keys
}

func claudeThreadContinuationNeedsAliasState(payload []byte) bool {
	if gjson.GetBytes(payload, "thread.type").String() != "continue" {
		return false
	}
	previousMessageID := gjson.GetBytes(payload, "thread.previous_message_id")
	if !previousMessageID.Exists() || previousMessageID.String() == "" {
		return false
	}
	tools := gjson.GetBytes(payload, "tools")
	return !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0
}

func (e *ClaudeExecutor) prepareClaudeOAuthToolNamesForRequest(payload []byte, options claudeMCPAliasOptions) ([]byte, map[string]string, error) {
	if claudeThreadContinuationNeedsAliasState(payload) {
		aliases, ok := e.claudeOAuthToolAliasStore().load(claudeOAuthToolAliasKeys(payload, ""))
		if !ok {
			return nil, nil, newClaudeThreadNotFoundError()
		}
		return payload, aliases, nil
	}
	remapped, aliases := prepareClaudeOAuthToolNamesForUpstream(payload, options)
	return remapped, aliases, nil
}

func (e *ClaudeExecutor) rememberClaudeOAuthToolAliases(payload []byte, aliases map[string]string, messageID string) {
	if gjson.GetBytes(payload, "thread.type").String() == "" {
		return
	}
	keys := claudeOAuthToolAliasKeys(payload, messageID)
	if len(keys) == 0 {
		return
	}
	e.claudeOAuthToolAliasStore().save(keys, aliases)
}

type claudeThreadNotFoundError struct {
	statusErr
}

func newClaudeThreadNotFoundError() claudeThreadNotFoundError {
	return claudeThreadNotFoundError{statusErr: statusErr{
		code: http.StatusNotFound,
		msg:  "No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread.",
	}}
}

func (claudeThreadNotFoundError) IsRequestScoped() bool { return true }

var _ cliproxyexecutor.RequestScopedError = claudeThreadNotFoundError{}
