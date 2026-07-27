package live

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestPionMediaRelaySelectsRemoteProxyMode(t *testing.T) {
	clientAPI := newTestWebRTCAPI(t)
	client, errClient := clientAPI.NewPeerConnection(webrtc.Configuration{})
	if errClient != nil {
		t.Fatalf("create client PeerConnection: %v", errClient)
	}
	defer closeTestPeerConnection(t, client)
	if _, errChannel := client.CreateDataChannel(realtimeDataChannelLabel, nil); errChannel != nil {
		t.Fatalf("create client DataChannel: %v", errChannel)
	}
	clientOffer := completeOffer(t, client)
	relay, errRelay := newPionMediaRelay(config.CodexLiveMediaRelayConfig{
		Enabled:  true,
		PublicIP: "198.51.100.1",
	})
	if errRelay != nil {
		t.Fatalf("create media relay: %v", errRelay)
	}

	for name, testCase := range map[string]struct {
		proxyURL string
		proxied  bool
	}{
		"inherit": {proxyURL: ""},
		"direct":  {proxyURL: "direct"},
		"HTTP":    {proxyURL: "http://proxy.example:8080", proxied: true},
		"HTTPS":   {proxyURL: "https://proxy.example:8443", proxied: true},
		"SOCKS5":  {proxyURL: "socks5://proxy.example:1080", proxied: true},
		"SOCKS5H": {proxyURL: "socks5h://proxy.example:1080", proxied: true},
	} {
		t.Run(name, func(t *testing.T) {
			session, upstreamOffer, errSession := relay.NewSession(context.Background(), clientOffer, mediaSessionRoute{proxyURL: testCase.proxyURL})
			if errSession != nil {
				t.Fatalf("create media session: %v", errSession)
			}
			pionSession, ok := session.(*pionMediaSession)
			if !ok {
				t.Fatalf("media session type = %T", session)
			}
			if got := pionSession.proxyDialer != nil; got != testCase.proxied {
				t.Fatalf("proxied = %t, want %t", got, testCase.proxied)
			}
			if testCase.proxied && !offerCandidatesAreLoopback(t, upstreamOffer) {
				t.Fatal("proxied upstream offer exposed a non-loopback candidate")
			}
			if errClose := session.Close(); errClose != nil {
				t.Fatalf("close media session: %v", errClose)
			}
		})
	}

	if _, _, errSession := relay.NewSession(context.Background(), clientOffer, mediaSessionRoute{proxyURL: "invalid-proxy"}); errSession == nil {
		t.Fatal("expected invalid proxy URL to fail media session creation")
	}
}

func TestMediaForwardingStartedLogRedactsProxyCredentials(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	hook := logtest.NewLocal(logger)
	defer logger.ReplaceHooks(previousHooks)

	for name, testCase := range map[string]struct {
		proxyURL   string
		connection string
		credential string
	}{
		"direct": {
			connection: "direct",
			credential: "Voice credential",
		},
		"HTTP": {
			proxyURL:   "http://user:secret@proxy.example:8080",
			connection: "via http proxy",
			credential: "Voice credential",
		},
		"SOCKS5 without label": {
			proxyURL:   "socks5://user:secret@proxy.example:1080",
			connection: "via socks5 proxy",
			credential: "auth-index",
		},
	} {
		t.Run(name, func(t *testing.T) {
			session := &pionMediaSession{
				mediaSessionID: "media-session-" + name,
				proxyScheme:    proxyScheme(testCase.proxyURL),
				credential:     testCase.credential,
				authIndex:      "auth-index",
			}
			if testCase.proxyURL != "" {
				session.proxyDialer = &recordingProxyDialer{dials: make(chan recordedProxyDial, 1)}
			}
			earlyFields := session.logFields("session")
			for _, field := range []string{"auth_id", "auth_label", "auth_index", "credential", "connection"} {
				if _, exists := earlyFields[field]; exists {
					t.Fatalf("session log exposed forwarding-only field %q before forwarding started: %#v", field, earlyFields)
				}
			}
			session.logForwardingStarted()
			session.logForwardingStarted()

			matching := 0
			for _, entry := range hook.AllEntries() {
				if entry.Message != "codex live remote media forwarding started" || entry.Data["media_session_id"] != session.mediaSessionID {
					continue
				}
				matching++
				if entry.Data["connection"] != testCase.connection || entry.Data["credential"] != testCase.credential {
					t.Fatalf("forwarding fields = %#v", entry.Data)
				}
				serialized := fmt.Sprint(entry.Data)
				for _, secret := range []string{"user", "secret", "proxy.example"} {
					if strings.Contains(serialized, secret) {
						t.Fatalf("forwarding log leaked %q: %s", secret, serialized)
					}
				}
			}
			if matching != 1 {
				t.Fatalf("forwarding log count = %d, want 1", matching)
			}
		})
	}
}

