package live

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestProjectUsageFrameSkipsLargeOutputAndKeepsAccounting(t *testing.T) {
	reader := io.MultiReader(strings.NewReader(`{"type":"response.done","response":{"output":[{"type":"message","content":[{"text":"`), io.LimitReader(repeatedProjectionByte('a'), 8<<20), strings.NewReader(`"}]},{"type":"web_search_call"},{"type":"file_search_call"},{"type":"web_search_call"},{"type":"code_interpreter_call"}],"id":"r1","model":"gpt-5.4","status":"completed","service_tier":"priority","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12},"tool_usage":{"image_gen":{"input_tokens":3}}}}`))
	projected, err := projectUsageFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) > 4096 || !json.Valid(projected) {
		t.Fatalf("invalid or oversized projection: %d bytes", len(projected))
	}
	for path, want := range map[string]string{"type": "response.done", "response.id": "r1", "response.model": "gpt-5.4", "response.status": "completed", "response.service_tier": "priority", "response.usage.total_tokens": "12", "response.tool_usage.image_gen.input_tokens": "3", "response.usage.web_search_calls": "2", "response.usage.file_search_calls": "1", "response.usage.unpriced_server_tools": "true"} {
		if got := gjson.GetBytes(projected, path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestProjectUsageFrameBillingOnlyDoesNotInventUsage(t *testing.T) {
	got, err := projectUsageFrame(strings.NewReader(`{"type":"response.done","response":{"id":"r1","output":[{"type":"web_search_call"},{"type":"file_search_call"},{"type":"shell_call"}],"tool_usage":{"web_search_calls":2e2,"image_gen":{"output_tokens":8}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(got, "response.usage").Exists() {
		t.Fatalf("invented measured usage: %s", got)
	}
	for path, want := range map[string]string{"response.tool_usage.web_search_calls": "200", "response.tool_usage.file_search_calls": "1", "response.tool_usage.unpriced_server_tools": "true", "response.tool_usage.image_gen.output_tokens": "8"} {
		if actual := gjson.GetBytes(got, path).String(); actual != want {
			t.Errorf("%s = %q, want %q", path, actual, want)
		}
	}
	if raw := gjson.GetBytes(got, "response.tool_usage.web_search_calls").Raw; raw != "2e2" {
		t.Errorf("provider numeric representation changed: %s", got)
	}
}

func TestProjectUsageFrameAllocationDoesNotScaleWithTranscript(t *testing.T) {
	measure := func(size int64) float64 {
		return testing.AllocsPerRun(2, func() {
			reader := io.MultiReader(strings.NewReader(`{"ignored":"`), io.LimitReader(repeatedProjectionByte('a'), size), strings.NewReader(`","type":"response.done","response":{"usage":{"total_tokens":12}}}`))
			if _, err := projectUsageFrame(reader); err != nil {
				t.Fatal(err)
			}
		})
	}
	small, large := measure(1<<20), measure(16<<20)
	if large > small+10 {
		t.Fatalf("allocations scale with skipped transcript: 1 MiB=%v, 16 MiB=%v", small, large)
	}
}

func TestProjectUsageFramePreservesTranscriptionSessionAndNull(t *testing.T) {
	for _, payload := range []string{
		`{"type":"session.updated","session":{"model":"gpt-realtime","audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}},"input_audio_transcription":{"model":"whisper-1"}}}`,
		`{"type":"session.updated","session":{"input_audio_transcription":null}}`,
		`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item-1","content_index":2,"usage":{"input_tokens":10,"output_tokens":3},"service_tier":"default"}`,
	} {
		got, err := projectUsageFrame(strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		var original, projected any
		_ = json.Unmarshal([]byte(payload), &original)
		_ = json.Unmarshal(got, &projected)
		a, _ := json.Marshal(original)
		b, _ := json.Marshal(projected)
		if !bytes.Equal(a, b) {
			t.Errorf("projection = %s, want %s", got, a)
		}
	}
}

func TestProjectUsageFrameRejectsMalformedAndOversizedMetadata(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.done","response":{"usage":{"input_tokens":1}}`,
		`{"ignored":"bad\q"}`, `{"ignored":[1,]}`, `{"ignored":01}`, `{"ignored":1.}`,
		`{"ignored":true false}`, `{"ignored":"line` + "\n" + `break"}`, `{"ignored":{foo:1}}`,
		`{"response":{"usage":{"input_tokens":1,}}}`, `{} false`,
		`{"ignored":` + strings.Repeat("[", 300) + strings.Repeat("]", 300) + `}`,
		`{"response":{"usage":{"dimension":"` + strings.Repeat("x", 300<<10) + `"}}}`,
	} {
		if got, err := projectUsageFrame(strings.NewReader(payload)); err == nil || got != nil {
			t.Errorf("accepted malformed/oversized JSON (%d bytes): projection %q, error %v", len(payload), got, err)
		}
	}
}

func TestProjectUsageFrameValidatesSkippedEscapesAndLongKeys(t *testing.T) {
	payload := `{"` + strings.Repeat("x", 1<<20) + `":"escaped \" \\ \u0041","ignored":[true,false,null,-1.23e+45,{"x":"y"}],"ty\u0070e":"response.done","response":{"id":"r1","usage":{"total_tokens":12}}}`
	got, err := projectUsageFrame(strings.NewReader(payload))
	if err != nil || gjson.GetBytes(got, "type").String() != "response.done" {
		t.Fatalf("projection = %s, error = %v", got, err)
	}
}

type repeatedProjectionByte byte

func (b repeatedProjectionByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
