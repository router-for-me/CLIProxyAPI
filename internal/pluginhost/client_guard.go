package pluginhost

import (
	"context"
	"fmt"
	"sync"
)

type guardedPluginClient struct {
	mu     sync.Mutex
	cond   *sync.Cond
	inner  pluginClient
	calls  int
	closed bool
}

func newGuardedPluginClient(inner pluginClient) pluginClient {
	client := &guardedPluginClient{inner: inner}
	client.cond = sync.NewCond(&client.mu)
	return client
}

func (c *guardedPluginClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	inner, errAcquire := c.acquire()
	if errAcquire != nil {
		return nil, errAcquire
	}
	defer c.release()
	return inner.Call(ctx, method, request)
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
	if c == nil {
		return
	}

	var inner pluginClient
	c.mu.Lock()
	if c.closed {
		for c.calls > 0 {
			c.cond.Wait()
		}
		c.mu.Unlock()
		return
	}
	c.closed = true
	for c.calls > 0 {
		c.cond.Wait()
	}
	inner = c.inner
	c.inner = nil
	c.mu.Unlock()

	if inner != nil {
		inner.Shutdown()
	}
}
