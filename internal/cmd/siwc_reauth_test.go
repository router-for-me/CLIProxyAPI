package cmd

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type siwcBindRoundTripper func(*http.Request) (*http.Response, error)

func (f siwcBindRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDoSIWCBindReusesSavedRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, returned string
		wantSuccess    bool
	}{
		{"callback omits unchanged client", "", true},
		{"callback confirms saved client", "oaiapp_saved", true},
		{"callback changes client", "oaiapp_other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "codex.json")
			const initial = `{"type":"codex","access_token":"outer","refresh_token":"outer-refresh","siwc":{"access_token":"old","refresh_token":"old-refresh","client_id":"oaiapp_saved","device_id":"e61bbe28-07ef-466d-8e5d-a344f94ab305"}}`
			if errWrite := os.WriteFile(path, []byte(initial), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			listener, errListen := net.Listen("tcp", "127.0.0.1:0")
			if errListen != nil {
				t.Fatal(errListen)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			if errClose := listener.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			previousTransport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previousTransport })
			exchanges := 0
			http.DefaultTransport = siwcBindRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != codex.SIWCTokenURL {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("127.0.0.1"))}, nil
				}
				exchanges++
				if errParse := req.ParseForm(); errParse != nil {
					return nil, errParse
				}
				if req.Form.Get("client_id") != "oaiapp_saved" {
					t.Errorf("token exchange did not use the saved registration")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"new","refresh_token":"new-refresh","id_token":"id","expires_in":3600,"scope":"openid chatgpt.tokens.use.direct"}`))}, nil
			})
			reader, writer, errPipe := os.Pipe()
			if errPipe != nil {
				t.Fatal(errPipe)
			}
			previousStdout := os.Stdout
			os.Stdout = writer
			t.Cleanup(func() {
				os.Stdout = previousStdout
				if errClose := reader.Close(); errClose != nil {
					t.Error(errClose)
				}
			})
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if errClose := writer.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				DoSIWCBind(&config.Config{AuthDir: dir}, path, &LoginOptions{NoBrowser: true, CallbackPort: port})
			}()
			scanner := bufio.NewScanner(reader)
			foundURL := false
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), codex.SIWCAuthorizeURL+"?") {
					continue
				}
				foundURL = true
				authURL, errParse := url.Parse(scanner.Text())
				if errParse != nil {
					t.Fatal(errParse)
				}
				query := authURL.Query()
				if query.Get("client_id") != "oaiapp_saved" || query.Has("agent_name_hint") || query.Get("ext_agent_host_id") != "urn:uuid:e61bbe28-07ef-466d-8e5d-a344f94ab305" || query.Get("code_challenge") == "" {
					t.Error("authorization URL did not preserve the issued registration, host identity, and PKCE")
				}
				callback := "http://127.0.0.1:" + strconv.Itoa(port) + codex.SIWCCallbackPath
				values := url.Values{"code": {"code"}, "state": {query.Get("state")}, "client_id": {tc.returned}}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, callback+"?"+values.Encode(), nil)
				if errRequest != nil {
					cancel()
					t.Fatal(errRequest)
				}
				callbackClient := &http.Client{Transport: previousTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				response, errCallback := callbackClient.Do(req)
				if errCallback == nil {
					if errClose := response.Body.Close(); errClose != nil {
						t.Error(errClose)
					}
				}
				cancel()
				if errCallback != nil {
					t.Fatal(errCallback)
				}
			}
			if errScan := scanner.Err(); errScan != nil {
				t.Fatal(errScan)
			}
			<-done
			if !foundURL {
				t.Fatal("authorization URL was not printed")
			}
			raw, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if tc.wantSuccess {
				if exchanges != 1 || ojson.GetBytes(raw, "siwc.refresh_token").String() != "new-refresh" || ojson.GetBytes(raw, "siwc.client_id").String() != "oaiapp_saved" {
					t.Fatal("reauthorization did not persist the replacement under the saved client")
				}
			} else if exchanges != 0 || string(raw) != initial {
				t.Fatal("mismatched callback exchanged tokens or changed the credential")
			}
		})
	}
}
