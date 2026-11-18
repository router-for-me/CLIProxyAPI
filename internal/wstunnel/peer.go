package wstunnel

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

const (
	defaultSendQueueSize        = 256
	defaultPingInterval         = 30 * time.Second
	defaultMaxPeerQueuedBytes   = int64(MaxInboundFrameBytes)
	defaultMaxTenantQueuedBytes = int64(256 << 20)
	// closeGracePeriod bounds local socket reclamation, not network I/O.
	closeGracePeriod = time.Second
	// protocolErrorCloseGrace bounds how long the busy-writer protocol-error
	// path waits for writeLoop to emit the protocol-error close frame before
	// reclaiming the transport and unblocking the stalled writer.
	protocolErrorCloseGrace = 100 * time.Millisecond
)

type outboundFrame struct {
	messageType    int
	data           []byte
	accountedBytes int64
}

type peer struct {
	hub        *Hub
	tenant     *realm
	apiKey     string
	uuid       string
	name       string
	serverType string
	role       Role

	conn          *websocket.Conn
	sendCh        chan outboundFrame
	pingCh        chan struct{}
	pongCh        chan string
	closed        chan struct{}
	done          chan struct{}
	writerDone    chan struct{}
	closeOnce     sync.Once
	transportOnce sync.Once
	closePayload  []byte
	// writerFinished closes when writeLoop stops performing writes (before it
	// reclaims the transport and reports writerDone); it lets finishClose wait
	// for the close frame to be attempted without watching the full teardown.
	writerFinished   chan struct{}
	writerFinishOnce sync.Once
	writeMu          sync.Mutex
	enqueueMu        sync.Mutex
	enqueueClosed    bool
	maxQueuedBytes   int64
	queuedBytes      atomic.Int64
}

func newPeer(hub *Hub, apiKey, uuid, name, serverType string, role Role, conn *websocket.Conn, queueSize int, maxQueuedBytes int64) *peer {
	if queueSize <= 0 {
		queueSize = defaultSendQueueSize
	}
	if maxQueuedBytes <= 0 {
		maxQueuedBytes = defaultMaxPeerQueuedBytes
	}
	p := &peer{
		hub:            hub,
		apiKey:         apiKey,
		uuid:           uuid,
		name:           name,
		serverType:     serverType,
		role:           role,
		conn:           conn,
		sendCh:         make(chan outboundFrame, queueSize),
		pingCh:         make(chan struct{}, 1),
		pongCh:         make(chan string, 1),
		closed:         make(chan struct{}),
		done:           make(chan struct{}),
		writerDone:     make(chan struct{}),
		writerFinished: make(chan struct{}),
		maxQueuedBytes: maxQueuedBytes,
	}
	if guardedConn, okGuarded := conn.UnderlyingConn().(*deadlineGuardConn); okGuarded {
		// Inspect before Gorilla sees fatal bytes, even while its write token is held.
		guardedConn.onProtocolError = func() {
			// Holding the idle writer lock prevents a new data/control write from
			// starting between this check and the close request. Never wait for it.
			idleWriter := p.writeMu.TryLock()
			p.close(websocket.CloseProtocolError, "", true)
			if idleWriter {
				p.writeMu.Unlock()
			} else {
				// A malformed peer must not hold an active writer indefinitely; the
				// short bounded wait lets writeLoop emit the close frame first.
				go p.finishClose(protocolErrorCloseGrace)
			}
			if hub != nil && hub.testBeforeProtocolErrorReturn != nil {
				hub.testBeforeProtocolErrorReturn()
			}
		}
	}
	conn.SetPingHandler(func(payload string) error {
		p.requestPong(payload)
		return nil
	})
	conn.SetCloseHandler(func(code int, _ string) error {
		p.close(code, "", true)
		return nil
	})
	return p
}

func tryReserveBudget(counter *atomic.Int64, delta, limit int64) bool {
	if delta <= 0 {
		return true
	}
	for {
		cur := counter.Load()
		if delta > limit-cur {
			return false
		}
		if counter.CompareAndSwap(cur, cur+delta) {
			return true
		}
	}
}

