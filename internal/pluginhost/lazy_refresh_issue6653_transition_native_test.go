//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestIssue6653TransitionNativeTerminalScheduledBarrier(t *testing.T) {
	h, p, record := issue6653NativeHost(t)
	for _, code := range []string{"invalid_grant", "token_revoked"} {
		for _, status := range []int{0, 400} {
			for _, disabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/disabled=%v", code, status, disabled), func(t *testing.T) {
					entered, release := make(chan struct{}), make(chan struct{})
					var exchanges, dispatches atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/refresh" {
							dispatches.Add(1)
							t.Error("terminal credential dispatched")
							return
						}
						if exchanges.Add(1) != 1 {
							t.Error("rejected grant exchanged twice")
							return
						}
						close(entered)
						<-release
						d := ojson.NewDoc([]byte(`{}`))
						_ = d.Set("qa_error_code", code)
						_ = d.Set("qa_error_status", status)
						_ = d.Set("qa_error_message", "qa-old-refresh renewal denied")
						_, _ = w.Write(d.Bytes())
					}))
					defer upstream.Close()
					m, _ := issue6653NativeManager(t, h, p, record, nil)
					a := issue6653NativeRegister(t, h, m, "qa-terminal-transition", upstream.URL, true)
					done := make(chan error, 1)
					go func() { done <- issue6653NativeOperation(m, h, a, "buffered") }()
					<-entered
					c, _ := m.GetByID(a.ID)
					c.RefreshPolicy = &coreauth.RefreshPolicy{Mode: "scheduled"}
					c.Disabled = disabled
					if _, err := m.Update(context.Background(), c); err != nil {
						t.Fatal(err)
					}
					close(release)
					err := <-done
					if err == nil {
						t.Fatal("terminal native rotation admitted")
					}
					if strings.Contains(err.Error(), "qa-old-refresh") {
						t.Fatal("first demand error retains attempted grant")
					}
					latest, _ := m.GetByID(a.ID)
					if !latest.RequiresSignIn || latest.RejectedRefreshToken != "qa-old-refresh" || latest.Disabled != disabled {
						t.Fatal("attempted terminal classification or concurrent disabled state lost")
					}
					for _, d := range []*coreauth.Error{latest.RefreshError, latest.LastError} {
						if d == nil || strings.Contains(d.Message+d.Code, "qa-old-refresh") {
							t.Fatal("stored native terminal diagnostic absent or retains grant")
						}
					}
					for _, op := range []string{"buffered", "stream", "quota"} {
						if err := issue6653NativeOperation(m, h, a, op); err == nil {
							t.Fatalf("%s accepted terminal credential", op)
						} else if strings.Contains(err.Error(), "qa-old-refresh") {
							t.Fatal("subsequent error retains grant")
						}
					}
					if exchanges.Load() != 1 || dispatches.Load() != 0 {
						t.Fatal("terminal transition reused grant or dispatched")
					}
				})
			}
		}
	}
}
