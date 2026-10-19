package clawmaster

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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

// agentTokenName is the agent's token file, beside the CA (--open) or in the client directory.
const agentTokenName = "agent.token"

// agentNoProxy exempts loopback: Claude uses one proxy for every scheme, and the agent only tunnels CONNECT.
const agentNoProxy = "localhost,127.0.0.1,::1"

// agentEndpoint is what an agent needs: how to reach the server, the CA Claude must trust, and its
// own address and token.
type agentEndpoint struct {
	upstream  upstreamDialer
	check     func(context.Context) error
	caPath    string
	listen    string
	token     string
	tokenPath string
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
	return serveAgent(ctx, listener, endpoint, opts.Out)
}

// serveAgent serves on listener, which it owns, until ctx ends.
func serveAgent(ctx context.Context, listener net.Listener, endpoint agentEndpoint, out io.Writer) error {
	if err := endpoint.check(ctx); err != nil {
		lg().Warn("agent: the claw-master server is not reachable now; requests fail until it is", "error", err)
	} else {
		lg().Info("agent: the claw-master server is reachable")
	}
	forwarder := serveLocalForwarderWithToken(listener, endpoint.upstream, "", endpoint.token, true)
	if out != nil {
		fmt.Fprintf(out, "claw-master agent: listening on %s (claw-master agent env prints the settings for Claude)\n", listener.Addr())
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
	proxyURL := (&url.URL{Scheme: "http", User: url.UserPassword(forwarderUser, endpoint.token), Host: endpoint.listen}).String()
	return []string{
		"HTTPS_PROXY=" + proxyURL,
		"NODE_EXTRA_CA_CERTS=" + endpoint.caPath,
		"NO_PROXY=" + agentNoProxy,
	}, nil
}

// agentSettingsAllowance accepts, in a settings file's env, exactly what `agent install` writes
// there for this machine's agent: HTTPS_PROXY to a loopback address with the token in tokenPath,
// NODE_EXTRA_CA_CERTS naming caPath, and NO_PROXY for loopback only. A launch whose Claude follows
// those still reaches only the pool. With no readable token, it accepts nothing.
func agentSettingsAllowance(tokenPath, caPath string) settingsEnvAllowance {
	token, err := readAgentToken(tokenPath)
	if err != nil {
		return nil
	}
	ca, err := filepath.Abs(caPath)
	if err != nil {
		return nil
	}
	return func(key, value string) bool {
		switch key {
		case "HTTPS_PROXY":
			u, err := url.Parse(value)
			if err != nil || u.Scheme != "http" || u.User == nil || u.User.Username() != forwarderUser ||
				(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
				return false
			}
			if password, ok := u.User.Password(); !ok || password != token {
				return false
			}
			_, err = agentAddress(u.Host)
			return err == nil
		case "NODE_EXTRA_CA_CERTS":
			return filepath.IsAbs(value) && filepath.Clean(value) == ca
		case "NO_PROXY":
			return value == agentNoProxy
		}
		return false
	}
}

// agentAddress is a loopback ADDRESS:PORT in one spelling, so equal sockets compare equal and the
// address Claude is given is the one the agent listens on.
func agentAddress(value string) (string, error) {
	ip, port, err := loopbackEndpoint(value)
	if err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", errors.New("the port must be a number from 1 to 65535")
	}
	return net.JoinHostPort(ip.String(), strconv.FormatUint(n, 10)), nil
}

func resolveAgent(opts AgentOptions) (agentEndpoint, error) {
	endpoint, err := resolveAgentPaths(opts)
	if err != nil {
		return agentEndpoint{}, err
	}
	if endpoint.token, err = agentToken(endpoint.tokenPath); err != nil {
		return agentEndpoint{}, err
	}
	return endpoint, nil
}

// resolveAgentPaths is resolveAgent without reading or creating the token.
func resolveAgentPaths(opts AgentOptions) (agentEndpoint, error) {
	listen, err := agentAddress(opts.Listen)
	if err != nil {
		return agentEndpoint{}, fmt.Errorf("--listen: %w", err)
	}
	endpoint := agentEndpoint{listen: listen}
	tokenFile := opts.TokenFile
	if opts.Open != "" {
		address, err := agentAddress(opts.Open)
		if err != nil {
			return agentEndpoint{}, fmt.Errorf("--open: %w", err)
		}
		if opts.CAFile == "" {
			return agentEndpoint{}, errors.New("--open needs --ca FILE, the server's public ca.pem")
		}
		if pemData, err := os.ReadFile(opts.CAFile); err != nil || len(x509CertPool(pemData).Subjects()) == 0 { //nolint:staticcheck
			return agentEndpoint{}, errors.New("cannot read a CA certificate from --ca")
		}
		if address == endpoint.listen {
			return agentEndpoint{}, errors.New("--listen and --open must differ")
		}
		endpoint.upstream = plainUpstream(address)
		endpoint.check = func(ctx context.Context) error { return checkOpen(ctx, address) }
		endpoint.caPath = opts.CAFile
		if tokenFile == "" {
			tokenFile = filepath.Join(filepath.Dir(opts.CAFile), agentTokenName)
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
			tokenFile = filepath.Join(opts.Dir, agentTokenName)
		}
	}
	if endpoint.caPath, err = filepath.Abs(endpoint.caPath); err != nil {
		return agentEndpoint{}, err
	}
	if endpoint.tokenPath, err = filepath.Abs(tokenFile); err != nil {
		return agentEndpoint{}, err
	}
	return endpoint, nil
}

// agentToken reads the agent's token, creating it on first use. The file must be a regular file only
// its owner can read: the token lets anything that holds it use the pool. A new token is written to a
// temporary file and linked into place, so no reader sees a partial one and concurrent starts agree.
func agentToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("no token file")
	}
	if token, err := readAgentToken(path); !errors.Is(err, fs.ErrNotExist) {
		return token, err
	}
	temp, err := createPrivateTemp(filepath.Dir(path), ".agent.token-")
	if err != nil {
		return "", fmt.Errorf("cannot create the agent token %s: %w", path, err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	_, errWrite := temp.WriteString(rand.Text() + "\n")
	errSync := temp.Sync()
	errClose := temp.Close()
	if err := errors.Join(errWrite, errSync, errClose); err != nil {
		return "", fmt.Errorf("cannot write the agent token %s: %w", path, err)
	}
	if err := os.Link(temp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("cannot create the agent token %s: %w", path, err)
	}
	return readAgentToken(path) // this start's token, or the one a concurrent start linked first
}

func readAgentToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the agent token %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() > 256 {
		return "", fmt.Errorf("the agent token %s is not a small regular file", path)
	}
	if readableByOthers(path, info) {
		return "", fmt.Errorf("the agent token %s can be read by others; make it private (chmod 600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read the agent token %s: %w", path, err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 16 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return "", fmt.Errorf("the agent token %s is malformed (16 or more letters, digits, - or _); delete it and start the agent again", path)
	}
	return token, nil
}