func (p *peer) releaseBytes(n int64) {
	if p == nil || n <= 0 {
		return
	}
	p.queuedBytes.Add(-n)
	if p.tenant != nil && p.tenant.queuedBytes.Add(-n) == 0 && p.hub != nil {
		select {
		case <-p.closed:
			// Only a closed peer can retain in-flight bytes after unregistration.
			p.hub.pruneRealm(p.apiKey, p.tenant)
		default:
			// Unregistration prunes open peers after queue draining, without
			// inverting the Hub-to-enqueue lock order during enqueue rollback.
		}
	}
}

func (p *peer) tryEnqueueControl(frame outboundFrame) bool {
	return p.tryEnqueue(frame)
}

func (p *peer) tryEnqueueData(rawFrame []byte) bool {
	return p.tryEnqueue(outboundFrame{messageType: websocket.BinaryMessage, data: rawFrame})
}

func (p *peer) tryEnqueue(frame outboundFrame) bool {
	if p == nil {
		return false
	}
	p.enqueueMu.Lock()
	defer p.enqueueMu.Unlock()
	if p.enqueueClosed {
		return false
	}
	size := int64(len(frame.data))
	if !tryReserveBudget(&p.queuedBytes, size, p.maxQueuedBytes) {
		return false
	}
	if p.tenant != nil && !tryReserveBudget(&p.tenant.queuedBytes, size, p.hub.maxTenantQueuedBytes) {
		p.queuedBytes.Add(-size)
		return false
	}
	frame.accountedBytes = size
	select {
	case p.sendCh <- frame:
		return true
	default:
		p.releaseBytes(size)
		return false
	}
}

func (p *peer) shutdownAndDrainQueue() {
	if p == nil {
		return
	}
	p.enqueueMu.Lock()
	defer p.enqueueMu.Unlock()
	p.enqueueClosed = true
	for {
		select {
		case rem := <-p.sendCh:
			p.releaseBytes(rem.accountedBytes)
		default:
			return
		}
	}
}

func (p *peer) sendError(code, message, targetUUID, requestID string) {
	if p == nil {
		return
	}
	raw, errEncode := EncodeErrorFrame(code, message, targetUUID, requestID)
	if errEncode != nil {
		return
	}
	_ = p.tryEnqueueControl(outboundFrame{
		messageType: websocket.TextMessage,
		data:        raw,
	})
}

func (p *peer) requestPing() {
	if p == nil {
		return
	}
	select {
	case <-p.closed:
		return
	default:
	}
	select {
	case p.pingCh <- struct{}{}:
	default:
	}
}

func (p *peer) requestPong(payload string) {
	p.enqueueMu.Lock()
	defer p.enqueueMu.Unlock()
	if p.enqueueClosed || len(payload) > 125 {
		return
	}
	// Coalesce pending pings to the latest payload without blocking the reader.
	select {
	case <-p.pongCh:
	default:
	}
	select {
	case p.pongCh <- payload:
	default:
	}
}

func (p *peer) writeFrame(frame outboundFrame) error {
	defer p.releaseBytes(frame.accountedBytes)
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	select {
	case <-p.closed:
		return websocket.ErrCloseSent
	default:
	}
	return p.conn.WriteMessage(frame.messageType, frame.data)
}

func (p *peer) writeControl(messageType int, payload []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	select {
	case <-p.closed:
		return websocket.ErrCloseSent
	default:
	}
	return p.conn.WriteControl(messageType, payload, time.Time{})
}

