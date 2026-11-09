package qoder

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestStartDeviceFlowUsesRegionalOfficialParameters(t *testing.T) {
	for _, test := range []struct {
		region string
		host   string
	}{
		{RegionGlobal, "qoder.com"},
		{RegionCN, "qoder.cn"},
	} {
		t.Run(test.region, func(t *testing.T) {
			client := NewClient(nil, test.region)
			flow, err := client.StartDeviceFlow()
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(flow.VerificationURI)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Hostname() != test.host || parsed.Path != "/device/selectAccounts" {
				t.Fatalf("authorization URL = %s", flow.VerificationURI)
			}
			query := parsed.Query()
			if query.Get("client_id") != deviceClientID || query.Get("challenge_method") != "S256" {
				t.Fatalf("query = %v", query)
			}
			digest := sha256.Sum256([]byte(flow.CodeVerifier))
			if query.Get("challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
				t.Fatal("PKCE challenge does not match verifier")
			}
			if query.Get("nonce") != flow.Nonce || query.Get("machine_id") != flow.MachineID {
				t.Fatal("flow state does not match authorization URL")
			}
			if len(flow.CodeVerifier) != 86 || len(flow.MachineID) != 96 {
				t.Fatalf("unexpected verifier/machine sizes: %d/%d", len(flow.CodeVerifier), len(flow.MachineID))
			}
			if query.Has("verifier") || query.Has("redirect_uri") {
				t.Fatalf("private or unsupported parameter leaked: %v", query)
			}
		})
	}
}

func TestDeviceLoginPollsAndBuildsMetadataOnlyCredential(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/deviceToken/poll":
			if r.URL.Query().Get("nonce") != "nonce-test" || r.URL.Query().Get("verifier") != "verifier-test" || r.URL.Query().Get("challenge_method") != "S256" {
				t.Errorf("poll query = %v", r.URL.Query())
			}
			if r.Header.Get("User-Agent") != "qoder/"+ClientVersion {
				t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
			}
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, `{"token":"dt-test","refresh_token":"drt-test","user_id":"user-test","expires_in":3600,"refresh_token_expires_at":"2030-01-01T00:00:00Z"}`)
		case "/api/v1/userinfo":
			if r.Header.Get("Authorization") != "Bearer dt-test" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			fmt.Fprint(w, `{"id":"user-test","name":"Test User","email":"user@example.com","organization":{"id":"org-test","name":"Org"},"organization_tags":["tag-a"],"data_policy_agreed":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(nil, RegionGlobal)
	client.endpoints = Endpoints{WebBase: server.URL, OpenAPIBase: server.URL, InferBase: server.URL}
	client.SetHTTPClient(server.Client())
	client.pollInterval = time.Millisecond
	client.loginTTL = time.Second
	storage, err := client.PollForToken(context.Background(), &DeviceFlow{CodeVerifier: "verifier-test", Nonce: "nonce-test", MachineID: "machine-test"})
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 2 || storage.AccessToken != "dt-test" || storage.RefreshToken != "drt-test" {
		t.Fatalf("polls/storage = %d/%+v", polls.Load(), storage)
	}
	if storage.UserID != "user-test" || storage.Email != "user@example.com" || storage.OrganizationID != "org-test" || len(storage.OrganizationTags) != 1 {
		t.Fatalf("identity = %+v", storage)
	}
	if storage.ExpireTime <= time.Now().Unix() || storage.RefreshTokenExpireTime != 1893456000 {
		t.Fatalf("expiry = %d/%d", storage.ExpireTime, storage.RefreshTokenExpireTime)
	}
	record, err := NewAuthRecord(storage)
	if err != nil {
		t.Fatal(err)
	}
	record.Storage = nil
	resumed, err := CredentialFromAuth(record)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.AccessToken != storage.AccessToken || resumed.MachineID != "machine-test" || resumed.Region != RegionGlobal {
		t.Fatalf("resumed credential = %+v", resumed)
	}
	if _, ok := record.ExpirationTime(); !ok {
		t.Fatal("shared scheduler cannot read Qoder expiry")
	}
}

func TestRefreshUsesOfficialDeviceTokenEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/deviceToken/refresh" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["refresh_token"] != "drt-old" || payload["machine_id"] != "machine-test" || payload["machine_type"] != "5" || payload["machine_code"] != "code-test" {
			t.Fatalf("refresh payload = %v", payload)
		}
		if _, exists := payload["access_token"]; exists {
			t.Fatal("access token must not be sent in refresh body")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"device_token":"dt-new","refresh_token":"drt-new","expires_at":"2030-02-01T00:00:00Z","refresh_token_expires_at":"2031-01-01T00:00:00Z"}`)
	}))
	defer server.Close()
	client := NewClient(nil, RegionCN)
	client.endpoints = Endpoints{OpenAPIBase: server.URL}
	client.SetHTTPClient(server.Client())
	updated, err := client.Refresh(context.Background(), &TokenStorage{
		Region: RegionCN, AccessToken: "dt-old", RefreshToken: "drt-old", MachineID: "machine-test", MachineType: "5", MachineCode: "code-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != "dt-new" || updated.SecurityOAuthToken != "dt-new" || updated.RefreshToken != "drt-new" {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.ExpireTime != 1896134400 || updated.RefreshTokenExpireTime != 1924992000 {
		t.Fatalf("expiry = %d/%d", updated.ExpireTime, updated.RefreshTokenExpireTime)
	}
}

