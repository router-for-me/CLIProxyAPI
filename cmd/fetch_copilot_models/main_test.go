package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

type modelTransport func(*http.Request) (*http.Response, error)

func (f modelTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestExportCopilotModels(t *testing.T) {
	const response = `{"data":[{"id":"hidden","policy":{"state":"disabled"},"unknown_limit":9007199254740993}],"upstream_only":true}`
	for _, tc := range []struct {
		name                                 string
		expired, retry, badDirectory, pretty bool
	}{
		{name: "valid"}, {name: "expired", expired: true, pretty: true}, {name: "rejected token", retry: true}, {name: "malformed directory", badDirectory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(configPath, []byte("config-version: 8\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			authDir := filepath.Join(dir, "auth")
			if err := os.Mkdir(authDir, 0o700); err != nil {
				t.Fatal(err)
			}
			expiry := time.Now().Add(time.Hour)
			if tc.expired {
				expiry = time.Now().Add(-time.Hour)
			}
			auth := fmt.Sprintf(`{"type":"copilot","base_url":"https://api.individual.githubcopilot.com","access_token":"stored","refresh_token":"grant","expired":%q}`, expiry.UTC().Format(time.RFC3339))
			if err := os.WriteFile(filepath.Join(authDir, "first.json"), []byte(`{"type":"copilot","disabled":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			authPath := filepath.Join(authDir, "selected.json")
			if err := os.WriteFile(authPath, []byte(auth), 0o600); err != nil {
				t.Fatal(err)
			}
			exchanges, fetches := 0, 0
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", modelTransport(func(req *http.Request) (*http.Response, error) {
				payload := response
				status := http.StatusOK
				switch req.URL.Path {
				case "/copilot_internal/v2/token":
					exchanges++
					if req.URL.Host != "api.github.com" || req.Header.Get("Authorization") != "Bearer grant" {
						t.Fatal("incorrect exchange request")
					}
					payload = fmt.Sprintf(`{"token":"fresh","expires_at":%d,"endpoints":{"api":"https://api.business.githubcopilot.com"}}`, time.Now().Add(time.Hour).Unix())
				case "/models":
					fetches++
					token, host := "stored", "api.individual.githubcopilot.com"
					if exchanges > 0 {
						token, host = "fresh", "api.business.githubcopilot.com"
					}
					if req.URL.Host != host || req.Header.Get("Authorization") != "Bearer "+token {
						t.Fatal("incorrect account or origin")
					}
					if tc.retry && exchanges == 0 {
						status = http.StatusUnauthorized
					}
					if tc.badDirectory {
						payload = `{"data":null}`
					}
				default:
					t.Fatalf("unexpected path %s", req.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
			}))
			output := filepath.Join(dir, "copilot_models.json")
			if err := os.WriteFile(output, []byte("previous"), 0o600); err != nil {
				t.Fatal(err)
			}
			selected := "selected.json"
			if tc.expired {
				selected = ""
			}
			err := run(ctx, configPath, authDir, selected, output, tc.pretty)
			if (err != nil) != tc.badDirectory {
				t.Fatalf("export: %v", err)
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if tc.badDirectory {
				if string(raw) != "previous" {
					t.Fatal("failed export replaced the previous file")
				}
			} else if !ojson.GetBytesNoCopy(raw, "upstream_only").Bool() || ojson.GetBytesNoCopy(raw, "data.0.unknown_limit").Raw != "9007199254740993" || (!tc.pretty && string(raw) != response) {
				t.Fatalf("incomplete export: %s", raw)
			}
			wantFetches := 1
			if tc.retry {
				wantFetches++
			}
			if fetches != wantFetches || (exchanges == 1) != (tc.expired || tc.retry) {
				t.Fatalf("fetches=%d exchanges=%d", fetches, exchanges)
			}
			if raw, err := os.ReadFile(authPath); err != nil || string(raw) != auth {
				t.Fatal("manual export modified the credential")
			}
		})
	}
}
