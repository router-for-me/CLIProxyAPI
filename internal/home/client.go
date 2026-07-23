package home

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
	log "github.com/sirupsen/logrus"
)

const (
	redisKeyConfig             = "config"
	redisChannelConfig         = "config"
	redisKeyUsage              = "usage"
	redisKeyInFlightSnapshot   = "in-flight-snapshot"
	redisKeyConcurrencyRelease = "concurrency-release"
	redisKeyRequestLog         = "request-log"
	redisKeyAppLog             = "app-log"
	redisKeyPluginStatus       = "plugin-status"
	redisKeyPluginTasks        = "plugin-tasks"
	redisKeyPluginSync         = "plugin-sync"

	homeReconnectInterval                     = time.Second
	homeReconnectFailoverThreshold            = 3
	homeRedisOperationTimeout                 = 3 * time.Second
	homePluginSyncOperationTimeout            = 2 * time.Minute
	homeSubscriptionReceiveTimeout            = 3 * time.Second
	credentialConcurrencyNodeHeartbeatTimeout = 20 * time.Second
	redisChannelCluster                       = "cluster"
)

const pluginSyncUnsupportedErrorType = "plugin_sync_unsupported"

// DispatchError classifies whether Home may have processed an auth dispatch request.
type DispatchError struct {
	Err       error
	Ambiguous bool
}

func (e *DispatchError) Error() string {
	if e == nil || e.Err == nil {
		return "home auth dispatch failed"
	}
	return e.Err.Error()
}

func (e *DispatchError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewAmbiguousDispatchError marks a post-send transport failure as requiring a client abort.
func NewAmbiguousDispatchError(err error) error {
	if err == nil {
		return nil
	}
	return &DispatchError{Err: err, Ambiguous: true}
}

// IsAmbiguousDispatchError reports whether Home may have processed the dispatch request.
func IsAmbiguousDispatchError(err error) bool {
	var dispatchErr *DispatchError
	return errors.As(err, &dispatchErr) && dispatchErr.Ambiguous
}

var (
	ErrDisabled              = errors.New("home client disabled")
	ErrNotConnected          = errors.New("home not connected")
	ErrEmptyResponse         = errors.New("home returned empty response")
	ErrAuthNotFound          = errors.New("home auth not found")
	ErrConfigNotFound        = errors.New("home config not found")
	ErrModelsNotFound        = errors.New("home models not found")
	ErrPluginSyncUnsupported = errors.New("home plugin sync is unsupported")
	ErrDispatchFenced        = errors.New("home auth dispatch is fenced")
)

type clusterNode struct {
	IP          string    `json:"ip"`
	Port        int       `json:"port"`
	ClientCount int       `json:"client_count"`
	IsMaster    bool      `json:"is_master"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

type clusterNodesEnvelope struct {
	OK    bool          `json:"ok"`
	Nodes []clusterNode `json:"nodes"`
}

type PluginTask struct {
	ID             uint      `json:"id"`
	Operation      string    `json:"operation"`
	PluginID       string    `json:"plugin_id"`
	TargetNodeType string    `json:"target_node_type,omitempty"`
	TargetNodeID   string    `json:"target_node_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type KVSetOptions struct {
	EX time.Duration
	PX time.Duration
	NX bool
	XX bool
}

type subscriptionCloser interface {
	Close() error
}

type Client struct {
	mu sync.Mutex

	homeCfg  config.HomeConfig
	seedHost string
	seedPort int

	cmd         *redis.Client
	cmdOptions  *redis.Options
	sub         *redis.Client
	release     *redis.Client
	connections map[*homeDispatchConn]struct{}
	lifecycle   config.CredentialConcurrencyConfig
	limiter     atomic.Pointer[config.CredentialConcurrencyConfig]
	managed     bool

	heartbeatOK       atomic.Bool
	dispatchFenced    atomic.Bool
	clusterNodes      []clusterNode
	reconnectFailures int
}

func New(homeCfg config.HomeConfig) *Client {
	return &Client{
		homeCfg:  homeCfg,
		seedHost: strings.TrimSpace(homeCfg.Host),
		seedPort: homeCfg.Port,
	}
}

func (c *Client) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.homeCfg.Enabled
}

func (c *Client) HeartbeatOK() bool {
	if c == nil {
		return false
	}
	if !c.Enabled() {
		return false
	}
	return c.heartbeatOK.Load()
}

// Close permanently ends this client's dispatch lifetime.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.dispatchFenced.Store(true)
	c.heartbeatOK.Store(false)
	c.mu.Lock()
	commandClient, subscriptionClient, connections := c.detachClientsLocked()
	releaseClient := c.release
	c.release = nil
	c.mu.Unlock()
	closeDetachedClients(commandClient, subscriptionClient, connections)
	if releaseClient != nil {
		_ = releaseClient.Close()
	}
}

// closeBootstrapPools replaces private bootstrap pools without ending the client lifetime.
func (c *Client) closeBootstrapPools() {
	if c == nil {
		return
	}
	c.heartbeatOK.Store(false)
	c.mu.Lock()
	commandClient, subscriptionClient, connections := c.detachClientsLocked()
	c.mu.Unlock()
	closeDetachedClients(commandClient, subscriptionClient, connections)
}

