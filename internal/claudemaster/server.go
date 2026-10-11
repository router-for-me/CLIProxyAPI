package claudemaster

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// ServeOptions configures a shared claude-master server: one box that holds the subscription logins
// and routes inference for every client box that presents a certificate it issued.
type ServeOptions struct {
	LaunchOptions
	Listen string // a specific private IP and port, e.g. 10.0.0.10:8443
	// OpenLoopback, when set, also serves a plain-HTTP listener with NO client certificate on this
	// loopback address (e.g. 127.0.0.1:8444), for clients that arrive through an authenticating
	// tunnel. Whatever can reach that address is trusted.
	OpenLoopback string
	// SnapshotInterval is how often the quota and proxy summaries are logged (default 5 minutes).
	SnapshotInterval time.Duration
	// DrainTimeout is how long a stopping server lets running requests finish before it cancels
	// them (default 60 seconds; systemd's default stop timeout is 90, so raise TimeoutStopSec with it).
	DrainTimeout time.Duration
	// Balanced says a load balancer in front stops sending new connections here once a rollout
	// begins: a draining server then serves requests on its existing connections, each closing its
	// connection, instead of answering them with a retryable 529.
	Balanced bool
	StateDir string    // the server's CA lives here (private, 0700)
	Out      io.Writer // one status line; never a secret
}

// Serve runs the proxy for other boxes until ctx ends. The caller holds every profile lock.
func Serve(ctx context.Context, profiles []Profile, opts ServeOptions) error {
	ip, _, err := privateEndpoint(opts.Listen)
	if err != nil {
		return fmt.Errorf("--listen: %w", err)
	}
	if opts.OpenLoopback != "" {
		if _, _, err := loopbackEndpoint(opts.OpenLoopback); err != nil {
			return fmt.Errorf("--open-loopback: %w", err)
		}
	}
	certs, err := loadOrCreatePersistentCertificate(opts.StateDir, serverNames(ip))
	if err != nil {
		return err
	}
	launch := opts.LaunchOptions
	if launch.SnapshotInterval == 0 {
		launch.SnapshotInterval = opts.SnapshotInterval
	}
	backend, err := newInferenceBackend(serveBackendLifetime(ctx), profiles, launch)
	if err != nil {
		return errors.New("cannot start the inference backend; check the profiles")
	}
	defer func() { _ = backend.Close() }()
	proxy, err := startServerProxy(certs, opts.Listen, opts.OpenLoopback, backend.Handler())
	if err != nil {
		return err
	}
	defer func() { _ = proxy.Close() }()
	proxy.setBalanced(opts.Balanced)
	lg().Info("claude-master server started", "listen", proxy.Addr(), "open_loopback", proxy.OpenAddr(), "api_backup", opts.BackupAPIKey != "", "balanced", opts.Balanced)
	summaryDone := startProxySummary(ctx, proxy, opts.SnapshotInterval)
	defer func() { <-summaryDone; lg().Info("claude-master server stopped") }()
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "claude-master serving on %s; clients trust %s\n", proxy.Addr(), certs.caPath)
		if proxy.OpenAddr() != "" {
			fmt.Fprintf(opts.Out, "claude-master also serving WITHOUT client certificates on %s (loopback only): put an authenticating tunnel in front of it\n", proxy.OpenAddr())
		}
	}
	<-ctx.Done()
	drain := opts.DrainTimeout
	if drain <= 0 {
		drain = defaultDrainTimeout
	}
	proxy.Drain(drain)
	return nil
}

const defaultDrainTimeout = 60 * time.Second

