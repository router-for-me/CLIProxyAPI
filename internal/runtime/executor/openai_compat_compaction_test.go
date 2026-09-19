package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorResponsesCompactionTriggerNonStream(t *testing.T) {
	var gotBodies [][]byte
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		writeOpenAICompatCompactionSummary(w)
	})
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	resp, errExecute := executor.Execute(context.Background(), openAICompatCompactionTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: openAICompatCompactionTriggerPayload(),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: openAICompatCompactionTriggerPayload(),
	})
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	assertOpenAICompatCompactionObject(t, resp.Payload, "response")
	assertOpenAICompatCompactionResponse(t, resp.Payload)
	if len(gotBodies) != 1 {
		t.Fatalf("upstream request count = %d, want 1", len(gotBodies))
	}
	if strings.Contains(string(gotBodies[0]), "compaction_trigger") {
		t.Fatalf("summary trigger reached upstream: %s", gotBodies[0])
	}
}

func TestOpenAICompatCompactionTriggerStream(t *testing.T) {
	var gotBodies [][]byte
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		writeOpenAICompatCompactionSummary(w)
	})
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	result, errExecute := executor.ExecuteStream(context.Background(), openAICompatCompactionTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: openAICompatCompactionTriggerPayload(),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}

	streamed := collectOpenAICompatCompactionStream(t, result)
	if !strings.HasSuffix(streamed, "\n\n") || !strings.Contains(streamed, "event: response.completed\n") {
		t.Fatalf("stream does not end with response.completed: %q", streamed)
	}
	frames := parseOpenAICompatCompactionStream(t, streamed)
	wantEvents := []string{"response.created", "response.in_progress", "response.output_item.added", "response.output_item.done", "response.completed"}
	if len(frames) != len(wantEvents) {
		t.Fatalf("stream frame count = %d, want %d: %q", len(frames), len(wantEvents), streamed)
	}
	for i, wantEvent := range wantEvents {
		if frames[i].event != wantEvent {
			t.Fatalf("stream event %d = %q, want %q", i, frames[i].event, wantEvent)
		}
	}
	added := frames[2].payload
	done := frames[3].payload
	if got := gjson.GetBytes(added, "output_index").Int(); got != 0 {
		t.Fatalf("added output_index = %d, want 0", got)
	}
	if got := gjson.GetBytes(done, "output_index").Int(); got != 0 {
		t.Fatalf("done output_index = %d, want 0", got)
	}
	addedID := gjson.GetBytes(added, "item.id").String()
	doneID := gjson.GetBytes(done, "item.id").String()
	if addedID == "" || addedID != doneID {
		t.Fatalf("added/done item ids = %q/%q, want same non-empty id", addedID, doneID)
	}
	terminal := frames[len(frames)-1].payload
	terminalResponse := []byte(gjson.GetBytes(terminal, "response").Raw)
	assertOpenAICompatCompactionObject(t, terminalResponse, "response")
	assertOpenAICompatCompactionResponse(t, terminalResponse)
	if len(gotBodies) != 1 {
		t.Fatalf("upstream request count = %d, want 1", len(gotBodies))
	}
}

func TestOpenAICompatCompactionAltDisabledPassesThrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
	}))
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	_, errExecute := executor.Execute(context.Background(), openAICompatCompactionTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","input":[{"type":"compaction","encrypted_content":"cpa-ag-compact-v1:upstream-capsule"},{"type":"compaction","encrypted_content":"upstream-native-opaque"},{"type":"message","role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Alt:            "responses/compact",
	})
	if errExecute == nil {
		t.Fatal("expected upstream compact error")
	}
	assertOpenAICompatCompactionStatus(t, errExecute, http.StatusNotFound)
	if errExecute.Error() != `{"detail":"Not Found"}` {
		t.Fatalf("error = %q, want upstream message", errExecute.Error())
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("upstream path = %q, want /v1/responses/compact", gotPath)
	}
	// A CPA-sealed capsule must never be forwarded to the upstream: the upstream
	// cannot decrypt it and rejects the whole request.
	if strings.Contains(string(gotBody), "cpa-ag-compact-v1:upstream-capsule") {
		t.Fatalf("CPA capsule was forwarded to upstream: %s", gotBody)
	}
	// An opaque item that is not CPA-sealed may be the upstream's own capsule, so it stays.
	if !strings.Contains(string(gotBody), "upstream-native-opaque") {
		t.Fatalf("native opaque compaction item was dropped: %s", gotBody)
	}
}

