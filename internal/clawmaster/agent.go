package clawmaster

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// AgentOptions configures a long-running forwarder for Claude Code that is not started by a launcher
// (the Code tab of the Claude desktop app runs its own copy of Claude Code). It reaches the server as
// Connect does: Server and Dir for a client certificate, or Open and CAFile through a tunnel.
type AgentOptions struct {
	Listen    string // a literal loopback ADDRESS:PORT
	Server    string
	Dir       string
	Open      string
	CAFile    string
	TokenFile string // the agent's token; created on first use (default: agent.token beside the CA)
	Out       io.Writer
}

// agentEndpoint is what an agent needs: how to reach the server, the CA Claude must trust, and its
// own address and token.
type agentEndpoint struct {
	upstream upstreamDialer
	check    func(context.Context) error
	caPath   string
	listen   string
	token    string
}

// RunAgent serves until ctx ends. Like Connect it never falls back to this box's own login: while
// the server cannot be reached, Claude's requests to api.anthropic.com fail (502), and a request
// without the agent's token is refused (407) rather than sent to Anthropic directly; both are logged.
func RunAgent(ctx context.Context, opts AgentOptions) error {
	endpoint, err := resolveAgent(opts)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", endpoint.listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", endpoint.listen, err)
	}
	if err := endpoint.check(ctx); err != nil {
		lg().Warn("agent: the claw-master server is not reachable now; requests fail until it is", "error", err)
	} else {
		lg().Info("agent: the claw-master server is reachable")
	}
	forwarder := serveLocalForwarderWithToken(listener, endpoint.upstream, "", endpoint.token, true)
	if opts.Out != nil {
		fmt.Fprintf(opts.Out, "claw-master agent: listening on %s (claw-master agent env prints the settings for Claude)\n", listener.Addr())
	}
	<-ctx.Done()
	return forwarder.Close()
}

// AgentEnvironment is what Claude needs to use a running agent, as NAME=VALUE lines: the proxy URL
// (it carries the agent's token, a private capability), the CA to trust, and NO_PROXY for loopback,
// because Claude uses one proxy for every scheme and the agent only tunnels CONNECT.
func AgentEnvironment(opts AgentOptions) ([]string, error) {
	endpoint, err := resolveAgent(opts)
	if err != nil {
		return nil, err
	}
	proxyURL := "http://" + forwarderUser + ":" + endpoint.token + "@" + endpoint.listen
	return []string{
		"HTTPS_PROXY=" + proxyURL,
		"NODE_EXTRA_CA_CERTS=" + endpoint.caPath,
		"NO_PROXY=localhost,127.0.0.1,::1",
	}, nil
}

func resolveAgent(opts AgentOptions) (agentEndpoint, error) {
	ip, port, err := loopbackEndpoint(opts.Listen)
	if err != nil {
		return agentEndpoint{}, fmt.Errorf("--listen: %w", err)
	}
	endpoint := agentEndpoint{listen: net.JoinHostPort(ip.String(), port)}
	tokenFile := opts.TokenFile
	if opts.Open != "" {
		openIP, openPort, err := loopbackEndpoint(opts.Open)
		if err != nil {
			return agentEndpoint{}, fmt.Errorf("--open: %w", err)
		}
		if opts.CAFile == "" {
			return agentEndpoint{}, errors.New("--open needs --ca FILE, the server's public ca.pem")
		}
		if pemData, err := os.ReadFile(opts.CAFile); err != nil || len(x509CertPool(pemData).Subjects()) == 0 { //nolint:staticcheck
			return agentEndpoint{}, errors.New("cannot read a CA certificate from --ca")
		}
		address := net.JoinHostPort(openIP.String(), openPort)
		if address == endpoint.listen {
			return agentEndpoint{}, errors.New("--listen and --open must differ")
		}
		endpoint.upstream = plainUpstream(address)
		endpoint.check = func(ctx context.Context) error { return checkOpen(ctx, address) }
		endpoint.caPath = opts.CAFile
		if tokenFile == "" {
			tokenFile = filepath.Join(filepath.Dir(opts.CAFile), "agent.token")
		}
	} else {
		if opts.Server == "" || opts.Dir == "" {
			return agentEndpoint{}, errors.New("give --server and --dir, or --open and --ca")
		}
		serverIP, serverPort, err := privateEndpoint(opts.Server)
		if err != nil {
			return agentEndpoint{}, fmt.Errorf("--server: %w", err)
		}
		identity, err := LoadClientIdentity(opts.Dir)
		if err != nil {
			return agentEndpoint{}, err
		}
		config, err := serverTLSConfig(serverIP, identity)
		if err != nil {
			return agentEndpoint{}, err
		}
		endpoint.upstream = tlsUpstream(net.JoinHostPort(serverIP.String(), serverPort), config)
		endpoint.check = func(ctx context.Context) error { return checkServer(ctx, serverIP, serverPort, identity) }
		endpoint.caPath = identity.CAPath
		if tokenFile == "" {
			tokenFile = filepath.Join(opts.Dir, "agent.token")
		}
	}
	if endpoint.caPath, err = filepath.Abs(endpoint.caPath); err != nil {
		return agentEndpoint{}, err
	}
	if endpoint.token, err = agentToken(tokenFile); err != nil {
		return agentEndpoint{}, err
	}
	return endpoint, nil
}

// agentToken reads the agent's token, creating it on first use. The file must be a regular file only
// its owner can read: the token lets anything that holds it use the pool.
func agentToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("no token file")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		token := rand.Text()
		_, errWrite := file.WriteString(token + "\n")
		errClose := file.Close()
		if errWrite != nil || errClose != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("cannot write the agent token %s", path)
		}
		return token, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("cannot create the agent token %s: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the agent token %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > 256 {
		return "", fmt.Errorf("the agent token %s is not a small regular file", path)
	}
	if agentTokenTooOpen(info) {
		return "", fmt.Errorf("the agent token %s can be read by others; make it private (chmod 600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the agent token %s: %w", path, err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 16 || strings.ContainsAny(token, ":@/ \t\r\n") {
		return "", fmt.Errorf("the agent token %s is malformed; delete it and start the agent again", path)
	}
	return token, nil
}

// agentTokenTooOpen reports permission bits that let others read the token. Windows has no such
// bits (os reports 0666 or 0444); there the file's ACL, inherited from the user's profile, decides.
func agentTokenTooOpen(info fs.FileInfo) bool {
	return os.PathSeparator == '/' && info.Mode().Perm()&0o077 != 0
}
