package executor

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"google.golang.org/protobuf/encoding/protowire"
)

type devinContentCheckpointReader func([]byte) (int, error)

func (r devinContentCheckpointReader) Read(p []byte) (int, error) {
	return r(p)
}

func TestDevinStreamEmitsContentBeforeThinkingCloses(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(string(format), func(t *testing.T) {
			// Each checkpoint runs synchronously when the executor requests the next
			// frame, after all output for the previous frame has been enqueued.
			// EOS is unavailable until every pre-EOS assertion has run.
			out := make(chan cliproxyexecutor.StreamChunk, 64)
			var content []string
			var thinking strings.Builder
			drain := func() {
				for len(out) > 0 {
					chunk := <-out
					if chunk.Err != nil {
						t.Fatalf("unexpected stream error: %v", chunk.Err)
					}
					for _, line := range strings.Split(string(chunk.Payload), "\n") {
						data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
						if !ojson.Valid(data) {
							continue
						}
						event := ojson.Parse(data)
						var text string
						switch format {
						case sdktranslator.FormatOpenAI:
							thinking.WriteString(event.Get("choices.0.delta.reasoning_content").String())
							text = event.Get("choices.0.delta.content").String()
						case sdktranslator.FormatOpenAIResponse:
							switch event.Get("type").String() {
							case "response.reasoning_summary_text.delta":
								thinking.WriteString(event.Get("delta").String())
							case "response.output_text.delta":
								text = event.Get("delta").String()
							}
						}
						if text != "" {
							content = append(content, text)
						}
					}
				}
			}

			stages := []struct {
				name  string
				field protowire.Number
				text  string
				want  []string
			}{
				{name: "thinking", field: 9, text: "planning"},
				{name: "incomplete UTF-8", field: 3, text: "甲"[:1]},
				{name: "first complete UTF-8 chunk", field: 3, text: "甲"[1:], want: []string{"甲"}},
				{name: "second complete UTF-8 chunk", field: 3, text: "乙", want: []string{"甲", "乙"}},
			}
			var readers []io.Reader
			checkpoints := 0
			for _, stage := range stages {
				frame := protowire.AppendTag(nil, stage.field, protowire.BytesType)
				frame = protowire.AppendString(frame, stage.text)
				readers = append(readers, bytes.NewReader(helps.WrapConnectEnvelope(frame)))
				readers = append(readers, devinContentCheckpointReader(func(_ []byte) (int, error) {
					drain()
					checkpoints++
					if thinking.String() != "planning" {
						t.Fatalf("%s: thinking = %q, want planning before EOS", stage.name, thinking.String())
					}
					if !slices.Equal(content, stage.want) {
						t.Errorf("%s: content deltas before upstream EOS = %q, want %q", stage.name, content, stage.want)
					}
					return 0, io.EOF
				}))
			}
			readers = append(readers, bytes.NewReader(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))))

			original := []byte(`{"model":"devin/swe-2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			if format == sdktranslator.FormatOpenAIResponse {
				original = []byte(`{"model":"devin/swe-2","stream":true,"input":"hi"}`)
			}
			opts := cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: original}
			req := cliproxyexecutor.Request{
				Model:   "devin/swe-2",
				Payload: sdktranslator.TranslateRequest(format, sdktranslator.FormatInteractions, "devin/swe-2", original, true),
			}
			e := &DevinExecutor{}
			e.streamDevinFrames(context.Background(), io.MultiReader(readers...), req, opts, "swe-2-high", format, nil, out)
			drain()
			if checkpoints != len(stages) {
				t.Fatalf("reached %d checkpoints, want %d", checkpoints, len(stages))
			}
			if !slices.Equal(content, []string{"甲", "乙"}) {
				t.Errorf("content deltas after EOS = %q, want [甲 乙] without loss or duplication", content)
			}
		})
	}
}

func devinContentTestFrame(field protowire.Number, text string) []byte {
	frame := protowire.AppendTag(nil, field, protowire.BytesType)
	return helps.WrapConnectEnvelope(protowire.AppendString(frame, text))
}

func devinContentTestEvents(out <-chan cliproxyexecutor.StreamChunk) ([]ojson.Result, []error) {
	var events []ojson.Result
	var errs []error
	for len(out) > 0 {
		chunk := <-out
		if chunk.Err != nil {
			errs = append(errs, chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if ojson.Valid(data) {
				events = append(events, ojson.Parse(data))
			}
		}
	}
	return events, errs
}

func TestDevinStreamContentLateSignatures(t *testing.T) {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	signature := base64.URLEncoding.EncodeToString(payload)
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		for _, split := range []bool{false, true} {
			for _, ending := range []string{"eos", "trailer_error", "truncated", "read_error"} {
				name := string(format) + "/late/" + ending
				if split {
					name = string(format) + "/split/" + ending
				}
				t.Run(name, func(t *testing.T) {
					out := make(chan cliproxyexecutor.StreamChunk, 64)
					var prefix bytes.Buffer
					prefix.Write(devinContentTestFrame(9, "planning"))
					tailSignature := signature
					if split {
						prefix.Write(devinContentTestFrame(10, signature[:20]))
						tailSignature = signature[20:]
					}
					prefix.Write(devinContentTestFrame(3, "甲"))
					var events []ojson.Result
					checkpointReached := false
					checkpoint := devinContentCheckpointReader(func(_ []byte) (int, error) {
						checkpointReached = true
						var errs []error
						events, errs = devinContentTestEvents(out)
						if len(errs) > 0 {
							t.Fatalf("unexpected pre-signature errors: %v", errs)
						}
						var text strings.Builder
						for _, event := range events {
							if format == sdktranslator.FormatOpenAI {
								text.WriteString(event.Get("choices.0.delta.content").String())
							} else {
								if event.Get("type").String() == "response.output_text.delta" {
									text.WriteString(event.Get("delta").String())
								}
								if event.Get("type").String() == "response.output_item.done" && event.Get("item.type").String() == "reasoning" {
									t.Error("reasoning item finalized before its late signature")
								}
							}
						}
						if text.String() != "甲" {
							t.Errorf("content before late signature and termination = %q, want 甲", text.String())
						}
						return 0, io.EOF
					})
					var suffix bytes.Buffer
					suffix.Write(devinContentTestFrame(10, tailSignature))
					switch ending {
					case "eos":
						suffix.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
					case "trailer_error":
						suffix.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{"error":{"code":"internal","message":"upstream failed"}}`)))
					case "read_error":
						suffix.Write([]byte{0, 0}) // Incomplete frame header causes io.ErrUnexpectedEOF.
					}
					e := &DevinExecutor{}
					e.streamDevinFrames(context.Background(), io.MultiReader(&prefix, checkpoint, &suffix), cliproxyexecutor.Request{Model: "devin/swe-2"}, cliproxyexecutor.Options{SourceFormat: format}, "swe-2-high", format, nil, out)
					remaining, errs := devinContentTestEvents(out)
					events = append(events, remaining...)
					if !checkpointReached {
						t.Fatal("pre-signature checkpoint not reached")
					}
					wantError := ending != "eos"
					if (len(errs) == 1) != wantError || len(errs) > 1 {
						t.Errorf("stream errors = %v, want exactly one error: %v", errs, wantError)
					}
					var reasoningDone, completedReasoning ojson.Result
					doneCount, messageDoneCount, completedCount, failedCount := 0, 0, 0, 0
					var text strings.Builder
					for _, event := range events {
						if format == sdktranslator.FormatOpenAI {
							text.WriteString(event.Get("choices.0.delta.content").String())
							if event.Get("error").Exists() {
								failedCount++
							}
							continue
						}
						switch event.Get("type").String() {
						case "response.output_text.delta":
							text.WriteString(event.Get("delta").String())
						case "response.output_item.done":
							if event.Get("item.type").String() == "message" {
								messageDoneCount++
							}
							if event.Get("item.type").String() == "reasoning" {
								reasoningDone = event.Get("item")
								doneCount++
							}
						case "response.completed":
							completedCount++
							completedReasoning = event.Get("response.output.0")
						case "response.failed":
							failedCount++
						}
					}
					if text.String() != "甲" {
						t.Errorf("final content = %q, want 甲 without duplication", text.String())
					}
					if (failedCount == 1) != wantError || failedCount > 1 {
						t.Errorf("failure event count = %d, want failure: %v", failedCount, wantError)
					}
					if format == sdktranslator.FormatOpenAIResponse {
						if doneCount != 1 || reasoningDone.Get("encrypted_content").String() != signature {
							t.Errorf("reasoning done count = %d, item = %s; want full signature %q", doneCount, reasoningDone.Raw, signature)
						}
						if messageDoneCount != 1 {
							t.Errorf("message done count = %d, want 1", messageDoneCount)
						}
						if wantError {
							if events[len(events)-1].Get("type").String() != "response.failed" {
								t.Error("failure must terminate the stream after closing open items")
							}
							if completedCount != 0 {
								t.Error("response.completed emitted after upstream failure")
							}
						} else if completedCount != 1 || completedReasoning.Raw != reasoningDone.Raw {
							t.Errorf("completed reasoning = %s, want same item as done = %s", completedReasoning.Raw, reasoningDone.Raw)
						}
					}
				})
			}
		}
	}
}

