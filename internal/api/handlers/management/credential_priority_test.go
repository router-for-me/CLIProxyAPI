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
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
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

type skippedPriorityStore struct{ memoryAuthStore }

func (*skippedPriorityStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", nil
}

func TestCredentialPriorityCASSkippedSaveDoesNotPublish(t *testing.T) {
	handler, manager, file := credentialPriorityFixture(t)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetStore(&skippedPriorityStore{})
	response := patchCredentialPriorityFixture(handler, `{"name":"claude-fixture.json","priority":4,"expected_priority":null}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("skipped save acknowledged: %d", response.Code)
	}
	current, _ := manager.GetByID("claude-fixture.json")
	if credentialPriority(current) != nil {
		t.Fatal("skipped durable mutation published to runtime")
	}
	after, err := os.ReadFile(file)
	if err != nil || string(before) != string(after) {
		t.Fatal("skipped durable mutation changed stored credential")
	}
	// Ordinary mutations retain their pre-existing non-fatal persistence policy.
	_, committed, err := manager.Modify(context.Background(), current.ID, current.RegistrationEpoch, func(auth *coreauth.Auth) error {
		auth.Metadata["priority"] = 2
		return nil
	})
	if err != nil || !committed {
		t.Fatalf("ordinary skipped save changed behavior: committed=%v, err=%v", committed, err)
	}
}

func TestCredentialPriorityCASDoesNotRecreateDeletedDisabledFile(t *testing.T) {
	handler, manager, file := credentialPriorityFixture(t)
	current, _ := manager.GetByID("claude-fixture.json")
	_, committed, err := manager.Modify(context.Background(), current.ID, current.RegistrationEpoch, func(auth *coreauth.Auth) error {
		auth.Disabled = true
		auth.Status = coreauth.StatusDisabled
		return nil
	})
	if err != nil || !committed {
		t.Fatalf("disable fixture: committed=%v, err=%v", committed, err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	response := patchCredentialPriorityFixture(handler, `{"name":"claude-fixture.json","priority":4,"expected_priority":null}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("deleted disabled credential acknowledged: %d", response.Code)
	}
	current, _ = manager.GetByID("claude-fixture.json")
	if !current.Disabled || credentialPriority(current) != nil {
		t.Fatal("skipped mutation changed disabled runtime credential")
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("deleted credential was recreated")
	}
}

func TestCredentialPriorityCASRejectsPendingCodexRefresh(t *testing.T) {
	for _, original := range []string{"null", "0", "-7", "4"} {
		for _, desired := range []string{"4", "0", "null"} {
			t.Run(original+"-to-"+desired, func(t *testing.T) {
				dir := t.TempDir()
				file := filepath.Join(dir, "codex-fixture.json")
				store := fileauth.NewFileTokenStore()
				store.SetBaseDir(dir)
				manager := coreauth.NewManager(store, &coreauth.RoundRobinSelector{}, nil)
				metadata := map[string]any{"type": "codex", "access_token": "synthetic-old-access", "refresh_token": "synthetic-refresh", "fixture_marker": "preserved"}
				initialPriority, err := decodeCredentialPriority(json.RawMessage(original))
				if err != nil {
					t.Fatal(err)
				}
				if initialPriority != nil {
					metadata["priority"] = *initialPriority
				}
				base, err := manager.Register(context.Background(), &coreauth.Auth{
					ID: "codex-fixture.json", FileName: "codex-fixture.json", Provider: "codex",
					Attributes: map[string]string{"path": file}, Metadata: metadata,
				})
				if err != nil {
					t.Fatal(err)
				}
				// Reproduce the real pending state: an ordinary refresh publishes its
				// new credentials even when its scoped patch could not reach storage.
				manager.SetStore(&failingPriorityStore{})
				refreshed := base.Clone()
				refreshed.Metadata["access_token"] = "synthetic-new-access"
				refreshed.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, refreshed.Metadata, true, false)
				before, err := manager.UpdateRefreshedAuth(context.Background(), base, refreshed)
				if err != nil || before == nil || before.CodexRefreshPatch == nil {
					t.Fatalf("pending refresh fixture not retained: %v", err)
				}
				storedBefore, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				manager.SetStore(store)
				handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
				body := fmt.Sprintf(`{"name":"codex-fixture.json","priority":%s,"expected_priority":%s}`, desired, original)
				response := patchCredentialPriorityFixture(handler, body)
				if response.Code != http.StatusConflict {
					t.Fatalf("pending refresh acknowledged priority edit: %d", response.Code)
				}
				after, _ := manager.GetByID(base.ID)
				if !reflect.DeepEqual(after, before) {
					t.Fatal("rejected edit changed pending runtime credentials or priority")
				}
				storedAfter, err := os.ReadFile(file)
				if err != nil || string(storedAfter) != string(storedBefore) {
					t.Fatal("rejected edit changed stored credentials or priority")
				}
				// Once ordinary persistence recovers, a retry must be durable and
				// preserve the now-saved refreshed credentials.
				if _, err = manager.Update(context.Background(), after); err != nil {
					t.Fatal(err)
				}
				response = patchCredentialPriorityFixture(handler, body)
				if response.Code != http.StatusOK {
					t.Fatalf("recovered edit rejected: %d", response.Code)
				}
				loaded, err := managerStoreRecords(file)
				if err != nil || len(loaded) != 1 {
					t.Fatalf("reload after recovered edit: %v", err)
				}
				want, _ := decodeCredentialPriority(json.RawMessage(desired))
				if !sameCredentialPriority(credentialPriority(loaded[0]), want) || loaded[0].Metadata["access_token"] != "synthetic-new-access" || loaded[0].Metadata["fixture_marker"] != "preserved" {
					t.Fatal("recovered edit lost priority or refreshed credentials")
				}
			})
		}
	}
}