func (p *peer) readLoop() {
	defer p.close(0, "", true)
	for {
		select {
		case <-p.closed:
			return
		default:
		}
		msgType, reader, errNextReader := p.conn.NextReader()
		if errNextReader != nil {
			return
		}
		limit := MaxInboundFrameBytes
		reason := "binary frame exceeds maximum allowed size"
		if msgType == websocket.TextMessage {
			limit = MaxControlFrameBytes
			reason = "control frame exceeds 4096 bytes"
		}
		// Bound reassembled messages without Gorilla writing a close from this reader.
		raw, errRead := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
		if len(raw) > limit {
			p.close(websocket.CloseMessageTooBig, reason, true)
			return
		}
		if errRead != nil {
			return
		}
		select {
		case <-p.closed:
			return
		default:
		}
		switch msgType {
		case websocket.TextMessage:
			p.hub.handleControlMessage(p, raw)
		case websocket.BinaryMessage:
			p.hub.handleDataMessage(p, raw)
		}
	}
}

func (p *peer) writeLoop(pingInterval time.Duration) {
	defer func() {
		p.close(0, "", true)
		p.writerFinishOnce.Do(func() { close(p.writerFinished) })
		p.finishClose(closeGracePeriod)
		close(p.writerDone)
	}()

	var tickerCh <-chan time.Time
	if pingInterval > 0 {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		tickerCh = ticker.C
	}

	for {
		// A pending close always takes priority over queued data and pings.
		select {
		case <-p.closed:
			// A non-nil empty payload still requests a Close frame without a status code.
			if p.closePayload != nil {
				p.writeMu.Lock()
				_ = p.conn.WriteControl(websocket.CloseMessage, p.closePayload, time.Time{})
				p.writeMu.Unlock()
			}
			return
		default:
		}
		var errWrite error
		select {
		case <-p.closed:
			continue
		case frame := <-p.sendCh:
			errWrite = p.writeFrame(frame)
		case <-tickerCh:
			errWrite = p.writeControl(websocket.PingMessage, nil)
		case <-p.pingCh:
			errWrite = p.writeControl(websocket.PingMessage, nil)
		case payload := <-p.pongCh:
			errWrite = p.writeControl(websocket.PongMessage, []byte(payload))
		}
		if errWrite != nil && errWrite != websocket.ErrCloseSent {
			return
		}
	}
}

// finishClose reclaims the transport after giving writeLoop a grace window to
// finish, which lets a queued close frame be written first. writerFinished and
// writerDone both close once writeLoop exits; a permanently blocked writer
// holds writeMu and stalls that exit, so the wait is capped by grace and
// reclaiming the socket then unblocks the stalled writer. A zero grace closes
// the transport immediately.
func (p *peer) finishClose(grace time.Duration) {
	p.transportOnce.Do(func() {
		if grace > 0 && p.closePayload != nil {
			timer := time.NewTimer(grace)
			defer timer.Stop()
			select {
			case <-p.writerFinished:
			case <-p.writerDone:
			case <-timer.C:
			}
		}
		if p.conn != nil {
			// The WebSocket close frame is sufficient; never block on TLS close_notify.
			if conn := rawTransportConn(p.conn.UnderlyingConn()); conn != nil {
				if errClose := conn.Close(); errClose != nil {
					log.Debugf("wstunnel: socket close error: %v", errClose)
				}
			}
		}
		close(p.done)
	})
}

func (p *peer) awaitClose() {
	// This timer only forces reclamation of an already closing local socket.
	// No read/write deadlines are installed on an upgraded connection.
	timer := time.NewTimer(closeGracePeriod)
	defer timer.Stop()
	forceClose := timer.C
	if p.hub != nil && p.hub.testForceClose != nil {
		forceClose = p.hub.testForceClose
	}
	select {
	case <-p.done:
	case <-forceClose:
		p.finishClose(0)
	}
}

func (p *peer) close(closeCode int, reason string, notifyRealm bool) {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		if closeCode > 0 {
			p.closePayload = websocket.FormatCloseMessage(closeCode, reason)
		}
		p.shutdownAndDrainQueue()
		close(p.closed)
		if p.hub != nil {
			p.hub.unregisterPeer(p, notifyRealm)
		}
		if closeCode == 0 || p.conn == nil {
			go p.finishClose(closeGracePeriod)
		} else {
			// Only writeLoop writes the close frame; callers never perform socket I/O.
			go p.awaitClose()
		}
	})
}
