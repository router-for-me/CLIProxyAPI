package helps

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// GeminiAppPrompt renders the complete text history for a stateless temporary chat.
// Gemini App has no API-level system or tool roles, so role boundaries are explicit.
func GeminiAppPrompt(body []byte) (string, error) {
	if !gjson.ValidBytes(body) {
		return "", fmt.Errorf("gemini-app: invalid request JSON")
	}
	root := gjson.ParseBytes(body)
	if len(root.Get("tools").Array()) > 0 {
		return "", fmt.Errorf("gemini-app: tool calling is not supported")
	}
	if err := ValidateGeminiAppOutput(body); err != nil {
		return "", err
	}
	var prompt strings.Builder
	appendParts := func(role string, parts gjson.Result) error {
		var text strings.Builder
		for _, part := range parts.Array() {
			if !part.IsObject() {
				return fmt.Errorf("gemini-app: only text parts are supported")
			}
			for key := range part.Map() {
				if key != "text" && key != "thought" && key != "thoughtSignature" {
					return fmt.Errorf("gemini-app: only text parts are supported")
				}
			}
			if part.Get("thought").Bool() {
				continue
			}
			if value := part.Get("text"); value.Type == gjson.String {
				text.WriteString(value.String())
			}
		}
		if text.Len() > 0 {
			fmt.Fprintf(&prompt, "%s:\n%s\n\n", role, text.String())
		}
		return nil
	}
	if err := appendParts("System", root.Get("systemInstruction.parts")); err != nil {
		return "", err
	}
	for _, content := range root.Get("contents").Array() {
		role := "User"
		switch content.Get("role").String() {
		case "model":
			role = "Assistant"
		case "", "user":
		default:
			return "", fmt.Errorf("gemini-app: unsupported content role")
		}
		if err := appendParts(role, content.Get("parts")); err != nil {
			return "", err
		}
	}
	if prompt.Len() == 0 {
		return "", fmt.Errorf("gemini-app: at least one text message is required")
	}
	return strings.TrimSpace(prompt.String()), nil
}

// ValidateGeminiAppOutput checks both native and OpenAI contracts, including
// fields that translation may discard rather than preserve in generationConfig.
func ValidateGeminiAppOutput(body []byte) error {
	root := gjson.ParseBytes(body)
	if mime := root.Get("generationConfig.responseMimeType").String(); mime != "" && mime != "text/plain" {
		return fmt.Errorf("gemini-app: structured output is not supported")
	}
	if root.Get("generationConfig.responseSchema").Exists() || root.Get("generationConfig.responseJsonSchema").Exists() {
		return fmt.Errorf("gemini-app: structured output is not supported")
	}
	for _, path := range []string{"n", "generationConfig.candidateCount"} {
		if root.Get(path).Float() > 1 {
			return fmt.Errorf("gemini-app: multiple output candidates are not supported")
		}
	}
	for _, path := range []string{"modalities", "generationConfig.responseModalities"} {
		for _, modality := range root.Get(path).Array() {
			if !strings.EqualFold(modality.String(), "text") {
				return fmt.Errorf("gemini-app: only text output is supported")
			}
		}
	}
	return nil
}

