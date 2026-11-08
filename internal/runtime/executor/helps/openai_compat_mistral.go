package helps

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// SanitizeMistralChatRequestDoc removes fields rejected by Mistral's Chat
// Completions schema. Run it after built-in request preparation and before
// payload rules so explicit operator configuration retains final precedence.
func SanitizeMistralChatRequestDoc(doc *ojson.Doc, baseURL string) {
	if doc == nil {
		return
	}
	parsedURL, errParse := url.Parse(strings.TrimSpace(baseURL))
	if errParse != nil {
		return
	}
	host := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
	if host != "mistral.ai" && !strings.HasSuffix(host, ".mistral.ai") {
		return
	}

	_ = doc.Delete("stream_options")
	messages := doc.Get("messages").Array()
	// Delete backwards to keep the remaining message indices stable.
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Get("role").String() != "assistant" || !message.Get("reasoning_content").Exists() {
			continue
		}
		path := fmt.Sprintf("messages.%d", i)
		if isEmptyReasoningOnlyChatAssistant(message) {
			_ = doc.Delete(path)
		} else {
			_ = doc.Delete(path + ".reasoning_content")
		}
	}
}

// Only remove simple reasoning-only turns. Preserve whitespace, multipart
// content, and any other fields, including explicit assistant prefixes.
func isEmptyReasoningOnlyChatAssistant(message ojson.Result) bool {
	content := message.Get("content")
	empty := !content.Exists() || content.Type == ojson.Null ||
		(content.Type == ojson.String && content.String() == "") ||
		(content.IsArray() && len(content.Array()) == 0)
	if !empty {
		return false
	}
	reasoningOnly := true
	message.ForEach(func(key, _ ojson.Result) bool {
		switch key.String() {
		case "role", "content", "reasoning_content":
			return true
		default:
			reasoningOnly = false
			return false
		}
	})
	return reasoningOnly
}
