// Package gemini provides request translation functionality for Codex to Gemini API compatibility.
// It handles parsing and transforming Codex API requests into Gemini API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Codex API format and Gemini API's expected format.
package gemini

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToCodex parses and transforms a Gemini API request into Codex API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Codex API.
// The function performs comprehensive transformation including:
// 1. Model name mapping and generation configuration extraction
// 2. System instruction conversion to Codex format
// 3. Message content conversion with proper role mapping
// 4. Tool call and tool result handling with FIFO queue for ID matching
// 5. Tool declaration and tool choice configuration mapping
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Gemini API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Codex API format
func ConvertGeminiRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	// Base template
	out := `{"model":"","instructions":"","input":[]}`

	root := gjson.ParseBytes(rawJSON)

	// Pre-compute tool name shortening map from declared functionDeclarations
	shortMap := map[string]string{}
	if tools := root.Get("tools"); tools.IsArray() {
		var names []string
		tarr := tools.Array()
		for i := 0; i < len(tarr); i++ {
			fns := tarr[i].Get("functionDeclarations")
			if !fns.IsArray() {
				continue
			}
			for _, fn := range fns.Array() {
				if v := fn.Get("name"); v.Exists() {
					names = append(names, v.String())
				}
			}
		}
		if len(names) > 0 {
			shortMap = buildShortNameMap(names)
		}
	}

	// helper for generating paired call IDs in the form: call_<alphanum>
	// Gemini uses sequential pairing across possibly multiple in-flight
	// functionCalls, so we keep a FIFO queue of generated call IDs and
	// consume them in order when functionResponses arrive.
	var pendingCallIDs []string

	// genCallID creates a random call id like: call_<8chars>
	genCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 8 chars random suffix
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "call_" + b.String()
	}

	// Model
	out, _ = sjson.Set(out, "model", modelName)

	// System instruction -> as a user message with input_text parts
	sysParts := root.Get("system_instruction.parts")
	if sysParts.IsArray() {
		msg := `{"type":"message","role":"developer","content":[]}`
		arr := sysParts.Array()
		for i := 0; i < len(arr); i++ {
			p := arr[i]
			if t := p.Get("text"); t.Exists() {
				part := `{}`
				part, _ = sjson.Set(part, "type", "input_text")
				part, _ = sjson.Set(part, "text", t.String())
				msg, _ = sjson.SetRaw(msg, "content.-1", part)
			}
		}
		if len(gjson.Get(msg, "content").Array()) > 0 {
			out, _ = sjson.SetRaw(out, "input.-1", msg)
		}
	}

	// Contents -> messages and function calls/results
	contents := root.Get("contents")
	if contents.IsArray() {
		items := contents.Array()
		for i := 0; i < len(items); i++ {
			item := items[i]
			role := item.Get("role").String()
			if role == "model" {
				role = "assistant"
			}

			parts := item.Get("parts")
			if !parts.IsArray() {
				continue
			}
			parr := parts.Array()
			for j := 0; j < len(parr); j++ {
				p := parr[j]
				// text part
				if t := p.Get("text"); t.Exists() {
					msg := `{"type":"message","role":"","content":[]}`
					msg, _ = sjson.Set(msg, "role", role)
					partType := "input_text"
					if role == "assistant" {
						partType = "output_text"
					}
					part := `{}`
					part, _ = sjson.Set(part, "type", partType)
					part, _ = sjson.Set(part, "text", t.String())
					msg, _ = sjson.SetRaw(msg, "content.-1", part)
					out, _ = sjson.SetRaw(out, "input.-1", msg)
					continue
				}

				// function call from model
				if fc := p.Get("functionCall"); fc.Exists() {
					fn := `{"type":"function_call"}`
					if name := fc.Get("name"); name.Exists() {
						n := name.String()
						if short, ok := shortMap[n]; ok {
							n = short
						} else {
							n = shortenNameIfNeeded(n)
						}
						fn, _ = sjson.Set(fn, "name", n)
					}
					if args := fc.Get("args"); args.Exists() {
						fn, _ = sjson.Set(fn, "arguments", args.Raw)
					}
					// generate a paired random call_id and enqueue it so the
					// corresponding functionResponse can pop the earliest id
					// to preserve ordering when multiple calls are present.
					id := genCallID()
					fn, _ = sjson.Set(fn, "call_id", id)
					pendingCallIDs = append(pendingCallIDs, id)
					out, _ = sjson.SetRaw(out, "input.-1", fn)
					continue
				}

				// function response from user
				if fr := p.Get("functionResponse"); fr.Exists() {
					fno := `{"type":"function_call_output"}`
					// Prefer a string result if present; otherwise embed the raw response as a string
					if res := fr.Get("response.result"); res.Exists() {
						fno, _ = sjson.Set(fno, "output", res.String())
					} else if resp := fr.Get("response"); resp.Exists() {
						fno, _ = sjson.Set(fno, "output", resp.Raw)
					}
					// fno, _ = sjson.Set(fno, "call_id", "call_W6nRJzFXyPM2LFBbfo98qAbq")
					// attach the oldest queued call_id to pair the response
					// with its call. If the queue is empty, generate a new id.
					var id string
					if len(pendingCallIDs) > 0 {
						id = pendingCallIDs[0]
						// pop the first element
						pendingCallIDs = pendingCallIDs[1:]
					} else {
						id = genCallID()
					}
					fno, _ = sjson.Set(fno, "call_id", id)
					out, _ = sjson.SetRaw(out, "input.-1", fno)
					continue
				}
			}
		}
	}

	// Tools mapping: Gemini functionDeclarations -> Codex tools
	tools := root.Get("tools")
	if tools.IsArray() {
		out, _ = sjson.SetRaw(out, "tools", `[]`)
		out, _ = sjson.Set(out, "tool_choice", "auto")
		tarr := tools.Array()
		for i := 0; i < len(tarr); i++ {
			td := tarr[i]
			fns := td.Get("functionDeclarations")
			if !fns.IsArray() {
				continue
			}
			farr := fns.Array()
			for j := 0; j < len(farr); j++ {
				fn := farr[j]
				tool := `{}`
				tool, _ = sjson.Set(tool, "type", "function")
				if v := fn.Get("name"); v.Exists() {
					name := v.String()
					if short, ok := shortMap[name]; ok {
						name = short
					} else {
						name = shortenNameIfNeeded(name)
					}
					tool, _ = sjson.Set(tool, "name", name)
				}
				if v := fn.Get("description"); v.Exists() {
					tool, _ = sjson.Set(tool, "description", v.String())
				}
				if prm := fn.Get("parameters"); prm.Exists() {
					// Remove optional $schema field if present
					cleaned := prm.Raw
					cleaned, _ = sjson.Delete(cleaned, "$schema")
					cleaned, _ = sjson.Set(cleaned, "additionalProperties", false)
					tool, _ = sjson.SetRaw(tool, "parameters", cleaned)
				} else if prm = fn.Get("parametersJsonSchema"); prm.Exists() {
					// Remove optional $schema field if present
					cleaned := prm.Raw
					cleaned, _ = sjson.Delete(cleaned, "$schema")
					cleaned, _ = sjson.Set(cleaned, "additionalProperties", false)
					tool, _ = sjson.SetRaw(tool, "parameters", cleaned)
				}
				tool, _ = sjson.Set(tool, "strict", false)
				out, _ = sjson.SetRaw(out, "tools.-1", tool)
			}
		}
	}

	// Fixed flags aligning with Codex expectations
	out, _ = sjson.Set(out, "parallel_tool_calls", true)

	// Convert Gemini thinkingConfig to Codex reasoning.effort.
	// Note: Google official Python SDK sends snake_case fields (thinking_level/thinking_budget).
	effortSet := false
	if genConfig := root.Get("generationConfig"); genConfig.Exists() {
		if thinkingConfig := genConfig.Get("thinkingConfig"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
			thinkingLevel := thinkingConfig.Get("thinkingLevel")
			if !thinkingLevel.Exists() {
				thinkingLevel = thinkingConfig.Get("thinking_level")
			}
			if thinkingLevel.Exists() {
				effort := strings.ToLower(strings.TrimSpace(thinkingLevel.String()))
				if effort != "" {
					out, _ = sjson.Set(out, "reasoning.effort", effort)
					effortSet = true
				}
			} else {
				thinkingBudget := thinkingConfig.Get("thinkingBudget")
				if !thinkingBudget.Exists() {
					thinkingBudget = thinkingConfig.Get("thinking_budget")
				}
				if thinkingBudget.Exists() {
					if effort, ok := thinking.ConvertBudgetToLevel(int(thinkingBudget.Int())); ok {
						out, _ = sjson.Set(out, "reasoning.effort", effort)
						effortSet = true
					}
				}
			}
		}
	}
	if !effortSet {
		// No thinking config, set default effort
		out, _ = sjson.Set(out, "reasoning.effort", "medium")
	}
	out, _ = sjson.Set(out, "reasoning.summary", "auto")
	out, _ = sjson.Set(out, "stream", true)
	out, _ = sjson.Set(out, "store", false)
	out, _ = sjson.Set(out, "include", []string{"reasoning.encrypted_content"})

	var pathsToLower []string
	toolsResult := gjson.Get(out, "tools")
	util.Walk(toolsResult, "", "type", &pathsToLower)
	for _, p := range pathsToLower {
		fullPath := fmt.Sprintf("tools.%s", p)
		out, _ = sjson.Set(out, fullPath, strings.ToLower(gjson.Get(out, fullPath).String()))
	}

	return []byte(out)
}

// shortenNameIfNeeded applies the simple shortening rule for a single name.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
	}
	return name[:limit]
}

// buildShortNameMap ensures uniqueness of shortened names within a request.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}
