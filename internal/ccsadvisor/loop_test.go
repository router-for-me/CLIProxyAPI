package ccsadvisor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func loopFixture() Message {
	return Message{"model": "executor", "max_tokens": 500, "system": "caller instructions", "messages": []any{Message{"role": "user", "content": "history sentinel"}}, "tools": []any{Message{"name": "advisor", "type": "advisor_20260301", "model": "ignored"}, Message{"name": "lookup", "input_schema": Message{"type": "object"}}}, "tool_choice": Message{"type": "tool", "name": "advisor"}}
}

func loopText(s string) Message { return Message{"type": "text", "text": s} }
func loopUse(id, name string) Message {
	return Message{"type": "tool_use", "id": id, "name": name, "input": Message{"question": "what now?"}}
}
func loopReply(reason string, blocks ...any) Message {
	return Message{"role": "assistant", "id": "response", "content": blocks, "stop_reason": reason, "usage": Message{"input_tokens": 10, "output_tokens": 2}}
}

func TestRunConsultsAndResumes(t *testing.T) {
	req := loopFixture()
	n := 0
	got, err := Run(context.Background(), req, "specialist", func(_ context.Context, r Message) (Message, error) {
		n++
		switch n {
		case 1:
			tools := r["tools"].([]any)
			def := tools[0].(Message)
			if def["input_schema"] == nil || def["name"] != "advisor" || def["model"] != nil || def["type"] == "advisor_20260301" || !reflect.DeepEqual(tools[1], req["tools"].([]any)[1]) {
				t.Fatalf("tools: %v", tools)
			}
			return loopReply("tool_use", loopText("Before"), loopUse("toolu_1", "advisor")), nil
		case 2:
			data := fmt.Sprint(r["messages"])
			sys := fmt.Sprint(r["system"])
			if r["model"] != "specialist" || r["tools"] != nil || r["tool_choice"] != nil || !strings.Contains(data, "what now?") || !strings.Contains(data, "history sentinel") || strings.Contains(sys, "history sentinel") || !strings.Contains(strings.ToLower(sys), "untrusted") {
				t.Fatalf("advisor request: %v", r)
			}
			return loopReply("end_turn", loopText("first"), loopText("second")), nil
		case 3:
			msgs := r["messages"].([]any)
			if len(msgs) != 3 || r["model"] != "executor" || reflect.DeepEqual(r["tool_choice"], req["tool_choice"]) {
				t.Fatalf("continuation: %v", r)
			}
			if !reflect.DeepEqual(msgs[1].(Message)["content"], []any{loopText("Before"), loopUse("toolu_1", "advisor")}) {
				t.Fatalf("assistant turn: %v", msgs[1])
			}
			result := msgs[2].(Message)["content"].([]any)[0].(Message)
			if msgs[2].(Message)["role"] != "user" || result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" || result["content"] != "first\nsecond" {
				t.Fatalf("result: %v", msgs[2])
			}
			return loopReply("end_turn", loopText("After")), nil
		default:
			t.Fatal("extra call")
			return nil, nil
		}
	})
	if err != nil || n != 3 {
		t.Fatalf("calls=%d err=%v", n, err)
	}
	blocks := got["content"].([]any)
	if len(blocks) != 4 {
		t.Fatalf("blocks %v", blocks)
	}
	use := blocks[1].(Message)
	result := blocks[2].(Message)
	if !reflect.DeepEqual(blocks[0], loopText("Before")) || !reflect.DeepEqual(blocks[3], loopText("After")) || use["type"] != "server_tool_use" || !strings.HasPrefix(fmt.Sprint(use["id"]), "srvtoolu_") || result["type"] != "advisor_tool_result" || result["tool_use_id"] != use["id"] || !reflect.DeepEqual(result["content"], Message{"type": "advisor_result", "text": "first\nsecond"}) {
		t.Fatalf("public response: %v", got)
	}
	if !reflect.DeepEqual(req, loopFixture()) {
		t.Fatalf("mutated request: %v", req)
	}
	usage := got["usage"].(Message)
	if len(usage["iterations"].([]any)) != 3 {
		t.Fatalf("usage: %v", usage)
	}
}

