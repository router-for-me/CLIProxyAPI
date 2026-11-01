package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const museReplayModel = "muse-spark-1.3"

// museReplayUpstream simulates Meta's account-bound reasoning envelopes. Every
// envelope it issues is bound to the account whose bearer token made the request,
// and a request that replays an envelope issued to a different account is rejected
// with the same 400 production returned for the naive pass-through. Anything that
// is not one of its own envelopes (including a proxy provenance tag that was not
// stripped) is rejected too, so a bug in either direction fails loudly instead of
// being accepted by a permissive fake.
type museReplayUpstream struct {
	t      *testing.T
	server *httptest.Server

	mu         sync.Mutex
	tokens     map[string]string // bearer token -> account name
	seq        int
	calls      []museReplayCall
	alwaysDeny bool // reject every replayed envelope, even this account's own
}

type museReplayCall struct {
	account   string
	status    int
	envelopes []string // encrypted_content of every reasoning item in the request input, in order
	items     int      // reasoning items in the request input, with or without envelope
	issued    string   // envelope issued in the response, if any
	body      []byte
}

func newMuseReplayUpstream(t *testing.T, accounts ...string) *museReplayUpstream {
	t.Helper()
	u := &museReplayUpstream{t: t, tokens: make(map[string]string)}
	for _, account := range accounts {
		u.tokens["token-"+account] = account
	}
	u.server = httptest.NewServer(http.HandlerFunc(u.handle))
	t.Cleanup(u.server.Close)
	return u
}

func (u *museReplayUpstream) handle(w http.ResponseWriter, r *http.Request) {
	account, ok := u.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"unknown token"}}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	call := museReplayCall{account: account, body: body}
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		call.items++
		if encrypted := item.Get("encrypted_content"); encrypted.Exists() {
			call.envelopes = append(call.envelopes, encrypted.String())
		}
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	for _, envelope := range call.envelopes {
		issuer, valid := museReplayEnvelopeIssuer(envelope)
		if !valid || issuer != account || u.alwaysDeny {
			call.status = http.StatusBadRequest
			u.calls = append(u.calls, call)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"reasoning encrypted_content was not issued to this caller","type":"invalid_request_error"}}`))
			return
		}
	}
	u.seq++
	call.issued = museEnv(account, u.seq)
	call.status = http.StatusOK
	u.calls = append(u.calls, call)

	inputTokens := 100 + 700*len(call.envelopes)
	callID := fmt.Sprintf("call_%d", u.seq)
	reasoning := func(withEnvelope bool) map[string]any {
		item := map[string]any{"id": fmt.Sprintf("rs_%d", u.seq), "type": "reasoning", "summary": []any{}}
		if withEnvelope {
			item["encrypted_content"] = call.issued
		}
		return item
	}
	functionCall := map[string]any{"id": "fc_" + callID, "type": "function_call", "call_id": callID, "name": "Bash", "arguments": `{"command":"ls"}`, "status": "completed"}
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": "resp_1", "object": "response", "status": "in_progress", "model": museReplayModel}},
		{"type": "response.output_item.added", "output_index": 0, "item": reasoning(false)},
		{"type": "response.output_item.done", "output_index": 0, "item": reasoning(true)},
		{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_" + callID, "type": "function_call", "call_id": callID, "name": "Bash", "arguments": "", "status": "in_progress"}},
		{"type": "response.function_call_arguments.delta", "output_index": 1, "item_id": "fc_" + callID, "delta": `{"command":"ls"}`},
		{"type": "response.output_item.done", "output_index": 1, "item": functionCall},
		{"type": "response.completed", "response": map[string]any{
			"id": "resp_1", "object": "response", "status": "completed", "model": museReplayModel,
			"output": []any{reasoning(true), functionCall},
			"usage":  map[string]any{"input_tokens": inputTokens, "output_tokens": 5, "total_tokens": inputTokens + 5},
		}},
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		raw, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], raw)
	}
}

// museEnv builds a synthetic envelope in the observed Meta Muse transport shape
// (fixed five-byte header, 45 + 1024 decoded bytes, unpadded base64url) that
// records which fake account issued it, so the native-only sanitizer accepts it
// and the fake upstream can still enforce account binding.
func museEnv(account string, seq int) string {
	decoded := make([]byte, 45+1024)
	copy(decoded, []byte{0x43, 0xe3, 0xda, 0x0e, 0x01})
	copy(decoded[5:], fmt.Sprintf("issuer=%s;seq=%d;", account, seq))
	return base64.RawURLEncoding.EncodeToString(decoded)
}

// museReplayEnvelopeIssuer returns the fake account recorded by museEnv.
func museReplayEnvelopeIssuer(envelope string) (string, bool) {
	if !signature.IsValidMetaReasoningSignature(envelope) {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(envelope)
	if err != nil || len(decoded) < 5 {
		return "", false
	}
	rest, ok := bytes.CutPrefix(decoded[5:], []byte("issuer="))
	if !ok {
		return "", false
	}
	account, _, ok := bytes.Cut(rest, []byte(";"))
	if !ok || len(account) == 0 {
		return "", false
	}
	return string(account), true
}

func museEnvIssuedBy(envelope, account string) bool {
	issuer, ok := museReplayEnvelopeIssuer(envelope)
	return ok && issuer == account
}

func (u *museReplayUpstream) recorded() []museReplayCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]museReplayCall(nil), u.calls...)
}

func (u *museReplayUpstream) auth(account string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:         "meta-" + account,
		Provider:   "meta",
		Attributes: map[string]string{"api_key": "token-" + account, "base_url": u.server.URL},
	}
}

func museReplayConfig(enabled bool, models ...string) *config.Config {
	return &config.Config{Meta: config.MetaConfig{ReasoningReplay: config.MetaReasoningReplayConfig{Enabled: enabled, Models: models}}}
}

// museReplayConversation plays a Claude Code style client: it keeps the whole
// history and resends it, thinking signatures included, on every turn.
type museReplayConversation struct {
	t        *testing.T
	messages []json.RawMessage
}

func newMuseReplayConversation(t *testing.T) *museReplayConversation {
	c := &museReplayConversation{t: t}
	c.messages = append(c.messages, json.RawMessage(`{"role":"user","content":"list the files"}`))
	return c
}

func (c *museReplayConversation) payload() []byte {
	body, err := json.Marshal(map[string]any{
		"model":      museReplayModel,
		"max_tokens": 1024,
		"messages":   c.messages,
		"tools": []any{map[string]any{
			"name": "Bash", "description": "run a command",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
		}},
	})
	if err != nil {
		c.t.Fatal(err)
	}
	return body
}

// absorb appends the assistant turn from a non-streaming Claude response, plus
// the tool result that answers it, and returns the thinking signature the client
// received.
func (c *museReplayConversation) absorb(response []byte) string {
	c.t.Helper()
	content := gjson.GetBytes(response, "content")
	if !content.IsArray() {
		c.t.Fatalf("response has no content array: %s", response)
	}
	var toolUseID, thinkingSignature string
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "thinking":
			thinkingSignature = block.Get("signature").String()
		case "tool_use":
			toolUseID = block.Get("id").String()
		}
	}
	if toolUseID == "" {
		c.t.Fatalf("response has no tool_use: %s", response)
	}
	c.messages = append(c.messages,
		json.RawMessage(fmt.Sprintf(`{"role":"assistant","content":%s}`, content.Raw)),
		json.RawMessage(fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}`, toolUseID)),
	)
	return thinkingSignature
}

func museReplayClaudeOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}
}

func museReplayExecute(t *testing.T, exec *MetaExecutor, auth *cliproxyauth.Auth, payload []byte) ([]byte, error) {
	t.Helper()
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: museReplayModel, Payload: payload}, museReplayClaudeOptions())
	return resp.Payload, err
}

func museReplayStream(t *testing.T, exec *MetaExecutor, auth *cliproxyauth.Auth, payload []byte, source string) (string, error) {
	t.Helper()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(source), Stream: true}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: museReplayModel, Payload: payload}, opts)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return out.String(), chunk.Err
		}
		out.Write(chunk.Payload)
		out.WriteByte('\n')
	}
	return out.String(), nil
}

// museReplayThinking returns an assistant turn that carries a thinking block with
// the given signature, followed by a tool_use and its result.
func museReplayThinkingTurns(signature string) []json.RawMessage {
	assistant, _ := sjson.SetBytes([]byte(`{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":""},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}`), "content.0.signature", signature)
	return []json.RawMessage{
		json.RawMessage(`{"role":"user","content":"list the files"}`),
		assistant,
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}`),
	}
}

func museReplayHistoryPayload(t *testing.T, signature string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model": museReplayModel, "max_tokens": 1024, "messages": museReplayThinkingTurns(signature),
		"tools": []any{map[string]any{"name": "Bash", "description": "run", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestMetaReasoningReplay_TagsReasoningReturnedToClaudeClients(t *testing.T) {
	upstream := newMuseReplayUpstream(t, "a")
	auth := upstream.auth("a")
	key := metaReasoningAccountKey(auth)
	if key == "" {
		t.Fatal("no account key derived")
	}

	t.Run("non-stream", func(t *testing.T) {
		response, err := museReplayExecute(t, NewMetaExecutor(museReplayConfig(true)), auth, newMuseReplayConversation(t).payload())
		if err != nil {
			t.Fatal(err)
		}
		sig := gjson.GetBytes(response, `content.#(type=="thinking").signature`).String()
		accountKey, envelope, ok := signature.ParseMetaReasoningTag(sig)
		if !ok || accountKey != key || !museEnvIssuedBy(envelope, "a") {
			t.Fatalf("thinking signature %q is not tagged for account key %q", sig, key)
		}
	})

	t.Run("stream", func(t *testing.T) {
		out, err := museReplayStream(t, NewMetaExecutor(museReplayConfig(true)), auth, newMuseReplayConversation(t).payload(), "claude")
		if err != nil {
			t.Fatal(err)
		}
		var sigs []string
		for _, line := range strings.Split(out, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(data, "delta.type").String() == "signature_delta" {
				sigs = append(sigs, gjson.Get(data, "delta.signature").String())
			}
		}
		if len(sigs) != 1 {
			t.Fatalf("got %d signature deltas, want 1:\n%s", len(sigs), out)
		}
		accountKey, envelope, ok := signature.ParseMetaReasoningTag(sigs[0])
		if !ok || accountKey != key || !museEnvIssuedBy(envelope, "a") {
			t.Fatalf("streamed signature %q is not tagged for account key %q", sigs[0], key)
		}
	})
}

