package management

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	previousServerID = "e8bd1fce-3d81-4e40-9589-17b8709159eb"
	updatedServerID  = "8237fe4a-1419-4a20-8b39-77dbc44168cc"
)

func TestManagementIdentityRemainsBoundToAuthenticatedRequest(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	h := NewHandlerWithoutConfigFilePath(&config.Config{ServerID: previousServerID}, nil)
	authenticated, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	router := gin.New()
	router.GET("/identity", h.Middleware(), func(c *gin.Context) {
		if c.Query("pause") == "true" {
			close(authenticated)
			<-resume
		}
	}, h.GetServerID)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-management-key")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	check := func(res *httptest.ResponseRecorder, id string) {
		t.Helper()
		if got := res.Header().Get(ServerIDHeader); res.Code != http.StatusOK || got != id || res.Body.String() != fmt.Sprintf(`{"server-id":%q}`, id) {
			t.Fatalf("response = %d, %q, %s; want ID %q", res.Code, got, res.Body, id)
		}
	}
	inFlight := make(chan *httptest.ResponseRecorder, 1)
	go func() { inFlight <- get("/identity?pause=true") }()
	select {
	case <-authenticated:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not authenticate")
	}
	// Publish a replacement between authentication and the endpoint's read.
	h.SetConfig(&config.Config{ServerID: updatedServerID})
	check(get("/identity"), updatedServerID)
	release()
	select {
	case res := <-inFlight:
		check(res, previousServerID)
	case <-time.After(5 * time.Second):
		t.Fatal("authenticated request did not resume")
	}
}

func TestConfigV8ConcurrentFileWriterDoesNotLoseNewSnapshot(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	for _, conditional := range []bool{false, true} {
		t.Run(fmt.Sprintf("If-Match=%t", conditional), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			original := []byte("server: {server-id: " + previousServerID + "}\n---\noperator: original\n")
			if errCreate := config.CreateConfigFile(path, original); errCreate != nil {
				t.Fatal(errCreate)
			}
			initial, errInitial := config.ReadConfigFileSnapshot(path)
			if errInitial != nil {
				t.Fatal(errInitial)
			}
			h := NewHandler(&config.Config{ServerID: previousServerID}, path, nil)
			router := gin.New()
			router.Use(h.Middleware())
			router.PATCH("/config", h.ConfigV8)
			router.GET("/config/*path", h.ConfigV8)
			latest := []byte("server: {server-id: " + updatedServerID + "}\n---\noperator: updated\n")
			body := &commitConfigOnRead{
				Reader: strings.NewReader(`{"server":{"server-id":null}}`),
				commit: func() {
					if _, errWrite := config.WriteConfigFileCAS(path, initial, latest); errWrite != nil {
						t.Fatal(errWrite)
					}
				},
			}
			req := httptest.NewRequest(http.MethodPatch, "/config", body)
			req.Header.Set("Authorization", "Bearer test-management-key")
			status := http.StatusConflict
			if conditional {
				req.Header.Set("If-Match", configV8ETag(initial.SourceHash()))
				status = http.StatusPreconditionFailed
			}
			res := httptest.NewRecorder()
			router.ServeHTTP(res, req)
			if res.Code != status || res.Header().Get(ServerIDHeader) != previousServerID || res.Header().Get("ETag") != configV8ETag(config.ComputeSourceHash(latest)) {
				t.Fatalf("response = %d, %v, %s", res.Code, res.Header(), res.Body)
			}
			data, errRead := config.ReadConfigFile(path)
			if errRead != nil || string(data) != string(latest) {
				t.Fatalf("concurrent writer's config lost: %v, %s", errRead, data)
			}
			// The identity in the config GET comes from its disk snapshot,
			// even before the watcher publishes that snapshot as runtime config.
			get := httptest.NewRequest(http.MethodGet, "/config/server/server-id", nil)
			get.Header.Set("Authorization", "Bearer test-management-key")
			got := httptest.NewRecorder()
			router.ServeHTTP(got, get)
			if got.Code != http.StatusOK || got.Header().Get(ServerIDHeader) != updatedServerID || got.Body.String() != fmt.Sprintf("%q", updatedServerID) {
				t.Fatalf("config GET = %d, %v, %s", got.Code, got.Header(), got.Body)
			}
		})
	}
}

type commitConfigOnRead struct {
	io.Reader
	commit func()
}

func (r *commitConfigOnRead) Read(b []byte) (int, error) {
	if commit := r.commit; commit != nil {
		r.commit = nil
		commit()
	}
	return r.Reader.Read(b)
}
