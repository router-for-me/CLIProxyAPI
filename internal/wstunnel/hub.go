package wstunnel

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Options configures a WebSocket tunnel Hub.
type Options struct {
	// SendQueueSize sets the per-peer outbound frame buffer capacity (default 256).
	SendQueueSize int
	// MaxPeerQueuedBytes caps all outbound bytes queued/in-flight for a single peer (default 64 MiB + 512).
	MaxPeerQueuedBytes int64
	// MaxTenantQueuedBytes caps all outbound bytes queued/in-flight per API-key tenant.
	// It defaults to the larger of 256 MiB and MaxPeerQueuedBytes.
	MaxTenantQueuedBytes int64
	// PingInterval sets the periodic WebSocket Ping interval (default 30s).
	PingInterval time.Duration
	// PingTrigger optionally triggers immediate WebSocket Ping frames across active peers.
	PingTrigger <-chan struct{}
}

// uuidReservation binds a handshake to its original tenant generation.
type uuidReservation struct {
	realm *realm
}

type realm struct {
	reserved    map[string]*uuidReservation
	peers       map[string]*peer
	queuedBytes atomic.Int64
}

func newRealm() *realm {
	return &realm{
		reserved: make(map[string]*uuidReservation),
		peers:    make(map[string]*peer),
	}
}

func (r *realm) isEmpty() bool {
	return r == nil || (len(r.reserved) == 0 && len(r.peers) == 0 && r.queuedBytes.Load() == 0)
}

func (r *realm) sortedServers() []ServerInfo {
	if r == nil || len(r.peers) == 0 {
		return nil
	}
	servers := make([]ServerInfo, 0)
	for _, p := range r.peers {
		if p.role == RoleServer {
			servers = append(servers, ServerInfo{
				UUID: p.uuid,
				Name: p.name,
				Type: p.serverType,
			})
		}
	}
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].UUID < servers[j].UUID
	})
	return servers
}

// Hub coordinates multi-tenant WebSocket tunnel realms isolated by access.api-keys.
type Hub struct {
	mu          sync.RWMutex
	closed      bool
	allowedKeys map[string]struct{}
	realms      map[string]*realm

	sendQueueSize        int
	maxPeerQueuedBytes   int64
	maxTenantQueuedBytes int64
	pingInterval         time.Duration
	stopCh               chan struct{}
	stopOnce             sync.Once
	upgrader             websocket.Upgrader

	testForceClose                <-chan time.Time
	testOnAfterReserve            func()
	testOnPeerUnregistered        func(apiKey, uuid string)
	testBeforeListServersEnqueue  func()
	testBeforeProtocolErrorReturn func()
}

// NewHub constructs an initialized WebSocket tunnel Hub.
func NewHub(opts Options) *Hub {
	queueSize := opts.SendQueueSize
	if queueSize <= 0 {
		queueSize = defaultSendQueueSize
	}
	maxPeerBytes := opts.MaxPeerQueuedBytes
	if maxPeerBytes <= 0 {
		maxPeerBytes = defaultMaxPeerQueuedBytes
	}
	maxTenantBytes := opts.MaxTenantQueuedBytes
	if maxTenantBytes <= 0 {
		maxTenantBytes = defaultMaxTenantQueuedBytes
		if maxTenantBytes < maxPeerBytes {
			maxTenantBytes = maxPeerBytes
		}
	}
	pingInterval := opts.PingInterval
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}

	h := &Hub{
		allowedKeys:          make(map[string]struct{}),
		realms:               make(map[string]*realm),
		sendQueueSize:        queueSize,
		maxPeerQueuedBytes:   maxPeerBytes,
		maxTenantQueuedBytes: maxTenantBytes,
		pingInterval:         pingInterval,
		stopCh:               make(chan struct{}),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(*http.Request) bool {
				return true
			},
		},
	}

	if opts.PingTrigger != nil {
		go h.watchPingTrigger(opts.PingTrigger)
	}
	return h
}

func (h *Hub) watchPingTrigger(trigger <-chan struct{}) {
	for {
		select {
		case <-h.stopCh:
			return
		case _, ok := <-trigger:
			if !ok {
				return
			}
			h.broadcastPing()
		}
	}
}

func (h *Hub) broadcastPing() {
	h.mu.RLock()
	var targets []*peer
	for _, rlm := range h.realms {
		for _, p := range rlm.peers {
			targets = append(targets, p)
		}
	}
	h.mu.RUnlock()

	for _, p := range targets {
		p.requestPing()
	}
}

