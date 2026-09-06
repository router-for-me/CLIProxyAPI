package pluginhost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

type hostHTTPClient struct {
	host     *Host
	auth     *coreauth.Auth
	provider string
}

func (h *Host) newHTTPClient(auth *coreauth.Auth, providers ...string) pluginapi.HostHTTPClient {
	provider := ""
	if len(providers) > 0 {
		provider = providers[0]
	}
	return &hostHTTPClient{host: h, auth: auth, provider: provider}
}

func (c *hostHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, cfg, cleanup, errDo := c.doHTTP(ctx, req)
	if errDo != nil {
		return pluginapi.HTTPResponse{}, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("pluginhost: response body close error: %v", errClose)
		}
		if cleanup != nil {
			cleanup()
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	body, errReadAll := io.ReadAll(resp.Body)
	if len(body) > 0 {
		helps.AppendAPIResponseChunk(ctx, cfg, body)
	}
	if errReadAll != nil {
		helps.RecordAPIResponseError(ctx, cfg, errReadAll)
		return pluginapi.HTTPResponse{}, fmt.Errorf("read host http response: %w", errReadAll)
	}
	return pluginapi.HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeader(resp.Header),
		Body:       body,
	}, nil
}

func (c *hostHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, cfg, cleanup, errDo := c.doHTTP(ctx, req)
	if errDo != nil {
		return pluginapi.HTTPStreamResponse{}, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	chunks := make(chan pluginapi.HTTPStreamChunk)
	go func() {
		defer close(chunks)
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Warnf("pluginhost: stream response body close error: %v", errClose)
			}
			if cleanup != nil {
				cleanup()
			}
		}()
		buf := make([]byte, 32*1024)
		for {
			n, errRead := resp.Body.Read(buf)
			if n > 0 {
				payload := bytes.Clone(buf[:n])
				helps.AppendAPIResponseChunk(ctx, cfg, payload)
				select {
				case <-ctx.Done():
					return
				case chunks <- pluginapi.HTTPStreamChunk{Payload: payload}:
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, cfg, errRead)
					select {
					case <-ctx.Done():
					case chunks <- pluginapi.HTTPStreamChunk{Err: errRead}:
					}
				}
				return
			}
		}
	}()
	return pluginapi.HTTPStreamResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeader(resp.Header),
		Chunks:     chunks,
	}, nil
}

func (c *hostHTTPClient) doHTTP(ctx context.Context, req pluginapi.HTTPRequest) (*http.Response, *config.Config, func(), error) {
	if c == nil || c.host == nil {
		return nil, nil, nil, fmt.Errorf("host http client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := c.host.currentRuntimeConfig()
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	httpReq, errNewRequest := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(bytes.Clone(req.Body)))
	if errNewRequest != nil {
		return nil, cfg, nil, fmt.Errorf("create host http request: %w", errNewRequest)
	}
	httpReq.Header = cloneHeader(req.Headers)
	c.recordHTTPRequest(ctx, cfg, httpReq, req.Body)
	client, cleanup, errClient := c.newHTTPClientForRequest(ctx, cfg, req, httpReq)
	if errClient != nil {
		return nil, cfg, nil, errClient
	}
	if client == nil {
		client = &http.Client{}
	}
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		if cleanup != nil {
			cleanup()
		}
		helps.RecordAPIResponseError(ctx, cfg, errDo)
		return nil, cfg, nil, fmt.Errorf("execute host http request: %w", errDo)
	}
	return resp, cfg, cleanup, nil
}

