package common

import (
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestRequestModelNamePrefersOriginalRequest(t *testing.T) {
	original := []byte(`{"model":"original-model"}`)
	translated := []byte(`{"model":"translated-model"}`)

	if got := RequestModelName(original, translated); got != "original-model" {
		t.Fatalf("model = %q, want original-model", got)
	}
}

func TestRequestModelNameSupportsWrappedRequest(t *testing.T) {
	request := []byte(`{"request":{"model":"wrapped-model"}}`)

	if got := RequestModelName(nil, request); got != "wrapped-model" {
		t.Fatalf("model = %q, want wrapped-model", got)
	}
}

func TestGenerateClaudeToolCallID(t *testing.T) {
	id := GenerateClaudeToolCallID()
	if len(id) != 30 {
		t.Fatalf("expected len 30 (toolu_ + 24), got %d: %q", len(id), id)
	}
	if id[:6] != "toolu_" {
		t.Fatalf("expected prefix toolu_, got %q", id)
	}
	for _, ch := range id[6:] {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			t.Fatalf("invalid character in ID %q: %c", id, ch)
		}
	}
}

func TestClaudeGenerationConstraintHelpers(t *testing.T) {
	t.Run("ClaudeMaxTokensRaw", func(t *testing.T) {
		tests := []struct {
			name   string
			body   string
			want   string
			wantOK bool
		}{
			{name: "positive integer", body: `{"max_tokens":256}`, want: "256", wantOK: true},
			{name: "zero preserved for cache pre-warm", body: `{"max_tokens":0}`, want: "0", wantOK: true},
			{name: "long decode integer preserved verbatim", body: `{"max_tokens":1030323}`, want: "1030323", wantOK: true},
			{name: "fractional preserved verbatim for upstream validation", body: `{"max_tokens":1024.9}`, want: "1024.9", wantOK: true},
			{name: "negative preserved verbatim for upstream validation", body: `{"max_tokens":-5}`, want: "-5", wantOK: true},
			{name: "scientific notation preserved verbatim", body: `{"max_tokens":1e30}`, want: "1e30", wantOK: true},
			{name: "string omitted", body: `{"max_tokens":"256"}`, wantOK: false},
			{name: "null omitted", body: `{"max_tokens":null}`, wantOK: false},
			{name: "missing omitted", body: `{}`, wantOK: false},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, ok := ClaudeMaxTokensRaw(ojson.Get(tc.body, "max_tokens"))
				if ok != tc.wantOK || got != tc.want {
					t.Fatalf("ClaudeMaxTokensRaw(%s) = (%q, %t), want (%q, %t)", tc.body, got, ok, tc.want, tc.wantOK)
				}
			})
		}
	})

	t.Run("ClaudeStopSequences", func(t *testing.T) {
		got := ClaudeStopSequences(ojson.Get(`{"stop_sequences":["END","\n\n"," ","",123,null]}`, "stop_sequences"))
		want := []string{"END", "\n\n", " "}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ClaudeStopSequences() = %#v, want %#v", got, want)
		}
		for _, body := range []string{`{"stop_sequences":[]}`, `{"stop_sequences":["",123]}`, `{"stop_sequences":"END"}`} {
			if seqs := ClaudeStopSequences(ojson.Get(body, "stop_sequences")); len(seqs) != 0 {
				t.Fatalf("ClaudeStopSequences(%s) = %#v, want nil", body, seqs)
			}
		}
	})

	t.Run("ClaudeOutputJSONSchema", func(t *testing.T) {
		schema, ok := ClaudeOutputJSONSchema(ojson.Get(`{"output_config":{"format":{"type":" JSON_SCHEMA ","schema":{"type":"object","additionalProperties":false}}}}`, "output_config.format"))
		if !ok || !schema.IsObject() || schema.Get("type").String() != "object" {
			t.Fatalf("ClaudeOutputJSONSchema() = (%s, %t), want valid schema object", schema.Raw, ok)
		}
		for _, body := range []string{
			`{"output_config":{"format":{"type":"json_schema"}}}`,
			`{"output_config":{"format":{"type":"json_schema","schema":"bad"}}}`,
			`{"output_config":{"format":{"type":"text","schema":{"type":"object"}}}}`,
		} {
			if got, ok := ClaudeOutputJSONSchema(ojson.Get(body, "output_config.format")); ok {
				t.Fatalf("ClaudeOutputJSONSchema(%s) = (%s, true), want false", body, got.Raw)
			}
		}
	})
}