// AbortAmbiguousDispatch fences this client after an auth dispatch response is ambiguous.
func (c *Client) AbortAmbiguousDispatch() {
	if c == nil {
		return
	}
	c.dispatchFenced.Store(true)
	c.heartbeatOK.Store(false)
	c.mu.Lock()
	commandClient, subscriptionClient, connections := c.detachClientsLocked()
	releaseClient := c.release
	c.release = nil
	c.mu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	if commandClient != nil {
		go func() {
			_ = commandClient.Close()
		}()
	}
	if subscriptionClient != nil {
		go func() {
			_ = subscriptionClient.Close()
		}()
	}
	if releaseClient != nil {
		go func() {
			_ = releaseClient.Close()
		}()
	}
}

func (c *Client) detachClientsLocked() (*redis.Client, *redis.Client, []*homeDispatchConn) {
	connections := make([]*homeDispatchConn, 0, len(c.connections))
	for conn := range c.connections {
		connections = append(connections, conn)
	}
	commandClient := c.cmd
	subscriptionClient := c.sub
	c.cmd = nil
	c.cmdOptions = nil
	c.sub = nil
	c.connections = nil
	return commandClient, subscriptionClient, connections
}

func closeDetachedClients(commandClient *redis.Client, subscriptionClient *redis.Client, connections []*homeDispatchConn) {
	for _, conn := range connections {
		_ = conn.Close()
	}
	if commandClient != nil {
		_ = commandClient.Close()
	}
	if subscriptionClient != nil {
		_ = subscriptionClient.Close()
	}
}

func (c *Client) closeClientsLocked() {
	commandClient, subscriptionClient, connections := c.detachClientsLocked()
	releaseClient := c.release
	c.release = nil
	go func() {
		closeDetachedClients(commandClient, subscriptionClient, connections)
		if releaseClient != nil {
			_ = releaseClient.Close()
		}
	}()
}

// SetManagedLifetime defers client shutdown to the Service lifetime owner.
func (c *Client) SetManagedLifetime(managed bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.managed = managed
	c.mu.Unlock()
}

func (c *Client) managedLifetime() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.managed
}

func (c *Client) addr() (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addrLocked()
}

func (c *Client) addrLocked() (string, bool) {
	host := strings.TrimSpace(c.homeCfg.Host)
	if host == "" {
		return "", false
	}
	if c.homeCfg.Port <= 0 {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(c.homeCfg.Port)), true
}

func (c *Client) ensureClients() error {
	if c == nil {
		return ErrDisabled
	}
	if c.dispatchFenced.Load() {
		return ErrDispatchFenced
	}
	if !c.Enabled() {
		return ErrDisabled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dispatchFenced.Load() {
		return ErrDispatchFenced
	}

	addr, ok := c.addrLocked()
	if !ok {
		return fmt.Errorf("home: invalid address (host=%q port=%d)", c.homeCfg.Host, c.homeCfg.Port)
	}

	if c.cmd == nil {
		options, errOptions := c.redisOptionsLocked(addr)
		if errOptions != nil {
			return errOptions
		}
		c.cmdOptions = cloneRedisOptions(options)
		c.cmd = redis.NewClient(options)
	}
	if c.sub == nil {
		options, errOptions := c.redisOptionsLocked(addr)
		if errOptions != nil {
			return errOptions
		}
		c.sub = redis.NewClient(options)
	}
	return nil
}

func (c *Client) redisOptionsLocked(addr string) (*redis.Options, error) {
	tlsConfig, errTLS := c.homeTLSConfigLocked(addr)
	if errTLS != nil {
		return nil, errTLS
	}
	options := &redis.Options{
		Addr:                  addr,
		TLSConfig:             tlsConfig,
		DialTimeout:           homeRedisOperationTimeout,
		ReadTimeout:           homeRedisOperationTimeout,
		WriteTimeout:          homeRedisOperationTimeout,
		MaxRetries:            -1,
		DialerRetries:         1,
		ContextTimeoutEnabled: true,
	}
	options.Dialer = c.trackedRedisDialer(redis.NewDialer(options))
	return options, nil
}

type homeDispatchConn struct {
	net.Conn
	client *Client
	once   sync.Once
}

func (c *Client) trackedRedisDialer(dialer func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		conn, errDial := dialer(ctx, network, address)
		if errDial != nil {
			return nil, errDial
		}
		wrapped := &homeDispatchConn{Conn: conn, client: c}
		if c == nil {
			return wrapped, nil
		}
		c.mu.Lock()
		if c.dispatchFenced.Load() {
			c.mu.Unlock()
			_ = wrapped.Close()
			return nil, ErrDispatchFenced
		}
		if c.connections == nil {
			c.connections = make(map[*homeDispatchConn]struct{})
		}
		c.connections[wrapped] = struct{}{}
		c.mu.Unlock()
		return wrapped, nil
	}
}

func (c *homeDispatchConn) Close() error {
	if c == nil || c.Conn == nil {
		return net.ErrClosed
	}
	c.once.Do(func() {
		if c.client != nil {
			c.client.mu.Lock()
			delete(c.client.connections, c)
			c.client.mu.Unlock()
		}
	})
	return c.Conn.Close()
}

func cloneRedisOptions(options *redis.Options) *redis.Options {
	if options == nil {
		return nil
	}
	cloned := *options
	if options.TLSConfig != nil {
		cloned.TLSConfig = options.TLSConfig.Clone()
	}
	if options.MaintNotificationsConfig != nil {
		maintNotifications := *options.MaintNotificationsConfig
		cloned.MaintNotificationsConfig = &maintNotifications
	}
	return &cloned
}

