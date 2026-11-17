package management

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type routingReceiptFlow struct {
	body    string
	lower   chan time.Time
	entered chan time.Time
	release chan struct{}
	once    sync.Once
}

func newRoutingReceiptFlow(body string) *routingReceiptFlow {
	return &routingReceiptFlow{body: body, lower: make(chan time.Time, 1), entered: make(chan time.Time, 1), release: make(chan struct{})}
}
func (f *routingReceiptFlow) allowBody() { f.once.Do(func() { close(f.release) }) }

type routingReceiptConn struct {
	net.Conn
	flow                     *routingReceiptFlow
	header                   string
	headersDone, bodyStarted bool
}

func (c *routingReceiptConn) Read(p []byte) (int, error) {
	if c.headersDone && !c.bodyStarted {
		c.bodyStarted = true
		c.flow.entered <- time.Now()
		<-c.flow.release
	}
	n, err := c.Conn.Read(p)
	if !c.headersDone {
		c.header += string(p[:n])
		c.headersDone = strings.Contains(c.header, "\r\n\r\n")
	}
	return n, err
}

func routingReceiptUsage(provider string, year int) string {
	if provider == "claude" {
		return fmt.Sprintf(`{"seven_day":{"utilization":1,"resets_at":"%d-01-01T00:00:00Z"},"five_hour":null}`, year)
	}
	at := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	return fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":604800,"reset_at":%d},"secondary_window":null}}`, at)
}

func routingReceiptFixture(t *testing.T, provider string, flows ...*routingReceiptFlow) (*coreauth.Manager, *coreauth.Auth, func() <-chan *httptest.ResponseRecorder) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	auth, err := manager.Register(context.Background(), &coreauth.Auth{ID: "receipt-" + provider, Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"access_token": "selected-token", "account_id": "selected-account"}})
	if err != nil {
		t.Fatal(err)
	}
	url := "https://api.anthropic.com/api/oauth/usage"
	headers := map[string]string{"Authorization": "Bearer $TOKEN$"}
	if provider == "codex" {
		url = "https://chatgpt.com/backend-api/wham/usage"
		headers["Chatgpt-Account-Id"] = "selected-account"
	}
	doc := ojson.NewDocString(`{}`)
	for key, value := range map[string]any{"authIndex": auth.EnsureIndex(), "method": "GET", "url": url, "header": headers} {
		if err := doc.Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	payload := doc.String()
	handler := &Handler{authManager: manager}
	router := gin.New()
	router.POST("/v8/management/requests/api-call", func(c *gin.Context) { c.Set(ConfigV8ContextKey, true); handler.APICall(c) })
	var mu sync.Mutex
	var servers, handlers sync.WaitGroup
	var connections []net.Conn
	next := 0
	previous := http.DefaultTransport
	transport := &http.Transport{DisableKeepAlives: true, DialTLSContext: func(_ context.Context, _, _ string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		if next >= len(flows) {
			return nil, fmt.Errorf("unexpected provider connection")
		}
		flow := flows[next]
		next++
		client, server := net.Pipe()
		connections = append(connections, client, server)
		servers.Add(1)
		go func() {
			defer servers.Done()
			defer server.Close()
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				t.Errorf("read provider request: %v", err)
				return
			}
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer selected-token" {
				t.Error("canonical provider request changed")
			}
			if provider == "codex" && request.Header.Get("Chatgpt-Account-Id") != "selected-account" {
				t.Error("canonical account changed")
			}
			flow.lower <- time.Now()
			if _, err = fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nX-Receipt-Evidence: preserved\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(flow.body)); err != nil {
				return
			}
			_, _ = io.WriteString(server, flow.body)
		}()
		return &routingReceiptConn{Conn: client, flow: flow}, nil
	}}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		for _, flow := range flows {
			flow.allowBody()
		}
		mu.Lock()
		for _, conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		handlers.Wait()
		servers.Wait()
		transport.CloseIdleConnections()
		http.DefaultTransport = previous
	})
	start := func() <-chan *httptest.ResponseRecorder {
		result := make(chan *httptest.ResponseRecorder, 1)
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			result <- recorder
		}()
		return result
	}
	return manager, auth, start
}

