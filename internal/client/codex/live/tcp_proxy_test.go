package live

import (
	"bytes"
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
	"testing"
	"time"

	"github.com/pion/sdp/v3"
	"github.com/pion/stun/v3"
	"github.com/pion/webrtc/v4"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type recordedProxyDial struct {
	address    string
	connection net.Conn
}

type recordingProxyDialer struct {
	mu    sync.Mutex
	dials chan recordedProxyDial
	err   error
}

type blockingContextDialer struct {
	started  chan struct{}
	canceled chan struct{}
}

type closedUpstreamDialer struct{}

func (*closedUpstreamDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (d *blockingContextDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	close(d.started)
	<-ctx.Done()
	close(d.canceled)
	return nil, ctx.Err()
}

func (d *recordingProxyDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}

func (d *recordingProxyDialer) DialContext(ctx context.Context, _ string, address string) (net.Conn, error) {
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	d.mu.Lock()
	channel := d.dials
	errDial := d.err
	d.mu.Unlock()
	if errDial != nil {
		channel <- recordedProxyDial{address: address}
		return nil, errDial
	}
	client, server := net.Pipe()
	channel <- recordedProxyDial{address: address, connection: server}
	return client, nil
}

func TestPrepareProxiedUpstreamAnswerRestrictsAndRewritesCandidates(t *testing.T) {
	dialer := &recordingProxyDialer{dials: make(chan recordedProxyDial, 1)}
	answer := testProxySDP("remote-ufrag", "remote-password", []string{
		"1 1 udp 2130706431 20.42.0.10 3478 typ host",
		"2 1 tcp 1671430143 20.42.0.20 443 typ host tcptype passive",
	})
	localOffer := testProxySDP("local-ufrag", "local-password", nil)

	rewritten, tunnels, errPrepare := prepareProxiedUpstreamAnswer(answer, localOffer, dialer)
	if errPrepare != nil {
		t.Fatalf("prepareProxiedUpstreamAnswer returned error: %v", errPrepare)
	}
	defer func() {
		if errClose := closeCandidateTunnels(tunnels); errClose != nil {
			t.Errorf("close candidate tunnels: %v", errClose)
		}
	}()
	if len(tunnels) != 1 {
		t.Fatalf("tunnel count = %d, want 1", len(tunnels))
	}
	if got := tunnels[0].target.String(); got != "20.42.0.20:443" {
		t.Fatalf("fixed target = %q, want 20.42.0.20:443", got)
	}
	if tunnels[0].expectedUser != "remote-ufrag:local-ufrag" {
		t.Fatalf("expected STUN username = %q", tunnels[0].expectedUser)
	}

	var description sdp.SessionDescription
	if errUnmarshal := description.UnmarshalString(rewritten); errUnmarshal != nil {
		t.Fatalf("unmarshal rewritten SDP: %v", errUnmarshal)
	}
	var candidates []string
	for _, media := range description.MediaDescriptions {
		for _, attribute := range media.Attributes {
			if attribute.IsICECandidate() {
				candidates = append(candidates, attribute.Value)
			}
		}
	}
	if len(candidates) != 1 {
		t.Fatalf("rewritten candidate count = %d, want 1: %v", len(candidates), candidates)
	}
	fields := strings.Fields(candidates[0])
	if len(fields) < 8 || fields[2] != "tcp" || fields[4] != "127.0.0.1" || fields[5] == "443" {
		t.Fatalf("rewritten candidate = %q", candidates[0])
	}
	if !strings.Contains(candidates[0], "tcptype passive") {
		t.Fatalf("rewritten candidate lost passive TCP type: %q", candidates[0])
	}
}

func TestPrepareProxiedUpstreamAnswerRejectsUnsafeTargets(t *testing.T) {
	for name, candidate := range map[string]string{
		"private target":         "1 1 tcp 1671430143 10.0.0.1 443 typ host tcptype passive",
		"zero network target":    "1 1 tcp 1671430143 0.0.0.1 443 typ host tcptype passive",
		"carrier NAT target":     "1 1 tcp 1671430143 100.64.0.1 443 typ host tcptype passive",
		"reserved target":        "1 1 tcp 1671430143 203.0.113.10 443 typ host tcptype passive",
		"site-local IPv6 target": "1 1 tcp 1671430143 fec0::1 443 typ host tcptype passive",
		"wrong port":             "1 1 tcp 1671430143 20.42.0.10 8443 typ host tcptype passive",
		"relay target":           "1 1 tcp 1671430143 20.42.0.10 443 typ relay raddr 192.0.2.1 rport 5000 tcptype passive",
		"active target":          "1 1 tcp 1671430143 20.42.0.10 443 typ host tcptype active",
	} {
		t.Run(name, func(t *testing.T) {
			dialer := &recordingProxyDialer{dials: make(chan recordedProxyDial, 1)}
			_, tunnels, errPrepare := prepareProxiedUpstreamAnswer(
				testProxySDP("remote", "remote-password", []string{candidate}),
				testProxySDP("local", "local-password", nil),
				dialer,
			)
			if errPrepare == nil {
				_ = closeCandidateTunnels(tunnels)
				t.Fatal("expected unsafe candidate to be rejected")
			}
		})
	}
}

func TestPrepareProxiedUpstreamAnswerLimitsCandidateCount(t *testing.T) {
	candidates := make([]string, 0, maxUpstreamICECandidates+1)
	for index := 0; index <= maxUpstreamICECandidates; index++ {
		candidates = append(candidates, fmt.Sprintf("%d 1 udp 2130706431 20.42.0.10 3478 typ host", index+1))
	}
	_, tunnels, errPrepare := prepareProxiedUpstreamAnswer(
		testProxySDP("remote", "remote-password", candidates),
		testProxySDP("local", "local-password", nil),
		&recordingProxyDialer{dials: make(chan recordedProxyDial, 1)},
	)
	if errPrepare == nil || !strings.Contains(errPrepare.Error(), "candidate limit") {
		_ = closeCandidateTunnels(tunnels)
		t.Fatalf("error = %v, want candidate limit", errPrepare)
	}
}

func TestReadValidatedICEBindingFrame(t *testing.T) {
	validFrame := buildTestICEFrame(t, "remote:local", "remote-password", true)
	for name, testCase := range map[string]struct {
		frame        []byte
		expectedUser string
		password     string
		wantError    bool
	}{
		"valid": {
			frame:        validFrame,
			expectedUser: "remote:local",
			password:     "remote-password",
		},
		"wrong username": {
			frame:        validFrame,
			expectedUser: "local:remote",
			password:     "remote-password",
			wantError:    true,
		},
		"wrong password": {
			frame:        validFrame,
			expectedUser: "remote:local",
			password:     "local-password",
			wantError:    true,
		},
		"missing fingerprint": {
			frame:        buildTestICEFrame(t, "remote:local", "remote-password", false),
			expectedUser: "remote:local",
			password:     "remote-password",
			wantError:    true,
		},
		"undersized": {
			frame:     []byte{0, 1, 0},
			wantError: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			validated, errValidate := readValidatedICEBindingFrame(
				&fragmentedReader{data: testCase.frame, maximum: 3},
				testCase.expectedUser,
				testCase.password,
			)
			if testCase.wantError {
				if errValidate == nil {
					t.Fatal("expected validation error")
				}
				return
			}
			if errValidate != nil {
				t.Fatalf("readValidatedICEBindingFrame returned error: %v", errValidate)
			}
			if !bytes.Equal(validated, testCase.frame) {
				t.Fatal("validated frame changed")
			}
		})
	}
}

func TestTCPCandidateTunnelAuthenticatesBeforeFixedTargetDial(t *testing.T) {
	dialer := &recordingProxyDialer{dials: make(chan recordedProxyDial, 1)}
	tunnel, errTunnel := newTCPCandidateTunnel(
		netip.MustParseAddrPort("20.42.0.20:443"),
		dialer,
		"remote:local",
		"remote-password",
	)
	if errTunnel != nil {
		t.Fatalf("newTCPCandidateTunnel returned error: %v", errTunnel)
	}
	defer func() { _ = tunnel.Close() }()
	forwardingStarted := make(chan struct{}, 1)
	tunnel.setForwardingStartedHandler(func() {
		forwardingStarted <- struct{}{}
	})

	client, errDial := net.Dial("tcp", tunnel.listener.Addr().String())
	if errDial != nil {
		t.Fatalf("dial candidate listener: %v", errDial)
	}
	defer func() { _ = client.Close() }()
	frame := buildTestICEFrame(t, "remote:local", "remote-password", true)
	if errWrite := writeAll(client, frame); errWrite != nil {
		t.Fatalf("write authenticated frame: %v", errWrite)
	}

	var dial recordedProxyDial
	select {
	case dial = <-dialer.dials:
	case <-time.After(time.Second):
		t.Fatal("proxy dial was not attempted after STUN authentication")
	}
	defer func() { _ = dial.connection.Close() }()
	if dial.address != "20.42.0.20:443" {
		t.Fatalf("proxy target = %q, want fixed candidate", dial.address)
	}
	forwarded := make([]byte, len(frame))
	if _, errRead := io.ReadFull(dial.connection, forwarded); errRead != nil {
		t.Fatalf("read forwarded STUN frame: %v", errRead)
	}
	if !bytes.Equal(forwarded, frame) {
		t.Fatal("forwarded STUN frame changed")
	}
	select {
	case <-forwardingStarted:
	case <-time.After(time.Second):
		t.Fatal("forwarding start handler was not called")
	}
	if errWrite := writeAll(dial.connection, []byte("reply")); errWrite != nil {
		t.Fatalf("write tunnel reply: %v", errWrite)
	}
	reply := make([]byte, len("reply"))
	if _, errRead := io.ReadFull(client, reply); errRead != nil {
		t.Fatalf("read tunnel reply: %v", errRead)
	}
	if string(reply) != "reply" {
		t.Fatalf("tunnel reply = %q", reply)
	}
}

func TestTCPCandidateTunnelCloseCancelsProxyDial(t *testing.T) {
	dialer := &blockingContextDialer{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	tunnel, errTunnel := newTCPCandidateTunnel(
		netip.MustParseAddrPort("20.42.0.20:443"),
		dialer,
		"remote:local",
		"remote-password",
	)
	if errTunnel != nil {
		t.Fatalf("newTCPCandidateTunnel returned error: %v", errTunnel)
	}
	client, errDial := net.Dial("tcp", tunnel.listener.Addr().String())
	if errDial != nil {
		t.Fatalf("dial candidate listener: %v", errDial)
	}
	if errWrite := writeAll(client, buildTestICEFrame(t, "remote:local", "remote-password", true)); errWrite != nil {
		t.Fatalf("write authenticated frame: %v", errWrite)
	}
	defer func() { _ = client.Close() }()
	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		t.Fatal("proxy dial did not start")
	}
	forwardingStarted := make(chan struct{}, 1)
	tunnel.setForwardingStartedHandler(func() { forwardingStarted <- struct{}{} })
	if errClose := tunnel.Close(); errClose != nil {
		t.Fatalf("close tunnel: %v", errClose)
	}
	select {
	case <-dialer.canceled:
	case <-time.After(time.Second):
		t.Fatal("tunnel close did not cancel proxy dial")
	}
	assertNoForwardingStart(t, forwardingStarted)
}

func TestTCPCandidateTunnelProxyFailureDoesNotFallBack(t *testing.T) {
	dialer := &recordingProxyDialer{
		dials: make(chan recordedProxyDial, 1),
		err:   errors.New("proxy blocked"),
	}
	tunnel, errTunnel := newTCPCandidateTunnel(
		netip.MustParseAddrPort("20.42.0.20:443"),
		dialer,
		"remote:local",
		"remote-password",
	)
	if errTunnel != nil {
		t.Fatalf("newTCPCandidateTunnel returned error: %v", errTunnel)
	}
	defer func() { _ = tunnel.Close() }()
	forwardingStarted := make(chan struct{}, 1)
	tunnel.setForwardingStartedHandler(func() { forwardingStarted <- struct{}{} })
	client, errDial := net.Dial("tcp", tunnel.listener.Addr().String())
	if errDial != nil {
		t.Fatalf("dial candidate listener: %v", errDial)
	}
	if errWrite := writeAll(client, buildTestICEFrame(t, "remote:local", "remote-password", true)); errWrite != nil {
		t.Fatalf("write authenticated frame: %v", errWrite)
	}
	defer func() { _ = client.Close() }()
	select {
	case dial := <-dialer.dials:
		if dial.address != "20.42.0.20:443" || dial.connection != nil {
			t.Fatalf("failed proxy dial = %#v", dial)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy dial was not attempted")
	}
	if _, errSecondDial := net.Dial("tcp", tunnel.listener.Addr().String()); errSecondDial == nil {
		t.Fatal("candidate listener remained available after proxy failure")
	}
	assertNoForwardingStart(t, forwardingStarted)
}

func TestTCPCandidateTunnelWriteFailureDoesNotLogForwardingStart(t *testing.T) {
	tunnel, errTunnel := newTCPCandidateTunnel(
		netip.MustParseAddrPort("20.42.0.20:443"),
		&closedUpstreamDialer{},
		"remote:local",
		"remote-password",
	)
	if errTunnel != nil {
		t.Fatalf("newTCPCandidateTunnel returned error: %v", errTunnel)
	}
	defer func() { _ = tunnel.Close() }()
	forwardingStarted := make(chan struct{}, 1)
	tunnel.setForwardingStartedHandler(func() { forwardingStarted <- struct{}{} })
	client, errDial := net.Dial("tcp", tunnel.listener.Addr().String())
	if errDial != nil {
		t.Fatalf("dial candidate listener: %v", errDial)
	}
	if errWrite := writeAll(client, buildTestICEFrame(t, "remote:local", "remote-password", true)); errWrite != nil {
		t.Fatalf("write authenticated frame: %v", errWrite)
	}
	_ = client.Close()
	assertNoForwardingStart(t, forwardingStarted)
}

func TestTCPCandidateTunnelRejectsUnauthenticatedConnectionWithoutDial(t *testing.T) {
	dialer := &recordingProxyDialer{dials: make(chan recordedProxyDial, 1)}
	tunnel, errTunnel := newTCPCandidateTunnel(
		netip.MustParseAddrPort("20.42.0.20:443"),
		dialer,
		"remote:local",
		"remote-password",
	)
	if errTunnel != nil {
		t.Fatalf("newTCPCandidateTunnel returned error: %v", errTunnel)
	}
	defer func() { _ = tunnel.Close() }()
	forwardingStarted := make(chan struct{}, 1)
	tunnel.setForwardingStartedHandler(func() { forwardingStarted <- struct{}{} })

	client, errDial := net.Dial("tcp", tunnel.listener.Addr().String())
	if errDial != nil {
		t.Fatalf("dial candidate listener: %v", errDial)
	}
	if errWrite := writeAll(client, buildTestICEFrame(t, "attacker:local", "remote-password", true)); errWrite != nil {
		t.Fatalf("write unauthenticated frame: %v", errWrite)
	}
	_ = client.Close()
	select {
	case dial := <-dialer.dials:
		_ = dial.connection.Close()
		t.Fatalf("unauthenticated connection triggered proxy dial to %q", dial.address)
	case <-time.After(100 * time.Millisecond):
	}
	assertNoForwardingStart(t, forwardingStarted)
}

func assertNoForwardingStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
		t.Fatal("forwarding start handler was called for an unestablished tunnel")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPionActiveTCPCandidatePassesTunnelAuthentication(t *testing.T) {
	localAPI, errAPI := newPionProxyAPI(config.CodexLiveMediaRelayConfig{})
	if errAPI != nil {
		t.Fatalf("create local Pion API: %v", errAPI)
	}
	localPeer, errPeer := localAPI.NewPeerConnection(webrtc.Configuration{})
	if errPeer != nil {
		t.Fatalf("create local PeerConnection: %v", errPeer)
	}
	defer func() { _ = localPeer.Close() }()
	if _, errChannel := localPeer.CreateDataChannel(realtimeDataChannelLabel, nil); errChannel != nil {
		t.Fatalf("create local DataChannel: %v", errChannel)
	}
	localGathering := webrtc.GatheringCompletePromise(localPeer)
	localOffer, errOffer := localPeer.CreateOffer(nil)
	if errOffer != nil {
		t.Fatalf("create local offer: %v", errOffer)
	}
	if errLocal := localPeer.SetLocalDescription(localOffer); errLocal != nil {
		t.Fatalf("set local offer: %v", errLocal)
	}
	<-localGathering
	localDescription := localPeer.LocalDescription()
	if localDescription == nil {
		t.Fatal("local description is nil")
	}

	tcpListener, errListen := net.Listen("tcp4", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen for remote ICE-TCP: %v", errListen)
	}
	remoteSettings := webrtc.SettingEngine{}
	remoteSettings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeTCP4})
	remoteSettings.SetIncludeLoopbackCandidate(true)
	remoteSettings.SetIPFilter(func(ip net.IP) bool { return ip != nil && ip.IsLoopback() })
	tcpMux := webrtc.NewICETCPMux(nil, tcpListener, 8)
	remoteSettings.SetICETCPMux(tcpMux)
	defer func() { _ = tcpMux.Close() }()
	remoteAPI := webrtc.NewAPI(webrtc.WithSettingEngine(remoteSettings))
	remotePeer, errPeer := remoteAPI.NewPeerConnection(webrtc.Configuration{})
	if errPeer != nil {
		t.Fatalf("create remote PeerConnection: %v", errPeer)
	}
	defer func() { _ = remotePeer.Close() }()
	if errRemote := remotePeer.SetRemoteDescription(*localDescription); errRemote != nil {
		t.Fatalf("set remote offer: %v", errRemote)
	}
	remoteGathering := webrtc.GatheringCompletePromise(remotePeer)
	remoteAnswer, errAnswer := remotePeer.CreateAnswer(nil)
	if errAnswer != nil {
		t.Fatalf("create remote answer: %v", errAnswer)
	}
	if errLocal := remotePeer.SetLocalDescription(remoteAnswer); errLocal != nil {
		t.Fatalf("set remote answer: %v", errLocal)
	}
	<-remoteGathering
	remoteDescription := remotePeer.LocalDescription()
	if remoteDescription == nil {
		t.Fatal("remote description is nil")
	}
	publicAnswer := rewriteTestTCPCandidateTarget(t, remoteDescription.SDP, "20.42.0.20", 443)

	dialer := &recordingProxyDialer{dials: make(chan recordedProxyDial, 1)}
	rewrittenAnswer, tunnels, errPrepare := prepareProxiedUpstreamAnswer(publicAnswer, localDescription.SDP, dialer)
	if errPrepare != nil {
		t.Fatalf("prepare proxied Pion answer: %v", errPrepare)
	}
	defer func() { _ = closeCandidateTunnels(tunnels) }()
	if errRemote := localPeer.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  rewrittenAnswer,
	}); errRemote != nil {
		t.Fatalf("set rewritten remote answer: %v", errRemote)
	}

	var dial recordedProxyDial
	select {
	case dial = <-dialer.dials:
	case <-time.After(5 * time.Second):
		t.Fatal("Pion active ICE-TCP did not reach the authenticated tunnel")
	}
	defer func() { _ = dial.connection.Close() }()
	localCredentials, errCredentials := bundledICECredentialsFromString(localDescription.SDP)
	if errCredentials != nil {
		t.Fatalf("read local credentials: %v", errCredentials)
	}
	remoteCredentials, errCredentials := bundledICECredentialsFromString(publicAnswer)
	if errCredentials != nil {
		t.Fatalf("read remote credentials: %v", errCredentials)
	}
	if _, errValidate := readValidatedICEBindingFrame(
		dial.connection,
		remoteCredentials.ufrag+":"+localCredentials.ufrag,
		remoteCredentials.password,
	); errValidate != nil {
		t.Fatalf("forwarded Pion STUN request failed validation: %v", errValidate)
	}
}

