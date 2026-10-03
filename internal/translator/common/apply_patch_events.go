package common

import "github.com/tidwall/sjson"

// ApplyPatchCallState owns the input decoder and identity of one tool call.
type ApplyPatchCallState struct {
	ItemID      string
	CallID      string
	Name        string
	Namespace   string
	OutputIndex int
	Decoder     ApplyPatchInputDecoder
}

// PushArguments decodes the next arguments fragment for this call.
func (s *ApplyPatchCallState) PushArguments(fragment string) (string, error) {
	return s.Decoder.Push(fragment)
}

// FinishArguments returns any unsent suffix and the complete decoded input.
func (s *ApplyPatchCallState) FinishArguments(arguments string) (string, string, error) {
	tail, errFinish := s.Decoder.Finish(arguments)
	if errFinish != nil {
		return "", "", errFinish
	}
	return tail, s.Decoder.Input(), nil
}

// ApplyPatchInputDelta builds a Responses custom-tool input delta without SSE framing.
func ApplyPatchInputDelta(s *ApplyPatchCallState, delta string, sequence int) []byte {
	payload := []byte(`{"type":"response.custom_tool_call_input.delta","item_id":"","call_id":"","output_index":0,"sequence_number":0,"delta":""}`)
	payload = applyPatchEventIdentity(payload, s, sequence)
	payload, _ = sjson.SetBytes(payload, "delta", delta)
	return payload
}

// ApplyPatchInputDone builds a Responses custom-tool input completion without SSE framing.
func ApplyPatchInputDone(s *ApplyPatchCallState, input string, sequence int) []byte {
	payload := []byte(`{"type":"response.custom_tool_call_input.done","item_id":"","call_id":"","output_index":0,"sequence_number":0,"input":""}`)
	payload = applyPatchEventIdentity(payload, s, sequence)
	payload, _ = sjson.SetBytes(payload, "input", input)
	return payload
}

func applyPatchEventIdentity(payload []byte, s *ApplyPatchCallState, sequence int) []byte {
	payload, _ = sjson.SetBytes(payload, "item_id", s.ItemID)
	payload, _ = sjson.SetBytes(payload, "call_id", s.CallID)
	payload, _ = sjson.SetBytes(payload, "output_index", s.OutputIndex)
	payload, _ = sjson.SetBytes(payload, "sequence_number", sequence)
	return payload
}

// ApplyPatchFailure builds a terminal Responses failure without exposing upstream arguments.
func ApplyPatchFailure(responseID string, sequence int) []byte {
	payload := []byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"","object":"response","status":"failed","error":{"type":"server_error","code":"invalid_tool_arguments","message":"Invalid apply_patch tool arguments received from upstream.","param":null}}}`)
	payload, _ = sjson.SetBytes(payload, "response.id", responseID)
	payload, _ = sjson.SetBytes(payload, "sequence_number", sequence)
	return payload
}

// ApplyPatchErrorState retains a conversion error for the caller.
type ApplyPatchErrorState struct {
	err error
}

// SetToolInputError stores the original conversion error for executor handling.
func (s *ApplyPatchErrorState) SetToolInputError(err error) {
	s.err = err
}

// ToolInputError returns the original conversion error, if any.
func (s *ApplyPatchErrorState) ToolInputError() error {
	return s.err
}
