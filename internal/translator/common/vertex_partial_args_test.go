package common

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestVertexPartialArgsAssembler_ComplexTypesAndPaths(t *testing.T) {
	chunks := []string{
		`{"name":"complex_tool","partialArgs":[{"jsonPath":"$.query","stringValue":"line1\n\"quoted\"\\slash\t","willContinue":true}],"willContinue":true}`,
		`{"partialArgs":[{"jsonPath":"$.query","stringValue":"end","willContinue":true}],"willContinue":true}`,
		`{"partialArgs":[{"jsonPath":"$.query"},{"jsonPath":"$.count","numberValue":0},{"jsonPath":"$.enabled","boolValue":false},{"jsonPath":"$.optional","nullValue":"NULL_VALUE"}],"willContinue":true}`,
		`{"partialArgs":[{"jsonPath":"$.nested.items[0].label","stringValue":"first","willContinue":true},{"jsonPath":"$.nested.items[0].score","numberValue":1.5}],"willContinue":true}`,
		`{"partialArgs":[{"jsonPath":"$.nested.items[0].label"},{"jsonPath":"$.nested.items[1].label","stringValue":"second"},{"jsonPath":"$.nested.items[1].active","boolValue":true}],"willContinue":true}`,
		`{}`,
	}

	asm := NewVertexPartialArgsAssembler()
	var deltas []string
	for i, raw := range chunks {
		delta, done := asm.PushFunctionCall(gjson.Parse(raw))
		if asm.Err() != nil {
			t.Fatalf("chunk %d unexpected error: %v", i, asm.Err())
		}
		deltas = append(deltas, delta)
		wantDone := i == len(chunks)-1
		if done != wantDone {
			t.Fatalf("chunk %d done = %v, want %v (delta=%q)", i, done, wantDone, delta)
		}
	}

	if !strings.Contains(deltas[0], `line1\n\"quoted\"\\slash\t`) {
		t.Fatalf("expected chunk 0 to stream escaped string immediately, got %q", deltas[0])
	}

	wantJSON := `{"query":"line1\n\"quoted\"\\slash\tend","count":0,"enabled":false,"optional":null,"nested":{"items":[{"label":"first","score":1.5},{"label":"second","active":true}]}}`
	joined := strings.Join(deltas, "")
	if joined != wantJSON {
		t.Fatalf("joined deltas = %s, want %s", joined, wantJSON)
	}
	if asm.FullArguments() != wantJSON {
		t.Fatalf("FullArguments() = %s, want %s", asm.FullArguments(), wantJSON)
	}
}

func TestVertexPartialArgsAssembler_InterleavedStringContinuations(t *testing.T) {
	chunks := []string{
		`{"name":"fn","partialArgs":[{"jsonPath":"$.a","stringValue":"A","willContinue":true}],"willContinue":true}`,
		`{"partialArgs":[{"jsonPath":"$.b","stringValue":"B","willContinue":true}],"willContinue":true}`,
		`{"partialArgs":[{"jsonPath":"$.b","stringValue":"C","willContinue":false}],"willContinue":true}`,
		`{"partialArgs":[{"jsonPath":"$.a","stringValue":"D","willContinue":false}],"willContinue":true}`,
		`{}`,
	}

	asm := NewVertexPartialArgsAssembler()
	var deltas []string
	for i, raw := range chunks {
		delta, done := asm.PushFunctionCall(gjson.Parse(raw))
		if asm.Err() != nil {
			t.Fatalf("chunk %d unexpected error: %v", i, asm.Err())
		}
		deltas = append(deltas, delta)
		if done != (i == len(chunks)-1) {
			t.Fatalf("chunk %d done = %v, want %v", i, done, i == len(chunks)-1)
		}
	}
	want := `{"a":"AD","b":"BC"}`
	if got := strings.Join(deltas, ""); got != want {
		t.Fatalf("interleaved strings = %s, want %s", got, want)
	}
}

func TestVertexPartialArgsAssembler_NonDepthFirstAndArrayOrdering(t *testing.T) {
	t.Run("revisiting ancestor object path", func(t *testing.T) {
		chunks := []string{
			`{"name":"fn","partialArgs":[{"jsonPath":"$.a.x","numberValue":1}],"willContinue":true}`,
			`{"partialArgs":[{"jsonPath":"$.b","numberValue":2}],"willContinue":true}`,
			`{"partialArgs":[{"jsonPath":"$.a.y","numberValue":3}],"willContinue":true}`,
			`{}`,
		}
		asm := NewVertexPartialArgsAssembler()
		var deltas []string
		for _, raw := range chunks {
			d, _ := asm.PushFunctionCall(gjson.Parse(raw))
			if asm.Err() != nil {
				t.Fatalf("unexpected error: %v", asm.Err())
			}
			deltas = append(deltas, d)
		}
		want := `{"a":{"x":1,"y":3},"b":2}`
		if got := strings.Join(deltas, ""); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	})

	t.Run("reverse array indices", func(t *testing.T) {
		chunks := []string{
			`{"name":"fn","partialArgs":[{"jsonPath":"$.items[1]","numberValue":2}],"willContinue":true}`,
			`{"partialArgs":[{"jsonPath":"$.items[0]","numberValue":1}],"willContinue":true}`,
			`{}`,
		}
		asm := NewVertexPartialArgsAssembler()
		var deltas []string
		for _, raw := range chunks {
			d, _ := asm.PushFunctionCall(gjson.Parse(raw))
			if asm.Err() != nil {
				t.Fatalf("unexpected error: %v", asm.Err())
			}
			deltas = append(deltas, d)
		}
		want := `{"items":[1,2]}`
		if got := strings.Join(deltas, ""); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	})

	t.Run("sparse array index filled with null on finish", func(t *testing.T) {
		chunks := []string{
			`{"name":"fn","partialArgs":[{"jsonPath":"$.items[2]","numberValue":3}],"willContinue":true}`,
			`{}`,
		}
		asm := NewVertexPartialArgsAssembler()
		var deltas []string
		for _, raw := range chunks {
			d, _ := asm.PushFunctionCall(gjson.Parse(raw))
			if asm.Err() != nil {
				t.Fatalf("unexpected error: %v", asm.Err())
			}
			deltas = append(deltas, d)
		}
		want := `{"items":[null,null,3]}`
		if got := strings.Join(deltas, ""); got != want {
			t.Fatalf("got %s, want %s", got, want)
		}
	})
}