func TestMetaReasoningReplay_TagsReasoningReturnedToResponsesClients(t *testing.T) {
	upstream := newMuseReplayUpstream(t, "a")
	auth := upstream.auth("a")
	key := metaReasoningAccountKey(auth)
	payload := []byte(`{"model":"` + museReplayModel + `","input":"list the files"}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
	assertTagged := func(t *testing.T, label, got string) {
		t.Helper()
		accountKey, envelope, ok := signature.ParseMetaReasoningTag(got)
		if !ok || accountKey != key || !museEnvIssuedBy(envelope, "a") {
			t.Fatalf("%s encrypted_content %q is not tagged for account key %q", label, got, key)
		}
	}

	t.Run("non-stream", func(t *testing.T) {
		resp, err := NewMetaExecutor(museReplayConfig(true)).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: museReplayModel, Payload: payload}, opts)
		if err != nil {
			t.Fatal(err)
		}
		assertTagged(t, "completed output", gjson.GetBytes(resp.Payload, `output.#(type=="reasoning").encrypted_content`).String())
	})

	t.Run("stream", func(t *testing.T) {
		streamOpts := opts
		streamOpts.Stream = true
		result, err := NewMetaExecutor(museReplayConfig(true)).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: museReplayModel, Payload: payload}, streamOpts)
		if err != nil {
			t.Fatal(err)
		}
		var doneItems, completedItems int
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			for _, line := range strings.Split(string(chunk.Payload), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				switch gjson.Get(data, "type").String() {
				case "response.output_item.done":
					if gjson.Get(data, "item.type").String() == "reasoning" {
						assertTagged(t, "output_item.done", gjson.Get(data, "item.encrypted_content").String())
						doneItems++
					}
				case "response.completed":
					assertTagged(t, "response.completed", gjson.Get(data, `response.output.#(type=="reasoning").encrypted_content`).String())
					completedItems++
				}
			}
		}
		if doneItems != 1 || completedItems != 1 {
			t.Fatalf("saw %d output_item.done and %d response.completed reasoning items, want 1 each", doneItems, completedItems)
		}
	})
}

// With the feature off the proxy must behave exactly as before: the raw envelope
// goes to the client and nothing is replayed.
func TestMetaReasoningReplay_DisabledLeavesResponsesUntaggedAndDropsReplay(t *testing.T) {
	upstream := newMuseReplayUpstream(t, "a")
	auth := upstream.auth("a")
	exec := NewMetaExecutor(museReplayConfig(false))

	response, err := museReplayExecute(t, exec, auth, newMuseReplayConversation(t).payload())
	if err != nil {
		t.Fatal(err)
	}
	sig := gjson.GetBytes(response, `content.#(type=="thinking").signature`).String()
	if signature.IsMetaReasoningTagged(sig) || !museEnvIssuedBy(sig, "a") {
		t.Fatalf("disabled feature changed the signature returned to the client: %q", sig)
	}

	// A tagged envelope from a time the feature was on must not reach Meta either.
	tagged := signature.TagMetaReasoning(metaReasoningAccountKey(auth), museEnv("a", 99))
	if _, err := museReplayExecute(t, exec, auth, museReplayHistoryPayload(t, tagged)); err != nil {
		t.Fatalf("disabled feature surfaced an error for a tagged envelope: %v", err)
	}
	calls := upstream.recorded()
	last := calls[len(calls)-1]
	if last.items != 0 || len(last.envelopes) != 0 {
		t.Fatalf("disabled feature forwarded %d reasoning items (%v) to Meta, want none", last.items, last.envelopes)
	}
}

func TestMetaReasoningReplay_ReplaysOnlySameAccountEnvelope(t *testing.T) {
	upstream := newMuseReplayUpstream(t, "a", "b")
	authA, authB := upstream.auth("a"), upstream.auth("b")
	exec := NewMetaExecutor(museReplayConfig(true))
	keyA, keyB := metaReasoningAccountKey(authA), metaReasoningAccountKey(authB)
	if keyA == keyB {
		t.Fatal("distinct accounts share an account key")
	}

	for _, tc := range []struct {
		name         string
		signature    string
		auth         *cliproxyauth.Auth
		wantEnvelope string // forwarded to Meta; empty means the reasoning item must be absent
	}{
		{"same account is replayed with the tag stripped", signature.TagMetaReasoning(keyA, museEnv("a", 7)), authA, museEnv("a", 7)},
		{"other account is dropped", signature.TagMetaReasoning(keyB, museEnv("b", 7)), authA, ""},
		{"legacy untagged envelope is dropped", museEnv("a", 7), authA, ""},
		{"tag naming an unknown account is dropped", signature.TagMetaReasoning("deadbeefdeadbeef", museEnv("a", 7)), authA, ""},
		{"empty signature is dropped", "", authA, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(upstream.recorded())
			if _, err := museReplayExecute(t, exec, tc.auth, museReplayHistoryPayload(t, tc.signature)); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			calls := upstream.recorded()[before:]
			if len(calls) != 1 {
				t.Fatalf("upstream saw %d requests, want 1 (no retry)", len(calls))
			}
			got := calls[0]
			if tc.wantEnvelope == "" {
				if got.items != 0 {
					t.Fatalf("a reasoning item with envelopes %v reached Meta, want none", got.envelopes)
				}
				return
			}
			if len(got.envelopes) != 1 || got.envelopes[0] != tc.wantEnvelope {
				t.Fatalf("envelopes forwarded = %v, want [%s]", got.envelopes, tc.wantEnvelope)
			}
			if bytes.Contains(got.body, []byte("meta#")) {
				t.Fatalf("provenance tag leaked to Meta: %s", got.body)
			}
		})
	}
}

