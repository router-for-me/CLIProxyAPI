package common

import (
	"errors"
	"testing"

	"github.com/tidwall/gjson"
)

func TestUserTurnDropsRefusesAnEmptiedTurnEvenWhenOtherTurnsRemain(t *testing.T) {
	var drops UserTurnDrops
	drops.EndTurn(1) // an earlier normal turn
	drops.Drop("container_upload")
	drops.EndTurn(0) // the turn the attachment emptied
	drops.EndTurn(3) // a later normal turn must not clear the refusal

	var unsupported *UnsupportedPartError
	err := drops.Err()
	if !errors.As(err, &unsupported) || unsupported.Type != "container_upload" {
		t.Fatalf("err = %v, want unsupported content part: container_upload", err)
	}
	if unsupported.StatusCode() != 400 || !unsupported.IsRequestScoped() {
		t.Fatalf("err = %#v", unsupported)
	}
}

func TestUserTurnDropsKeepsATurnThatStillHasSendableParts(t *testing.T) {
	var drops UserTurnDrops
	drops.Drop("file")
	drops.EndTurn(1)
	if err := drops.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestUserTurnDropsIgnoresAnEmptyTurnWithoutADroppedPart(t *testing.T) {
	var drops UserTurnDrops
	drops.EndTurn(0)
	if err := drops.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestUserTurnDropsReportsTheFirstEmptiedTurn(t *testing.T) {
	var drops UserTurnDrops
	drops.Drop("document")
	drops.Drop("container_upload")
	drops.EndTurn(0)
	drops.Drop("file")
	drops.EndTurn(0)
	if got, want := drops.Err().Error(), "unsupported content part: document"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestUserTurnDropsDoesNotLeakAFlagIntoTheNextTurn(t *testing.T) {
	var drops UserTurnDrops
	drops.Drop("file")
	drops.EndTurn(1)
	drops.EndTurn(0)
	if err := drops.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestIsHTTPURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.test/a.png": true,
		"HTTP://example.test/a.png":  true,
		"  https://example.test/a  ": true,
		"gs://bucket/a.pdf":          false,
		"files/abc123":               false,
		"ftp://example.test/a.png":   false,
		"data:image/png;base64,aGk=": false,
		"https://":                   false,
		"":                           false,
	}
	for value, want := range cases {
		if got := IsHTTPURL(value); got != want {
			t.Fatalf("IsHTTPURL(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestUserRunRefusesATurnEmptiedByADroppedPart(t *testing.T) {
	var run UserRun
	run.Add()
	run.End() // an earlier normal turn
	run.Drop("image")
	run.End() // the turn the attachment emptied
	run.Add()
	run.End() // a later normal turn must not clear the refusal
	if got, want := run.Err().Error(), "unsupported content part: image"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestUserRunKeepsATurnWithOtherSendableContent(t *testing.T) {
	var run UserRun
	run.Drop("audio")
	run.Add()
	run.End()
	if err := run.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestUserRunIsANoOpOnANilReceiver(t *testing.T) {
	var run *UserRun
	run.Add()
	run.Drop("image")
	run.End()
	if err := run.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestInteractionsAttachmentType(t *testing.T) {
	cases := map[string]string{
		`{"type":"image","uri":"gs://b/a.png"}`:     "image",
		`{"type":" Audio "}`:                        "audio",
		`{"type":"text","text":"hi"}`:               "",
		`{"text":"hi"}`:                             "",
		`{"inlineData":{"mimeType":"image/png"}}`:   "inlineData",
		`{"inline_data":{"mime_type":"image/png"}}`: "inlineData",
		`{"fileData":{"mimeType":"image/png"}}`:     "fileData",
		`{"file_data":{"mime_type":"image/png"}}`:   "fileData",
		`{}`:             "",
		`"plain string"`: "",
		`{"type":"image_url","image_url":{"url":""}}`: "image_url",
	}
	for raw, want := range cases {
		if got := InteractionsAttachmentType(gjson.Parse(raw)); got != want {
			t.Fatalf("InteractionsAttachmentType(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestCountSendableGeminiPartsIgnoresBlankText(t *testing.T) {
	parts := [][]byte{
		[]byte(`{"text":""}`),
		[]byte(`{"text":"  \n\t"}`),
		[]byte(`{"text":"hi"}`),
		[]byte(`{"inlineData":{"mimeType":"image/png","data":"aGk="}}`),
		[]byte(`{"fileData":{"mimeType":"image/png","fileUri":"gs://b/a.png"}}`),
		[]byte(`{"functionResponse":{"name":"f","response":{}}}`),
	}
	if got, want := CountSendableGeminiParts(parts), 4; got != want {
		t.Fatalf("CountSendableGeminiParts = %d, want %d", got, want)
	}
}

func TestIsInteractionsInstructionStep(t *testing.T) {
	cases := []struct {
		raw       string
		inherited bool
		want      bool
	}{
		{`{"type":"user_input","role":"developer","content":"note"}`, false, true},
		{`{"type":"user_input","role":"System","content":"note"}`, false, true},
		{`{"role":"system","content":"note"}`, false, true},
		{`{"type":"system","text":"note"}`, false, true},
		{`{"type":"developer","text":"note"}`, false, true},
		{`{"type":"user_input","role":"user","content":"hi"}`, false, false},
		{`{"type":"user_input","content":"hi"}`, false, false},
		{`{"type":"user_input","content":"hi"}`, true, true},
		{`{"type":"user_input","role":"user","content":"hi"}`, true, false},
		{`{"type":"model_output","content":"hi"}`, true, false},
		{`{"role":"assistant","content":"hi"}`, true, false},
		{`{"role":"user","type":"system"}`, false, false},
		{`"plain string"`, true, true},
		{`"plain string"`, false, false},
	}
	for _, tc := range cases {
		if got := IsInteractionsInstructionStep(gjson.Parse(tc.raw), tc.inherited); got != tc.want {
			t.Fatalf("IsInteractionsInstructionStep(%s, %v) = %v, want %v", tc.raw, tc.inherited, got, tc.want)
		}
	}
}
