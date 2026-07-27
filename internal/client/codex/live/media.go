package live

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

const (
	realtimeDataChannelLabel = "oai-events"
	mediaDataQueueSize       = 64
	mediaDataMessageMaxSize  = 256 << 10
	mediaDataBufferedMaxSize = 1 << 20
)

var opusCodec = webrtc.RTPCodecCapability{
	MimeType:    webrtc.MimeTypeOpus,
	ClockRate:   48000,
	Channels:    2,
	SDPFmtpLine: "minptime=10;useinbandfec=1",
}

type mediaRelaySession interface {
	AcceptUpstreamAnswer(context.Context, string) (string, error)
	SetCallID(string)
	SetCloseHandler(func(string))
	Close() error
	CloseWithReason(string) error
}

type mediaRelayFactory interface {
	NewSession(context.Context, string, mediaSessionRoute) (mediaRelaySession, string, error)
}

type mediaSessionRoute struct {
	proxyURL   string
	credential string
	authIndex  string
}

type pionMediaRelay struct {
	downstreamAPI    *webrtc.API
	upstreamAPI      *webrtc.API
	proxyUpstreamAPI *webrtc.API
	configuration    webrtc.Configuration
	limiter          *mediaSessionLimiter
}

type mediaSessionLimiter struct {
	mu     sync.Mutex
	limit  int
	active int
}

type pionMediaSession struct {
	downstream *webrtc.PeerConnection
	upstream   *webrtc.PeerConnection
	bridge     *dataChannelBridge

	done           chan struct{}
	closeOnce      sync.Once
	closeErr       error
	failureOnce    sync.Once
	handlerMu      sync.Mutex
	onClose        func(string)
	failureReason  string
	handlerCalled  bool
	mediaSessionID string
	callID         string
	releaseSlot    func()

	proxyDialer       proxy.ContextDialer
	proxyScheme       string
	credential        string
	authIndex         string
	forwardingLogOnce sync.Once
	localOffer        string
	tunnelsMu         sync.Mutex
	tunnels           []*tcpCandidateTunnel
}

type dataChannelMessage struct {
	data     []byte
	isString bool
}

type dataChannelPipe struct {
	name        string
	done        <-chan struct{}
	queue       chan dataChannelMessage
	ready       chan struct{}
	readyOnce   sync.Once
	writable    chan struct{}
	destination *webrtc.DataChannel
	mu          sync.RWMutex
	onError     func(error)
}

type dataChannelBridge struct {
	done         <-chan struct{}
	downToUp     *dataChannelPipe
	upToDown     *dataChannelPipe
	closeOnce    sync.Once
	downstreamMu sync.Mutex
	downstream   *webrtc.DataChannel
	upstreamMu   sync.Mutex
	upstream     *webrtc.DataChannel
}

func newPionMediaRelay(relayConfig config.CodexLiveMediaRelayConfig) (*pionMediaRelay, error) {
	return newPionMediaRelayWithLimiter(relayConfig, &mediaSessionLimiter{})
}

func newPionMediaRelayWithLimiter(relayConfig config.CodexLiveMediaRelayConfig, limiter *mediaSessionLimiter) (*pionMediaRelay, error) {
	if errValidate := relayConfig.Validate(); errValidate != nil {
		return nil, errValidate
	}
	downstreamAPI, errAPI := newPionAPI(relayConfig, relayConfig.DisablePrivateRemoteIPs)
	if errAPI != nil {
		return nil, errAPI
	}
	upstreamAPI, errAPI := newPionAPI(relayConfig, false)
	if errAPI != nil {
		return nil, errAPI
	}
	proxyUpstreamAPI, errAPI := newPionProxyAPI(relayConfig)
	if errAPI != nil {
		return nil, errAPI
	}
	iceServers := make([]webrtc.ICEServer, 0, len(relayConfig.ICEServers))
	for _, server := range relayConfig.ICEServers {
		urls := make([]string, 0, len(server.URLs))
		for _, rawURL := range server.URLs {
			urls = append(urls, strings.TrimSpace(rawURL))
		}
		iceServers = append(iceServers, webrtc.ICEServer{
			URLs:           urls,
			Username:       server.Username,
			Credential:     server.Credential,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	}
	if limiter == nil {
		limiter = &mediaSessionLimiter{}
	}
	limiter.setLimit(relayConfig.EffectiveMaxSessions())
	return &pionMediaRelay{
		downstreamAPI:    downstreamAPI,
		upstreamAPI:      upstreamAPI,
		proxyUpstreamAPI: proxyUpstreamAPI,
		configuration:    webrtc.Configuration{ICEServers: iceServers},
		limiter:          limiter,
	}, nil
}

func (l *mediaSessionLimiter) setLimit(limit int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.limit = limit
	l.mu.Unlock()
}

func (l *mediaSessionLimiter) acquire() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limit <= 0 || l.active >= l.limit {
		return false
	}
	l.active++
	return true
}

func (l *mediaSessionLimiter) release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.mu.Unlock()
}