func TestMetaReasoningReplay_ResponsesClientHistory(t *testing.T) {
	upstream := newMuseReplayUpstream(t, "a", "b")
	authA := upstream.auth("a")
	exec := NewMetaExecutor(museReplayConfig(true))
	keyA, keyB := metaReasoningAccountKey(authA), metaReasoningAccountKey(upstream.auth("b"))

	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "list the files"},
		map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": signature.TagMetaReasoning(keyA, museEnv("a", 1))},
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "Bash", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
		map[string]any{"type": "reasoning", "id": "rs_2", "summary": []any{}, "encrypted_content": signature.TagMetaReasoning(keyB, museEnv("b", 2))},
		map[string]any{"type": "reasoning", "id": "rs_3", "summary": []any{map[string]any{"type": "summary_text", "text": "kept summary"}}, "encrypted_content": signature.TagMetaReasoning(keyB, museEnv("b", 3))},
		map[string]any{"type": "reasoning", "id": "rs_4", "summary": []any{}, "encrypted_content": museEnv("a", 4)},
		map[string]any{"type": "message", "role": "user", "content": "go on"},
	}
	payload, _ := json.Marshal(map[string]any{"model": museReplayModel, "input": input})
	if _, err := exec.Execute(context.Background(), authA, cliproxyexecutor.Request{Model: museReplayModel, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}); err != nil {
		t.Fatal(err)
	}
	calls := upstream.recorded()
	got := calls[len(calls)-1]
	if len(calls) != 1 || len(got.envelopes) != 1 || got.envelopes[0] != museEnv("a", 1) {
		t.Fatalf("calls=%d envelopes=%v, want exactly the same-account envelope Q-PaDg-a-1", len(calls), got.envelopes)
	}
	// rs_2 and rs_4 carried nothing but an unusable envelope and disappear. rs_3 keeps
	// its summary, which is not account-bound, and only loses the envelope and, as
	// for any reasoning item without a usable envelope, its store id.
	var items []string
	for _, item := range gjson.GetBytes(got.body, "input").Array() {
		if item.Get("type").String() == "reasoning" {
			items = append(items, fmt.Sprintf("id=%q envelope=%v summary=%q", item.Get("id").String(), item.Get("encrypted_content").Exists(), item.Get("summary.0.text").String()))
		}
	}
	want := []string{`id="rs_1" envelope=true summary=""`, `id="" envelope=false summary="kept summary"`}
	if strings.Join(items, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reasoning items forwarded =\n%s\nwant\n%s", strings.Join(items, "\n"), strings.Join(want, "\n"))
	}
}

// Meta may still reject a replayed envelope (for instance when it binds to
// something finer than the account). The agent must never see that 400.
func TestMetaReasoningReplay_RetriesOnceWithoutReasoningWhenMetaRejects(t *testing.T) {
	for _, mode := range []string{"non-stream", "stream"} {
		t.Run(mode, func(t *testing.T) {
			upstream := newMuseReplayUpstream(t, "a")
			upstream.alwaysDeny = true // rejects any replayed envelope, including the matching account's
			auth := upstream.auth("a")
			signed := signature.TagMetaReasoning(metaReasoningAccountKey(auth), museEnv("a", 7))
			payload := museReplayHistoryPayload(t, signed)
			exec := NewMetaExecutor(museReplayConfig(true))

			var err error
			if mode == "stream" {
				_, err = museReplayStream(t, exec, auth, payload, "claude")
			} else {
				_, err = museReplayExecute(t, exec, auth, payload)
			}
			if err != nil {
				t.Fatalf("the rejected-envelope 400 reached the caller: %v", err)
			}
			calls := upstream.recorded()
			if len(calls) != 2 {
				t.Fatalf("upstream saw %d requests, want 2 (rejected, then retried)", len(calls))
			}
			if calls[0].status != http.StatusBadRequest || len(calls[0].envelopes) != 1 || calls[0].envelopes[0] != museEnv("a", 7) {
				t.Fatalf("first request = status %d envelopes %v, want the replayed envelope rejected", calls[0].status, calls[0].envelopes)
			}
			if calls[1].status != http.StatusOK || calls[1].items != 0 {
				t.Fatalf("retry = status %d with %d reasoning items, want 200 with none", calls[1].status, calls[1].items)
			}
		})
	}
}