func TestPionMediaRelayBridgesAudioAndDataChannel(t *testing.T) {
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	logger.SetLevel(log.DebugLevel)
	hook := logtest.NewLocal(logger)
	defer func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
	}()
	clientAPI := newTestWebRTCAPI(t)
	client, errClient := clientAPI.NewPeerConnection(webrtc.Configuration{})
	if errClient != nil {
		t.Fatalf("create client PeerConnection: %v", errClient)
	}
	defer closeTestPeerConnection(t, client)
	clientDone := make(chan struct{})
	defer close(clientDone)

	clientAudio, errTrack := webrtc.NewTrackLocalStaticRTP(opusCodec, "client-audio", "client")
	if errTrack != nil {
		t.Fatalf("create client audio track: %v", errTrack)
	}
	clientSender, errTrack := client.AddTrack(clientAudio)
	if errTrack != nil {
		t.Fatalf("add client audio track: %v", errTrack)
	}
	go drainRTCP("test-client", clientSender, clientDone)
	clientData, errData := client.CreateDataChannel(realtimeDataChannelLabel, nil)
	if errData != nil {
		t.Fatalf("create client DataChannel: %v", errData)
	}
	clientMessages := make(chan webrtc.DataChannelMessage, 4)
	clientData.OnMessage(func(message webrtc.DataChannelMessage) {
		message.Data = append([]byte(nil), message.Data...)
		clientMessages <- message
	})
	clientAudioMessages := make(chan []byte, 1)
	client.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, errRead := track.ReadRTP()
		if errRead == nil {
			clientAudioMessages <- append([]byte(nil), packet.Payload...)
		}
	})

	clientOffer := completeOffer(t, client)
	relayConfig := config.CodexLiveMediaRelayConfig{
		Enabled:                 true,
		MaxSessions:             1,
		DisablePrivateRemoteIPs: false,
	}
	relay, errRelay := newPionMediaRelay(relayConfig)
	if errRelay != nil {
		t.Fatalf("create media relay: %v", errRelay)
	}
	session, relayOffer, errSession := relay.NewSession(context.Background(), clientOffer, mediaSessionRoute{
		credential: "Voice credential",
		authIndex:  "auth-index",
	})
	if errSession != nil {
		t.Fatalf("create media relay session: %v", errSession)
	}
	session.SetCallID("call-log-test")
	defer func() {
		if errClose := session.Close(); errClose != nil {
			t.Errorf("close media relay session: %v", errClose)
		}
	}()
	reloadedRelay, errRelay := newPionMediaRelayWithLimiter(relayConfig, relay.limiter)
	if errRelay != nil {
		t.Fatalf("reload media relay: %v", errRelay)
	}
	if _, _, errCapacity := reloadedRelay.NewSession(context.Background(), clientOffer, mediaSessionRoute{}); errCapacity == nil {
		t.Fatal("reloaded media relay bypassed the shared session capacity")
	}

	upstreamAPI := newTestWebRTCAPI(t)
	upstream, errUpstream := upstreamAPI.NewPeerConnection(webrtc.Configuration{})
	if errUpstream != nil {
		t.Fatalf("create upstream PeerConnection: %v", errUpstream)
	}
	defer closeTestPeerConnection(t, upstream)
	upstreamDone := make(chan struct{})
	defer close(upstreamDone)

	upstreamDataChannels := make(chan *webrtc.DataChannel, 1)
	upstreamMessages := make(chan webrtc.DataChannelMessage, 4)
	upstream.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() != realtimeDataChannelLabel {
			return
		}
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			message.Data = append([]byte(nil), message.Data...)
			upstreamMessages <- message
		})
		upstreamDataChannels <- channel
	})
	upstreamAudioMessages := make(chan []byte, 1)
	upstream.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, errRead := track.ReadRTP()
		if errRead == nil {
			upstreamAudioMessages <- append([]byte(nil), packet.Payload...)
		}
	})
	if errRemote := upstream.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: relayOffer}); errRemote != nil {
		t.Fatalf("set upstream offer: %v", errRemote)
	}
	upstreamAudio, errTrack := webrtc.NewTrackLocalStaticRTP(opusCodec, "upstream-audio", "upstream")
	if errTrack != nil {
		t.Fatalf("create upstream audio track: %v", errTrack)
	}
	upstreamSender, errTrack := upstream.AddTrack(upstreamAudio)
	if errTrack != nil {
		t.Fatalf("add upstream audio track: %v", errTrack)
	}
	go drainRTCP("test-upstream", upstreamSender, upstreamDone)
	upstreamAnswer := completeAnswer(t, upstream)
	downstreamAnswer, errAnswer := session.AcceptUpstreamAnswer(context.Background(), upstreamAnswer)
	if errAnswer != nil {
		t.Fatalf("accept upstream answer: %v", errAnswer)
	}
	if errRemote := client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: downstreamAnswer}); errRemote != nil {
		t.Fatalf("set client answer: %v", errRemote)
	}

	upstreamData := receiveDataChannel(t, upstreamDataChannels)
	waitDataChannelOpen(t, clientData)
	waitDataChannelOpen(t, upstreamData)
	if errSend := clientData.SendText("from-client"); errSend != nil {
		t.Fatalf("send client DataChannel message: %v", errSend)
	}
	if got := receiveDataMessage(t, upstreamMessages); !got.IsString || string(got.Data) != "from-client" {
		t.Fatalf("upstream DataChannel message = %#v, want text from-client", got)
	}
	if errSend := upstreamData.SendText("from-upstream"); errSend != nil {
		t.Fatalf("send upstream DataChannel message: %v", errSend)
	}
	if got := receiveDataMessage(t, clientMessages); !got.IsString || string(got.Data) != "from-upstream" {
		t.Fatalf("client DataChannel message = %#v, want text from-upstream", got)
	}
	if errSend := clientData.Send([]byte{0x01, 0x02, 0x03}); errSend != nil {
		t.Fatalf("send client binary DataChannel message: %v", errSend)
	}
	if got := receiveDataMessage(t, upstreamMessages); got.IsString || string(got.Data) != string([]byte{0x01, 0x02, 0x03}) {
		t.Fatalf("upstream binary DataChannel message = %#v", got)
	}

	clientPayload := []byte{0xf8, 0xff, 0xfe}
	sendTestRTP(t, clientAudio, clientPayload, upstreamAudioMessages)
	upstreamPayload := []byte{0xf8, 0xfe, 0xfd}
	sendTestRTP(t, upstreamAudio, upstreamPayload, clientAudioMessages)
	if errClose := session.Close(); errClose != nil {
		t.Fatalf("close media relay session for logging: %v", errClose)
	}
	replacementSession, _, errReplacement := reloadedRelay.NewSession(context.Background(), clientOffer, mediaSessionRoute{})
	if errReplacement != nil {
		t.Fatalf("shared capacity was not released: %v", errReplacement)
	}
	if errClose := replacementSession.CloseWithReason("test_complete"); errClose != nil {
		t.Fatalf("close replacement media session: %v", errClose)
	}
	for _, peer := range []string{"local", "remote"} {
		assertPeerLog(t, hook, "codex live WebRTC peer connected", peer, "call-log-test")
		assertPeerLog(t, hook, "codex live WebRTC peer closed", peer, "call-log-test")
	}
	assertForwardingLog(t, hook, "direct", "Voice credential", "auth-index", "connected")
	assertForwardingAfterRemoteConnected(t, hook)
	assertSessionLog(t, hook, "codex live WebRTC media session closed", "closed", "call-log-test")
}

