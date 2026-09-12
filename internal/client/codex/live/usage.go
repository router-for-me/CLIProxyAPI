package live

import (
	"context"
	"errors"
	"io"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// Observe only upstream data events. Client frames and repeated response.done
// notifications must never manufacture or double-count provider usage.
type liveUsageObserver struct {
	ctx                context.Context
	auth               *auth.Auth
	model              string
	transcriptionModel string
	seen               map[string]bool
	pending            map[string]*helps.UsageReporter
}

func newLiveUsageObserver(ctx context.Context, selected *auth.Auth, model string) *liveUsageObserver {
	return &liveUsageObserver{ctx: ctx, auth: selected, model: model, seen: map[string]bool{}, pending: map[string]*helps.UsageReporter{}}
}
func liveUsageDetail(payload []byte) (usage.Detail, bool) {
	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "response.done", "response.completed", "response.incomplete":
		return helps.ParseCodexUsage(payload)
	case "conversation.item.input_audio_transcription.completed":
		d := helps.ParseOpenAIUsage(payload)
		return d, d.UsageObserved
	}
	return usage.Detail{}, false
}
func (o *liveUsageObserver) observe(payload []byte) {
	root := gjson.ParseBytes(payload)
	if model := root.Get("session.model").String(); model != "" {
		o.model = model
	}
	for _, path := range []string{"session.audio.input.transcription", "session.input_audio_transcription"} {
		if node := root.Get(path); node.Exists() {
			o.transcriptionModel = node.Get("model").String()
			break
		}
	}
	kind := root.Get("type").String()
	id := root.Get("response.id").String()
	if id == "" {
		id = root.Get("item_id").String()
	}
	transcription := kind == "conversation.item.input_audio_transcription.completed"
	if transcription && id != "" {
		id = "transcription:" + id + ":" + root.Get("content_index").String()
	} else if id != "" {
		id = "response:" + id
	}
	if kind == "response.created" && id != "" && !o.seen[id] {
		model := root.Get("response.model").String()
		if model == "" {
			model = o.model
		}
		r := helps.NewUsageReporter(usage.WithNewGeneration(o.ctx), "codex", model, o.auth)
		r.SetStream(true)
		r.SetTransport("websocket")
		o.pending[id] = r
		return
	}
	terminal := kind == "response.done" || kind == "response.completed" || kind == "response.incomplete" || kind == "conversation.item.input_audio_transcription.completed"
	if !terminal || id != "" && o.seen[id] {
		return
	}
	detail, _ := liveUsageDetail(payload)
	model := root.Get("response.model").String()
	if model == "" {
		model = o.model
	}
	if transcription {
		model = o.transcriptionModel
		if model == "" {
			model = "unknown"
		}
	}
	r := o.pending[id]
	if r == nil {
		r = helps.NewUsageReporter(usage.WithNewGeneration(o.ctx), "codex", model, o.auth)
		r.SetStream(true)
		r.SetTransport("websocket")
	}
	if transcription {
		r.SetOperation("tool", "")
	}
	ctx := usage.WithNewGeneration(o.ctx)
	status := root.Get("response.status").String()
	if status == "failed" || status == "cancelled" || status == "incomplete" {
		r.PublishFailureWithDetail(ctx, detail, errors.New("realtime response "+status))
	} else {
		r.Publish(ctx, detail)
	}
	if id != "" {
		o.seen[id] = true
		delete(o.pending, id)
	}
}
func (o *liveUsageObserver) close() {
	for _, r := range o.pending {
		r.PublishFailure(o.ctx, errors.New("realtime connection closed before terminal usage"))
	}
}

// Bounded capture never limits forwarding of large media frames.
type usageFrameCapture struct {
	data     []byte
	overflow bool
}

func (b *usageFrameCapture) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 1<<20 {
		b.overflow = true
	} else if !b.overflow {
		b.data = append(b.data, p...)
	}
	return len(p), nil
}

// Read-side capture must survive a downstream write failure: the provider has
// already incurred usage even when the client no longer receives the response.
func forwardUsageFrame(writer io.Writer, reader io.Reader, observers ...func([]byte)) error {
	if len(observers) == 0 {
		_, errCopy := io.Copy(writer, reader)
		return errCopy
	}
	capture := &usageFrameCapture{}
	observedReader := io.TeeReader(reader, capture)
	destination := &usageForwardWriter{Writer: writer}
	_, errCopy := io.Copy(destination, observedReader)
	complete := errCopy == nil
	if destination.err != nil {
		_, errDrain := io.Copy(io.Discard, observedReader)
		complete = errDrain == nil
	}
	if complete && !capture.overflow && len(capture.data) > 0 {
		for _, observe := range observers {
			observe(capture.data)
		}
	}
	return errCopy
}

type usageForwardWriter struct {
	io.Writer
	err error
}

func (w *usageForwardWriter) Write(p []byte) (int, error) {
	n, errWrite := w.Writer.Write(p)
	if errWrite == nil && n != len(p) {
		errWrite = io.ErrShortWrite
	}
	w.err = errWrite
	return n, errWrite
}
