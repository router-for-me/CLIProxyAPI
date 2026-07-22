package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type codexRoundTripFunc func(*http.Request) (*http.Response, error)

func (f codexRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDoCodexHTTPRequestTimesOutWaitingForHeaders(t *testing.T) {
	client := &http.Client{Transport: codexRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	started := time.Now()
	_, err = doCodexHTTPRequest(context.Background(), client, req, 30*time.Millisecond)
	if err == nil {
		t.Fatal("doCodexHTTPRequest() error = nil, want response header timeout")
	}
	var timeoutErr codexRequestTimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.phase != "response header" {
		t.Fatalf("doCodexHTTPRequest() error = %T %v, want response header timeout", err, err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("response header timeout took %s", elapsed)
	}
}

func TestCodexActivityTimeoutBodyTimesOutBeforeFirstEvent(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	body := newCodexActivityTimeoutBody(context.Background(), reader, 30*time.Millisecond, time.Second)
	defer body.Close()

	_, err := body.Read(make([]byte, 1))
	var timeoutErr codexRequestTimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.phase != "first event" {
		t.Fatalf("Read() error = %T %v, want first event timeout", err, err)
	}
}

func TestCodexActivityTimeoutBodyTimesOutOnMidstreamStall(t *testing.T) {
	reader, writer := io.Pipe()
	body := newCodexActivityTimeoutBody(context.Background(), reader, time.Second, 30*time.Millisecond)
	defer body.Close()
	go func() {
		_, _ = writer.Write([]byte("a"))
	}()

	buffer := make([]byte, 1)
	if n, err := body.Read(buffer); n != 1 || err != nil {
		t.Fatalf("first Read() = (%d, %v), want (1, nil)", n, err)
	}
	_, err := body.Read(buffer)
	var timeoutErr codexRequestTimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.phase != "stream idle" {
		t.Fatalf("second Read() error = %T %v, want stream idle timeout", err, err)
	}
	_ = writer.Close()
}

func TestCodexActivityTimeoutBodyAllowsLongActiveOutput(t *testing.T) {
	reader, writer := io.Pipe()
	body := newCodexActivityTimeoutBody(context.Background(), reader, 100*time.Millisecond, 50*time.Millisecond)
	defer body.Close()
	go func() {
		defer writer.Close()
		for _, chunk := range []string{"one", "two", "three", "four"} {
			_, _ = writer.Write([]byte(chunk))
			time.Sleep(20 * time.Millisecond)
		}
	}()

	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got, want := string(data), "onetwothreefour"; got != want {
		t.Fatalf("ReadAll() = %q, want %q", got, want)
	}
}

func TestCodexActivityTimeoutBodyPrefersContextDeadline(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	body := newCodexActivityTimeoutBody(ctx, reader, 40*time.Millisecond, time.Second)
	defer body.Close()

	_, err := body.Read(make([]byte, 1))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Read() error = %v, want context deadline exceeded", err)
	}
}

func TestDoCodexHTTPRequestCancelsRequestWhenBodyCloses(t *testing.T) {
	canceled := make(chan struct{})
	client := &http.Client{Transport: codexRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		go func() {
			<-req.Context().Done()
			close(canceled)
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString("ok")),
		}, nil
	})}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", strings.NewReader(""))
	response, err := doCodexHTTPRequest(context.Background(), client, req, time.Second)
	if err != nil {
		t.Fatalf("doCodexHTTPRequest() error = %v", err)
	}
	if errClose := response.Body.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("request context was not canceled when response body closed")
	}
}
