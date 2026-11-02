package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func credentialPriorityFixture(t *testing.T) (*Handler, *coreauth.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-fixture.json")
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, &coreauth.RoundRobinSelector{}, nil)
	_, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: "claude-fixture.json", FileName: "claude-fixture.json", Provider: "claude",
		Attributes: map[string]string{"path": path}, Metadata: map[string]any{"type": "claude", "fixture_marker": "preserved"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager), manager, path
}

func patchCredentialPriorityFixture(handler *Handler, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/priority", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler.PatchCredentialPriority(ctx)
	return response
}

func TestCredentialPriorityCASRestoresExactAbsenceZeroAndNegative(t *testing.T) {
	for _, original := range []string{"null", "0", "-7"} {
		t.Run(original, func(t *testing.T) {
			handler, manager, file := credentialPriorityFixture(t)
			patch := func(value, expected string) {
				t.Helper()
				response := patchCredentialPriorityFixture(handler, fmt.Sprintf(`{"name":"claude-fixture.json","priority":%s,"expected_priority":%s}`, value, expected))
				if response.Code != http.StatusOK {
					t.Fatalf("PATCH = %d %s", response.Code, response.Body.String())
				}
			}
			if original != "null" {
				patch(original, "null")
			}
			patch("4", original)
			patch(original, "4")
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err = json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			value, present := data["priority"]
			if original == "null" && present {
				t.Fatalf("original absence became %v", value)
			}
			if original == "0" && (!present || value != float64(0)) {
				t.Fatalf("zero lost: %v", data)
			}
			if original == "-7" && (!present || value != float64(-7)) {
				t.Fatalf("negative lost: %v", data)
			}
			if data["fixture_marker"] != "preserved" {
				t.Fatal("unrelated metadata changed")
			}
			// Reload through the real store, rather than trusting the immediate JSON response.
			loaded, err := managerStoreRecords(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded) != 1 {
				t.Fatalf("store records = %d", len(loaded))
			}
			if sameCredentialPriority(credentialPriority(loaded[0]), nil) != (original == "null") {
				t.Fatal("reload changed priority presence")
			}
			current, _ := manager.GetByID("claude-fixture.json")
			if current.Metadata["fixture_marker"] != "preserved" {
				t.Fatal("runtime metadata changed")
			}
		})
	}
}

func managerStoreRecords(file string) ([]*coreauth.Auth, error) {
	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(filepath.Dir(file))
	return store.List(context.Background())
}

func TestCredentialPriorityCASConflictsDoNotOverwrite(t *testing.T) {
	handler, manager, _ := credentialPriorityFixture(t)
	responses := make(chan int, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, value := range []int{3, 4} {
		workers.Add(1)
		go func(value int) {
			defer workers.Done()
			<-start
			response := patchCredentialPriorityFixture(handler, fmt.Sprintf(`{"name":"claude-fixture.json","priority":%d,"expected_priority":null}`, value))
			responses <- response.Code
		}(value)
	}
	close(start)
	workers.Wait()
	close(responses)
	counts := map[int]int{}
	for status := range responses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent statuses = %v", counts)
	}
	current, _ := manager.GetByID("claude-fixture.json")
	if p := credentialPriority(current); p == nil || (*p != 3 && *p != 4) {
		t.Fatalf("priority = %v", p)
	}
}

func TestCredentialPriorityCASRejectsMissingAndInvalidPreconditions(t *testing.T) {
	handler, _, _ := credentialPriorityFixture(t)
	for _, body := range []string{
		`{"name":"claude-fixture.json","priority":4}`,
		`{"name":"claude-fixture.json","priority":1.5,"expected_priority":null}`,
		`{"name":"claude-fixture.json","priority":4,"expected_priority":"0"}`,
		`{"name":"claude-fixture.json","priority":9007199254740992,"expected_priority":null}`,
	} {
		if response := patchCredentialPriorityFixture(handler, body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid precondition accepted: %s -> %d", body, response.Code)
		}
	}
}

type failingPriorityStore struct{ memoryAuthStore }

func (*failingPriorityStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", errors.New("synthetic storage failure")
}

func TestCredentialPriorityCASPersistenceFailureDoesNotPublish(t *testing.T) {
	handler, manager, file := credentialPriorityFixture(t)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetStore(&failingPriorityStore{})
	response := patchCredentialPriorityFixture(handler, `{"name":"claude-fixture.json","priority":4,"expected_priority":null}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure acknowledged: %d", response.Code)
	}
	current, _ := manager.GetByID("claude-fixture.json")
	if credentialPriority(current) != nil {
		t.Fatal("failed durable mutation published to runtime")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed durable mutation changed stored credential")
	}
}
