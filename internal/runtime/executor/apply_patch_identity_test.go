package executor

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Check every lifecycle identity, not merely the final snapshot or event counts.
func assertApplyPatchIdentityLifecycle(t *testing.T, events [][]byte, id, call, input string, index int, fragments []string) {
	t.Helper()
	counts := map[string]int{}
	var actual []string
	for _, event := range events {
		root := gjson.ParseBytes(event)
		kind := root.Get("type").String()
		identity, idKey := root, "item_id"
		switch kind {
		case "response.output_item.added", "response.output_item.done":
			if root.Get("item.type").String() != "custom_tool_call" {
				continue
			}
			identity, idKey = root.Get("item"), "id"
			if root.Get("output_index").Int() != int64(index) {
				t.Fatalf("wrong output index: %s", event)
			}
			if kind == "response.output_item.added" && identity.Get("input").String() != "" {
				t.Fatalf("nonempty added input: %s", event)
			}
			if kind == "response.output_item.done" && identity.Get("input").String() != input {
				t.Fatalf("item input: %s", event)
			}
		case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
			if root.Get("output_index").Int() != int64(index) {
				t.Fatalf("wrong input index: %s", event)
			}
			if kind == "response.custom_tool_call_input.delta" {
				actual = append(actual, root.Get("delta").String())
			}
			if kind == "response.custom_tool_call_input.done" && root.Get("input").String() != input {
				t.Fatalf("done input: %s", event)
			}
		case "response.completed":
			identity, idKey = root.Get("response.output.0"), "id"
			if identity.Get("input").String() != input {
				t.Fatalf("final input: %s", event)
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.failed":
			t.Fatalf("unsafe patch event: %s", event)
		default:
			continue
		}
		counts[kind]++
		if identity.Get(idKey).String() != id || identity.Get("call_id").String() != call {
			t.Fatalf("unstable identity: %s", event)
		}
	}
	for _, kind := range []string{"response.output_item.added", "response.custom_tool_call_input.done", "response.output_item.done", "response.completed"} {
		if counts[kind] != 1 {
			t.Fatalf("missing/duplicate lifecycle: %v", counts)
		}
	}
	if strings.Join(actual, "|") != strings.Join(fragments, "|") || len(actual) != len(fragments) {
		t.Fatalf("source fragment replay: got %q want %q", actual, fragments)
	}
}

func applyPatchIdentitySource(interactions bool, first, boundary string, snapshot bool) [][]byte {
	id, call := "", ""
	if first == "item" {
		id = "a"
	}
	if first == "call" {
		call = "c"
	}
	args := `{"input":"pq"}`
	var events [][]byte
	if interactions {
		events = append(events, []byte(fmt.Sprintf(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":%q,"call_id":%q,"name":"apply_patch"}}`, id, call)))
		if !snapshot {
			for _, fragment := range []string{`{"input":"p`, `q"}`} {
				events = append(events, []byte(fmt.Sprintf(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":%q}}`, fragment)))
			}
		}
		if strings.HasPrefix(first, "neither-") {
			key, value := "id", "a"
			if first == "neither-call-first" {
				key, value = "call_id", "c"
			}
			events = append(events, []byte(fmt.Sprintf(`{"event_type":"step.start","index":0,"step":{"type":"function_call","name":"apply_patch",%q:%q}}`, key, value)))
		}
		if boundary == "delta" {
			events = append(events, []byte(`{"event_type":"step.delta","index":0,"step":{"id":"a","call_id":"c"},"delta":{"type":"arguments_delta","arguments":""}}`))
		}
		step := fmt.Sprintf(`{"index":0,"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":%s}`, args)
		if boundary == "item" || boundary == "delta" {
			events = append(events, []byte(`{"event_type":"step.stop","index":0,"step":`+step+`}`))
		} else {
			events = append(events, []byte(`{"event_type":"step.stop","index":0}`))
		}
		events = append(events, []byte(`{"event_type":"interaction.completed","interaction":{"steps":[`+step+`]}}`))
	} else {
		events = append(events, []byte(fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":%q,"call_id":%q,"name":"apply_patch","arguments":""}}`, id, call)))
		if !snapshot {
			for _, fragment := range []string{`{"input":"p`, `q"}`} {
				events = append(events, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%q}`, fragment)))
			}
		}
		if strings.HasPrefix(first, "neither-") {
			key, value := "item_id", "a"
			if first == "neither-call-first" {
				key, value = "call_id", "c"
			}
			events = append(events, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,%q:%q,"delta":""}`, key, value)))
		}
		if boundary == "delta" {
			events = append(events, []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"a","call_id":"c","delta":""}`))
		}
		events = append(events, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.done","output_index":0,"arguments":%q}`, args)))
		item := fmt.Sprintf(`{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":%q}`, args)
		if boundary == "item" || boundary == "delta" {
			events = append(events, []byte(`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`))
		}
		events = append(events, []byte(`{"type":"response.completed","response":{"output":[`+item+`]}}`))
	}
	return events
}

func TestApplyPatchNamedLateIdentityActualTransports(t *testing.T) {
	for _, transport := range []string{"xai", "meta", "kimi", "interactions", "ws-sse", "ws-raw"} {
		for _, first := range []string{"item", "call", "neither", "neither-item-first", "neither-call-first"} {
			for _, boundary := range []string{"delta", "item", "terminal"} {
				for _, snapshot := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/snapshot=%v", transport, first, boundary, snapshot), func(t *testing.T) {
						interactions := transport == "interactions"
						source := applyPatchIdentitySource(interactions, first, boundary, snapshot)
						exec, auth := applyPatchIdentityExecutor(t, transport, source)
						ctx := t.Context()
						if transport == "ws-raw" {
							ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
						}
						stream, errExecuteStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
						if errExecuteStream != nil {
							t.Fatal(errExecuteStream)
						}
						var events [][]byte
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							events = append(events, applyPatchTestPayloads(chunk.Payload)...)
						}
						fragments := []string{"p", "q"}
						if snapshot {
							fragments = nil
							// Interactions retains its existing one-snapshot compatibility delta.
							if interactions {
								fragments = []string{"pq"}
							}
						}
						assertApplyPatchIdentityLifecycle(t, events, "a", "c", "pq", 0, fragments)
					})
				}
			}
		}
	}
}

func applyPatchIdentityExecutor(t *testing.T, transport string, source [][]byte) (cliproxyauth.ProviderExecutor, *cliproxyauth.Auth) {
	t.Helper()
	ws := strings.HasPrefix(transport, "ws-")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ws {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, task6RepairSSE(source))
			return
		}
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil {
				t.Logf("close fixture socket: %v", errClose)
			}
		}()
		if _, _, errReadMessage := conn.ReadMessage(); errReadMessage != nil {
			return
		}
		for _, event := range source {
			if errWriteMessage := conn.WriteMessage(websocket.TextMessage, event); errWriteMessage != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	exec := task6RepairExecutor(transport)
	if transport == "interactions" {
		exec = NewGeminiInteractionsExecutor(&config.Config{})
	}
	if ws {
		wsExec := NewXAIWebsocketsExecutor(&config.Config{})
		wsExec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
		exec = wsExec
	}
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: exec.Identifier(), Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "websockets": "true"}}
	return exec, auth
}

