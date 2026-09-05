package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeFoldAnchorText is appended after a trailing tool_result when a user
// turn would otherwise end in a tool_result block. Some Anthropic-compatible
// upstreams (MiniMax) extend the prompt cache only up to the last TEXT block
// of the last user message and ignore cache_control on tool_result blocks and
// on mid-conversation role=system turns. The anchor is deterministic on every
// such user turn so the prefix stays byte-stable across turns. It is
// deliberately short and non-instructional.
const claudeFoldAnchorText = "."

// foldMidSystemMessagesIntoUserTurns rewrites a Claude Messages payload for
// upstreams that drop mid-conversation {"role":"system"} turns:
//
//  1. every role=system turn's text blocks are appended, in order, to the
//     immediately preceding user turn (string content is promoted to a text
//     block); when the preceding turn is not a user turn, a user turn holding
//     the text is inserted in place;
//  2. afterwards, every user turn whose last content block is still a
//     tool_result gets the anchor text block appended; a cache_control carried
//     by that tool_result moves onto the anchor.
//
// Moved text blocks keep their own cache_control, so a marker the caller put on
// the trailing system turn ends up on the last text block of the last user
// message — the position these upstreams honor.
//
// The fold fails closed (original payload, false) when the payload is not
// valid JSON, messages is not an array, a system turn carries anything but
// non-empty text (tool_addition / tool_removal / output_config / empty), a
// system turn directly follows an assistant turn that issued a tool_use (a
// synthetic user turn there would break the tool_result protocol), or any JSON
// edit fails. Payloads with nothing to fold are returned unchanged with false.
func foldMidSystemMessagesIntoUserTurns(payload []byte) ([]byte, bool) {
	if !gjson.ValidBytes(payload) {
		return payload, false
	}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, false
	}
	type outMsg struct {
		raw        string
		isUser     bool
		hasToolUse bool
	}
	var out []outMsg
	foldable, changed := true, false
	messages.ForEach(func(_, message gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
		switch role {
		case "system":
			parts, ok := claudeFoldSystemTextParts(message.Get("content"))
			if !ok {
				foldable = false
				return false
			}
			if n := len(out); n > 0 && out[n-1].isUser {
				merged, okAppend := appendTextBlocksToUserMessage(out[n-1].raw, parts)
				if !okAppend {
					foldable = false
					return false
				}
				out[n-1].raw = merged
			} else {
				if n := len(out); n > 0 && out[n-1].hasToolUse {
					foldable = false
					return false
				}
				out = append(out, outMsg{raw: `{"role":"user","content":` + string(rawJSONArray(parts)) + `}`, isUser: true})
			}
			changed = true
		case "user":
			out = append(out, outMsg{raw: message.Raw, isUser: true})
		default:
			out = append(out, outMsg{raw: message.Raw, hasToolUse: messageHasBlockType(message, "tool_use")})
		}
		return true
	})
	if !foldable {
		return payload, false
	}
	raws := make([]string, 0, len(out))
	for _, m := range out {
		raw := m.raw
		if m.isUser {
			anchored, did, ok := anchorTrailingToolResult(raw)
			if !ok {
				return payload, false
			}
			if did {
				raw, changed = anchored, true
			}
		}
		raws = append(raws, raw)
	}
	if !changed {
		return payload, false
	}
	updated, err := sjson.SetRawBytes(payload, "messages", rawJSONArray(raws))
	if err != nil {
		return payload, false
	}
	return updated, true
}

