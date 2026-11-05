package kimi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func oauthJSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestIncreasePollIntervalFollowsRFC8628SlowDown(t *testing.T) {
	interval := defaultPollInterval
	want := []time.Duration{
		10 * time.Second,
		15 * time.Second,
		20 * time.Second,
	}
	for i, next := range want {
		got := increasePollInterval(interval)
		if got != next {
			t.Fatalf("slow_down %d: increasePollInterval(%s) = %s, want %s", i+1, interval, got, next)
		}
		interval = got
	}
}

func TestExchangeDeviceCodeSlowDownIncreasesIntervalByFiveSeconds(t *testing.T) {
	var calls int
	client := &DeviceFlowClient{httpClient: &http.Client{Transport: kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return oauthJSONResponse(req, `{"error":"slow_down"}`), nil
	})}}

	interval := defaultPollInterval
	for i, want := range []time.Duration{10 * time.Second, 15 * time.Second, 20 * time.Second} {
		token, errExchange, next, cont := client.exchangeDeviceCode(context.Background(), "device-1", interval)
		if token != nil || errExchange != nil {
			t.Fatalf("slow_down %d: token=%v err=%v", i+1, token, errExchange)
		}
		if !cont {
			t.Fatalf("slow_down %d: shouldContinue = false", i+1)
		}
		if next != want {
			t.Fatalf("slow_down %d: nextInterval = %s, want %s", i+1, next, want)
		}
		interval = next
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestExchangeDeviceCodeAuthorizationPendingKeepsInterval(t *testing.T) {
	client := &DeviceFlowClient{httpClient: &http.Client{Transport: kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return oauthJSONResponse(req, `{"error":"authorization_pending"}`), nil
	})}}

	token, errExchange, next, cont := client.exchangeDeviceCode(context.Background(), "device-1", defaultPollInterval)
	if token != nil || errExchange != nil || !cont {
		t.Fatalf("token=%v err=%v shouldContinue=%v", token, errExchange, cont)
	}
	if next != defaultPollInterval {
		t.Fatalf("nextInterval = %s, want %s", next, defaultPollInterval)
	}
}

func TestPollForTokenSlowDownIncreasesIntervalByFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var pollTimes []time.Time
		var calls int
		client := &DeviceFlowClient{httpClient: &http.Client{Transport: kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			pollTimes = append(pollTimes, time.Now())
			calls++
			body := `{"error":"slow_down"}`
			if calls == 3 {
				body = `{"access_token":"access-slow","refresh_token":"refresh-slow","token_type":"Bearer","expires_in":3600}`
			}
			return oauthJSONResponse(req, body), nil
		})}}

		token, errPoll := client.PollForToken(context.Background(), &DeviceCodeResponse{
			DeviceCode: "device-1",
			ExpiresIn:  120,
			Interval:   5,
		})
		if errPoll != nil {
			t.Fatalf("PollForToken() error = %v", errPoll)
		}
		if token == nil || token.AccessToken != "access-slow" {
			t.Fatalf("token = %#v, want access-slow", token)
		}
		if len(pollTimes) != 3 {
			t.Fatalf("poll count = %d, want 3", len(pollTimes))
		}
		// RFC 8628 §3.5: each slow_down adds +5s (5→10→15), not exponential (5→10→20).
		if gap := pollTimes[1].Sub(pollTimes[0]); gap != 10*time.Second {
			t.Fatalf("gap after first slow_down = %s, want 10s", gap)
		}
		if gap := pollTimes[2].Sub(pollTimes[1]); gap != 15*time.Second {
			t.Fatalf("gap after second slow_down = %s, want 15s", gap)
		}
	})
}

