package gemini

import (
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// NormalizeAntigravityRequestDoc applies the native Antigravity wire format.
func NormalizeAntigravityRequestDoc(doc *ojson.Doc) {
	normalizeAntigravityRequestDocAt(doc, "request.")
}

func normalizeAntigravityRequestDocAt(doc *ojson.Doc, prefix string) {
	if doc == nil {
		return
	}
	normalizeAntigravitySystemInstructionDoc(doc, prefix)
	normalizeAntigravityFunctionResponseResultsDoc(doc, prefix)
}

func normalizeAntigravitySystemInstructionDoc(doc *ojson.Doc, prefix string) {
	instructionPath := prefix + "systemInstruction"
	instruction := doc.Get(instructionPath)
	if !instruction.Exists() {
		return
	}
	if !instruction.IsObject() {
		if instruction.Type == ojson.Null {
			_ = doc.Delete(instructionPath)
		}
		return
	}
	_ = doc.Set(instructionPath+".role", "user")
	parts := instruction.Get("parts").Array()
	if len(parts) == 0 {
		return
	}

	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		text := part.Get("text")
		if !text.Exists() || text.Type != ojson.String {
			return
		}
		if text.String() != "" {
			texts = append(texts, text.String())
		}
	}
	if len(texts) == 0 {
		_ = doc.Delete(instructionPath)
		return
	}

	part := ojson.NewDocString(`{"text":""}`)
	_ = part.Set("text", strings.Join(texts, "\n\n"))
	_ = doc.SetRawBytes(instructionPath+".parts", []byte("["+part.String()+"]"))
}

var antigravityFunctionResponseFields = [...]struct {
	name          string
	responseQuery string
}{
	{name: "functionResponse", responseQuery: "functionResponse.response"},
	{name: "function_response", responseQuery: "function_response.response"},
}

func normalizeAntigravityFunctionResponseResultsDoc(doc *ojson.Doc, prefix string) {
	contentsPath := prefix + "contents"
	contents := doc.Get(contentsPath)
	if !contents.IsArray() {
		return
	}

	var pathArr [96]byte
	pathBuf := pathArr[:0]
	var rawArr [256]byte
	rawBuf := rawArr[:0]

	normalizePart := func(contentIndex int64, partIndex int, part ojson.Result, replaceResponseObject bool) {
		for _, field := range antigravityFunctionResponseFields {
			response := part.Get(field.responseQuery)
			resultRaw, resultCount := singleStringResultValue(response)
			if resultCount == 0 {
				continue
			}
			pathBuf = appendAntigravityFunctionResponsePath(pathBuf[:0], contentsPath, contentIndex, partIndex, field.name)
			responsePath := string(pathBuf)
			if replaceResponseObject && resultCount == 1 {
				rawBuf = append(rawBuf[:0], `{"output":`...)
				rawBuf = append(rawBuf, resultRaw...)
				rawBuf = append(rawBuf, '}')
				_ = doc.SetRawBytes(responsePath, rawBuf)
				continue
			}
			_ = doc.SetRaw(responsePath+".output", resultRaw)
			_ = doc.Delete(responsePath + ".result")
		}
	}

	contents.ForEach(func(contentIndex, content ojson.Result) bool {
		idx := contentIndex.Int()
		parts := content.Get("parts")
		if parts.IsArray() {
			partIndex := 0
			parts.ForEach(func(_, part ojson.Result) bool {
				normalizePart(idx, partIndex, part, true)
				partIndex++
				return true
			})
			return true
		}
		if parts.Exists() && parts.Type != ojson.Null {
			normalizePart(idx, 0, parts, false)
		}
		return true
	})
}

// singleStringResultValue inspects a response object without allocating a map.
// It returns the raw JSON string of the first "result" field and the number of
// "result" keys when every key in response is "result" and the first value is a
// JSON string (matching result.Type == ojson.String && len(response.Map()) == 1).
func singleStringResultValue(response ojson.Result) (string, int) {
	if !response.IsObject() {
		return "", 0
	}
	result := response.Get("result")
	if result.Type != ojson.String {
		return "", 0
	}
	count := 0
	valid := true
	response.ForEach(func(key, _ ojson.Result) bool {
		if key.String() != "result" {
			valid = false
			return false
		}
		count++
		return true
	})
	if !valid || count == 0 {
		return "", 0
	}
	return result.Raw, count
}

func appendAntigravityFunctionResponsePath(dst []byte, contentsPath string, contentIndex int64, partIndex int, field string) []byte {
	dst = append(dst, contentsPath...)
	dst = append(dst, '.')
	dst = strconv.AppendInt(dst, contentIndex, 10)
	dst = append(dst, ".parts."...)
	dst = strconv.AppendInt(dst, int64(partIndex), 10)
	dst = append(dst, '.')
	dst = append(dst, field...)
	dst = append(dst, ".response"...)
	return dst
}