func TestBundledICECredentialsRejectsMixedCredentials(t *testing.T) {
	mixed := strings.Replace(
		testProxySDP("first", "first-password", nil),
		"a=mid:1\r\na=ice-ufrag:first\r\na=ice-pwd:first-password",
		"a=mid:1\r\na=ice-ufrag:second\r\na=ice-pwd:second-password",
		1,
	)
	var description sdp.SessionDescription
	if errUnmarshal := description.UnmarshalString(mixed); errUnmarshal != nil {
		t.Fatalf("unmarshal mixed SDP: %v", errUnmarshal)
	}
	if _, errCredentials := bundledICECredentials(&description); errCredentials == nil {
		t.Fatal("expected inconsistent bundled credentials to be rejected")
	}
}

func buildTestICEFrame(t *testing.T, username, password string, fingerprint bool) []byte {
	t.Helper()
	setters := []stun.Setter{
		stun.BindingRequest,
		stun.TransactionID,
		stun.NewUsername(username),
		stun.NewShortTermIntegrity(password),
	}
	if fingerprint {
		setters = append(setters, stun.Fingerprint)
	}
	message, errBuild := stun.Build(setters...)
	if errBuild != nil {
		t.Fatalf("build STUN request: %v", errBuild)
	}
	if len(message.Raw) > int(^uint16(0)) {
		t.Fatal("test STUN request is too large")
	}
	frame := make([]byte, 2+len(message.Raw))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(message.Raw)))
	copy(frame[2:], message.Raw)
	return frame
}

