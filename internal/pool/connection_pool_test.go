package pool

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestNewConnectionPool(t *testing.T) {
	pool := NewConnectionPool()
	if pool == nil {
		t.Fatal("NewConnectionPool returned nil")
	}
	defer pool.Close()

	if pool.clients == nil {
		t.Error("clients map should be initialized")
	}
}

func TestGetClientReturnsHTTPClient(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	client := pool.GetClient("claude")
	if client == nil {
		t.Fatal("GetClient returned nil")
	}
}

func TestGetClientReturnsSameClientForSameProvider(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	client1 := pool.GetClient("claude")
	client2 := pool.GetClient("claude")

	if client1 != client2 {
		t.Error("GetClient should return the same client for the same provider")
	}
}

func TestGetClientReturnsDifferentClientsForDifferentProviders(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	claudeClient := pool.GetClient("claude")
	openaiClient := pool.GetClient("openai")

	if claudeClient == openaiClient {
		t.Error("GetClient should return different clients for different providers")
	}
}

func TestPoolSettingsMaxIdleConns(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	client := pool.GetClient("test")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("client transport is not *http.Transport")
	}

	if transport.MaxIdleConns != DefaultMaxIdleConns {
		t.Errorf("expected MaxIdleConns=%d, got %d", DefaultMaxIdleConns, transport.MaxIdleConns)
	}
}

func TestPoolSettingsMaxIdleConnsPerHost(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	client := pool.GetClient("test")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("client transport is not *http.Transport")
	}

	if transport.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
		t.Errorf("expected MaxIdleConnsPerHost=%d, got %d", DefaultMaxIdleConnsPerHost, transport.MaxIdleConnsPerHost)
	}
}

func TestPoolSettingsIdleConnTimeout(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	client := pool.GetClient("test")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("client transport is not *http.Transport")
	}

	if transport.IdleConnTimeout != DefaultIdleConnTimeout {
		t.Errorf("expected IdleConnTimeout=%v, got %v", DefaultIdleConnTimeout, transport.IdleConnTimeout)
	}
}

func TestPoolKeepAliveEnabled(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	client := pool.GetClient("test")
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("client transport is not *http.Transport")
	}

	if transport.DisableKeepAlives {
		t.Error("keep-alive should be enabled (DisableKeepAlives=false)")
	}
}

func TestPoolConcurrentAccess(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	var wg sync.WaitGroup
	clients := make([]*http.Client, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			clients[idx] = pool.GetClient("concurrent-test")
		}(i)
	}
	wg.Wait()

	first := clients[0]
	for i, client := range clients {
		if client != first {
			t.Errorf("client at index %d differs from first client", i)
		}
	}
}

func TestPoolHealthCheckInterval(t *testing.T) {
	if DefaultHealthCheckInterval != 30*time.Second {
		t.Errorf("expected DefaultHealthCheckInterval=30s, got %v", DefaultHealthCheckInterval)
	}
}

func TestPoolCloseStopsHealthChecker(t *testing.T) {
	pool := NewConnectionPool()
	err := pool.Close()
	if err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestPoolCloseIdempotent(t *testing.T) {
	pool := NewConnectionPool()
	pool.Close()
	err := pool.Close()
	if err != nil {
		t.Errorf("second Close should not error: %v", err)
	}
}

func TestPoolStats(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	pool.GetClient("claude")
	pool.GetClient("openai")
	pool.GetClient("gemini")

	stats := pool.Stats()
	if stats.ProviderCount != 3 {
		t.Errorf("expected 3 providers, got %d", stats.ProviderCount)
	}
}

func TestPoolRemoveClient(t *testing.T) {
	pool := NewConnectionPool()
	defer pool.Close()

	client1 := pool.GetClient("test")
	pool.RemoveClient("test")
	client2 := pool.GetClient("test")

	if client1 == client2 {
		t.Error("after RemoveClient, GetClient should return a new client")
	}
}

func TestDefaultPoolConstants(t *testing.T) {
	tests := []struct {
		name     string
		expected interface{}
		actual   interface{}
	}{
		{"MaxIdleConns", 100, DefaultMaxIdleConns},
		{"MaxIdleConnsPerHost", 10, DefaultMaxIdleConnsPerHost},
		{"IdleConnTimeout", 90 * time.Second, DefaultIdleConnTimeout},
		{"HealthCheckInterval", 30 * time.Second, DefaultHealthCheckInterval},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.actual != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, tt.actual)
			}
		})
	}
}
