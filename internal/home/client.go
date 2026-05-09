package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

	homeReconnectInterval = time.Second
)

var (
	ErrDisabled       = errors.New("home client disabled")
	ErrNotConnected   = errors.New("home not connected")
	ErrEmptyResponse  = errors.New("home returned empty response")
	ErrAuthNotFound   = errors.New("home auth not found")
	ErrConfigNotFound = errors.New("home config not found")
	ErrModelsNotFound = errors.New("home models not found")
)

type Client struct {
	homeCfg config.HomeConfig

	cmd *redis.Client
	sub *redis.Client

	heartbeatOK atomic.Bool
}

func New(homeCfg config.HomeConfig) *Client {
	return &Client{homeCfg: homeCfg}
}

func (c *Client) Enabled() bool {
	if c == nil {
		return false
	}
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
	addr, ok := c.addr()
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

func (c *Client) Ping(ctx context.Context) error {
	if err := c.ensureClients(); err != nil {
		return err
	}
	if c.cmd == nil {
		return ErrNotConnected
	}
	return c.cmd.Ping(ctx).Err()
}

func (c *Client) GetConfig(ctx context.Context) ([]byte, error) {
	if err := c.ensureClients(); err != nil {
		return nil, err
	}
	raw, err := c.cmd.Get(ctx, redisKeyConfig).Bytes()
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
	if err := c.ensureClients(); err != nil {
		return nil, err
	}
	raw, err := c.cmd.Get(ctx, redisKeyModels).Bytes()
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
	if err := c.ensureClients(); err != nil {
		return nil, err
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

	raw, err := c.cmd.RPop(ctx, string(keyBytes)).Bytes()
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
	if err := c.ensureClients(); err != nil {
		return nil, err
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

	raw, err := c.cmd.Get(ctx, string(keyBytes)).Bytes()
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
	if err := c.ensureClients(); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	return c.cmd.LPush(ctx, redisKeyUsage, payload).Err()
}

func (c *Client) RPushRequestLog(ctx context.Context, payload []byte) error {
	if err := c.ensureClients(); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	return c.cmd.RPush(ctx, redisKeyRequestLog, payload).Err()
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
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		if errPing := c.Ping(ctx); errPing != nil {
			log.Warn("unable to connect to home control center, retrying in 1 second")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		raw, errGet := c.GetConfig(ctx)
		if errGet != nil {
			log.Warn("unable to fetch config from home control center, retrying in 1 second")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}
		if errApply := onConfig(raw); errApply != nil {
			log.Warn("unable to apply config from home control center, retrying in 1 second")
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		if c.sub == nil {
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		pubsub := c.sub.Subscribe(ctx, redisChannelConfig)
		if pubsub == nil {
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		// Ensure the subscription is established before marking heartbeat OK.
		if _, errReceive := pubsub.Receive(ctx); errReceive != nil {
			_ = pubsub.Close()
			sleepWithContext(ctx, homeReconnectInterval)
			continue
		}

		c.heartbeatOK.Store(true)

		for {
			msg, errMsg := pubsub.ReceiveMessage(ctx)
			if errMsg != nil {
				_ = pubsub.Close()
				c.heartbeatOK.Store(false)
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
