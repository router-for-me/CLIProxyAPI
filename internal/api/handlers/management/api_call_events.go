package management

import (
	"context"
	"net/http"
	"sync"

	log "github.com/sirupsen/logrus"
)

// ManagementAPICallEvent is published after management /api-call returns a response.
type ManagementAPICallEvent struct {
	AuthIndex  string
	Method     string
	URL        string
	StatusCode int
	RespHeader http.Header
	RespBody   []byte
}

// ManagementAPICallListener consumes management API-call events.
type ManagementAPICallListener interface {
	OnManagementAPICall(ctx context.Context, evt ManagementAPICallEvent)
}

// ManagementAPICallListenerFunc adapts a function to ManagementAPICallListener.
type ManagementAPICallListenerFunc func(context.Context, ManagementAPICallEvent)

// OnManagementAPICall forwards to f.
func (f ManagementAPICallListenerFunc) OnManagementAPICall(ctx context.Context, evt ManagementAPICallEvent) {
	f(ctx, evt)
}

type managementAPICallEventBus struct {
	mu        sync.RWMutex
	listeners []ManagementAPICallListener
}

func newManagementAPICallEventBus() *managementAPICallEventBus {
	return &managementAPICallEventBus{}
}

func (b *managementAPICallEventBus) Register(listener ManagementAPICallListener) {
	if b == nil || listener == nil {
		return
	}

	b.mu.Lock()
	b.listeners = append(b.listeners, listener)
	b.mu.Unlock()
}

func (b *managementAPICallEventBus) Publish(ctx context.Context, evt ManagementAPICallEvent) {
	if b == nil {
		return
	}

	b.mu.RLock()
	listeners := append([]ManagementAPICallListener(nil), b.listeners...)
	b.mu.RUnlock()

	for _, listener := range listeners {
		if listener == nil {
			continue
		}

		eventCopy := cloneManagementAPICallEvent(evt)
		go func(listener ManagementAPICallListener, event ManagementAPICallEvent) {
			defer func() {
				if r := recover(); r != nil {
					log.Errorf("management api-call listener panic recovered: %v", r)
				}
			}()
			listener.OnManagementAPICall(ctx, event)
		}(listener, eventCopy)
	}
}

func cloneManagementAPICallEvent(evt ManagementAPICallEvent) ManagementAPICallEvent {
	copied := evt
	if evt.RespHeader != nil {
		copied.RespHeader = evt.RespHeader.Clone()
	}
	if evt.RespBody != nil {
		copied.RespBody = append([]byte(nil), evt.RespBody...)
	}
	return copied
}