func TestDevinStreamContentDoesNotOvertakeQueuedTool(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(string(format), func(t *testing.T) {
			var frames bytes.Buffer
			frames.Write(devinContentTestFrame(9, "planning"))
			var tool []byte
			for i, value := range []string{"call_1", "bash", `{"cmd":"ls"}`} {
				tool = protowire.AppendTag(tool, protowire.Number(i+1), protowire.BytesType)
				tool = protowire.AppendString(tool, value)
			}
			frames.Write(devinContentTestFrame(6, string(tool)))
			frames.Write(devinContentTestFrame(3, "甲"))
			out := make(chan cliproxyexecutor.StreamChunk, 64)
			checkpointReached := false
			checkpoint := devinContentCheckpointReader(func(_ []byte) (int, error) {
				checkpointReached = true
				events, errs := devinContentTestEvents(out)
				if len(errs) > 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				for _, event := range events {
					if event.Get("choices.0.delta.content").String() != "" || event.Get("choices.0.delta.tool_calls").Exists() || event.Get("type").String() == "response.output_text.delta" || event.Get("item.type").String() == "function_call" {
						t.Errorf("queued tool or following text emitted before thinking signature: %s", event.Raw)
					}
				}
				return 0, io.EOF
			})
			var tail bytes.Buffer
			tail.Write(devinContentTestFrame(10, "CAQS-late-tool-signature"))
			tail.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
			e := &DevinExecutor{}
			e.streamDevinFrames(context.Background(), io.MultiReader(&frames, checkpoint, &tail), cliproxyexecutor.Request{Model: "devin/swe-2"}, cliproxyexecutor.Options{SourceFormat: format}, "swe-2-high", format, nil, out)
			events, errs := devinContentTestEvents(out)
			if !checkpointReached || len(errs) > 0 {
				t.Fatalf("checkpoint = %v, errors = %v", checkpointReached, errs)
			}
			toolSeen, textSeen := false, false
			for _, event := range events {
				if event.Get("choices.0.delta.tool_calls.0.id").String() == "call_1" || (event.Get("type").String() == "response.output_item.done" && event.Get("item.call_id").String() == "call_1") {
					toolSeen = true
				}
				if event.Get("choices.0.delta.content").String() == "甲" || (event.Get("type").String() == "response.output_text.delta" && event.Get("delta").String() == "甲") {
					textSeen = true
					if !toolSeen {
						t.Error("content overtook queued tool")
					}
				}
			}
			if !toolSeen || !textSeen {
				t.Errorf("tool seen = %v, text seen = %v; want both", toolSeen, textSeen)
			}
		})
	}
}