// TestMetaReasoningReplay_PayloadRulesStayFinalOnRetry locks the payload-barrier
// ordering for the retry path: built-in reasoning cleanup runs first and user
// payload rules are applied exactly once to each attempt's final body, so the
// retry neither drops a configured value nor restores a filtered field.
func TestMetaReasoningReplay_PayloadRulesStayFinalOnRetry(t *testing.T) {
	upstream := newMuseReplayUpstream(t, "a")
	upstream.alwaysDeny = true
	auth := upstream.auth("a")
	signed := signature.TagMetaReasoning(metaReasoningAccountKey(auth), museEnv("a", 7))
	cfg := museReplayConfig(true)
	cfg.Payload = config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: museReplayModel}}, Params: map[string]any{"max_output_tokens": 321}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: museReplayModel}}, Params: []string{"instructions"}}},
	}
	if _, err := museReplayExecute(t, NewMetaExecutor(cfg), auth, museReplayHistoryPayload(t, signed)); err != nil {
		t.Fatalf("the rejected-envelope 400 reached the caller: %v", err)
	}
	calls := upstream.recorded()
	if len(calls) != 2 {
		t.Fatalf("upstream saw %d requests, want 2 (rejected, then retried)", len(calls))
	}
	for attempt, call := range calls {
		if got := gjson.GetBytes(call.body, "max_output_tokens").Int(); got != 321 {
			t.Fatalf("attempt %d: max_output_tokens = %d, want the payload override 321: %s", attempt, got, call.body)
		}
		if gjson.GetBytes(call.body, "instructions").Exists() {
			t.Fatalf("attempt %d: filtered instructions field was restored: %s", attempt, call.body)
		}
	}
	if calls[1].items != 0 {
		t.Fatalf("retry carried %d reasoning items, want none", calls[1].items)
	}
}

func TestMetaReasoningReplay_DisabledForwardsUntaggedEnvelopeAndRetriesWhenForeign(t *testing.T) {
	for _, mode := range []string{"non-stream", "stream"} {
		t.Run(mode, func(t *testing.T) {
			upstream := newMuseReplayUpstream(t, "a", "b")
			auth := upstream.auth("a")
			// A Responses client resends untagged envelopes verbatim, and with the feature
			// off nothing proves which account issued them.
			input := []any{
				map[string]any{"type": "message", "role": "user", "content": "list the files"},
				map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": museEnv("b", 3)},
				map[string]any{"type": "message", "role": "user", "content": "go on"},
			}
			payload, _ := json.Marshal(map[string]any{"model": museReplayModel, "input": input})
			exec := NewMetaExecutor(museReplayConfig(false))

			var err error
			if mode == "stream" {
				_, err = museReplayStream(t, exec, auth, payload, "openai-response")
			} else {
				_, err = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: museReplayModel, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
			}
			if err != nil {
				t.Fatalf("the foreign-envelope 400 reached the caller: %v", err)
			}
			calls := upstream.recorded()
			if len(calls) != 2 {
				t.Fatalf("upstream saw %d requests, want 2 (rejected, then retried)", len(calls))
			}
			if calls[0].status != http.StatusBadRequest || len(calls[0].envelopes) != 1 || calls[0].envelopes[0] != museEnv("b", 3) {
				t.Fatalf("first request = status %d envelopes %v, want the untagged envelope forwarded and rejected", calls[0].status, calls[0].envelopes)
			}
			if calls[1].status != http.StatusOK || calls[1].items != 0 {
				t.Fatalf("retry = status %d with %d reasoning items, want 200 with none", calls[1].status, calls[1].items)
			}
		})
	}
}

