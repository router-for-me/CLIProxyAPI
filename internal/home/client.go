package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	redisKeyConfig     = "config"
	redisChannelConfig = "config"
	redisKeyModels     = "models"
	redisKeyUsage      = "usage"
	redisKeyRequestLog = "request-log"

	homeReconnectInterval          = time.Second
	homeReconnectFailoverThreshold = 3
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
	return fmt.Sprintf("%s:%d", host, c.homeCfg.Port), true
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
		c.cmd = redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: c.homeCfg.Password,
		})
	}
	if c.sub == nil {
		c.sub = redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: c.homeCfg.Password,
		})
	}
	return nil
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

func (c *Client) refreshBestClusterNode(ctx context.Context) {
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

	var envelope clusterNodesEnvelope
	if errUnmarshal := json.Unmarshal([]byte(raw), &envelope); errUnmarshal != nil {
		return false, errUnmarshal
	}
	nodes := normalizeClusterNodes(envelope.Nodes)
	if len(nodes) == 0 {
		return false, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusterNodes = nodes
	c.reconnectFailures = 0
	return c.switchToNodeLocked(nodes[0]), nil
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

	c.reconnectFailures++
	if c.reconnectFailures < homeReconnectFailoverThreshold {
		return false, ""
	}
	c.reconnectFailures = 0

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

func (c *Client) GetModels(ctx context.Context) ([]byte, error) {
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return nil, errClient
	}
	raw, err := cmd.Get(ctx, redisKeyModels).Bytes()
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
		if _, errReceive := pubsub.Receive(ctx); errReceive != nil {
			_ = pubsub.Close()
			c.markReconnectFailure("subscribe")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		c.resetReconnectFailures()
		c.heartbeatOK.Store(true)

		for {
			msg, errMsg := pubsub.ReceiveMessage(ctx)
			if errMsg != nil {
				_ = pubsub.Close()
				c.heartbeatOK.Store(false)
				c.markReconnectFailure("subscription")
				sleepWithContext(ctx, homeReconnectInterval)
				break
			}
			if msg == nil {
				continue
			}
			if payload := strings.TrimSpace(msg.Payload); payload != "" {
				if errApply := onConfig([]byte(payload)); errApply != nil {
					log.Warn("failed to apply config update from home control center, ignoring")
				}
			}
		}
	}
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