func TestOpenAICompatCompactionAltEnabled(t *testing.T) {
	var gotPath string
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.ReadAll(r.Body)
		writeOpenAICompatCompactionSummary(w)
	})
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, true)
	resp, errExecute := executor.Execute(context.Background(), openAICompatCompactionTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Alt:            "responses/compact",
	})
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	assertOpenAICompatCompactionObject(t, resp.Payload, "response.compaction")
	assertOpenAICompatCompactionResponse(t, resp.Payload)
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("summary upstream path = %q, want /v1/chat/completions", gotPath)
	}
}

func TestOpenAICompatCompactionStreamRejectsAlt(t *testing.T) {
	executor := newOpenAICompatCompactionTestExecutor("http://127.0.0.1:1", false)
	result, errExecute := executor.ExecuteStream(context.Background(), openAICompatCompactionTestAuth("http://127.0.0.1:1"), cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"history"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Alt:          "responses/compact",
		Stream:       true,
	})
	if result != nil {
		t.Fatal("expected no stream result for /responses/compact")
	}
	if errExecute == nil {
		t.Fatal("expected streaming compact rejection")
	}
	assertOpenAICompatCompactionStatus(t, errExecute, http.StatusBadRequest)
	if !strings.Contains(errExecute.Error(), "streaming not supported for /responses/compact") {
		t.Fatalf("error = %q, want streaming compact rejection", errExecute.Error())
	}
}

func TestOpenAICompatCompactionSummaryUpstreamFailureRelaysStatusAndMessage(t *testing.T) {
	const message = `{"error":{"message":"summary unavailable"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(message))
	}))
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	_, errExecute := executor.Execute(context.Background(), openAICompatCompactionTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: openAICompatCompactionTriggerPayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExecute == nil {
		t.Fatal("expected summary upstream error")
	}
	assertOpenAICompatCompactionStatus(t, errExecute, http.StatusTeapot)
	if errExecute.Error() != message {
		t.Fatalf("error = %q, want %q", errExecute.Error(), message)
	}
}

func TestOpenAICompatCompactionSummaryWithoutTextReturns502(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_empty","object":"chat.completion","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`))
	}))
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	resp, errExecute := executor.Execute(context.Background(), openAICompatCompactionTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: openAICompatCompactionTriggerPayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExecute == nil {
		t.Fatal("expected missing summary text error")
	}
	assertOpenAICompatCompactionStatus(t, errExecute, http.StatusBadGateway)
	if len(resp.Payload) != 0 || strings.Contains(string(resp.Payload), "encrypted_content") {
		t.Fatalf("unexpected partial compaction response: %s", resp.Payload)
	}
}

func TestOpenAICompatCompactionOrdinaryRequestIsUnchanged(t *testing.T) {
	var gotBody []byte
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		writeOpenAICompatNormalResponse(w)
	})
	defer server.Close()

	payload := []byte(`{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}]}`)
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	_, errExecute := executor.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}, cliproxyexecutor.Request{Model: "gpt-5.6", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("ordinary translated payload changed:\n got: %s\nwant: %s", gotBody, payload)
	}
}

func TestOpenAICompatCompactionReplayExpandsOwnCapsule(t *testing.T) {
	var gotBodies [][]byte
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		writeOpenAICompatCompactionSummary(w)
	})
	defer server.Close()

	auth := openAICompatCompactionTestAuth(server.URL)
	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	compactResp, errCompact := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: openAICompatCompactionTriggerPayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errCompact != nil {
		t.Fatalf("compaction error: %v", errCompact)
	}
	capsule := gjson.GetBytes(compactResp.Payload, "output.0.encrypted_content").String()
	if capsule == "" {
		t.Fatalf("empty capsule: %s", compactResp.Payload)
	}

	nextPayload := []byte(fmt.Sprintf(`{"model":"gpt-5.6","input":[{"type":"compaction","encrypted_content":%q},{"type":"message","role":"user","content":"next task"}]}`, capsule))
	_, errNext := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: nextPayload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errNext != nil {
		t.Fatalf("replay error: %v", errNext)
	}
	if len(gotBodies) != 2 {
		t.Fatalf("upstream request count = %d, want 2", len(gotBodies))
	}
	if !strings.Contains(string(gotBodies[1]), "Context summary from previous turns:") {
		t.Fatalf("summary context missing from replay request: %s", gotBodies[1])
	}
	if strings.Contains(string(gotBodies[1]), capsule) || gjson.GetBytes(gotBodies[1], "input").Exists() || strings.Contains(string(gotBodies[1]), `"type":"compaction"`) {
		t.Fatalf("opaque compaction item reached upstream: %s", gotBodies[1])
	}
}