func TestApplyPatchNamedLateIdentityActualFailures(t *testing.T) {
	for _, transport := range []string{"xai", "meta", "kimi", "interactions", "ws-sse", "ws-raw"} {
		for _, mode := range []string{"item-conflict", "call-conflict", "partial-snapshot", "invalid-snapshot", "eof"} {
			t.Run(transport+"/"+mode, func(t *testing.T) {
				interactions := transport == "interactions"
				first := "item"
				if mode == "call-conflict" {
					first = "call"
				}
				source := applyPatchIdentitySource(interactions, first, "item", false)
				itemPath, finalPath := "item", "response.output.0"
				if interactions {
					itemPath, finalPath = "step", "interaction.steps.0"
				}
				switch mode {
				case "item-conflict", "call-conflict":
					key := ".id"
					if mode == "call-conflict" {
						key = ".call_id"
					}
					source[len(source)-2], _ = sjson.SetBytes(source[len(source)-2], itemPath+key, "changed")
					source[len(source)-1], _ = sjson.SetBytes(source[len(source)-1], finalPath+key, "changed")
				case "partial-snapshot", "invalid-snapshot":
					args := `{"input":"p`
					if mode == "invalid-snapshot" {
						args = `{"input":"p","extra":"RAW_SECRET"}`
					}
					source[0], _ = sjson.SetBytes(source[0], itemPath+".arguments", args)
				case "eof":
					source = source[:len(source)-2]
				}
				exec, auth := applyPatchIdentityExecutor(t, transport, source)
				checkUsage := task6CaptureFailureUsage(t, auth.ID)
				defer checkUsage()
				ctx := t.Context()
				if transport == "ws-raw" {
					ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
				}
				stream, errExecuteStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				if errExecuteStream != nil {
					t.Fatal(errExecuteStream)
				}
				failed, errors := 0, 0
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						errors++
						assertTask6PatchError(t, chunk.Err)
					}
					if strings.Contains(string(chunk.Payload), "RAW_SECRET") || strings.Contains(string(chunk.Payload), "[DONE]") {
						t.Fatalf("failure leaked/completed: %s", chunk.Payload)
					}
					for _, payload := range applyPatchTestPayloads(chunk.Payload) {
						if gjson.GetBytes(payload, "type").String() == "response.failed" {
							failed++
						} else {
							t.Fatalf("unresolved call published: %s", payload)
						}
					}
				}
				if failed != 1 || errors != 1 {
					t.Fatalf("failure count: failed=%d errors=%d", failed, errors)
				}
			})
		}
	}
}