// UpdateAllowedAPIKeys atomically updates the allowed access.api-keys set and
// immediately disconnects all peers belonging to revoked keys.
func (h *Hub) UpdateAllowedAPIKeys(keys []string) {
	if h == nil {
		return
	}
	normalized := make(map[string]struct{}, len(keys))
	for _, raw := range keys {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		normalized[trimmed] = struct{}{}
	}

	var evicted []*peer
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.allowedKeys = normalized
	for apiKey, rlm := range h.realms {
		if _, ok := normalized[apiKey]; ok {
			continue
		}
		for _, p := range rlm.peers {
			evicted = append(evicted, p)
		}
		clear(rlm.reserved)
		clear(rlm.peers)
		if rlm.isEmpty() {
			delete(h.realms, apiKey)
		}
	}
	h.mu.Unlock()

	for _, p := range evicted {
		p.close(websocket.ClosePolicyViolation, "api key revoked", false)
	}
}

// Close shuts down the Hub and closes all active tunnel connections.
func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() {
		close(h.stopCh)

		var evicted []*peer
		h.mu.Lock()
		h.closed = true
		for _, rlm := range h.realms {
			for _, p := range rlm.peers {
				evicted = append(evicted, p)
			}
		}
		h.realms = make(map[string]*realm)
		h.allowedKeys = make(map[string]struct{})
		h.mu.Unlock()

		for _, p := range evicted {
			p.close(websocket.CloseGoingAway, "server shutting down", false)
		}
	})
}

func (h *Hub) reserveUUID(apiKey, uuid string) (*uuidReservation, int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, http.StatusServiceUnavailable
	}
	if _, ok := h.allowedKeys[apiKey]; !ok {
		return nil, http.StatusUnauthorized
	}
	rlm := h.realms[apiKey]
	if rlm == nil {
		rlm = newRealm()
		h.realms[apiKey] = rlm
	}
	if _, exists := rlm.peers[uuid]; exists {
		return nil, http.StatusConflict
	}
	if _, reserved := rlm.reserved[uuid]; reserved {
		return nil, http.StatusConflict
	}
	reservation := &uuidReservation{realm: rlm}
	rlm.reserved[uuid] = reservation
	return reservation, 0
}

func (h *Hub) releaseReservation(apiKey, uuid string, reservation *uuidReservation) {
	h.mu.Lock()
	defer h.mu.Unlock()

	rlm := h.realms[apiKey]
	if rlm == nil || reservation == nil || rlm.reserved[uuid] != reservation {
		return
	}
	delete(rlm.reserved, uuid)
	if rlm.isEmpty() {
		delete(h.realms, apiKey)
	}
}

func (h *Hub) pruneRealm(apiKey string, rlm *realm) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.realms[apiKey] == rlm && rlm.isEmpty() {
		delete(h.realms, apiKey)
	}
}

func (h *Hub) commitPeer(p *peer, reservation *uuidReservation) bool {
	var overflowClients []*peer

	h.mu.Lock()
	rlm := h.realms[p.apiKey]
	reservationValid := rlm != nil && reservation != nil && reservation.realm == rlm && rlm.reserved[p.uuid] == reservation
	if reservationValid {
		delete(rlm.reserved, p.uuid)
	}
	_, keyAllowed := h.allowedKeys[p.apiKey]
	if h.closed || !keyAllowed || !reservationValid {
		closeCode := websocket.ClosePolicyViolation
		reason := "api key revoked"
		if h.closed {
			closeCode = websocket.CloseGoingAway
			reason = "server shutting down"
		}
		if rlm != nil && rlm.isEmpty() {
			delete(h.realms, p.apiKey)
		}
		h.mu.Unlock()

		go p.writeLoop(0)
		p.close(closeCode, reason, false)
		return false
	}

	p.tenant = rlm
	if p.role == RoleClient {
		rawList, errEncode := EncodeServerListFrame("", rlm.sortedServers())
		if errEncode == nil {
			if !p.tryEnqueueControl(outboundFrame{
				messageType: websocket.TextMessage,
				data:        rawList,
			}) {
				overflowClients = append(overflowClients, p)
			}
		}
		rlm.peers[p.uuid] = p
	} else {
		rlm.peers[p.uuid] = p
		rawOnline, errEncode := EncodeServerEventFrame(ControlTypeServerOnline, ServerInfo{
			UUID: p.uuid,
			Name: p.name,
			Type: p.serverType,
		})
		if errEncode == nil {
			frame := outboundFrame{
				messageType: websocket.TextMessage,
				data:        rawOnline,
			}
			for _, member := range rlm.peers {
				if member.role != RoleClient {
					continue
				}
				if !member.tryEnqueueControl(frame) {
					overflowClients = append(overflowClients, member)
				}
			}
		}
	}
	h.mu.Unlock()

	for _, slow := range overflowClients {
		slow.close(websocket.ClosePolicyViolation, "control queue overflow", true)
	}

	go p.writeLoop(h.pingInterval)
	go p.readLoop()
	return true
}