func testProxySDP(ufrag, password string, candidates []string) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0 1\r\n")
	for _, media := range []struct {
		line string
		mid  string
	}{
		{line: "m=audio 9 UDP/TLS/RTP/SAVPF 111", mid: "0"},
		{line: "m=application 9 UDP/DTLS/SCTP webrtc-datachannel", mid: "1"},
	} {
		_, _ = fmt.Fprintf(&builder, "%s\r\nc=IN IP4 0.0.0.0\r\na=mid:%s\r\na=ice-ufrag:%s\r\na=ice-pwd:%s\r\n", media.line, media.mid, ufrag, password)
		if media.mid == "0" {
			for _, candidate := range candidates {
				_, _ = fmt.Fprintf(&builder, "a=candidate:%s\r\n", candidate)
			}
		}
	}
	return builder.String()
}

type fragmentedReader struct {
	data    []byte
	maximum int
}

func (r *fragmentedReader) Read(destination []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	limit := len(destination)
	if limit > r.maximum {
		limit = r.maximum
	}
	if limit > len(r.data) {
		limit = len(r.data)
	}
	copy(destination, r.data[:limit])
	r.data = r.data[limit:]
	return limit, nil
}

func rewriteTestTCPCandidateTarget(t *testing.T, rawSDP, address string, port int) string {
	t.Helper()
	var description sdp.SessionDescription
	if errUnmarshal := description.UnmarshalString(rawSDP); errUnmarshal != nil {
		t.Fatalf("unmarshal test SDP: %v", errUnmarshal)
	}
	rewritten := 0
	for _, media := range description.MediaDescriptions {
		for index := range media.Attributes {
			attribute := &media.Attributes[index]
			if !attribute.IsICECandidate() {
				continue
			}
			fields := strings.Fields(attribute.Value)
			if len(fields) < 8 || !strings.EqualFold(fields[2], "tcp") || !strings.Contains(attribute.Value, "tcptype passive") {
				continue
			}
			fields[4] = address
			fields[5] = strconv.Itoa(port)
			attribute.Value = strings.Join(fields, " ")
			rewritten++
		}
	}
	if rewritten == 0 {
		t.Fatal("test SDP has no passive TCP candidate")
	}
	marshaled, errMarshal := description.Marshal()
	if errMarshal != nil {
		t.Fatalf("marshal test SDP: %v", errMarshal)
	}
	return string(marshaled)
}

func bundledICECredentialsFromString(rawSDP string) (iceCredentials, error) {
	var description sdp.SessionDescription
	if errUnmarshal := description.UnmarshalString(rawSDP); errUnmarshal != nil {
		return iceCredentials{}, errUnmarshal
	}
	return bundledICECredentials(&description)
}
