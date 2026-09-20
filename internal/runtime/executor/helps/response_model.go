package helps

import (
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

const (
	// maxResponseModelLength is a defensive bound on an upstream-controlled string
	// reaching logs and usage records; known model ids stay under ~128 bytes.
	maxResponseModelLength      = 128
	maxCodexResponseModelLength = maxResponseModelLength

	// modelSubstitutionWarnWindow bounds how often one credential and model pair
	// warns: on an affected credential every request is substituted.
	modelSubstitutionWarnWindow      = 10 * time.Minute
	codexModelSubstitutionWarnWindow = modelSubstitutionWarnWindow

	// modelSubstitutionWarnMaxEntries caps the throttle state, naturally bounded by
	// credentials times models; memory safety wins over perfect throttling.
	modelSubstitutionWarnMaxEntries      = 1024
	codexModelSubstitutionWarnMaxEntries = modelSubstitutionWarnMaxEntries
)

// extractResponseModelEvent returns the model an upstream reports serving, read
// from a raw JSON frame or an SSE line, and whether the event terminates the response.
func extractResponseModelEvent(payload []byte, provider string) (model string, terminal bool) {
	data := jsonPayload(payload)
	if len(data) == 0 {
		return "", false
	}
	normProvider := strings.ToLower(strings.TrimSpace(provider))
	switch normProvider {
	case "codex":
		return extractCodexResponseModelEvent(payload)
	case "claude":
		return extractClaudeResponseModelEvent(data)
	case "gemini", "gemini-interactions", "vertex", "aistudio", "antigravity":
		return extractGeminiResponseModelEvent(data)
	default:
		return extractGenericResponseModelEvent(data)
	}
}

// extractClaudeResponseModelEvent extracts the response model from an Anthropic Claude response.
func extractClaudeResponseModelEvent(data []byte) (model string, terminal bool) {
	eventType := gjson.GetBytes(data, "type").String()
	switch eventType {
	case "message_start":
		m := gjson.GetBytes(data, "message.model")
		if m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, false
			}
		}
		return "", false
	case "message_stop":
		return "", true
	case "message":
		m := gjson.GetBytes(data, "model")
		if m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, true
			}
		}
		return "", true
	default:
		if !gjson.ValidBytes(data) {
			return "", false
		}
		if m := gjson.GetBytes(data, "message.model"); m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, false
			}
		} else if m := gjson.GetBytes(data, "model"); m.Type == gjson.String {
			s := strings.TrimSpace(m.String())
			if len(s) <= maxResponseModelLength {
				return s, false
			}
		}
		return "", false
	}
}

// extractGeminiResponseModelEvent extracts the response model from a Google Gemini / Vertex / AIStudio response.
func extractGeminiResponseModelEvent(data []byte) (model string, terminal bool) {
	if !gjson.ValidBytes(data) {
		return "", false
	}
	m := gjson.GetBytes(data, "response.modelVersion")
	if !m.Exists() || m.Type != gjson.String {
		m = gjson.GetBytes(data, "modelVersion")
	}
	if !m.Exists() || m.Type != gjson.String {
		m = gjson.GetBytes(data, "interaction.model")
	}
	if !m.Exists() || m.Type != gjson.String {
		m = gjson.GetBytes(data, "model")
	}
	var served string
	if m.Type == gjson.String {
		served = strings.TrimSpace(m.String())
		if len(served) > maxResponseModelLength {
			served = ""
		}
	}
	cand := gjson.GetBytes(data, "candidates.0.finishReason")
	if !cand.Exists() {
		cand = gjson.GetBytes(data, "response.candidates.0.finishReason")
	}
	terminal = cand.Exists() && cand.String() != ""
	if !terminal {
		eventType := gjson.GetBytes(data, "event_type").String()
		if eventType == "" {
			eventType = gjson.GetBytes(data, "type").String()
		}
		status := gjson.GetBytes(data, "interaction.status").String()
		terminal = isInteractionsTerminal(eventType, status)
	}
	return served, terminal
}