func TestIsPublicRemoteIP(t *testing.T) {
	for rawIP, want := range map[string]bool{
		"8.8.8.8":      true,
		"2001:4860::1": true,
		"127.0.0.1":    false,
		"10.0.0.1":     false,
		"169.254.1.1":  false,
		"224.0.0.1":    false,
		"::1":          false,
		"fc00::1":      false,
		"fe80::1":      false,
		"ff02::1":      false,
		"0.0.0.0":      false,
	} {
		if got := isPublicRemoteIP(net.ParseIP(rawIP)); got != want {
			t.Errorf("isPublicRemoteIP(%q) = %t, want %t", rawIP, got, want)
		}
	}
	if isPublicRemoteIP(nil) {
		t.Fatal("isPublicRemoteIP(nil) = true, want false")
	}
}

func offerCandidatesAreLoopback(t *testing.T, offer string) bool {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(offer, "\r\n", "\n"), "\n")
	candidateCount := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		candidateCount++
		fields := strings.Fields(strings.TrimPrefix(line, "a=candidate:"))
		if len(fields) < 6 {
			t.Fatalf("malformed offer candidate: %q", line)
		}
		address := net.ParseIP(fields[4])
		if address == nil || !address.IsLoopback() {
			return false
		}
	}
	return candidateCount > 0
}

func newTestWebRTCAPI(t *testing.T) *webrtc.API {
	t.Helper()
	mediaEngine := &webrtc.MediaEngine{}
	if errRegister := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: opusCodec,
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); errRegister != nil {
		t.Fatalf("register test Opus codec: %v", errRegister)
	}
	interceptorRegistry := &interceptor.Registry{}
	if errRegister := webrtc.RegisterDefaultInterceptors(mediaEngine, interceptorRegistry); errRegister != nil {
		t.Fatalf("register test interceptors: %v", errRegister)
	}
	return webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(interceptorRegistry),
	)
}

