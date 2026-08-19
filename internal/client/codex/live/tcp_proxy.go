package live

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/pion/ice/v4"
	"github.com/pion/sdp/v3"
	"github.com/pion/stun/v3"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

const (
	maxUpstreamICECandidates   = 64
	maxProxiedTCPCandidates    = 16
	maxUnauthenticatedTCPConns = 4
	maxInitialSTUNFrameSize    = 4096
	stunMessageHeaderSize      = 20
)

var nonRoutableProxyTargetPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

type iceCredentials struct {
	ufrag    string
	password string
}

type tcpCandidateTunnel struct {
	listener       net.Listener
	target         netip.AddrPort
	dialer         proxy.ContextDialer
	expectedUser   string
	remotePassword string

	mu                  sync.Mutex
	closed              bool
	claimed             bool
	connections         map[net.Conn]struct{}
	validationSlots     chan struct{}
	onForwardingStarted func()
	ctx                 context.Context
	cancel              context.CancelFunc
}

type tcpCandidatePlan struct {
	mediaIndex     int
	attributeIndex int
	fields         []string
	target         netip.AddrPort
}

func prepareProxiedUpstreamAnswer(answer, localOffer string, dialer proxy.ContextDialer) (string, []*tcpCandidateTunnel, error) {
	if dialer == nil {
		return "", nil, errors.New("Codex live TCP proxy dialer is unavailable")
	}
	var remoteDescription sdp.SessionDescription
	if errUnmarshal := remoteDescription.UnmarshalString(answer); errUnmarshal != nil {
		return "", nil, fmt.Errorf("parse upstream WebRTC answer for TCP proxy: %w", errUnmarshal)
	}
	var localDescription sdp.SessionDescription
	if errUnmarshal := localDescription.UnmarshalString(localOffer); errUnmarshal != nil {
		return "", nil, fmt.Errorf("parse upstream WebRTC offer for TCP proxy: %w", errUnmarshal)
	}
	remoteCredentials, errCredentials := bundledICECredentials(&remoteDescription)
	if errCredentials != nil {
		return "", nil, fmt.Errorf("read upstream WebRTC answer ICE credentials: %w", errCredentials)
	}
	localCredentials, errCredentials := bundledICECredentials(&localDescription)
	if errCredentials != nil {
		return "", nil, fmt.Errorf("read upstream WebRTC offer ICE credentials: %w", errCredentials)
	}

	plans := make([]tcpCandidatePlan, 0, 4)
	candidateCount := 0
	for mediaIndex, media := range remoteDescription.MediaDescriptions {
		if media == nil {
			continue
		}
		filtered := make([]sdp.Attribute, 0, len(media.Attributes))
		for attributeIndex := range media.Attributes {
			attribute := media.Attributes[attributeIndex]
			if !attribute.IsICECandidate() {
				filtered = append(filtered, attribute)
				continue
			}
			candidateCount++
			if candidateCount > maxUpstreamICECandidates {
				return "", nil, fmt.Errorf("upstream WebRTC answer exceeds the %d candidate limit", maxUpstreamICECandidates)
			}
			plan, keep, errCandidate := proxiedTCPCandidatePlan(attribute.Value)
			if errCandidate != nil {
				return "", nil, errCandidate
			}
			if !keep {
				continue
			}
			if len(plans) >= maxProxiedTCPCandidates {
				return "", nil, fmt.Errorf("upstream WebRTC answer exceeds the %d TCP candidate proxy limit", maxProxiedTCPCandidates)
			}
			plan.mediaIndex = mediaIndex
			plan.attributeIndex = len(filtered)
			filtered = append(filtered, attribute)
			plans = append(plans, plan)
		}
		media.Attributes = filtered
	}
	if len(plans) == 0 {
		return "", nil, errors.New("upstream WebRTC answer has no supported public TCP passive candidate on port 443")
	}

	expectedUser := remoteCredentials.ufrag + ":" + localCredentials.ufrag
	tunnels := make([]*tcpCandidateTunnel, 0, len(plans))
	closeTunnels := func() {
		for _, tunnel := range tunnels {
			if errClose := tunnel.Close(); errClose != nil {
				log.WithError(errClose).Debug("codex live TCP proxy: close candidate tunnel after setup error")
			}
		}
	}
	for _, plan := range plans {
		tunnel, errTunnel := newTCPCandidateTunnel(plan.target, dialer, expectedUser, remoteCredentials.password)
		if errTunnel != nil {
			closeTunnels()
			return "", nil, errTunnel
		}
		tunnels = append(tunnels, tunnel)
		listenerAddress, ok := tunnel.listener.Addr().(*net.TCPAddr)
		if !ok || listenerAddress.IP == nil {
			closeTunnels()
			return "", nil, errors.New("Codex live TCP proxy listener returned an invalid address")
		}
		fields := append([]string(nil), plan.fields...)
		fields[4] = listenerAddress.IP.String()
		fields[5] = strconv.Itoa(listenerAddress.Port)
		remoteDescription.MediaDescriptions[plan.mediaIndex].Attributes[plan.attributeIndex].Value = strings.Join(fields, " ")
	}

	rewritten, errMarshal := remoteDescription.Marshal()
	if errMarshal != nil {
		closeTunnels()
		return "", nil, fmt.Errorf("marshal proxied upstream WebRTC answer: %w", errMarshal)
	}
	return string(rewritten), tunnels, nil
}

