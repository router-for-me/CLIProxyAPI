package common

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// isValidCacheControl checks if the cache_control object has an exact valid "type" of "ephemeral".
func isValidCacheControl(cc gjson.Result) bool {
	if !cc.Exists() || cc.Type == gjson.Null || !cc.IsObject() {
		return false
	}
	typ := cc.Get("type")
	return typ.Type == gjson.String && typ.String() == "ephemeral"
}

// AttachCacheControl copies a Claude-compatible cache_control object from src onto dst.
// Returns dst unchanged when cache_control is missing or not an object.
func AttachCacheControl(dst []byte, src gjson.Result) []byte {
	cc := src.Get("cache_control")
	if !isValidCacheControl(cc) {
		return dst
	}
	out, err := sjson.SetRawBytes(dst, "cache_control", []byte(cc.Raw))
	if err != nil {
		return dst
	}
	return out
}

// AttachMessageCacheControl applies message-level cache_control onto the last content block.
// Part-level cache_control wins when the last block already has one.
// String content is promoted to a content array so Claude can accept cache_control.
func AttachMessageCacheControl(msg []byte, src gjson.Result) []byte {
	cc := src.Get("cache_control")
	if !isValidCacheControl(cc) {
		return msg
	}

	content := gjson.GetBytes(msg, "content")
	if content.IsArray() {
		arr := content.Array()
		if len(arr) == 0 {
			return msg
		}
		lastIdx := len(arr) - 1
		if arr[lastIdx].Get("cache_control").Exists() {
			return msg
		}
		path := fmt.Sprintf("content.%d.cache_control", lastIdx)
		out, err := sjson.SetRawBytes(msg, path, []byte(cc.Raw))
		if err != nil {
			return msg
		}
		return out
	}

	if content.Type != gjson.String {
		return msg
	}

	textPart := []byte(`{"type":"text","text":""}`)
	textPart, _ = sjson.SetBytes(textPart, "text", content.String())
	textPart, errSet := sjson.SetRawBytes(textPart, "cache_control", []byte(cc.Raw))
	if errSet != nil {
		return msg
	}
	out, err := sjson.SetRawBytes(msg, "content", []byte("[]"))
	if err != nil {
		return msg
	}
	out, _ = sjson.SetRawBytes(out, "content.-1", textPart)
	return out
}

// AttachToolMessageCacheControl hoists part-level or message-level cache_control
// onto the tool_result content block in msg.
// Part-level cache_control from src content takes precedence over message-level cache_control.
func AttachToolMessageCacheControl(msg []byte, src gjson.Result) []byte {
	rawCC := extractFirstPartCacheControl(src)
	if rawCC == "" {
		if cc := src.Get("cache_control"); isValidCacheControl(cc) {
			rawCC = cc.Raw
		}
	}
	if rawCC == "" {
		return msg
	}

	content := gjson.GetBytes(msg, "content")
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
		out, err := sjson.SetRawBytes(msg, path, []byte(rawCC))
		if err == nil {
			return out
		}
	}
	return msg
}

func extractFirstPartCacheControl(src gjson.Result) string {
	content := src.Get("content")
	if !content.Exists() {
		content = src
	}
	if content.IsArray() {
		var raw string
		content.ForEach(func(_, part gjson.Result) bool {
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