func TestMetaReasoningReplay_RetryHappensOnceAndOnlyForThisRejection(t *testing.T) {
	t.Run("a rejection of the retry is surfaced, not retried again", func(t *testing.T) {
		// A bearer token the fake does not know makes every request fail with 401, so use a
		// bespoke server that always returns the replay 400 whatever the body holds.
		var count int
		var mu sync.Mutex
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			count++
			mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"reasoning encrypted_content was not issued to this caller"}}`))
		}))
		defer server.Close()
		auth := &cliproxyauth.Auth{ID: "meta-x", Provider: "meta", Attributes: map[string]string{"api_key": "t", "base_url": server.URL}}
		payload := museReplayHistoryPayload(t, signature.TagMetaReasoning(metaReasoningAccountKey(auth), museEnv("x", 1)))
		if _, err := museReplayExecute(t, NewMetaExecutor(museReplayConfig(true)), auth, payload); err == nil {
			t.Fatal("expected the persistent rejection to surface")
		}
		if count != 2 {
			t.Fatalf("upstream saw %d requests, want exactly 2", count)
		}
	})

	t.Run("no retry when no envelope was forwarded", func(t *testing.T) {
		var count int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"reasoning encrypted_content was not issued to this caller"}}`))
		}))
		defer server.Close()
		auth := &cliproxyauth.Auth{ID: "meta-x", Provider: "meta", Attributes: map[string]string{"api_key": "t", "base_url": server.URL}}
		payload := museReplayHistoryPayload(t, signature.TagMetaReasoning("0123456789abcdef", museEnv("x", 1))) // foreign: dropped
		if _, err := museReplayExecute(t, NewMetaExecutor(museReplayConfig(true)), auth, payload); err == nil {
			t.Fatal("expected the error to surface")
		}
		if count != 1 {
			t.Fatalf("upstream saw %d requests, want 1: a request without envelopes must not be retried", count)
		}
	})

	t.Run("no retry for an unrelated 400", func(t *testing.T) {
		var count int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"tools.0.name is invalid"}}`))
		}))
		defer server.Close()
		auth := &cliproxyauth.Auth{ID: "meta-x", Provider: "meta", Attributes: map[string]string{"api_key": "t", "base_url": server.URL}}
		payload := museReplayHistoryPayload(t, signature.TagMetaReasoning(metaReasoningAccountKey(auth), museEnv("x", 1)))
		if _, err := museReplayExecute(t, NewMetaExecutor(museReplayConfig(true)), auth, payload); err == nil {
			t.Fatal("expected the error to surface")
		}
		if count != 1 {
			t.Fatalf("upstream saw %d requests, want 1", count)
		}
	})
}

// A Meta endpoint that answers with a plain JSON response object instead of an
// SSE stream still has its reasoning tagged.
func TestMetaReasoningReplay_TagsPlainJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"muse-spark-1.3","output":[` +
			`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"` + museEnv("a", 1) + `"},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Bash","arguments":"{}","status":"completed"}],` +
			`"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: "meta-a", Provider: "meta", Attributes: map[string]string{"api_key": "token-a", "base_url": server.URL}}
	response, err := museReplayExecute(t, NewMetaExecutor(museReplayConfig(true)), auth, newMuseReplayConversation(t).payload())
	if err != nil {
		t.Fatal(err)
	}
	key, envelope, ok := signature.ParseMetaReasoningTag(gjson.GetBytes(response, `content.#(type=="thinking").signature`).String())
	if !ok || key != metaReasoningAccountKey(auth) || envelope != museEnv("a", 1) {
		t.Fatalf("plain JSON response was not tagged: %s", response)
	}
}

func TestMetaReasoningReplay_StreamRetryHappensOnce(t *testing.T) {
	var count int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"reasoning encrypted_content was not issued to this caller"}}`))
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: "meta-x", Provider: "meta", Attributes: map[string]string{"api_key": "t", "base_url": server.URL}}
	payload := museReplayHistoryPayload(t, signature.TagMetaReasoning(metaReasoningAccountKey(auth), museEnv("x", 1)))
	if _, err := museReplayStream(t, NewMetaExecutor(museReplayConfig(true)), auth, payload, "claude"); err == nil {
		t.Fatal("expected the persistent rejection to surface")
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 2 {
		t.Fatalf("upstream saw %d requests, want exactly 2", count)
	}
}

// The same rejection can arrive as an error event inside a 200 SSE body. A
// non-streaming request has returned nothing yet, so it is retried as well.
func TestMetaReasoningReplay_RetriesWhenTheRejectionIsAnErrorEvent(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if bytes.Contains(body, []byte(`"encrypted_content"`)) {
			_, _ = w.Write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"reasoning encrypted_content was not issued to this caller\"}}\n\n"))
			return
		}
		writeMetaResponsesOK(w, "recovered")
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: "meta-a", Provider: "meta", Attributes: map[string]string{"api_key": "token-a", "base_url": server.URL}}
	payload := museReplayHistoryPayload(t, signature.TagMetaReasoning(metaReasoningAccountKey(auth), museEnv("a", 1)))

	if _, err := museReplayExecute(t, NewMetaExecutor(museReplayConfig(true)), auth, payload); err != nil {
		t.Fatalf("the rejection event reached the caller: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !bytes.Contains(bodies[0], []byte(`"encrypted_content"`)) || bytes.Contains(bodies[1], []byte(`"encrypted_content"`)) {
		t.Fatalf("upstream saw %d requests; want the replay, then a retry without any envelope", len(bodies))
	}
}

func TestMetaReasoningReplay_ModelScope(t *testing.T) {
	upstream := newMuseReplayUpstream(t, "a")
	auth := upstream.auth("a")
	for _, tc := range []struct {
		name       string
		models     []string
		wantTagged bool
	}{
		{"enabled for every Meta model when no list is given", nil, true},
		{"enabled for a matching model", []string{"muse-spark-1.3"}, true},
		{"enabled for a matching pattern", []string{"muse-*"}, true},
		{"off for a model outside the canary list", []string{"muse-spark-1.2*"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := museReplayExecute(t, NewMetaExecutor(museReplayConfig(true, tc.models...)), auth, newMuseReplayConversation(t).payload())
			if err != nil {
				t.Fatal(err)
			}
			sig := gjson.GetBytes(response, `content.#(type=="thinking").signature`).String()
			if got := signature.IsMetaReasoningTagged(sig); got != tc.wantTagged {
				t.Fatalf("tagged = %v, want %v (signature %q)", got, tc.wantTagged, sig)
			}
		})
	}

	t.Run("an alias on the canary list enables it for the requested name", func(t *testing.T) {
		opts := museReplayClaudeOptions()
		opts.Metadata = map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "muse-canary"}
		resp, err := NewMetaExecutor(museReplayConfig(true, "muse-canary")).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: museReplayModel, Payload: newMuseReplayConversation(t).payload()}, opts)
		if err != nil {
			t.Fatal(err)
		}
		if sig := gjson.GetBytes(resp.Payload, `content.#(type=="thinking").signature`).String(); !signature.IsMetaReasoningTagged(sig) {
			t.Fatalf("alias on the canary list was not tagged: %q", sig)
		}
	})
}