func (c *Client) homeTLSConfigLocked(addr string) (*tls.Config, error) {
	serverName := strings.TrimSpace(c.homeCfg.TLS.ServerName)
	if serverName == "" {
		if c.homeCfg.TLS.UseTargetServerName {
			serverName = hostFromAddress(addr)
		} else {
			serverName = strings.TrimSpace(c.seedHost)
		}
	}
	if serverName == "" {
		serverName = strings.TrimSpace(c.homeCfg.Host)
	}
	return newHomeTLSConfig(c.homeCfg.TLS, serverName)
}

func hostFromAddress(addr string) string {
	host, _, errSplit := net.SplitHostPort(strings.TrimSpace(addr))
	if errSplit == nil {
		return strings.TrimSpace(host)
	}
	return strings.TrimSpace(addr)
}

func newHomeTLSConfig(cfg config.HomeTLSConfig, fallbackServerName string) (*tls.Config, error) {
	if !cfg.Enable {
		return nil, nil
	}

	serverName := strings.TrimSpace(cfg.ServerName)
	if serverName == "" {
		serverName = strings.TrimSpace(fallbackServerName)
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}

	clientCertPath := strings.TrimSpace(cfg.ClientCert)
	clientKeyPath := strings.TrimSpace(cfg.ClientKey)
	if clientCertPath != "" || clientKeyPath != "" {
		if clientCertPath == "" || clientKeyPath == "" {
			return nil, fmt.Errorf("home tls: client certificate and key must be set together")
		}
		certPair, errLoad := tls.LoadX509KeyPair(clientCertPath, clientKeyPath)
		if errLoad != nil {
			return nil, fmt.Errorf("home tls: load client certificate: %w", errLoad)
		}
		tlsConfig.Certificates = []tls.Certificate{certPair}
	}

	caCertPath := strings.TrimSpace(cfg.CACert)
	if caCertPath == "" {
		return tlsConfig, nil
	}

	caCertPEM, errRead := os.ReadFile(caCertPath)
	if errRead != nil {
		return nil, fmt.Errorf("home tls: read ca-cert: %w", errRead)
	}

	certPool, errPool := x509.SystemCertPool()
	if errPool != nil || certPool == nil {
		certPool = x509.NewCertPool()
	}
	if !certPool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("home tls: ca-cert contains no PEM certificates")
	}
	tlsConfig.RootCAs = certPool

	return tlsConfig, nil
}

func (c *Client) commandClient() (*redis.Client, error) {
	if c == nil || c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	if errEnsure := c.ensureClients(); errEnsure != nil {
		return nil, errEnsure
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	if c.cmd == nil {
		return nil, ErrNotConnected
	}
	return c.cmd, nil
}

func (c *Client) pluginSyncCommandOptions() (*redis.Options, error) {
	if errEnsure := c.ensureClients(); errEnsure != nil {
		return nil, errEnsure
	}
	c.mu.Lock()
	options := cloneRedisOptions(c.cmdOptions)
	c.mu.Unlock()
	if options == nil {
		return nil, ErrNotConnected
	}
	return options, nil
}

func (c *Client) subscriptionClient() (*redis.Client, error) {
	if errEnsure := c.ensureClients(); errEnsure != nil {
		return nil, errEnsure
	}
	c.mu.Lock()
	sub := c.sub
	c.mu.Unlock()
	if sub == nil {
		return nil, ErrNotConnected
	}
	return sub, nil
}

func (c *Client) Ping(ctx context.Context) error {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	return cmd.Ping(ctx).Err()
}

func (c *Client) clusterDiscoveryEnabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clusterDiscoveryEnabledLocked()
}

func (c *Client) clusterDiscoveryEnabledLocked() bool {
	return !c.homeCfg.DisableClusterDiscovery
}

func (c *Client) refreshBestClusterNode(ctx context.Context) {
	if !c.clusterDiscoveryEnabled() {
		return
	}
	switched, errRefresh := c.refreshClusterNodes(ctx)
	if errRefresh != nil {
		log.Debugf("home cluster nodes unavailable: %v", errRefresh)
		return
	}
	if switched {
		if addr, ok := c.addr(); ok {
			log.Infof("home cluster target switched to %s", addr)
		}
	}
}

func (c *Client) refreshClusterNodes(ctx context.Context) (bool, error) {
	if !c.clusterDiscoveryEnabled() {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return false, errClient
	}
	raw, errDo := cmd.Do(ctx, "CLUSTER", "NODES").Text()
	if errDo != nil {
		return false, errDo
	}

	nodes, errParse := parseClusterNodesPayload([]byte(raw))
	if errParse != nil {
		return false, errParse
	}
	if len(nodes) == 0 {
		return false, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusterNodes = nodes
	c.reconnectFailures = 0
	return c.switchToNodeLocked(nodes[0]), nil
}

func parseClusterNodesPayload(raw []byte) ([]clusterNode, error) {
	var envelope clusterNodesEnvelope
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return normalizeClusterNodes(envelope.Nodes), nil
}

func (c *Client) updateClusterNodesFromPayload(raw []byte) error {
	if c == nil || !c.clusterDiscoveryEnabled() {
		return nil
	}
	nodes, errParse := parseClusterNodesPayload(raw)
	if errParse != nil {
		return errParse
	}
	c.mu.Lock()
	c.clusterNodes = nodes
	c.mu.Unlock()
	return nil
}

func normalizeClusterNodes(nodes []clusterNode) []clusterNode {
	out := make([]clusterNode, 0, len(nodes))
	for _, node := range nodes {
		node.IP = strings.TrimSpace(node.IP)
		if node.IP == "" || node.Port <= 0 {
			continue
		}
		if node.ClientCount < 0 {
			node.ClientCount = 0
		}
		out = append(out, node)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ClientCount < out[j].ClientCount
	})
	return out
}