func TestDevinGPTReasoningRoundTrip(t *testing.T) {
	const model = "gpt-6-1-sol"
	const format = sdktranslator.FormatOpenAIResponse
	token := make([]byte, 73)
	token[0] = 0x80
	sig := ` [{"id":"rs_first","type":"reasoning","summary":[],"encrypted_content":"` + base64.URLEncoding.EncodeToString(token) + `"}] `
	var frames bytes.Buffer

	frames.Write(devinContentTestFrame(10, sig[:20]))
	frames.Write(devinContentTestFrame(10, sig[20:]))
	frames.Write(devinContentTestFrame(21, "openai"))
	frames.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
	e := NewDevinExecutor(&config.Config{})
	ctx := t.Context()
	assertReplay := func(t *testing.T, item ojson.Result) {
		t.Helper()
		if item.Get("encrypted_content").String() == "" {
			t.Fatal("returned reasoning item has empty encrypted_content")
		}
		next := ojson.NewDocString(`{"input":[{"type":"message","role":"user","content":"go"},{},{},{"type":"function_call","call_id":"c","name":"run","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"ok"}]}`)
		_ = next.SetRaw("input.1", item.Raw)
		// Preserve a second distinct thought, even without an intervening user turn.
		_ = next.SetRaw("input.2", strings.Replace(item.Raw, "rs_first", "rs_second", 1))
		body := next.Bytes()
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}
		_, _, payload, err := e.prepareDevinHTTPRequest(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: body}, cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: body})
		if err != nil {
			t.Fatal(err)
		}
		prompts := ojson.GetBytesNoCopy(payload, "prompts").Array()
		if len(prompts) != 4 {
			t.Fatalf("got %d prompts, want user, two signed assistant prompts and tool result", len(prompts))
		}
		for i, p := range prompts[1:3] {
			want := sig
			if i == 1 {
				want = strings.Replace(sig, "rs_first", "rs_second", 1)
			}
			if p.Get("signature_type").String() != "openai" || p.Get("signature").String() != base64.StdEncoding.EncodeToString([]byte(want)) {
				t.Fatalf("upstream prompt lost typed signature: %s", p.Raw)
			}
		}
		other := sdktranslator.TranslateRequest(format, sdktranslator.FormatInteractions, "gemini-3.5-flash", body, false)
		for _, step := range ojson.GetBytesNoCopy(other, "input").Array() {
			if step.Get("type").String() == "thought" {
				t.Fatal("replayed Devin reasoning to Gemini")
			}
		}
	}
	for _, tc := range []struct {
		name  string
		first []byte
	}{
		{"stream", devinContentTestFrame(9, "plan")},
		{"stream_content_first_no_summary", devinContentTestFrame(3, "answer")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := make(chan cliproxyexecutor.StreamChunk, 64)
			e.streamDevinFrames(ctx, io.MultiReader(bytes.NewReader(tc.first), bytes.NewReader(frames.Bytes())), cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{SourceFormat: format}, model, format, nil, out)
			events, errs := devinContentTestEvents(out)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			var done, completed ojson.Result
			for _, event := range events {
				switch event.Get("type").String() {
				case "response.output_item.done":
					if event.Get("item.type").String() == "reasoning" {
						done = event.Get("item")
					}
				case "response.completed":
					for _, item := range event.Get("response.output").Array() {
						if item.Get("type").String() == "reasoning" {
							completed = item
						}
					}
				}
			}
			if !done.Exists() {
				t.Fatal("missing reasoning item after signature frame")
			}
			if done.Raw != completed.Raw {
				t.Fatal("completed reasoning differs from done item")
			}
			assertReplay(t, done)
		})
	}
	t.Run("nonstream", func(t *testing.T) {
		interactions, _, err := consumeDevinFramesToInteractions(bytes.NewReader(frames.Bytes()), model, model)
		if err != nil {
			t.Fatal(err)
		}
		var param any
		response := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatInteractions, format, model, nil, nil, interactions, &param)
		assertReplay(t, ojson.GetBytesNoCopy(response, "output.0"))
	})
}
