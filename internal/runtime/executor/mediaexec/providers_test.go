package mediaexec_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/mediaexec"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/media"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestHiggsfieldMapsFrozenRequest(t *testing.T) {
	var sawAuth string
	doer := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		body := `{"request_id":"req1","status":"queued"}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	h := &mediaexec.Higgsfield{Doer: doer}
	hdr := make(http.Header)
	hdr.Set("X-Media-Key-ID", "kid")
	hdr.Set("X-Media-Key-Secret", "sec")
	res, err := h.ExecuteMedia(context.Background(), media.Request{Phase: media.PhaseCreate, Model: "dop-turbo", Prompt: "p"}, media.Options{Headers: hdr})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sawAuth, "Key kid:sec") {
		t.Fatalf("auth=%q", sawAuth)
	}
	if !res.AcceptedHandle || !res.HTTPResponded {
		t.Fatalf("res=%+v", res)
	}
}

func TestKlingSelectedVersionUsesFrozenAuthScheme(t *testing.T) {
	k := &mediaexec.Kling{}
	hdr := make(http.Header)
	hdr.Set("X-Kling-Auth-Scheme", "legacy_jwt")
	res, err := k.ExecuteMedia(context.Background(), media.Request{Phase: media.PhaseCreate, Model: "kling-3.0"}, media.Options{Headers: hdr})
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != "invalid_auth" {
		t.Fatalf("want invalid_auth, got %+v", res)
	}
}

func TestMiniMaxMusicTaskSignatureRejectsSpeechFields(t *testing.T) {
	m := &mediaexec.MiniMaxMusic{}
	res, err := m.ExecuteMedia(context.Background(), media.Request{
		Phase: media.PhaseCreate, Model: "music-3.0", Prompt: "x",
		Params: map[string]any{"voice_id": "v1"},
	}, media.Options{Headers: http.Header{"Authorization": []string{"Bearer k"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != "invalid_request" {
		t.Fatalf("%+v", res)
	}
}

func TestKlingCreateDoesNotRetryAfterResponse(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"t1","task_status":"submitted"}}`))
	}))
	defer srv.Close()
	k := &mediaexec.Kling{BaseURL: srv.URL, Doer: http.DefaultClient}
	hdr := make(http.Header)
	hdr.Set("Authorization", "Bearer key")
	res, err := k.ExecuteMedia(context.Background(), media.Request{Phase: media.PhaseCreate, Model: "kling-3.0", Prompt: "p"}, media.Options{Headers: hdr})
	if err != nil {
		t.Fatal(err)
	}
	if !res.HTTPResponded || calls != 1 {
		t.Fatalf("calls=%d responded=%v", calls, res.HTTPResponded)
	}
	var h map[string]string
	_ = json.Unmarshal(res.Handle, &h)
	if h["task_id"] != "t1" {
		t.Fatalf("handle=%v", h)
	}
}
