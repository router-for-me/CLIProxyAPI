package home

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
)

const redisChannelRebalance = "rebalance"

// ErrRebalanceRequested asks the supervisor to reconnect without draining safe active executions.
var ErrRebalanceRequested = errors.New("home cluster rebalance requested")

type rebalanceInstruction struct {
	ID     string      `json:"id"`
	Term   int64       `json:"term"`
	Target clusterNode `json:"target"`
}

type clusterBalanceState struct {
	moving           bool
	returning        bool
	target           clusterNode
	origin           clusterNode
	request          rebalanceInstruction
	result           string
	registered       bool
	unsupported      bool
	latencySupported bool
}

func (c *Client) canExecuteRebalanceLocked() bool {
	return c.managed && c.clusterDiscoveryEnabledLocked() && !c.legacyMembership &&
		c.lifecycle.LifecycleConfigRevision > 0 && c.heartbeatOK.Load() &&
		!c.dispatchFenced.Load() && !c.ambiguousDispatch.Load() &&
		recoveryState(c.recoveryState.Load()) == recoveryStateStable && !c.balance.moving
}

// rebalanceCommand owns a cancellable transport independent of issued dispatches.
func (c *Client) rebalanceCommand(ctx context.Context, address string, args ...any) (string, error) {
	if errContext := ctx.Err(); errContext != nil {
		return "", errContext
	}
	c.mu.Lock()
	if address == "" {
		address, _ = c.addrLocked()
	}
	options, errOptions := c.redisOptionsLocked(address)
	c.mu.Unlock()
	if errOptions != nil {
		return "", errOptions
	}
	options.ReadTimeout = -1
	options.WriteTimeout = -1
	baseDialer := redis.NewDialer(options)
	options.Dialer = func(dialCtx context.Context, network, dialAddress string) (net.Conn, error) {
		conn, errDial := baseDialer(dialCtx, network, dialAddress)
		if errDial != nil {
			return nil, errDial
		}
		if ctx.Err() != nil {
			_ = closeUnderlyingTransport(conn)
			return nil, context.Cause(ctx)
		}
		return newPluginSyncCancelableConn(ctx, conn), nil
	}
	command := redis.NewClient(options)
	stopCancel := context.AfterFunc(ctx, func() { _ = command.Close() })
	defer func() {
		stopCancel()
		if errClose := command.Close(); errClose != nil && ctx.Err() == nil {
			log.Debugf("Home rebalance transport close failed: %v", errClose)
		}
	}()
	raw, errCommand := command.Do(ctx, args...).Text()
	if errCommand != nil {
		return "", fmt.Errorf("Home rebalance %v: %w", args[0], errCommand)
	}
	return raw, nil
}

