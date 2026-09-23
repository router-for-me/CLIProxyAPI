package cliproxy

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/discovery"
)

type fakeAdvertiser struct {
	mu         sync.Mutex
	starts     int
	stops      int
	spec       discovery.ServiceSpec
	startErr   error
	entered    chan struct{}
	blockStart chan struct{}
}

func (f *fakeAdvertiser) Start(ctx context.Context, spec discovery.ServiceSpec) error {
	if f.entered != nil {
		select {
		case <-f.entered:
		default:
			close(f.entered)
		}
	}
	if f.blockStart != nil {
		select {
		case <-f.blockStart:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.spec = spec
	return f.startErr
}

func (f *fakeAdvertiser) Stop() error {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
	return nil
}

func (f *fakeAdvertiser) snapshot() (starts, stops int, spec discovery.ServiceSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops, f.spec
}

func newTestDiscoveryManager() *discoveryAdvertiserManager {
	mgr := newDiscoveryAdvertiserManager()
	mgr.refreshInterval = -1
	return mgr
}

func TestSpecEqualDetectsAdvertisedIPChange(t *testing.T) {
	a := discovery.ServiceSpec{InstanceName: "n", Port: 8317, AdvertisedIPs: []string{"192.0.2.10"}}
	b := discovery.ServiceSpec{InstanceName: "n", Port: 8317, AdvertisedIPs: []string{"192.0.2.11"}}
	if specEqual(a, a) != true {
		t.Fatal("expected identical specs to be equal")
	}
	if specEqual(a, b) {
		t.Fatal("expected advertised IP change to be detected")
	}
}

func TestSpecEqualDetectsInterfaceIndexChange(t *testing.T) {
	a := discovery.ServiceSpec{InstanceName: "n", Port: 8317, Interfaces: []net.Interface{{Index: 1, Name: "en0"}}}
	b := discovery.ServiceSpec{InstanceName: "n", Port: 8317, Interfaces: []net.Interface{{Index: 2, Name: "en0"}}}
	if !specEqual(a, a) {
		t.Fatal("expected identical interface specs to be equal")
	}
	if specEqual(a, b) {
		t.Fatal("expected interface index change to be detected")
	}
}

func TestDiscoveryManagerStartsOnceForUnchangedSpec(t *testing.T) {
	mgr := newTestDiscoveryManager()
	adv := &fakeAdvertiser{}
	mgr.newAdvertiser = func() discovery.Advertiser { return adv }
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		return discovery.ServiceSpec{InstanceName: "n", Port: 8317, AdvertisedIPs: []string{"192.0.2.10"}}, nil
	}
	cfg := &config.Config{}
	cfg.Discovery.Enabled = true

	if !mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("first apply failed")
	}
	if !mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("second apply failed")
	}
	starts, stops, _ := adv.snapshot()
	if starts != 1 || stops != 0 {
		t.Fatalf("starts=%d stops=%d, want starts=1 stops=0", starts, stops)
	}
}

func TestDiscoveryManagerPreservesBoundEndpointOnReload(t *testing.T) {
	mgr := newTestDiscoveryManager()
	adv := &fakeAdvertiser{}
	mgr.newAdvertiser = func() discovery.Advertiser { return adv }
	var gotHost string
	var gotPort int
	var gotTLS bool
	mgr.buildSpec = func(cfg *config.Config, port int, tlsEnabled bool) (discovery.ServiceSpec, error) {
		gotHost = cfg.Host
		gotPort = port
		gotTLS = tlsEnabled
		return discovery.ServiceSpec{InstanceName: "n", Port: port}, nil
	}
	first := &config.Config{}
	first.Host = ""
	first.Discovery.Enabled = true
	if !mgr.ApplyContext(context.Background(), first, 8317, false) {
		t.Fatal("initial apply failed")
	}
	second := &config.Config{}
	second.Host = "192.0.2.20"
	second.Discovery.Enabled = true
	if !mgr.ApplyContext(context.Background(), second, 9999, true) {
		t.Fatal("reload apply failed")
	}
	if gotHost != "" || gotPort != 8317 || gotTLS {
		t.Fatalf("reloaded endpoint = host %q port %d tls %t, want original endpoint", gotHost, gotPort, gotTLS)
	}
}

func TestDiscoveryManagerRestartsOnIPChange(t *testing.T) {
	mgr := newTestDiscoveryManager()
	var created []*fakeAdvertiser
	mgr.newAdvertiser = func() discovery.Advertiser {
		adv := &fakeAdvertiser{}
		created = append(created, adv)
		return adv
	}
	ips := []string{"192.0.2.10"}
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		return discovery.ServiceSpec{
			InstanceName:  "n",
			Port:          8317,
			AdvertisedIPs: append([]string(nil), ips...),
		}, nil
	}
	cfg := &config.Config{}
	cfg.Discovery.Enabled = true

	if !mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("first apply failed")
	}
	ips = []string{"192.0.2.11"}
	if !mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("second apply failed")
	}
	if len(created) != 2 {
		t.Fatalf("created %d advertisers, want 2", len(created))
	}
	starts, stops, _ := created[0].snapshot()
	if starts != 1 || stops != 1 {
		t.Fatalf("first advertiser starts=%d stops=%d, want 1/1", starts, stops)
	}
	starts, stops, spec := created[1].snapshot()
	if starts != 1 || stops != 0 {
		t.Fatalf("second advertiser starts=%d stops=%d, want 1/0", starts, stops)
	}
	if len(spec.AdvertisedIPs) != 1 || spec.AdvertisedIPs[0] != "192.0.2.11" {
		t.Fatalf("second spec IPs = %v", spec.AdvertisedIPs)
	}
}

