package openai

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesNoticeFilterDropsUsageWarnings(t *testing.T) {
	for _, percentage := range []string{"5%", "10%", "25%"} {
		filter := newResponsesNoticeFilter()
		message := "Heads up, you have less than " + percentage + " of your weekly limit left. Run /status for a breakdown"

		first := filter.FilterPayload([]byte(`{"type":"response.output_text.delta","item_id":"msg-1","delta":"` + message + `"}`))
		if len(first) != 0 {
			t.Fatalf("first payload should be dropped for %s", percentage)
		}

		second := filter.FilterPayload([]byte(`{"type":"response.output_text.done","item_id":"msg-1","text":"` + message + `"}`))
		if len(second) != 0 {
			t.Fatalf("suppressed payload should be dropped for %s", percentage)
		}
	}
}

func TestResponsesNoticeFilterSanitizesCompletedOutput(t *testing.T) {
	filter := newResponsesNoticeFilter()

	payload := filter.FilterPayload([]byte(`{
		"type":"response.completed",
		"response":{
			"output":[
				{"id":"msg-1","type":"message","content":[{"type":"output_text","text":"Heads up, you have less than 10% of your weekly limit left. Run /status for a breakdown"}]},
				{"id":"msg-2","type":"message","content":[{"type":"output_text","text":"real output"}]}
			]
		}
	}`))

	output := gjson.GetBytes(payload, "response.output").Array()
	if len(output) != 1 {
		t.Fatalf("response output len = %d, want 1", len(output))
	}
	if output[0].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected remaining output id: %s", output[0].Get("id").String())
	}
}

func TestResponsesNoticeFilterSanitizesResponseObjectOutput(t *testing.T) {
	filter := newResponsesNoticeFilter()

	payload := filter.FilterResponseObject([]byte(`{
		"id":"resp-1",
		"output":[
			{"id":"msg-1","type":"message","content":[{"type":"output_text","text":"Heads up, you have less than 25% of your weekly limit left. Run /status for a breakdown"}]},
			{"id":"msg-2","type":"message","content":[{"type":"output_text","text":"real output"}]}
		]
	}`))

	output := gjson.GetBytes(payload, "output").Array()
	if len(output) != 1 {
		t.Fatalf("response object output len = %d, want 1", len(output))
	}
	if output[0].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected remaining response object output id: %s", output[0].Get("id").String())
	}
}

func TestResponsesSSEFramerDropsUsageWarningFrame(t *testing.T) {
	var out bytes.Buffer
	framer := &responsesSSEFramer{noticeFilter: newResponsesNoticeFilter()}

	framer.WriteChunk(&out, []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg-1\",\"delta\":\"Heads up, you have less than 5% of your weekly limit left. Run /status for a breakdown\"}\n\n"))
	framer.WriteChunk(&out, []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"msg-2\",\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"real output\"}]}]}}\n\n"))

	if bytes.Contains(out.Bytes(), []byte("weekly limit left")) {
		t.Fatalf("usage warning should be filtered")
	}
	if !bytes.Contains(out.Bytes(), []byte("real output")) {
		t.Fatalf("normal payload should remain")
	}
}
