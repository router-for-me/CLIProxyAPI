//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type nativeAuthRefreshWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *nativeAuthRefreshWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestNativeAuthRefreshNewLoginWaiterIgnoresObsoleteFailure(t *testing.T) {
	native := nativeAuthRefreshBuildNativeHost(t)
	upstream := &nativeAuthRefreshLoopback{refreshStarted: make(chan struct{}), refreshRelease: make(chan struct{})}
	provider := httptest.NewServer(upstream)
	defer provider.Close()
	credential := nativeAuthRefreshCredential("native-auth-refresh-replacement.json", "expired", provider.URL)
	credential["test_refresh_policy"] = "on-demand"
	credential["refresh_mode"] = "transient"
	auth := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, credential)
	manager := nativeAuthRefreshManager(t, native.cfg, native.host, auth)
	original, _ := manager.GetByID(auth.ID)
	oldResult := make(chan error, 1)
	go func() {
		_, err := manager.EnsureRequestAuth(context.Background(), original, "native-auth-refresh-chat")
		oldResult <- err
	}()
	<-upstream.refreshStarted
	login := nativeAuthRefreshCredential(auth.ID, "valid", provider.URL)
	login["test_refresh_policy"] = "on-demand"
	parsed := nativeAuthRefreshParse(t, native.host, native.cfg.AuthDir, login)
	if _, err := manager.Register(context.Background(), parsed); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(auth.ID)
	newResult := make(chan error, 1)
	waiter := &nativeAuthRefreshWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	go func() {
		_, err := manager.EnsureRequestAuth(waiter, current, "native-auth-refresh-chat")
		newResult <- err
	}()
	var err error
	select {
	case <-waiter.waiting:
		close(upstream.refreshRelease)
		err = <-newResult
	case err = <-newResult:
		close(upstream.refreshRelease)
	}
	oldErr := <-oldResult
	if oldErr == nil || !strings.Contains(oldErr.Error(), "synthetic refresh unavailable") {
		t.Fatalf("original native error=%v", oldErr)
	}
	counters := nativeAuthRefreshNativeCounters(t, native.client)
	t.Logf("native old-error=%v new-login-error=%v old-epoch=%d current-epoch=%d refresh=%d token-requests=%d", oldErr, err, original.RegistrationEpoch, current.RegistrationEpoch, counters.Refresh, len(upstream.snapshot()))
	if err != nil {
		t.Fatalf("fresh native login inherited obsolete in-flight refresh failure: %v", err)
	}
	server := nativeAuthRefreshHandlerServer(t, native.cfg, manager, native.host)
	resp, err := server.Client().Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"native-auth-refresh-chat","messages":[{"role":"user","content":"new login"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || !strings.Contains(string(body), "synthetic-native-ok") || counters.Refresh != 1 || len(upstream.snapshot()) != 2 {
		t.Fatalf("fresh native replacement not executable: status=%d error=%v body=%s", resp.StatusCode, err, body)
	}
}
