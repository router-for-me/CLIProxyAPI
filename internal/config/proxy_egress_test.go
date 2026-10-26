package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestProxyEgressConfigLoadLogsInvalidGlobalProxy(t *testing.T) {
	hook := logtest.NewGlobal()
	defer hook.Reset()
	for raw, want := range map[string]int{"ftp://u:secretpw@h:1": 1, "http://h:8080": 0, "direct": 0, "": 0} {
		hook.Reset()
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("proxy-url: \""+raw+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil || cfg == nil {
			t.Fatalf("proxy %q: load refused: %v", raw, err)
		}
		n := 0
		for _, e := range hook.AllEntries() {
			if e.Level == log.ErrorLevel && strings.Contains(e.Message, "proxy") {
				n++
				if strings.Contains(e.Message, "secretpw") {
					t.Errorf("log line leaks userinfo: %s", e.Message)
				}
			}
		}
		if n != want {
			t.Errorf("proxy %q: %d proxy Error lines, want %d", raw, n, want)
		}
	}
}