func (c *Client) syncClusterBalance(ctx context.Context) error {
	c.mu.Lock()
	eligible := c.canExecuteRebalanceLocked() && !c.balance.unsupported
	registered, request, result := c.balance.registered, c.balance.request, c.balance.result
	c.mu.Unlock()
	if !eligible {
		return nil
	}
	if !registered {
		raw, errEnable := c.rebalanceCommand(ctx, "", "CLUSTER", "BALANCE", "ENABLE")
		if errEnable != nil {
			message := strings.ToLower(errEnable.Error())
			if isHomeCommandUnsupported(errEnable) || strings.Contains(message, "unsupported subcommand") || strings.Contains(message, "rebalance unsupported") {
				// Legacy Home has no capability code; only an explicit unsupported reply disables balancing.
				c.mu.Lock()
				c.balance.unsupported = true
				c.mu.Unlock()
				return nil
			}
			return fmt.Errorf("register Home rebalance: %w", errEnable)
		}
		var response struct {
			OK           bool     `json:"ok"`
			Capabilities []string `json:"capabilities"`
		}
		if errJSON := json.Unmarshal([]byte(raw), &response); errJSON != nil {
			return fmt.Errorf("decode Home rebalance ENABLE response: %w", errJSON)
		}
		if !response.OK {
			return nil
		}
		c.mu.Lock()
		c.balance.registered = true
		for _, capability := range response.Capabilities {
			if capability == clusterLatencyCapability {
				c.balance.latencySupported = true
			}
		}
		c.mu.Unlock()
	}
	if result == "" {
		return nil
	}

	raw, errResult := c.rebalanceCommand(ctx, "", "CLUSTER", "BALANCE", "RESULT", request.ID, result)
	if errResult != nil {
		return fmt.Errorf("report Home rebalance result: %w", errResult)
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if errJSON := json.Unmarshal([]byte(raw), &response); errJSON != nil {
		return fmt.Errorf("decode Home rebalance RESULT response: %w", errJSON)
	}
	if !response.OK {
		return nil
	}
	c.mu.Lock()
	if c.balance.request.ID == request.ID && c.balance.result == result {
		c.balance.result = ""
		c.balance.request = rebalanceInstruction{}
	}
	c.mu.Unlock()
	return nil
}

// executeClusterRebalance claims an exact persisted plan before touching any transport.
// This lifetime keeps its original address; only the supervisor's replacement uses the target.
func (c *Client) executeClusterRebalance(ctx context.Context, payload []byte) error {
	var request rebalanceInstruction
	if errJSON := json.Unmarshal(payload, &request); errJSON != nil {
		return fmt.Errorf("decode Home rebalance instruction: %w", errJSON)
	}
	request.Target.IP = strings.TrimSpace(request.Target.IP)
	if request.ID == "" || request.Term <= 0 || request.Target.IP == "" || request.Target.Port <= 0 || request.Target.Port > 65535 {
		return nil
	}
	c.mu.Lock()
	eligible := c.canExecuteRebalanceLocked() && c.balance.registered && c.balance.result == "" &&
		(request.Target.IP != strings.TrimSpace(c.homeCfg.Host) || request.Target.Port != c.homeCfg.Port)
	c.mu.Unlock()
	if !eligible || ctx.Err() != nil {
		return nil
	}
	raw, errClaim := c.rebalanceCommand(ctx, "", "CLUSTER", "BALANCE", "CLAIM", request.ID, request.Term, request.Target.IP, request.Target.Port)
	if errClaim != nil {
		return errClaim
	}
	var response struct {
		OK bool `json:"ok"`
	}
	if errJSON := json.Unmarshal([]byte(raw), &response); errJSON != nil {
		return fmt.Errorf("decode Home rebalance CLAIM response: %w", errJSON)
	}
	if !response.OK {
		return nil
	}
	c.mu.Lock()
	c.balance.request = request
	c.balance.result = "failed"
	c.mu.Unlock()
	if _, errPing := c.rebalanceCommand(ctx, clusterNodeAddress(request.Target), "PING"); errPing != nil {
		if errReport := c.syncClusterBalance(ctx); errReport != nil {
			return errors.Join(errPing, errReport)
		}
		c.mu.Lock()
		recovered := ctx.Err() == nil && c.canExecuteRebalanceLocked() && c.balance.result == ""
		c.mu.Unlock()
		if !recovered {
			return errPing
		}
		// No switch occurred. An acknowledged failure and a healthy source safely end this plan, not the subscription.
		log.Warnf("Home rebalance preflight failed; failure reported, keeping source: plan=%q target=%q error=%v", request.ID, clusterNodeAddress(request.Target), errPing)
		return nil
	}
	c.mu.Lock()
	if ctx.Err() != nil || !c.canExecuteRebalanceLocked() {
		c.mu.Unlock()
		return c.syncClusterBalance(ctx)
	}
	c.balance.origin = clusterNode{IP: strings.TrimSpace(c.homeCfg.Host), Port: c.homeCfg.Port}
	c.balance.target = request.Target
	c.balance.result = ""
	c.balance.moving = true
	c.markMembershipTakeoverEligible()
	origin := c.balance.origin
	c.mu.Unlock()
	log.Infof("executing Home rebalance plan: plan=%q from=%q to=%q", request.ID, clusterNodeAddress(origin), clusterNodeAddress(request.Target))
	return ErrRebalanceRequested
}

func (c *Client) finishClusterRebalance(ctx context.Context) error {
	c.mu.Lock()
	if c.balance.moving {
		c.balance.moving = false
		c.balance.result = "success"
	}
	c.balance.returning = false
	c.mu.Unlock()
	return c.syncClusterBalance(ctx)
}

func (c *Client) failClusterRebalance() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.balance.moving {
		return
	}
	c.balance.moving = false
	c.balance.returning = true
	c.balance.result = "failed"
	c.switchToNodeLocked(c.balance.origin)
}

// chooseClusterNodeLocked only chooses bootstrap/fault-recovery targets, never active migrations.
func (c *Client) chooseClusterNodeLocked(nodes []clusterNode, excludeCurrent bool, draw float64) (clusterNode, bool) {
	var choices []clusterNode
	for _, node := range nodes {
		if node.IP == "" || node.Port <= 0 || node.Port > 65535 {
			continue
		}
		if excludeCurrent && node.IP == strings.TrimSpace(c.homeCfg.Host) && node.Port == c.homeCfg.Port {
			continue
		}
		if len(choices) == 0 || node.ClientCount < choices[0].ClientCount {
			choices = []clusterNode{node}
		} else if node.ClientCount == choices[0].ClientCount {
			choices = append(choices, node)
		}
	}
	if len(choices) == 0 {
		return clusterNode{}, false
	}
	return choices[int(draw*float64(len(choices)))], true
}

func clusterNodeAddress(node clusterNode) string {
	return net.JoinHostPort(node.IP, strconv.Itoa(node.Port))
}