func (h *Hub) unregisterPeer(p *peer, notifyRealm bool) {
	if h == nil || p == nil {
		return
	}

	var (
		removed         bool
		overflowClients []*peer
	)

	h.mu.Lock()
	rlm := h.realms[p.apiKey]
	if rlm != nil {
		if existing := rlm.peers[p.uuid]; existing == p {
			delete(rlm.peers, p.uuid)
			removed = true
			if notifyRealm && p.role == RoleServer && !h.closed {
				rawOffline, errEncode := EncodeServerEventFrame(ControlTypeServerOffline, ServerInfo{
					UUID: p.uuid,
					Name: p.name,
					Type: p.serverType,
				})
				if errEncode == nil {
					frame := outboundFrame{
						messageType: websocket.TextMessage,
						data:        rawOffline,
					}
					for _, member := range rlm.peers {
						if member.role != RoleClient {
							continue
						}
						if !member.tryEnqueueControl(frame) {
							overflowClients = append(overflowClients, member)
						}
					}
				}
			}
		}
		if rlm.isEmpty() {
			delete(h.realms, p.apiKey)
		}
	}
	onUnregistered := h.testOnPeerUnregistered
	h.mu.Unlock()

	for _, slow := range overflowClients {
		slow.close(websocket.ClosePolicyViolation, "control queue overflow", true)
	}
	if removed && onUnregistered != nil {
		onUnregistered(p.apiKey, p.uuid)
	}
}

func (h *Hub) handleControlMessage(sender *peer, raw []byte) {
	req, errCode, errParse := ParseControlFrame(raw)
	if errParse != nil {
		sender.sendError(errCode, errParse.Error(), "", "")
		return
	}

	switch req.Type {
	case ControlTypeListServers:
		if sender.role != RoleClient {
			sender.sendError(ErrCodeUnsupportedRoleAction, "only client peers can request server_list", "", req.RequestID)
			return
		}
		// Hold RLock across snapshot + enqueue so list_servers cannot overtake concurrent server_online/server_offline broadcasts.
		h.mu.RLock()
		rlm := h.realms[sender.apiKey]
		if rlm == nil || rlm.peers[sender.uuid] != sender {
			h.mu.RUnlock()
			return
		}
		servers := rlm.sortedServers()
		rawList, errEncode := EncodeServerListFrame(req.RequestID, servers)
		if errEncode != nil {
			h.mu.RUnlock()
			return
		}
		if hook := h.testBeforeListServersEnqueue; hook != nil && req.RequestID == "race-snapshot" {
			hook()
		}
		enqueued := sender.tryEnqueueControl(outboundFrame{
			messageType: websocket.TextMessage,
			data:        rawList,
		})
		h.mu.RUnlock()
		if !enqueued {
			sender.close(websocket.ClosePolicyViolation, "control queue overflow", true)
		}
	}
}

func (h *Hub) handleDataMessage(sender *peer, rawFrame []byte) {
	view, errCode, errParse := ParseDataFrameHeader(rawFrame)
	if errParse != nil {
		sender.sendError(errCode, errParse.Error(), "", "")
		return
	}
	if view.SrcUUID != sender.uuid {
		sender.sendError(ErrCodeSourceUUIDMismatch, "frame source uuid does not match authenticated device uuid", "", "")
		return
	}

	h.mu.RLock()
	rlm := h.realms[sender.apiKey]
	if rlm == nil || rlm.peers[sender.uuid] != sender {
		h.mu.RUnlock()
		return
	}
	target := rlm.peers[view.DstUUID]
	if target == nil {
		h.mu.RUnlock()
		sender.sendError(ErrCodeTargetNotFound, "target peer is not connected", view.DstUUID, "")
		return
	}
	if target.role == sender.role {
		h.mu.RUnlock()
		sender.sendError(ErrCodeInvalidTargetRole, "cannot route data between peers of the same role", view.DstUUID, "")
		return
	}

	// Keep membership validation and enqueue atomic with respect to revocation.
	enqueued := target.tryEnqueueData(rawFrame)
	h.mu.RUnlock()
	if !enqueued {
		sender.sendError(ErrCodeTargetBufferFull, "target peer send buffer is full", view.DstUUID, "")
	}
}