func (c *Client) switchToNodeLocked(node clusterNode) bool {
	host := strings.TrimSpace(node.IP)
	if host == "" || node.Port <= 0 {
		return false
	}
	if strings.TrimSpace(c.homeCfg.Host) == host && c.homeCfg.Port == node.Port {
		return false
	}
	c.homeCfg.Host = host
	c.homeCfg.Port = node.Port
	c.closeClientsLocked()
	return true
}

func (c *Client) markReconnectFailure(reason string) {
	switched, addr := c.failoverAfterReconnectFailure()
	if switched {
		log.Warnf("home control center unavailable after repeated %s failures; switching to %s", reason, addr)
	}
}

func (c *Client) failoverAfterReconnectFailure() (bool, string) {
	if c == nil {
		return false, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.clusterDiscoveryEnabledLocked() {
		c.reconnectFailures = 0
		return false, ""
	}
	c.reconnectFailures++
	if c.reconnectFailures < homeReconnectFailoverThreshold {
		return false, ""
	}
	c.reconnectFailures = 0

	return c.switchToNextNodeLocked()
}

func (c *Client) failoverAfterSubscriptionTimeout() (bool, string) {
	if c == nil {
		return false, ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.clusterDiscoveryEnabledLocked() {
		c.reconnectFailures = 0
		return false, ""
	}
	c.reconnectFailures = 0
	return c.switchToNextNodeLocked()
}

func (c *Client) switchToNextNodeLocked() (bool, string) {
	currentHost := strings.TrimSpace(c.homeCfg.Host)
	currentPort := c.homeCfg.Port
	candidates := append([]clusterNode(nil), c.clusterNodes...)
	if strings.TrimSpace(c.seedHost) != "" && c.seedPort > 0 {
		candidates = append(candidates, clusterNode{IP: c.seedHost, Port: c.seedPort})
	}
	for _, node := range candidates {
		host := strings.TrimSpace(node.IP)
		if host == "" || node.Port <= 0 {
			continue
		}
		if host == currentHost && node.Port == currentPort {
			continue
		}
		if c.switchToNodeLocked(clusterNode{IP: host, Port: node.Port}) {
			addr, _ := c.addrLocked()
			return true, addr
		}
	}
	return false, ""
}

func (c *Client) markSubscriptionTimeout() {
	switched, addr := c.failoverAfterSubscriptionTimeout()
	if switched {
		log.Warnf("home subscription heartbeat timeout; switching to %s", addr)
	}
}

func (c *Client) resetReconnectFailures() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.reconnectFailures = 0
	c.mu.Unlock()
}

func (c *Client) GetConfig(ctx context.Context) ([]byte, error) {
	c.refreshBestClusterNode(ctx)
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, errClient
	}
	raw, err := cmd.Get(ctx, redisKeyConfig).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrConfigNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, ErrEmptyResponse
	}
	return raw, nil
}

func (c *Client) GetModels(ctx context.Context, headers http.Header, query url.Values) ([]byte, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, errClient
	}
	req := modelsRequest{
		Type:    "models",
		Headers: headersToLowerMap(headers),
		Query:   queryToLowerMap(query),
	}
	keyBytes, err := json.Marshal(&req)
	if err != nil {
		return nil, err
	}
	raw, err := cmd.Get(ctx, string(keyBytes)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrModelsNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, ErrEmptyResponse
	}
	return raw, nil
}

func buildKVSetArgs(key string, value []byte, opts KVSetOptions) ([]any, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("home kv: key is empty")
	}
	if opts.EX > 0 && opts.PX > 0 {
		return nil, fmt.Errorf("home kv: EX and PX are mutually exclusive")
	}
	if opts.EX < 0 || opts.PX < 0 {
		return nil, fmt.Errorf("home kv: ttl must not be negative")
	}
	if opts.NX && opts.XX {
		return nil, fmt.Errorf("home kv: NX and XX are mutually exclusive")
	}

	args := []any{key, append([]byte(nil), value...)}
	if opts.EX > 0 {
		args = append(args, "EX", durationCeil(opts.EX, time.Second))
	}
	if opts.PX > 0 {
		args = append(args, "PX", durationCeil(opts.PX, time.Millisecond))
	}
	if opts.NX {
		args = append(args, "NX")
	}
	if opts.XX {
		args = append(args, "XX")
	}
	return args, nil
}

func durationCeil(value time.Duration, unit time.Duration) int64 {
	if value <= 0 || unit <= 0 {
		return 0
	}
	return int64((value + unit - 1) / unit)
}

func (c *Client) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, false, errClient
	}
	raw, errGet := cmd.Get(ctx, key).Bytes()
	if errors.Is(errGet, redis.Nil) {
		return nil, false, nil
	}
	if errGet != nil {
		return nil, false, errGet
	}
	return append([]byte(nil), raw...), true, nil
}

func (c *Client) KVSet(ctx context.Context, key string, value []byte, opts KVSetOptions) (bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return false, errClient
	}
	args, errArgs := buildKVSetArgs(key, value, opts)
	if errArgs != nil {
		return false, errArgs
	}
	result, errSet := cmd.Do(ctx, append([]any{"SET"}, args...)...).Result()
	if errors.Is(errSet, redis.Nil) {
		return false, nil
	}
	if errSet != nil {
		return false, errSet
	}
	if result == nil {
		return false, nil
	}
	return true, nil
}

