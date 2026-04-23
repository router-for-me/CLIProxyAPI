package handlers

import "github.com/tidwall/gjson"

// RequestBodyDetails captures hot-path request fields without full unmarshalling.
type RequestBodyDetails struct {
	Model     string
	Stream    bool
	HasStream bool
}

// ParseRequestBodyDetails extracts commonly used request fields in one pass.
func ParseRequestBodyDetails(rawJSON []byte) RequestBodyDetails {
	results := gjson.GetManyBytes(rawJSON, "model", "stream")
	details := RequestBodyDetails{}
	if len(results) > 0 {
		details.Model = results[0].String()
	}
	if len(results) > 1 {
		details.HasStream = results[1].Exists()
		details.Stream = results[1].Type == gjson.True
	}
	return details
}

// OpenAIChatRequestBodyDetails captures chat-completions request shape checks in one pass.
type OpenAIChatRequestBodyDetails struct {
	RequestBodyDetails
	HasMessages     bool
	HasInput        bool
	HasInstructions bool
}

// ParseOpenAIChatRequestBodyDetails extracts OpenAI chat request routing fields in one pass.
func ParseOpenAIChatRequestBodyDetails(rawJSON []byte) OpenAIChatRequestBodyDetails {
	results := gjson.GetManyBytes(rawJSON, "model", "stream", "messages", "input", "instructions")
	details := OpenAIChatRequestBodyDetails{}
	if len(results) > 0 {
		details.Model = results[0].String()
	}
	if len(results) > 1 {
		details.HasStream = results[1].Exists()
		details.Stream = results[1].Type == gjson.True
	}
	if len(results) > 2 {
		details.HasMessages = results[2].Exists()
	}
	if len(results) > 3 {
		details.HasInput = results[3].Exists()
	}
	if len(results) > 4 {
		details.HasInstructions = results[4].Exists()
	}
	return details
}

// UsesResponsesFormat reports whether the payload looks like an OpenAI Responses request.
func (d OpenAIChatRequestBodyDetails) UsesResponsesFormat() bool {
	return !d.HasMessages && (d.HasInput || d.HasInstructions)
}