func (c *hostHTTPClient) recordHTTPRequest(ctx context.Context, cfg *config.Config, req *http.Request, body []byte) {
	if req == nil {
		return
	}
	provider := c.provider
	var authID, authLabel, authType, authValue string
	if c.auth != nil {
		authID = c.auth.ID
		authLabel = c.auth.Label
		authType, authValue = c.auth.AccountInfo()
		if provider == "" {
			provider = c.auth.Provider
		}
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       req.URL.String(),
		Method:    req.Method,
		Headers:   req.Header.Clone(),
		Body:      bytes.Clone(body),
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func (h *Host) currentRuntimeConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runtimeConfig
}

func (c *hostHTTPClient) newHTTPClientForRequest(ctx context.Context, cfg *config.Config, req pluginapi.HTTPRequest, httpReq *http.Request) (*http.Client, func(), error) {
	profile := req.WireProfile
	if profile == nil || (!profile.HTTP1Only && !profile.DisableAutoCompression && len(profile.HeaderProfile) == 0) {
		client := helps.NewProxyAwareHTTPClient(ctx, cfg, c.auth, 0)
		if client == nil {
			client = &http.Client{}
		}
		return client, nil, nil
	}

	// Priority 1: Auth proxy
	var proxyStr string
	if c.auth != nil {
		proxyStr = strings.TrimSpace(c.auth.ProxyURL)
	}
	// Priority 2: Config proxy
	if proxyStr == "" && cfg != nil {
		proxyStr = strings.TrimSpace(cfg.ProxyURL)
	}

	var baseTransport *http.Transport
	var explicitDirect bool
	var isConfiguredSOCKS bool
	var builtByProxyutil bool
	if proxyStr != "" {
		setting, errParse := proxyutil.Parse(proxyStr)
		if errParse != nil {
			return nil, nil, fmt.Errorf("pluginhost: parse proxy %s: %w", proxyutil.Redact(proxyStr), errParse)
		}
		switch setting.Mode {
		case proxyutil.ModeDirect:
			explicitDirect = true
			baseTransport = proxyutil.NewDirectTransport()
			proxyStr = ""
		case proxyutil.ModeProxy:
			builtTransport, _, errBuild := proxyutil.BuildHTTPTransport(proxyStr)
			if errBuild != nil {
				return nil, nil, fmt.Errorf("pluginhost: build proxy transport for %s: %w", proxyutil.Redact(proxyStr), errBuild)
			}
			baseTransport = builtTransport
			builtByProxyutil = true
			if setting.URL != nil && (strings.EqualFold(setting.URL.Scheme, "socks5") || strings.EqualFold(setting.URL.Scheme, "socks5h")) {
				isConfiguredSOCKS = true
			}
		default: // ModeInherit
			proxyStr = ""
		}
	}

	var ctxTransport *http.Transport
	if ctx != nil {
		if ctxRoundTripper, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && ctxRoundTripper != nil {
			if t, ok := ctxRoundTripper.(*http.Transport); ok && t != nil {
				ctxTransport = t
			} else if baseTransport == nil {
				return nil, nil, fmt.Errorf("pluginhost: wire profile is not supported with custom context RoundTripper %T", ctxRoundTripper)
			}
		}
	}

	// Priority 3 & 4: Context RoundTripper or Default transport
	if baseTransport == nil {
		if ctxTransport != nil {
			baseTransport = ctxTransport.Clone()
		} else if !explicitDirect {
			if def, ok := http.DefaultTransport.(*http.Transport); ok && def != nil {
				baseTransport = def.Clone()
			} else {
				return nil, nil, fmt.Errorf("pluginhost: wire profile is not supported with custom default RoundTripper %T", http.DefaultTransport)
			}
		} else {
			baseTransport = proxyutil.NewDirectTransport()
		}
	} else if ctxTransport != nil && ctxTransport.TLSClientConfig != nil {
		baseTransport.TLSClientConfig = ctxTransport.TLSClientConfig.Clone()
	}

	if profile.DisableAutoCompression {
		baseTransport.DisableCompression = true
	}

	headerProfile := append([]string(nil), profile.HeaderProfile...)
	hasHeaderProfile := len(headerProfile) > 0
	forceHTTP1 := profile.HTTP1Only || hasHeaderProfile

	if forceHTTP1 {
		if baseTransport.Protocols != nil {
			protocols := &http.Protocols{}
			protocols.SetHTTP1(true)
			protocols.SetHTTP2(false)
			protocols.SetUnencryptedHTTP2(false)
			baseTransport.Protocols = protocols
		}
		baseTransport.ForceAttemptHTTP2 = false
		baseTransport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
		if baseTransport.TLSClientConfig == nil {
			baseTransport.TLSClientConfig = &tls.Config{}
		} else {
			baseTransport.TLSClientConfig = baseTransport.TLSClientConfig.Clone()
		}
		baseTransport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	}

	var callerTLSDialer func(context.Context, string, string) (net.Conn, error)
	if ctxTransport != nil {
		if ctxTransport.DialTLSContext != nil {
			callerTLSDialer = ctxTransport.DialTLSContext
		} else if ctxTransport.DialTLS != nil {
			callerTLSDialer = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
				return ctxTransport.DialTLS(network, addr)
			}
		}
	} else if baseTransport != nil && !builtByProxyutil {
		if baseTransport.DialTLSContext != nil {
			callerTLSDialer = baseTransport.DialTLSContext
		} else if baseTransport.DialTLS != nil {
			callerTLSDialer = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
				return baseTransport.DialTLS(network, addr)
			}
		}
	}

	if forceHTTP1 && !hasHeaderProfile {
		if callerTLSDialer != nil {
			baseTransport.DialTLSContext = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
				tlsConn, errTLS := callerTLSDialer(dialCtx, network, addr)
				if errTLS != nil {
					return nil, errTLS
				}
				if tc, ok := tlsConn.(*tls.Conn); ok && tc != nil {
					if errHandshake := tc.HandshakeContext(dialCtx); errHandshake != nil {
						_ = tlsConn.Close()
						return nil, fmt.Errorf("pluginhost: custom TLS handshake: %w", errHandshake)
					}
					cs := tc.ConnectionState()
					if cs.NegotiatedProtocol != "" && cs.NegotiatedProtocol != "http/1.1" {
						_ = tlsConn.Close()
						return nil, fmt.Errorf("pluginhost: custom TLS dialer negotiated unsupported protocol %q; wire profile requires HTTP/1.1", cs.NegotiatedProtocol)
					}
				}
				return tlsConn, nil
			}
		}
	}

	reqHolder := &requestHolder{req: httpReq}

	if hasHeaderProfile {
		headerOrderFunc := func(_, _ string) []string {
			return headerProfile
		}

		origDialContext := baseTransport.DialContext
		if origDialContext == nil && baseTransport.Dial != nil {
			origDialContext = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
				return baseTransport.Dial(network, addr)
			}
		}
		if origDialContext == nil {
			origDialContext = (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext
		}
		origProxyFunc := baseTransport.Proxy

		// For plain HTTP requests, wrapping DialContext intercepts the plaintext HTTP request bytes.
		// Handshake bytes (like SOCKS5 or direct writes) are bypassed cleanly by NewOrderedRequestConn.
		baseTransport.DialContext = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			conn, errDialContext := origDialContext(dialCtx, network, addr)
			if errDialContext != nil {
				return nil, errDialContext
			}
			return httpwire.NewOrderedRequestConn(conn, headerOrderFunc), nil
		}

		// Configure dynamic proxy function:
		// For plain HTTP, keep origProxyFunc so standard library forwards HTTP requests to the proxy.
		// For HTTPS, return nil so that DialTLSContext is always called to perform TLS wrapping.
		baseTransport.Proxy = func(r *http.Request) (*url.URL, error) {
			if r != nil && r.URL != nil && r.URL.Scheme == "http" {
				if origProxyFunc != nil {
					return origProxyFunc(r)
				}
				return nil, nil
			}
			return nil, nil
		}

		baseTransport.DialTLSContext = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			currentReq := reqHolder.get()
			targetProxy, errProxy := resolveProxyForRequest(currentReq, c.auth, cfg, origProxyFunc)
			if errProxy != nil {
				return nil, errProxy
			}

			isTargetHTTPS := currentReq != nil && currentReq.URL != nil && currentReq.URL.Scheme == "https"

			var rawConn net.Conn
			var errDial error
			if !isTargetHTTPS {
				// Target is plain HTTP; standard library called DialTLSContext to connect to an HTTPS proxy itself
				if callerTLSDialer != nil {
					tlsConn, errTLS := callerTLSDialer(dialCtx, network, addr)
					if errTLS != nil {
						return nil, errTLS
					}
					if tc, ok := tlsConn.(*tls.Conn); ok && tc != nil {
						if errHandshake := tc.HandshakeContext(dialCtx); errHandshake != nil {
							_ = tlsConn.Close()
							return nil, fmt.Errorf("pluginhost: custom TLS proxy handshake: %w", errHandshake)
						}
						cs := tc.ConnectionState()
						if cs.NegotiatedProtocol != "" && cs.NegotiatedProtocol != "http/1.1" {
							_ = tlsConn.Close()
							return nil, fmt.Errorf("pluginhost: custom TLS proxy dialer negotiated unsupported protocol %q; wire profile requires HTTP/1.1", cs.NegotiatedProtocol)
						}
					}
					return httpwire.NewOrderedRequestConn(tlsConn, headerOrderFunc), nil
				}
				rawConn, errDial = origDialContext(dialCtx, network, addr)
				if errDial != nil {
					return nil, errDial
				}
				pHost, _, errSplitHost := net.SplitHostPort(addr)
				if errSplitHost != nil {
					pHost = addr
				}
				tlsCfg := baseTransport.TLSClientConfig.Clone()
				if tlsCfg.ServerName == "" {
					tlsCfg.ServerName = pHost
				}
				tlsCfg.NextProtos = []string{"http/1.1"}
				tlsConn := tls.Client(rawConn, tlsCfg)
				if errHandshake := tlsConn.HandshakeContext(dialCtx); errHandshake != nil {
					_ = rawConn.Close()
					return nil, fmt.Errorf("HTTPS proxy TLS handshake: %w", errHandshake)
				}
				return httpwire.NewOrderedRequestConn(tlsConn, headerOrderFunc), nil
			} else if isConfiguredSOCKS {
				// Configured SOCKS5 proxy: origDialContext is already the SOCKS5 dialer from BuildHTTPTransport
				rawConn, errDial = origDialContext(dialCtx, network, addr)
			} else if targetProxy != nil {
				// Target is HTTPS; tunnel through the proxy (HTTP/HTTPS/SOCKS) to addr
				rawConn, errDial = dialProxyTunnel(dialCtx, origDialContext, callerTLSDialer, targetProxy, addr, baseTransport)
			} else {
				if callerTLSDialer != nil {
					tlsConn, errOrigTLS := callerTLSDialer(dialCtx, network, addr)
					if errOrigTLS != nil {
						return nil, errOrigTLS
					}
					if tc, ok := tlsConn.(*tls.Conn); ok && tc != nil {
						if errHandshake := tc.HandshakeContext(dialCtx); errHandshake != nil {
							_ = tlsConn.Close()
							return nil, fmt.Errorf("pluginhost: custom TLS handshake: %w", errHandshake)
						}
						cs := tc.ConnectionState()
						if cs.NegotiatedProtocol != "" && cs.NegotiatedProtocol != "http/1.1" {
							_ = tlsConn.Close()
							return nil, fmt.Errorf("pluginhost: custom TLS dialer negotiated unsupported protocol %q; wire profile requires HTTP/1.1", cs.NegotiatedProtocol)
						}
					}
					return httpwire.NewOrderedRequestConn(tlsConn, headerOrderFunc), nil
				}
				rawConn, errDial = origDialContext(dialCtx, network, addr)
			}
			if errDial != nil {
				return nil, errDial
			}

			host, _, errSplit := net.SplitHostPort(addr)
			if errSplit != nil {
				host = addr
			}
			tlsCfg := baseTransport.TLSClientConfig.Clone()
			if tlsCfg.ServerName == "" {
				tlsCfg.ServerName = host
			}
			tlsCfg.NextProtos = []string{"http/1.1"}
			tlsConn := tls.Client(rawConn, tlsCfg)
			if errHandshake := tlsConn.HandshakeContext(dialCtx); errHandshake != nil {
				if errClose := rawConn.Close(); errClose != nil {
					log.Debugf("pluginhost: close connection after TLS handshake failure: %v", errClose)
				}
				return nil, fmt.Errorf("pluginhost TLS handshake: %w", errHandshake)
			}
			return httpwire.NewOrderedRequestConn(tlsConn, headerOrderFunc), nil
		}
	}

	cleanup := func() {
		baseTransport.CloseIdleConnections()
	}

	baseTransport.DisableKeepAlives = true

	client := &http.Client{
		Transport: baseTransport,
		CheckRedirect: func(redirectReq *http.Request, via []*http.Request) error {
			reqHolder.set(redirectReq)
			baseTransport.CloseIdleConnections()
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}

	return client, cleanup, nil
}