func (c *Client) KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	opts := KVSetOptions{NX: true}
	if ttl > 0 {
		opts.EX = ttl
	}
	return c.KVSet(ctx, key, value, opts)
}

// KVCompareAndSwap atomically replaces a value only when its current state matches the expected state.
func (c *Client) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return false, errClient
	}
	const script = `
local current = redis.call("GET", KEYS[1])
if ARGV[1] == "1" then
  if not current or current ~= ARGV[2] then
    return 0
  end
elseif current then
  return 0
end
local ttl = tonumber(ARGV[4])
if ttl and ttl > 0 then
  redis.call("SET", KEYS[1], ARGV[3], "PX", ttl)
else
  redis.call("SET", KEYS[1], ARGV[3])
end
return 1
`
	expectedFlag := "0"
	if expectedExists {
		expectedFlag = "1"
	}
	result, errEval := cmd.Eval(ctx, script, []string{key}, expectedFlag, expected, value, durationCeil(ttl, time.Millisecond)).Int64()
	if errEval != nil {
		return false, errEval
	}
	return result == 1, nil
}

func (c *Client) KVDel(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return 0, errClient
	}
	return cmd.Del(ctx, keys...).Result()
}

func (c *Client) KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return false, errClient
	}
	return cmd.Expire(ctx, key, ttl).Result()
}

func (c *Client) KVTTL(ctx context.Context, key string) (time.Duration, bool, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return 0, false, errClient
	}
	ttl, errTTL := cmd.TTL(ctx, key).Result()
	if errTTL != nil {
		return 0, false, errTTL
	}
	switch {
	case ttl <= -2*time.Second:
		return 0, false, nil
	case ttl == -1*time.Second:
		return 0, true, nil
	default:
		return ttl, true, nil
	}
}

func (c *Client) KVIncrBy(ctx context.Context, key string, delta int64) (int64, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return 0, errClient
	}
	return cmd.IncrBy(ctx, key, delta).Result()
}

func (c *Client) KVMGet(ctx context.Context, keys ...string) ([][]byte, []bool, error) {
	if len(keys) == 0 {
		return nil, nil, nil
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, nil, errClient
	}
	items, errMGet := cmd.MGet(ctx, keys...).Result()
	if errMGet != nil {
		return nil, nil, errMGet
	}
	values := make([][]byte, len(items))
	found := make([]bool, len(items))
	for i, item := range items {
		switch typed := item.(type) {
		case nil:
			continue
		case string:
			values[i] = []byte(typed)
			found[i] = true
		case []byte:
			values[i] = append([]byte(nil), typed...)
			found[i] = true
		default:
			return nil, nil, fmt.Errorf("home kv: unsupported MGET item type %T", item)
		}
	}
	return values, found, nil
}

func (c *Client) KVMSet(ctx context.Context, pairs map[string][]byte) error {
	if len(pairs) == 0 {
		return nil
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	keys := make([]string, 0, len(pairs))
	for key := range pairs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]any, 0, 1+len(keys)*2)
	args = append(args, "MSET")
	for _, key := range keys {
		args = append(args, key, append([]byte(nil), pairs[key]...))
	}
	return cmd.Do(ctx, args...).Err()
}

func headersToLowerMap(headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, values := range headers {
		k := strings.ToLower(strings.TrimSpace(key))
		if k == "" {
			continue
		}
		if len(values) == 0 {
			out[k] = ""
			continue
		}
		trimmed := make([]string, 0, len(values))
		for _, v := range values {
			trimmed = append(trimmed, strings.TrimSpace(v))
		}
		out[k] = strings.Join(trimmed, ", ")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func queryToLowerMap(query url.Values) map[string]string {
	if len(query) == 0 {
		return nil
	}
	out := make(map[string]string, len(query))
	for key, values := range query {
		k := strings.ToLower(strings.TrimSpace(key))
		if k == "" {
			continue
		}
		if len(values) == 0 {
			out[k] = ""
			continue
		}
		trimmed := make([]string, 0, len(values))
		for _, v := range values {
			trimmed = append(trimmed, strings.TrimSpace(v))
		}
		out[k] = strings.Join(trimmed, ", ")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func newAuthDispatchRequest(requestedModel string, sessionID string, headers http.Header, count int) authDispatchRequest {
	if count <= 0 {
		count = 1
	}
	return authDispatchRequest{
		Type:                "auth",
		Model:               requestedModel,
		Count:               count,
		ConcurrencyProtocol: 1,
		SessionID:           strings.TrimSpace(sessionID),
		Headers:             headersToLowerMap(headers),
	}
}

func (c *Client) RPopAuth(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int) ([]byte, error) {
	if c == nil || c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil, fmt.Errorf("home: requested model is empty")
	}
	req := newAuthDispatchRequest(requestedModel, sessionID, headers, count)
	keyBytes, errMarshal := json.Marshal(&req)
	if errMarshal != nil {
		return nil, errMarshal
	}
	if c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, errClient
	}
	if c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	conn := cmd.Conn()
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			log.WithError(errClose).Debug("Home auth dispatch connection close failed")
		}
	}()
	if errProbe := conn.Ping(ctx).Err(); errProbe != nil {
		return nil, errProbe
	}
	if c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	raw, errRPop := conn.RPop(ctx, string(keyBytes)).Bytes()
	if errors.Is(errRPop, redis.Nil) {
		return nil, ErrAuthNotFound
	}
	if errRPop != nil {
		if isAmbiguousIssuedRPopAuthError(errRPop) {
			return nil, NewAmbiguousDispatchError(errRPop)
		}
		return nil, errRPop
	}
	if len(raw) == 0 {
		return nil, ErrEmptyResponse
	}
	return raw, nil
}

