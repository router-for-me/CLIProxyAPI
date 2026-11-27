package pluginhost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// heldPickClient blocks in scheduler.pick like a scheduler that holds it.
type heldPickClient struct {
	request chan []byte
	release chan struct{}
}

func (c *heldPickClient) Call(_ context.Context, _ string, request []byte) ([]byte, error) {
	c.request <- request
	<-c.release
	return []byte(`{"ok":true,"result":{"handled":false}}`), nil
}

func (c *heldPickClient) Shutdown() {}

// cancelingPickClient selects an auth after its request was cancelled.
type cancelingPickClient struct{ cancel context.CancelFunc }

func (c cancelingPickClient) Call(context.Context, string, []byte) ([]byte, error) {
	c.cancel()
	return []byte(`{"ok":true,"result":{"handled":true,"auth_id":"a"}}`), nil
}

func (cancelingPickClient) Shutdown() {}

// host.context.done reports a cancelled caller, releases its waiter when the
// call returns first, and refuses another plugin's callback ID.
func TestHostContextDone(t *testing.T) {
	host := New()
	caller := withHostCallbackPluginID(context.Background(), "plugin-a")
	wait := func(callerCtx context.Context, id string) (rpcHostContextDoneResponse, error) {
		raw, _ := json.Marshal(rpcHostContextDoneRequest{HostCallbackID: id})
		out, err := host.callFromPlugin(callerCtx, pluginabi.MethodHostContextDone, raw)
		if err != nil {
			return rpcHostContextDoneResponse{}, err
		}
		var env struct {
			Result rpcHostContextDoneResponse `json:"result"`
		}
		if errUnmarshal := json.Unmarshal(out, &env); errUnmarshal != nil {
			t.Fatalf("decode %s: %v", out, errUnmarshal)
		}
		return env.Result, nil
	}
	start := func(id string) <-chan rpcHostContextDoneResponse {
		got := make(chan rpcHostContextDoneResponse, 1)
		go func() {
			resp, err := wait(caller, id)
			if err != nil {
				t.Errorf("host.context.done: %v", err)
			}
			got <- resp
		}()
		return got
	}

	ctx, cancel := context.WithCancel(context.Background())
	id, closeCallback := host.openCallbackContextForPlugin(ctx, "plugin-a")
	if _, err := wait(withHostCallbackPluginID(context.Background(), "plugin-b"), id); err == nil {
		t.Fatal("plugin-b waited on plugin-a's callback context")
	}
	got := start(id)
	cancel()
	if resp := <-got; !resp.Done || resp.Error != context.Canceled.Error() {
		t.Fatalf("cancelled caller = %+v, want done", resp)
	}
	closeCallback()

	id, closeCallback = host.openCallbackContextForPlugin(context.Background(), "plugin-a")
	got = start(id)
	closeCallback()
	if resp := <-got; resp.Done {
		t.Fatalf("returned call = %+v, want not done", resp)
	}
	if resp, err := wait(caller, id); err != nil || resp.Done {
		t.Fatalf("closed callback = %+v, %v; want not done", resp, err)
	}

	// Through a scheduler pick: the guarded client returns when the request
	// ends while the plugin still holds, so the ID must outlive the Go call.
	inner := &heldPickClient{request: make(chan []byte, 1), release: make(chan struct{})}
	adapter := &rpcPluginAdapter{id: "plugin-a", host: host, client: newGuardedPluginClient(inner)}
	ctx, cancel = context.WithCancel(context.Background())
	picked := make(chan error, 1)
	go func() {
		// Metadata can hold Go callbacks; they must not reach the plugin.
		_, err := adapter.Pick(ctx, pluginapi.SchedulerPickRequest{Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"selected": func(string) {}}}})
		picked <- err
	}()
	var pick struct {
		HostCallbackID string `json:"host_callback_id"`
	}
	if errDecode := json.Unmarshal(<-inner.request, &pick); errDecode != nil || pick.HostCallbackID == "" {
		t.Fatalf("pick request has no host_callback_id: %v", errDecode)
	}
	cancel()
	if err := <-picked; err == nil {
		t.Fatal("cancelled pick returned no error")
	}
	if resp, err := wait(caller, pick.HostCallbackID); err != nil || !resp.Done {
		t.Fatalf("watcher after the caller returned = %+v, %v; want done", resp, err)
	}
	close(inner.release)

	ctx, cancel = context.WithCancel(context.Background())
	adapter = &rpcPluginAdapter{id: "plugin-a", host: host, client: cancelingPickClient{cancel}}
	if resp, err := adapter.Pick(ctx, pluginapi.SchedulerPickRequest{}); err == nil || resp.AuthID != "" {
		t.Fatalf("pick of a cancelled request = %+v, %v; want the context error", resp, err)
	}
}
