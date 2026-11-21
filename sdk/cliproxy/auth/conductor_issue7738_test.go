package auth

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7738ResultHook struct {
	mu      sync.Mutex
	results []Result
	done    chan struct{}
	once    sync.Once
}

func newIssue7738ResultHook() *issue7738ResultHook {
	return &issue7738ResultHook{
		done: make(chan struct{}),
	}
}

func (*issue7738ResultHook) OnAuthRegistered(context.Context, *Auth) {}
func (*issue7738ResultHook) OnAuthUpdated(context.Context, *Auth)    {}

func (h *issue7738ResultHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
	h.once.Do(func() { close(h.done) })
}

func (h *issue7738ResultHook) snapshot() []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Result, len(h.results))
	copy(out, h.results)
	return out
}

func collectIssue7738Stream(t *testing.T, res *cliproxyexecutor.StreamResult) []byte {
	t.Helper()
	if res == nil || res.Chunks == nil {
		t.Fatal("wrapStreamResult() returned nil stream or chunks channel")
	}
	var out []byte
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		out = append(out, chunk.Payload...)
	}
	return out
}

// TestIssue7738WrapStreamResultDoesNotRebufferFinishTail verifies that the
// already-rewritten tail returned by finishForceMappedStreamChunks is not fed
// back through rewriteForceMappedStreamChunk(rewriter, tail). When Finish()
// flushes a rewritten frame followed by an unterminated trailing line after
// "\n\n", re-feeding the tail into the finalized rewriter re-buffers the
// trailing segment into pendingBuf and silently drops it.
func TestIssue7738WrapStreamResultDoesNotRebufferFinishTail(t *testing.T) {
	hook := newIssue7738ResultHook()
	manager := NewManager(nil, nil, hook)
	auth := &Auth{ID: "issue7738-auth-tail", Provider: "antigravity", Status: StatusActive}

	remaining := make(chan cliproxyexecutor.StreamChunk)
	close(remaining)

	buffered := []cliproxyexecutor.StreamChunk{
		{
			Payload: []byte(
				`data: {"type":"response.created","response":{"model":"upstream-model"}}` + "\n\n" +
					`data: {"type":"response.completed","response":{"model":"upstream-model"}}` + "\n" +
					`data: {"partial":true`,
			),
		},
	}

	aliasResult := OAuthModelAliasResult{
		UpstreamModel: "upstream-model",
		ForceMapping:  true,
		OriginalAlias: "my-alias",
	}

	res := manager.wrapStreamResult(
		context.Background(),
		auth,
		"antigravity",
		"my-alias",
		"my-alias",
		nil,
		buffered,
		remaining,
		aliasResult,
		true,
		cliproxyexecutor.Options{},
		1,
	)

	got := string(collectIssue7738Stream(t, res))
	if !strings.Contains(got, `"type":"response.completed"`) {
		t.Fatalf("stream output missing response.completed event: %q", got)
	}
	if !strings.Contains(got, `"model":"my-alias"`) {
		t.Fatalf("stream output missing rewritten alias model: %q", got)
	}
	if strings.Contains(got, "upstream-model") {
		t.Fatalf("stream output leaked upstream model: %q", got)
	}
	if !strings.Contains(got, `data: {"partial":true`) {
		t.Fatalf("flushed tail remainder was silently dropped by re-feeding into rewriter: %q", got)
	}
}

// TestIssue7738WrapStreamResultFlushesUnterminatedFinalEvent verifies that a
// buffered final SSE event without a trailing "\n\n" delimiter is delivered on
// Chunks with its model rewritten to the configured alias and still records
// CompletedResponse.
func TestIssue7738WrapStreamResultFlushesUnterminatedFinalEvent(t *testing.T) {
	hook := newIssue7738ResultHook()
	manager := NewManager(nil, nil, hook)
	auth := &Auth{ID: "issue7738-auth-final", Provider: "antigravity", Status: StatusActive}

	remaining := make(chan cliproxyexecutor.StreamChunk)
	close(remaining)

	buffered := []cliproxyexecutor.StreamChunk{
		{
			Payload: []byte(
				"event: response.created\n" +
					`data: {"type":"response.created","response":{"model":"upstream-model"}}` + "\n\n" +
					"event: response.completed\n" +
					`data: {"type":"response.completed","response":{"model":"upstream-model"}}`,
			),
		},
	}

	aliasResult := OAuthModelAliasResult{
		UpstreamModel: "upstream-model",
		ForceMapping:  true,
		OriginalAlias: "my-alias",
	}

	res := manager.wrapStreamResult(
		context.Background(),
		auth,
		"antigravity",
		"my-alias",
		"my-alias",
		nil,
		buffered,
		remaining,
		aliasResult,
		true,
		cliproxyexecutor.Options{},
		1,
	)

	got := string(collectIssue7738Stream(t, res))
	if !strings.Contains(got, "event: response.completed") || !strings.Contains(got, `"type":"response.completed"`) {
		t.Fatalf("stream output missing flushed response.completed event: %q", got)
	}
	if count := strings.Count(got, `"model":"my-alias"`); count != 2 {
		t.Fatalf("rewritten alias model count = %d, want 2; output=%q", count, got)
	}
	if strings.Contains(got, "upstream-model") {
		t.Fatalf("stream output leaked upstream model: %q", got)
	}

	select {
	case <-hook.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnResult after stream completion")
	}
	results := hook.snapshot()
	if len(results) != 1 || !results[0].Success || !results[0].CompletedResponse {
		t.Fatalf("recorded results = %+v, want single Success=true CompletedResponse=true result", results)
	}
}