func newPionAPI(relayConfig config.CodexLiveMediaRelayConfig, filterPrivateRemoteIPs bool) (*webrtc.API, error) {
	return newPionAPIWithOptions(relayConfig, filterPrivateRemoteIPs, false)
}

func newPionProxyAPI(relayConfig config.CodexLiveMediaRelayConfig) (*webrtc.API, error) {
	return newPionAPIWithOptions(relayConfig, false, true)
}

func newPionAPIWithOptions(relayConfig config.CodexLiveMediaRelayConfig, filterPrivateRemoteIPs, loopbackOnly bool) (*webrtc.API, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if errRegister := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: opusCodec,
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); errRegister != nil {
		return nil, fmt.Errorf("register Opus codec: %w", errRegister)
	}
	interceptorRegistry := &interceptor.Registry{}
	if errRegister := webrtc.RegisterDefaultInterceptors(mediaEngine, interceptorRegistry); errRegister != nil {
		return nil, fmt.Errorf("register WebRTC interceptors: %w", errRegister)
	}
	settingEngine := webrtc.SettingEngine{}
	if !loopbackOnly {
		if relayConfig.UDPPortMin != 0 {
			if errPorts := settingEngine.SetEphemeralUDPPortRange(relayConfig.UDPPortMin, relayConfig.UDPPortMax); errPorts != nil {
				return nil, fmt.Errorf("configure WebRTC UDP port range: %w", errPorts)
			}
		}
		if publicIP := strings.TrimSpace(relayConfig.PublicIP); publicIP != "" {
			settingEngine.SetNAT1To1IPs([]string{publicIP}, webrtc.ICECandidateTypeHost)
		}
	}
	if filterPrivateRemoteIPs {
		settingEngine.SetRemoteIPFilter(isPublicRemoteIP)
	}
	if loopbackOnly {
		settingEngine.SetNetworkTypes([]webrtc.NetworkType{
			webrtc.NetworkTypeUDP4,
			webrtc.NetworkTypeUDP6,
			webrtc.NetworkTypeTCP4,
			webrtc.NetworkTypeTCP6,
		})
		settingEngine.SetIncludeLoopbackCandidate(true)
		settingEngine.SetIPFilter(func(ip net.IP) bool {
			return ip != nil && ip.IsLoopback()
		})
	}
	return webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(interceptorRegistry),
		webrtc.WithSettingEngine(settingEngine),
	), nil
}

func isPublicRemoteIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsPrivate() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

