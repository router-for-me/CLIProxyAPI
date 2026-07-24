package pluginhost

import (
	"context"
	"fmt"
	"sync"
)

type guardedPluginClient struct {
	mu              sync.Mutex
	inner           pluginClient
	calls           int
	pins            int
	closed          bool
	shutdownStarted bool
	shutdownDone    chan struct{}
}

type guardedPluginClientLease struct {
	mu       sync.Mutex
	parent   *guardedPluginClient
	released bool
}

func newGuardedPluginClient(inner pluginClient) *guardedPluginClient {
	return &guardedPluginClient{inner: inner, shutdownDone: make(chan struct{})}
}

func (c *guardedPluginClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	inner, errAcquire := c.acquire()
	if errAcquire != nil {
		return nil, errAcquire
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan guardedPluginCallResult, 1)
	go func() {
		defer c.release()
		defer func() {
			if recovered := recover(); recovered != nil {
				result <- guardedPluginCallResult{recovered: recovered}
			}
		}()
		response, errCall := inner.Call(ctx, method, request)
		result <- guardedPluginCallResult{response: response, err: errCall}
	}()
	select {
	case callResult := <-result:
		if callResult.recovered != nil {
			panic(callResult.recovered)
		}
		return callResult.response, callResult.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type guardedPluginCallResult struct {
	response  []byte
	err       error
	recovered any
}

func (c *guardedPluginClient) Pin() (pluginClient, error) {
	if c == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.inner == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	c.pins++
	return &guardedPluginClientLease{parent: c}, nil
}

func (c *guardedPluginClient) acquire() (pluginClient, error) {
	if c == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.inner == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	c.calls++
	return c.inner, nil
}

func (c *guardedPluginClient) release() {
	c.mu.Lock()
	c.calls--
	inner := c.takeInnerForShutdownLocked()
	c.mu.Unlock()
	c.finishShutdown(inner)
}

func (c *guardedPluginClient) Shutdown() {
	c.ShutdownContext(context.Background())
}

// ShutdownContext detaches the client immediately and waits for active calls only
// until ctx is canceled. Detached cleanup continues asynchronously when needed.
func (c *guardedPluginClient) ShutdownContext(ctx context.Context) {
	if c == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	c.closed = true
	shutdownDeferred := c.pins > 0
	inner := c.takeInnerForShutdownLocked()
	done := c.shutdownDone
	c.mu.Unlock()
	c.finishShutdown(inner)
	if shutdownDeferred {
		return
	}

	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (c *guardedPluginClient) finishShutdown(inner pluginClient) {
	if inner == nil {
		return
	}
	go func() {
		c.finishShutdownSync(inner)
	}()
}

func (c *guardedPluginClient) finishShutdownSync(inner pluginClient) {
	if inner == nil {
		return
	}
	inner.Shutdown()
	close(c.shutdownDone)
}

func (c *guardedPluginClient) takeInnerForShutdownLocked() pluginClient {
	if c == nil || !c.closed || c.pins > 0 || c.calls > 0 || c.shutdownStarted || c.inner == nil {
		return nil
	}
	c.shutdownStarted = true
	inner := c.inner
	c.inner = nil
	return inner
}

func (c *guardedPluginClient) acquirePinned() (pluginClient, error) {
	if c == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inner == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	c.calls++
	return c.inner, nil
}

func (c *guardedPluginClient) releasePin() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.pins > 0 {
		c.pins--
	}
	if c.pins > 0 || !c.closed {
		c.mu.Unlock()
		return
	}
	inner := c.takeInnerForShutdownLocked()
	c.mu.Unlock()
	c.finishShutdownSync(inner)
}

func (l *guardedPluginClientLease) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if l == nil {
		return nil, fmt.Errorf("plugin client lease is closed")
	}
	l.mu.Lock()
	if l.released || l.parent == nil {
		l.mu.Unlock()
		return nil, fmt.Errorf("plugin client lease is closed")
	}
	parent := l.parent
	inner, errAcquire := parent.acquirePinned()
	l.mu.Unlock()
	if errAcquire != nil {
		return nil, errAcquire
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan guardedPluginCallResult, 1)
	go func() {
		defer parent.release()
		defer func() {
			if recovered := recover(); recovered != nil {
				result <- guardedPluginCallResult{recovered: recovered}
			}
		}()
		response, errCall := inner.Call(ctx, method, request)
		result <- guardedPluginCallResult{response: response, err: errCall}
	}()
	select {
	case callResult := <-result:
		if callResult.recovered != nil {
			panic(callResult.recovered)
		}
		return callResult.response, callResult.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *guardedPluginClientLease) Shutdown() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return
	}
	l.released = true
	parent := l.parent
	l.parent = nil
	l.mu.Unlock()
	if parent != nil {
		parent.releasePin()
	}
}