// A capsule this instance can open is inlined as context; one it cannot open is dropped. Neither
// outcome may fail the request, and CPA ciphertext must never reach the upstream.
func TestOpenAICompatCompactionReplayHandlesCapsulesByReadability(t *testing.T) {
	var gotBodies [][]byte
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		writeOpenAICompatNormalResponse(w)
	})
	defer server.Close()

	auth := openAICompatCompactionTestAuth(server.URL)
	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	ownCapsule, errSeal := helps.SealAntigravityCompaction("summary", "gpt-5.6")
	if errSeal != nil {
		t.Fatalf("seal own capsule: %v", errSeal)
	}
	corrupted := corruptOpenAICompatCompactionCapsule(t, ownCapsule)
	// Capsules minted by an older fork build are no longer readable, but they are still CPA
	// ciphertext, so they must be deleted instead of forwarded.
	legacyPerLaneCapsule := "cpa-compat-compact-v2:AAAAAAAAAAAAAAAA:AAAA"
	legacyDerivedKeyCapsule := "cpa-compat-compact-v3:AAAA"

	tests := []struct {
		name        string
		capsule     string
		wantInlined bool
	}{
		{name: "own capsule", capsule: ownCapsule, wantInlined: true},
		{name: "corrupted capsule", capsule: corrupted},
		{name: "legacy per-lane capsule", capsule: legacyPerLaneCapsule},
		{name: "legacy derived-key capsule", capsule: legacyDerivedKeyCapsule},
		{name: "native opaque capsule", capsule: "not-a-cpa-capsule"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(gotBodies)
			_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.6",
				Payload: []byte(fmt.Sprintf(`{"model":"gpt-5.6","input":[{"type":"compaction","encrypted_content":%q},{"type":"message","role":"user","content":"next"}]}`, test.capsule)),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if errExecute != nil {
				t.Fatalf("an unusable capsule must not fail the request: %v", errExecute)
			}
			sent := gotBodies[before:]
			if len(sent) != 1 {
				t.Fatalf("upstream calls = %d, want 1", len(sent))
			}
			body := string(sent[0])
			if strings.Contains(body, `"type":"compaction"`) || strings.Contains(body, test.capsule) {
				t.Fatalf("compaction ciphertext reached upstream: %s", body)
			}
			inlined := strings.Contains(body, "Context summary from previous turns:")
			if inlined != test.wantInlined {
				t.Fatalf("inlined = %v, want %v; body=%s", inlined, test.wantInlined, body)
			}
			if !strings.Contains(body, "next") {
				t.Fatalf("unrelated request content was lost: %s", body)
			}
		})
	}
}