type requestHolder struct {
	mu  sync.Mutex
	req *http.Request
}

func (h *requestHolder) get() *http.Request {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.req
}

func (h *requestHolder) set(req *http.Request) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.req = req
}

func resolveProxyForRequest(r *http.Request, auth *coreauth.Auth, cfg *config.Config, proxyFunc func(*http.Request) (*url.URL, error)) (*url.URL, error) {
	if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
		setting, errParse := proxyutil.Parse(strings.TrimSpace(auth.ProxyURL))
		if errParse != nil {
			return nil, fmt.Errorf("pluginhost: parse auth proxy: %w", errParse)
		}
		if setting.Mode == proxyutil.ModeDirect {
			return nil, nil
		}
		if setting.Mode == proxyutil.ModeProxy {
			return setting.URL, nil
		}
	}
	if cfg != nil && strings.TrimSpace(cfg.ProxyURL) != "" {
		setting, errParse := proxyutil.Parse(strings.TrimSpace(cfg.ProxyURL))
		if errParse != nil {
			return nil, fmt.Errorf("pluginhost: parse config proxy: %w", errParse)
		}
		if setting.Mode == proxyutil.ModeDirect {
			return nil, nil
		}
		if setting.Mode == proxyutil.ModeProxy {
			return setting.URL, nil
		}
	}
	if proxyFunc != nil && r != nil {
		return proxyFunc(r)
	}
	return nil, nil
}

