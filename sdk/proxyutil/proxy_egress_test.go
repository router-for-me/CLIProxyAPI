package proxyutil

import (
	"errors"
	"strings"
	"testing"
)

var egressUnusable = []string{
	"ftp://u:secretpw@127.0.0.1:1",
	"http://u:secretpw@",
	"::garbage::",
	"http://u:secretpw@127.0.0.1:99999",
	"socks5://u:secretpw@:1080",
	"https://u:secretpw@host:0",
	"u:secretpw@host:8080",
}

func TestProxyEgressBuildersReturnTypedError(t *testing.T) {
	for _, raw := range egressUnusable {
		if _, _, err := BuildHTTPTransport(raw); !errors.Is(err, ErrUnusable) {
			t.Errorf("BuildHTTPTransport(%q) err = %v, want ErrUnusable", Redact(raw), err)
		} else if strings.Contains(err.Error(), "secretpw") {
			t.Errorf("BuildHTTPTransport error leaks userinfo: %v", err)
		}
		if _, _, err := BuildDialer(raw); !errors.Is(err, ErrUnusable) {
			t.Errorf("BuildDialer(%q) err = %v, want ErrUnusable", Redact(raw), err)
		} else if strings.Contains(err.Error(), "secretpw") {
			t.Errorf("BuildDialer error leaks userinfo: %v", err)
		}
		if err := Check(raw); !errors.Is(err, ErrUnusable) || strings.Contains(err.Error(), "secretpw") {
			t.Errorf("Check(%q) = %v", Redact(raw), err)
		}
	}
}

func TestProxyEgressUsableValuesStayUsable(t *testing.T) {
	for _, raw := range []string{"", "  ", "direct", "NONE", "http://h:8080", "https://h", "socks5://u:p@h:1080", "socks5://h", "http://h:", "https://h:", "socks5h://h:1"} {
		if err := Check(raw); err != nil {
			t.Errorf("Check(%q) = %v, want nil", raw, err)
		}
	}
	if _, _, err := BuildHTTPTransport("socks5://h"); err != nil {
		t.Errorf("BuildHTTPTransport(socks5://h) = %v, want nil", err)
	}
	if !ValidRequestProxy("socks5://h") { // the plugin tunnel defaults SOCKS5 to port 1080
		t.Error("ValidRequestProxy(socks5://h) = false")
	}
}