func TestOpenAICompatCompactionCredentialRotationKeepsCapsuleValid(t *testing.T) {
	var gotBodies [][]byte
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		writeOpenAICompatCompactionSummary(w)
	})
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	authA := openAICompatCompactionTestAuth(server.URL)
	authA.ID = "credential-a"
	authA.Attributes["api_key"] = "key-a"
	authB := openAICompatCompactionTestAuth(server.URL)
	authB.ID = "credential-b"
	authB.Attributes["api_key"] = "key-b"

	compactResp, errCompact := executor.Execute(context.Background(), authA, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: openAICompatCompactionTriggerPayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errCompact != nil {
		t.Fatalf("compaction error: %v", errCompact)
	}
	capsule := gjson.GetBytes(compactResp.Payload, "output.0.encrypted_content").String()
	if capsule == "" {
		t.Fatalf("empty capsule: %s", compactResp.Payload)
	}

	_, errReplay := executor.Execute(context.Background(), authB, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(fmt.Sprintf(`{"model":"gpt-5.6","input":[{"type":"compaction","encrypted_content":%q},{"type":"message","role":"user","content":"next"}]}`, capsule)),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errReplay != nil {
		t.Fatalf("credential-rotated replay error: %v", errReplay)
	}
	if len(gotBodies) != 2 || !strings.Contains(string(gotBodies[1]), "Context summary from previous turns:") {
		t.Fatalf("credential-rotated replay did not expand capsule: %q", gotBodies)
	}
}

func TestOpenAICompatCompactionExpandsOriginalPayloadOnly(t *testing.T) {
	executor := newOpenAICompatCompactionTestExecutor("http://127.0.0.1:1", false)
	auth := openAICompatCompactionTestAuth("http://127.0.0.1:1")
	capsule, errSeal := helps.SealAntigravityCompaction("original summary", "gpt-5.6")
	if errSeal != nil {
		t.Fatalf("seal capsule: %v", errSeal)
	}
	payload := []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"current"}]}`)
	original := []byte(fmt.Sprintf(`{"model":"gpt-5.6","input":[{"type":"compaction","encrypted_content":%q}]}`, capsule))
	expandedPayload, expandedOriginal := executor.expandResponsesCompactionPayloads(context.Background(), auth, payload, original, openAICompatCompactionExpandTranslate)
	if string(expandedPayload) != string(payload) {
		t.Fatalf("payload changed unexpectedly: %s", expandedPayload)
	}
	if strings.Contains(string(expandedOriginal), `"type":"compaction"`) || gjson.GetBytes(expandedOriginal, "input.0.content.0.text").String() != "Context summary from previous turns:\noriginal summary" {
		t.Fatalf("original-only compaction was not expanded: %s", expandedOriginal)
	}
}

func TestOpenAICompatCompactionDropsForeignOriginalPayload(t *testing.T) {
	var gotBodies [][]byte
	server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		writeOpenAICompatNormalResponse(w)
	})
	defer server.Close()

	executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
	auth := openAICompatCompactionTestAuth(server.URL)
	foreignCapsule, errSeal := helps.SealAntigravityCompaction("summary", "gpt-5.6")
	if errSeal != nil {
		t.Fatalf("seal foreign capsule: %v", errSeal)
	}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6",
		Payload: []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"current"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: []byte(fmt.Sprintf(`{"model":"gpt-5.6","input":[{"type":"compaction","encrypted_content":%q}]}`, foreignCapsule)),
	})
	if errExecute != nil {
		t.Fatalf("foreign original payload must not fail the request: %v", errExecute)
	}
	if len(gotBodies) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(gotBodies))
	}
	if strings.Contains(string(gotBodies[0]), `"type":"compaction"`) {
		t.Fatalf("foreign capsule reached upstream: %s", gotBodies[0])
	}
}

// A compact request forwarded to an upstream's native endpoint must keep the
// upstream's own opaque capsule, while CPA-sealed capsules are stripped even on
// that path because the upstream cannot decrypt them.
func TestOpenAICompatCompactionPassthroughKeepsNativeCapsule(t *testing.T) {
	tests := []struct {
		name          string
		capsule       string
		wantPreserved bool
	}{
		{name: "native opaque capsule preserved", capsule: "upstream-native-opaque-blob", wantPreserved: true},
		{name: "cpa capsule stripped", capsule: cpaCapsuleForPassthroughTest(t), wantPreserved: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotBodies [][]byte
			server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBodies = append(gotBodies, body)
				writeOpenAICompatNormalResponse(w)
			})
			defer server.Close()

			executor := newOpenAICompatCompactionTestExecutor(server.URL, false)
			auth := openAICompatCompactionTestAuth(server.URL)
			_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.6",
				Payload: []byte(fmt.Sprintf(`{"model":"gpt-5.6","input":[{"type":"compaction","encrypted_content":%q}]}`, test.capsule)),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"})
			if errExecute != nil {
				t.Fatalf("passthrough compact error: %v", errExecute)
			}
			if len(gotBodies) != 1 {
				t.Fatalf("upstream calls = %d, want 1", len(gotBodies))
			}
			preserved := strings.Contains(string(gotBodies[0]), test.capsule)
			if preserved != test.wantPreserved {
				t.Fatalf("capsule preserved = %v, want %v; body=%s", preserved, test.wantPreserved, gotBodies[0])
			}
		})
	}
}

func cpaCapsuleForPassthroughTest(t *testing.T) string {
	t.Helper()
	capsule, errSeal := helps.SealAntigravityCompaction("summary", "gpt-5.6")
	if errSeal != nil {
		t.Fatalf("seal capsule: %v", errSeal)
	}
	return capsule
}

func TestOpenAICompatCompactionSummaryDoesNotReenterCompaction(t *testing.T) {
	tests := []struct {
		name    string
		alt     string
		summary []byte
	}{
		{name: "trigger", summary: openAICompatCompactionTriggerPayload()},
		{name: "alt", alt: "responses/compact", summary: []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"history"}]}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			server := newOpenAICompatCompactionTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "compaction_trigger") {
					t.Fatalf("compaction trigger reached summary upstream: %s", body)
				}
				writeOpenAICompatCompactionSummary(w)
			})
			defer server.Close()

			executor := newOpenAICompatCompactionTestExecutor(server.URL, true)
			_, errExecute := executor.Execute(context.Background(), openAICompatCompactionTestAuth(server.URL), cliproxyexecutor.Request{
				Model:   "gpt-5.6",
				Payload: test.summary,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse,
				Alt:          test.alt,
			})
			if errExecute != nil {
				t.Fatalf("Execute error: %v", errExecute)
			}
			if calls != 1 {
				t.Fatalf("summary upstream calls = %d, want 1", calls)
			}
		})
	}
}

func newOpenAICompatCompactionTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func newOpenAICompatCompactionTestExecutor(serverURL string, synthesize bool) *OpenAICompatExecutor {
	return NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                          "compat",
			BaseURL:                       "https://configured.example/v1",
			SynthesizeResponsesCompaction: synthesize,
		}},
	})
}

func openAICompatCompactionTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     serverURL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
}

func openAICompatCompactionTriggerPayload() []byte {
	return []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"history"},{"type":"compaction_trigger"}]}`)
}

