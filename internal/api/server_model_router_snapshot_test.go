package api

import (
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestCodexAlphaSearchModelRouterHostConcurrentPublication(t *testing.T) {
	server := &Server{handlers: handlers.NewBaseAPIHandlers(nil, nil)}
	host := &codexSearchModelRouter{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			server.handlers.SetModelRouterHost(host)
			server.handlers.SetModelRouterHost(nil)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			if got := server.codexAlphaSearchModelRouterHost(); got != nil && got != host {
				t.Errorf("model router host = %v, want nil or published host", got)
				return
			}
		}
	}()
	close(start)
	wg.Wait()
}

func TestCodexAlphaSearchModelRouterHostSnapshot(t *testing.T) {
	router := &codexSearchModelRouter{}
	pluginHost := &pluginhost.Host{}
	routerHandlers := handlers.NewBaseAPIHandlers(nil, nil)
	routerHandlers.SetModelRouterHost(router)
	pluginHandlers := handlers.NewBaseAPIHandlers(nil, nil)
	pluginHandlers.SetPluginHost(pluginHost)
	typedNilHandlers := handlers.NewBaseAPIHandlers(nil, nil)
	typedNilHandlers.SetModelRouterHost((*codexSearchModelRouter)(nil))

	tests := []struct {
		name   string
		server *Server
		want   handlers.PluginModelRouterHost
	}{
		{name: "nil server"},
		{name: "no handlers", server: &Server{}},
		{name: "no router", server: &Server{handlers: handlers.NewBaseAPIHandlers(nil, nil)}},
		{name: "explicit router", server: &Server{handlers: routerHandlers}, want: router},
		{name: "server plugin host takes precedence", server: &Server{handlers: routerHandlers, pluginHost: pluginHost}, want: pluginHost},
		{name: "handler plugin host is not a fallback", server: &Server{handlers: pluginHandlers}},
		{name: "typed nil router", server: &Server{handlers: typedNilHandlers}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.server.codexAlphaSearchModelRouterHost(); got != test.want {
				t.Errorf("model router host = %v, want %v", got, test.want)
			}
		})
	}
}