func (r *pionMediaRelay) NewSession(ctx context.Context, clientOffer string, route mediaSessionRoute) (mediaRelaySession, string, error) {
	if r == nil || r.downstreamAPI == nil || r.upstreamAPI == nil || r.proxyUpstreamAPI == nil || r.limiter == nil {
		return nil, "", errors.New("Codex live media relay unavailable")
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, "", errContext
	}
	builtProxyDialer, proxyMode, errProxy := proxyutil.BuildDialer(route.proxyURL)
	if errProxy != nil {
		return nil, "", fmt.Errorf("configure Codex live remote TCP proxy: %w", errProxy)
	}
	proxied := proxyMode == proxyutil.ModeProxy
	var proxyDialer proxy.ContextDialer
	if proxied {
		contextDialer, ok := builtProxyDialer.(proxy.ContextDialer)
		if !ok {
			return nil, "", errors.New("Codex live remote TCP proxy does not support cancellation")
		}
		proxyDialer = contextDialer
	}
	if !r.limiter.acquire() {
		return nil, "", errors.New("Codex live media relay capacity exhausted")
	}
	releaseSlot := r.limiter.release
	downstream, errDownstream := r.downstreamAPI.NewPeerConnection(r.configuration)
	if errDownstream != nil {
		releaseSlot()
		return nil, "", fmt.Errorf("create downstream PeerConnection: %w", errDownstream)
	}
	upstreamAPI := r.upstreamAPI
	upstreamConfiguration := r.configuration
	if proxied {
		upstreamAPI = r.proxyUpstreamAPI
		upstreamConfiguration.ICEServers = nil
	}
	upstream, errUpstream := upstreamAPI.NewPeerConnection(upstreamConfiguration)
	if errUpstream != nil {
		releaseSlot()
		if errClose := downstream.Close(); errClose != nil {
			log.WithError(errClose).Debug("codex live media: close downstream PeerConnection after setup error")
		}
		return nil, "", fmt.Errorf("create upstream PeerConnection: %w", errUpstream)
	}

	session := &pionMediaSession{
		downstream:     downstream,
		upstream:       upstream,
		done:           make(chan struct{}),
		mediaSessionID: uuid.NewString(),
		releaseSlot:    releaseSlot,
		proxyDialer:    proxyDialer,
		proxyScheme:    proxyScheme(route.proxyURL),
		credential:     strings.TrimSpace(route.credential),
		authIndex:      strings.TrimSpace(route.authIndex),
	}
	session.bridge = newDataChannelBridge(session.done, func(err error) {
		session.fail("data_channel_failed", err)
	})
	session.installStateHandlers()
	log.WithFields(session.logFields("session")).Info("codex live WebRTC media session created")

	if errRemote := downstream.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  clientOffer,
	}); errRemote != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("set downstream WebRTC offer: %w", errRemote)
	}

	toDesktop, errTrack := webrtc.NewTrackLocalStaticRTP(opusCodec, "audio", "codex-live")
	if errTrack != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("create downstream audio track: %w", errTrack)
	}
	downstreamSender, errTrack := downstream.AddTrack(toDesktop)
	if errTrack != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("add downstream audio track: %w", errTrack)
	}
	go drainRTCP("downstream", downstreamSender, session.done)

	toOpenAI, errTrack := webrtc.NewTrackLocalStaticRTP(opusCodec, "audio", "codex-live")
	if errTrack != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("create upstream audio track: %w", errTrack)
	}
	upstreamSender, errTrack := upstream.AddTrack(toOpenAI)
	if errTrack != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("add upstream audio track: %w", errTrack)
	}
	go drainRTCP("upstream", upstreamSender, session.done)

	downstream.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeOpus) {
			return
		}
		go relayRTP("downstream-to-upstream", track, toOpenAI, session.done)
	})
	upstream.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeOpus) {
			return
		}
		go relayRTP("upstream-to-downstream", track, toDesktop, session.done)
	})
	downstream.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() != realtimeDataChannelLabel {
			if errClose := channel.Close(); errClose != nil {
				log.WithError(errClose).Debug("codex live media: close unsupported downstream DataChannel")
			}
			return
		}
		session.bridge.attachDownstream(channel)
	})
	upstreamChannel, errChannel := upstream.CreateDataChannel(realtimeDataChannelLabel, nil)
	if errChannel != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("create upstream DataChannel: %w", errChannel)
	}
	session.bridge.attachUpstream(upstreamChannel)

	gatherComplete := webrtc.GatheringCompletePromise(upstream)
	offer, errOffer := upstream.CreateOffer(nil)
	if errOffer != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("create upstream WebRTC offer: %w", errOffer)
	}
	if errLocal := upstream.SetLocalDescription(offer); errLocal != nil {
		_ = session.Close()
		return nil, "", fmt.Errorf("set upstream WebRTC offer: %w", errLocal)
	}
	select {
	case <-gatherComplete:
	case <-ctx.Done():
		_ = session.Close()
		return nil, "", fmt.Errorf("gather upstream WebRTC candidates: %w", ctx.Err())
	}
	localDescription := upstream.LocalDescription()
	if localDescription == nil || strings.TrimSpace(localDescription.SDP) == "" {
		_ = session.Close()
		return nil, "", errors.New("upstream WebRTC offer is empty")
	}
	session.localOffer = localDescription.SDP
	return session, localDescription.SDP, nil
}

