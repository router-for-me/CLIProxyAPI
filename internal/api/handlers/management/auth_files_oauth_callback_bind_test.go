package management

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestCallbackForwarderHonorsLoopbackBindAndPreservesRedirect(t *testing.T) {
	// Calibrate the alternate address: macOS may not route 127.0.0.2.
	baseline, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer baseline.Close()
	addresses := []string{"127.0.0.2"}
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range interfaces {
		if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil && !network.IP.IsLoopback() {
			addresses = append(addresses, network.IP.String())
		}
	}
	var alternate string
	for _, address := range addresses {
		connection, err := net.DialTimeout("tcp4", net.JoinHostPort(address, strconv.Itoa(baseline.Addr().(*net.TCPAddr).Port)), time.Second)
		if err == nil {
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
			alternate = address
			break
		}
	}
	if alternate == "" {
		t.Fatal("no alternate local IPv4 address reachable through wildcard listener")
	}
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	target := "http://127.0.0.1:8317/codex/callback?existing=value"
	forwarder, err := startCallbackForwarder("127.0.0.1", port, "bind-test", target)
	if err != nil {
		t.Fatal(err)
	}
	defer stopCallbackForwarderInstance(port, forwarder)
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/auth/callback?code=test-code&state=test-state")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Error(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != target+"&code=test-code&state=test-state" {
		t.Fatalf("redirect status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("OAuth redirect must not be cached")
	}
	other, err := net.DialTimeout("tcp4", net.JoinHostPort(alternate, strconv.Itoa(port)), time.Second)
	if err == nil {
		if err := other.Close(); err != nil {
			t.Error(err)
		}
		t.Fatalf("callback configured for 127.0.0.1 accepted connection on %s", alternate)
	}
}
