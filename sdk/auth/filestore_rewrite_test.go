package auth

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f testRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFileTokenStoreReadAntigravityProjectIDDoesNotRecreateDeletedFile(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "antigravity.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"antigravity","access_token":"test-token"}`), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	previousTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-releaseResponse
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"project-from-api"}`)),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() {
		http.DefaultClient.Transport = previousTransport
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	result := make(chan error, 1)
	go func() {
		_, errRead := store.readAuthFile(path, baseDir)
		result <- errRead
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for project ID request")
	}
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatalf("Remove() error = %v", errRemove)
	}
	close(releaseResponse)

	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for readAuthFile()")
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("Stat() error = %v, want file to remain absent", errStat)
	}
}
