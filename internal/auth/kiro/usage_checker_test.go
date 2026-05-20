package kiro

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type usageCheckerRoundTripper struct {
	lastReq *http.Request
}

func (rt *usageCheckerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.lastReq = req
	body := `{"usageBreakdownList":[{"resourceType":"AGENTIC_REQUEST","usageLimitWithPrecision":10,"currentUsageWithPrecision":2}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func TestUsageCheckerCheckUsageOmitsEmptyProfileARN(t *testing.T) {
	rt := &usageCheckerRoundTripper{}
	checker := NewUsageCheckerWithClient(&http.Client{Transport: rt})

	_, err := checker.CheckUsage(context.Background(), &KiroTokenData{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ClientID:     "client-id",
	})
	if err != nil {
		t.Fatalf("CheckUsage returned error: %v", err)
	}
	if rt.lastReq == nil {
		t.Fatal("expected request to be captured")
	}

	query := rt.lastReq.URL.Query()
	if query.Get("profileArn") != "" {
		t.Fatalf("profileArn query = %q, want empty", query.Get("profileArn"))
	}
	if query.Get("isEmailRequired") != "true" {
		t.Fatalf("isEmailRequired query = %q, want true", query.Get("isEmailRequired"))
	}
}

func TestUsageCheckerCheckUsageIncludesProfileARN(t *testing.T) {
	rt := &usageCheckerRoundTripper{}
	checker := NewUsageCheckerWithClient(&http.Client{Transport: rt})

	_, err := checker.CheckUsage(context.Background(), &KiroTokenData{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ClientID:     "client-id",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC",
	})
	if err != nil {
		t.Fatalf("CheckUsage returned error: %v", err)
	}
	if rt.lastReq == nil {
		t.Fatal("expected request to be captured")
	}

	query := rt.lastReq.URL.Query()
	if query.Get("profileArn") == "" {
		t.Fatal("profileArn query is empty, want populated")
	}
	if query.Get("isEmailRequired") != "" {
		t.Fatalf("isEmailRequired query = %q, want empty", query.Get("isEmailRequired"))
	}
}
