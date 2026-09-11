package devin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestBuildGetUserStatusRequest(t *testing.T) {
	reqBytes := BuildGetUserStatusRequest("test-token-123", "test-device-seed")
	if len(reqBytes) == 0 {
		t.Fatal("expected non-empty request bytes")
	}

	num, wireType, n := protowire.ConsumeTag(reqBytes)
	if num != 1 || wireType != protowire.BytesType {
		t.Fatalf("expected Field 1 BytesType, got num=%d wireType=%d", num, wireType)
	}

	f1Bytes, _ := protowire.ConsumeBytes(reqBytes[n:])
	if len(f1Bytes) == 0 {
		t.Fatal("expected non-empty ClientMetadata bytes")
	}
}

func buildMockUserStatusResponse() []byte {
	// PlanInfo
	var planInfo []byte
	planInfo = protowire.AppendTag(planInfo, 2, protowire.BytesType)
	planInfo = protowire.AppendString(planInfo, "Pro")

	var orgInfo []byte
	orgInfo = protowire.AppendTag(orgInfo, 4, protowire.BytesType)
	orgInfo = protowire.AppendString(orgInfo, "org-test-123")
	orgInfo = protowire.AppendTag(orgInfo, 8, protowire.BytesType)
	orgInfo = protowire.AppendString(orgInfo, "TestOrg")

	planInfo = protowire.AppendTag(planInfo, 33, protowire.BytesType)
	planInfo = protowire.AppendBytes(planInfo, orgInfo)

	// PlanStatus
	var planStatus []byte
	planStatus = protowire.AppendTag(planStatus, 1, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planInfo)

	// plan_start
	var planStart []byte
	planStart = protowire.AppendTag(planStart, 1, protowire.VarintType)
	planStart = protowire.AppendVarint(planStart, 1789087364)
	planStatus = protowire.AppendTag(planStatus, 2, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planStart)

	// plan_end
	var planEnd []byte
	planEnd = protowire.AppendTag(planEnd, 1, protowire.VarintType)
	planEnd = protowire.AppendVarint(planEnd, 1791679364)
	planStatus = protowire.AppendTag(planStatus, 3, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planEnd)

	// daily quota: 100%
	planStatus = protowire.AppendTag(planStatus, 14, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 100)

	// weekly quota: 50%
	planStatus = protowire.AppendTag(planStatus, 15, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 50)

	// daily reset: 1789200000
	planStatus = protowire.AppendTag(planStatus, 17, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789200000)

	// weekly reset: 1789286400
	planStatus = protowire.AppendTag(planStatus, 18, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, 1789286400)

	// UserStatus
	var userStatus []byte
	userStatus = protowire.AppendTag(userStatus, 3, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "testuser")

	userStatus = protowire.AppendTag(userStatus, 5, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "team-123")

	userStatus = protowire.AppendTag(userStatus, 7, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "testuser@example.com")

	userStatus = protowire.AppendTag(userStatus, 13, protowire.BytesType)
	userStatus = protowire.AppendBytes(userStatus, planStatus)

	userStatus = protowire.AppendTag(userStatus, 36, protowire.BytesType)
	userStatus = protowire.AppendString(userStatus, "user-id-456")

	// Top-level response: Field 1 is UserStatus
	var resp []byte
	resp = protowire.AppendTag(resp, 1, protowire.BytesType)
	resp = protowire.AppendBytes(resp, userStatus)

	return resp
}