// serveBackendLifetime keeps ctx's values but not its cancellation. Every request handler stops
// when the backend's lifetime ends, and ctx ends on the signal that stops the server, so with ctx
// itself a SIGTERM cancelled every running request before the drain began: on 2026-10-10 06:57 the
// drain found 2 running and "waited 2ms". The backend now stops when Serve's deferred Close cancels
// it, which runs after the drain.
func serveBackendLifetime(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

// serverNames are the addresses the server's certificate vouches for: the one it listens on, and
// loopback, so a client can reach it through a local tunnel (an SSH or SSM port forward, a
// cloudflared access tunnel) by dialling 127.0.0.1 and still verify who it is talking to.
func serverNames(listen net.IP) []net.IP {
	loopback := net.IPv4(127, 0, 0, 1)
	if listen.Equal(loopback) {
		return []net.IP{loopback}
	}
	return []net.IP{listen, loopback}
}

func startServerProxy(certs *processCertificate, listen, openLoopback string, inference http.Handler) (*Proxy, error) {
	return StartProxy(ProxyOptions{
		GetCertificate: certs.getCertificate, Inference: inference,
		ProxyCertificate: certs.proxyServerCertificate, ClientCAs: certs.clientPool(), Listen: listen,
		OpenListen: openLoopback,
	})
}

// ConnectOptions points a box's Claude at a claude-master server.
type ConnectOptions struct {
	Server string // the server's private ADDRESS:PORT
	Dir    string // client.key, client.pem and ca.pem (see LoadClientIdentity)
	// Open instead reaches the server's open loopback listener (no client certificate) at this
	// loopback ADDRESS:PORT, typically the local end of an authenticating tunnel; CAFile is the
	// server's public ca.pem. Server and Dir are then unused.
	Open   string
	CAFile string
	Out    io.Writer
}

// Connect runs the native Claude through a claude-master server. It holds no profile and never falls
// back to this box's own login: a server that cannot be reached is an error.
func Connect(ctx context.Context, opts ConnectOptions, args []string) (int, error) {
	if opts.Open != "" {
		return connectOpen(ctx, opts, args)
	}
	ip, port, err := privateEndpoint(opts.Server)
	if err != nil {
		return 2, fmt.Errorf("--server: %w", err)
	}
	identity, err := LoadClientIdentity(opts.Dir)
	if err != nil {
		return 1, err
	}
	if left := time.Until(identity.Expires); left < 3*24*time.Hour && opts.Out != nil {
		fmt.Fprintf(opts.Out, "claude-master: this box's certificate expires %s; ask the server for a new one\n", identity.Expires.Format("2006-01-02"))
	}
	if err := checkServer(ctx, ip, port, identity); err != nil {
		return 1, err
	}
	environ, err := launchEnvironment(os.Environ(), "")
	if err != nil {
		return 1, err
	}
	args, err = NativeArguments("claude", args)
	if err != nil {
		return 1, err
	}
	bin, err := preflight(ctx, args, environ)
	if err != nil {
		return 1, err
	}
	config, err := serverTLSConfig(ip, identity)
	if err != nil {
		return 1, err
	}
	return runThroughForwarder(ctx, bin, args, environ, identity.CAPath, tlsUpstream(net.JoinHostPort(ip.String(), port), config))
}

// checkServer completes one TLS handshake with this box's client certificate, so a wrong address, a
// certificate the server does not accept, or a server that is down is reported now and clearly.
func checkServer(ctx context.Context, ip net.IP, port string, identity ClientIdentity) error {
	config, err := serverTLSConfig(ip, identity)
	if err != nil {
		return err
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dialer := &tls.Dialer{Config: config}
	conn, err := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		return serverError(net.JoinHostPort(ip.String(), port), err)
	}
	defer func() { _ = conn.Close() }()
	// With TLS 1.3 the client handshake returns before the server has judged the client certificate:
	// a rejection arrives as an alert on the next read. Ask for one response so an unaccepted
	// certificate is reported here, not later as a confusing failure inside Claude. The proxy answers
	// anything that is not a CONNECT with a plain 400, which is all this needs.
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := probeProxy(conn); err != nil {
		return serverError(net.JoinHostPort(ip.String(), port), err)
	}
	return nil
}

// serverTLSConfig is how this box reaches a server: its own client certificate, the server's CA,
// and the server's address as the name to verify.
func serverTLSConfig(ip net.IP, identity ClientIdentity) (*tls.Config, error) {
	pair, err := tls.LoadX509KeyPair(identity.CertPath, identity.KeyPath)
	if err != nil {
		return nil, errors.New("cannot load this box's client certificate")
	}
	caPEM, err := os.ReadFile(identity.CAPath)
	if err != nil {
		return nil, errors.New("cannot read ca.pem")
	}
	return &tls.Config{RootCAs: x509CertPool(caPEM), Certificates: []tls.Certificate{pair}, ServerName: ip.String(), MinVersion: tls.VersionTLS12}, nil
}

// probeProxy asks the connection for the proxy's own probe response and requires the header only a
// claude-master proxy sets, so "some HTTP service answered" is not mistaken for "the proxy answered".
func probeProxy(conn net.Conn) error {
	if _, err := io.WriteString(conn, "GET "+proxyProbePath+" HTTP/1.1\r\nHost: claude-master\r\nConnection: close\r\n\r\n"); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.Header.Get(proxyProbeHeader) != "1" {
		return errors.New("not a claude-master proxy")
	}
	return nil
}

func serverError(endpoint string, err error) error {
	reason := "is it running, and is this box allowed to reach it?"
	if strings.Contains(err.Error(), "certificate") || strings.Contains(err.Error(), "bad") {
		reason = "it did not accept this box's certificate (expired, or issued by another server)"
	}
	return fmt.Errorf("cannot connect to the claude-master server at %s: %s", endpoint, reason)
}

func x509CertPool(pemData []byte) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemData)
	return pool
}

