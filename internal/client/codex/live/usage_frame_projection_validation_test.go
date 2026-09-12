package live

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/tidwall/gjson"
)

func TestUsageProjectionValidationGrammarMatchesJSON(t *testing.T) {
	values := []string{
		`0`, `-0`, `12`, `-123456789012345678901234567890`, `0.0`, `-0.01`, `1e0`, `1E+10`, `1e-10`, `1e9999`,
		`true`, `false`, `null`, `""`, `"\"\\\/\b\f\n\r\t"`, `"\u0041\u00e9\uD83D\uDE00"`, `"\ud800"`, `"Русский 日本語 😀"`,
		`[]`, `{}`, `[0,true,null,{"nested":["\u0041",-1.2e+3]}]`, `{"same":1,"same":2}`,
		`01`, `-01`, `+1`, `.1`, `1.`, `1e`, `1e+`, `1e-`, `--1`, `NaN`, `Infinity`, `0x10`, `1e1.2`,
		`tru`, `True`, `nul`, `false true`, `"\q"`, `"\u000"`, `"\u00xz"`, `"\x20"`, `"unterminated`,
		`"raw` + "\x00" + `control"`, `"raw` + "\n" + `newline"`, `[1,]`, `[,1]`, `{"a":1,}`, `{"a" 1}`, `{a:1}`, `{"a":}`,
	}
	for _, value := range values {
		for _, key := range []string{"ignored", "usage"} {
			payload := ` {"type":"response.done","` + key + `":` + value + `,"response":{"id":"r1"}} `
			wantValid := json.Valid([]byte(payload))
			for _, fragmented := range []bool{false, true} {
				var reader io.Reader = strings.NewReader(payload)
				if fragmented {
					reader = iotest.OneByteReader(reader)
				}
				got, err := projectUsageFrame(reader)
				if (err == nil) != wantValid {
					t.Fatalf("JSON agreement mismatch key=%s value=%q fragmented=%v valid=%v error=%v", key, value, fragmented, wantValid, err)
				}
				if err != nil {
					if got != nil {
						t.Fatalf("invalid frame returned partial projection: %s", got)
					}
					continue
				}
				if !json.Valid(got) || gjson.GetBytes(got, "type").String() != "response.done" || gjson.GetBytes(got, "response.id").String() != "r1" {
					t.Fatalf("invalid metadata projection for value %q: %s", value, got)
				}
			}
		}
	}
}

func TestUsageProjectionValidationEveryTruncatedPrefix(t *testing.T) {
	payload := `{"ignored":[null,-1.25e-10,{"key":"\uD83D\uDE00\\quoted\""}],"type":"response.done","response":{"usage":{"input_tokens":9007199254740993,"output_tokens":0}}}`
	for end := 0; end <= len(payload); end++ {
		prefix := payload[:end]
		got, err := projectUsageFrame(iotest.OneByteReader(strings.NewReader(prefix)))
		if (err == nil) != json.Valid([]byte(prefix)) {
			t.Fatalf("prefix %d validity mismatch: %q, projection=%s error=%v", end, prefix, got, err)
		}
		if err != nil && got != nil {
			t.Fatalf("prefix %d emitted partial metadata", end)
		}
	}
	for _, suffix := range []string{"\n\t\r ", "{}", "false", "0", "\x00"} {
		document := payload + suffix
		_, err := projectUsageFrame(iotest.OneByteReader(strings.NewReader(document)))
		if (err == nil) != json.Valid([]byte(document)) {
			t.Fatalf("suffix %q: error=%v", suffix, err)
		}
	}
}