func dialProxyTunnel(
	ctx context.Context,
	baseDial func(context.Context, string, string) (net.Conn, error),
	baseDialTLS func(context.Context, string, string) (net.Conn, error),
	proxyURL *url.URL,
	targetAddr string,
	transport *http.Transport,
) (net.Conn, error) {
	if proxyURL == nil {
		return baseDial(ctx, "tcp", targetAddr)
	}

	pHost := proxyURL.Hostname()
	pPort := proxyURL.Port()
	if pPort == "" {
		switch proxyURL.Scheme {
		case "https":
			pPort = "443"
		case "socks5", "socks5h":
			pPort = "1080"
		default:
			pPort = "80"
		}
	}
	proxyAddr := net.JoinHostPort(pHost, pPort)

	dialerToUse := baseDial
	if dialerToUse == nil {
		dialerToUse = (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext
	}

	if proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h" {
		var auth *proxy.Auth
		if proxyURL.User != nil {
			pwd, _ := proxyURL.User.Password()
			auth = &proxy.Auth{
				User:     proxyURL.User.Username(),
				Password: pwd,
			}
		}
		socksDialer, errSOCKS := proxy.SOCKS5("tcp", proxyAddr, auth, &contextDialerWrapper{dial: dialerToUse})
		if errSOCKS != nil {
			return nil, fmt.Errorf("create SOCKS5 dialer: %w", errSOCKS)
		}
		if cd, ok := socksDialer.(proxy.ContextDialer); ok {
			return cd.DialContext(ctx, "tcp", targetAddr)
		}
		return socksDialer.Dial("tcp", targetAddr)
	}

	var conn net.Conn
	if proxyURL.Scheme == "https" && baseDialTLS != nil {
		proxyTLS, errTLS := baseDialTLS(ctx, "tcp", proxyAddr)
		if errTLS != nil {
			return nil, fmt.Errorf("HTTPS proxy custom TLS dial failed: %w", errTLS)
		}
		if tc, ok := proxyTLS.(*tls.Conn); ok && tc != nil {
			if errHandshake := tc.HandshakeContext(ctx); errHandshake != nil {
				_ = proxyTLS.Close()
				return nil, fmt.Errorf("HTTPS proxy custom TLS handshake: %w", errHandshake)
			}
			cs := tc.ConnectionState()
			if cs.NegotiatedProtocol != "" && cs.NegotiatedProtocol != "http/1.1" {
				_ = proxyTLS.Close()
				return nil, fmt.Errorf("HTTPS proxy custom TLS dialer negotiated unsupported protocol %q; wire profile requires HTTP/1.1", cs.NegotiatedProtocol)
			}
		}
		conn = proxyTLS
	} else {
		rawConn, errDial := dialerToUse(ctx, "tcp", proxyAddr)
		if errDial != nil {
			return nil, fmt.Errorf("dial proxy %s: %w", proxyutil.Redact(proxyURL.String()), errDial)
		}
		conn = rawConn

		if proxyURL.Scheme == "https" {
			var tlsCfg *tls.Config
			if transport != nil && transport.TLSClientConfig != nil {
				tlsCfg = transport.TLSClientConfig.Clone()
			} else {
				tlsCfg = &tls.Config{}
			}
			if tlsCfg.ServerName == "" {
				tlsCfg.ServerName = proxyURL.Hostname()
			}
			tlsCfg.NextProtos = []string{"http/1.1"}
			proxyTLS := tls.Client(rawConn, tlsCfg)
			if errHandshake := proxyTLS.HandshakeContext(ctx); errHandshake != nil {
				_ = rawConn.Close()
				return nil, fmt.Errorf("HTTPS proxy TLS handshake: %w", errHandshake)
			}
			conn = proxyTLS
		}
	}

	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.Close()
		close(cancelDone)
	})
	defer func() {
		if !stopCancel() {
			<-cancelDone
		}
	}()

	connectReq := (&http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: targetAddr},
		Host:   targetAddr,
		Header: make(http.Header),
	}).WithContext(ctx)

	if transport != nil {
		if transport.GetProxyConnectHeader != nil {
			hdr, errHdr := transport.GetProxyConnectHeader(ctx, proxyURL, targetAddr)
			if errHdr != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("get proxy connect header: %w", errHdr)
			}
			for k, vv := range hdr {
				for _, v := range vv {
					connectReq.Header.Add(k, v)
				}
			}
		} else if transport.ProxyConnectHeader != nil {
			for k, vv := range transport.ProxyConnectHeader {
				for _, v := range vv {
					connectReq.Header.Add(k, v)
				}
			}
		}
	}

	if proxyURL.User != nil {
		pwd, _ := proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + pwd))
		connectReq.Header.Set("Proxy-Authorization", "Basic "+token)
	}

	if errWrite := connectReq.Write(conn); errWrite != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write CONNECT request: %w", errWrite)
	}

	br := bufio.NewReader(conn)
	resp, errResp := http.ReadResponse(br, connectReq)
	if errResp != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", errResp)
	}
	if transport != nil && transport.OnProxyConnectResponse != nil {
		if errOn := transport.OnProxyConnectResponse(ctx, proxyURL, connectReq, resp); errOn != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("on proxy connect response: %w", errOn)
		}
	}
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed with status %d %s", resp.StatusCode, resp.Status)
	}

	return conn, nil
}

type contextDialerWrapper struct {
	dial func(context.Context, string, string) (net.Conn, error)
}

func (w *contextDialerWrapper) Dial(network, addr string) (net.Conn, error) {
	return w.dial(context.Background(), network, addr)
}

func (w *contextDialerWrapper) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return w.dial(ctx, network, addr)
}
