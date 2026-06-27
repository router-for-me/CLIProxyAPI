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

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
)

const (
	redisKeyConfig       = "config"
	redisChannelConfig   = "config"
	redisKeyUsage        = "usage"
	redisKeyRequestLog   = "request-log"
	redisKeyAppLog       = "app-log"
	redisKeyPluginStatus = "plugin-status"
	redisKeyPluginTasks  = "plugin-tasks"

	homeReconnectInterval          = time.Second
	homeReconnectFailoverThreshold = 3
	homeRedisOperationTimeout      = 3 * time.Second
	homeSubscriptionReceiveTimeout = 3 * time.Second
	redisChannelCluster            = "cluster"
)

var (
	ErrDisabled       = errors.New("home client disabled")
	ErrNotConnected   = errors.New("home not connected")
	ErrEmptyResponse  = errors.New("home returned empty response")
	ErrAuthNotFound   = errors.New("home auth not found")
	ErrConfigNotFound = errors.New("home config not found")
	ErrModelsNotFound = errors.New("home models not found")
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

type Client struct {
	mu sync.Mutex

	homeCfg  config.HomeConfig
	seedHost string
	seedPort int

	cmd *redis.Client
	sub *redis.Client

	heartbeatOK       atomic.Bool
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

func (c *Client) Close() {
	if c == nil {
		return
	}
	c.heartbeatOK.Store(false)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeClientsLocked()
}

func (c *Client) closeClientsLocked() {
	if c.cmd != nil {
		_ = c.cmd.Close()
	}
	if c.sub != nil {
		_ = c.sub.Close()
	}
	c.cmd = nil
	c.sub = nil
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
	if !c.Enabled() {
		return ErrDisabled
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	addr, ok := c.addrLocked()
	if !ok {
		return fmt.Errorf("home: invalid address (host=%q port=%d)", c.homeCfg.Host, c.homeCfg.Port)
	}

	if c.cmd == nil {
		options, errOptions := c.redisOptionsLocked(addr)
		if errOptions != nil {
			return errOptions
		}
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
	return &redis.Options{
		Addr:                  addr,
		TLSConfig:             tlsConfig,
		DialTimeout:           homeRedisOperationTimeout,
		ReadTimeout:           homeRedisOperationTimeout,
		WriteTimeout:          homeRedisOperationTimeout,
		MaxRetries:            -1,
		DialerRetries:         1,
		ContextTimeoutEnabled: true,
	}, nil
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
	if errEnsure := c.ensureClients(); errEnsure != nil {
		return nil, errEnsure
	}
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd == nil {
		return nil, ErrNotConnected
	}
	return cmd, nil
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
		Type:      "auth",
		Model:     requestedModel,
		Count:     count,
		SessionID: strings.TrimSpace(sessionID),
		Headers:   headersToLowerMap(headers),
	}
}

func (c *Client) RPopAuth(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int) ([]byte, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, errClient
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil, fmt.Errorf("home: requested model is empty")
	}
	req := newAuthDispatchRequest(requestedModel, sessionID, headers, count)
	keyBytes, err := json.Marshal(&req)
	if err != nil {
		return nil, err
	}

	raw, err := cmd.RPop(ctx, string(keyBytes)).Bytes()
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

// StartConfigSubscriber connects to home, fetches config once via GET config, then subscribes to
// the "config" channel to receive runtime config updates.
//
// The subscription connection is treated as the home heartbeat. HeartbeatOK is set to true only
// after the initial GET config succeeds and the SUBSCRIBE connection is established. When the
// subscription ends unexpectedly, HeartbeatOK becomes false and the loop reconnects.
func (c *Client) StartConfigSubscriber(ctx context.Context, onConfig func([]byte) error) {
	if c == nil {
		return
	}
	if !c.Enabled() {
		return
	}
	if onConfig == nil {
		return
	}

	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				c.heartbeatOK.Store(false)
				return
			default:
			}
		}

		c.heartbeatOK.Store(false)
		c.Close()

		if errEnsure := c.ensureClients(); errEnsure != nil {
			log.Warn("unable to connect to home control center, retrying in 1 second")
			c.markReconnectFailure("connect")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		if errPing := c.Ping(ctx); errPing != nil {
			log.Warn("unable to connect to home control center, retrying in 1 second")
			c.markReconnectFailure("ping")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		raw, errGet := c.GetConfig(ctx)
		if errGet != nil {
			log.Warn("unable to fetch config from home control center, retrying in 1 second")
			c.markReconnectFailure("config fetch")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}
		if errApply := onConfig(raw); errApply != nil {
			log.Warn("unable to apply config from home control center, retrying in 1 second")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		sub, errSubClient := c.subscriptionClient()
		if errSubClient != nil {
			c.markReconnectFailure("subscribe client")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		pubsub := sub.Subscribe(ctx, redisChannelConfig)
		if pubsub == nil {
			c.markReconnectFailure("subscribe")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		// Ensure the subscription is established before marking heartbeat OK.
		if _, errReceive := pubsub.ReceiveTimeout(ctx, homeSubscriptionReceiveTimeout); errReceive != nil {
			_ = pubsub.Close()
			c.markReconnectFailure("subscribe")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		c.resetReconnectFailures()
		c.heartbeatOK.Store(true)

		for {
			event, errMsg := pubsub.ReceiveTimeout(ctx, homeSubscriptionReceiveTimeout)
			if errMsg != nil {
				_ = pubsub.Close()
				c.heartbeatOK.Store(false)
				if isTimeoutError(errMsg) {
					c.markSubscriptionTimeout()
				} else {
					c.markReconnectFailure("subscription")
				}
				sleepWithContext(ctx, homeReconnectInterval)
				break
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
