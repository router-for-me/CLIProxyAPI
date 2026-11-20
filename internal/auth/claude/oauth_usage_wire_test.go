package claude

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/textproto"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
)

// TestFetchOAuthUsageWireHeaderOrderMatchesAuthenticatedGET drives the usage
// poll through the same ordered connection the uTLS transport installs and
// checks the serialized header order, not the pre-transport http.Request: the
// listed Axios GET headers in the captured order, then the request's own
// anthropic-beta after them.
func TestFetchOAuthUsageWireHeaderOrderMatchesAuthenticatedGET(t *testing.T) {
	t.Parallel()

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		if errClose := clientConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close client connection: %v", errClose)
		}
		if errClose := serverConn.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Errorf("close server connection: %v", errClose)
		}
	})

	headDone := make(chan []string, 1)
	go func() {
		if errDeadline := serverConn.SetDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
			headDone <- nil
			return
		}
		reader := bufio.NewReader(serverConn)
		var names []string
		requestLine, errLine := reader.ReadString('\n')
		if errLine != nil || !strings.HasPrefix(requestLine, "GET /api/oauth/usage HTTP/1.1") {
			headDone <- nil
			return
		}
		for {
			line, errRead := reader.ReadString('\n')
			if errRead != nil {
				headDone <- nil
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			name, _, _ := strings.Cut(line, ":")
			names = append(names, textproto.CanonicalMIMEHeaderKey(name))
		}
		body := `{"limits":[{"kind":"session","percent":12}]}`
		response := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " +
			strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n" + body
		if _, errWrite := serverConn.Write([]byte(response)); errWrite != nil {
			headDone <- nil
			return
		}
		headDone <- names
	}()

	transport := &http.Transport{
		DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			return httpwire.NewOrderedRequestConn(clientConn, claudeOAuthRequestHeaderOrder), nil
		},
		DisableCompression: true,
	}
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: transport}}
	if _, errFetch := auth.FetchOAuthUsage(context.Background(), "access"); errFetch != nil {
		t.Fatalf("FetchOAuthUsage: %v", errFetch)
	}

	var got []string
	select {
	case got = <-headDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out reading the usage request head")
	}
	if got == nil {
		t.Fatal("server side did not see a usage request")
	}

	present := make(map[string]bool, len(got))
	for _, name := range got {
		present[name] = true
	}
	var want []string
	for _, name := range claudeOAuthInspectHeaderOrder {
		if present[textproto.CanonicalMIMEHeaderKey(name)] {
			want = append(want, textproto.CanonicalMIMEHeaderKey(name))
		}
	}
	want = append(want, "Anthropic-Beta")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire header order = %v, want %v", got, want)
	}
	for _, required := range []string{"Authorization", "Cache-Control", "User-Agent", "Anthropic-Beta"} {
		if !present[required] {
			t.Fatalf("wire request lacks %s: %v", required, got)
		}
	}
}
