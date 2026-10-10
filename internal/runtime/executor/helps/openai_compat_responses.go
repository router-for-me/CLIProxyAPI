package helps

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CompletedV1ResponsesBody extracts a completed response and buffers usage from valid upstream frames.
func CompletedV1ResponsesBody(body []byte, usage *StreamUsageBuffer) ([]byte, error) {
	if gjson.ValidBytes(body) {
		if usage != nil {
			usage.ObserveOpenAIStream(body)
			usage.Observe(ParseCodexUsage(body))
		}
		if err := ResponsesStreamEventError(body, ""); err != nil {
			return nil, err
		}
		if gjson.GetBytes(body, "type").String() == "response.incomplete" {
			return nil, ResponsesCompactionError{Message: "upstream returned an incomplete Responses result"}
		}
		if nested := gjson.GetBytes(body, "response"); nested.IsObject() {
			bodyWithUsage, errUsage := PreserveV1ResponsesUsage([]byte(nested.Raw), gjson.GetBytes(body, "usage"))
			if errUsage != nil {
				return nil, errUsage
			}
			body = bodyWithUsage
		}
		if !gjson.ParseBytes(body).IsObject() || gjson.GetBytes(body, "status").String() != "completed" || !gjson.GetBytes(body, "output").IsArray() {
			return nil, ResponsesCompactionError{Message: "upstream returned an invalid completed Responses result"}
		}
		return body, nil
	}
	var dataLines [][]byte
	var eventName string
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	var completed []byte
	responseUsage := &gjson.Result{}
	for _, line := range bytes.Split(body, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			data, errFrame := completedResponsesFrame(dataLines, eventName, outputItemsByIndex, &outputItemsFallback, usage, responseUsage)
			dataLines = nil
			eventName = ""
			if errFrame != nil {
				return nil, errFrame
			}
			if len(data) > 0 {
				if len(completed) > 0 {
					return nil, ResponsesCompactionError{Message: "upstream Responses stream returned multiple terminal responses"}
				}
				completed = data
				usage = nil
				responseUsage = nil
			}
		} else if bytes.HasPrefix(trimmed, []byte("data:")) {
			dataLines = append(dataLines, bytes.Clone(bytes.TrimSpace(trimmed[len("data:"):])))
		} else if bytes.HasPrefix(trimmed, []byte("event:")) {
			eventName = strings.TrimSpace(string(trimmed[len("event:"):]))
		}
	}
	if len(dataLines) > 0 {
		data, errFrame := completedResponsesFrame(dataLines, eventName, outputItemsByIndex, &outputItemsFallback, usage, responseUsage)
		if errFrame != nil {
			return nil, errFrame
		}
		if len(data) > 0 {
			if len(completed) > 0 {
				return nil, ResponsesCompactionError{Message: "upstream Responses stream returned multiple terminal responses"}
			}
			completed = data
		}
	}
	if len(completed) == 0 {
		return nil, ResponsesCompactionError{Message: "upstream Responses stream closed before response.completed"}
	}
	return completed, nil
}

// IsV1ResponsesSSE identifies an event stream from its media type or data frames.
func IsV1ResponsesSSE(body []byte, contentType string) bool {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return true
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			return bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte(":"))
		}
	}
	return false
}

// PatchResponsesCompletedOutput uses output_item.done events when the terminal omits output.
func PatchResponsesCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	output := gjson.GetBytes(eventData, "response.output")
	if len(outputItemsByIndex) == 0 && len(outputItemsFallback) == 0 {
		return eventData
	}
	if output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		patched := eventData
		for outputIndex, outputItem := range output.Array() {
			if !outputItem.Get("id").Exists() {
				if completedItem, ok := outputItemsByIndex[int64(outputIndex)]; ok {
					completedID := gjson.GetBytes(completedItem, "id")
					if completedID.Type == gjson.String && strings.TrimSpace(completedID.String()) != "" {
						patched, _ = sjson.SetBytes(patched, "response.output."+strconv.Itoa(outputIndex)+".id", completedID.String())
					}
				}
			}
		}
		return patched
	}
	indexes := make([]int64, 0, len(outputItemsByIndex))
	for index := range outputItemsByIndex {
		indexes = append(indexes, index)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
	items := make([]json.RawMessage, 0, len(indexes)+len(outputItemsFallback))
	for _, index := range indexes {
		items = append(items, json.RawMessage(outputItemsByIndex[index]))
	}
	for _, item := range outputItemsFallback {
		items = append(items, json.RawMessage(item))
	}
	patched, err := sjson.SetBytes(eventData, "response.output", items)
	if err != nil {
		return eventData
	}
	return patched
}