func (s *pionMediaSession) AcceptUpstreamAnswer(ctx context.Context, upstreamAnswer string) (string, error) {
	if s == nil || s.upstream == nil || s.downstream == nil {
		return "", errors.New("Codex live media session unavailable")
	}
	answerToApply := upstreamAnswer
	if s.proxyDialer != nil {
		rewrittenAnswer, tunnels, errProxy := prepareProxiedUpstreamAnswer(upstreamAnswer, s.localOffer, s.proxyDialer)
		if errProxy != nil {
			return "", errProxy
		}
		for _, tunnel := range tunnels {
			tunnel.setForwardingStartedHandler(s.logForwardingStarted)
		}
		if !s.installCandidateTunnels(tunnels) {
			errClosed := errors.New("Codex live media session closed while configuring TCP proxy")
			if errClose := closeCandidateTunnels(tunnels); errClose != nil {
				return "", errors.Join(errClosed, fmt.Errorf("close TCP candidate tunnels: %w", errClose))
			}
			return "", errClosed
		}
		answerToApply = rewrittenAnswer
	}
	if errRemote := s.upstream.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerToApply,
	}); errRemote != nil {
		errSetRemote := fmt.Errorf("set upstream WebRTC answer: %w", errRemote)
		if errClose := s.closeCandidateTunnels(); errClose != nil {
			return "", errors.Join(errSetRemote, fmt.Errorf("close TCP candidate tunnels: %w", errClose))
		}
		return "", errSetRemote
	}
	gatherComplete := webrtc.GatheringCompletePromise(s.downstream)
	answer, errAnswer := s.downstream.CreateAnswer(nil)
	if errAnswer != nil {
		return "", fmt.Errorf("create downstream WebRTC answer: %w", errAnswer)
	}
	if errLocal := s.downstream.SetLocalDescription(answer); errLocal != nil {
		return "", fmt.Errorf("set downstream WebRTC answer: %w", errLocal)
	}
	select {
	case <-gatherComplete:
	case <-ctx.Done():
		return "", fmt.Errorf("gather downstream WebRTC candidates: %w", ctx.Err())
	}
	localDescription := s.downstream.LocalDescription()
	if localDescription == nil || strings.TrimSpace(localDescription.SDP) == "" {
		return "", errors.New("downstream WebRTC answer is empty")
	}
	return localDescription.SDP, nil
}

func (s *pionMediaSession) installCandidateTunnels(tunnels []*tcpCandidateTunnel) bool {
	if s == nil {
		return false
	}
	s.tunnelsMu.Lock()
	defer s.tunnelsMu.Unlock()
	select {
	case <-s.done:
		return false
	default:
	}
	s.tunnels = tunnels
	return true
}

func (s *pionMediaSession) closeCandidateTunnels() error {
	if s == nil {
		return nil
	}
	s.tunnelsMu.Lock()
	tunnels := s.tunnels
	s.tunnels = nil
	s.tunnelsMu.Unlock()
	return closeCandidateTunnels(tunnels)
}

func (s *pionMediaSession) SetCallID(callID string) {
	if s == nil {
		return
	}
	s.handlerMu.Lock()
	s.callID = strings.TrimSpace(callID)
	s.handlerMu.Unlock()
}

