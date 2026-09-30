package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	responsestools "github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// fakeResponsesExecutor captures the wire payload it receives and returns a
// canned upstream response without touching HTTP or plugins.
type fakeResponsesExecutor struct {
	provider   string
	toFormat   sdktranslator.Format
	wire       [][]byte
	originals  [][]byte
	execCalls  int
	countCalls int
	streamCall int
	response   []byte
	err        error
	wireGuard  *cliproxyexecutor.WireContract
}

func TestCollectResponsesToolsWireRequirementsPreservesQualifiedHistoryNames(t *testing.T) {
	wire := &WireContract{
		CustomAliases:  []string{"ccb_abc"},
		HistoryAliases: []string{"ccb_abc"},
	}
	body := []byte(`{"tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"ccb_abc"}]}],"input":[{"type":"function_call","name":"ccb_abc","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
	if err := collectResponsesToolsWireRequirements(body, wire); err != nil {
		t.Fatalf("collect wire requirements: %v", err)
	}
	if len(wire.RequiredFunctionNames) != 1 || wire.RequiredFunctionNames[0] != "fs__ccb_abc" {
		t.Fatalf("required function names = %v, want namespace-qualified function", wire.RequiredFunctionNames)
	}
	if len(wire.HistoryCalls) != 1 || wire.HistoryCalls[0].Name != "ccb_abc" ||
		wire.HistoryCalls[0].CallID != "call_1" {
		t.Fatalf("history calls = %+v, want the bridged call identity", wire.HistoryCalls)
	}
	if len(wire.HistoryCalls[0].AlternateNames) != 1 || wire.HistoryCalls[0].AlternateNames[0] != "fs__ccb_abc" {
		t.Fatalf("history alternate names = %v, want namespace-qualified wire identity", wire.HistoryCalls[0].AlternateNames)
	}
	hasQualifiedHistoryAlias := false
	for _, alias := range wire.HistoryAliases {
		if alias == "fs__ccb_abc" {
			hasQualifiedHistoryAlias = true
			break
		}
	}
	if !hasQualifiedHistoryAlias {
		t.Fatalf("history aliases = %v, want the namespace-qualified wire identity", wire.HistoryAliases)
	}
}

// A tool name that merely starts with its namespace is still unqualified. The
// guard must require the name the provider translators actually emit, or a
// valid eager namespaced tool is rejected before the request is sent.
func TestCollectResponsesToolsWireRequirementsQualifiesPrefixedToolNames(t *testing.T) {
	wire := &WireContract{}
	body := []byte(`{"tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"fs_read","parameters":{"type":"object"}}]}],"input":[]}`)
	if err := collectResponsesToolsWireRequirements(body, wire); err != nil {
		t.Fatalf("collect wire requirements: %v", err)
	}
	if len(wire.RequiredFunctionNames) != 1 || wire.RequiredFunctionNames[0] != "fs__fs_read" {
		t.Fatalf("required function names = %v, want the namespace-qualified name", wire.RequiredFunctionNames)
	}
}

func (f *fakeResponsesExecutor) Identifier() string { return f.provider }

func (f *fakeResponsesExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	f.execCalls++
	f.wire = append(f.wire, req.Payload)
	f.originals = append(f.originals, opts.OriginalRequest)
	f.wireGuard = cliproxyexecutor.WireContractFromContext(ctx)
	if f.err != nil {
		return cliproxyexecutor.Response{}, f.err
	}
	return cliproxyexecutor.Response{Payload: f.response}, nil
}

func (f *fakeResponsesExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	f.streamCall++
	resp, err := f.Execute(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (f *fakeResponsesExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (f *fakeResponsesExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	f.countCalls++
	f.wire = append(f.wire, req.Payload)
	f.originals = append(f.originals, opts.OriginalRequest)
	return cliproxyexecutor.Response{Payload: []byte("{\"tokens\":1}")}, nil
}

func (f *fakeResponsesExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func (f *fakeResponsesExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return f.toFormat
}

func responsesToolsTestManager() *Manager {
	return responsesToolsTestManagerWithClientSearch("bridge")
}

func responsesToolsTestManagerWithClientSearch(clientSearch string) *Manager {
	return responsesToolsTestManagerWithModes(clientSearch, "inherit")
}

func responsesToolsTestManagerWithModes(clientSearch, customTools string) *Manager {
	cfg := &internalconfig.Config{}
	enabled := true
	cfg.ResponsesTools.Enabled = &enabled
	cfg.ResponsesTools.Routes = []internalconfig.ResponsesToolsRoute{{
		Match: internalconfig.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth",
			UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
		},
		ClientSearch: clientSearch, CustomTools: customTools, CustomGrammar: "describe",
	}}
	cfg.NormalizeResponsesToolsConfig()
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	return mgr
}

func responsesToolsTestAuth(t *testing.T, mgr *Manager) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("rt-auth", "codex", []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
	t.Cleanup(func() { reg.UnregisterClient("rt-auth") })
	_, err := mgr.Register(context.Background(), &Auth{
		ID: "rt-auth", Provider: "codex",
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{"disable_cooling": true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestResponsesToolsExecuteBridgesSearch(t *testing.T) {
	mgr := responsesToolsTestManager()
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	body := "{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatCodex,
		OriginalRequest: []byte(body),
	}
	exec.response = []byte("{\"output\": [{\"type\": \"function_call\", \"id\": \"fc_call_bridge_1\", \"name\": \"tool_search\", \"call_id\": \"c1\", \"arguments\": \"{\\\"query\\\":\\\"x\\\"}\"}]}")
	resp, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(exec.wire) != 1 {
		t.Fatalf("expected one upstream call, got %d", len(exec.wire))
	}
	if strings.Contains(string(exec.wire[0]), "tool_search\":") || strings.Contains(string(exec.wire[0]), "\"type\":\"tool_search\"") {
		t.Fatalf("search declaration leaked upstream: %s", exec.wire[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal(resp.Payload, &decoded); err != nil {
		t.Fatalf("response: %v", err)
	}
	output := decoded["output"].([]any)
	item := output[0].(map[string]any)
	if item["type"] != "tool_search_call" {
		t.Fatalf("search call not restored: %v", item)
	}
}

func TestResponsesToolsExecutePassesWireContractToExecutor(t *testing.T) {
	mgr := responsesToolsTestManager()
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{
		provider: "codex",
		toFormat: sdktranslator.FormatCodex,
		response: []byte(`{"output":[]}`),
	}
	mgr.RegisterExecutor(exec)
	body := `{"tools":[{"type":"tool_search"}],"input":[]}`
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}

	if _, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if exec.wireGuard == nil {
		t.Fatal("executor did not receive the prepared wire contract")
	}
	if len(exec.wireGuard.SearchAliases) != 1 || exec.wireGuard.SearchAliases[0] == "" {
		t.Fatalf("search aliases = %v, want one active alias", exec.wireGuard.SearchAliases)
	}
}

type failingResponsesToolsStreamAttempt struct {
	closed chan struct{}
}

func (a *failingResponsesToolsStreamAttempt) Feed([]byte) ([][]byte, error) {
	return nil, errors.New("bridge failure")
}

func (*failingResponsesToolsStreamAttempt) Finish() ([][]byte, error) { return nil, nil }

func (a *failingResponsesToolsStreamAttempt) Close() { close(a.closed) }

type terminalResponsesToolsStreamAttempt struct {
	closed      chan struct{}
	finishCalls int
	finishErr   error
}

func (*terminalResponsesToolsStreamAttempt) Feed([]byte) ([][]byte, error) { return nil, nil }
func (a *terminalResponsesToolsStreamAttempt) Finish() ([][]byte, error) {
	a.finishCalls++
	return nil, a.finishErr
}
func (a *terminalResponsesToolsStreamAttempt) Close() { close(a.closed) }

func TestAdaptResponsesToolsStreamCancelsUpstreamAfterBridgeError(t *testing.T) {
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	attemptCtx, cancelAttempt := context.WithCancel(parentCtx)
	attempt := &failingResponsesToolsStreamAttempt{closed: make(chan struct{})}
	upstream := make(chan cliproxyexecutor.StreamChunk)
	producerExited := make(chan struct{})
	go func() {
		defer close(producerExited)
		select {
		case upstream <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.output_item.added"}`)}:
		case <-attemptCtx.Done():
			return
		}
		<-attemptCtx.Done()
	}()

	result := adaptResponsesToolsStream(parentCtx, attemptCtx, cancelAttempt, attempt, &cliproxyexecutor.StreamResult{Chunks: upstream}, false)
	chunk, ok := <-result.Chunks
	if !ok || chunk.Err == nil {
		t.Fatalf("stream chunk = %+v, open = %v; want one bridge error", chunk, ok)
	}
	select {
	case <-producerExited:
	case <-time.After(time.Second):
		t.Fatal("upstream producer did not exit after bridge failure")
	}
	select {
	case <-attempt.closed:
	default:
		t.Fatal("attempt lease was not released after bridge failure")
	}
}

func TestAdaptResponsesToolsStreamPreservesUpstreamErrorOverFinishError(t *testing.T) {
	upstreamErr := errors.New("upstream 429")
	finishErr := errors.New("incomplete bridge tail")
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	attemptCtx, cancelAttempt := context.WithCancel(parentCtx)
	attempt := &terminalResponsesToolsStreamAttempt{
		closed:    make(chan struct{}),
		finishErr: finishErr,
	}
	upstream := make(chan cliproxyexecutor.StreamChunk, 1)
	upstream <- cliproxyexecutor.StreamChunk{Err: upstreamErr}
	close(upstream)

	result := adaptResponsesToolsStream(parentCtx, attemptCtx, cancelAttempt, attempt, &cliproxyexecutor.StreamResult{Chunks: upstream}, false)
	chunk, ok := <-result.Chunks
	if !ok || !errors.Is(chunk.Err, upstreamErr) {
		t.Fatalf("first terminal chunk = %+v, open = %v; want original upstream error", chunk, ok)
	}
	if _, ok := <-result.Chunks; ok {
		t.Fatal("stream emitted more than one terminal error")
	}
	if attempt.finishCalls != 0 {
		t.Fatalf("Finish called %d times after upstream failure, want 0", attempt.finishCalls)
	}
	select {
	case <-attempt.closed:
	case <-time.After(time.Second):
		t.Fatal("attempt lease was not released after upstream failure")
	}
}

func TestNormalizeCodexResponsesDataLine(t *testing.T) {
	input := []byte(`data: {"type":"response.created","response":{"id":"r1"}}`)
	got := normalizeCodexResponsesDataLine(input)
	if string(got) != `{"type":"response.created","response":{"id":"r1"}}` {
		t.Fatalf("normalized data line = %q", got)
	}
	for _, dropped := range [][]byte{
		[]byte(`event: response.created`),
		[]byte("id: 42"),
		[]byte("retry: 1000"),
		[]byte(": keep-alive"),
		[]byte("  \n"),
	} {
		if got := normalizeCodexResponsesDataLine(dropped); len(got) != 0 {
			t.Fatalf("redundant field line kept: input=%q output=%q", dropped, got)
		}
	}
	for _, unchanged := range [][]byte{
		[]byte(`data: {"type":"response.created"`),
		[]byte("data: {\"type\":\"response.created\"}\ndata: continued"),
		[]byte("data: [DONE]"),
	} {
		if got := normalizeCodexResponsesDataLine(unchanged); string(got) != string(unchanged) {
			t.Fatalf("incomplete/non-event frame changed: input=%q output=%q", unchanged, got)
		}
	}
}

// A codex-mode producer that emits the upstream event: field lines as their own
// chunks used to leave the feed stuck in SSE framing, so the stream failed as
// incomplete. Normalizing the sequence must let it finish.
func TestNormalizeCodexResponsesDataLineLetsFeedFinish(t *testing.T) {
	contract := responsestools.ParseContract([]byte(`{"tools":[{"type":"tool_search"}],"input":[]}`))
	if contract == nil {
		t.Fatal("no contract")
	}
	contract.SearchBridged = true
	feed := responsestools.NewStreamFeed(contract, nil, &responsestools.Lease{}, responsestools.DefaultLimits())
	for _, chunk := range [][]byte{
		[]byte("event: response.created"),
		[]byte(`data: {"type":"response.created","response":{"id":"r1"}}`),
		[]byte("\n"),
		[]byte("event: response.completed"),
		[]byte(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`),
	} {
		payload := normalizeCodexResponsesDataLine(chunk)
		if len(payload) == 0 {
			continue
		}
		if _, err := feed.Feed(payload); err != nil {
			t.Fatalf("feed %q: %v", chunk, err)
		}
	}
	if _, err := feed.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestResponsesToolsExecutePassthroughWhenDisabled(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	body := "{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}
	exec.response = []byte("{\"ok\":true}")
	resp, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(exec.wire[0]) != body {
		t.Fatalf("disabled route must pass through byte-for-byte")
	}
	if string(resp.Payload) != "{\"ok\":true}" {
		t.Fatalf("response must pass through")
	}
}

func TestResponsesToolsBridgeErrorStopsWithoutRotation(t *testing.T) {
	mgr := responsesToolsTestManager()
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	// disabled client-search rejects explicit search contracts at Prepare.
	cfg := &internalconfig.Config{}
	enabled := true
	cfg.ResponsesTools.Enabled = &enabled
	cfg.ResponsesTools.Routes = []internalconfig.ResponsesToolsRoute{{
		Match: internalconfig.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth",
			UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
		},
		ClientSearch: "disabled", CustomTools: "inherit",
	}}
	cfg.NormalizeResponsesToolsConfig()
	mgr.SetConfig(cfg)
	body := "{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}
	_, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
	if err == nil {
		t.Fatalf("expected rejection")
	}
	if len(exec.wire) != 0 {
		t.Fatalf("rejected request must never reach upstream")
	}
}

func TestResponsesToolsStreamPrepareErrorStopsWithoutNilStreamPanic(t *testing.T) {
	mgr := responsesToolsTestManagerWithClientSearch("disabled")
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	body := `{"tools":[{"type":"tool_search"}],"input":[]}`
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatCodex,
		OriginalRequest: []byte(body),
	}

	_, err := mgr.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
	if err == nil {
		t.Fatal("expected the disabled client-search request to stop")
	}
	var statusErr interface{ StatusCode() int }
	if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusUnprocessableEntity {
		t.Fatalf("stream error status = %v, want 422", err)
	}
	if len(exec.wire) != 0 {
		t.Fatalf("rejected stream request reached the executor: %d calls", len(exec.wire))
	}
}

func TestResponsesToolsHistoryOnlyCustomDoesNotRequireDeclaration(t *testing.T) {
	mgr := responsesToolsTestManagerWithModes("bridge", "function")
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{
		provider: "codex", toFormat: sdktranslator.FormatCodex,
		response: []byte(`{"output":[]}`),
	}
	mgr.RegisterExecutor(exec)
	body := `{"model":"gpt-5.6-sol","input":[{"type":"custom_tool_call","name":"apply_patch","call_id":"c1","input":"patch"},{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]}`
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}

	if _, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
		t.Fatalf("history-only custom call: %v", err)
	}
	if len(exec.wire) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(exec.wire))
	}
	wire := string(exec.wire[0])
	if strings.Contains(wire, `"type":"custom_tool_call"`) {
		t.Fatalf("custom history was not bridged: %s", wire)
	}
}

func TestNormalizeResponsesStreamFrame(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"full frame gains terminator", "event: response.created\ndata: {\"type\":\"response.created\"}", "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"},
		{"data-only frame gains terminator", "data: {\"type\":\"response.created\"}", "data: {\"type\":\"response.created\"}\n\n"},
		{"trailing newline is completed", "data: {\"type\":\"response.created\"}\n", "data: {\"type\":\"response.created\"}\n\n"},
		{"terminated frame unchanged", "data: {\"type\":\"response.created\"}\n\n", "data: {\"type\":\"response.created\"}\n\n"},
		{"terminated CRLF frame unchanged", "data: {\"type\":\"response.created\"}\r\n\r\n", "data: {\"type\":\"response.created\"}\r\n\r\n"},
		{"truncated JSON unchanged", "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\"", "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\""},
		{"event only unchanged", "event: response.created\n", "event: response.created\n"},
		{"two frames unchanged", "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.in_progress\"}", "data: {\"type\":\"response.created\"}\n\ndata: {\"type\":\"response.in_progress\"}"},
		{"non responses type unchanged", "data: {\"type\":\"other.event\"}", "data: {\"type\":\"other.event\"}"},
		{"bare JSON unchanged", "{\"type\":\"response.created\"}", "{\"type\":\"response.created\"}"},
		{"done frame gains terminator", "data: [DONE]", "data: [DONE]\n\n"},
	}
	for _, tc := range cases {
		if got := string(normalizeResponsesStreamFrame([]byte(tc.in))); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

type feedBackedAttempt struct {
	feed *responsestools.StreamFeed
}

func (a *feedBackedAttempt) Feed(chunk []byte) ([][]byte, error) { return a.feed.Feed(chunk) }
func (a *feedBackedAttempt) Finish() ([][]byte, error)           { return a.feed.Finish() }
func (*feedBackedAttempt) Close()                                {}

func TestAdaptResponsesToolsStreamTerminatesTranslatorFrames(t *testing.T) {
	contract := responsestools.ParseContract([]byte(`{"tools":[{"type":"tool_search"}],"input":[]}`))
	if contract == nil {
		t.Fatal("no contract")
	}
	attempt := &feedBackedAttempt{feed: responsestools.NewStreamFeed(contract, nil, &responsestools.Lease{}, responsestools.DefaultLimits())}
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	attemptCtx, cancelAttempt := context.WithCancel(parentCtx)
	upstream := make(chan cliproxyexecutor.StreamChunk, 4)
	for _, frame := range []string{
		"event: response.created\ndata: {\"type\":\"response.created\"}",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}",
	} {
		upstream <- cliproxyexecutor.StreamChunk{Payload: []byte(frame)}
	}
	close(upstream)

	result := adaptResponsesToolsStream(parentCtx, attemptCtx, cancelAttempt, attempt, &cliproxyexecutor.StreamResult{Chunks: upstream}, false)
	var body strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		body.Write(chunk.Payload)
	}
	got := body.String()
	if !strings.Contains(got, "response.created") || !strings.Contains(got, "response.completed") {
		t.Fatalf("terminal frames were not forwarded: %q", got)
	}
}

func TestAdaptResponsesToolsStreamStillRejectsTruncatedTail(t *testing.T) {
	contract := responsestools.ParseContract([]byte(`{"tools":[{"type":"tool_search"}],"input":[]}`))
	if contract == nil {
		t.Fatal("no contract")
	}
	attempt := &feedBackedAttempt{feed: responsestools.NewStreamFeed(contract, nil, &responsestools.Lease{}, responsestools.DefaultLimits())}
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	attemptCtx, cancelAttempt := context.WithCancel(parentCtx)
	upstream := make(chan cliproxyexecutor.StreamChunk, 2)
	upstream <- cliproxyexecutor.StreamChunk{Payload: []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\"")}
	close(upstream)

	result := adaptResponsesToolsStream(parentCtx, attemptCtx, cancelAttempt, attempt, &cliproxyexecutor.StreamResult{Chunks: upstream}, false)
	sawError := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("truncated tail was accepted as success")
	}
}

// A replayed native history is repaired before it is sent, on every entry
// point, and the repaired body is what the executor actually receives. A nil
// attempt does not mean the request was left alone.
func TestResponsesToolsNativeRepairAllEntryPoints(t *testing.T) {
	const polluted = `{"tools":[{"type":"tool_search","execution":"client"}],"input":[` +
		`{"type":"tool_search_call","id":"fc_call_function_olq4gx3ow6cs_1","call_id":"c1","arguments":{},"execution":"client"},` +
		`{"type":"tool_search_output","id":"tso_out_1","call_id":"c1","execution":"client","tools":[]}]}`
	const repaired = "tsc_call_function_olq4gx3ow6cs_1"

	for _, testCase := range []struct {
		name   string
		invoke func(*Manager, cliproxyexecutor.Request, cliproxyexecutor.Options) error
		calls  func(*fakeResponsesExecutor) int
	}{
		{
			name: "execute",
			invoke: func(mgr *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
				return err
			},
			calls: func(exec *fakeResponsesExecutor) int { return exec.execCalls },
		},
		{
			name: "count",
			invoke: func(mgr *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, err := mgr.ExecuteCount(context.Background(), []string{"codex"}, req, opts)
				return err
			},
			calls: func(exec *fakeResponsesExecutor) int { return exec.countCalls },
		},
		{
			name: "stream",
			invoke: func(mgr *Manager, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				result, err := mgr.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				if err != nil {
					return err
				}
				for range result.Chunks {
				}
				return nil
			},
			calls: func(exec *fakeResponsesExecutor) int { return exec.streamCall },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mgr := responsesToolsTestManagerWithClientSearch("native")
			responsesToolsTestAuth(t, mgr)
			exec := &fakeResponsesExecutor{
				provider: "codex", toFormat: sdktranslator.FormatCodex,
				response: []byte(`{"output":[]}`),
			}
			mgr.RegisterExecutor(exec)
			req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(polluted)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(polluted)}

			if err := testCase.invoke(mgr, req, opts); err != nil {
				t.Fatalf("%s: %v", testCase.name, err)
			}
			if got := testCase.calls(exec); got != 1 {
				t.Fatalf("%s reached the executor %d times, want 1", testCase.name, got)
			}
			if len(exec.wire) != 1 || !strings.Contains(string(exec.wire[0]), repaired) {
				t.Fatalf("%s did not send the repaired body: %s", testCase.name, exec.wire)
			}
			// Both body views stay in step, so a translator that reads the
			// original request sees the same repaired history.
			if len(exec.originals) != 1 || !strings.Contains(string(exec.originals[0]), repaired) {
				t.Fatalf("%s original request was not repaired: %s", testCase.name, exec.originals)
			}
			if strings.Contains(string(exec.wire[0]), "fc_call_function_olq4gx3ow6cs_1") {
				t.Fatalf("%s still carries the polluted id: %s", testCase.name, exec.wire[0])
			}
			// A pass-through repair builds no attempt, so there is no wire
			// contract to hand the executor.
			if exec.wireGuard != nil {
				t.Fatalf("%s received a wire contract for a pass-through repair: %+v", testCase.name, exec.wireGuard)
			}
		})
	}
}

// A request the repair refuses must never reach the upstream, and it must not
// be retried, rotated, or cooled down: the failure belongs to this request.
func TestResponsesToolsRepairErrorStopsWithoutRotation(t *testing.T) {
	const polluted = `{"previous_response_id":"resp_1","tools":[{"type":"tool_search","execution":"client"}],"input":[` +
		`{"type":"tool_search_call","id":"fc_call_function_olq4gx3ow6cs_1","call_id":"c1","arguments":{},"execution":"client"}]}`
	mgr := responsesToolsTestManagerWithClientSearch("native")
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex, response: []byte(`{"output":[]}`)}
	mgr.RegisterExecutor(exec)
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(polluted)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(polluted)}

	_, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
	if err == nil {
		t.Fatal("expected the opaque repair refusal to surface")
	}
	if !responsestools.IsRequestScopedError(err) {
		t.Fatalf("error must stay request scoped: %v", err)
	}
	if exec.execCalls != 0 || len(exec.wire) != 0 {
		t.Fatalf("the refused request reached the executor: %d calls", exec.execCalls)
	}
}

// With the feature switched off the request keeps its original bytes and
// slices, so the emergency gate really is a passthrough.
func TestResponsesToolsRepairIsSkippedWhenDisabled(t *testing.T) {
	const polluted = `{"tools":[{"type":"tool_search","execution":"client"}],"input":[` +
		`{"type":"tool_search_call","id":"fc_call_function_olq4gx3ow6cs_1","call_id":"c1","arguments":{},"execution":"client"}]}`
	cfg := &internalconfig.Config{}
	enabled := false
	cfg.ResponsesTools.Enabled = &enabled
	cfg.ResponsesTools.Routes = []internalconfig.ResponsesToolsRoute{{
		Match: internalconfig.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth",
			UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
		},
		ClientSearch: "native", CustomTools: "inherit", CustomGrammar: "describe",
	}}
	cfg.NormalizeResponsesToolsConfig()
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex, response: []byte(`{"output":[]}`)}
	mgr.RegisterExecutor(exec)
	payload := []byte(polluted)
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: payload}

	if _, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(exec.wire) != 1 || string(exec.wire[0]) != polluted {
		t.Fatalf("disabled feature must stay a passthrough: %s", exec.wire[0])
	}
}