// TestIssue7738WrapStreamResultFlushedGluedTailRecordsCompletion verifies that
// forwarding the flushed tail through emit (after clearing rewriter) still runs
// streamPayloadCompletionFlags on the flushed tail, so completion events that
// were glued in the raw upstream chunk still mark CompletedResponse=true.
func TestIssue7738WrapStreamResultFlushedGluedTailRecordsCompletion(t *testing.T) {
	hook := newIssue7738ResultHook()
	manager := NewManager(nil, nil, hook)
	auth := &Auth{ID: "issue7738-auth-glued", Provider: "antigravity", Status: StatusActive}

	remaining := make(chan cliproxyexecutor.StreamChunk)
	close(remaining)

	// On the raw chunk, the second line glues two JSON objects with "}data:", so
	// streamPayloadCompletionFlags cannot parse response.completed until Finish()
	// flushes the normalized tail.
	buffered := []cliproxyexecutor.StreamChunk{
		{
			Payload: []byte(
				`data: {"type":"response.created","response":{"model":"upstream-model"}}event: delta` + "\n" +
					`data: {"type":"delta","model":"upstream-model"}data: {"type":"response.completed","response":{"model":"upstream-model"}}`,
			),
		},
	}

	aliasResult := OAuthModelAliasResult{
		UpstreamModel: "upstream-model",
		ForceMapping:  true,
		OriginalAlias: "my-alias",
	}

	res := manager.wrapStreamResult(
		context.Background(),
		auth,
		"antigravity",
		"my-alias",
		"my-alias",
		nil,
		buffered,
		remaining,
		aliasResult,
		true,
		cliproxyexecutor.Options{},
		1,
	)

	got := string(collectIssue7738Stream(t, res))
	if !strings.Contains(got, `"type":"response.completed"`) {
		t.Fatalf("stream output missing flushed response.completed event: %q", got)
	}
	if strings.Contains(got, "upstream-model") {
		t.Fatalf("stream output leaked upstream model: %q", got)
	}

	select {
	case <-hook.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OnResult after stream completion")
	}
	results := hook.snapshot()
	if len(results) != 1 || !results[0].Success || !results[0].CompletedResponse {
		t.Fatalf("recorded results = %+v, want single Success=true CompletedResponse=true result", results)
	}
}

// TestIssue7738WrapStreamResultTailRespectsContextCancellation verifies that
// context cancellation while delivering the flushed tail stops emission without
// blocking, delivering the tail chunk, or recording a successful execution result.
func TestIssue7738WrapStreamResultTailRespectsContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		hook := newIssue7738ResultHook()
		manager := NewManager(nil, nil, hook)
		auth := &Auth{ID: "issue7738-auth-cancel", Provider: "antigravity", Status: StatusActive}

		remaining := make(chan cliproxyexecutor.StreamChunk)
		close(remaining)

		buffered := []cliproxyexecutor.StreamChunk{
			{
				Payload: []byte(
					`data: {"type":"response.created","response":{"model":"upstream-model"}}` + "\n\n" +
						`data: {"type":"response.completed","response":{"model":"upstream-model"}}`,
				),
			},
		}

		aliasResult := OAuthModelAliasResult{
			UpstreamModel: "upstream-model",
			ForceMapping:  true,
			OriginalAlias: "my-alias",
		}

		res := manager.wrapStreamResult(
			ctx,
			auth,
			"antigravity",
			"my-alias",
			"my-alias",
			nil,
			buffered,
			remaining,
			aliasResult,
			true,
			cliproxyexecutor.Options{},
			1,
		)

		// Read only the first emitted frame, then wait until the forwarder goroutine
		// is durably blocked attempting to send the flushed tail on res.Chunks.
		first, ok := <-res.Chunks
		if !ok || !bytes.Contains(first.Payload, []byte("response.created")) {
			t.Fatalf("first chunk = (%q, %v), want response.created", first.Payload, ok)
		}
		synctest.Wait()

		// Cancel without reading from res.Chunks so a blocking send ignoring ctx.Done()
		// cannot make progress.
		cancel()
		synctest.Wait()

		select {
		case chunk, open := <-res.Chunks:
			if open {
				t.Fatalf("unexpected tail chunk delivered after cancel: %q", chunk.Payload)
			}
		default:
			t.Fatal("Chunks channel remained open after cancel; tail send did not exit on ctx.Done()")
		}

		if results := hook.snapshot(); len(results) != 0 {
			t.Fatalf("recorded results after canceled tail send = %+v, want none", results)
		}
	})
}