// connectOpen runs the native Claude through the open loopback listener of a server, which asks for
// no certificate: the trust is the tunnel that leads to it. It only ever talks plain HTTP to a
// LOOPBACK address, so an unauthenticated proxy request never crosses a network.
func connectOpen(ctx context.Context, opts ConnectOptions, args []string) (int, error) {
	ip, port, err := loopbackEndpoint(opts.Open)
	if err != nil {
		return 2, fmt.Errorf("--open: %w", err)
	}
	if opts.CAFile == "" {
		return 2, errors.New("--open needs --ca FILE, the server's public ca.pem")
	}
	if pemData, err := os.ReadFile(opts.CAFile); err != nil || len(x509CertPool(pemData).Subjects()) == 0 { //nolint:staticcheck
		return 1, errors.New("cannot read a CA certificate from --ca")
	}
	endpoint := net.JoinHostPort(ip.String(), port)
	if err := checkOpen(ctx, endpoint); err != nil {
		return 1, err
	}
	environ, err := launchEnvironment(os.Environ(), "")
	if err != nil {
		return 1, err
	}
	args, err = NativeArguments("claude", args)
	if err != nil {
		return 1, err
	}
	bin, err := preflight(ctx, args, environ)
	if err != nil {
		return 1, err
	}
	return runThroughForwarder(ctx, bin, args, environ, opts.CAFile, plainUpstream(endpoint))
}

// checkOpen asks the open listener for one response, so a tunnel that is down or leads nowhere is
// reported now and clearly instead of as a hang inside Claude.
func checkOpen(ctx context.Context, endpoint string) error {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", endpoint)
	if err != nil {
		return fmt.Errorf("nothing is listening on %s: is the tunnel to the claude-master server running?", endpoint)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := probeProxy(conn); err != nil {
		return fmt.Errorf("%s is not a claude-master proxy: does the tunnel lead to the server's open listener?", endpoint)
	}
	return nil
}

// startProxySummary logs what the proxy did since the last line, every interval (default 5 minutes;
// negative turns it off). The returned channel closes when the goroutine has stopped.
func startProxySummary(ctx context.Context, proxy *Proxy, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	if interval < 0 {
		close(done)
		return done
	}
	if interval == 0 {
		interval = defaultSnapshotInterval
	}
	go func() {
		defer close(done)
		previous := proxy.Snapshot()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := proxy.Snapshot()
				lg().Info("proxy summary", "interval", interval.String(),
					"connects_accepted", now.ConnectAccepted-previous.ConnectAccepted,
					"connects_rejected", now.ConnectRejected-previous.ConnectRejected,
					"api_requests", now.APIRequests-previous.APIRequests,
					"inference_requests", now.InferenceRequests-previous.InferenceRequests,
					"control_requests", now.ControlRequests-previous.ControlRequests,
					"blocked_requests", now.BlockedRequests-previous.BlockedRequests,
					"active_connections", now.ActiveConnections)
				previous = now
			}
		}
	}()
	return done
}
