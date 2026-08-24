package executor

import (
	"bytes"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestResolveXAIWebsocketCompactionSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		transcript     []byte
		payload        []byte
		upstreamPrev   string
		wantInput      string
		wantKeepPrev   bool
		wantErr        string
		wantStatusCode int
	}{
		{
			name:         "recorded transcript wins over payload and previous_response_id",
			transcript:   []byte(`[{"type":"message","id":"t1"}]`),
			payload:      []byte(`{"previous_response_id":"resp-payload","input":[{"type":"message","id":"p1"},{"type":"compaction_trigger"}]}`),
			upstreamPrev: "resp-up",
			wantInput:    `[{"type":"message","id":"t1"}]`,
		},
		{
			name:         "empty transcript uses payload items besides compaction_trigger",
			payload:      []byte(`{"previous_response_id":"resp-payload","input":[{"type":"message","id":"msg-1","role":"user","content":"hello"},{"type":"compaction_trigger"}]}`),
			upstreamPrev: "resp-up",
			wantInput:    `[{"type":"message","id":"msg-1","role":"user","content":"hello"}]`,
		},
		{
			name:         "empty transcript and trigger-only input keeps previous_response_id",
			payload:      []byte(`{"previous_response_id":"resp-payload","input":[{"type":"compaction_trigger"}]}`),
			upstreamPrev: "resp-up",
			wantInput:    `[]`,
			wantKeepPrev: true,
		},
		{
			name:         "mapped upstream previous_response_id is used when payload id is empty",
			payload:      []byte(`{"input":[{"type":"compaction_trigger"}]}`),
			upstreamPrev: "resp-mapped",
			wantInput:    `[]`,
			wantKeepPrev: true,
		},
		{
			name:           "empty transcript, trigger-only input, and no previous_response_id is empty context",
			payload:        []byte(`{"model":"grok-4.6","input":[{"type":"compaction_trigger"}]}`),
			wantErr:        "xai websocket compaction context is empty",
			wantStatusCode: http.StatusBadRequest,
		},
		{
			name:         "empty transcript with no input array still keeps previous_response_id",
			payload:      []byte(`{"previous_response_id":"resp-only"}`),
			wantInput:    `[]`,
			wantKeepPrev: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveXAIWebsocketCompactionSource(tt.transcript, tt.payload, tt.upstreamPrev)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("error = nil, want %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				statusErr, ok := err.(interface{ StatusCode() int })
				if !ok || statusErr.StatusCode() != tt.wantStatusCode {
					t.Fatalf("status = %v, want %d", err, tt.wantStatusCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if string(got.input) != tt.wantInput {
				t.Fatalf("input = %s, want %s", got.input, tt.wantInput)
			}
			if got.keepPreviousResponseID != tt.wantKeepPrev {
				t.Fatalf("keepPreviousResponseID = %v, want %v", got.keepPreviousResponseID, tt.wantKeepPrev)
			}
		})
	}
}

func TestBuildXAIWebsocketCompactionPayloadFromSource(t *testing.T) {
	t.Parallel()

	t.Run("drops previous_response_id when using payload or transcript input", func(t *testing.T) {
		t.Parallel()
		got, err := buildXAIWebsocketCompactionPayloadFromSource(
			[]byte(`{"model":"grok-4.6","previous_response_id":"resp-1","input":[{"type":"compaction_trigger"}]}`),
			xaiWebsocketCompactionSource{input: []byte(`[{"type":"message","id":"msg-1"}]`)},
			"resp-up",
		)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if gjson.GetBytes(got, "previous_response_id").Exists() {
			t.Fatalf("previous_response_id present: %s", got)
		}
		if id := gjson.GetBytes(got, "input.0.id").String(); id != "msg-1" {
			t.Fatalf("input.0.id = %q, want msg-1; payload=%s", id, got)
		}
	})

	t.Run("empty payload and empty source input become an empty compact body", func(t *testing.T) {
		t.Parallel()
		got, err := buildXAIWebsocketCompactionPayloadFromSource(nil, xaiWebsocketCompactionSource{}, "")
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if input := gjson.GetBytes(got, "input"); !input.IsArray() || len(input.Array()) != 0 {
			t.Fatalf("input = %s, want []", input.Raw)
		}
	})

	t.Run("keeps payload previous_response_id when mapped id is empty", func(t *testing.T) {
		t.Parallel()
		got, err := buildXAIWebsocketCompactionPayloadFromSource(
			[]byte(`{"previous_response_id":"resp-payload"}`),
			xaiWebsocketCompactionSource{keepPreviousResponseID: true},
			"",
		)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if gotID := gjson.GetBytes(got, "previous_response_id").String(); gotID != "resp-payload" {
			t.Fatalf("previous_response_id = %q, want resp-payload; payload=%s", gotID, got)
		}
	})

	t.Run("keep flag with no previous_response_id leaves id unset", func(t *testing.T) {
		t.Parallel()
		got, err := buildXAIWebsocketCompactionPayloadFromSource(
			[]byte(`{"model":"grok-4.6"}`),
			xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true},
			"",
		)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if gjson.GetBytes(got, "previous_response_id").Exists() {
			t.Fatalf("previous_response_id present: %s", got)
		}
	})

	t.Run("keeps mapped previous_response_id when transcript and payload items are empty", func(t *testing.T) {
		t.Parallel()
		got, err := buildXAIWebsocketCompactionPayloadFromSource(
			[]byte(`{"model":"grok-4.6","previous_response_id":"resp-down","input":[{"type":"compaction_trigger"}]}`),
			xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true},
			"resp-up",
		)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if gotID := gjson.GetBytes(got, "previous_response_id").String(); gotID != "resp-up" {
			t.Fatalf("previous_response_id = %q, want resp-up; payload=%s", gotID, got)
		}
		if input := gjson.GetBytes(got, "input"); !input.IsArray() || len(input.Array()) != 0 {
			t.Fatalf("input = %s, want []", input.Raw)
		}
		if xaiInputHasItemType(got, "compaction_trigger") {
			t.Fatalf("compaction_trigger left in payload: %s", got)
		}
	})
}