func TestRunHandsSiblingBackAndReplaysItsResult(t *testing.T) {
	req := loopFixture()
	lookup := loopUse("toolu_lookup", "lookup")
	n := 0
	got, err := Run(context.Background(), req, "specialist", func(_ context.Context, r Message) (Message, error) {
		n++
		switch n {
		case 1:
			return loopReply("tool_use", lookup, loopUse("toolu_2", "advisor")), nil
		case 2:
			return loopReply("end_turn", loopText("advice")), nil
		default:
			t.Fatal("continued without client result")
			return nil, nil
		}
	})
	if err != nil || got["stop_reason"] != "tool_use" {
		t.Fatalf("handoff: %v %v", got, err)
	}
	req["messages"] = append(req["messages"].([]any), Message{"role": "assistant", "content": got["content"]}, Message{"role": "user", "content": []any{Message{"type": "tool_result", "tool_use_id": "toolu_lookup", "content": "value"}}})
	_, err = Run(context.Background(), req, "specialist", func(_ context.Context, r Message) (Message, error) {
		msgs := r["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("split parallel tools: %v", msgs)
		}
		a := msgs[1].(Message)["content"].([]any)
		u := msgs[2].(Message)["content"].([]any)
		if len(a) != 2 || len(u) != 2 || !reflect.DeepEqual(a[0], lookup) {
			t.Fatalf("lost parallel tools: %v", msgs)
		}
		ids := map[any]any{}
		for _, v := range u {
			b := v.(Message)
			if b["type"] != "tool_result" {
				t.Fatalf("bad result: %v", b)
			}
			ids[b["tool_use_id"]] = b["content"]
		}
		if a[1].(Message)["type"] != "tool_use" || ids[a[1].(Message)["id"]] != "advice" || ids[lookup["id"]] != "value" {
			t.Fatalf("unmatched results: %v", msgs)
		}
		return loopReply("end_turn", loopText("done")), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunDoesNotInterceptClientDefinedAdvisor(t *testing.T) {
	req := loopFixture()
	req["tools"] = []any{Message{"name": "advisor", "input_schema": Message{"type": "object"}}}
	want := loopReply("tool_use", loopUse("one", "advisor"))
	n := 0
	got, err := Run(context.Background(), req, "specialist", func(_ context.Context, r Message) (Message, error) {
		n++
		if n != 1 || !reflect.DeepEqual(r, req) {
			t.Fatal("intercepted client-defined tool")
		}
		return want, nil
	})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestReplayRedactedAdvisorKeepsTurnWithoutLeakingCiphertext(t *testing.T) {
	const secret = "synthetic-encrypted-advice"
	req := loopFixture()
	archived := []any{Message{"type": "server_tool_use", "name": "advisor", "id": "srvtoolu_old", "input": Message{}}, Message{"type": "advisor_tool_result", "tool_use_id": "srvtoolu_old", "content": Message{"type": "advisor_redacted_result", "encrypted_content": secret}}, loopText("visible conclusion")}
	req["messages"] = []any{turn("user", "old question"), turn("assistant", archived), turn("user", "new question")}
	calls := 0
	_, err := Run(t.Context(), req, "specialist", func(_ context.Context, step Message) (Message, error) {
		calls++
		messages := list(step["messages"])
		if len(messages) != 5 || strings.Contains(fmt.Sprint(messages), secret) || !strings.Contains(fmt.Sprint(messages[3:]), "visible conclusion") || !strings.Contains(fmt.Sprint(messages[4]), "new question") {
			t.Fatalf("invalid replay: %v", messages)
		}
		call := obj(list(obj(messages[1])["content"])[0])
		result := obj(list(obj(messages[2])["content"])[0])
		if call["id"] != result["tool_use_id"] || result["is_error"] != true || !strings.Contains(fmt.Sprint(result["content"]), "unavailable") {
			t.Fatalf("missing redaction marker: %v", result)
		}
		return loopReply("end_turn", loopText("done")), nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d: %v", calls, err)
	}
}

func TestRunDoesNotInventAdvice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply Message
		err   error
	}{{"advisor error", nil, errors.New("unavailable")}, {"empty advice", loopReply("end_turn", loopText("  ")), nil}, {"recursive tool", loopReply("tool_use", loopUse("bad", "advisor")), nil}} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			_, err := Run(context.Background(), loopFixture(), "specialist", func(_ context.Context, r Message) (Message, error) {
				n++
				if n == 1 {
					return loopReply("tool_use", loopUse("one", "advisor")), nil
				}
				if n > 2 {
					t.Fatal("continued on failed advice")
				}
				return tc.reply, tc.err
			})
			if err == nil || tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRunBoundsRepeatedConsultation(t *testing.T) {
	consults, executions := 0, 0
	_, err := Run(context.Background(), loopFixture(), "specialist", func(_ context.Context, r Message) (Message, error) {
		if r["model"] == "specialist" {
			consults++
			return loopReply("end_turn", loopText("advice")), nil
		}
		executions++
		if executions > 3 {
			t.Fatal("unbounded executor loop")
		}
		return loopReply("tool_use", loopUse(fmt.Sprintf("toolu_%d", executions), "advisor")), nil
	})
	if err == nil || consults < 1 || consults > 2 {
		t.Fatalf("consults=%d err=%v", consults, err)
	}
}

func TestRunPreservesClientToolWhenAdvisorUnavailable(t *testing.T) {
	req := loopFixture()
	lookup := loopUse("toolu_lookup", "lookup")
	n := 0
	got, err := Run(context.Background(), req, "specialist", func(_ context.Context, r Message) (Message, error) {
		n++
		switch n {
		case 1:
			return loopReply("tool_use", loopUse("one", "advisor"), lookup), nil
		case 2:
			return nil, errors.New("private backend details")
		default:
			t.Fatal("continued while sibling pending")
			return nil, nil
		}
	})
	if err != nil || n != 2 || got["stop_reason"] != "tool_use" {
		t.Fatalf("handoff: %v, %v", got, err)
	}
	blocks := got["content"].([]any)
	if len(blocks) != 3 || !reflect.DeepEqual(blocks[1], lookup) || !reflect.DeepEqual(blocks[2].(Message)["content"], Message{"type": "advisor_tool_result_error", "error_code": "unavailable"}) {
		t.Fatalf("incorrect public failure: %v", blocks)
	}
	req["messages"] = append(req["messages"].([]any), Message{"role": "assistant", "content": blocks}, Message{"role": "user", "content": []any{Message{"type": "tool_result", "tool_use_id": "toolu_lookup", "content": "client answer"}}})
	_, err = Run(context.Background(), req, "specialist", func(_ context.Context, r Message) (Message, error) {
		msgs := r["messages"].([]any)
		results := msgs[2].(Message)["content"].([]any)
		if len(msgs) != 3 || len(results) != 2 || results[0].(Message)["is_error"] != true {
			t.Fatalf("invalid upstream failure: %v", msgs)
		}
		return loopReply("end_turn", loopText("done")), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