func proxiedTCPCandidatePlan(rawCandidate string) (tcpCandidatePlan, bool, error) {
	trimmed := strings.TrimSpace(rawCandidate)
	candidate, errCandidate := ice.UnmarshalCandidate(trimmed)
	if errCandidate != nil {
		return tcpCandidatePlan{}, false, fmt.Errorf("parse upstream WebRTC candidate: %w", errCandidate)
	}
	if candidate.NetworkType() != ice.NetworkTypeTCP4 && candidate.NetworkType() != ice.NetworkTypeTCP6 {
		return tcpCandidatePlan{}, false, nil
	}
	if candidate.TCPType() != ice.TCPTypePassive {
		return tcpCandidatePlan{}, false, nil
	}
	if candidate.Component() != uint16(ice.ComponentRTP) || candidate.Type() != ice.CandidateTypeHost {
		return tcpCandidatePlan{}, false, nil
	}
	if candidate.Port() != 443 {
		return tcpCandidatePlan{}, false, fmt.Errorf("upstream WebRTC TCP proxy candidate uses disallowed port %d", candidate.Port())
	}
	address, errAddress := netip.ParseAddr(candidate.Address())
	if errAddress != nil {
		return tcpCandidatePlan{}, false, errors.New("upstream WebRTC TCP proxy candidate address must be an IP")
	}
	address = address.Unmap()
	if !isPublicProxyTarget(address) {
		return tcpCandidatePlan{}, false, errors.New("upstream WebRTC TCP proxy candidate address must be globally routable")
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 8 {
		return tcpCandidatePlan{}, false, errors.New("upstream WebRTC TCP proxy candidate is malformed")
	}
	return tcpCandidatePlan{
		fields: fields,
		target: netip.AddrPortFrom(address, uint16(candidate.Port())),
	}, true, nil
}

func isPublicProxyTarget(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsUnspecified() || address.IsLoopback() ||
		address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	for _, prefix := range nonRoutableProxyTargetPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func bundledICECredentials(description *sdp.SessionDescription) (iceCredentials, error) {
	if description == nil {
		return iceCredentials{}, errors.New("SDP is unavailable")
	}
	sessionUfrag, _ := description.Attribute("ice-ufrag")
	sessionPassword, _ := description.Attribute("ice-pwd")
	var selected iceCredentials
	for _, media := range description.MediaDescriptions {
		if media == nil {
			continue
		}
		ufrag := sessionUfrag
		if mediaUfrag, ok := media.Attribute("ice-ufrag"); ok {
			ufrag = mediaUfrag
		}
		password := sessionPassword
		if mediaPassword, ok := media.Attribute("ice-pwd"); ok {
			password = mediaPassword
		}
		ufrag = strings.TrimSpace(ufrag)
		password = strings.TrimSpace(password)
		if ufrag == "" && password == "" {
			continue
		}
		if ufrag == "" || password == "" {
			return iceCredentials{}, errors.New("SDP contains incomplete ICE credentials")
		}
		current := iceCredentials{ufrag: ufrag, password: password}
		if selected.ufrag == "" {
			selected = current
			continue
		}
		if selected != current {
			return iceCredentials{}, errors.New("SDP contains inconsistent bundled ICE credentials")
		}
	}
	if selected.ufrag == "" {
		selected = iceCredentials{ufrag: strings.TrimSpace(sessionUfrag), password: strings.TrimSpace(sessionPassword)}
	}
	if selected.ufrag == "" || selected.password == "" {
		return iceCredentials{}, errors.New("SDP is missing ICE credentials")
	}
	return selected, nil
}

func closeCandidateTunnels(tunnels []*tcpCandidateTunnel) error {
	var closeErrors []error
	for _, tunnel := range tunnels {
		if errClose := tunnel.Close(); errClose != nil {
			closeErrors = append(closeErrors, errClose)
		}
	}
	return errors.Join(closeErrors...)
}

func newTCPCandidateTunnel(target netip.AddrPort, dialer proxy.ContextDialer, expectedUser, remotePassword string) (*tcpCandidateTunnel, error) {
	if !isPublicProxyTarget(target.Addr()) || target.Port() != 443 {
		return nil, errors.New("Codex live TCP proxy target is not allowed")
	}
	if dialer == nil || strings.TrimSpace(expectedUser) == "" || strings.TrimSpace(remotePassword) == "" {
		return nil, errors.New("Codex live TCP proxy tunnel configuration is incomplete")
	}
	network := "tcp4"
	listenAddress := "127.0.0.1:0"
	if target.Addr().Is6() {
		network = "tcp6"
		listenAddress = "[::1]:0"
	}
	listener, errListen := net.Listen(network, listenAddress)
	if errListen != nil {
		return nil, fmt.Errorf("listen for Codex live TCP proxy candidate: %w", errListen)
	}
	tunnelContext, cancelTunnel := context.WithCancel(context.Background())
	tunnel := &tcpCandidateTunnel{
		listener:        listener,
		target:          target,
		dialer:          dialer,
		expectedUser:    expectedUser,
		remotePassword:  remotePassword,
		connections:     make(map[net.Conn]struct{}),
		validationSlots: make(chan struct{}, maxUnauthenticatedTCPConns),
		ctx:             tunnelContext,
		cancel:          cancelTunnel,
	}
	go tunnel.accept()
	return tunnel, nil
}

func (t *tcpCandidateTunnel) accept() {
	for {
		connection, errAccept := t.listener.Accept()
		if errAccept != nil {
			if !errors.Is(errAccept, net.ErrClosed) {
				log.WithError(errAccept).Warn("codex live TCP proxy: accept candidate connection failed")
			}
			return
		}
		if !t.trackConnection(connection) {
			if errClose := connection.Close(); errClose != nil {
				log.WithError(errClose).Debug("codex live TCP proxy: close connection after tunnel shutdown")
			}
			return
		}
		select {
		case t.validationSlots <- struct{}{}:
			go func() {
				defer func() { <-t.validationSlots }()
				t.handleConnection(connection)
			}()
		default:
			t.untrackAndClose(connection)
			log.Warn("codex live TCP proxy: rejected excess unauthenticated candidate connection")
		}
	}
}

func (t *tcpCandidateTunnel) handleConnection(client net.Conn) {
	firstFrame, errValidate := readValidatedICEBindingFrame(client, t.expectedUser, t.remotePassword)
	if errValidate != nil {
		t.untrackAndClose(client)
		log.WithError(errValidate).Warn("codex live TCP proxy: rejected unauthenticated candidate connection")
		return
	}
	if !t.claim() {
		t.untrackAndClose(client)
		return
	}
	if errClose := t.listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
		log.WithError(errClose).Debug("codex live TCP proxy: close claimed candidate listener")
	}
	upstream, errDial := t.dialer.DialContext(t.ctx, "tcp", t.target.String())
	if errDial != nil {
		t.untrackAndClose(client)
		log.WithError(errDial).Warn("codex live TCP proxy: connect fixed upstream candidate failed")
		return
	}
	if !t.trackConnection(upstream) {
		if errClose := upstream.Close(); errClose != nil {
			log.WithError(errClose).Debug("codex live TCP proxy: close upstream after tunnel shutdown")
		}
		t.untrackAndClose(client)
		return
	}
	if errWrite := writeAll(upstream, firstFrame); errWrite != nil {
		t.untrackAndClose(upstream)
		t.untrackAndClose(client)
		log.WithError(errWrite).Warn("codex live TCP proxy: forward authenticated ICE frame failed")
		return
	}
	t.notifyForwardingStarted()

	copyDone := make(chan struct{}, 2)
	copyConnection := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		copyDone <- struct{}{}
	}
	go copyConnection(upstream, client)
	go copyConnection(client, upstream)
	<-copyDone
	t.untrackAndClose(upstream)
	t.untrackAndClose(client)
	<-copyDone
}

