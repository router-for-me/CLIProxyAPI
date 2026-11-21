package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"google.golang.org/protobuf/encoding/protowire"
)

// devinStallTestRequest builds a minimal interactions request for stream tests.
func devinStallTestRequest(t *testing.T, format sdktranslator.Format) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	t.Helper()
	original := []byte(`{"model":"devin/swe-2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: original}
	req := cliproxyexecutor.Request{
		Model:   "devin/swe-2",
		Payload: sdktranslator.TranslateRequest(format, sdktranslator.FormatInteractions, "devin/swe-2", original, true),
	}
	return req, opts
}

func devinStallDrain(out <-chan cliproxyexecutor.StreamChunk) (payloads []string, errs []error) {
	for len(out) > 0 {
		chunk := <-out
		if chunk.Err != nil {
			errs = append(errs, chunk.Err)
			continue
		}
		payloads = append(payloads, string(chunk.Payload))
	}
	return payloads, errs
}

// TestDevinStreamEmptyCompletionFailsOver proves a clean EOS stream that
// reports an all-zero usage is emitted as a failover-eligible 502 failure
// instead of an empty 200 — the scheduler can try another credential.
func TestDevinStreamEmptyCompletionFailsOver(t *testing.T) {
	// Usage field 7 with every token counter zero: the dead-slot signature
	// observed upstream (accepted turn, 0/0/0 usage, dangling stream).
	f7 := protowire.AppendTag(nil, 2, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 0)
	f7 = protowire.AppendTag(f7, 3, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 0)
	f7 = protowire.AppendTag(f7, 5, protowire.VarintType)
	f7 = protowire.AppendVarint(f7, 0)
	usageFrame := protowire.AppendTag(nil, 7, protowire.BytesType)
	usageFrame = protowire.AppendBytes(usageFrame, f7)
	body := io.MultiReader(
		bytes.NewReader(helps.WrapConnectEnvelope(usageFrame)),
		bytes.NewReader(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))),
	)
	out := make(chan cliproxyexecutor.StreamChunk, 64)
	req, opts := devinStallTestRequest(t, sdktranslator.FormatOpenAI)
	e := &DevinExecutor{}
	e.streamDevinFrames(context.Background(), body, req, opts, "swe-2-high", sdktranslator.FormatOpenAI, nil, out)
	_, errs := devinStallDrain(out)
	if len(errs) == 0 {
		t.Fatal("expected a stream error for an empty upstream completion, got none")
	}
	// A request-scoped error would stop the scheduler before credential
	// failover; the dead slot must stay on the failover-eligible 502 path.
	var requestErr cliproxyexecutor.RequestScopedError
	if errors.As(errs[0], &requestErr) && requestErr.IsRequestScoped() {
		t.Fatalf("empty completion error = %v (%T), must stay failover-eligible (not request-scoped)", errs[0], errs[0])
	}
	if !strings.Contains(errs[0].Error(), "empty completion") {
		t.Fatalf("empty completion error = %q, want message about empty completion", errs[0].Error())
	}
}

// TestDevinStreamBootstrapHeldUntilGenerated proves bookkeeping events stay
// buffered until the first generated (step.*) event, keeping the downstream
// stream uncommitted while the upstream has produced nothing.
func TestDevinStreamBootstrapHeldUntilGenerated(t *testing.T) {
	// A normal stream: a text frame followed by a clean EOS trailer.
	content := protowire.AppendTag(nil, 3, protowire.BytesType)
	content = protowire.AppendString(content, "hello")
	body := io.MultiReader(
		bytes.NewReader(helps.WrapConnectEnvelope(content)),
		bytes.NewReader(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`))),
	)
	out := make(chan cliproxyexecutor.StreamChunk, 64)
	req, opts := devinStallTestRequest(t, sdktranslator.FormatInteractions)
	e := &DevinExecutor{}
	e.streamDevinFrames(context.Background(), body, req, opts, "swe-2-high", sdktranslator.FormatInteractions, nil, out)
	payloads, errs := devinStallDrain(out)
	if len(errs) != 0 {
		t.Fatalf("unexpected stream errors on a healthy stream: %v", errs)
	}
	if len(payloads) == 0 {
		t.Fatal("expected emitted events for a healthy stream, got none")
	}
	joined := strings.Join(payloads, "\n")
	createdIdx := strings.Index(joined, `"event_type":"interaction.created"`)
	stepIdx := strings.Index(joined, `"event_type":"step.`)
	completedIdx := strings.Index(joined, `"event_type":"interaction.completed"`)
	if createdIdx < 0 || stepIdx < 0 || completedIdx < 0 {
		t.Fatalf("expected created+step+completed events, got: %s", joined)
	}
	if createdIdx > stepIdx {
		t.Fatalf("interaction.created must be released before the first generated event, got: %s", joined)
	}
}