func TestUsageProjectionValidationSelectedFieldsExact(t *testing.T) {
	payload := `{"ty\u0070e":"response.done","item_id":"item-\u2603","content_index":2,"usage":{"input_tokens":9007199254740993,"cost_in_usd_ticks":9223372036854775807,"nested":{"unknown":true}},"tool_usage":{"image_gen":{"input_tokens":7}},"service_tier":"priority","response":{"id":"\ud83d\ude00","model":"model","status":"completed","service_tier":"flex","usage":{"input_tokens":1e3,"output_tokens":-0,"modality":[1,2,3]},"tool_usage":{"custom":"\u0061"}},"session":{"model":"realtime","audio":{"input":{"transcription":{"model":"transcribe"}}},"input_audio_transcription":{"model":"legacy"}}}`
	got, err := projectUsageFrame(iotest.OneByteReader(strings.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	decode := func(data []byte) any {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(got), decode([]byte(payload))) {
		t.Fatalf("selected metadata changed:\ngot %s\nwant %s", got, payload)
	}
}

func TestUsageProjectionValidationDuplicateKeys(t *testing.T) {
	tests := []struct {
		name, payload string
		web, file     int64
	}{
		{"last output empty", `{"response":{"output":[{"type":"web_search_call"}],"output":[],"usage":{"input_tokens":1}}}`, 0, 0},
		{"last output replacement", `{"response":{"output":[{"type":"web_search_call"}],"output":[{"type":"file_search_call"}],"usage":{"input_tokens":1}}}`, 0, 1},
		{"last output null", `{"response":{"output":[{"type":"web_search_call"}],"output":null,"usage":{"input_tokens":1}}}`, 0, 0},
		{"last type string", `{"response":{"output":[{"type":"web_search_call","type":"file_search_call"}],"usage":{"input_tokens":1}}}`, 0, 1},
		{"last type null", `{"response":{"output":[{"type":"web_search_call","type":null}],"usage":{"input_tokens":1}}}`, 0, 0},
		{"last type long", `{"response":{"output":[{"type":"web_search_call","type":"` + strings.Repeat("x", 300) + `"}],"usage":{"input_tokens":1}}}`, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.payload)) {
				t.Fatal("invalid fixture")
			}
			got, err := projectUsageFrame(iotest.OneByteReader(strings.NewReader(tc.payload)))
			if err != nil {
				t.Fatal(err)
			}
			if web, file := gjson.GetBytes(got, "response.usage.web_search_calls").Int(), gjson.GetBytes(got, "response.usage.file_search_calls").Int(); web != tc.web || file != tc.file {
				t.Fatalf("duplicate keys changed billable counts: web=%d file=%d projection=%s", web, file, got)
			}
		})
	}
}

func TestUsageProjectionValidationLargeUnknownData(t *testing.T) {
	for _, kind := range []string{"string", "number", "key"} {
		t.Run(kind, func(t *testing.T) {
			prefix, suffix := `{"ignored":"`, `","type":"response.done","response":{"id":"after-large","usage":{"input_tokens":1}}}`
			fill := byte('a')
			if kind == "number" {
				prefix = `{"ignored":1`
				suffix = `,"type":"response.done","response":{"id":"after-large","usage":{"input_tokens":1}}}`
				fill = '0'
			}
			if kind == "key" {
				prefix = `{"`
				suffix = `":null,"type":"response.done","response":{"id":"after-large","usage":{"input_tokens":1}}}`
			}
			reader := io.MultiReader(strings.NewReader(prefix), io.LimitReader(validationRepeatedByte(fill), 2<<20), strings.NewReader(suffix))
			got, err := projectUsageFrame(iotest.OneByteReader(reader))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) > 1024 || !json.Valid(got) || gjson.GetBytes(got, "response.id").String() != "after-large" || gjson.GetBytes(got, "response.usage.input_tokens").Int() != 1 {
				t.Fatalf("large unknown %s affected projection: %s", kind, got)
			}
		})
	}
}

type validationRepeatedByte byte

func (b validationRepeatedByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

func TestUsageProjectionValidationPropagatesReaderError(t *testing.T) {
	failure := errors.New("validation transport failure")
	for _, prefix := range []string{``, `{"ignored":"abc`, `{"ignored":1`, `{"type":"response.done"}`} {
		got, err := projectUsageFrame(io.MultiReader(strings.NewReader(prefix), iotest.ErrReader(failure)))
		if err == nil || got != nil {
			t.Fatalf("reader error emitted accounting: prefix=%q got=%s error=%v", prefix, got, err)
		}
	}
}