func (t *tcpCandidateTunnel) setForwardingStartedHandler(handler func()) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.onForwardingStarted = handler
	t.mu.Unlock()
}

func (t *tcpCandidateTunnel) notifyForwardingStarted() {
	if t == nil {
		return
	}
	t.mu.Lock()
	handler := t.onForwardingStarted
	t.mu.Unlock()
	if handler != nil {
		handler()
	}
}

func (t *tcpCandidateTunnel) trackConnection(connection net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.connections[connection] = struct{}{}
	return true
}

func (t *tcpCandidateTunnel) untrackAndClose(connection net.Conn) {
	if connection == nil {
		return
	}
	t.mu.Lock()
	delete(t.connections, connection)
	t.mu.Unlock()
	if errClose := connection.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
		log.WithError(errClose).Debug("codex live TCP proxy: close tunnel connection")
	}
}

func (t *tcpCandidateTunnel) claim() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.claimed {
		return false
	}
	t.claimed = true
	return true
}

func (t *tcpCandidateTunnel) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	cancel := t.cancel
	connections := make([]net.Conn, 0, len(t.connections))
	for connection := range t.connections {
		connections = append(connections, connection)
	}
	t.connections = make(map[net.Conn]struct{})
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	var closeErrors []error
	if t.listener != nil {
		if errClose := t.listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			closeErrors = append(closeErrors, errClose)
		}
	}
	for _, connection := range connections {
		if errClose := connection.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			closeErrors = append(closeErrors, errClose)
		}
	}
	return errors.Join(closeErrors...)
}

