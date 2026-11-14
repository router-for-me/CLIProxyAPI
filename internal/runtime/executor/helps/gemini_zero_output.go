package helps

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// GeminiPayloadInspection summarizes output semantics of a Gemini or Antigravity response frame.
type GeminiPayloadInspection struct {
	FinishReason     string
	HasCandidate     bool
	HasVisibleOutput bool
	HasThoughtOutput bool
	HasUsageMetadata bool
	HasPromptBlock   bool
}

// GeminiZeroOutputStreamAction instructs stream executors how to process the current frame.
type GeminiZeroOutputStreamAction int

const (
	// GeminiZeroOutputContinue absorbs the current frame (either buffered as a shell or waiting for trailing usage).
	GeminiZeroOutputContinue GeminiZeroOutputStreamAction = iota
	// GeminiZeroOutputEmit flushes any buffered shell frames and processes the current frame normally.
	GeminiZeroOutputEmit
	// GeminiZeroOutputFailNow terminates the stream with a retryable 502 error without emitting completion frames.
	GeminiZeroOutputFailNow
	// GeminiZeroOutputEmitErrorThenFail emits a post-commit Claude error event before failing with 502.
	GeminiZeroOutputEmitErrorThenFail
)

// IsGeminiZeroOutputAbnormalFinishReason reports whether finishReason indicates an abnormal
// function-call or malformed response termination that yields no answer when output is empty.
func IsGeminiZeroOutputAbnormalFinishReason(reason string) bool {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "MALFORMED_RESPONSE":
		return true
	default:
		return false
	}
}

// GeminiZeroOutputErrorMessage formats a 502 error message consistent with the downstream format.
func GeminiZeroOutputErrorMessage(responseFormat sdktranslator.Format, finishReason string) string {
	reason := strings.ToUpper(strings.TrimSpace(finishReason))
	if responseFormat == sdktranslator.FormatClaude {
		doc := ojson.NewDocString(`{"type":"error","error":{"type":"api_error","message":""}}`)
		if reason == "" {
			_ = doc.Set("error.message", "Gemini stream closed with zero output")
		} else {
			_ = doc.Set("error.message", fmt.Sprintf("Gemini response finished with %s", reason))
		}
		return doc.String()
	}
	if reason == "" {
		return "upstream stream closed with zero output"
	}
	return fmt.Sprintf("upstream returned zero output with finishReason %s", reason)
}

func isValidGeminiFunctionCallPart(fc ojson.Result) bool {
	if !fc.IsObject() {
		return false
	}
	if strings.TrimSpace(fc.Get("name").String()) != "" || strings.TrimSpace(fc.Get("id").String()) != "" {
		return true
	}
	if partial := fc.Get("partialArgs"); partial.IsArray() && len(partial.Array()) > 0 {
		return true
	}
	if partial := fc.Get("partial_args"); partial.IsArray() && len(partial.Array()) > 0 {
		return true
	}
	if args := fc.Get("args"); args.IsObject() && len(args.Map()) > 0 {
		return true
	}
	return false
}

func isValidGeminiInlineDataPart(d ojson.Result) bool {
	return d.IsObject() && strings.TrimSpace(d.Get("data").String()) != ""
}

func isValidGeminiFileDataPart(fd ojson.Result) bool {
	return fd.IsObject() && (strings.TrimSpace(fd.Get("fileUri").String()) != "" || strings.TrimSpace(fd.Get("file_uri").String()) != "")
}

func isValidGeminiExecutableCodePart(ec ojson.Result) bool {
	return ec.IsObject() && strings.TrimSpace(ec.Get("code").String()) != ""
}

func isValidGeminiCodeExecutionResultPart(cer ojson.Result) bool {
	return cer.IsObject() && (strings.TrimSpace(cer.Get("output").String()) != "" || strings.TrimSpace(cer.Get("outcome").String()) != "")
}

