package common

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// isValidCacheControl checks if the cache_control object has an exact valid "type" of "ephemeral".
func isValidCacheControl(cc ojson.Result) bool {
	if !cc.Exists() || cc.Type == ojson.Null || !cc.IsObject() {
		return false
	}
	typ := cc.Get("type")
	return typ.Type == ojson.String && typ.String() == "ephemeral"
}

func claudeBlockCanHostCacheControl(block ojson.Result) bool {
	if !block.IsObject() {
		return false
	}
	switch block.Get("type").String() {
	case "":
		return true
	case "thinking", "redacted_thinking":
		return false
	case "text":
		return strings.TrimSpace(block.Get("text").String()) != ""
	default:
		return true
	}
}

// AttachCacheControl copies a Claude-compatible cache_control object from src onto dst.
// Returns dst unchanged when cache_control is missing, not an object, or dst is an empty text block.
func AttachCacheControl(dst []byte, src ojson.Result) []byte {
	cc := src.Get("cache_control")
	if !isValidCacheControl(cc) || !claudeBlockCanHostCacheControl(util.ParseGJSONBytesNoCopy(dst)) {
		return dst
	}
	out, err := ojson.SetRawBytes(dst, "cache_control", []byte(cc.Raw))
	if err != nil {
		return dst
	}
	return out
}

// AttachMessageCacheControl applies message-level cache_control onto the last eligible non-empty content block.
// Part-level cache_control wins when the target block already has one.
// String content is promoted to a content array so Claude can accept cache_control.
func AttachMessageCacheControl(msg []byte, src ojson.Result) []byte {
	cc := src.Get("cache_control")
	if !isValidCacheControl(cc) {
		return msg
	}

	content := ojson.GetBytesNoCopy(msg, "content")
	if content.IsArray() {
		arr := content.Array()
		targetIdx := -1
		for i := len(arr) - 1; i >= 0; i-- {
			if claudeBlockCanHostCacheControl(arr[i]) {
				targetIdx = i
				break
			}
		}
		if targetIdx < 0 {
			return msg
		}
		if arr[targetIdx].Get("cache_control").Exists() {
			return msg
		}
		path := fmt.Sprintf("content.%d.cache_control", targetIdx)
		out, err := ojson.SetRawBytes(msg, path, []byte(cc.Raw))
		if err != nil {
			return msg
		}
		return out
	}

	if content.Type != ojson.String || strings.TrimSpace(content.String()) == "" {
		return msg
	}

	textPartDoc := ojson.NewDoc([]byte(`{"type":"text","text":""}`))
	_ = textPartDoc.Set("text", content.String())
	if errSet := textPartDoc.SetRaw("cache_control", cc.Raw); errSet != nil {
		return msg
	}
	outDoc := ojson.NewDoc(msg)
	if errSetContent := outDoc.SetRaw("content", "[]"); errSetContent != nil {
		return msg
	}
	_ = outDoc.SetRawBytes("content.-1", textPartDoc.Bytes())
	return outDoc.Bytes()
}

// AttachToolMessageCacheControl hoists part-level or message-level cache_control
// onto the tool_result content block in msg.
// Part-level cache_control from src content takes precedence over message-level cache_control.
func AttachToolMessageCacheControl(msg []byte, src ojson.Result) []byte {
	rawCC := extractFirstPartCacheControl(src)
	if rawCC == "" {
		if cc := src.Get("cache_control"); isValidCacheControl(cc) {
			rawCC = cc.Raw
		}
	}
	if rawCC == "" {
		return msg
	}

	content := ojson.GetBytesNoCopy(msg, "content")
	if content.IsArray() {
		arr := content.Array()
		if len(arr) == 0 {
			return msg
		}
		targetIdx := -1
		for i, block := range arr {
			if block.Get("type").String() == "tool_result" {
				targetIdx = i
				break
			}
		}
		if targetIdx == -1 {
			return msg
		}
		path := fmt.Sprintf("content.%d.cache_control", targetIdx)
		out, err := ojson.SetRawBytes(msg, path, []byte(rawCC))
		if err == nil {
			return out
		}
	}
	return msg
}

func extractFirstPartCacheControl(src ojson.Result) string {
	content := src.Get("content")
	if !content.Exists() {
		content = src
	}
	if content.IsArray() {
		var raw string
		content.ForEach(func(_, part ojson.Result) bool {
			if cc := part.Get("cache_control"); isValidCacheControl(cc) {
				raw = cc.Raw
				return false
			}
			return true
		})
		return raw
	}
	if content.IsObject() {
		if cc := content.Get("cache_control"); isValidCacheControl(cc) {
			return cc.Raw
		}
	}
	return ""
}
