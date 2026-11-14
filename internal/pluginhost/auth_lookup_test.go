package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func newAuthLookupTestHost(tb testing.TB, size int) (*Host, string) {
	tb.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	var index string
	for i := 0; i < size; i++ {
		auth, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID: fmt.Sprintf("lookup-%d.json", i), Provider: "codex", Status: coreauth.StatusActive,
			Attributes:  map[string]string{"source": "fixture"},
			Metadata:    map[string]any{"type": "codex"},
			ModelStates: map[string]*coreauth.ModelState{"test-model": {Status: coreauth.StatusActive}},
		})
		if errRegister != nil {
			tb.Fatal(errRegister)
		}
		index = auth.Index
	}
	host := New()
	host.SetAuthManager(manager)
	return host, index
}

func TestHostAuthByIndexAllocationsDoNotScaleWithPool(t *testing.T) {
	measure := func(size int) float64 {
		host, index := newAuthLookupTestHost(t, size)
		return testing.AllocsPerRun(10, func() {
			auth, errGet := host.authByIndex(index)
			if errGet != nil || auth.Index != index {
				t.Fatalf("lookup failed: %v", errGet)
			}
		})
	}
	small, large := measure(1), measure(2000)
	t.Logf("single-account lookup allocations: 1 account=%.0f, 2000 accounts=%.0f", small, large)
	if large > small+10 {
		t.Fatalf("lookup allocations scale with unrelated accounts: small=%.0f large=%.0f", small, large)
	}
}

func TestHostAuthGetCallbacksRespectCancellation(t *testing.T) {
	host := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, call := range map[string]func(context.Context, []byte) ([]byte, error){
		"physical": host.callHostAuthGet,
		"runtime":  host.callHostAuthGetRuntime,
	} {
		t.Run(name, func(t *testing.T) {
			_, errCall := call(ctx, []byte(`{"auth_index":"fixture"}`))
			if !errors.Is(errCall, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled before looking up credentials", errCall)
			}
		})
	}
}

func BenchmarkHostAuthByIndex(b *testing.B) {
	for _, size := range []int{1, 100, 2000} {
		b.Run(fmt.Sprintf("accounts_%d", size), func(b *testing.B) {
			host, index := newAuthLookupTestHost(b, size)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, errGet := host.authByIndex(index); errGet != nil {
					b.Fatal(errGet)
				}
			}
		})
	}
}
