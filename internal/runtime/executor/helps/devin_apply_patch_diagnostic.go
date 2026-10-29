package helps

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

const (
	devinPatchMarker            = "*** Begin Patch"
	devinPatchEndMarker         = "*** End Patch"
	maxDevinDiagnosticToolCalls = 128
)

// DevinApplyPatchDiagnostics records only argument lengths, a short prefix for
// shape classification, and fixed error codes. It never logs argument text.
// One instance belongs to one upstream response.
type DevinApplyPatchDiagnostics struct {
	original []byte
	calls    []*devinApplyPatchCallStats
	byKey    map[int]*devinApplyPatchCallStats
	byID     map[string]int
	lastKey  int
}

type devinApplyPatchCallStats struct {
	name           string
	bytes          int
	invalidJSON    bool
	recovered      bool
	parseErrorCode string
	head           []byte
}

// NewDevinApplyPatchDiagnostics resolves apply_patch calls against the original
// client request declarations.
func NewDevinApplyPatchDiagnostics(original []byte) *DevinApplyPatchDiagnostics {
	if !ApplyPatchRequested(original) {
		return nil
	}
	return &DevinApplyPatchDiagnostics{
		original: original,
		byKey:    make(map[int]*devinApplyPatchCallStats),
		byID:     make(map[string]int),
		lastKey:  -1,
	}
}

// ObserveDelta resolves the call key for a raw wire delta and records its shape.
func (d *DevinApplyPatchDiagnostics) ObserveDelta(tc DevinToolCallDelta) int {
	if d == nil {
		return -1
	}
	id := EncodeDevinToolCallID(tc.ID)
	var key int
	exists := false
	if id != "" {
		key, exists = d.byID[id]
	} else if d.lastKey >= 0 {
		key = d.lastKey
		exists = true
	}
	if !exists {
		if len(d.calls) >= maxDevinDiagnosticToolCalls {
			return -1
		}
		key = len(d.calls)
		if id != "" {
			d.byID[id] = key
		}
	} else if id != "" {
		if _, ok := d.byID[id]; !ok {
			d.byID[id] = key
		}
	}
	d.lastKey = key
	d.Observe(key, tc)
	return key
}

// MarkRecovered marks the call identified by key as recovered from a complete
// bare patch in invalid_json_str so subsequent failures on later calls report
// the actually failing call.
func (d *DevinApplyPatchDiagnostics) MarkRecovered(key int) {
	if d == nil {
		return
	}
	if call := d.byKey[key]; call != nil {
		call.recovered = true
	}
}

// Observe accumulates one Devin tool-call delta for the call identified by key.
func (d *DevinApplyPatchDiagnostics) Observe(key int, tc DevinToolCallDelta) {
	if d == nil || key < 0 {
		return
	}
	call := d.byKey[key]
	if call == nil {
		if len(d.calls) >= maxDevinDiagnosticToolCalls {
			return
		}
		call = &devinApplyPatchCallStats{}
		d.byKey[key] = call
		d.calls = append(d.calls, call)
	}
	if tc.Name != "" {
		call.name = tc.Name
	}
	chunk := tc.Arguments
	if chunk == "" && tc.InvalidJSONStr != "" {
		chunk = tc.InvalidJSONStr
		call.invalidJSON = true
	} else if tc.InvalidJSONStr != "" {
		call.invalidJSON = true
	}
	if tc.InvalidJSONErr != "" {
		call.parseErrorCode = devinParseErrorCode(tc.InvalidJSONErr)
	}
	if len(call.head) < len(devinPatchMarker) {
		if len(call.head) == 0 {
			chunk = strings.TrimLeft(chunk, " \t\r\n")
		}
		need := min(len(devinPatchMarker)-len(call.head), len(chunk))
		call.head = append(call.head, chunk[:need]...)
	}
	call.bytes += len(tc.Arguments) + len(tc.InvalidJSONStr)
}