func TestApplyPatchNamedLateIdentityActualInterleaved(t *testing.T) {
	for _, transport := range []string{"xai", "meta", "kimi", "interactions", "ws-sse", "ws-raw"} {
		t.Run(transport, func(t *testing.T) {
			interactions := transport == "interactions"
			first := applyPatchIdentitySource(interactions, "item", "item", false)
			second := applyPatchIdentitySource(interactions, "call", "item", false)
			indexKey, itemPath, finalPath := "output_index", "item", "response.output"
			if interactions {
				indexKey, itemPath, finalPath = "index", "step", "interaction.steps"
			}
			for i, event := range second {
				if i == len(second)-1 {
					continue
				}
				if gjson.GetBytes(event, indexKey).Exists() {
					event, _ = sjson.SetBytes(event, indexKey, 1)
				}
				if gjson.GetBytes(event, itemPath+".id").String() == "a" {
					event, _ = sjson.SetBytes(event, itemPath+".id", "b")
				}
				if gjson.GetBytes(event, itemPath+".call_id").String() == "c" {
					event, _ = sjson.SetBytes(event, itemPath+".call_id", "d")
				}
				if interactions && gjson.GetBytes(event, "step.index").Exists() {
					event, _ = sjson.SetBytes(event, "step.index", 1)
				}
				event = []byte(strings.ReplaceAll(string(event), "pq", "uv"))
				if i == 1 || i == 2 {
					path, value := "delta", `{"input":"u`
					if interactions {
						path = "delta.arguments"
					}
					if i == 2 {
						value = `v"}`
					}
					event, _ = sjson.SetBytes(event, path, value)
				}
				second[i] = event
			}
			terminal := first[len(first)-1]
			item := gjson.GetBytes(second[len(second)-2], itemPath).Raw
			terminal, _ = sjson.SetRawBytes(terminal, finalPath+".1", []byte(item))
			source := [][]byte{first[0], second[0], first[1], second[1], second[2], first[2]}
			source = append(source, second[3:len(second)-1]...)
			source = append(source, first[3:len(first)-1]...)
			source = append(source, terminal)
			exec, auth := applyPatchIdentityExecutor(t, transport, source)
			ctx := t.Context()
			if transport == "ws-raw" {
				ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
			}
			stream, errExecuteStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "grok-4", Payload: []byte(task6PatchRequest)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if errExecuteStream != nil {
				t.Fatal(errExecuteStream)
			}
			perCall := [2][][]byte{}
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				for _, event := range applyPatchTestPayloads(chunk.Payload) {
					if gjson.GetBytes(event, "type").String() == "response.completed" {
						if len(gjson.GetBytes(event, "response.output").Array()) != 2 {
							t.Fatalf("lost final call: %s", event)
						}
						for index := range perCall {
							item := gjson.GetBytes(event, fmt.Sprintf("response.output.%d", index))
							final, _ := sjson.SetRawBytes(event, "response.output", []byte("["+item.Raw+"]"))
							perCall[index] = append(perCall[index], final)
						}
					} else {
						index := int(gjson.GetBytes(event, "output_index").Int())
						if index < 0 || index > 1 {
							t.Fatalf("unknown call index: %s", event)
						}
						perCall[index] = append(perCall[index], event)
					}
				}
			}
			assertApplyPatchIdentityLifecycle(t, perCall[0], "a", "c", "pq", 0, []string{"p", "q"})
			assertApplyPatchIdentityLifecycle(t, perCall[1], "b", "d", "uv", 1, []string{"u", "v"})
		})
	}
}
