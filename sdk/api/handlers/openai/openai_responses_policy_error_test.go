package openai

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestResponsesSSEFramerTypelessBodyPolicyError(t *testing.T) {
	framer := &responsesSSEFramer{}
	frame := []byte("data: " + `{"body":{"error":{"code":"bio_policy","message":" "}},"sequence_number":7}` + "\n\n")
	repaired := framer.repairFrame(frame)
	if framer.terminalError == nil || framer.terminalError.StatusCode != http.StatusBadRequest {
		t.Fatalf("terminal error = %#v", framer.terminalError)
	}
	payload, ok := responsesSSEDataPayload(repaired)
	if !ok {
		t.Fatalf("missing repaired payload: %s", repaired)
	}
	if got := ojson.GetBytes(payload, "error.code").String(); got != "bio_policy" {
		t.Fatalf("error code = %q", got)
	}
	if got := ojson.GetBytes(payload, "error.message").String(); got != "This content was flagged for possible biological risk." {
		t.Fatalf("message = %q", got)
	}
	if got := ojson.GetBytes(payload, "sequence_number").Int(); got != 7 {
		t.Fatalf("sequence = %d", got)
	}

	bodyStatusFramer := &responsesSSEFramer{}
	_ = bodyStatusFramer.repairFrame([]byte("data: " + `{"body":{"status":429,"error":{"message":"rate limited"}}}` + "\n\n"))
	if bodyStatusFramer.terminalError == nil || bodyStatusFramer.terminalError.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("body status terminal error = %#v", bodyStatusFramer.terminalError)
	}

	metadataFramer := &responsesSSEFramer{isCodexClient: true}
	metadataFrame := []byte("event: response.metadata\ndata: " + `{"type":"response.metadata","body":{"error":{"code":"bio_policy"}}}` + "\n\n")
	repairedMetadata := metadataFramer.repairFrame(metadataFrame)
	if metadataFramer.terminalError != nil {
		t.Fatalf("unexpected terminal error on response.metadata: %#v", metadataFramer.terminalError)
	}
	metadataPayload, ok := responsesSSEDataPayload(repairedMetadata)
	if !ok || ojson.GetBytes(metadataPayload, "type").String() != "response.metadata" {
		t.Fatalf("response.metadata frame was rewritten: %s", repairedMetadata)
	}
}