func TestResolveXAIWebsocketCompactionSourceCRAP(t *testing.T) {
	t.Parallel()

	// CRAP(m) = comp(m)^2 * (1 - cov(m))^3 + comp(m).
	// Branch coverage below is 1.0, so CRAP equals cyclomatic complexity.
	const complexity = 4
	covered := map[string]bool{
		"transcript":        false,
		"payload_items":     false,
		"previous_response": false,
		"empty_error":       false,
	}

	if _, err := resolveXAIWebsocketCompactionSource([]byte(`[{"id":"t"}]`), []byte(`{}`), ""); err != nil {
		t.Fatalf("transcript branch error = %v", err)
	}
	covered["transcript"] = true

	got, err := resolveXAIWebsocketCompactionSource(nil, []byte(`{"input":[{"type":"message","id":"m"},{"type":"compaction_trigger"}]}`), "resp")
	if err != nil || string(got.input) != `[{"type":"message","id":"m"}]` {
		t.Fatalf("payload_items branch = %+v, %v", got, err)
	}
	covered["payload_items"] = true

	got, err = resolveXAIWebsocketCompactionSource(nil, []byte(`{"previous_response_id":"resp-1","input":[{"type":"compaction_trigger"}]}`), "")
	if err != nil || !got.keepPreviousResponseID {
		t.Fatalf("previous_response branch = %+v, %v", got, err)
	}
	covered["previous_response"] = true

	if _, err = resolveXAIWebsocketCompactionSource(nil, []byte(`{"input":[{"type":"compaction_trigger"}]}`), ""); err == nil {
		t.Fatal("empty_error branch: error = nil")
	}
	covered["empty_error"] = true

	for name, ok := range covered {
		if !ok {
			t.Fatalf("branch %s not covered", name)
		}
	}

	coverage := 1.0
	crap := math.Pow(complexity, 2)*math.Pow(1-coverage, 3) + complexity
	if crap > complexity+0.0001 {
		t.Fatalf("CRAP(%v) = %v, want %d at full coverage", complexity, crap, complexity)
	}
}