func TestDiscoveryManagerStopsAdvertiserOnBuildFailure(t *testing.T) {
	mgr := newTestDiscoveryManager()
	adv := &fakeAdvertiser{}
	buildErr := false
	mgr.newAdvertiser = func() discovery.Advertiser { return adv }
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		if buildErr {
			return discovery.ServiceSpec{}, context.Canceled
		}
		return discovery.ServiceSpec{InstanceName: "n", Port: 8317}, nil
	}
	cfg := &config.Config{}
	cfg.Discovery.Enabled = true
	if !mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("initial apply failed")
	}
	buildErr = true
	if mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("expected build failure")
	}
	starts, stops, _ := adv.snapshot()
	if starts != 1 || stops != 1 {
		t.Fatalf("starts=%d stops=%d, want 1/1", starts, stops)
	}
}

func TestDiscoveryManagerRejectsApplyAfterShutdown(t *testing.T) {
	mgr := newTestDiscoveryManager()
	called := false
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		called = true
		return discovery.ServiceSpec{}, nil
	}
	if err := mgr.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	cfg := &config.Config{}
	cfg.Discovery.Enabled = true
	if mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("apply after shutdown unexpectedly succeeded")
	}
	if called {
		t.Fatal("buildSpec called after shutdown")
	}
}

func TestDiscoveryRefreshStopIsIdempotent(t *testing.T) {
	refresh := &discoveryRefresh{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	close(refresh.done)
	refresh.stopAndWait()
	refresh.stopAndWait()
}

func TestDiscoveryManagerConcurrentShutdownIsSafe(t *testing.T) {
	mgr := newDiscoveryAdvertiserManager()
	mgr.refreshInterval = time.Millisecond
	adv := &fakeAdvertiser{}
	mgr.newAdvertiser = func() discovery.Advertiser { return adv }
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		return discovery.ServiceSpec{InstanceName: "n", Port: 8317}, nil
	}
	cfg := &config.Config{}
	cfg.Discovery.Enabled = true
	if !mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("initial apply failed")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mgr.Shutdown(); err != nil {
				t.Errorf("shutdown: %v", err)
			}
		}()
	}
	wg.Wait()
	if mgr.ApplyContext(context.Background(), cfg, 8317, false) {
		t.Fatal("apply after concurrent shutdown unexpectedly succeeded")
	}
}

func TestDiscoveryManagerDisableStopsAdvertiser(t *testing.T) {
	mgr := newTestDiscoveryManager()
	adv := &fakeAdvertiser{}
	mgr.newAdvertiser = func() discovery.Advertiser { return adv }
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		return discovery.ServiceSpec{InstanceName: "n", Port: 8317}, nil
	}
	enabled := &config.Config{}
	enabled.Discovery.Enabled = true
	if !mgr.ApplyContext(context.Background(), enabled, 8317, false) {
		t.Fatal("enable failed")
	}
	disabled := &config.Config{}
	if !mgr.ApplyContext(context.Background(), disabled, 8317, false) {
		t.Fatal("disable failed")
	}
	starts, stops, _ := adv.snapshot()
	if starts != 1 || stops != 1 {
		t.Fatalf("starts=%d stops=%d, want 1/1", starts, stops)
	}
}

func TestDiscoveryManagerCanceledApplyStopsInFlightStart(t *testing.T) {
	mgr := newTestDiscoveryManager()
	entered := make(chan struct{})
	block := make(chan struct{})
	adv := &fakeAdvertiser{entered: entered, blockStart: block}
	mgr.newAdvertiser = func() discovery.Advertiser { return adv }
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		return discovery.ServiceSpec{InstanceName: "n", Port: 8317}, nil
	}
	cfg := &config.Config{}
	cfg.Discovery.Enabled = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan bool, 1)
	go func() {
		done <- mgr.ApplyContext(ctx, cfg, 8317, false)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("start did not begin")
	}
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("canceled apply should not commit")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled apply did not return")
	}
	starts, stops, _ := adv.snapshot()
	if starts != 0 || stops == 0 {
		t.Fatalf("starts=%d stops=%d, want starts=0 and at least one stop", starts, stops)
	}
}

func TestDiscoveryManagerShutdownAbandonsInFlightStart(t *testing.T) {
	mgr := newTestDiscoveryManager()
	entered := make(chan struct{})
	block := make(chan struct{})
	adv := &fakeAdvertiser{entered: entered, blockStart: block}
	mgr.newAdvertiser = func() discovery.Advertiser { return adv }
	mgr.buildSpec = func(*config.Config, int, bool) (discovery.ServiceSpec, error) {
		return discovery.ServiceSpec{InstanceName: "n", Port: 8317}, nil
	}
	cfg := &config.Config{}
	cfg.Discovery.Enabled = true

	done := make(chan bool, 1)
	go func() {
		done <- mgr.ApplyContext(context.Background(), cfg, 8317, false)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("start did not begin")
	}
	if err := mgr.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	close(block)
	select {
	case ok := <-done:
		if ok {
			t.Fatal("in-flight apply should not commit after shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("apply did not return")
	}
	_, stops, _ := adv.snapshot()
	if stops == 0 {
		t.Fatal("expected in-flight advertiser to be stopped")
	}
}