// InspectGeminiZeroOutputPayload inspects a Gemini or Antigravity JSON/SSE payload without modifying it.
func InspectGeminiZeroOutputPayload(payload []byte, allowResponseWrapper bool) GeminiPayloadInspection {
	var out GeminiPayloadInspection
	jsonBytes := JSONPayload(payload)
	if len(jsonBytes) == 0 {
		trimmed := bytes.TrimSpace(payload)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			jsonBytes = trimmed
		} else {
			return out
		}
	}
	if !ojson.ValidBytes(jsonBytes) {
		return out
	}

	root := util.ParseGJSONBytesNoCopy(jsonBytes)
	usageRes := root.Get("usageMetadata")
	if !usageRes.Exists() {
		usageRes = root.Get("usage_metadata")
	}
	if !usageRes.Exists() && allowResponseWrapper {
		usageRes = root.Get("response.usageMetadata")
		if !usageRes.Exists() {
			usageRes = root.Get("response.usage_metadata")
		}
	}
	out.HasUsageMetadata = usageRes.Exists()

	promptRes := root.Get("promptFeedback")
	if !promptRes.Exists() && allowResponseWrapper {
		promptRes = root.Get("response.promptFeedback")
	}
	if promptRes.Exists() && strings.TrimSpace(promptRes.Get("blockReason").String()) != "" {
		out.HasPromptBlock = true
	}

	candidates := root.Get("candidates")
	if !candidates.Exists() && allowResponseWrapper {
		candidates = root.Get("response.candidates")
	}
	if !candidates.IsArray() {
		return out
	}

	candList := candidates.Array()
	if len(candList) > 0 {
		out.HasCandidate = true
	}

	for _, cand := range candList {
		if fr := strings.TrimSpace(cand.Get("finishReason").String()); fr != "" && out.FinishReason == "" {
			out.FinishReason = strings.Clone(fr)
		}
		if chunks := cand.Get("groundingMetadata.groundingChunks"); chunks.IsArray() && len(chunks.Array()) > 0 {
			out.HasVisibleOutput = true
		}
		parts := cand.Get("content.parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			hasMediaOrTool := isValidGeminiFunctionCallPart(part.Get("functionCall")) || isValidGeminiFunctionCallPart(part.Get("function_call")) ||
				isValidGeminiInlineDataPart(part.Get("inlineData")) || isValidGeminiInlineDataPart(part.Get("inline_data")) ||
				isValidGeminiFileDataPart(part.Get("fileData")) || isValidGeminiFileDataPart(part.Get("file_data")) ||
				isValidGeminiExecutableCodePart(part.Get("executableCode")) || isValidGeminiExecutableCodePart(part.Get("executable_code")) ||
				isValidGeminiCodeExecutionResultPart(part.Get("codeExecutionResult")) || isValidGeminiCodeExecutionResultPart(part.Get("code_execution_result"))
			text := part.Get("text").String()
			if part.Get("thought").Bool() {
				if text != "" || hasMediaOrTool {
					out.HasThoughtOutput = true
				}
				continue
			}
			if hasMediaOrTool || strings.TrimSpace(text) != "" {
				out.HasVisibleOutput = true
				continue
			}
		}
	}

	return out
}

// CheckGeminiZeroOutputNonStream reports whether a complete non-streaming response ended abnormally
// with zero visible output.
func CheckGeminiZeroOutputNonStream(responseFormat sdktranslator.Format, rawPayload []byte, allowResponseWrapper bool) (finishReason, errMsg string, zeroOutput bool) {
	insp := InspectGeminiZeroOutputPayload(rawPayload, allowResponseWrapper)
	if IsGeminiZeroOutputAbnormalFinishReason(insp.FinishReason) && !insp.HasVisibleOutput {
		return insp.FinishReason, GeminiZeroOutputErrorMessage(responseFormat, insp.FinishReason), true
	}
	return "", "", false
}

// GeminiZeroOutputStreamGuard tracks visible output across streaming frames, withholds leading
// contentless shell frames until meaningful content arrives, and intercepts abnormal zero-output
// terminations before HTTP 200 can be committed by readStreamBootstrap.
type GeminiZeroOutputStreamGuard struct {
	allowResponseWrapper bool
	responseFormat       sdktranslator.Format
	pendingShells        [][]byte
	sawCandidateShell    bool
	sawVisibleOutput     bool
	emittedToOut         bool
	abnormalFinishReason string
	abnormalFrame        []byte
}

// NewGeminiZeroOutputStreamGuard creates a stream guard for Gemini-family response streams.
func NewGeminiZeroOutputStreamGuard(allowResponseWrapper bool, responseFormat sdktranslator.Format) *GeminiZeroOutputStreamGuard {
	return &GeminiZeroOutputStreamGuard{
		allowResponseWrapper: allowResponseWrapper,
		responseFormat:       responseFormat,
	}
}

