package common

import "github.com/MoeForge/moego/ojson"

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
	doc := ojson.NewDoc([]byte(`{"type":"response.custom_tool_call_input.delta","item_id":"","call_id":"","output_index":0,"sequence_number":0,"delta":""}`))
	applyPatchEventIdentityDoc(doc, s, sequence)
	_ = doc.Set("delta", delta)
	return doc.Bytes()
}

// ApplyPatchInputDone builds a Responses custom-tool input completion without SSE framing.
func ApplyPatchInputDone(s *ApplyPatchCallState, input string, sequence int) []byte {
	doc := ojson.NewDoc([]byte(`{"type":"response.custom_tool_call_input.done","item_id":"","call_id":"","output_index":0,"sequence_number":0,"input":""}`))
	applyPatchEventIdentityDoc(doc, s, sequence)
	_ = doc.Set("input", input)
	return doc.Bytes()
}

func applyPatchEventIdentityDoc(doc *ojson.Doc, s *ApplyPatchCallState, sequence int) {
	_ = doc.Set("item_id", s.ItemID)
	_ = doc.Set("call_id", s.CallID)
	_ = doc.Set("output_index", s.OutputIndex)
	_ = doc.Set("sequence_number", sequence)
}

// ApplyPatchFailure builds a terminal Responses failure without exposing upstream arguments.
func ApplyPatchFailure(responseID string, sequence int) []byte {
	doc := ojson.NewDoc([]byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"","object":"response","status":"failed","error":{"type":"server_error","code":"invalid_tool_arguments","message":"Invalid apply_patch tool arguments received from upstream.","param":null}}}`))
	_ = doc.Set("response.id", responseID)
	_ = doc.Set("sequence_number", sequence)
	return doc.Bytes()
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
