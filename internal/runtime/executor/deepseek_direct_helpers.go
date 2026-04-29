package executor

import (
	"encoding/json"
	"net/http"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func observeDeepSeekContinue(chunk map[string]any, state *deepSeekContinueState) {
	if state == nil {
		return
	}
	if id := intFromAny(chunk["response_message_id"]); id > 0 {
		state.ResponseMessageID = id
	}
	if asString(chunk["p"]) == "response/status" {
		if status := asString(chunk["v"]); status != "" {
			state.LastStatus = strings.TrimSpace(status)
			if strings.EqualFold(status, "FINISHED") {
				state.Finished = true
			}
		}
	}
	for _, rootKey := range []string{"v", "message"} {
		root, _ := chunk[rootKey].(map[string]any)
		response, _ := root["response"].(map[string]any)
		if response == nil {
			continue
		}
		if id := intFromAny(response["message_id"]); id > 0 {
			state.ResponseMessageID = id
		}
		if status := asString(response["status"]); status != "" {
			state.LastStatus = strings.TrimSpace(status)
			if strings.EqualFold(status, "FINISHED") {
				state.Finished = true
			}
		}
		if autoContinue, _ := response["auto_continue"].(bool); autoContinue {
			state.LastStatus = "AUTO_CONTINUE"
		}
	}
}

func kindForDeepSeekPath(path string, currentType string) string {
	path = strings.ToLower(path)
	if strings.Contains(path, "thinking") {
		return "reasoning"
	}
	if strings.Contains(path, "content") {
		return "content"
	}
	if currentType == "thinking" || currentType == "reasoning" {
		return "reasoning"
	}
	return "content"
}

func shouldSkipDeepSeekPath(path string) bool {
	if path == "response/search_status" || isDeepSeekFragmentStatusPath(path) {
		return true
	}
	for _, part := range []string{"quasi_status", "elapsed_secs", "token_usage", "pending_fragment", "conversation_mode", "fragments/-1/status", "fragments/-2/status", "fragments/-3/status"} {
		if strings.Contains(path, part) {
			return true
		}
	}
	return false
}

func isDeepSeekStatusPath(path string) bool {
	return path == "response/status" || path == "status"
}

func isDeepSeekFragmentStatusPath(path string) bool {
	if !strings.HasPrefix(path, "response/fragments/") || !strings.HasSuffix(path, "/status") {
		return false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(path, "response/fragments/"), "/status")
	mid = strings.TrimPrefix(mid, "-")
	if mid == "" {
		return false
	}
	for _, r := range mid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func deepSeekModelConfig(model string) (bool, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	noThinking := strings.HasSuffix(model, "-nothinking")
	base := strings.TrimSuffix(model, "-nothinking")
	switch base {
	case "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-vision":
		return !noThinking, false
	case "deepseek-v4-flash-search", "deepseek-v4-pro-search", "deepseek-v4-vision-search":
		return !noThinking, true
	default:
		return !noThinking, false
	}
}

func deepSeekModelType(model string) string {
	base := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(model)), "-nothinking")
	switch base {
	case "deepseek-v4-pro", "deepseek-v4-pro-search":
		return "expert"
	case "deepseek-v4-vision", "deepseek-v4-vision-search":
		return "vision"
	default:
		return "default"
	}
}

func resolveDeepSeekModel(requested string) string {
	model := strings.ToLower(strings.TrimSpace(requested))
	if model == "" {
		return ""
	}
	base := strings.TrimSuffix(model, "-nothinking")
	switch base {
	case "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-search", "deepseek-v4-pro-search", "deepseek-v4-vision", "deepseek-v4-vision-search":
		return model
	case "deepseek-chat":
		return withNoThinking(model, "deepseek-v4-flash")
	case "deepseek-reasoner":
		return withNoThinking(model, "deepseek-v4-pro")
	default:
		return ""
	}
}

func withNoThinking(original, mapped string) string {
	if strings.HasSuffix(original, "-nothinking") && !strings.HasSuffix(mapped, "-nothinking") {
		return mapped + "-nothinking"
	}
	return mapped
}

func extractDeepSeekToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	for _, key := range []string{"api_key", "token", "access_token"} {
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return value
			}
		}
		if auth.Metadata != nil {
			if value := strings.TrimSpace(asString(auth.Metadata[key])); value != "" {
				return value
			}
		}
	}
	if auth.Metadata != nil {
		if tokenMap, ok := auth.Metadata["token"].(map[string]any); ok {
			return strings.TrimSpace(asString(tokenMap["access_token"]))
		}
	}
	return ""
}

func setDeepSeekAuthToken(auth *cliproxyauth.Auth, token string) {
	if auth == nil || strings.TrimSpace(token) == "" {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	auth.Metadata["token"] = token
}

func stringFromAuth(auth *cliproxyauth.Auth, key string) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
			return value
		}
	}
	if auth.Metadata != nil {
		if value := strings.TrimSpace(asString(auth.Metadata[key])); value != "" {
			return value
		}
	}
	return ""
}

type deepSeekAPIError struct {
	status  int
	code    int
	bizCode int
	msg     string
}

func (e deepSeekAPIError) Error() string { return e.msg }

func isDeepSeekAuthError(err error) bool {
	apiErr, ok := err.(deepSeekAPIError)
	if !ok {
		return false
	}
	combined := strings.ToLower(apiErr.msg)
	return apiErr.status == http.StatusUnauthorized || apiErr.status == http.StatusForbidden ||
		apiErr.code == 40001 || apiErr.code == 40002 || apiErr.code == 40003 ||
		apiErr.bizCode == 40001 || apiErr.bizCode == 40002 || apiErr.bizCode == 40003 ||
		strings.Contains(combined, "token") || strings.Contains(combined, "login") ||
		strings.Contains(combined, "unauthorized") || strings.Contains(combined, "expired") ||
		strings.Contains(combined, "invalid jwt")
}

func deepSeekResponseStatus(resp map[string]any) (int, int, string, string) {
	code := intFromAny(resp["code"])
	msg := asString(resp["msg"])
	data, _ := resp["data"].(map[string]any)
	bizCode := intFromAny(data["biz_code"])
	bizMsg := asString(data["biz_msg"])
	if bizMsg == "" {
		if bizData, _ := data["biz_data"].(map[string]any); bizData != nil {
			bizMsg = asString(bizData["msg"])
		}
	}
	return code, bizCode, msg, bizMsg
}

func failureMessage(msg, bizMsg, fallback string) string {
	if strings.TrimSpace(bizMsg) != "" {
		return strings.TrimSpace(bizMsg)
	}
	if strings.TrimSpace(msg) != "" {
		return strings.TrimSpace(msg)
	}
	return fallback
}

func statusCodeOr(status, fallback int) int {
	if status != 0 {
		return status
	}
	return fallback
}

func headersToHTTPHeader(headers map[string]string) http.Header {
	out := http.Header{}
	for key, value := range headers {
		out.Set(key, value)
	}
	return out
}

func authID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func authLabel(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Label
}

func asString(value any) string {
	s, _ := value.(string)
	return strings.TrimSpace(s)
}

func boolValue(value any) bool {
	b, _ := value.(bool)
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func jsonPointer(root map[string]any, path ...string) any {
	var current any = root
	for _, part := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[part]
	}
	return current
}

func jsonPointerString(root map[string]any, path ...string) string {
	return asString(jsonPointer(root, path...))
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		v, _ := typed.Int64()
		return int(v)
	default:
		return 0
	}
}

func int64FromAny(value any, fallback int64) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		v, err := typed.Int64()
		if err == nil {
			return v
		}
	}
	return fallback
}
