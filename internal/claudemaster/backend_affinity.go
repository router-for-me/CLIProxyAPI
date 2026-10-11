package claudemaster

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

type nativeRequestAffinity struct {
	requiresAccount bool
	validJSON       bool
}

type nativeContinuationScan struct {
	requiresAccount   bool
	serverToolUses    map[string]struct{}
	serverToolResults map[string]struct{}
}

// nativeRequestRequiresAccountAffinity reports whether a Messages request
// carries opaque or stateful continuation data. The values are never decoded
// or modified; their presence only prevents the selector from moving the turn
// to another subscription.
func nativeRequestRequiresAccountAffinity(body []byte) bool {
	return analyzeNativeRequestAffinity(body).requiresAccount
}

func analyzeNativeRequestAffinity(body []byte) nativeRequestAffinity {
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&request) != nil || request == nil {
		return nativeRequestAffinity{}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nativeRequestAffinity{}
	}
	// Fallback credits are redeemable only by the organization/workspace that
	// received them, even when the rest of the retry is self-contained.
	if valuePresent(request["container"]) || valuePresent(request["fallback_credit_token"]) {
		return nativeRequestAffinity{requiresAccount: true, validJSON: true}
	}
	scan := nativeContinuationScan{}
	for key, value := range request {
		// Tool definitions and their JSON schemas are caller-controlled data. A
		// property named file_id, signature, or encrypted_content there does not
		// represent live provider state.
		if key == "tools" {
			continue
		}
		scan.continuationValue(value)
	}
	if scan.requiresAccount || scan.hasUnfinishedServerTool() {
		return nativeRequestAffinity{requiresAccount: true, validJSON: true}
	}
	return nativeRequestAffinity{validJSON: true}
}

func (s *nativeContinuationScan) continuationValue(value any) {
	if s.requiresAccount {
		return
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			s.continuationValue(item)
		}
	case map[string]any:
		blockType, _ := typed["type"].(string)
		blockType = strings.ToLower(strings.TrimSpace(blockType))
		// Client-executed tool_use/tool_result blocks are ordinary request
		// history and can cross accounts. A completed server-tool exchange is
		// also self-contained unless one of its blocks carries opaque state;
		// only an unmatched server_tool_use represents work still held upstream.
		if blockType == "server_tool_use" {
			id, _ := typed["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				s.requiresAccount = true
				return
			}
			if s.serverToolUses == nil {
				s.serverToolUses = make(map[string]struct{})
			}
			s.serverToolUses[id] = struct{}{}
		}
		if strings.HasSuffix(blockType, "_tool_result") && blockType != "tool_result" {
			id, _ := typed["tool_use_id"].(string)
			id = strings.TrimSpace(id)
			if id != "" {
				if s.serverToolResults == nil {
					s.serverToolResults = make(map[string]struct{})
				}
				s.serverToolResults[id] = struct{}{}
			}
		}
		if blockType == "container_upload" || blockType == "pause_turn" {
			s.requiresAccount = true
			return
		}
		if stopReason, _ := typed["stop_reason"].(string); strings.EqualFold(strings.TrimSpace(stopReason), "pause_turn") {
			s.requiresAccount = true
			return
		}
		// Encrypted provider payloads can be nested inside server-tool result
		// blocks. Never inspect or rewrite them; presence alone pins the turn.
		for _, key := range []string{"thought_signature", "encrypted_content", "encrypted_stdout", "encrypted_stderr", "encrypted_index"} {
			if valuePresent(typed[key]) {
				s.requiresAccount = true
				return
			}
		}
		if valuePresent(typed["file_id"]) {
			s.requiresAccount = true
			return
		}
		// A thinking block's signature does not pin the account. Verified 2026-10-09 on this
		// pool's own subscriptions with the thinking-binding-controls beta: an Opus 5.5 block
		// minted on one subscription replays on another with input_transformations [] even
		// in "error" mode; a Sonnet 5.5 block is dropped by the API (end_user_binding_mismatch)
		// and the request still succeeds. redacted_thinking is the same encrypted reasoning
		// (its data plays the role of the signature). A signed compaction block is different:
		// it stands in for the whole history, so losing it would answer with no context.
		if blockType == "compaction" && valuePresent(typed["signature"]) {
			s.requiresAccount = true
			return
		}
		for key, child := range typed {
			// input belongs to a client-defined tool schema. A field named
			// "signature" there has no provider affinity semantics. Server-tool
			// input is provider-defined, so account-bound file references in it
			// still need to be detected.
			if key == "text" || key == "input" && blockType == "tool_use" {
				continue
			}
			s.continuationValue(child)
		}
	}
}

func (s *nativeContinuationScan) hasUnfinishedServerTool() bool {
	for id := range s.serverToolUses {
		if _, complete := s.serverToolResults[id]; !complete {
			return true
		}
	}
	return false
}

func valuePresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	default:
		return true
	}
}
