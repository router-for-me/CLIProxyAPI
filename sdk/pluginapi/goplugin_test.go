package pluginapi

import (
	"context"
	"testing"
)

// fakeGoPlugin is a minimal GoPlugin used to prove the contract types exist
// and carry the expected method set. It deliberately has no behavior.
type fakeGoPlugin struct{}

func (fakeGoPlugin) Call(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}

func (fakeGoPlugin) Shutdown() {}

func TestGoPluginContract(t *testing.T) {
	var _ GoPlugin = fakeGoPlugin{}

	// GoPluginCaller shares the exact same method shape as GoPlugin.Call;
	// a GoPlugin therefore satisfies GoPluginCaller too.
	var _ GoPluginCaller = fakeGoPlugin{}
}

func TestGoPluginFactoryType(t *testing.T) {
	// A factory is a plain function value so host code can assert against
	// the unnamed function type rather than this named alias. This test pins
	// the signature so the two cannot drift apart.
	var f GoPluginFactory = func(host GoPluginCaller) (GoPlugin, error) {
		return fakeGoPlugin{}, nil
	}
	if f == nil {
		t.Fatal("factory must be assignable")
	}
}