// GeminiAppPayload builds the business payload before the user's final payload rules.
func GeminiAppPayload(prompt string) []byte {
	request := make([]any, 81)
	request[0] = []any{prompt, 0, nil, nil, nil, nil, 0}
	request[1] = []any{"en"}
	request[2] = []any{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	request[6], request[7] = []any{1}, 1
	request[10], request[11] = 1, 0
	request[17], request[18] = []any{[]any{0}}, 0
	request[27], request[30] = 1, []any{4}
	request[41], request[45], request[53] = []any{1}, 1, 0
	request[59], request[61] = strings.ToUpper(uuid.NewString()), []any{}
	request[68], request[79], request[80] = 1, 1, 1
	body, _ := json.Marshal(request)
	return body
}

// GeminiAppWirePayload wraps an already finalized business payload without rewriting it.
func GeminiAppWirePayload(body []byte) ([]byte, error) {
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsArray() {
		return nil, fmt.Errorf("gemini-app: payload rules must preserve the request array")
	}
	return json.Marshal([]any{nil, string(body)})
}

type geminiAppError struct {
	code    int
	message string
}

func (e geminiAppError) Error() string   { return e.message }
func (e geminiAppError) StatusCode() int { return e.code }

type geminiAppRequestError struct{ geminiAppError }

func (geminiAppRequestError) IsRequestScoped() bool { return true }

// NewGeminiAppRequestError prevents unsupported inputs from cooling an account.
func NewGeminiAppRequestError(code int, message string) error {
	return geminiAppRequestError{geminiAppError{code, message}}
}

// ReadGeminiAppStream decodes Google's XSSI-prefixed, length-framed JSON snapshots.
// Length records are decoded as JSON numbers, avoiding assumptions about UTF-16 lengths.
func ReadGeminiAppStream(reader io.Reader, emit func(string) error) (string, error) {
	buffer := bufio.NewReader(reader)
	if prefix, _ := buffer.Peek(4); bytes.Equal(prefix, []byte(")]}'")) {
		_, _ = buffer.Discard(4)
	}
	decoder := json.NewDecoder(buffer)
	text, completed := "", false
	for {
		var frame json.RawMessage
		if err := decoder.Decode(&frame); err != nil {
			if err == io.EOF {
				break
			}
			return text, geminiAppError{http.StatusBadGateway, "gemini-app: invalid or truncated upstream frame"}
		}
		if len(frame) > 0 && frame[0] != '[' {
			if _, err := strconv.ParseUint(string(frame), 10, 64); err == nil {
				continue
			}
			return text, geminiAppError{http.StatusBadGateway, "gemini-app: unexpected upstream frame"}
		}
		for _, part := range gjson.ParseBytes(frame).Array() {
			if code := part.Get("5.2.0.1.0").Int(); code != 0 {
				status := http.StatusBadGateway
				if code == 1037 {
					status = http.StatusTooManyRequests
				}
				return text, geminiAppError{status, fmt.Sprintf("gemini-app: upstream error %d", code)}
			}
			if part.Get("0").String() == "er" {
				return text, geminiAppError{http.StatusBadGateway, "gemini-app: upstream RPC error"}
			}
			inner := part.Get("2").String()
			if inner == "" {
				continue
			}
			if !gjson.Valid(inner) {
				return text, geminiAppError{http.StatusBadGateway, "gemini-app: invalid upstream response"}
			}
			result := gjson.Parse(inner)
			candidate := result.Get("4.0")
			if snapshot := candidate.Get("1.0"); snapshot.Type == gjson.String {
				next := snapshot.String()
				if !strings.HasPrefix(next, text) {
					return text, geminiAppError{http.StatusBadGateway, "gemini-app: upstream revised already delivered text"}
				}
				if delta := strings.TrimPrefix(next, text); delta != "" {
					if err := emit(delta); err != nil {
						return text, err
					}
				}
				text = next
			}
			if candidate.Get("8.0").Int() == 2 || result.Get("25").String() != "" {
				completed = true
			}
		}
	}
	if !completed || text == "" {
		return text, geminiAppError{http.StatusBadGateway, "gemini-app: response ended without a complete text answer"}
	}
	return text, nil
}

// NewGeminiAppResponse preserves one local response identity across all chunks.
// The model identifies the proxy route, not an unreported upstream model version.
func NewGeminiAppResponse(model string) func(string, bool) []byte {
	id, created := "gemini-app-"+uuid.NewString(), time.Now().UTC().Format(time.RFC3339Nano)
	return func(text string, final bool) []byte {
		candidate := map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}}}
		if final {
			candidate["finishReason"] = "STOP"
		}
		body, _ := json.Marshal(map[string]any{"candidates": []any{candidate}, "responseId": id, "modelVersion": model, "createTime": created})
		return body
	}
}