func readValidatedICEBindingFrame(connection io.Reader, expectedUser, remotePassword string) ([]byte, error) {
	var header [2]byte
	if _, errRead := io.ReadFull(connection, header[:]); errRead != nil {
		return nil, fmt.Errorf("read ICE-TCP frame header: %w", errRead)
	}
	frameSize := int(binary.BigEndian.Uint16(header[:]))
	if frameSize < stunMessageHeaderSize || frameSize > maxInitialSTUNFrameSize {
		return nil, fmt.Errorf("invalid initial ICE-TCP STUN frame size %d", frameSize)
	}
	payload := make([]byte, frameSize)
	if _, errRead := io.ReadFull(connection, payload); errRead != nil {
		return nil, fmt.Errorf("read ICE-TCP STUN frame: %w", errRead)
	}
	message := stun.NewWithOptions(stun.WithStrict(true))
	if errDecode := stun.Decode(payload, message); errDecode != nil {
		return nil, fmt.Errorf("decode initial ICE-TCP STUN message: %w", errDecode)
	}
	if len(payload) != stunMessageHeaderSize+int(message.Length) {
		return nil, errors.New("initial ICE-TCP STUN message contains trailing data")
	}
	if message.Type != stun.BindingRequest {
		return nil, fmt.Errorf("initial ICE-TCP STUN message has unexpected type %s", message.Type)
	}
	var username stun.Username
	if errUsername := username.GetFrom(message); errUsername != nil {
		return nil, fmt.Errorf("read initial ICE-TCP STUN username: %w", errUsername)
	}
	if string(username) != expectedUser {
		return nil, errors.New("initial ICE-TCP STUN username does not match the media session")
	}
	if errIntegrity := stun.NewShortTermIntegrity(remotePassword).Check(message); errIntegrity != nil {
		return nil, fmt.Errorf("verify initial ICE-TCP STUN integrity: %w", errIntegrity)
	}
	if errFingerprint := stun.Fingerprint.Check(message); errFingerprint != nil {
		return nil, fmt.Errorf("verify initial ICE-TCP STUN fingerprint: %w", errFingerprint)
	}
	frame := make([]byte, len(header)+len(payload))
	copy(frame, header[:])
	copy(frame[len(header):], payload)
	return frame, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, errWrite := writer.Write(data)
		if errWrite != nil {
			return errWrite
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func proxyScheme(rawProxyURL string) string {
	trimmed := strings.TrimSpace(rawProxyURL)
	if index := strings.Index(trimmed, "://"); index > 0 {
		return strings.ToLower(trimmed[:index])
	}
	return "proxy"
}
