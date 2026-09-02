package pluginhost

import (
	"context"
	"fmt"
	"sync"
)

type guardedPluginClient struct {
	mu           sync.Mutex
	cond         *sync.Cond
	inner        pluginClient
	calls        int
	closed       bool
	shutdownDone chan struct{}
}

func newGuardedPluginClient(inner pluginClient) *guardedPluginClient {
	client := &guardedPluginClient{inner: inner, shutdownDone: make(chan struct{})}
	client.cond = sync.NewCond(&client.mu)
	return client
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
	if c.calls == 0 {
		c.cond.Broadcast()
	}
	c.mu.Unlock()
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
	if c.closed {
		done := c.shutdownDone
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		return
	}
	c.closed = true
	inner := c.inner
	c.inner = nil
	done := c.shutdownDone
	c.mu.Unlock()

	go func() {
		c.mu.Lock()
		for c.calls > 0 {
			c.cond.Wait()
		}
		c.mu.Unlock()
		if inner != nil {
			inner.Shutdown()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
}