// MarkEmitted records that at least one non-empty payload chunk has been delivered downstream.
func (g *GeminiZeroOutputStreamGuard) MarkEmitted() {
	if g != nil {
		g.emittedToOut = true
	}
}

// Emitted reports whether any non-empty payload chunk has been delivered downstream.
func (g *GeminiZeroOutputStreamGuard) Emitted() bool {
	return g != nil && g.emittedToOut
}

// HasVisibleOutput reports whether any non-thought visible output has been observed so far.
func (g *GeminiZeroOutputStreamGuard) HasVisibleOutput() bool {
	return g != nil && g.sawVisibleOutput
}

// HasAbnormalFinish reports whether an abnormal zero-output termination was latched.
func (g *GeminiZeroOutputStreamGuard) HasAbnormalFinish() bool {
	return g != nil && g.abnormalFinishReason != ""
}

// AbnormalFrame returns the cloned abnormal finish frame retained while awaiting trailing usage.
func (g *GeminiZeroOutputStreamGuard) AbnormalFrame() []byte {
	if g == nil || len(g.abnormalFrame) == 0 {
		return nil
	}
	return bytes.Clone(g.abnormalFrame)
}

func (g *GeminiZeroOutputStreamGuard) postCommitErrorAction() GeminiZeroOutputStreamAction {
	if g.emittedToOut && g.responseFormat == sdktranslator.FormatClaude && strings.EqualFold(g.abnormalFinishReason, "MALFORMED_FUNCTION_CALL") {
		return GeminiZeroOutputEmitErrorThenFail
	}
	return GeminiZeroOutputFailNow
}

// Observe inspects a raw upstream payload (before usage-metadata filtering) and decides
// whether to buffer, flush/emit, or abort with a zero-output failure.
func (g *GeminiZeroOutputStreamGuard) Observe(rawPayload []byte) ([][]byte, GeminiZeroOutputStreamAction, string) {
	if g == nil {
		return nil, GeminiZeroOutputEmit, ""
	}
	insp := InspectGeminiZeroOutputPayload(rawPayload, g.allowResponseWrapper)
	if insp.HasVisibleOutput {
		g.sawVisibleOutput = true
	}

	if g.abnormalFinishReason != "" {
		if insp.HasUsageMetadata {
			return nil, g.postCommitErrorAction(), g.abnormalFinishReason
		}
		return nil, GeminiZeroOutputContinue, g.abnormalFinishReason
	}

	if IsGeminiZeroOutputAbnormalFinishReason(insp.FinishReason) && !g.sawVisibleOutput {
		g.abnormalFinishReason = insp.FinishReason
		g.abnormalFrame = bytes.Clone(rawPayload)
		g.pendingShells = nil
		if insp.HasUsageMetadata {
			return nil, g.postCommitErrorAction(), g.abnormalFinishReason
		}
		return nil, GeminiZeroOutputContinue, g.abnormalFinishReason
	}

	if !g.emittedToOut && insp.FinishReason == "" && !insp.HasVisibleOutput && !insp.HasThoughtOutput && !insp.HasPromptBlock {
		if insp.HasCandidate || insp.HasUsageMetadata {
			g.sawCandidateShell = true
		}
		g.pendingShells = append(g.pendingShells, bytes.Clone(rawPayload))
		return nil, GeminiZeroOutputContinue, ""
	}

	var flush [][]byte
	if len(g.pendingShells) > 0 {
		flush = g.pendingShells
		g.pendingShells = nil
	}
	return flush, GeminiZeroOutputEmit, ""
}

// Finalize resolves any pending abnormal termination or buffered shell frames at upstream EOF.
func (g *GeminiZeroOutputStreamGuard) Finalize() ([][]byte, GeminiZeroOutputStreamAction, string) {
	if g == nil {
		return nil, GeminiZeroOutputEmit, ""
	}
	if g.abnormalFinishReason != "" {
		return nil, g.postCommitErrorAction(), g.abnormalFinishReason
	}
	if len(g.pendingShells) > 0 {
		if !g.emittedToOut && g.sawCandidateShell {
			g.pendingShells = nil
			return nil, GeminiZeroOutputFailNow, ""
		}
		flush := g.pendingShells
		g.pendingShells = nil
		return flush, GeminiZeroOutputEmit, ""
	}
	return nil, GeminiZeroOutputEmit, ""
}