func (s *pionMediaSession) logFields(peer string) log.Fields {
	fields := log.Fields{
		"media_session_id": s.mediaSessionID,
		"peer":             peer,
	}
	s.handlerMu.Lock()
	callID := s.callID
	s.handlerMu.Unlock()
	if callID != "" {
		fields["call_id"] = callID
	}
	if s.proxyDialer != nil && (peer == "remote" || peer == "session") {
		fields["remote_transport"] = "tcp"
		fields["proxy_scheme"] = s.proxyScheme
	}
	return fields
}

func (s *pionMediaSession) forwardingLogFields() log.Fields {
	fields := s.logFields("remote")
	if s.authIndex != "" {
		fields["auth_index"] = s.authIndex
	}
	if s.credential != "" {
		fields["credential"] = s.credential
	}
	if s.proxyDialer != nil {
		fields["connection"] = "via " + s.proxyScheme + " proxy"
		fields["remote_transport"] = "tcp"
	} else {
		fields["connection"] = "direct"
		fields["remote_transport"] = "ice"
	}
	if s.upstream != nil {
		fields["state"] = s.upstream.ConnectionState().String()
	}
	return fields
}

func (s *pionMediaSession) logForwardingStarted() {
	if s == nil {
		return
	}
	s.forwardingLogOnce.Do(func() {
		log.WithFields(s.forwardingLogFields()).Info("codex live remote media forwarding started")
	})
}

func (s *pionMediaSession) SetCloseHandler(handler func(string)) {
	if s == nil {
		return
	}
	s.handlerMu.Lock()
	s.onClose = handler
	reason := s.failureReason
	callHandler := handler != nil && reason != "" && !s.handlerCalled
	if callHandler {
		s.handlerCalled = true
	}
	s.handlerMu.Unlock()
	if callHandler {
		handler(reason)
	}
}

func (s *pionMediaSession) Close() error {
	return s.CloseWithReason("closed")
}

func (s *pionMediaSession) CloseWithReason(reason string) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		fields := s.logFields("session")
		fields["reason"] = reason
		log.WithFields(fields).Info("codex live WebRTC media session closing")
		close(s.done)
		if s.bridge != nil {
			s.bridge.close()
		}
		var closeErrors []error
		if errClose := s.closeCandidateTunnels(); errClose != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close TCP candidate tunnels: %w", errClose))
		}
		if errClose := s.closePeerConnection("local", s.downstream); errClose != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close downstream PeerConnection: %w", errClose))
		}
		if errClose := s.closePeerConnection("remote", s.upstream); errClose != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close upstream PeerConnection: %w", errClose))
		}
		if s.releaseSlot != nil {
			s.releaseSlot()
		}
		s.closeErr = errors.Join(closeErrors...)
		if s.closeErr != nil {
			log.WithFields(fields).WithError(s.closeErr).Warn("codex live WebRTC media session closed with errors")
		} else {
			log.WithFields(fields).Info("codex live WebRTC media session closed")
		}
	})
	return s.closeErr
}

func (s *pionMediaSession) closePeerConnection(peer string, connection *webrtc.PeerConnection) error {
	if connection == nil {
		return nil
	}
	fields := s.logFields(peer)
	fields["state_before"] = connection.ConnectionState().String()
	errClose := connection.Close()
	fields["state_after"] = connection.ConnectionState().String()
	if errClose != nil {
		log.WithFields(fields).WithError(errClose).Warn("codex live WebRTC peer close failed")
		return errClose
	}
	log.WithFields(fields).Info("codex live WebRTC peer closed")
	return nil
}