func completeOffer(t *testing.T, connection *webrtc.PeerConnection) string {
	t.Helper()
	gatherComplete := webrtc.GatheringCompletePromise(connection)
	offer, errOffer := connection.CreateOffer(nil)
	if errOffer != nil {
		t.Fatalf("create offer: %v", errOffer)
	}
	if errLocal := connection.SetLocalDescription(offer); errLocal != nil {
		t.Fatalf("set local offer: %v", errLocal)
	}
	select {
	case <-gatherComplete:
	case <-time.After(5 * time.Second):
		t.Fatal("offer ICE gathering did not complete")
	}
	return connection.LocalDescription().SDP
}

func completeAnswer(t *testing.T, connection *webrtc.PeerConnection) string {
	t.Helper()
	gatherComplete := webrtc.GatheringCompletePromise(connection)
	answer, errAnswer := connection.CreateAnswer(nil)
	if errAnswer != nil {
		t.Fatalf("create answer: %v", errAnswer)
	}
	if errLocal := connection.SetLocalDescription(answer); errLocal != nil {
		t.Fatalf("set local answer: %v", errLocal)
	}
	select {
	case <-gatherComplete:
	case <-time.After(5 * time.Second):
		t.Fatal("answer ICE gathering did not complete")
	}
	return connection.LocalDescription().SDP
}

func waitDataChannelOpen(t *testing.T, channel *webrtc.DataChannel) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if channel.ReadyState() == webrtc.DataChannelStateOpen {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("DataChannel %q did not open", channel.Label())
}

func receiveDataChannel(t *testing.T, channels <-chan *webrtc.DataChannel) *webrtc.DataChannel {
	t.Helper()
	select {
	case channel := <-channels:
		return channel
	case <-time.After(5 * time.Second):
		t.Fatal("upstream DataChannel was not created")
		return nil
	}
}

func receiveDataMessage(t *testing.T, messages <-chan webrtc.DataChannelMessage) webrtc.DataChannelMessage {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(5 * time.Second):
		t.Fatal("DataChannel message was not relayed")
		return webrtc.DataChannelMessage{}
	}
}

func sendTestRTP(t *testing.T, track *webrtc.TrackLocalStaticRTP, payload []byte, received <-chan []byte) {
	t.Helper()
	for sequence := uint16(1); sequence <= 25; sequence++ {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111,
				SequenceNumber: sequence,
				Timestamp:      uint32(sequence) * 960,
				SSRC:           1234,
			},
			Payload: payload,
		}
		if errWrite := track.WriteRTP(packet); errWrite != nil {
			t.Fatalf("write test RTP: %v", errWrite)
		}
		select {
		case got := <-received:
			if string(got) != string(payload) {
				t.Fatalf("relayed RTP payload = %v, want %v", got, payload)
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("RTP packet was not relayed")
}

func assertForwardingAfterRemoteConnected(t *testing.T, hook *logtest.Hook) {
	t.Helper()
	connectedIndex := -1
	forwardingIndex := -1
	for index, entry := range hook.AllEntries() {
		if entry.Message == "codex live WebRTC peer connected" && entry.Data["peer"] == "remote" && connectedIndex == -1 {
			connectedIndex = index
		}
		if entry.Message == "codex live remote media forwarding started" && forwardingIndex == -1 {
			forwardingIndex = index
		}
	}
	if connectedIndex == -1 || forwardingIndex <= connectedIndex {
		t.Fatalf("remote connected index=%d, forwarding index=%d", connectedIndex, forwardingIndex)
	}
}

func assertForwardingLog(t *testing.T, hook *logtest.Hook, connection, credential, authIndex, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range hook.AllEntries() {
			if entry.Message == "codex live remote media forwarding started" &&
				entry.Data["connection"] == connection &&
				entry.Data["credential"] == credential &&
				entry.Data["auth_index"] == authIndex &&
				entry.Data["state"] == state {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing forwarding log for connection %q and credential %q", connection, credential)
}

func assertSessionLog(t *testing.T, hook *logtest.Hook, message, reason, callID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range hook.AllEntries() {
			if entry.Message == message && entry.Data["reason"] == reason && entry.Data["call_id"] == callID {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing session log message %q for reason %q and call %q", message, reason, callID)
}

func assertPeerLog(t *testing.T, hook *logtest.Hook, message, peer, callID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, entry := range hook.AllEntries() {
			if entry.Message == message && entry.Data["peer"] == peer && entry.Data["call_id"] == callID {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing log message %q for peer %q and call %q", message, peer, callID)
}

func closeTestPeerConnection(t *testing.T, connection *webrtc.PeerConnection) {
	t.Helper()
	if errClose := connection.Close(); errClose != nil {
		t.Errorf("close test PeerConnection: %v", errClose)
	}
}
