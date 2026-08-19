package client

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/transport"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

// requestClients bundles the four transport variants used per proxy/account:
// regular and stream (Chrome-impersonating uTLS), plus their std-lib fallbacks.
type requestClients struct {
	regular   transport.Doer
	stream    transport.Doer
	fallback  transport.Doer
	fallbackS transport.Doer
}

// Client drives the DeepSeek web chat API. It is decoupled from ds2api's
// auth/store/pool: methods take (ctx, token, locale, proxyURL) and the
// conductor (CLIP executor layer) handles retries, token refresh and
// account switching.
type Client struct {
	regular   transport.Doer
	stream    transport.Doer
	fallback  transport.Doer
	fallbackS transport.Doer
	powCache  *powChallengeCache
	cookies   *transport.CookieJar

	proxyClientsMu sync.RWMutex
	proxyClients   map[string]requestClients
}

// NewClient creates a DeepSeek client with default Chrome-impersonating
// transports and an in-memory cookie jar.
func NewClient() *Client {
	return &Client{
		regular:      transport.New(60 * time.Second),
		stream:       transport.New(0),
		fallback:     transport.NewFallbackClient(60*time.Second, nil),
		fallbackS:    transport.NewFallbackClient(0, nil),
		powCache:     newPowChallengeCache(),
		cookies:      transport.NewCookieJar(),
		proxyClients: map[string]requestClients{},
	}
}

func (c *Client) defaultRequestClients() requestClients {
	return c.decorate(requestClients{
		regular:   c.regular,
		stream:    c.stream,
		fallback:  c.fallback,
		fallbackS: c.fallbackS,
	})
}

// decorate layers per-account cookie replay and response decompression over a
// bundle. Both must apply on every path, including std-transport fallbacks.
func (c *Client) decorate(bundle requestClients) requestClients {
	return requestClients{
		regular:   transport.NewWireDoer(bundle.regular, c.cookies),
		stream:    transport.NewWireDoer(bundle.stream, c.cookies),
		fallback:  transport.NewWireDoer(bundle.fallback, c.cookies),
		fallbackS: transport.NewWireDoer(bundle.fallbackS, c.cookies),
	}
}

// requestClientsForProxy returns the transport bundle for the given proxy URL.
// Empty proxyURL uses the default (no-proxy) bundle; otherwise a per-proxy
// bundle is built and cached.
func (c *Client) requestClientsForProxy(proxyURL string) requestClients {
	key := strings.TrimSpace(proxyURL)
	if key == "" {
		return c.defaultRequestClients()
	}
	c.proxyClientsMu.RLock()
	cached, ok := c.proxyClients[key]
	c.proxyClientsMu.RUnlock()
	if ok {
		return cached
	}
	dialContext, err := proxyDialContext(key)
	if err != nil || dialContext == nil {
		return c.defaultRequestClients()
	}
	bundle := c.decorate(requestClients{
		regular:   transport.NewWithDialContext(60*time.Second, dialContext),
		stream:    transport.NewWithDialContext(0, dialContext),
		fallback:  transport.NewFallbackClient(60*time.Second, dialContext),
		fallbackS: transport.NewFallbackClient(0, dialContext),
	})
	c.proxyClientsMu.Lock()
	c.proxyClients[key] = bundle
	c.proxyClientsMu.Unlock()
	return bundle
}

// proxyDialContext builds a transport.DialContextFunc from a raw proxy URL
// string using CLIP's proxyutil.BuildDialer. Returns nil for direct/inherit
// modes (caller falls back to the default net dialer).
func proxyDialContext(rawProxyURL string) (transport.DialContextFunc, error) {
	dialer, mode, err := proxyutil.BuildDialer(rawProxyURL)
	if err != nil {
		return nil, err
	}
	if mode == proxyutil.ModeDirect || mode == proxyutil.ModeInherit {
		return nil, nil
	}
	if ctxDialer, ok := dialer.(proxy.ContextDialer); ok {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ctxDialer.DialContext(ctx, network, addr)
		}, nil
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.Dial(network, addr)
	}, nil
}
