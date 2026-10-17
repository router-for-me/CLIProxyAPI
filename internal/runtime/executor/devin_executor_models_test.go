package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestDevinExecutorFetchModels(t *testing.T) {
	var entry []byte
	entry = protowire.AppendTag(entry, 1, protowire.BytesType)
	entry = protowire.AppendString(entry, "Fetch Test High")
	entry = protowire.AppendTag(entry, 22, protowire.BytesType)
	entry = protowire.AppendString(entry, "fetch-test-model-high")
	var resp []byte
	resp = protowire.AppendTag(resp, 1, protowire.BytesType)
	resp = protowire.AppendBytes(resp, entry)

	var gotPath, gotAuth, gotContentType string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write(resp)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "devin-fetch", Provider: "devin", Attributes: map[string]string{
		"api_key":  "test-key",
		"base_url": server.URL,
	}}
	models, err := NewDevinExecutor(&config.Config{}).FetchModels(context.Background(), auth)
	if err != nil {
		t.Fatalf("FetchModels() error = %v", err)
	}
	if gotPath != helps.DevinGetCliModelConfigsPath {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Basic test-key-test-key" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if gotContentType != "application/proto" {
		t.Errorf("content-type = %q", gotContentType)
	}
	if num, typ, n := protowire.ConsumeTag(gotBody); num != 1 || typ != protowire.BytesType || n < 0 || !bytes.Contains(gotBody, []byte("test-key")) {
		t.Errorf("request body is not client metadata carrying the api key")
	}
	found := false
	for _, m := range models {
		if m.ID == "devin/fetch-test-model" {
			found = m.DisplayName == "Fetch Test" && m.Thinking != nil && len(m.Thinking.Levels) == 1 && m.Thinking.Levels[0] == "high"
		}
	}
	if !found {
		t.Errorf("devin/fetch-test-model missing or malformed in %+v", models)
	}
}

func TestDevinExecutorFetchModels_Errors(t *testing.T) {
	status := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()

	exec := NewDevinExecutor(&config.Config{})
	if _, err := exec.FetchModels(context.Background(), &cliproxyauth.Auth{Provider: "devin"}); err == nil {
		t.Error("expected error without credentials")
	}
	auth := &cliproxyauth.Auth{Provider: "devin", Attributes: map[string]string{"api_key": "k", "base_url": server.URL}}
	if _, err := exec.FetchModels(context.Background(), auth); err == nil {
		t.Error("expected error for non-200 status")
	}
	status = http.StatusOK
	if _, err := exec.FetchModels(context.Background(), auth); err == nil {
		t.Error("expected error for empty model list")
	}
}