func routingReceiptTime(t *testing.T, ch <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-ch:
		return at
	case <-time.After(5 * time.Second):
		t.Fatal("receipt boundary not reached")
		return time.Time{}
	}
}
func routingReceiptResult(t *testing.T, ch <-chan *httptest.ResponseRecorder, want string) {
	t.Helper()
	var response *httptest.ResponseRecorder
	select {
	case response = <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("owned management call did not complete")
		return
	}
	var result apiCallResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("invalid management response: status=%d body=%s", response.Code, response.Body.String())
	}
	if result.StatusCode != 200 || result.Body != want || http.Header(result.Header).Get("X-Receipt-Evidence") != "preserved" {
		t.Fatal("forwarded provider response changed")
	}
}

func TestRoutingUsageReceiptBeforeBody(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			flow := newRoutingReceiptFlow(routingReceiptUsage(provider, 2030))
			manager, auth, start := routingReceiptFixture(t, provider, flow)
			result := start()
			lower := routingReceiptTime(t, flow.lower)
			upper := routingReceiptTime(t, flow.entered)
			flow.allowBody()
			routingReceiptResult(t, result, flow.body)
			after, _ := manager.GetByID(auth.ID)
			observed := after.QuotaResetSchedule.ObservedAt
			if observed.Before(lower) || observed.After(upper) {
				t.Fatalf("usage receipt was not captured between response headers and body read: lower=%s observed=%s upper=%s", lower, observed, upper)
			}
			if after.QuotaResetSchedule.WeeklyResetAt.Year() != 2030 {
				t.Fatal("weekly schedule missing")
			}
		})
	}
}

func TestRoutingUsageReceiptRetainsNewerStream(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			expired := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
			future := expired.Add(25 * time.Hour)
			var body string
			if provider == "claude" {
				body = fmt.Sprintf(`{"seven_day":{"utilization":1,"resets_at":%q},"five_hour":null}`, expired.Format(time.RFC3339))
			} else {
				body = fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":604800,"reset_at":%d},"secondary_window":null}}`, expired.Unix())
			}
			old := newRoutingReceiptFlow(body)
			manager, auth, start := routingReceiptFixture(t, provider, old)
			oldResult := start()
			routingReceiptTime(t, old.lower)
			routingReceiptTime(t, old.entered)
			before, _ := manager.GetByID(auth.ID)
			if before.Generation != auth.Generation {
				t.Fatal("usage changed generation before its body completed")
			}
			headers := http.Header{}
			if provider == "claude" {
				headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(future.Unix(), 10))
			} else {
				headers.Set("X-Codex-Primary-Window-Minutes", "10080")
				headers.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(future.Unix(), 10))
			}
			streamCtx := internallogging.WithResponseHeadersHolder(context.Background())
			receivedAt := time.Now()
			internallogging.SetResponseHeadersAt(streamCtx, headers, receivedAt)
			old.allowBody()
			routingReceiptResult(t, oldResult, old.body)
			afterUsage, _ := manager.GetByID(auth.ID)
			if !afterUsage.QuotaResetSchedule.WeeklyResetAt.Equal(expired) || afterUsage.Generation <= auth.Generation {
				t.Fatal("late usage did not publish its reset through the real generation guard")
			}
			// Stream completion uses MarkResult to merge the response holder's original receipt.
			manager.MarkResult(streamCtx, coreauth.Result{AuthID: auth.ID, Provider: provider, Success: true})
			after, _ := manager.GetByID(auth.ID)
			if !after.QuotaResetSchedule.WeeklyResetAt.Equal(future) || !after.QuotaResetSchedule.ObservedAt.Equal(receivedAt) {
				t.Fatalf("late usage body displaced newer stream receipt: schedule=%+v receivedAt=%s", after.QuotaResetSchedule, receivedAt)
			}
		})
	}
}
