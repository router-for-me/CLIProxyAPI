package helps

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

const (
	devinPatchMarker = "*** Begin Patch"
)

// DevinApplyPatchDiagnostics records only argument lengths, a short prefix for
// shape classification, and fixed error codes. It never logs argument text.
// One instance belongs to one upstream response.
type DevinApplyPatchDiagnostics struct {
	original []byte
	calls    []*devinApplyPatchCallStats
	byKey    map[int]*devinApplyPatchCallStats
}

type devinApplyPatchCallStats struct {
	name           string
	bytes          int
	invalidJSON    bool
	parseErrorCode string
	head           []byte
}

// NewDevinApplyPatchDiagnostics resolves apply_patch calls against the original
// client request declarations.
func NewDevinApplyPatchDiagnostics(original []byte) *DevinApplyPatchDiagnostics {
	return &DevinApplyPatchDiagnostics{original: original, byKey: make(map[int]*devinApplyPatchCallStats)}
}

// Observe accumulates one Devin tool-call delta for the call identified by key.
func (d *DevinApplyPatchDiagnostics) Observe(key int, tc DevinToolCallDelta) {
	if d == nil {
		return
	}
	call := d.byKey[key]
	if call == nil {
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
	// Describe only the call most likely rejected: the first one Devin flagged
	// as invalid JSON, otherwise the latest one.
	index := len(calls) - 1
	for i, call := range calls {
		if call.invalidJSON {
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
		"apply_patch arguments must be a JSON object",
		"apply_patch arguments must contain the input field",
		"apply_patch input key must be followed by a colon",
		"apply_patch input must be a string",
		"apply_patch arguments must contain only one input field",
		"apply_patch arguments must not contain trailing JSON",
		"upstream apply_patch stream ended before protocol completion":
		return reason
	default:
		return "other"
	}
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