func TestPollForTokenAuthorizationPendingKeepsInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var pollTimes []time.Time
		var calls int
		client := &DeviceFlowClient{httpClient: &http.Client{Transport: kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			pollTimes = append(pollTimes, time.Now())
			calls++
			body := `{"error":"authorization_pending"}`
			if calls == 3 {
				body = `{"access_token":"access-ok","refresh_token":"refresh-ok","token_type":"Bearer","expires_in":3600}`
			}
			return oauthJSONResponse(req, body), nil
		})}}

		token, errPoll := client.PollForToken(context.Background(), &DeviceCodeResponse{
			DeviceCode: "device-1",
			ExpiresIn:  120,
			Interval:   5,
		})
		if errPoll != nil {
			t.Fatalf("PollForToken() error = %v", errPoll)
		}
		if token == nil || token.AccessToken != "access-ok" {
			t.Fatalf("token = %#v, want access-ok", token)
		}
		if len(pollTimes) != 3 {
			t.Fatalf("poll count = %d, want 3", len(pollTimes))
		}
		if gap := pollTimes[1].Sub(pollTimes[0]); gap != defaultPollInterval {
			t.Fatalf("pending gap = %s, want %s", gap, defaultPollInterval)
		}
		if gap := pollTimes[2].Sub(pollTimes[1]); gap != defaultPollInterval {
			t.Fatalf("pending gap = %s, want %s", gap, defaultPollInterval)
		}
	})
}

func TestPollWaitDurationCapsAtDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		tests := []struct {
			name     string
			interval time.Duration
			deadline time.Time
			want     time.Duration
		}{
			{name: "interval shorter than remaining", interval: 10 * time.Second, deadline: now.Add(30 * time.Second), want: 10 * time.Second},
			{name: "interval longer than remaining", interval: 10 * time.Second, deadline: now.Add(3 * time.Second), want: 3 * time.Second},
			{name: "already expired", interval: 10 * time.Second, deadline: now.Add(-time.Second), want: 0},
			{name: "deadline now", interval: 10 * time.Second, deadline: now, want: 0},
		}
		for _, tt := range tests {
			got := pollWaitDuration(tt.interval, tt.deadline)
			if got != tt.want {
				t.Fatalf("%s: pollWaitDuration() = %s, want %s", tt.name, got, tt.want)
			}
		}
	})
}

func TestPollForTokenSlowDownDoesNotSleepPastDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var pollTimes []time.Time
		client := &DeviceFlowClient{httpClient: &http.Client{Transport: kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			pollTimes = append(pollTimes, time.Now())
			return oauthJSONResponse(req, `{"error":"slow_down"}`), nil
		})}}

		start := time.Now()
		_, errPoll := client.PollForToken(context.Background(), &DeviceCodeResponse{
			DeviceCode: "device-1",
			ExpiresIn:  8, // first wait 5s; slow_down would otherwise reset for 10s
			Interval:   5,
		})
		if errPoll == nil || !strings.Contains(errPoll.Error(), "device code expired") {
			t.Fatalf("error = %v, want device code expired", errPoll)
		}
		if elapsed := time.Since(start); elapsed != 8*time.Second {
			t.Fatalf("elapsed = %s, want 8s (deadline), not the uncapped 15s wait", elapsed)
		}
		if len(pollTimes) != 1 {
			t.Fatalf("poll count = %d, want 1 (do not poll at the deadline)", len(pollTimes))
		}
	})
}

func TestPollForTokenFirstWaitCappedToDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		client := &DeviceFlowClient{httpClient: &http.Client{Transport: kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return oauthJSONResponse(req, `{"error":"authorization_pending"}`), nil
		})}}

		start := time.Now()
		_, errPoll := client.PollForToken(context.Background(), &DeviceCodeResponse{
			DeviceCode: "device-1",
			ExpiresIn:  3,
			Interval:   5,
		})
		if errPoll == nil || !strings.Contains(errPoll.Error(), "device code expired") {
			t.Fatalf("error = %v, want device code expired", errPoll)
		}
		if elapsed := time.Since(start); elapsed != 3*time.Second {
			t.Fatalf("elapsed = %s, want 3s (deadline), not the 5s first interval", elapsed)
		}
		if calls != 0 {
			t.Fatalf("poll count = %d, want 0 (do not poll at the deadline)", calls)
		}
	})
}

func TestPollForTokenNilDeviceCode(t *testing.T) {
	client := &DeviceFlowClient{httpClient: &http.Client{}}
	_, errPoll := client.PollForToken(context.Background(), nil)
	if errPoll == nil {
		t.Fatal("expected error for nil device code")
	}
}