func isAmbiguousIssuedRPopAuthError(err error) bool {
	if err == nil || errors.Is(err, redis.Nil) {
		return false
	}
	var redisErr redis.Error
	return !errors.As(err, &redisErr)
}

func (c *Client) GetRefreshAuth(ctx context.Context, authIndex string) ([]byte, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, errClient
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("home: auth_index is empty")
	}
	req := refreshRequest{
		Type:      "refresh",
		AuthIndex: authIndex,
	}
	keyBytes, err := json.Marshal(&req)
	if err != nil {
		return nil, err
	}

	raw, err := cmd.Get(ctx, string(keyBytes)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrAuthNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, ErrEmptyResponse
	}
	return raw, nil
}

func (c *Client) LPushUsage(ctx context.Context, payload []byte) error {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	if len(payload) == 0 {
		return nil
	}
	return cmd.LPush(ctx, redisKeyUsage, payload).Err()
}

// LPushInFlightSnapshot publishes a bounded in-flight observation frame.
func (c *Client) LPushInFlightSnapshot(ctx context.Context, payload []byte) error {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	return cmd.LPush(ctx, redisKeyInFlightSnapshot, payload).Err()
}

// PushConcurrencyRelease sends one cumulative concurrency release frame through an independent client.
func (c *Client) PushConcurrencyRelease(ctx context.Context, frame ConcurrencyReleaseFrame) error {
	if frame.CredentialID == "" || frame.Model == "" || frame.ReleaseSeq <= 0 {
		return fmt.Errorf("invalid concurrency release frame")
	}
	cmd, errClient := c.concurrencyReleaseClient()
	if errClient != nil {
		return errClient
	}
	payload, errMarshal := json.Marshal(frame)
	if errMarshal != nil {
		return fmt.Errorf("marshal concurrency release frame: %w", errMarshal)
	}
	return cmd.Do(ctx, "LPUSH", redisKeyConcurrencyRelease, payload).Err()
}

func (c *Client) concurrencyReleaseClient() (*redis.Client, error) {
	if c == nil || c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dispatchFenced.Load() {
		return nil, ErrDispatchFenced
	}
	if c.release != nil {
		return c.release, nil
	}
	addr, ok := c.addrLocked()
	if !ok {
		return nil, fmt.Errorf("home: invalid address (host=%q port=%d)", c.homeCfg.Host, c.homeCfg.Port)
	}
	options, errOptions := c.redisOptionsLocked(addr)
	if errOptions != nil {
		return nil, errOptions
	}
	options.Dialer = redis.NewDialer(options)
	c.release = redis.NewClient(options)
	return c.release, nil
}

func (c *Client) RPushRequestLog(ctx context.Context, payload []byte) error {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	if len(payload) == 0 {
		return nil
	}
	return cmd.RPush(ctx, redisKeyRequestLog, payload).Err()
}

func (c *Client) RPushAppLog(ctx context.Context, payload []byte) error {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	if len(payload) == 0 {
		return nil
	}
	return cmd.RPush(ctx, redisKeyAppLog, payload).Err()
}

func (c *Client) RPushPluginStatus(ctx context.Context, payload []byte) error {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return errClient
	}
	if len(payload) == 0 {
		return nil
	}
	return cmd.RPush(ctx, redisKeyPluginStatus, payload).Err()
}

func (c *Client) GetPluginTasks(ctx context.Context) ([]PluginTask, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, errClient
	}
	raw, errGet := cmd.Get(ctx, redisKeyPluginTasks).Bytes()
	if errors.Is(errGet, redis.Nil) {
		return nil, nil
	}
	if errGet != nil {
		return nil, errGet
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var tasks []PluginTask
	if errUnmarshal := json.Unmarshal(raw, &tasks); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return tasks, nil
}

func (c *Client) GetPluginSync(ctx context.Context, request pluginstore.PluginSyncRequest) (pluginstore.PluginSyncResponse, error) {
	options, errOptions := c.pluginSyncCommandOptions()
	if errOptions != nil {
		return pluginstore.PluginSyncResponse{}, errOptions
	}
	payload, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return pluginstore.PluginSyncResponse{}, fmt.Errorf("marshal plugin sync request: %w", errMarshal)
	}
	requestCmd := redis.NewStringCmd(ctx, "get", redisKeyPluginSync, string(payload))
	if errProcess := processPluginSyncCommand(ctx, options, requestCmd); errProcess != nil {
		if message, ok := pluginSyncUnsupportedMessage(errProcess.Error()); ok {
			return pluginstore.PluginSyncResponse{}, fmt.Errorf("%w: %s", ErrPluginSyncUnsupported, message)
		}
		return pluginstore.PluginSyncResponse{}, errProcess
	}
	raw, errBytes := requestCmd.Bytes()
	if errBytes != nil {
		return pluginstore.PluginSyncResponse{}, errBytes
	}
	defer func() {
		requestCmd.SetVal("")
		for index := range raw {
			raw[index] = 0
		}
	}()
	if len(raw) == 0 {
		return pluginstore.PluginSyncResponse{}, ErrEmptyResponse
	}
	if message, ok := pluginSyncUnsupportedResponse(raw); ok {
		return pluginstore.PluginSyncResponse{}, fmt.Errorf("%w: %s", ErrPluginSyncUnsupported, message)
	}
	var response pluginstore.PluginSyncResponse
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		response.Clear()
		return pluginstore.PluginSyncResponse{}, fmt.Errorf("decode plugin sync response: %w", errUnmarshal)
	}
	if errValidate := response.Validate(time.Now().UTC()); errValidate != nil {
		response.Clear()
		return pluginstore.PluginSyncResponse{}, errValidate
	}
	return response, nil
}