// Render returns a bounded log diagnostic using only fixed error codes.
func (d *DevinApplyPatchDiagnostics) Render(param any, reason string) string {
	var b strings.Builder
	b.WriteString(ApplyPatchUpstreamErrorMessage)
	if reason == "" {
		if errTranslate := ApplyPatchTranslationError(param); errTranslate != nil {
			reason = errTranslate.Error()
		}
	}
	if reason != "" {
		fmt.Fprintf(&b, " reason=%q", devinPatchReasonCode(reason))
	}
	if d == nil || len(d.calls) == 0 {
		return logging.SafeDiagnosticForLog(b.String())
	}
	var calls []*devinApplyPatchCallStats
	for _, call := range d.calls {
		// An unnamed call may still be the patch call whose identity arrives late.
		if call.name == "" || IsApplyPatchUpstreamTool(d.original, call.name) {
			calls = append(calls, call)
		}
	}
	fmt.Fprintf(&b, " apply_patch_calls=%d", len(calls))
	if len(calls) == 0 {
		return logging.SafeDiagnosticForLog(b.String())
	}
	// Describe only the call most likely rejected: the first unrecovered one
	// Devin flagged as invalid JSON, otherwise the latest one.
	index := len(calls) - 1
	for i, call := range calls {
		if call.invalidJSON && !call.recovered {
			index = i
			break
		}
	}
	call := calls[index]
	fmt.Fprintf(&b, " call[%d]={invalid_json_str=%t bytes=%d shape=%s", index, call.invalidJSON, call.bytes, call.shape())
	if call.parseErrorCode != "" {
		fmt.Fprintf(&b, " devin_parse_error=%q", call.parseErrorCode)
	}
	b.WriteString("}")
	return logging.SafeDiagnosticForLog(b.String())
}

// shape classifies only the argument prefix: a JSON object, a bare patch, or other text.
func (c *devinApplyPatchCallStats) shape() string {
	switch {
	case c.bytes == 0:
		return "empty"
	case string(c.head) == devinPatchMarker:
		return "raw_patch"
	case len(c.head) > 0 && c.head[0] == '{':
		return "json_object"
	default:
		return "other"
	}
}

func devinPatchReasonCode(reason string) string {
	switch reason {
	case "legacy_invalid_json_str", "devin_trailer_error", "devin_stream_ended_before_eos",
		"devin_stream_read_error", "devin_response_failed",
		"legacy freeform arguments are invalid for apply_patch",
		"apply_patch arguments received after completion",
		"apply_patch arguments must be a JSON object",
		"apply_patch arguments must contain the input field",
		"invalid control character in apply_patch input key",
		"apply_patch input key must be followed by a colon",
		"apply_patch input must be a string",
		"apply_patch arguments must contain only one input field",
		"apply_patch arguments must not contain trailing JSON",
		"invalid Unicode escape in apply_patch input",
		"invalid UTF-8 in apply_patch input",
		"apply_patch input high surrogate requires a low surrogate",
		"invalid escape in apply_patch input",
		"unpaired low surrogate in apply_patch input",
		"invalid control character in apply_patch input",
		"conflicting apply_patch arguments completion",
		"final apply_patch input conflicts with streamed input",
		"upstream apply_patch stream ended before protocol completion",
		"upstream apply_patch interaction failed",
		"unresolved Interactions apply_patch call identity",
		"invalid Interactions apply_patch response JSON",
		"invalid Interactions apply_patch event JSON",
		"apply_patch delta after source stop",
		"apply_patch delta after item completion",
		"apply_patch complete source conflicts with snapshot",
		"conflicting Interactions apply_patch step identity",
		"conflicting apply_patch item type",
		"conflicting apply_patch call identity",
		"conflicting apply_patch call name",
		"conflicting apply_patch full snapshots",
		"apply_patch snapshot conflicts with completed input":
		return reason
	}
	if strings.HasPrefix(reason, "decode apply_patch input value: ") {
		switch {
		case strings.HasSuffix(reason, "in string literal"):
			return "invalid control character in apply_patch input"
		case strings.HasPrefix(reason, "decode apply_patch input value: invalid character ") && strings.HasSuffix(reason, " in string"):
			quoted := strings.TrimSuffix(strings.TrimPrefix(reason, "decode apply_patch input value: invalid character "), " in string")
			if character, errUnquote := strconv.Unquote(quoted); errUnquote == nil && len(character) == 1 && character[0] < 0x20 {
				return "invalid control character in apply_patch input"
			}
		case strings.Contains(reason, "in \\u hexadecimal character escape"):
			return "invalid Unicode escape in apply_patch input"
		case strings.Contains(reason, "in string escape code"):
			return "invalid escape in apply_patch input"
		}
	}
	return "other"
}