func TestTokenStorageRoundTripSupportsChinesePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "中文", "qoder-cn.json")
	storage := &TokenStorage{
		Region: RegionCN, AccessToken: "dt-test", RefreshToken: "drt-test", UserID: "user-test", MachineID: "machine-test", ExpireTime: 1893456000,
		Metadata: map[string]any{"note": "中文备注"},
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	resumed, err := CredentialFromMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Region != RegionCN || resumed.AccessToken != "dt-test" || resumed.ExpireTime != 1893456000 || String(metadata, "note") != "中文备注" {
		t.Fatalf("round trip = %+v, metadata = %+v", resumed, metadata)
	}
}

func TestTokenExpiryHandlesMillisecondDuration(t *testing.T) {
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	got := tokenExpiry(map[string]any{"expires_in": int64(86400000)}, now, "expires_in")
	if got != now.Add(24*time.Hour).Unix() {
		t.Fatalf("expiry = %d, want %d", got, now.Add(24*time.Hour).Unix())
	}
	seconds := tokenExpiry(map[string]any{"expires_in": int64(2592000)}, now, "expires_in")
	if seconds != now.Add(30*24*time.Hour).Unix() {
		t.Fatalf("seconds expiry = %d, want %d", seconds, now.Add(30*24*time.Hour).Unix())
	}
}

func TestCredentialRegionNeverFallsBackAcrossProviders(t *testing.T) {
	auth := &coreauth.Auth{Provider: ProviderCN, Metadata: map[string]any{
		"access_token": "dt-test", "refresh_token": "drt-test", "user_id": "user-test", "machine_id": "machine-test",
	}}
	storage, err := CredentialFromAuth(auth)
	if err != nil {
		t.Fatal(err)
	}
	if storage.Region != RegionCN || ProviderForRegion(storage.Region) != ProviderCN {
		t.Fatalf("region/provider = %s/%s", storage.Region, ProviderForRegion(storage.Region))
	}
	global := &coreauth.Auth{Provider: ProviderGlobal, Metadata: map[string]any{
		"access_token": "dt-global", "user_id": "user-global", "machine_id": "machine-global", "region": RegionCN,
	}}
	globalStorage, err := CredentialFromAuth(global)
	if err != nil {
		t.Fatal(err)
	}
	if globalStorage.Region != RegionGlobal {
		t.Fatalf("provider metadata drifted to region %q", globalStorage.Region)
	}
}