// claudeFoldSystemTextParts returns the text blocks of a system turn, or
// ok=false when the content is empty or holds anything other than non-empty
// strings / text blocks (message-level controls must not be folded away).
func claudeFoldSystemTextParts(content gjson.Result) ([]string, bool) {
	if !content.Exists() {
		return nil, false
	}
	if content.Type == gjson.String {
		if strings.TrimSpace(content.String()) == "" {
			return nil, false
		}
		block := []byte(`{"type":"text","text":""}`)
		block, _ = sjson.SetBytes(block, "text", content.String())
		return []string{string(block)}, true
	}
	if !content.IsArray() {
		return nil, false
	}
	var parts []string
	ok := true
	content.ForEach(func(_, item gjson.Result) bool {
		switch {
		case item.Type == gjson.String && strings.TrimSpace(item.String()) != "":
			block := []byte(`{"type":"text","text":""}`)
			block, _ = sjson.SetBytes(block, "text", item.String())
			parts = append(parts, string(block))
		case item.IsObject() && item.Get("type").String() == "text" && strings.TrimSpace(item.Get("text").String()) != "":
			parts = append(parts, item.Raw)
		default:
			ok = false
			return false
		}
		return true
	})
	if !ok || len(parts) == 0 {
		return nil, false
	}
	return parts, true
}

func messageHasBlockType(message gjson.Result, blockType string) bool {
	found := false
	content := message.Get("content")
	if !content.IsArray() {
		return false
	}
	content.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == blockType {
			found = true
			return false
		}
		return true
	})
	return found
}

// appendTextBlocksToUserMessage appends raw text blocks to a user message's
// content, promoting string content to a single text block first. ok=false
// when the content has an unsupported shape or the edit fails.
func appendTextBlocksToUserMessage(rawMessage string, parts []string) (string, bool) {
	content := gjson.Get(rawMessage, "content")
	var blocks []string
	switch {
	case content.IsArray():
		content.ForEach(func(_, item gjson.Result) bool {
			blocks = append(blocks, item.Raw)
			return true
		})
	case content.Type == gjson.String:
		block := []byte(`{"type":"text","text":""}`)
		block, _ = sjson.SetBytes(block, "text", content.String())
		blocks = append(blocks, string(block))
	default:
		return rawMessage, false
	}
	blocks = append(blocks, parts...)
	updated, err := sjson.SetRaw(rawMessage, "content", string(rawJSONArray(blocks)))
	if err != nil {
		return rawMessage, false
	}
	return updated, true
}

// anchorTrailingToolResult appends the anchor text block when the user message
// ends in a tool_result block, moving that block's cache_control (if any) onto
// the anchor. Returns (message, changed, ok); ok=false means an edit failed and
// the caller must abandon the fold.
func anchorTrailingToolResult(rawMessage string) (string, bool, bool) {
	content := gjson.Get(rawMessage, "content")
	if !content.IsArray() {
		return rawMessage, false, true
	}
	items := content.Array()
	if len(items) == 0 {
		return rawMessage, false, true
	}
	last := items[len(items)-1]
	if last.Get("type").String() != "tool_result" {
		return rawMessage, false, true
	}
	anchor := []byte(`{"type":"text","text":""}`)
	anchor, _ = sjson.SetBytes(anchor, "text", claudeFoldAnchorText)
	updated := rawMessage
	if cc := last.Get("cache_control"); cc.Exists() {
		var err error
		anchor, err = sjson.SetRawBytes(anchor, "cache_control", []byte(cc.Raw))
		if err != nil {
			return rawMessage, false, false
		}
		updated, err = sjson.Delete(updated, "content."+strconv.Itoa(len(items)-1)+".cache_control")
		if err != nil {
			return rawMessage, false, false
		}
	}
	appended, err := sjson.SetRaw(updated, "content.-1", string(anchor))
	if err != nil {
		return rawMessage, false, false
	}
	return appended, true, true
}

// logFoldApplied emits one line per folded request so worker transcripts
// (session id) can be joined to the credential cohort (hashed key) during a
// staged rollout.
func logFoldApplied(payload []byte, apiKey string) {
	userID := gjson.GetBytes(payload, "metadata.user_id").String()
	sessionID := userID
	if parsed := gjson.Parse(userID); parsed.IsObject() {
		sessionID = parsed.Get("session_id").String()
	} else if idx := strings.LastIndex(userID, "_session_"); idx >= 0 {
		sessionID = userID[idx+len("_session_"):]
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	log.Infof("[fold-mid-system] applied session=%s auth=%s", sessionID, hex.EncodeToString(sum[:])[:8])
}