func devinParseErrorCode(message string) string {
	switch {
	case strings.HasPrefix(message, "invalid character"):
		return "invalid_character"
	case strings.HasPrefix(message, "unexpected end"), strings.HasPrefix(message, "unexpected EOF"):
		return "unexpected_end"
	default:
		return "other"
	}
}

// CanBeDevinBareApplyPatchPrefix reports whether raw can still become a valid
// bare apply_patch payload as further streaming chunks arrive.
func CanBeDevinBareApplyPatchPrefix(raw string) bool {
	if !utf8.ValidString(raw) {
		return false
	}
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	if trimmed == "" {
		return true
	}
	if len(trimmed) < len(devinPatchMarker) {
		return strings.HasPrefix(devinPatchMarker, trimmed)
	}
	if !strings.HasPrefix(trimmed, devinPatchMarker) {
		return false
	}
	if len(trimmed) == len(devinPatchMarker) {
		return true
	}
	next := trimmed[len(devinPatchMarker)]
	return next == '\n' || next == '\r'
}

// RecoverDevinBareApplyPatch validates that raw is a complete, well-framed
// bare Codex apply_patch payload and wraps it into {"input": "..."}.
func RecoverDevinBareApplyPatch(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	trimmed := strings.TrimSpace(raw)
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 3 {
		return "", false
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if lines[0] != devinPatchMarker || lines[len(lines)-1] != devinPatchEndMarker {
		return "", false
	}

	sawEnvID := false
	mode := ""
	fileOps := 0
	sectionHasBody := false
	hasMove := false
	inHunk := false
	hunkHasLines := false
	afterEOF := false

	canCloseSection := func() bool {
		if (mode == "add" || mode == "update") && !sectionHasBody {
			return false
		}
		if mode == "update" && inHunk && !hunkHasLines {
			return false
		}
		return true
	}

	for _, line := range lines[1 : len(lines)-1] {
		if strings.HasPrefix(line, "***") {
			switch {
			case strings.HasPrefix(line, "*** Environment ID: "):
				if mode != "" || sawEnvID || strings.TrimSpace(strings.TrimPrefix(line, "*** Environment ID: ")) == "" {
					return "", false
				}
				sawEnvID = true
			case strings.HasPrefix(line, "*** Add File: "):
				if !canCloseSection() || strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: ")) == "" {
					return "", false
				}
				mode = "add"
				sectionHasBody = false
				hasMove = false
				inHunk = false
				hunkHasLines = false
				afterEOF = false
				fileOps++
			case strings.HasPrefix(line, "*** Delete File: "):
				if !canCloseSection() || strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: ")) == "" {
					return "", false
				}
				mode = "delete"
				sectionHasBody = true
				hasMove = false
				inHunk = false
				hunkHasLines = false
				afterEOF = false
				fileOps++
			case strings.HasPrefix(line, "*** Update File: "):
				if !canCloseSection() || strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: ")) == "" {
					return "", false
				}
				mode = "update"
				sectionHasBody = false
				hasMove = false
				inHunk = false
				hunkHasLines = false
				afterEOF = false
				fileOps++
			case strings.HasPrefix(line, "*** Move to: "):
				if mode != "update" || hasMove || sectionHasBody || inHunk || afterEOF || strings.TrimSpace(strings.TrimPrefix(line, "*** Move to: ")) == "" {
					return "", false
				}
				hasMove = true
			case line == "*** End of File":
				if mode != "update" || !inHunk || !hunkHasLines || afterEOF {
					return "", false
				}
				afterEOF = true
			default:
				return "", false
			}
			continue
		}

		switch mode {
		case "add":
			if !strings.HasPrefix(line, "+") {
				return "", false
			}
			sectionHasBody = true
		case "update":
			if afterEOF {
				return "", false
			}
			if line == "@@" || strings.HasPrefix(line, "@@ ") {
				if inHunk && !hunkHasLines {
					return "", false
				}
				inHunk = true
				hunkHasLines = false
				continue
			}
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") {
				inHunk = true
				hunkHasLines = true
				sectionHasBody = true
				continue
			}
			return "", false
		default:
			return "", false
		}
	}

	if fileOps == 0 || !canCloseSection() {
		return "", false
	}
	return applypatch.WrapInput(trimmed), true
}