func processPluginSyncCommand(ctx context.Context, options *redis.Options, command redis.Cmder) error {
	if options == nil {
		return ErrNotConnected
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pluginSyncClient := newPluginSyncCommandClient(ctx, options)
	if pluginSyncClient == nil {
		return ErrNotConnected
	}
	errProcess := pluginSyncClient.Process(ctx, command)
	errClose := pluginSyncClient.Close()
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if errProcess != nil {
		return errProcess
	}
	if errClose != nil {
		return fmt.Errorf("close plugin sync command client: %w", errClose)
	}
	return nil
}

func newPluginSyncCommandClient(ctx context.Context, template *redis.Options) *redis.Client {
	options := cloneRedisOptions(template)
	if options == nil {
		return nil
	}
	options.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	baseDialer := options.Dialer
	if baseDialer == nil {
		baseDialer = pluginSyncDialer(options)
	}
	options.Dialer = func(dialCtx context.Context, network string, address string) (net.Conn, error) {
		conn, errDial := baseDialer(dialCtx, network, address)
		if errDial != nil {
			return nil, errDial
		}
		return newPluginSyncCancelableConn(ctx, conn), nil
	}
	options.ReadTimeout = homePluginSyncOperationTimeout
	options.MaxRetries = -1
	return redis.NewClient(options)
}

func pluginSyncDialer(options *redis.Options) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: options.DialTimeout, KeepAlive: 5 * time.Minute}
		conn, errDial := dialer.DialContext(ctx, network, address)
		if errDial != nil {
			return nil, errDial
		}
		if options.TLSConfig == nil {
			return conn, nil
		}
		tlsConn := tls.Client(conn, options.TLSConfig)
		if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
			return nil, errors.Join(errHandshake, conn.Close())
		}
		return tlsConn, nil
	}
}

type pluginSyncCancelableConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func newPluginSyncCancelableConn(ctx context.Context, conn net.Conn) net.Conn {
	wrapped := &pluginSyncCancelableConn{Conn: conn, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			if errDeadline := conn.SetDeadline(time.Now()); errDeadline != nil {
				_ = conn.Close()
			}
		case <-wrapped.done:
		}
	}()
	return wrapped
}

func (c *pluginSyncCancelableConn) Close() error {
	if c == nil || c.Conn == nil {
		return net.ErrClosed
	}
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

func pluginSyncUnsupportedResponse(raw []byte) (string, bool) {
	var response struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
		return "", false
	}
	if pluginSyncUnsupportedCode(response.Error.Code) || pluginSyncUnsupportedCode(response.Error.Type) {
		message := strings.TrimSpace(response.Error.Message)
		if message == "" {
			message = pluginSyncUnsupportedErrorType
		}
		return message, true
	}
	return pluginSyncUnsupportedMessage(response.Error.Message)
}

func pluginSyncUnsupportedCode(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), pluginSyncUnsupportedErrorType)
}

func pluginSyncUnsupportedMessage(message string) (string, bool) {
	message = strings.ToLower(strings.TrimSpace(message))
	message = strings.TrimSpace(strings.TrimPrefix(message, "err "))
	switch message {
	case pluginSyncUnsupportedErrorType,
		"unsupported key",
		"wrong number of arguments for 'get' command":
		return message, true
	default:
		return "", false
	}
}

func (c *Client) SetLifecycleConfig(cfg config.CredentialConcurrencyConfig) error {
	if c == nil {
		return ErrDisabled
	}
	cfg = cfg.WithDefaults()
	if errValidate := config.ValidateCredentialConcurrency(cfg); errValidate != nil {
		return fmt.Errorf("validate credential concurrency lifecycle config: %w", errValidate)
	}
	c.mu.Lock()
	c.lifecycle = cfg
	c.mu.Unlock()
	c.limiter.Store(&cfg)
	return nil
}

// LimiterConfig returns the latest immutable, validated Home limiter configuration.
func (c *Client) LimiterConfig() config.CredentialConcurrencyConfig {
	if c == nil {
		return config.CredentialConcurrencyConfig{}.WithDefaults()
	}
	if cfg := c.limiter.Load(); cfg != nil {
		return *cfg
	}
	return config.CredentialConcurrencyConfig{}.WithDefaults()
}

func (c *Client) subscriptionParameters() ([]string, time.Duration) {
	if c == nil {
		return []string{redisChannelConfig}, config.CredentialConcurrencyConfig{}.WithDefaults().CPAHeartbeatTimeout
	}
	c.mu.Lock()
	cfg := c.lifecycle.WithDefaults()
	c.mu.Unlock()

	args := []string{redisChannelConfig}
	if cfg.LifecycleConfigRevision > 0 {
		args = append(args, strconv.FormatInt(cfg.LifecycleConfigRevision, 10))
	}
	return args, cfg.CPAHeartbeatTimeout
}