// The canary needs a control group on the very same pool: an alias on the model list
// must switch the feature on for requests addressed to the alias and leave requests
// for the upstream model name exactly as they were.
func TestMetaReasoningReplay_CanaryAliasThroughManager(t *testing.T) {
	const alias = "muse-canary"
	upstream := newMuseReplayUpstream(t, "a")
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewMetaExecutor(museReplayConfig(true, alias)))
	manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{"meta": {{Name: museReplayModel, Alias: alias, Fork: true}}})
	auth := upstream.auth("a")
	auth.ID = "meta-canary-" + t.Name()
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "meta", []*registry.ModelInfo{{ID: museReplayModel}, {ID: alias}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	signatureFor := func(model string) string {
		t.Helper()
		payload, _ := sjson.SetBytes(newMuseReplayConversation(t).payload(), "model", model)
		resp, err := manager.Execute(context.Background(), []string{"meta"}, cliproxyexecutor.Request{Model: model, Payload: payload}, museReplayClaudeOptions())
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		return gjson.GetBytes(resp.Payload, `content.#(type=="thinking").signature`).String()
	}
	if sig := signatureFor(alias); !signature.IsMetaReasoningTagged(sig) {
		t.Fatalf("a request for the canary alias was not tagged: %q", sig)
	}
	if sig := signatureFor(museReplayModel); signature.IsMetaReasoningTagged(sig) || !museEnvIssuedBy(sig, "a") {
		t.Fatalf("the control group request changed behaviour: %q", sig)
	}
}

func TestMetaReasoningAccountKey(t *testing.T) {
	a := metaReasoningAccountKey(&cliproxyauth.Auth{ID: "meta-a"})
	if a == "" || a != metaReasoningAccountKey(&cliproxyauth.Auth{ID: "meta-a", Attributes: map[string]string{"api_key": "rotated"}}) {
		t.Fatalf("account key must depend on identity only and survive token rotation, got %q", a)
	}
	if a == metaReasoningAccountKey(&cliproxyauth.Auth{ID: "meta-b"}) {
		t.Fatal("distinct accounts share a key")
	}
	if _, _, ok := signature.ParseMetaReasoningTag(signature.TagMetaReasoning(a, "x")); !ok {
		t.Fatalf("account key %q is not a valid tag key", a)
	}
	if got := metaReasoningAccountKey(nil); got != "" {
		t.Fatalf("nil auth produced key %q", got)
	}
	if got := metaReasoningAccountKey(&cliproxyauth.Auth{}); got != "" {
		t.Fatalf("anonymous auth produced key %q", got)
	}
	if got := metaReasoningAccountKey(&cliproxyauth.Auth{Attributes: map[string]string{"api_key": "k"}}); got != "" {
		t.Fatalf("a key must never be derived from the API key, got %q", got)
	}
}