func (s *pionMediaSession) installStateHandlers() {
	handle := func(peer, reasonPrefix string) func(webrtc.PeerConnectionState) {
		return func(state webrtc.PeerConnectionState) {
			fields := s.logFields(peer)
			fields["state"] = state.String()
			switch state {
			case webrtc.PeerConnectionStateConnecting:
				log.WithFields(fields).Info("codex live WebRTC peer connecting")
			case webrtc.PeerConnectionStateConnected:
				log.WithFields(fields).Info("codex live WebRTC peer connected")
				if peer == "remote" {
					s.logForwardingStarted()
				}
			case webrtc.PeerConnectionStateDisconnected:
				log.WithFields(fields).Warn("codex live WebRTC peer disconnected")
			case webrtc.PeerConnectionStateFailed:
				log.WithFields(fields).Warn("codex live WebRTC peer failed")
				s.fail(reasonPrefix+"_failed", fmt.Errorf("%s PeerConnection failed", reasonPrefix))
			case webrtc.PeerConnectionStateClosed:
				select {
				case <-s.done:
					return
				default:
					log.WithFields(fields).Info("codex live WebRTC peer closed by remote")
					s.fail(reasonPrefix+"_closed", fmt.Errorf("%s PeerConnection closed", reasonPrefix))
				}
			default:
				log.WithFields(fields).Debug("codex live WebRTC peer state changed")
			}
		}
	}
	s.downstream.OnConnectionStateChange(handle("local", "downstream"))
	s.upstream.OnConnectionStateChange(handle("remote", "upstream"))
}

func (s *pionMediaSession) fail(reason string, err error) {
	s.failureOnce.Do(func() {
		if err != nil {
			log.WithFields(s.logFields("session")).WithField("reason", reason).WithError(err).Warn("codex live WebRTC media session failed")
		}
		if errClose := s.CloseWithReason(reason); errClose != nil {
			log.WithError(errClose).Debug("codex live media: close failed session")
		}
		s.handlerMu.Lock()
		s.failureReason = reason
		handler := s.onClose
		callHandler := handler != nil && !s.handlerCalled
		if callHandler {
			s.handlerCalled = true
		}
		s.handlerMu.Unlock()
		if callHandler {
			handler(reason)
		}
	})
}

func relayRTP(name string, source *webrtc.TrackRemote, destination *webrtc.TrackLocalStaticRTP, done <-chan struct{}) {
	for {
		packet, _, errRead := source.ReadRTP()
		if errRead != nil {
			if !isClosedMediaError(errRead, done) {
				log.WithError(errRead).Debugf("codex live media: %s RTP read stopped", name)
			}
			return
		}
		normalizeRTPPacket(packet)
		if errWrite := destination.WriteRTP(packet); errWrite != nil {
			if !isClosedMediaError(errWrite, done) {
				log.WithError(errWrite).Debugf("codex live media: %s RTP write stopped", name)
			}
			return
		}
	}
}

func normalizeRTPPacket(packet *rtp.Packet) {
	if packet == nil {
		return
	}
	packet.Extension = false
	packet.ExtensionProfile = 0
	packet.Extensions = nil
}

func drainRTCP(name string, sender *webrtc.RTPSender, done <-chan struct{}) {
	for {
		if _, _, errRead := sender.ReadRTCP(); errRead != nil {
			if !isClosedMediaError(errRead, done) {
				log.WithError(errRead).Debugf("codex live media: %s RTCP reader stopped", name)
			}
			return
		}
	}
}

func isClosedMediaError(err error, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
	}
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

func newDataChannelBridge(done <-chan struct{}, onError func(error)) *dataChannelBridge {
	bridge := &dataChannelBridge{done: done}
	bridge.downToUp = newDataChannelPipe("downstream-to-upstream", done, onError)
	bridge.upToDown = newDataChannelPipe("upstream-to-downstream", done, onError)
	return bridge
}

func newDataChannelPipe(name string, done <-chan struct{}, onError func(error)) *dataChannelPipe {
	pipe := &dataChannelPipe{
		name:     name,
		done:     done,
		queue:    make(chan dataChannelMessage, mediaDataQueueSize),
		ready:    make(chan struct{}),
		writable: make(chan struct{}, 1),
		onError:  onError,
	}
	go pipe.run()
	return pipe
}

func (b *dataChannelBridge) attachDownstream(channel *webrtc.DataChannel) {
	b.downstreamMu.Lock()
	if b.downstream != nil {
		b.downstreamMu.Unlock()
		if errClose := channel.Close(); errClose != nil {
			log.WithError(errClose).Debug("codex live media: close duplicate downstream DataChannel")
		}
		return
	}
	b.downstream = channel
	b.downstreamMu.Unlock()
	b.upToDown.setDestination(channel)
	b.bindSource(channel, b.downToUp)
}

