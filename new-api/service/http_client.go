package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"golang.org/x/net/proxy"
)

var (
	httpClient              *http.Client
	ssrfProtectedHTTPClient *http.Client
	proxyClients            = proxyHTTPClientCache{
		clients: make(map[string]*http.Client),
		aliases: make(map[string]string),
	}
	legacyProxyURLWarnings sync.Map
)

type proxyHTTPClientCache struct {
	mutex   sync.RWMutex
	clients map[string]*http.Client
	aliases map[string]string // rawProxyURL -> canonicalProxyURL
}

type proxyURLConfig struct {
	parsedURL *url.URL
	cacheKey  string
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	urlStr := req.URL.String()
	if err := validateURLWithCurrentFetchSetting(urlStr, true); err != nil {
		return fmt.Errorf("redirect to %s blocked: %v", urlStr, err)
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

func checkProtectedFetchRedirect(req *http.Request, via []*http.Request) error {
	urlStr := req.URL.String()
	if err := ValidateSSRFProtectedFetchURL(urlStr); err != nil {
		return fmt.Errorf("redirect to %s blocked: %v", urlStr, err)
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

func validateURLWithCurrentFetchSetting(urlStr string, applyDomainIPFilter bool) error {
	fetchSetting := system_setting.GetFetchSetting()
	return common.ValidateURLWithFetchSetting(urlStr, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, applyDomainIPFilter && fetchSetting.ApplyIPFilterForDomain)
}

func ValidateSSRFProtectedFetchURL(urlStr string) error {
	return validateURLWithCurrentFetchSetting(urlStr, true)
}

// maxTimeoutSeconds is the largest number of seconds that still converts to a
// time.Duration without overflowing (~292 years).
const maxTimeoutSeconds = int(math.MaxInt64 / int64(time.Second))

func newRelayHTTPTransport() *http.Transport {
	var transport *http.Transport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok && defaultTransport != nil {
		transport = defaultTransport.Clone()
	} else {
		dialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	transport.MaxIdleConns = common.RelayMaxIdleConns
	transport.MaxIdleConnsPerHost = common.RelayMaxIdleConnsPerHost
	transport.IdleConnTimeout = time.Duration(common.RelayIdleConnTimeout) * time.Second
	// Bound the wait for upstream response headers. Without it, an upstream that
	// accepts the connection but never responds (and never sends FIN/RST) parks the
	// goroutine forever, and every buffer that request owns -- the raw body read by
	// io.ReadAll, the decoded messages, and the re-marshalled upstream body -- stays
	// reachable for the lifetime of the process.
	//
	// This only covers the wait for the headers; streaming after the headers arrive
	// is not affected. Set RELAY_RESPONSE_HEADER_TIMEOUT=0 to restore the old
	// unbounded behaviour.
	if seconds := common.RelayResponseHeaderTimeout; seconds > 0 {
		// Clamp before converting: seconds beyond maxTimeoutSeconds overflow
		// time.Duration and can wrap into a tiny positive timeout, which would cut
		// every relay request instead of only the stuck ones.
		if seconds > maxTimeoutSeconds {
			seconds = maxTimeoutSeconds
		}
		transport.ResponseHeaderTimeout = time.Duration(seconds) * time.Second
	}
	transport.ForceAttemptHTTP2 = true
	if common.TLSInsecureSkipVerify {
		transport.TLSClientConfig = common.InsecureTLSConfig
	}
	return transport
}

func newRelayHTTPClient(transport http.RoundTripper) *http.Client {
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: checkRedirect,
	}
	if common.RelayTimeout != 0 {
		client.Timeout = time.Duration(common.RelayTimeout) * time.Second
	}
	return client
}

func clientCacheKey(proxyCacheKey string, policy HTTPTransportPolicy) string {
	return proxyCacheKey + "\x00" + policy.cacheKeyPart()
}

func InitHttpClient() {
	policy := defaultHTTPTransportPolicy()
	httpClient = newDirectHTTPClient(policy, nil)
	proxyClients.store(clientCacheKey("", policy), httpClient)
	ssrfProtectedHTTPClient = newProtectedFetchHTTPClient()
}

// GetHttpClient returns the general outbound client used by relay/provider
// integrations. Do not attach the SSRF-protected dialer here: provider base URLs
// are root/operator-managed deployment targets, not arbitrary user-controlled
// input, and may legitimately point at private networks, private-link endpoints,
// self-hosted services, or local proxies. Code paths that fetch arbitrary
// user-controlled URLs must use GetSSRFProtectedHTTPClient or
// ValidateSSRFProtectedFetchURL instead.
func GetHttpClient() *http.Client {
	return httpClient
}

// GetSSRFProtectedHTTPClient 返回带拨号时 SSRF 校验的客户端。
// ssrfProtectedHTTPClient 由 InitHttpClient 在启动时初始化，运行期只读。
func GetSSRFProtectedHTTPClient() *http.Client {
	if fetchSetting := system_setting.GetFetchSetting(); fetchSetting != nil && !fetchSetting.EnableSSRFProtection {
		return GetHttpClient()
	}
	return ssrfProtectedHTTPClient
}

func newProxyURLConfig(parsedURL *url.URL) *proxyURLConfig {
	return &proxyURLConfig{
		parsedURL: parsedURL,
		cacheKey:  parsedURL.String(),
	}
}

func warnLegacyProxyURLOnce(config *proxyURLConfig) {
	if _, loaded := legacyProxyURLWarnings.LoadOrStore(config.cacheKey, struct{}{}); loaded {
		return
	}
	logger.LogWarn(
		context.Background(),
		fmt.Sprintf(
			"legacy proxy URL suffix ignored at runtime: scheme=%s host=%s; update the channel proxy setting",
			config.parsedURL.Scheme,
			config.parsedURL.Host,
		),
	)
}

// NormalizeProxyURL validates a proxy URL using runtime-compatible rules and returns its canonical cache key.
func NormalizeProxyURL(rawProxyURL string) (string, error) {
	parsedURL, legacySuffixStripped, err := common.ParseProxyURLRuntime(rawProxyURL)
	if err != nil {
		return "", err
	}
	if parsedURL == nil {
		return "", nil
	}
	config := newProxyURLConfig(parsedURL)
	if legacySuffixStripped {
		warnLegacyProxyURLOnce(config)
	}
	return config.cacheKey, nil
}

// ValidateProxyURL validates a channel proxy URL without connecting to it.
func ValidateProxyURL(rawProxyURL string) error {
	_, err := common.ParseProxyURLStrict(rawProxyURL)
	return err
}

func (cache *proxyHTTPClientCache) store(fullKey string, client *http.Client) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.clients[fullKey] = client
}

func (cache *proxyHTTPClientCache) resolveProxyKey(rawProxyURL string) string {
	if canonicalKey, ok := cache.aliases[rawProxyURL]; ok {
		return canonicalKey
	}
	return rawProxyURL
}

func (cache *proxyHTTPClientCache) get(rawProxyURL string, policy HTTPTransportPolicy) (*http.Client, bool) {
	cache.mutex.RLock()
	defer cache.mutex.RUnlock()
	proxyKey := cache.resolveProxyKey(rawProxyURL)
	client, ok := cache.clients[clientCacheKey(proxyKey, policy)]
	return client, ok
}

func (cache *proxyHTTPClientCache) getOrCreate(
	rawProxyURL string,
	config *proxyURLConfig,
	policy HTTPTransportPolicy,
	factory func() (*http.Client, error),
) (*http.Client, error) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	proxyKey := ""
	if config != nil {
		proxyKey = config.cacheKey
		cache.aliases[rawProxyURL] = proxyKey
	} else if rawProxyURL != "" {
		proxyKey = cache.resolveProxyKey(rawProxyURL)
	}
	fullKey := clientCacheKey(proxyKey, policy)
	if client, ok := cache.clients[fullKey]; ok {
		return client, nil
	}

	client, err := factory()
	if err != nil {
		return nil, err
	}
	cache.clients[fullKey] = client
	return client, nil
}

func (cache *proxyHTTPClientCache) removeProxy(proxyCacheKey string) []*http.Client {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	removed := make([]*http.Client, 0)
	prefix := proxyCacheKey + "\x00"
	for key, client := range cache.clients {
		if strings.HasPrefix(key, prefix) {
			removed = append(removed, client)
			delete(cache.clients, key)
		}
	}
	for alias, canonicalKey := range cache.aliases {
		if canonicalKey == proxyCacheKey {
			delete(cache.aliases, alias)
		}
	}
	return removed
}

func (cache *proxyHTTPClientCache) reset() map[string]*http.Client {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	oldClients := cache.clients
	cache.clients = make(map[string]*http.Client)
	cache.aliases = make(map[string]string)
	return oldClients
}

func configureProxyTransport(transport *http.Transport, proxyURL *url.URL) error {
	switch proxyURL.Scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(proxyURL)
		return nil
	case "socks5", "socks5h":
		transport.Proxy = nil
		forwardDialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		dialer, err := proxy.FromURL(proxyURL, forwardDialer)
		if err != nil {
			return err
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return fmt.Errorf("SOCKS proxy dialer does not support context cancellation")
		}
		transport.DialContext = contextDialer.DialContext
		return nil
	default:
		return fmt.Errorf("unsupported proxy scheme")
	}
}

func newTransportFactory(proxyURL *url.URL, tlsConfig *tls.Config) (func() *http.Transport, error) {
	// Validate proxy configuration once before creating shard transports.
	if proxyURL != nil {
		probe := newRelayHTTPTransport()
		if err := configureProxyTransport(probe, proxyURL); err != nil {
			return nil, err
		}
	}
	return func() *http.Transport {
		transport := newRelayHTTPTransport()
		if proxyURL != nil {
			_ = configureProxyTransport(transport, proxyURL)
		} else {
			transport.Proxy = http.ProxyFromEnvironment
		}
		if tlsConfig != nil {
			transport.TLSClientConfig = tlsConfig.Clone()
		}
		return transport
	}, nil
}

func newHTTPClientFromPolicy(policy HTTPTransportPolicy, proxyURL *url.URL, tlsConfig *tls.Config) (*http.Client, error) {
	factory, err := newTransportFactory(proxyURL, tlsConfig)
	if err != nil {
		return nil, err
	}
	return newHTTPClientFromTransportFactory(policy, factory), nil
}

func newHTTPClientFromTransportFactory(policy HTTPTransportPolicy, factory func() *http.Transport) *http.Client {
	if policy.Shards < 1 {
		policy.Shards = 1
	}
	if policy.Protocol == dto.HTTPProtocolHTTP1 || policy.Shards == 1 {
		transport := factory()
		applyHTTPTransportPolicy(transport, policy)
		return newRelayHTTPClient(transport)
	}
	shardedFactory := func() *http.Transport {
		transport := factory()
		applyHTTPTransportPolicy(transport, policy)
		return transport
	}
	return newRelayHTTPClient(newShardedRoundTripper(policy, shardedFactory))
}

func newDirectHTTPClient(policy HTTPTransportPolicy, tlsConfig *tls.Config) *http.Client {
	client, err := newHTTPClientFromPolicy(policy, nil, tlsConfig)
	if err != nil {
		// Direct clients cannot fail proxy configuration.
		transport := newRelayHTTPTransport()
		applyHTTPTransportPolicy(transport, policy)
		return newRelayHTTPClient(transport)
	}
	return client
}

// newHTTPClientWithPolicyAndTLS is a test seam that builds a never-used transport
// stack with the given policy and TLS config (for httptest certificate trust).
func newHTTPClientWithPolicyAndTLS(policy HTTPTransportPolicy, tlsConfig *tls.Config) *http.Client {
	return newDirectHTTPClient(policy, tlsConfig)
}

func newProxyHTTPClient(proxyURL *url.URL) (*http.Client, error) {
	return newHTTPClientFromPolicy(defaultHTTPTransportPolicy(), proxyURL, nil)
}

// GetHttpClientWithProxy returns the default client or a cached proxy-enabled client.
func GetHttpClientWithProxy(rawProxyURL string) (*http.Client, error) {
	return GetHttpClientWithProxySettings(rawProxyURL, dto.ChannelSettings{})
}

// GetHttpClientWithProxySettings returns a cached HTTP client for the proxy URL and
// channel transport settings. Default auto + 1 shard shares the same client pool as
// GetHttpClientWithProxy / GetHttpClient for the empty-proxy case.
func GetHttpClientWithProxySettings(rawProxyURL string, settings dto.ChannelSettings) (*http.Client, error) {
	policy := NormalizeHTTPTransportPolicy(settings)
	trimmedProxyURL := strings.TrimSpace(rawProxyURL)

	if trimmedProxyURL == "" {
		return getOrCreateDirectClient(policy)
	}

	if client, ok := proxyClients.get(trimmedProxyURL, policy); ok {
		return client, nil
	}

	parsedURL, legacySuffixStripped, err := common.ParseProxyURLRuntime(trimmedProxyURL)
	if err != nil {
		return nil, err
	}
	config := newProxyURLConfig(parsedURL)
	if legacySuffixStripped {
		warnLegacyProxyURLOnce(config)
	}
	return proxyClients.getOrCreate(trimmedProxyURL, config, policy, func() (*http.Client, error) {
		return newHTTPClientFromPolicy(policy, config.parsedURL, nil)
	})
}

func getOrCreateDirectClient(policy HTTPTransportPolicy) (*http.Client, error) {
	defaultPolicy := defaultHTTPTransportPolicy()
	if policy == defaultPolicy {
		if client := GetHttpClient(); client != nil {
			return client, nil
		}
		// Compatibility with pre-init callers: never assign httpClient outside InitHttpClient.
		return http.DefaultClient, nil
	}

	if client, ok := proxyClients.get("", policy); ok {
		return client, nil
	}
	return proxyClients.getOrCreate("", nil, policy, func() (*http.Client, error) {
		return newDirectHTTPClient(policy, nil), nil
	})
}

// InvalidateProxyClient removes every cached policy variant for one proxy and
// closes their idle connections (including all HTTP/2 shards).
func InvalidateProxyClient(rawProxyURL string) {
	parsedURL, legacySuffixStripped, err := common.ParseProxyURLRuntime(rawProxyURL)
	if err != nil || parsedURL == nil {
		return
	}
	config := newProxyURLConfig(parsedURL)
	if legacySuffixStripped {
		warnLegacyProxyURLOnce(config)
	}
	for _, client := range proxyClients.removeProxy(config.cacheKey) {
		client.CloseIdleConnections()
	}
}

// ResetProxyClientCache clears cached proxy and non-default direct policy clients
// and closes idle connections on every transport/shard. The package-level default
// httpClient pointer stays stable after InitHttpClient; it is only closed and
// re-registered in the policy cache so concurrent GetHttpClient readers never race
// a pointer replacement.
func ResetProxyClientCache() {
	defaultClient := httpClient
	for _, client := range proxyClients.reset() {
		client.CloseIdleConnections()
	}
	if defaultClient == nil {
		return
	}
	defaultClient.CloseIdleConnections()
	proxyClients.store(clientCacheKey("", defaultHTTPTransportPolicy()), defaultClient)
}

// NewProxyHttpClient is kept for compatibility.
// Deprecated: use GetHttpClientWithProxy.
func NewProxyHttpClient(proxyURL string) (*http.Client, error) {
	return GetHttpClientWithProxy(proxyURL)
}