// CollectResponsesOutputItemDone retains authoritative output indexes for a terminal response.
func CollectResponsesOutputItemDone(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	item := gjson.GetBytes(eventData, "item")
	if item.IsObject() {
		if index := gjson.GetBytes(eventData, "output_index"); index.Exists() {
			outputItemsByIndex[index.Int()] = []byte(item.Raw)
		} else {
			*outputItemsFallback = append(*outputItemsFallback, []byte(item.Raw))
		}
	}
}

// ResponsesStreamEventError returns a request-scoped error for upstream Responses error events.
func ResponsesStreamEventError(data []byte, eventName string) error {
	if !gjson.ValidBytes(data) {
		return nil
	}
	eventType := strings.ToLower(gjson.GetBytes(data, "type").String())
	eventName = strings.ToLower(strings.TrimSpace(eventName))
	for _, path := range []string{"error", "response.error"} {
		errorNode := gjson.GetBytes(data, path)
		if errorNode.Exists() && errorNode.Type != gjson.Null {
			return ResponsesCompactionError{Message: "upstream Responses stream returned an error"}
		}
	}
	if eventType == "error" || eventType == "response.error" || eventType == "response.failed" || eventName == "error" || eventName == "response.error" || eventName == "response.failed" {
		return ResponsesCompactionError{Message: "upstream Responses stream returned an error"}
	}
	return nil
}

// ResponsesHeaders returns the selected response content type while retaining upstream headers.
func ResponsesHeaders(headers http.Header, stream bool) http.Header {
	result := headers.Clone()
	if result == nil {
		result = make(http.Header)
	}
	result.Del("Content-Length")
	if stream {
		result.Set("Content-Type", "text/event-stream")
	} else {
		result.Set("Content-Type", "application/json")
	}
	return result
}

func completedResponsesFrame(dataLines [][]byte, eventName string, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte, usage *StreamUsageBuffer, responseUsage *gjson.Result) ([]byte, error) {
	if len(dataLines) == 0 {
		return nil, nil
	}
	data := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
	if bytes.Equal(data, []byte("[DONE]")) {
		return nil, nil
	}
	if !gjson.ValidBytes(data) {
		return nil, ResponsesCompactionError{Message: "upstream Responses stream contained an incomplete event"}
	}
	if usage != nil {
		usage.ObserveOpenAIStream(data)
		usage.Observe(ParseCodexUsage(data))
	}
	if responseUsage != nil {
		currentUsage := gjson.GetBytes(data, "response.usage")
		if !currentUsage.IsObject() {
			currentUsage = gjson.GetBytes(data, "usage")
		}
		if currentUsage.IsObject() {
			*responseUsage = currentUsage
		}
	}
	if err := ResponsesStreamEventError(data, eventName); err != nil {
		return nil, err
	}
	eventType := gjson.GetBytes(data, "type").String()
	if eventType == "" {
		eventType = eventName
	}
	if eventType == "response.output_item.done" {
		CollectResponsesOutputItemDone(data, outputItemsByIndex, outputItemsFallback)
	}
	if eventType == "response.incomplete" {
		return nil, ResponsesCompactionError{Message: "upstream returned an incomplete Responses result"}
	}
	if eventType != "response.completed" {
		return nil, nil
	}
	prepared, err := PrepareV1ResponsesTerminalEvent(data, eventType, outputItemsByIndex, *outputItemsFallback)
	if err != nil {
		return nil, err
	}
	response := []byte(gjson.GetBytes(prepared, "response").Raw)
	if responseUsage != nil {
		return PreserveV1ResponsesUsage(response, *responseUsage)
	}
	return response, nil
}

// PrepareV1ResponsesTerminalEvent validates the envelope before restoring omitted output.
func PrepareV1ResponsesTerminalEvent(data []byte, eventType string, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) ([]byte, error) {
	var result []byte
	var err error
	response := gjson.GetBytes(data, "response")
	if !response.IsObject() {
		err = ResponsesCompactionError{Message: "upstream terminal event omitted its response"}
	} else if response.Get("status").String() != strings.TrimPrefix(eventType, "response.") {
		err = ResponsesCompactionError{Message: "upstream terminal event returned an invalid response status"}
	} else if errValidate := validateResponsesCompactionTerminalOutput(data); errValidate != nil {
		err = errValidate
	} else {
		patched := PatchResponsesCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
		if gjson.GetBytes(patched, "response.output").IsArray() {
			result = patched
		} else {
			err = ResponsesCompactionError{Message: "upstream terminal event omitted its output"}
		}
	}
	return result, err
}

// PreserveV1ResponsesUsage restores observed usage when the terminal response omits it.
func PreserveV1ResponsesUsage(response []byte, usage gjson.Result) ([]byte, error) {
	var err error
	if usage.IsObject() && !gjson.GetBytes(response, "usage").IsObject() {
		response, err = sjson.SetRawBytes(response, "usage", []byte(usage.Raw))
		if err != nil {
			err = ResponsesCompactionError{Message: "upstream Responses usage could not be encoded"}
		}
	}
	return response, err
}
