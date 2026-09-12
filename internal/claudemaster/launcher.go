package claudemaster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// NativeClaudeVersion is the reviewed native endpoint/settings contract. Advancing it needs proof.
const NativeClaudeVersion = "2.1.269"

// Launch starts the native master with its existing personal Claude login and a process-only proxy.
// The caller must hold the profile lock until this returns. It never edits Claude configuration.
func Launch(ctx context.Context, profile Profile, model string, args []string) (int, error) {
	if strings.TrimSpace(model) == "" {
		return 1, errors.New("an explicit backend model is required")
	}
	if _, err := ChildEnvironment(os.Environ(), args, "http://127.0.0.1:1", "/unused"); err != nil {
		return 1, err
	}
	if err := ValidateNativeSettings(); err != nil {
		return 1, err
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return 1, errors.New("native Claude Code is not installed on PATH")
	}
	if err := verifyNativeVersion(ctx, bin); err != nil {
		return 1, err
	}
	certs, err := newProcessCertificate()
	if err != nil {
		return 1, err
	}
	defer func() { _ = os.RemoveAll(certs.dir) }()
	backend, err := NewBackend(ctx, BackendOptions{AuthDir: profile.AuthDir, Provider: profile.Provider, AuthID: profile.AuthID, Model: model})
	if err != nil {
		return 1, errors.New("cannot start selected inference backend; check the profile and model")
	}
	defer func() { _ = backend.Close() }()
	proxy, err := StartProxy(ProxyOptions{Certificate: certs.leaf, Inference: backend.Handler()})
	if err != nil {
		return 1, errors.New("cannot start private inference proxy")
	}
	defer func() { _ = proxy.Close() }()
	env, err := ChildEnvironment(os.Environ(), args, proxy.URL(), certs.caPath)
	if err != nil {
		return 1, err
	}
	child := exec.CommandContext(ctx, bin, args...)
	child.Cancel = func() error { return child.Process.Signal(syscall.SIGTERM) }
	// This bounds process shutdown only; inference streams have no post-connect wall-clock timeout.
	child.WaitDelay = 5 * time.Second
	child.Env = env
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := child.Run(); err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			code := exited.ExitCode()
			if code < 0 {
				code = 130
			}
			return code, nil
		}
		return 1, errors.New("native Claude could not start")
	}
	return 0, nil
}

// ChildEnvironment preserves the master login while denying configuration that bypasses the
// process proxy or switches Claude to an API/third-party mode that disables native Remote Control.
// The proxy URL contains a private capability and must never be printed or placed in argv.
func ChildEnvironment(environ, args []string, proxyURL, caPath string) ([]string, error) {
	removed := map[string]bool{
		"HTTPS_PROXY": true, "https_proxy": true, "HTTP_PROXY": true, "http_proxy": true,
		"ALL_PROXY": true, "all_proxy": true, "NO_PROXY": true, "no_proxy": true,
		"NODE_EXTRA_CA_CERTS": true, "CLAUDE_CODE_CHILD_SESSION": true,
		"CLAUDE_CODE_SESSION_ID": true, "REMOTE_CLAW_SECRET_FILE": true,
		"VERCEL_AUTOMATION_BYPASS_SECRET": true,
	}
	for _, arg := range args {
		flag := strings.SplitN(arg, "=", 2)[0]
		switch flag {
		case "--settings", "--setting-sources", "--sdk-url", "--remote-control-session-id", "--claudeai-user-id", "--claudeai-org-id", "--api-key", "--base-url", "--cwd", "--worktree", "-w":
			return nil, errors.New("Claude launcher flags cannot override master identity or provider routing")
		}
	}
	out := make([]string, 0, len(environ)+3)
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if forbiddenProviderEnv(key) && strings.TrimSpace(value) != "" {
			return nil, errors.New("clear custom Claude provider, login-directory, or Node bypass settings before launching")
		}
		if removed[key] || forbiddenProviderEnv(key) {
			continue
		}
		// The selected inference login is held by the backend, never passed into the native master.
		if strings.HasPrefix(key, "CLAUDE_MASTER_") {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, "HTTPS_PROXY="+proxyURL, "https_proxy="+proxyURL, "NODE_EXTRA_CA_CERTS="+caPath)
	return out, nil
}

func verifyNativeVersion(ctx context.Context, bin string) error {
	cmd := exec.CommandContext(ctx, bin, "--version")
	var output boundedOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil || !supportedNativeVersion(output.String()) {
		return errors.New("native Claude version is unsupported; this launcher requires verified Claude Code " + NativeClaudeVersion)
	}
	return nil
}

func supportedNativeVersion(output string) bool {
	text := strings.TrimSpace(output)
	return text == NativeClaudeVersion || text == NativeClaudeVersion+" (Claude Code)"
}

type processCertificate struct {
	dir    string
	caPath string
	leaf   tls.Certificate
}

func newProcessCertificate() (*processCertificate, error) {
	dir, err := os.MkdirTemp("", "claude-master-certs-")
	if err != nil {
		return nil, errors.New("cannot create private process certificate directory")
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("cannot create process CA key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, errors.New("cannot create certificate serial")
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "claude-master process CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(7 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, errors.New("cannot create process CA certificate")
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err := writePrivateFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})); err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("cannot create process leaf key")
	}
	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, errors.New("cannot create certificate serial")
	}
	leafTemplate := &x509.Certificate{SerialNumber: leafSerial, Subject: pkix.Name{CommonName: "api.anthropic.com"}, DNSNames: []string{"api.anthropic.com"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(7 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, errors.New("cannot create process leaf certificate")
	}
	// Private CA and leaf keys remain only in this process. Only the scoped CA certificate is on disk.
	cleanup = false
	return &processCertificate{dir: dir, caPath: caPath, leaf: tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}, nil
}