// extractGenericResponseModelEvent extracts the response model from standard JSON / SSE responses.
func extractGenericResponseModelEvent(data []byte) (model string, terminal bool) {
	if !gjson.ValidBytes(data) {
		return "", false
	}
	if m := gjson.GetBytes(data, "response.model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			eventType := gjson.GetBytes(data, "type").String()
			terminal := eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete"
			return served, terminal
		}
	}
	if m := gjson.GetBytes(data, "interaction.model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			eventType := gjson.GetBytes(data, "event_type").String()
			if eventType == "" {
				eventType = gjson.GetBytes(data, "type").String()
			}
			status := gjson.GetBytes(data, "interaction.status").String()
			terminal := isInteractionsTerminal(eventType, status)
			return served, terminal
		}
	}
	if m := gjson.GetBytes(data, "modelVersion"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			cand := gjson.GetBytes(data, "candidates.0.finishReason")
			return served, cand.Exists() && cand.String() != ""
		}
	}
	if m := gjson.GetBytes(data, "response.modelVersion"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			cand := gjson.GetBytes(data, "response.candidates.0.finishReason")
			return served, cand.Exists() && cand.String() != ""
		}
	}
	if m := gjson.GetBytes(data, "message.model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			return served, false
		}
	}
	if m := gjson.GetBytes(data, "model"); m.Type == gjson.String {
		served := strings.TrimSpace(m.String())
		if len(served) <= maxResponseModelLength {
			objectType := gjson.GetBytes(data, "object").String()
			finishReason := gjson.GetBytes(data, "choices.0.finish_reason").String()
			status := gjson.GetBytes(data, "status").String()
			terminal := objectType == "chat.completion" || finishReason != "" || status == "completed" || status == "incomplete"
			return served, terminal
		}
	}
	eventType := gjson.GetBytes(data, "event_type").String()
	if eventType == "" {
		eventType = gjson.GetBytes(data, "type").String()
	}
	status := gjson.GetBytes(data, "interaction.status").String()
	if isInteractionsTerminal(eventType, status) || eventType == "message_stop" {
		return "", true
	}
	return "", false
}

func isInteractionsTerminal(eventType, status string) bool {
	switch eventType {
	case "interaction.completed", "interaction.done", "interaction.failed", "interaction.cancelled":
		return true
	}
	switch status {
	case "completed", "incomplete", "cancelled", "failed":
		return true
	}
	return false
}

// extractCodexResponseModelEvent returns the model a codex upstream reports serving, read
// from a raw JSON frame or an SSE line, and whether the event terminates the response.
func extractCodexResponseModelEvent(payload []byte) (model string, terminal bool) {
	data := jsonPayload(payload)
	if len(data) == 0 {
		return "", false
	}
	// The event type is checked before the payload is validated, so the hot path
	// (output deltas) stays a single cheap lookup.
	carriesModel, terminal := codexResponseModelEventKind(gjson.GetBytes(data, "type").String())
	if !carriesModel {
		return "", false
	}
	if !gjson.ValidBytes(data) {
		return "", false
	}
	// The value is upstream-controlled: reject non-string and oversized names
	// rather than propagating them into logs and usage records.
	modelResult := gjson.GetBytes(data, "response.model")
	if modelResult.Type != gjson.String {
		return "", terminal
	}
	model = strings.TrimSpace(modelResult.String())
	if len(model) > maxCodexResponseModelLength {
		return "", terminal
	}
	return model, terminal
}

// codexResponseModelEventKind reports whether a codex event embeds the authoritative
// response object, and whether that event terminates the response.
func codexResponseModelEventKind(eventType string) (carriesModel bool, terminal bool) {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress":
		return true, false
	case "response.completed", "response.incomplete", "response.done":
		return true, true
	default:
		return false, false
	}
}

// normalizeModelName lower-cases a model id and drops its thinking suffix,
// which never reaches the upstream request body.
func normalizeModelName(model string) string {
	return strings.TrimSpace(thinking.ParseSuffix(strings.ToLower(strings.TrimSpace(model))).ModelName)
}

// normalizeCodexModelName lower-cases a model id and drops its thinking suffix,
// which never reaches the upstream request body.
func normalizeCodexModelName(model string) string {
	return normalizeModelName(model)
}

func stripModelProviderPrefix(model string) string {
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return model[idx+1:]
	}
	return model
}

// IsCodexModelSubstituted reports whether the upstream served a model other than the
// requested one; a dated alias pins a snapshot of the same model and is accepted.
func IsCodexModelSubstituted(requested, served string) bool {
	return IsModelSubstituted(requested, served)
}

