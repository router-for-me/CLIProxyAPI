// Package session derives stable conversation identities and extracts hierarchical session relationships.
package session

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// SessionInfo encapsulates incoming request details needed for session affinity and upstream reporting.
type SessionInfo struct {
	SessionID       string         `json:"session_id"`
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	AgentName       string         `json:"agent_name,omitempty"`
	ClientType      string         `json:"client_type,omitempty"`
	CallerScope     string         `json:"caller_scope,omitempty"`
	AuthID          string         `json:"auth_id,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	Model           string         `json:"model,omitempty"`
	IsFork          bool           `json:"is_fork,omitempty"`
	IsSubagent      bool           `json:"is_subagent,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// SessionTreeInfo is an alias for SessionInfo for backward compatibility.
type SessionTreeInfo = SessionInfo

// ExtractSessionInfo extracts session hierarchy and client identification from request attributes.
// Priority matches selector.go:
//  1. X-Claude-Code-Session-Id
//  2. Claude Code metadata.user_id session
//  3. Session-Id / Session_id (Codex and compatible clients)
//  4. X-Http-Session-Id (Antigravity CLI)
//  5. X-Session-ID / X-Session-Affinity / X-Slot-Session-Id
//  6. X-Conversation-Id / X-Thread-Id / X-Client-Request-Id
//  7. Gemini cachedContent
//  8. OpenAI thread_id
//  9. session_id / sessionId
//  10. prompt_cache_key (pck:), conversation.id (conv:), metadata.user_id (user:)
//  11. conversation_id / chat_id
//  12. execution_session_id metadata
func ExtractSessionInfo(headers http.Header, payload []byte, metadata map[string]any) (SessionInfo, bool) {
	var info SessionInfo
	if metadata != nil {
		if scope, ok := metadata[cliproxyexecutor.CallerScopeMetadataKey].(string); ok {
			info.CallerScope = strings.TrimSpace(scope)
		}
	}

	var root gjson.Result
	var reqRoot gjson.Result
	var hasNestedReq bool
	var parentCandidate string

	if len(payload) > 0 {
		root = util.ParseGJSONBytesNoCopy(payload)
		reqRoot = root
		req := root.Get("request")
		hasNestedReq = req.Exists() && !root.Get("contents").Exists()
		if hasNestedReq {
			reqRoot = req
		}
		for _, p := range []string{
			"parent_session_id", "parentSessionId",
			"parent_thread_id", "parentThreadId",
			"forked_from_thread_id", "forked_from_id",
			"parent_conversation_id", "parentConversationId",
			"metadata.parent_session_id", "metadata.parent_thread_id",
			"metadata.forked_from_thread_id", "metadata.forked_from_id",
			"extra_body.parent_session_id", "extra_body.parent_thread_id",
			"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
		} {
			if val := normalizedSessionCandidate(root.Get(p).String()); val != "" {
				parentCandidate = val
				break
			}
			if hasNestedReq {
				if val := normalizedSessionCandidate(reqRoot.Get(p).String()); val != "" {
					parentCandidate = val
					break
				}
			}
		}
		if parentCandidate == "" {
			parentCandidate = ClaudeMetadataParentSessionID(payload)
		}
	}

	// 1. Anthropic / Claude Code Headers
	if sid := sessionHeaderValue(headers, "X-Claude-Code-Session-Id"); sid != "" {
		info.ClientType = "claude"
		agentID := sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
		if agentID == "" && root.Exists() {
			agentID = normalizedSessionCandidate(root.Get("metadata.agent_id").String())
			if agentID == "" {
				agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
			}
			if agentID == "" && hasNestedReq {
				agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
				if agentID == "" {
					agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
				}
			}
		}
		if agentID == "" {
			_, _, agentID = ClaudeMetadataIdentities(payload)
		}
		parentAgentID := sessionHeaderValue(headers, "X-Claude-Code-Parent-Agent-Id")
		if agentID != "" && agentID != "main" {
			info.AgentName = agentID
			info.ParentSessionID = "claude:" + sid
			if parentAgentID != "" && parentAgentID != "main" && parentAgentID != agentID {
				info.ParentSessionID = "claude:" + sid + ":agent:" + parentAgentID
			} else if parentCandidate != "" && parentCandidate != sid {
				info.ParentSessionID = "claude:" + parentCandidate
			}
			info.SessionID = "claude:" + sid + ":agent:" + agentID
		} else {
			info.AgentName = "main"
			info.SessionID = "claude:" + sid
			if parentCandidate != "" && parentCandidate != sid {
				info.ParentSessionID = "claude:" + parentCandidate
				info.AgentName = "subagent"
			}
		}
		return finalizeSessionInfo(info)
	}

	// 2. Claude Code metadata.user_id in payload (outranks generic headers)
	if len(payload) > 0 {
		if sid, parentSID, agentID := ClaudeMetadataIdentities(payload); sid != "" {
			info.ClientType = "claude"
			if agentID == "" {
				agentID = sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
			}
			if agentID == "" && root.Exists() {
				agentID = normalizedSessionCandidate(root.Get("metadata.agent_id").String())
				if agentID == "" {
					agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
				}
				if agentID == "" && hasNestedReq {
					agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
					if agentID == "" {
						agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
					}
				}
			}
			parentAgentID := sessionHeaderValue(headers, "X-Claude-Code-Parent-Agent-Id")
			if agentID != "" && agentID != "main" {
				info.SessionID = "claude:" + sid + ":agent:" + agentID
				info.ParentSessionID = "claude:" + sid
				if parentAgentID != "" && parentAgentID != "main" && parentAgentID != agentID {
					info.ParentSessionID = "claude:" + sid + ":agent:" + parentAgentID
				} else if parentSID != "" && parentSID != sid {
					info.ParentSessionID = "claude:" + parentSID
				} else if parentCandidate != "" && parentCandidate != sid {
					info.ParentSessionID = "claude:" + parentCandidate
				}
				info.AgentName = agentID
			} else {
				info.SessionID = "claude:" + sid
				if parentSID != "" && parentSID != sid {
					info.ParentSessionID = "claude:" + parentSID
					info.AgentName = "subagent"
				} else if parentCandidate != "" && parentCandidate != sid {
					info.ParentSessionID = "claude:" + parentCandidate
					info.AgentName = "subagent"
				} else {
					info.AgentName = "main"
				}
			}
			return finalizeSessionInfo(info)
		}
	}

	// 3. OpenAI / Codex CLI Headers
	sid := sessionHeaderValue(headers, "Session-Id")
	if sid == "" {
		sid = sessionHeaderValue(headers, "Session_id")
	}
	tid := sessionHeaderValue(headers, "Thread-Id")
	if tid == "" {
		tid = sessionHeaderValue(headers, "Thread_id")
	}

	var codexTurnMeta string
	for k, v := range headers {
		if strings.EqualFold(k, "X-Codex-Turn-Metadata") && len(v) > 0 {
			codexTurnMeta = strings.TrimSpace(v[0])
			break
		}
	}
	var codexTurnMetaJSON gjson.Result
	if codexTurnMeta != "" {
		codexTurnMetaJSON = gjson.Parse(codexTurnMeta)
	}

	if sid == "" && codexTurnMetaJSON.Exists() {
		sid = normalizedSessionCandidate(codexTurnMetaJSON.Get("session_id").String())
	}
	if tid == "" && codexTurnMetaJSON.Exists() {
		tid = normalizedSessionCandidate(codexTurnMetaJSON.Get("thread_id").String())
	}
	if tid == "" && sid != "" && root.Exists() {
		for _, path := range []string{"thread_id", "threadId", "metadata.thread_id"} {
			if tid = normalizedSessionCandidate(root.Get(path).String()); tid != "" {
				break
			}
			if hasNestedReq {
				if tid = normalizedSessionCandidate(reqRoot.Get(path).String()); tid != "" {
					break
				}
			}
		}
	}

	if sid != "" || tid != "" {
		info.ClientType = "codex"
		parentThread := sessionHeaderValue(headers, "x-codex-parent-thread-id")
		if parentThread == "" {
			parentThread = sessionHeaderValue(headers, "X-Codex-Parent-Thread-Id")
		}
		if parentThread == "" && codexTurnMetaJSON.Exists() {
			parentThread = normalizedSessionCandidate(codexTurnMetaJSON.Get("parent_thread_id").String())
		}

		forkedFrom := ""
		if codexTurnMetaJSON.Exists() {
			forkedFrom = normalizedSessionCandidate(codexTurnMetaJSON.Get("forked_from_thread_id").String())
			if forkedFrom == "" {
				forkedFrom = normalizedSessionCandidate(codexTurnMetaJSON.Get("forked_from_id").String())
			}
		}
		if forkedFrom == "" && root.Exists() {
			for _, forkPath := range []string{
				"forked_from_thread_id", "forked_from_id",
				"metadata.forked_from_thread_id", "metadata.forked_from_id",
				"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
			} {
				if forkedFrom = normalizedSessionCandidate(root.Get(forkPath).String()); forkedFrom != "" {
					break
				}
				if hasNestedReq {
					if forkedFrom = normalizedSessionCandidate(reqRoot.Get(forkPath).String()); forkedFrom != "" {
						break
					}
				}
			}
		}

		cleanAgentName := ""
		if codexTurnMetaJSON.Exists() {
			rawName := codexTurnMetaJSON.Get("agent_name").String()
			rawName = strings.TrimPrefix(rawName, "/root/")
			rawName = strings.TrimPrefix(rawName, "/")
			rawName = strings.TrimSpace(rawName)
			rawName = normalizedSessionCandidate(rawName)
			if rawName != "" && rawName != "root" && rawName != "main" {
				cleanAgentName = rawName
			}
		}

		subVal := sessionHeaderValue(headers, "X-Openai-Subagent")
		subagentSignal := subVal != "" && !strings.EqualFold(subVal, "false") && subVal != "0"
		if codexTurnMetaJSON.Exists() && codexTurnMetaJSON.Get("subagent_kind").String() == "thread_spawn" {
			subagentSignal = true
		}

		// 1. Fork detection
		if forkedFrom != "" {
			forkSessionID := tid
			if forkSessionID == "" {
				forkSessionID = sid
			}
			if forkSessionID == forkedFrom && sid != "" && sid != forkedFrom {
				forkSessionID = sid
			}
			info.SessionID = "codex:" + forkSessionID
			info.ParentSessionID = "codex:" + forkedFrom
			info.AgentName = "main"
			info.IsFork = true
			info.IsSubagent = false
			return finalizeSessionInfo(info)
		}

		// 2. Subagent detection (Multi-Agent v2)
		if subagentSignal || (tid != "" && sid != "" && tid != sid) || (parentThread != "" && parentThread != tid && parentThread != sid) {
			childSessionID := tid
			if childSessionID == "" {
				childSessionID = sid
			}
			parentSID := parentThread
			if parentSID == "" {
				parentSID = sid
			}
			if cleanAgentName != "" && sid != "" {
				info.SessionID = "codex:" + sid + ":agent:" + cleanAgentName
				info.AgentName = cleanAgentName
				if parentSID != "" {
					info.ParentSessionID = "codex:" + parentSID
				} else if parentCandidate != "" && parentCandidate != sid {
					info.ParentSessionID = "codex:" + parentCandidate
				}
			} else {
				info.SessionID = "codex:" + childSessionID
				if cleanAgentName != "" {
					info.AgentName = cleanAgentName
				} else {
					info.AgentName = "subagent"
				}
				if parentSID != "" && parentSID != childSessionID {
					info.ParentSessionID = "codex:" + parentSID
				} else if parentCandidate != "" && parentCandidate != childSessionID {
					info.ParentSessionID = "codex:" + parentCandidate
				}
			}
			info.IsSubagent = true
			return finalizeSessionInfo(info)
		}

		// 3. Normal interactive session
		sessionID := sid
		if sessionID == "" {
			sessionID = tid
		}
		info.SessionID = "codex:" + sessionID
		if parentThread != "" && parentThread != sessionID {
			info.ParentSessionID = "codex:" + parentThread
			info.AgentName = "subagent"
			info.IsSubagent = true
		} else if parentCandidate != "" && parentCandidate != sessionID {
			info.ParentSessionID = "codex:" + parentCandidate
			info.AgentName = "subagent"
			info.IsSubagent = true
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}

	// 4. Antigravity CLI (agy) Headers
	if sid := sessionHeaderValue(headers, "X-Http-Session-Id"); sid != "" {
		info.ClientType = "agy"
		info.SessionID = "agy:" + sid
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID != "" && parentSID != sid {
			info.ParentSessionID = "agy:" + parentSID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "agy:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}

	// 5. OpenCode / Pi Slot / Generic Headers
	if sid := sessionHeaderValue(headers, "X-Session-ID"); sid != "" {
		info.ClientType = "generic"
		info.SessionID = "header:" + sid
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID != "" && parentSID != sid {
			info.ParentSessionID = "header:" + parentSID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "header:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Session-Affinity"); sid != "" {
		info.ClientType = "opencode"
		info.SessionID = "affinity:" + sid
		parentAffinity := sessionHeaderValue(headers, "X-Parent-Session-Affinity")
		if parentAffinity == "" {
			parentAffinity = sessionHeaderValue(headers, "X-Parent-Session-ID")
		}
		if parentAffinity != "" && parentAffinity != sid {
			info.ParentSessionID = "affinity:" + parentAffinity
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "affinity:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Slot-Session-Id"); sid != "" {
		info.ClientType = "pi"
		info.SessionID = "slot:" + sid
		parentSID := sessionHeaderValue(headers, "X-Parent-Session-ID")
		if parentSID == "" {
			parentSID = sessionHeaderValue(headers, "X-Parent-Session-Id")
		}
		if parentSID != "" && parentSID != sid {
			info.ParentSessionID = "slot:" + parentSID
			info.AgentName = "subagent"
		} else if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "slot:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "slot"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Conversation-Id"); sid != "" {
		info.ClientType = "conv"
		info.SessionID = "conv:" + sid
		if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "conv:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Conversation-ID"); sid != "" {
		info.ClientType = "conv"
		info.SessionID = "conv:" + sid
		if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "conv:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Thread-Id"); sid != "" {
		info.ClientType = "openai-thread"
		info.SessionID = "thread:" + sid
		if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "thread:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Thread-ID"); sid != "" {
		info.ClientType = "openai-thread"
		info.SessionID = "thread:" + sid
		if parentCandidate != "" && parentCandidate != sid {
			info.ParentSessionID = "thread:" + parentCandidate
			info.AgentName = "subagent"
		} else {
			info.AgentName = "main"
		}
		return finalizeSessionInfo(info)
	}
	if sid := sessionHeaderValue(headers, "X-Client-Request-Id"); sid != "" {
		info.ClientType = "generic"
		info.SessionID = "clientreq:" + sid
		info.AgentName = "main"
		return finalizeSessionInfo(info)
	}

	// 6. Payload inspection
	if len(payload) > 0 && root.Exists() {
		// Gemini context caching
		for _, cachePath := range []string{"cachedContent", "cached_content"} {
			cacheID := normalizedSessionCandidate(root.Get(cachePath).String())
			if cacheID == "" && hasNestedReq {
				cacheID = normalizedSessionCandidate(reqRoot.Get(cachePath).String())
			}
			if cacheID != "" {
				info.ClientType = "gemini"
				info.SessionID = "geminicache:" + cacheID
				if parentCandidate != "" && parentCandidate != cacheID {
					info.ParentSessionID = "geminicache:" + parentCandidate
					info.AgentName = "subagent"
				} else {
					info.AgentName = "main"
				}
				return finalizeSessionInfo(info)
			}
		}

		// OpenAI thread in payload
		for _, threadPath := range []string{"thread_id", "threadId", "metadata.thread_id"} {
			tid := normalizedSessionCandidate(root.Get(threadPath).String())
			if tid == "" && hasNestedReq {
				tid = normalizedSessionCandidate(reqRoot.Get(threadPath).String())
			}
			if tid != "" {
				info.ClientType = "openai-thread"
				info.SessionID = "thread:" + tid
				if parentCandidate != "" && parentCandidate != tid {
					info.ParentSessionID = "thread:" + parentCandidate
					if isBodyForkCandidate(root, reqRoot, hasNestedReq) {
						info.IsFork = true
						info.IsSubagent = false
						info.AgentName = "main"
					} else {
						info.AgentName = "subagent"
						info.IsSubagent = true
					}
				} else {
					info.AgentName = "main"
				}
				return finalizeSessionInfo(info)
			}
		}

		// Generic session in payload
		agentID := normalizedSessionCandidate(root.Get("metadata.agent_id").String())
		if agentID == "" {
			agentID = normalizedSessionCandidate(root.Get("metadata.subagent_id").String())
		}
		if agentID == "" {
			agentID = sessionHeaderValue(headers, "X-Claude-Code-Agent-Id")
		}
		if agentID == "" {
			agentID = sessionHeaderValue(headers, "x-agent-id")
		}
		if agentID == "" && hasNestedReq {
			agentID = normalizedSessionCandidate(reqRoot.Get("metadata.agent_id").String())
			if agentID == "" {
				agentID = normalizedSessionCandidate(reqRoot.Get("metadata.subagent_id").String())
			}
		}

		for _, path := range []string{"session_id", "sessionId", "sessionID", "metadata.session_id", "extra_body.session_id"} {
			sid := normalizedSessionCandidate(root.Get(path).String())
			if sid == "" && hasNestedReq {
				sid = normalizedSessionCandidate(reqRoot.Get(path).String())
			}
			if sid != "" {
				info.ClientType = "generic"
				if agentID != "" && agentID != "main" {
					info.SessionID = "session:" + sid + ":agent:" + agentID
					info.ParentSessionID = "session:" + sid
					if parentCandidate != "" && parentCandidate != sid {
						info.ParentSessionID = "session:" + parentCandidate
					}
					info.AgentName = agentID
				} else {
					info.SessionID = "session:" + sid
					if parentCandidate != "" && parentCandidate != sid {
						info.ParentSessionID = "session:" + parentCandidate
						if isBodyForkCandidate(root, reqRoot, hasNestedReq) {
							info.IsFork = true
							info.IsSubagent = false
							info.AgentName = "main"
						} else {
							info.AgentName = "subagent"
							info.IsSubagent = true
						}
					} else {
						info.AgentName = "main"
					}
				}
				return finalizeSessionInfo(info)
			}
		}

		// Prompt cache key & Conversation object
		var conversationID string
		conversation := root.Get("conversation")
		if !conversation.Exists() && hasNestedReq {
			conversation = reqRoot.Get("conversation")
		}
		if sid := normalizedSessionCandidate(conversation.Get("id").String()); sid != "" {
			conversationID = "conv:" + sid
		} else if conversation.Type == gjson.String {
			if sid := normalizedSessionCandidate(conversation.String()); sid != "" {
				conversationID = "conv:" + sid
			}
		}
		pck := normalizedSessionCandidate(root.Get("prompt_cache_key").String())
		if pck == "" {
			pck = normalizedSessionCandidate(root.Get("promptCacheKey").String())
		}
		if pck == "" && hasNestedReq {
			pck = normalizedSessionCandidate(reqRoot.Get("prompt_cache_key").String())
			if pck == "" {
				pck = normalizedSessionCandidate(reqRoot.Get("promptCacheKey").String())
			}
		}
		if pck != "" {
			info.ClientType = "generic"
			info.SessionID = "pck:" + pck
			if conversationID != "" {
				info.ParentSessionID = conversationID
				info.AgentName = "subagent"
			} else {
				info.AgentName = "main"
			}
			return finalizeSessionInfo(info)
		}
		if conversationID != "" {
			info.ClientType = "conv"
			info.SessionID = conversationID
			if parentCandidate != "" && ("conv:"+parentCandidate) != conversationID {
				info.ParentSessionID = "conv:" + parentCandidate
				info.AgentName = "subagent"
			} else {
				info.AgentName = "main"
			}
			return finalizeSessionInfo(info)
		}

		// Plain metadata.user_id
		userID := normalizedSessionCandidate(root.Get("metadata.user_id").String())
		if userID == "" && hasNestedReq {
			userID = normalizedSessionCandidate(reqRoot.Get("metadata.user_id").String())
		}
		if userID != "" {
			info.ClientType = "generic"
			info.SessionID = "user:" + userID
			info.AgentName = "main"
			return finalizeSessionInfo(info)
		}

		// Legacy conversation string paths
		for _, convPath := range []string{"conversation_id", "conversationId", "chat_id", "chatId", "metadata.conversation_id", "extra_body.conversation_id"} {
			cid := normalizedSessionCandidate(root.Get(convPath).String())
			if cid == "" && hasNestedReq {
				cid = normalizedSessionCandidate(reqRoot.Get(convPath).String())
			}
			if cid != "" {
				info.ClientType = "conv"
				info.SessionID = "conv:" + cid
				if parentCandidate != "" && ("conv:"+parentCandidate) != ("conv:"+cid) {
					info.ParentSessionID = "conv:" + parentCandidate
					info.AgentName = "subagent"
				} else {
					info.AgentName = "main"
				}
				return finalizeSessionInfo(info)
			}
		}
	}

	// 7. ExecutionSessionMetadataKey
	if executionID, ok := metadata[cliproxyexecutor.ExecutionSessionMetadataKey].(string); ok {
		if executionID = normalizedSessionCandidate(executionID); executionID != "" {
			info.ClientType = "generic"
			info.SessionID = "execution:" + executionID
			info.AgentName = "main"
			return finalizeSessionInfo(info)
		}
	}

	return SessionInfo{}, false
}

func isBodyForkCandidate(root, reqRoot gjson.Result, hasNestedReq bool) bool {
	if !root.Exists() {
		return false
	}
	for _, k := range []string{
		"forked_from_thread_id", "forked_from_id",
		"metadata.forked_from_thread_id", "metadata.forked_from_id",
		"extra_body.forked_from_thread_id", "extra_body.forked_from_id",
	} {
		if val := normalizedSessionCandidate(root.Get(k).String()); val != "" {
			return true
		}
		if hasNestedReq {
			if val := normalizedSessionCandidate(reqRoot.Get(k).String()); val != "" {
				return true
			}
		}
	}
	return false
}

func finalizeSessionInfo(info SessionInfo) (SessionInfo, bool) {
	if info.SessionID == "" {
		return SessionInfo{}, false
	}
	if info.AgentName == "" {
		info.AgentName = "main"
	}
	if info.ClientType == "" {
		info.ClientType = "generic"
	}
	// Self-referential loop protection
	if info.ParentSessionID == info.SessionID {
		info.ParentSessionID = ""
	}
	return info, true
}

// ExtractTreeInfo is an alias for ExtractSessionInfo for backward compatibility.
func ExtractTreeInfo(headers http.Header, payload []byte, metadata map[string]any) (SessionInfo, bool) {
	return ExtractSessionInfo(headers, payload, metadata)
}

func sessionHeaderValue(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := normalizedSessionCandidate(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if normalized := normalizedSessionCandidate(value); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func normalizedSessionCandidate(raw string) string {
	return NormalizeExplicitID(raw)
}
