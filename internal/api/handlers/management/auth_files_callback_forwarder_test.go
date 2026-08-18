package management

import "testing"

func TestCallbackForwarderListenHostDefaultsToLoopback(t *testing.T) {
	t.Setenv("CLIPROXY_CALLBACK_FORWARDER_BIND_ALL", "")

	if got := callbackForwarderListenHostForRuntime(false); got != "127.0.0.1" {
		t.Fatalf("listen host = %q, want loopback", got)
	}
}

func TestCallbackForwarderListenHostAllowsDockerReachability(t *testing.T) {
	t.Setenv("CLIPROXY_CALLBACK_FORWARDER_BIND_ALL", "")

	if got := callbackForwarderListenHostForRuntime(true); got != "0.0.0.0" {
		t.Fatalf("listen host = %q, want all interfaces in container", got)
	}
}

func TestCallbackForwarderListenHostSupportsExplicitBindAll(t *testing.T) {
	t.Setenv("CLIPROXY_CALLBACK_FORWARDER_BIND_ALL", "true")

	if got := callbackForwarderListenHostForRuntime(false); got != "0.0.0.0" {
		t.Fatalf("listen host = %q, want explicit all-interface bind", got)
	}
}
