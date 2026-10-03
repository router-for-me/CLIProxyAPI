package devin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAuthRecord(t *testing.T) {
	for _, profileAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "profile", false: "quota fallback"}[profileAvailable], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v3/self":
					if r.Header.Get("Authorization") != "Bearer devin-session-token$eyJ.test.token" {
						t.Error("profile request missing normalized session token")
					}
					if !profileAvailable {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = w.Write([]byte(`{"user_name":"profile-user","user_id":"profile-id","org_id":"profile-org"}`))
				case DevinGetUserStatusPath:
					_, _ = w.Write(buildMockUserStatusResponse())
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			svc := NewDevinAuthService(server.Client())
			svc.apiBaseURL = server.URL
			svc.serverBaseURL = server.URL
			record, errRecord := svc.CreateAuthRecord(context.Background(), "eyJ.test.token")
			if errRecord != nil {
				t.Fatal(errRecord)
			}
			wantName := "profile-user"
			if !profileAvailable {
				wantName = "testuser"
			}
			if record.Provider != "devin" || record.FileName != "devin-"+wantName+".json" || record.ID != record.FileName {
				t.Fatalf("unexpected record identity: %s %s", record.Provider, record.FileName)
			}
			for key, want := range map[string]string{
				"api_key":       "devin-session-token$eyJ.test.token",
				"session_token": "devin-session-token$eyJ.test.token",
				"auth_kind":     "oauth", "user_name": wantName, "email": "testuser@example.com", "plan": "Pro",
			} {
				if record.Attributes[key] != want || record.Metadata[key] != want {
					t.Errorf("field %s differs between credential attributes and metadata", key)
				}
			}
			if record.Quota.Signals["daily_quota_remaining_percent"] != "100%" || record.Quota.Signals["weekly_quota_remaining_percent"] != "50%" || record.Quota.Signals["daily_quota_reset_at"] == "" {
				t.Fatalf("unexpected quota signals: %v", record.Quota.Signals)
			}
		})
	}
}

func TestCreateAuthRecordSafeFallbackFilename(t *testing.T) {
	for _, userName := range []string{"", "../../outside/file", `..\outside\file`, strings.Repeat("x", 300)} {
		t.Run(userName, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v3/self" {
					_ = json.NewEncoder(w).Encode(map[string]string{"user_name": userName})
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			defer server.Close()
			svc := NewDevinAuthService(server.Client())
			svc.apiBaseURL = server.URL
			svc.serverBaseURL = server.URL
			record, errRecord := svc.CreateAuthRecord(context.Background(), "eyJ.first.token")
			if errRecord != nil {
				t.Fatal(errRecord)
			}
			if record.FileName != filepath.Base(record.FileName) || strings.ContainsAny(record.FileName, `/\`) || len(record.FileName) > 200 {
				t.Fatalf("unsafe credential filename: %q", record.FileName)
			}
			if userName == "" {
				second, errSecond := svc.CreateAuthRecord(context.Background(), "eyJ.second.token")
				if errSecond != nil {
					t.Fatal(errSecond)
				}
				if second.FileName == record.FileName {
					t.Fatal("different unknown accounts overwrite the same file")
				}
			}
		})
	}
	if _, errRecord := NewDevinAuthService(nil).CreateAuthRecord(context.Background(), ""); errRecord == nil {
		t.Fatal("empty token accepted")
	}
}
