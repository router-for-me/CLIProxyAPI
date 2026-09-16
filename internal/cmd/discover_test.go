package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/discovery"
)

type fakeBrowser struct {
	result      []discovery.DiscoveredService
	err         error
	serviceType string
	deadline    time.Duration
}

func (f *fakeBrowser) Browse(context.Context, string, string) ([]discovery.DiscoveredService, error) {
	return f.result, f.err
}

func (f *fakeBrowser) BrowseWithFallback(context.Context) ([]discovery.DiscoveredService, error) {
	return f.result, f.err
}

func (f *fakeBrowser) BrowseWithFallbackServiceType(ctx context.Context, serviceType string) ([]discovery.DiscoveredService, error) {
	f.serviceType = serviceType
	if deadline, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(deadline)
	}
	return f.result, f.err
}

func TestRunDiscoverJSONEmpty(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDiscover(0, true, &stdout, &stderr, func() (discovery.Browser, error) {
		return &fakeBrowser{result: nil}, nil
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var got []discovery.DiscoveredService
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v\nstdout=%s", err, stdout.String())
	}
	if len(got) != 0 {
		t.Fatalf("gateways = %#v, want empty array", got)
	}
}

func TestRunDiscoverCustomServiceType(t *testing.T) {
	var stdout, stderr bytes.Buffer
	browser := &fakeBrowser{}
	code := runDiscoverWithServiceType(2*time.Second, true, "_custom._tcp", &stdout, &stderr, func() (discovery.Browser, error) {
		return browser, nil
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if browser.serviceType != "_custom._tcp" {
		t.Fatalf("service type = %q, want _custom._tcp", browser.serviceType)
	}
}

func TestRunDiscoverUsesExactTimeout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	browser := &fakeBrowser{}
	code := runDiscoverWithServiceType(2*time.Second, true, "", &stdout, &stderr, func() (discovery.Browser, error) {
		return browser, nil
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if browser.deadline < 1500*time.Millisecond || browser.deadline > 2500*time.Millisecond {
		t.Fatalf("browse deadline remaining = %v, want ~2s", browser.deadline)
	}
}

func TestResolveDiscoveryInterfaceFiltersPrefersCLI(t *testing.T) {
	include, exclude := ResolveDiscoveryInterfaceFilters([]string{"docker0"}, nil, []string{"en0"}, []string{"awdl0"})
	if len(include) != 1 || include[0] != "docker0" || len(exclude) != 0 {
		t.Fatalf("cli filters = include %v exclude %v", include, exclude)
	}
	include, exclude = ResolveDiscoveryInterfaceFilters(nil, nil, []string{"docker0"}, []string{"veth0"})
	if len(include) != 1 || include[0] != "docker0" || len(exclude) != 1 || exclude[0] != "veth0" {
		t.Fatalf("config filters = include %v exclude %v", include, exclude)
	}
}

func TestLoadDiscoveryScanFiltersIgnoresUnrelatedConfigWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "redis-usage-queue-retention-seconds: 99999\ndiscovery:\n  interfaces:\n    include:\n      - docker0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	include, exclude := LoadDiscoveryScanFilters(path)
	if len(include) != 1 || include[0] != "docker0" || len(exclude) != 0 {
		t.Fatalf("loaded filters = include %v exclude %v", include, exclude)
	}
}

func TestLoadDiscoveryScanFiltersReadsInclude(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("discovery:\n  interfaces:\n    include:\n      - docker0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	include, exclude := LoadDiscoveryScanFilters(path)
	if len(include) != 1 || include[0] != "docker0" || len(exclude) != 0 {
		t.Fatalf("loaded filters = include %v exclude %v", include, exclude)
	}
}

func TestLoadDiscoveryScanFiltersEmptyPathUsesWorkingConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("discovery:\n  interfaces:\n    include:\n      - tailscale0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	include, exclude := LoadDiscoveryScanFilters("")
	if len(include) != 1 || include[0] != "tailscale0" || len(exclude) != 0 {
		t.Fatalf("empty-path filters = include %v exclude %v", include, exclude)
	}
}

func TestPreferredDisplayAddressesSkipsBareLinkLocal(t *testing.T) {
	primary, all := preferredDisplayAddresses(discovery.DiscoveredService{
		Host: "gateway.local.",
		IPv6: []net.IP{net.ParseIP("fe80::1")},
	})
	if primary != "gateway.local" {
		t.Fatalf("primary = %q, want hostname", primary)
	}
	if len(all) != 2 || all[0] != "gateway.local" || all[1] != "fe80::1" {
		t.Fatalf("addresses = %v", all)
	}
	primary, all = preferredDisplayAddresses(discovery.DiscoveredService{
		Host: "\x1b[31mevil.local.",
		IPv6: []net.IP{net.ParseIP("fe80::1")},
	})
	if primary != "fe80::1" {
		t.Fatalf("unsafe hostname primary = %q, want link-local fallback", primary)
	}
	if strings.Contains(primary, "\x1b") || strings.Contains(strings.Join(all, ","), "\x1b") || strings.Contains(strings.Join(all, ","), "evil") {
		t.Fatalf("unsanitized hostname leaked: %q %v", primary, all)
	}
	primary, _ = preferredDisplayAddresses(discovery.DiscoveredService{
		IPv6: []net.IP{net.ParseIP("fe80::1"), net.ParseIP("2001:db8::10")},
	})
	if primary != "2001:db8::10" {
		t.Fatalf("primary = %q, want routable IPv6", primary)
	}
}

func TestRunDiscoverJSONError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDiscover(2*time.Second, true, &stdout, &stderr, func() (discovery.Browser, error) {
		return nil, errNoIfaces
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("json: %v\nstdout=%s", err, stdout.String())
	}
	if payload["error"] == nil {
		t.Fatalf("expected error field, got %#v", payload)
	}
}

func TestRunDiscoverClampsTimeoutAndPrintsText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDiscover(2*time.Minute, false, &stdout, &stderr, func() (discovery.Browser, error) {
		return &fakeBrowser{result: []discovery.DiscoveredService{{
			InstanceName: "office-gateway",
			Product:      "cliproxyapi",
			Version:      "1",
			Port:         8317,
			IPv4:         []net.IP{net.ParseIP("192.0.2.10")},
			Endpoints:    map[string]string{"openai": "/v1"},
			RawTXT:       map[string]string{"tls": "0"},
		}}}, nil
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "timeout 1m0s") {
		t.Fatalf("expected timeout clamped to 60s, got %q", out)
	}
	if !strings.Contains(out, "office-gateway") {
		t.Fatalf("expected instance name in output, got %q", out)
	}
}

type staticError string

func (e staticError) Error() string { return string(e) }

const errNoIfaces staticError = "no qualified physical interfaces found for LAN discovery"
