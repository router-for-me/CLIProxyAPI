package claudemaster

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"
)

func environmentMap(env []string) map[string]string {
	out := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		out[key] = value
	}
	return out
}

func TestChildEnvironmentPreservesMasterAndScopesProxy(t *testing.T) {
	env, err := ChildEnvironment([]string{"PATH=/usr/bin", "USER=alice", "HTTPS_PROXY=http://old", "NO_PROXY=*", "no_proxy=api.anthropic.com", "CLAUDE_CODE_CHILD_SESSION=parent", "CLAUDE_CODE_SESSION_ID=old", "REMOTE_CLAW_SECRET_FILE=private", "CLAUDE_MASTER_TEST_SECRET=secret"}, []string{"--remote-control"}, "http://private-capability@127.0.0.1:1234", "/process/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	got := environmentMap(env)
	for _, key := range []string{"NO_PROXY", "no_proxy", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID", "REMOTE_CLAW_SECRET_FILE", "CLAUDE_MASTER_TEST_SECRET"} {
		if _, ok := got[key]; ok {
			t.Errorf("leaked %s", key)
		}
	}
	if got["PATH"] != "/usr/bin" || got["USER"] != "alice" || got["HTTPS_PROXY"] != got["https_proxy"] || got["NODE_EXTRA_CA_CERTS"] != "/process/ca.pem" {
		t.Fatal("incorrect child environment")
	}
}

func TestChildEnvironmentRejectsBypassConfiguration(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CONFIG_DIR", "NODE_OPTIONS", "NODE_TLS_REJECT_UNAUTHORIZED"} {
		t.Run(key, func(t *testing.T) {
			_, err := ChildEnvironment([]string{key + "=sensitive-canary"}, nil, "proxy", "ca")
			if err == nil {
				t.Fatal("accepted bypass")
			}
			if strings.Contains(err.Error(), "sensitive-canary") {
				t.Fatal("secret leaked in error")
			}
		})
	}
	for _, arg := range []string{"--settings=canary", "--setting-sources", "--sdk-url=wss://bad", "--api-key=canary", "--base-url=https://bad"} {
		if _, err := ChildEnvironment(nil, []string{arg}, "proxy", "ca"); err == nil {
			t.Errorf("accepted flag %s", arg)
		}
	}
}

func TestProcessCertificateOnlyTrustsAnthropicAndHasNoDiskKeys(t *testing.T) {
	certs, err := newProcessCertificate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(certs.dir) })
	st, err := os.Stat(certs.dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatal("certificate directory is not private")
	}
	st, err = os.Stat(certs.caPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("certificate is not private")
	}
	entries, err := os.ReadDir(certs.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "ca.pem" {
		t.Fatal("unexpected private key or artifact on disk")
	}
	data, err := os.ReadFile(certs.caPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("missing CA")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certs.leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "api.anthropic.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "other.example"}); err == nil {
		t.Fatal("certificate covers an unrelated host")
	}
}