func TestParseGetUserStatusResponse(t *testing.T) {
	mockBytes := buildMockUserStatusResponse()

	status, err := ParseGetUserStatusResponse(mockBytes)
	if err != nil {
		t.Fatalf("ParseGetUserStatusResponse failed: %v", err)
	}

	if status.UserName != "testuser" {
		t.Errorf("expected UserName=testuser, got %q", status.UserName)
	}
	if status.Email != "testuser@example.com" {
		t.Errorf("expected Email=testuser@example.com, got %q", status.Email)
	}
	if status.UserID != "user-id-456" {
		t.Errorf("expected UserID=user-id-456, got %q", status.UserID)
	}
	if status.TeamID != "team-123" {
		t.Errorf("expected TeamID=team-123, got %q", status.TeamID)
	}
	if status.Plan != "Pro" {
		t.Errorf("expected Plan=Pro, got %q", status.Plan)
	}
	if status.OrgID != "org-test-123" {
		t.Errorf("expected OrgID=org-test-123, got %q", status.OrgID)
	}
	if status.OrgName != "TestOrg" {
		t.Errorf("expected OrgName=TestOrg, got %q", status.OrgName)
	}
	if status.DailyQuotaRemainingPercent != 100 {
		t.Errorf("expected DailyQuotaRemainingPercent=100, got %d", status.DailyQuotaRemainingPercent)
	}
	if status.WeeklyQuotaRemainingPercent != 50 {
		t.Errorf("expected WeeklyQuotaRemainingPercent=50, got %d", status.WeeklyQuotaRemainingPercent)
	}
	expectedDailyReset := time.Unix(1789200000, 0).UTC()
	if !status.DailyQuotaResetAt.Equal(expectedDailyReset) {
		t.Errorf("expected DailyQuotaResetAt=%v, got %v", expectedDailyReset, status.DailyQuotaResetAt)
	}
	expectedWeeklyReset := time.Unix(1789286400, 0).UTC()
	if !status.WeeklyQuotaResetAt.Equal(expectedWeeklyReset) {
		t.Errorf("expected WeeklyQuotaResetAt=%v, got %v", expectedWeeklyReset, status.WeeklyQuotaResetAt)
	}
}

func TestFetchUserStatusLiveMock(t *testing.T) {
	mockResp := buildMockUserStatusResponse()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DevinGetUserStatusPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Errorf("missing Connect-Protocol-Version header")
		}
		if r.Header.Get("Content-Type") != "application/proto" {
			t.Errorf("expected Content-Type application/proto, got %s", r.Header.Get("Content-Type"))
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Basic mytoken-mytoken" {
			t.Errorf("unexpected Authorization header: %s", authHeader)
		}

		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mockResp)
	}))
	defer server.Close()

	svc := NewDevinAuthService(server.Client())
	svc.SetServerBaseURL(server.URL)

	status, err := svc.FetchUserStatus(context.Background(), "mytoken", "device-seed")
	if err != nil {
		t.Fatalf("FetchUserStatus failed: %v", err)
	}

	if status.Plan != "Pro" {
		t.Errorf("expected plan Pro, got %s", status.Plan)
	}
	if status.DailyQuotaRemainingPercent != 100 {
		t.Errorf("expected daily quota 100, got %d", status.DailyQuotaRemainingPercent)
	}
}

func TestLiveDevinUserStatus(t *testing.T) {
	authPath := "../../../auths/devin-cli.json"
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Skip("auths/devin-cli.json not found, skipping live probe")
	}

	var authJSON struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(data, &authJSON); err != nil || authJSON.APIKey == "" {
		t.Skip("invalid auths/devin-cli.json, skipping")
	}

	// Route through local mitmproxy / direct
	proxyURL, _ := url.Parse("http://127.0.0.1:28082")
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}

	svc := NewDevinAuthService(client)
	status, err := svc.FetchUserStatus(context.Background(), authJSON.APIKey, "")
	if err != nil {
		t.Skipf("Live FetchUserStatus skipped (network or proxy unreachable): %v", err)
	}

	t.Logf("Live Devin User Status:")
	t.Logf("  Email: %s", status.Email)
	t.Logf("  UserName: %s", status.UserName)
	t.Logf("  UserID: %s", status.UserID)
	t.Logf("  TeamID: %s", status.TeamID)
	t.Logf("  OrgID: %s", status.OrgID)
	t.Logf("  OrgName: %s", status.OrgName)
	t.Logf("  Plan: %s", status.Plan)
	t.Logf("  Daily Quota: %d%% (Resets at %v)", status.DailyQuotaRemainingPercent, status.DailyQuotaResetAt)
	t.Logf("  Weekly Quota: %d%% (Resets at %v)", status.WeeklyQuotaRemainingPercent, status.WeeklyQuotaResetAt)
	t.Logf("  Plan Period: %v -> %v", status.PlanStart, status.PlanEnd)

	if status.Plan != "Pro" {
		t.Errorf("expected plan Pro, got %s", status.Plan)
	}
	if status.Email == "" {
		t.Error("expected non-empty email")
	}
}
