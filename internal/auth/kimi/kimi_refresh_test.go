package kimi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

type kimiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kimiRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func resetKimiRefreshGroupForTest() {
	kimiRefreshGroup = singleflight.Group{}
}

func TestRefreshToken_DeduplicatesConcurrentRefreshAcrossInstances(t *testing.T) {
	resetKimiRefreshGroupForTest()
	t.Cleanup(resetKimiRefreshGroupForTest)

	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	transport := kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-release
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"access_token":"new-access",
				"refresh_token":"new-refresh",
				"token_type":"Bearer",
				"expires_in":3600
			}`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})
	clientA := &DeviceFlowClient{httpClient: &http.Client{Transport: transport}}
	clientB := &DeviceFlowClient{httpClient: &http.Client{Transport: transport}}

	results := make(chan *KimiTokenData, 2)
	errs := make(chan error, 2)
	runRefresh := func(client *DeviceFlowClient, launched chan<- struct{}) {
		if launched != nil {
			close(launched)
		}
		tokenData, errRefresh := client.RefreshToken(context.Background(), "shared-refresh-token")
		results <- tokenData
		errs <- errRefresh
	}

	go runRefresh(clientA, nil)
	<-started

	secondLaunched := make(chan struct{})
	go runRefresh(clientB, secondLaunched)
	<-secondLaunched
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if errRefresh := <-errs; errRefresh != nil {
			t.Fatalf("expected refresh to succeed, got %v", errRefresh)
		}
		tokenData := <-results
		if tokenData == nil || tokenData.AccessToken != "new-access" || tokenData.RefreshToken != "new-refresh" {
			t.Fatalf("unexpected token data: %#v", tokenData)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected both refresh callers to share a single upstream call, got %d", got)
	}
}
