package pool

import (
	"net"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	DefaultMaxIdleConns        = 100
	DefaultMaxIdleConnsPerHost = 10
	DefaultIdleConnTimeout     = 90 * time.Second
	DefaultHealthCheckInterval = 30 * time.Second
)

type PoolStats struct {
	ProviderCount int
	Providers     []string
}

type ConnectionPool struct {
	clients  map[string]*http.Client
	mu       sync.RWMutex
	stopCh   chan struct{}
	stopped  bool
	stoppedMu sync.Mutex
}

func NewConnectionPool() *ConnectionPool {
	pool := &ConnectionPool{
		clients: make(map[string]*http.Client),
		stopCh:  make(chan struct{}),
	}
	go pool.healthChecker()
	return pool
}

func (p *ConnectionPool) GetClient(providerID string) *http.Client {
	p.mu.RLock()
	client, exists := p.clients[providerID]
	p.mu.RUnlock()

	if exists {
		return client
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if client, exists = p.clients[providerID]; exists {
		return client
	}

	client = p.createClient()
	p.clients[providerID] = client
	log.Debugf("pool: created HTTP client for provider %s", providerID)
	return client
}

func (p *ConnectionPool) createClient() *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        DefaultMaxIdleConns,
		MaxIdleConnsPerHost: DefaultMaxIdleConnsPerHost,
		IdleConnTimeout:     DefaultIdleConnTimeout,
		DisableKeepAlives:   false,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   0,
	}
}

func (p *ConnectionPool) RemoveClient(providerID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if client, exists := p.clients[providerID]; exists {
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
		delete(p.clients, providerID)
		log.Debugf("pool: removed HTTP client for provider %s", providerID)
	}
}

func (p *ConnectionPool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	providers := make([]string, 0, len(p.clients))
	for id := range p.clients {
		providers = append(providers, id)
	}

	return PoolStats{
		ProviderCount: len(p.clients),
		Providers:     providers,
	}
}

func (p *ConnectionPool) healthChecker() {
	ticker := time.NewTicker(DefaultHealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.cleanupIdleConnections()
		case <-p.stopCh:
			return
		}
	}
}

func (p *ConnectionPool) cleanupIdleConnections() {
	p.mu.RLock()
	clients := make([]*http.Client, 0, len(p.clients))
	for _, client := range p.clients {
		clients = append(clients, client)
	}
	p.mu.RUnlock()

	for _, client := range clients {
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
	log.Debugf("pool: health check completed, cleaned up idle connections for %d clients", len(clients))
}

func (p *ConnectionPool) Close() error {
	p.stoppedMu.Lock()
	if p.stopped {
		p.stoppedMu.Unlock()
		return nil
	}
	p.stopped = true
	close(p.stopCh)
	p.stoppedMu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	for providerID, client := range p.clients {
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
		delete(p.clients, providerID)
	}

	log.Debug("pool: connection pool closed")
	return nil
}