func (c *Client) rebuildCommandPoolAndProbe(ctx context.Context) error {
	c.promoteSubscription()
	return c.Ping(ctx)
}

func (c *Client) promoteSubscription() {
	if c == nil {
		return
	}
	c.mu.Lock()
	commandClient := c.cmd
	c.cmd = nil
	c.cmdOptions = nil
	c.mu.Unlock()
	if commandClient != nil {
		if errClose := commandClient.Close(); errClose != nil {
			log.WithError(errClose).Warn("Home bootstrap command client close failed")
		}
	}
}

func (c *Client) handleSubscriptionPayload(ctx context.Context, channel string, payload string, onConfig func([]byte) error) error {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}

	switch strings.ToLower(strings.TrimSpace(channel)) {
	case redisChannelConfig:
		if onConfig == nil {
			return nil
		}
		return onConfig([]byte(payload))
	case redisChannelCluster:
		return c.updateClusterNodesFromPayload([]byte(payload))
	default:
		return nil
	}
}

// RunConfigSubscriberLifetime runs one GET, SUBSCRIBE, and receive lifetime.
// Reconnection is owned by the service so each replacement can install a new client lifetime.
func (c *Client) RunConfigSubscriberLifetime(ctx context.Context, onConfig func([]byte) error, onReady func()) error {
	if c == nil || !c.Enabled() {
		return ErrDisabled
	}
	if onConfig == nil {
		return fmt.Errorf("home config subscriber callback is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.closeBootstrapPools()
	if errEnsure := c.ensureClients(); errEnsure != nil {
		return c.endConfigSubscriberLifetime(errEnsure)
	}

	raw, errGet := c.GetConfig(ctx)
	if errGet != nil {
		return c.endConfigSubscriberLifetime(errGet)
	}
	if errApply := onConfig(raw); errApply != nil {
		return c.endConfigSubscriberLifetime(errApply)
	}

	sub, errSubClient := c.subscriptionClient()
	if errSubClient != nil {
		return c.endConfigSubscriberLifetime(errSubClient)
	}
	args, receiveTimeout := c.subscriptionParameters()
	pubsub := sub.Subscribe(ctx, args...)
	if pubsub == nil {
		return c.endConfigSubscriberLifetime(ErrNotConnected)
	}

	if errACK := receiveSubscriptionACKs(ctx, pubsub, receiveTimeout, args[:1]); errACK != nil {
		return c.endConfigSubscriberLifetimeWithSubscription(errACK, pubsub, "failed ACK")
	}

	if errProbe := c.rebuildCommandPoolAndProbe(ctx); errProbe != nil {
		return c.endConfigSubscriberLifetimeWithSubscription(errProbe, pubsub, "fresh command probe failure")
	}
	c.heartbeatOK.Store(true)
	if onReady != nil {
		onReady()
	}

	for {
		_, receiveTimeout = c.subscriptionParameters()
		event, errReceive := pubsub.ReceiveTimeout(ctx, receiveTimeout)
		if errReceive != nil {
			return c.endConfigSubscriberLifetimeWithSubscription(errReceive, pubsub, "heartbeat loss")
		}
		switch msg := event.(type) {
		case *redis.Message:
			if msg == nil {
				continue
			}
			if errApply := c.handleSubscriptionPayload(ctx, msg.Channel, msg.Payload, onConfig); errApply != nil {
				if strings.EqualFold(strings.TrimSpace(msg.Channel), redisChannelCluster) {
					log.Warn("failed to apply cluster update from home control center, ignoring")
				} else {
					log.Warn("failed to apply config update from home control center, ignoring")
				}
			}
		case *redis.Pong:
			c.resetReconnectFailures()
		case *redis.Subscription:
			continue
		default:
			log.Debugf("home subscription returned unsupported message type %T", event)
		}
	}
}

func receiveSubscriptionACKs(ctx context.Context, pubsub *redis.PubSub, receiveTimeout time.Duration, channels []string) error {
	if pubsub == nil || len(channels) == 0 {
		return fmt.Errorf("Home subscription ACK is missing")
	}
	for index, channel := range channels {
		event, errReceive := pubsub.ReceiveTimeout(ctx, receiveTimeout)
		if errReceive != nil {
			return errReceive
		}
		ack, ok := event.(*redis.Subscription)
		if !ok || ack == nil || ack.Kind != "subscribe" || ack.Channel != channel || ack.Count != index+1 {
			return fmt.Errorf("invalid Home subscription ACK")
		}
	}
	return nil
}

func (c *Client) endConfigSubscriberLifetime(err error) error {
	c.heartbeatOK.Store(false)
	if !c.managedLifetime() {
		c.Close()
	}
	return err
}

func (c *Client) endConfigSubscriberLifetimeWithSubscription(err error, subscription subscriptionCloser, reason string) error {
	c.heartbeatOK.Store(false)
	if subscription != nil {
		if errClose := subscription.Close(); errClose != nil {
			log.WithError(errClose).Debugf("Home subscription close after %s", reason)
		}
	}
	if !c.managedLifetime() {
		c.Close()
	}
	return err
}

// StartConfigSubscriber is retained for callers that do not need the lifetime error.
func (c *Client) StartConfigSubscriber(ctx context.Context, onConfig func([]byte) error) {
	if errRun := c.RunConfigSubscriberLifetime(ctx, onConfig, nil); errRun != nil && !errors.Is(errRun, context.Canceled) {
		log.WithError(errRun).Warn("Home config subscription lifetime ended")
	}
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func sleepWithContext(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}