func writeOpenAICompatCompactionSummary(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"chatcmpl_summary","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"summary text"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
}

func writeOpenAICompatNormalResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"chatcmpl_normal","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func assertOpenAICompatCompactionResponse(t *testing.T, payload []byte) {
	t.Helper()
	output := gjson.GetBytes(payload, "output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected exactly one output item, got %s", payload)
	}
	item := output.Get("0")
	if item.Get("type").String() != "compaction" {
		t.Fatalf("output item type = %q, want compaction; payload=%s", item.Get("type").String(), payload)
	}
	if item.Get("encrypted_content").String() == "" {
		t.Fatalf("encrypted_content is empty: %s", payload)
	}
	for _, itemResult := range output.Array() {
		itemType := itemResult.Get("type").String()
		if itemType == "reasoning" || itemType == "message" {
			t.Fatalf("unexpected ordinary output item %q: %s", itemType, payload)
		}
	}
}

func assertOpenAICompatCompactionObject(t *testing.T, payload []byte, want string) {
	t.Helper()
	if got := gjson.GetBytes(payload, "object").String(); got != want {
		t.Fatalf("object = %q, want %q; payload=%s", got, want, payload)
	}
	if got := gjson.GetBytes(payload, "status").String(); got != "completed" {
		t.Fatalf("status = %q, want completed; payload=%s", got, payload)
	}
}

func collectOpenAICompatCompactionStream(t *testing.T, result *cliproxyexecutor.StreamResult) string {
	t.Helper()
	var streamed strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	return streamed.String()
}

type openAICompatCompactionStreamFrame struct {
	event   string
	payload []byte
}

func parseOpenAICompatCompactionStream(t *testing.T, streamed string) []openAICompatCompactionStreamFrame {
	t.Helper()
	var frames []openAICompatCompactionStreamFrame
	for _, rawFrame := range strings.Split(strings.TrimSuffix(streamed, "\n\n"), "\n\n") {
		if strings.TrimSpace(rawFrame) == "" {
			continue
		}
		var event string
		var payload []byte
		for _, line := range strings.Split(rawFrame, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				payload = []byte(strings.TrimPrefix(line, "data: "))
			}
		}
		if event == "" || len(payload) == 0 {
			t.Fatalf("invalid SSE frame: %q", rawFrame)
		}
		frames = append(frames, openAICompatCompactionStreamFrame{event: event, payload: payload})
	}
	return frames
}

func assertOpenAICompatCompactionStatus(t *testing.T, err error, want int) {
	t.Helper()
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose status code: %v", err, err)
	}
	if got := status.StatusCode(); got != want {
		t.Fatalf("status = %d, want %d; error=%v", got, want, err)
	}
}

// corruptOpenAICompatCompactionCapsule flips one byte of the decoded payload so that the AEAD tag
// no longer matches. Flipping a trailing base64 character is not enough: the unused padding bits
// of the last character decode to the same bytes.
func corruptOpenAICompatCompactionCapsule(t *testing.T, capsule string) string {
	t.Helper()
	separator := strings.LastIndexByte(capsule, ':')
	if separator < 0 {
		t.Fatalf("capsule has no prefix separator: %q", capsule)
	}
	prefix, payload := capsule[:separator+1], capsule[separator+1:]
	decoded, errDecode := base64.RawURLEncoding.DecodeString(payload)
	if errDecode != nil {
		t.Fatalf("decode capsule payload: %v", errDecode)
	}
	if len(decoded) < 2 {
		t.Fatalf("capsule payload is too short: %q", capsule)
	}
	decoded[len(decoded)/2] ^= 1
	return prefix + base64.RawURLEncoding.EncodeToString(decoded)
}