// museReplayPool registers one Meta credential per account behind a real Manager,
// so the executor sees whichever account the conductor selects, exactly like
// production. Round-robin guarantees consecutive turns land on different accounts.
func museReplayPool(t *testing.T, cfg *config.Config, upstream *museReplayUpstream, accounts ...string) *cliproxyauth.Manager {
	t.Helper()
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewMetaExecutor(cfg))
	for _, account := range accounts {
		auth := upstream.auth(account)
		auth.ID = "meta-pool-" + t.Name() + "-" + account
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "meta", []*registry.ModelInfo{{ID: museReplayModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	return manager
}

// The production failure: a long agent run whose history mixes envelopes from
// several pool accounts. Every turn is served by a different account than the one
// before it, and the naive pass-through is rejected on the second turn.
func TestMetaReasoningReplay_LongConversationAcrossAccounts(t *testing.T) {
	const turns = 24
	for _, accounts := range [][]string{{"a", "b"}, {"a", "b", "c"}, {"a", "b", "c", "d"}} {
		t.Run(fmt.Sprintf("%d accounts", len(accounts)), func(t *testing.T) {
			upstream := newMuseReplayUpstream(t, accounts...)
			manager := museReplayPool(t, museReplayConfig(true), upstream, accounts...)
			conversation := newMuseReplayConversation(t)

			var clientSignatures []string
			for turn := 0; turn < turns; turn++ {
				resp, err := manager.Execute(context.Background(), []string{"meta"}, cliproxyexecutor.Request{Model: museReplayModel, Payload: conversation.payload()}, museReplayClaudeOptions())
				if err != nil {
					t.Fatalf("turn %d: the agent saw an error: %v", turn, err)
				}
				clientSignatures = append(clientSignatures, conversation.absorb(resp.Payload))
			}

			calls := upstream.recorded()
			if len(calls) != turns {
				t.Fatalf("upstream saw %d requests for %d turns: an unexpected retry or rejection happened", len(calls), turns)
			}
			distinct, switches := map[string]bool{}, 0
			for i, call := range calls {
				if call.status != http.StatusOK {
					t.Fatalf("turn %d: Meta rejected the request (status %d)", i, call.status)
				}
				distinct[call.account] = true
				if i > 0 && call.account != calls[i-1].account {
					switches++
				}
				// The oracle is derived from what actually happened, not from an assumed
				// rotation: turn i may replay exactly the envelopes this same account issued
				// on an earlier turn, in order, and nothing else.
				var want []string
				for j := 0; j < i; j++ {
					if calls[j].account == call.account {
						want = append(want, calls[j].issued)
					}
				}
				if strings.Join(call.envelopes, ",") != strings.Join(want, ",") {
					t.Fatalf("turn %d on account %s forwarded %v, want exactly %v", i, call.account, call.envelopes, want)
				}
			}
			// Without this the test would pass vacuously on a pool that never rotated.
			if len(distinct) != len(accounts) || switches < turns/2 {
				t.Fatalf("the pool did not exercise cross-account turns: %d distinct accounts, %d account switches in %d turns", len(distinct), switches, turns)
			}
			for i, sig := range clientSignatures {
				key, envelope, ok := signature.ParseMetaReasoningTag(sig)
				if !ok || envelope != calls[i].issued || key != metaReasoningAccountKey(&cliproxyauth.Auth{ID: "meta-pool-" + t.Name() + "-" + calls[i].account}) {
					t.Fatalf("turn %d: client received %q, want a tag for account %s around %s", i, sig, calls[i].account, calls[i].issued)
				}
			}
		})
	}
}

// A single account (perfect affinity) must replay the entire history: this is the
// win the feature exists for, and the only case a one-account probe can see.
func TestMetaReasoningReplay_SingleAccountReplaysWholeHistory(t *testing.T) {
	const turns = 8
	upstream := newMuseReplayUpstream(t, "a")
	manager := museReplayPool(t, museReplayConfig(true), upstream, "a")
	conversation := newMuseReplayConversation(t)
	for turn := 0; turn < turns; turn++ {
		resp, err := manager.Execute(context.Background(), []string{"meta"}, cliproxyexecutor.Request{Model: museReplayModel, Payload: conversation.payload()}, museReplayClaudeOptions())
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		conversation.absorb(resp.Payload)
	}
	calls := upstream.recorded()
	for i, call := range calls {
		if len(call.envelopes) != i {
			t.Fatalf("turn %d forwarded %d envelopes, want all %d earlier ones", i, len(call.envelopes), i)
		}
	}
}

// The same long cross-account conversation through the streaming path.
func TestMetaReasoningReplay_StreamingConversationAcrossAccounts(t *testing.T) {
	const turns = 9
	upstream := newMuseReplayUpstream(t, "a", "b", "c")
	manager := museReplayPool(t, museReplayConfig(true), upstream, "a", "b", "c")
	conversation := newMuseReplayConversation(t)
	opts := museReplayClaudeOptions()
	opts.Stream = true

	for turn := 0; turn < turns; turn++ {
		result, err := manager.ExecuteStream(context.Background(), []string{"meta"}, cliproxyexecutor.Request{Model: museReplayModel, Payload: conversation.payload()}, opts)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		var signatureDelta, toolID, toolName string
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("turn %d: stream error: %v", turn, chunk.Err)
			}
			for _, line := range strings.Split(string(chunk.Payload), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				if gjson.Get(data, "delta.type").String() == "signature_delta" {
					signatureDelta = gjson.Get(data, "delta.signature").String()
				}
				if gjson.Get(data, "content_block.type").String() == "tool_use" {
					toolID, toolName = gjson.Get(data, "content_block.id").String(), gjson.Get(data, "content_block.name").String()
				}
			}
		}
		if signatureDelta == "" || toolID == "" {
			t.Fatalf("turn %d: stream carried signature %q tool %q", turn, signatureDelta, toolID)
		}
		conversation.messages = append(conversation.messages,
			json.RawMessage(fmt.Sprintf(`{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":%q},{"type":"tool_use","id":%q,"name":%q,"input":{"command":"ls"}}]}`, signatureDelta, toolID, toolName)),
			json.RawMessage(fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}`, toolID)),
		)
	}

	calls := upstream.recorded()
	if len(calls) != turns {
		t.Fatalf("upstream saw %d requests for %d turns", len(calls), turns)
	}
	var kept int
	for i, call := range calls {
		var want []string
		for j := 0; j < i; j++ {
			if calls[j].account == call.account {
				want = append(want, calls[j].issued)
			}
		}
		if strings.Join(call.envelopes, ",") != strings.Join(want, ",") {
			t.Fatalf("turn %d on account %s forwarded %v, want %v", i, call.account, call.envelopes, want)
		}
		kept += len(want)
	}
	if kept == 0 {
		t.Fatal("no envelope was ever replayed on the same account; the test did not exercise replay")
	}
}
