package common

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/tidwall/gjson"
)

// DeriveClaudeUserID returns a stable value for the Claude request field
// metadata.user_id. It preserves any caller-supplied metadata.user_id or
// OpenAI Chat Completions user field, then derives a deterministic value from
// stable client signals (prompt_cache_key, session_id, conversation_id, first user
// message content, and model/system instructions). The same conversation therefore gets
// the same user_id on every worker and every turn, while different
// conversations get different values.
func DeriveClaudeUserID(rawJSON []byte) string {
	root := gjson.ParseBytes(rawJSON)

	if v := root.Get("metadata.user_id"); v.Exists() && v.Type == gjson.String {
		if raw := v.String(); strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	if v := root.Get("user"); v.Exists() && v.Type == gjson.String {
		if raw := v.String(); strings.TrimSpace(raw) != "" {
			return raw
		}
	}

	var seed strings.Builder

	if v := root.Get("prompt_cache_key"); v.Exists() {
		if value := strings.TrimSpace(v.String()); value != "" {
			seed.WriteString("prompt_cache_key:")
			seed.WriteString(value)
		}
	}

	if seed.Len() == 0 {
		for _, path := range []string{"session_id", "sessionId"} {
			if v := root.Get(path); v.Exists() {
				if value := strings.TrimSpace(v.String()); value != "" {
					seed.WriteString("session_id:")
					seed.WriteString(value)
					break
				}
			}
		}
	}

	if seed.Len() == 0 {
		conversation := root.Get("conversation")
		if sid := strings.TrimSpace(conversation.Get("id").String()); sid != "" {
			seed.WriteString("conversation_id:")
			seed.WriteString(sid)
		} else if conversation.Type == gjson.String {
			if sid := strings.TrimSpace(conversation.String()); sid != "" {
				seed.WriteString("conversation_id:")
				seed.WriteString(sid)
			}
		} else if v := root.Get("conversation_id"); v.Exists() {
			if sid := strings.TrimSpace(v.String()); sid != "" {
				seed.WriteString("conversation_id:")
				seed.WriteString(sid)
			}
		}
	}

	if seed.Len() == 0 {
		if content := firstStableRequestContent(root); content != "" {
			seed.WriteString("content:")
			seed.WriteString(content)
		}
	}

	if seed.Len() == 0 {
		if v := root.Get("model"); v.Exists() {
			if value := strings.TrimSpace(v.String()); value != "" {
				seed.WriteString("model:")
				seed.WriteString(value)
			}
		}
		if v := root.Get("instructions"); v.Exists() {
			seed.WriteString(";instructions:")
			seed.WriteString(v.String())
		}
		if v := root.Get("system"); v.Exists() {
			seed.WriteString(";system:")
			seed.WriteString(v.String())
		}
		if v := root.Get("systemInstruction"); v.Exists() {
			seed.WriteString(";systemInstruction:")
			seed.WriteString(v.String())
		}
		if v := root.Get("system_instruction"); v.Exists() {
			seed.WriteString(";system_instruction:")
			seed.WriteString(v.String())
		}
	}

	if seed.Len() == 0 {
		return "unknown"
	}

	sum := sha256.Sum256([]byte(seed.String()))
	return hex.EncodeToString(sum[:])
}

func firstStableRequestContent(root gjson.Result) string {
	if messages := root.Get("messages"); messages.IsArray() {
		var content string
		messages.ForEach(func(_, message gjson.Result) bool {
			role := strings.ToLower(strings.TrimSpace(message.Get("role").String()))
			if role == "user" {
				content = extractTextContent(message.Get("content"))
				if content != "" {
					return false
				}
			}
			return true
		})
		if content != "" {
			return content
		}
	}

	if input := root.Get("input"); input.Exists() {
		if input.Type == gjson.String {
			if text := strings.TrimSpace(input.String()); text != "" {
				return text
			}
		} else if input.IsArray() {
			var content string
			input.ForEach(func(_, item gjson.Result) bool {
				if isResponsesUserItem(item) {
					content = extractResponsesItemText(item.Get("content"))
					if content != "" {
						return false
					}
				}
				return true
			})
			if content != "" {
				return content
			}
		}
	}

	if contents := root.Get("contents"); contents.IsArray() {
		var content string
		contents.ForEach(func(_, contentItem gjson.Result) bool {
			role := strings.ToLower(strings.TrimSpace(contentItem.Get("role").String()))
			// In Gemini API format, missing role defaults to "user"
			if role == "" || role == "user" {
				if parts := contentItem.Get("parts"); parts.IsArray() {
					var texts []string
					parts.ForEach(func(_, part gjson.Result) bool {
						if IsGeminiThoughtPart(part) {
							return true
						}
						if text := part.Get("text"); text.Exists() {
							if val := strings.TrimSpace(text.String()); val != "" {
								texts = append(texts, val)
							}
						}
						return true
					})
					if len(texts) > 0 {
						content = strings.Join(texts, "\n")
						return false
					}
				}
			}
			return true
		})
		if content != "" {
			return content
		}
	}

	return ""
}

func extractTextContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			if text := part.Get("text"); text.Exists() {
				if val := strings.TrimSpace(text.String()); val != "" {
					texts = append(texts, val)
				}
			}
		}
		return true
	})
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

func isResponsesUserItem(item gjson.Result) bool {
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	if role == "user" {
		return true
	}
	if role == "system" || role == "developer" || role == "assistant" {
		return false
	}
	typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	if typ == "message" {
		// Non-assistant / non-system message defaults to user
		return true
	}
	return false
}

func extractResponsesItemText(content gjson.Result) string {
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String())
	}
	if !content.IsArray() {
		return ""
	}
	var texts []string
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "input_text", "output_text", "text":
			if text := part.Get("text"); text.Exists() {
				if val := strings.TrimSpace(text.String()); val != "" {
					texts = append(texts, val)
				}
			}
		}
		return true
	})
	return strings.TrimSpace(strings.Join(texts, "\n"))
}