func (b *dataChannelBridge) attachUpstream(channel *webrtc.DataChannel) {
	b.upstreamMu.Lock()
	if b.upstream != nil {
		b.upstreamMu.Unlock()
		if errClose := channel.Close(); errClose != nil {
			log.WithError(errClose).Debug("codex live media: close duplicate upstream DataChannel")
		}
		return
	}
	b.upstream = channel
	b.upstreamMu.Unlock()
	b.downToUp.setDestination(channel)
	b.bindSource(channel, b.upToDown)
}

func (b *dataChannelBridge) bindSource(channel *webrtc.DataChannel, destination *dataChannelPipe) {
	channel.OnMessage(func(message webrtc.DataChannelMessage) {
		if len(message.Data) > mediaDataMessageMaxSize {
			destination.reportError(fmt.Errorf("%s DataChannel message exceeds %d bytes", destination.name, mediaDataMessageMaxSize))
			return
		}
		payload := append([]byte(nil), message.Data...)
		select {
		case destination.queue <- dataChannelMessage{data: payload, isString: message.IsString}:
		case <-b.done:
		}
	})
	channel.OnError(func(err error) {
		destination.reportError(fmt.Errorf("%s DataChannel error: %w", destination.name, err))
	})
	channel.OnClose(func() {
		select {
		case <-b.done:
			return
		default:
			destination.reportError(fmt.Errorf("%s DataChannel closed", destination.name))
		}
	})
}

func (b *dataChannelBridge) close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		b.downstreamMu.Lock()
		downstream := b.downstream
		b.downstreamMu.Unlock()
		if downstream != nil {
			if errClose := downstream.Close(); errClose != nil {
				log.WithError(errClose).Debug("codex live media: close downstream DataChannel")
			}
		}
		b.upstreamMu.Lock()
		upstream := b.upstream
		b.upstreamMu.Unlock()
		if upstream != nil {
			if errClose := upstream.Close(); errClose != nil {
				log.WithError(errClose).Debug("codex live media: close upstream DataChannel")
			}
		}
	})
}

func (p *dataChannelPipe) setDestination(channel *webrtc.DataChannel) {
	p.mu.Lock()
	p.destination = channel
	p.mu.Unlock()
	markReady := func() {
		p.readyOnce.Do(func() { close(p.ready) })
	}
	channel.SetBufferedAmountLowThreshold(mediaDataBufferedMaxSize / 2)
	channel.OnBufferedAmountLow(func() {
		select {
		case p.writable <- struct{}{}:
		default:
		}
	})
	channel.OnOpen(markReady)
	if channel.ReadyState() == webrtc.DataChannelStateOpen {
		markReady()
	}
}

func (p *dataChannelPipe) run() {
	select {
	case <-p.ready:
	case <-p.done:
		return
	}
	for {
		select {
		case message := <-p.queue:
			p.mu.RLock()
			destination := p.destination
			p.mu.RUnlock()
			if destination == nil {
				p.reportError(fmt.Errorf("%s DataChannel destination unavailable", p.name))
				return
			}
			if !p.waitWritable(destination, len(message.data)) {
				return
			}
			var errSend error
			if message.isString {
				errSend = destination.SendText(string(message.data))
			} else {
				errSend = destination.Send(message.data)
			}
			if errSend != nil {
				p.reportError(fmt.Errorf("send %s DataChannel message: %w", p.name, errSend))
				return
			}
		case <-p.done:
			return
		}
	}
}

func (p *dataChannelPipe) waitWritable(destination *webrtc.DataChannel, messageSize int) bool {
	for destination.BufferedAmount()+uint64(messageSize) > mediaDataBufferedMaxSize {
		select {
		case <-p.writable:
		case <-p.done:
			return false
		}
	}
	return true
}

func (p *dataChannelPipe) reportError(err error) {
	if p.onError != nil {
		p.onError(err)
	}
}