func TestVertexPartialArgsAssembler_SingleChunkParameterlessAndEmptyPartialArgsFallback(t *testing.T) {
	ping := gjson.Parse(`{"name":"ping","willContinue":false}`)
	if !IsVertexStreamingFunctionCall(ping) {
		t.Fatalf("expected IsVertexStreamingFunctionCall=true for single-chunk willContinue:false call")
	}
	asmPing := NewVertexPartialArgsAssembler()
	deltaPing, donePing := asmPing.PushFunctionCall(ping)
	if !donePing || deltaPing != "{}" {
		t.Fatalf("got delta=%q done=%v, want {} true", deltaPing, donePing)
	}

	withArgs := gjson.Parse(`{"name":"get_weather","args":{"city":"Paris"},"partialArgs":[]}`)
	if IsVertexStreamingFunctionCall(withArgs) {
		t.Fatalf("expected IsVertexStreamingFunctionCall=false when complete args is present with empty partialArgs")
	}
	asmArgs := NewVertexPartialArgsAssembler()
	deltaArgs, doneArgs := asmArgs.PushFunctionCall(withArgs)
	if !doneArgs || deltaArgs != `{"city":"Paris"}` {
		t.Fatalf("got delta=%q done=%v, want city Paris true", deltaArgs, doneArgs)
	}
}

func TestVertexStreamingToolCall_CanContinueWith(t *testing.T) {
	c1 := gjson.Parse(`{"id":"call_1","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}`)
	active := NewVertexStreamingToolCall("get_weather", "get_weather", "call_1", "call_1", 0)
	if _, done := active.Assembler.PushFunctionCall(c1); done {
		t.Fatalf("expected done=false on chunk 1")
	}

	c2OmittedID := gjson.Parse(`{"name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"York"}],"willContinue":false}`)
	if !active.CanContinueWith(c2OmittedID) {
		t.Fatalf("expected CanContinueWith=true when continuation repeats name and omits id")
	}

	c2DifferentID := gjson.Parse(`{"id":"call_2","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"LA"}],"willContinue":false}`)
	if active.CanContinueWith(c2DifferentID) {
		t.Fatalf("expected CanContinueWith=false when explicit id differs")
	}
}

func TestVertexPartialArgsAssembler_QuotedJSONPathUnicodeAndLimits(t *testing.T) {
	t.Run("quoted keys with unicode, surrogate pairs, backslash, and quotes", func(t *testing.T) {
		chunks := []string{
			`{"name":"fn","partialArgs":[{"jsonPath":"$['\\u0061']","numberValue":1},{"jsonPath":"$[\"\\uD83D\\uDE00\"]","stringValue":"smile"},{"jsonPath":"$['k\\'ey\\\\name']","boolValue":true}],"willContinue":false}`,
		}
		asm := NewVertexPartialArgsAssembler()
		delta, done := asm.PushFunctionCall(gjson.Parse(chunks[0]))
		if asm.Err() != nil {
			t.Fatalf("unexpected error: %v", asm.Err())
		}
		if !done {
			t.Fatalf("expected done=true")
		}
		want := `{"a":1,"😀":"smile","k'ey\\name":true}`
		if delta != want {
			t.Fatalf("got %s, want %s", delta, want)
		}
	})

	t.Run("rejects invalid escapes and huge or overflowing array indices", func(t *testing.T) {
		invalidChunks := []string{
			`{"name":"fn","partialArgs":[{"jsonPath":"$['\\x41']","numberValue":1}],"willContinue":false}`,
			`{"name":"fn","partialArgs":[{"jsonPath":"$['\\uD800']","numberValue":1}],"willContinue":false}`,
			`{"name":"fn","partialArgs":[{"jsonPath":"$.items[5000]","numberValue":1}],"willContinue":false}`,
			`{"name":"fn","partialArgs":[{"jsonPath":"$.items[999999999999999999999999999]","numberValue":1}],"willContinue":false}`,
		}
		for _, raw := range invalidChunks {
			asm := NewVertexPartialArgsAssembler()
			_, done := asm.PushFunctionCall(gjson.Parse(raw))
			if asm.Err() == nil {
				t.Fatalf("expected error for invalid chunk %s, got nil", raw)
			}
			if done {
				t.Fatalf("expected done=false on error for chunk %s", raw)
			}
		}
	})
}