// IsModelSubstituted reports whether the upstream served a model other than the
// requested one for any provider; dated aliases and snapshot pins are accepted.
func IsModelSubstituted(requested, served string) bool {
	servedModel := normalizeModelName(served)
	if servedModel == "" {
		return false
	}
	requestedModel := normalizeModelName(requested)
	if requestedModel == "" {
		return false
	}
	if requestedModel == servedModel {
		return false
	}

	if isDatedModelAlias(requestedModel, servedModel) || isDatedModelAlias(servedModel, requestedModel) {
		return false
	}

	cleanReq := stripModelProviderPrefix(requestedModel)
	cleanSrv := stripModelProviderPrefix(servedModel)
	if cleanReq == cleanSrv {
		return false
	}
	if isDatedModelAlias(cleanReq, cleanSrv) || isDatedModelAlias(cleanSrv, cleanReq) {
		return false
	}

	reqNoLatest := strings.TrimSuffix(cleanReq, "-latest")
	srvNoLatest := strings.TrimSuffix(cleanSrv, "-latest")
	if reqNoLatest == srvNoLatest {
		return false
	}
	if isDatedModelAlias(reqNoLatest, srvNoLatest) || isDatedModelAlias(srvNoLatest, reqNoLatest) {
		return false
	}

	return true
}

func isDatedModelAlias(base, dated string) bool {
	prefix := base + "-"
	if !strings.HasPrefix(dated, prefix) {
		return false
	}
	suffix := dated[len(prefix):]
	return isModelDateSuffix(suffix) || isModelNumericVersionSuffix(suffix)
}

// isCodexDatedModelAlias reports whether dated is base plus a release date suffix,
// which upstreams use to pin the exact snapshot of the same model.
func isCodexDatedModelAlias(base, dated string) bool {
	return isDatedModelAlias(base, dated)
}

func isModelDateSuffix(suffix string) bool {
	switch len(suffix) {
	case len("YYYY-MM-DD"):
		if suffix[4] != '-' || suffix[7] != '-' {
			return false
		}
		return isModelDigits(suffix[:4]) && isModelDigits(suffix[5:7]) && isModelDigits(suffix[8:])
	case len("YYYYMMDD"):
		return isModelDigits(suffix)
	default:
		return false
	}
}

// isCodexModelDateSuffix reports whether suffix is a YYYY-MM-DD or YYYYMMDD date.
func isCodexModelDateSuffix(suffix string) bool {
	return isModelDateSuffix(suffix)
}

func isModelNumericVersionSuffix(suffix string) bool {
	if len(suffix) == 3 && isModelDigits(suffix) {
		return true
	}
	return false
}

func isModelDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// isCodexModelDigits reports whether value is a non-empty run of ASCII digits.
func isCodexModelDigits(value string) bool {
	return isModelDigits(value)
}

type codexModelSubstitutionKey struct {
	provider  string
	authID    string
	requested string
	served    string
}

// codexModelSubstitutionThrottle records the last warning per key; nowFunc is
// injectable so tests can advance the window without sleeping.
type codexModelSubstitutionThrottle struct {
	mu       sync.Mutex
	nowFunc  func() time.Time
	lastWarn map[codexModelSubstitutionKey]time.Time
}

func newCodexModelSubstitutionThrottle(nowFunc func() time.Time) *codexModelSubstitutionThrottle {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &codexModelSubstitutionThrottle{
		nowFunc:  nowFunc,
		lastWarn: make(map[codexModelSubstitutionKey]time.Time),
	}
}

// allow reports whether the key may emit a warning now and records the decision.
// Suppressed repeats stay silent instead of moving to a lower level.
func (t *codexModelSubstitutionThrottle) allow(key codexModelSubstitutionKey) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.nowFunc()
	if last, ok := t.lastWarn[key]; ok && now.Sub(last) < codexModelSubstitutionWarnWindow {
		return false
	}
	if len(t.lastWarn) >= codexModelSubstitutionWarnMaxEntries {
		for storedKey, storedAt := range t.lastWarn {
			if now.Sub(storedAt) >= codexModelSubstitutionWarnWindow {
				delete(t.lastWarn, storedKey)
			}
		}
		if len(t.lastWarn) >= codexModelSubstitutionWarnMaxEntries {
			clear(t.lastWarn)
		}
	}
	t.lastWarn[key] = now
	return true
}

// codexModelSubstitutionWarns throttles substitution warnings process-wide.
var codexModelSubstitutionWarns = newCodexModelSubstitutionThrottle(time.Now)