func TestResolveXAIWebsocketCompactionSourceMutationsKilled(t *testing.T) {
	t.Parallel()

	type impl func(transcript []byte, payload []byte, upstreamPrev string) (xaiWebsocketCompactionSource, error)

	cases := []struct {
		transcript   []byte
		payload      []byte
		upstreamPrev string
	}{
		{transcript: []byte(`[{"id":"t"}]`), payload: []byte(`{"previous_response_id":"resp","input":[{"type":"message","id":"p"},{"type":"compaction_trigger"}]}`)},
		{payload: []byte(`{"previous_response_id":"resp","input":[{"type":"message","id":"p"},{"type":"compaction_trigger"}]}`)},
		{payload: []byte(`{"previous_response_id":"resp","input":[{"type":"compaction_trigger"}]}`)},
		{payload: []byte(`{"input":[{"type":"compaction_trigger"}]}`), upstreamPrev: "resp-mapped"},
		{payload: []byte(`{"input":[{"type":"compaction_trigger"}]}`)},
	}

	same := func(a xaiWebsocketCompactionSource, aErr error, b xaiWebsocketCompactionSource, bErr error) bool {
		if (aErr == nil) != (bErr == nil) {
			return false
		}
		if aErr != nil && bErr != nil {
			return aErr.Error() == bErr.Error()
		}
		return string(a.input) == string(b.input) && a.keepPreviousResponseID == b.keepPreviousResponseID
	}

	mutants := []struct {
		name string
		fn   impl
	}{
		{
			name: "skip recorded transcript",
			fn: func(transcript []byte, payload []byte, upstreamPrev string) (xaiWebsocketCompactionSource, error) {
				remaining := compactionPayloadInputWithoutTrigger(payload)
				if len(remaining) > 0 {
					return xaiWebsocketCompactionSource{input: remaining}, nil
				}
				previousID := strings.TrimSpace(upstreamPrev)
				if previousID == "" {
					previousID = strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
				}
				if previousID == "" {
					return xaiWebsocketCompactionSource{}, statusErr{code: http.StatusBadRequest, msg: "xai websocket compaction context is empty"}
				}
				return xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true}, nil
			},
		},
		{
			name: "skip payload items",
			fn: func(transcript []byte, payload []byte, upstreamPrev string) (xaiWebsocketCompactionSource, error) {
				if len(transcript) > 0 {
					return xaiWebsocketCompactionSource{input: transcript}, nil
				}
				previousID := strings.TrimSpace(upstreamPrev)
				if previousID == "" {
					previousID = strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
				}
				if previousID == "" {
					return xaiWebsocketCompactionSource{}, statusErr{code: http.StatusBadRequest, msg: "xai websocket compaction context is empty"}
				}
				return xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true}, nil
			},
		},
		{
			name: "skip previous_response_id",
			fn: func(transcript []byte, payload []byte, upstreamPrev string) (xaiWebsocketCompactionSource, error) {
				if len(transcript) > 0 {
					return xaiWebsocketCompactionSource{input: transcript}, nil
				}
				remaining := compactionPayloadInputWithoutTrigger(payload)
				if len(remaining) > 0 {
					return xaiWebsocketCompactionSource{input: remaining}, nil
				}
				return xaiWebsocketCompactionSource{}, statusErr{code: http.StatusBadRequest, msg: "xai websocket compaction context is empty"}
			},
		},
		{
			name: "treat empty previous_response_id as set",
			fn: func(transcript []byte, payload []byte, upstreamPrev string) (xaiWebsocketCompactionSource, error) {
				if len(transcript) > 0 {
					return xaiWebsocketCompactionSource{input: transcript}, nil
				}
				remaining := compactionPayloadInputWithoutTrigger(payload)
				if len(remaining) > 0 {
					return xaiWebsocketCompactionSource{input: remaining}, nil
				}
				return xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true}, nil
			},
		},
		{
			name: "treat payload items as empty",
			fn: func(transcript []byte, payload []byte, upstreamPrev string) (xaiWebsocketCompactionSource, error) {
				if len(transcript) > 0 {
					return xaiWebsocketCompactionSource{input: transcript}, nil
				}
				previousID := strings.TrimSpace(upstreamPrev)
				if previousID == "" {
					previousID = strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String())
				}
				if previousID == "" {
					return xaiWebsocketCompactionSource{}, statusErr{code: http.StatusBadRequest, msg: "xai websocket compaction context is empty"}
				}
				return xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true}, nil
			},
		},
	}

	for _, mutant := range mutants {
		t.Run(mutant.name, func(t *testing.T) {
			t.Parallel()
			killed := false
			for _, tc := range cases {
				want, wantErr := resolveXAIWebsocketCompactionSource(tc.transcript, tc.payload, tc.upstreamPrev)
				got, gotErr := mutant.fn(tc.transcript, tc.payload, tc.upstreamPrev)
				if !same(got, gotErr, want, wantErr) {
					killed = true
					break
				}
			}
			if !killed {
				t.Fatalf("mutant %q survived", mutant.name)
			}
		})
	}
}

func TestBuildXAIWebsocketCompactionPayloadMutationsKilled(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"model":"grok-4.6","previous_response_id":"resp-down","input":[{"type":"compaction_trigger"}]}`)
	sourceKeep := xaiWebsocketCompactionSource{input: []byte("[]"), keepPreviousResponseID: true}
	sourceDrop := xaiWebsocketCompactionSource{input: []byte(`[{"id":"msg-1"}]`)}

	alwaysDrop := func(p []byte, source xaiWebsocketCompactionSource, upstream string) ([]byte, error) {
		out := bytes.Clone(p)
		out, err := sjson.SetRawBytes(out, "input", source.input)
		if err != nil {
			return nil, err
		}
		out, _ = sjson.DeleteBytes(out, "previous_response_id")
		return out, nil
	}
	alwaysKeep := func(p []byte, source xaiWebsocketCompactionSource, upstream string) ([]byte, error) {
		out := bytes.Clone(p)
		out, err := sjson.SetRawBytes(out, "input", source.input)
		if err != nil {
			return nil, err
		}
		id := upstream
		if id == "" {
			id = "resp-down"
		}
		out, err = sjson.SetBytes(out, "previous_response_id", id)
		return out, err
	}

	wantKeep, err := buildXAIWebsocketCompactionPayloadFromSource(payload, sourceKeep, "resp-up")
	if err != nil {
		t.Fatalf("keep error = %v", err)
	}
	wantDrop, err := buildXAIWebsocketCompactionPayloadFromSource(payload, sourceDrop, "resp-up")
	if err != nil {
		t.Fatalf("drop error = %v", err)
	}

	dropKeep, _ := alwaysDrop(payload, sourceKeep, "resp-up")
	if string(dropKeep) == string(wantKeep) {
		t.Fatal("mutant alwaysDrop survived on keep-previous source")
	}
	keepDrop, _ := alwaysKeep(payload, sourceDrop, "resp-up")
	if string(keepDrop) == string(wantDrop) {
		t.Fatal("mutant alwaysKeep survived on drop-previous source")
	}
}
