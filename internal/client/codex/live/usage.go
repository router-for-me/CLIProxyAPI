package live

import (
	"context"
	"errors"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

// Observe only upstream data events. Client frames and repeated response.done
// notifications must never manufacture or double-count provider usage.
type liveUsageObserver struct {
	ctx     context.Context
	auth    *auth.Auth
	model   string
	seen    map[string]bool
	pending map[string]*helps.UsageReporter
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
	kind := root.Get("type").String()
	id := root.Get("response.id").String()
	if id == "" {
		id = root.Get("item_id").String()
	}
	if kind == "response.created" && id != "" && !o.seen[id] {
		r := helps.NewUsageReporter(usage.WithNewGeneration(o.ctx), "codex", o.model, o.auth)
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
	r := o.pending[id]
	if r == nil {
		r = helps.NewUsageReporter(usage.WithNewGeneration(o.ctx), "codex", model, o.auth)
		r.SetStream(true)
		r.SetTransport("websocket")
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
