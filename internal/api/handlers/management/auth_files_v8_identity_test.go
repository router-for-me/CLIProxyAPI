package management

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAuthFileUploadVersionedPluginSemantics(t *testing.T) {
	for _, version := range []string{"v0", "v8"} {
		for _, multipartUpload := range []bool{false, true} {
			for _, outcome := range []string{"multi", "error", "empty"} {
				name := version + "/" + outcome
				if multipartUpload {
					name += "/multipart"
				}
				t.Run(name, func(t *testing.T) {
					h, dir := newIdentityTestHandler(t)
					h.tokenStore = &memoryAuthStore{}
					switch outcome {
					case "multi":
						h.pluginAuthParser = &fakeMultiPluginAuthParser{handled: true, auths: []*coreauth.Auth{{ID: "child-a", Provider: "plugin-provider"}, {ID: "child-b", Provider: "plugin-provider"}}}
					case "empty":
						h.pluginAuthParser = &fakeMultiPluginAuthParser{handled: true}
					case "error":
						h.pluginAuthParser = &failingMultiPluginAuthParser{failingPluginAuthParser{handled: true, err: errors.New("parser failure")}}
					}
					router := gin.New()
					router.POST("/v0/management/auth-files", h.UploadAuthFile)
					router.POST("/v8/management/auth-files", h.UploadAuthFileV8)
					payload := []byte(`{"type":"plugin-provider"}`)
					var body bytes.Buffer
					contentType := "application/json"
					if multipartUpload {
						writer := multipart.NewWriter(&body)
						part, errPart := writer.CreateFormFile("file", "source.json")
						if errPart != nil {
							t.Fatal(errPart)
						}
						if _, errWrite := part.Write(payload); errWrite != nil {
							t.Fatal(errWrite)
						}
						if errClose := writer.Close(); errClose != nil {
							t.Fatal(errClose)
						}
						contentType = writer.FormDataContentType()
					} else {
						body.Write(payload)
					}
					req := httptest.NewRequest(http.MethodPost, "/"+version+"/management/auth-files?name=source.json", &body)
					req.Header.Set("Content-Type", contentType)
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					wantStatus, wantCount := http.StatusOK, 1
					if version == "v8" {
						switch outcome {
						case "multi":
							wantCount = 2
						case "empty":
							wantCount = 0
						case "error":
							wantStatus = http.StatusBadRequest
							wantCount = 0
						}
					}
					if rec.Code != wantStatus {
						t.Fatalf("status=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
					}
					auths := h.authManager.List()
					if len(auths) != wantCount {
						t.Fatalf("auth count=%d want=%d", len(auths), wantCount)
					}
					if version == "v0" && (auths[0].ID != "source.json" || auths[0].FileName != "source.json") {
						t.Fatalf("legacy identity changed: %v", auths[0])
					}
					_, errStat := os.Stat(filepath.Join(dir, "source.json"))
					if wantStatus == http.StatusBadRequest {
						if !os.IsNotExist(errStat) {
							t.Fatalf("rejected upload reached disk: %v", errStat)
						}
					} else if errStat != nil {
						t.Fatal(errStat)
					}
				})
			}
		}
	}
}

func TestManagedPluginFileNamesMatchWatcher(t *testing.T) {
	h, dir := newIdentityTestHandler(t)
	h.tokenStore = &memoryAuthStore{}
	source := filepath.Join(dir, "source.json")
	data := []byte(`{"type":"plugin-provider"}`)
	newParser := func() *fakeMultiPluginAuthParser {
		return &fakeMultiPluginAuthParser{handled: true, auths: []*coreauth.Auth{
			{ID: "child-a", Provider: "plugin-provider", FileName: "named-child.json"},
			{ID: "child-b", Provider: "plugin-provider"},
		}}
	}
	h.pluginAuthParser = newParser()
	if errWrite := h.writeAuthFileV8(context.Background(), filepath.Base(source), data); errWrite != nil {
		t.Fatal(errWrite)
	}
	watched, errSynthesize := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config: h.cfg, AuthDir: dir, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator(), PluginAuthParser: newParser(), StrictFileParsing: true,
	}, source, data)
	if errSynthesize != nil {
		t.Fatal(errSynthesize)
	}
	for _, expected := range watched {
		got, exists := h.authManager.GetByID(expected.ID)
		wantName := expected.FileName
		if wantName == "" {
			wantName = filepath.Base(source)
		}
		if !exists || got.FileName != wantName {
			t.Fatalf("record %q: management=%v expected filename=%q", expected.ID, got, wantName)
		}
	}
}
