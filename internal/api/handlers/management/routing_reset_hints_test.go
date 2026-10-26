package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPutRoutingResetHints(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	selector := &coreauth.SoonestResetFirstSelector{}
	manager := coreauth.NewManager(nil, selector, nil)
	a := &coreauth.Auth{ID: "a", FileName: "a.json", Provider: "codex", Attributes: map[string]string{"weight": "20", "priority": "50"}}
	b := &coreauth.Auth{ID: "b", FileName: "b.json", Provider: "codex", Attributes: map[string]string{"weight": "1", "priority": "50"}}
	for _, auth := range []*coreauth.Auth{a, b} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	now := time.Now().UTC().Add(-time.Second)
	body := fmt.Sprintf(`{"accounts":[{"auth_index":%q,"observed_at":%q,"reset_at":%q}]}`, b.EnsureIndex(), now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	request := func(body string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/v8/management/routing/reset-hints", strings.NewReader(body))
		h.PutRoutingResetHints(c)
		if rec.Code != want {
			t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body.String())
		}
	}
	request(body, http.StatusOK)
	entry := fmt.Sprintf(`{"auth_index":%q,"observed_at":%q,"reset_at":%q}`, b.EnsureIndex(), now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	futureObservation := fmt.Sprintf(`{"accounts":[{"auth_index":%q,"observed_at":%q,"reset_at":%q}]}`, b.EnsureIndex(), now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(2*time.Hour).Format(time.RFC3339Nano))
	for _, invalid := range []string{
		`{}`, `{"accounts":null}`, `{"accounts":[],"priority":100}`, body + ` {}`,
		`{"accounts":[{"auth_index":"b","observed_at":"bad"}]}`,
		`{"accounts":[` + entry + `,` + entry + `]}`,
		`{"accounts":[` + strings.Repeat(entry+",", 1024) + entry + `]}`,
		strings.Repeat(" ", 256<<10) + body,
		futureObservation,
	} {
		request(invalid, http.StatusBadRequest)
	}
	got, err := selector.Pick(context.Background(), "codex", "m", cliproxyexecutor.Options{}, []*coreauth.Auth{a, b})
	if err != nil || got == nil || got.ID != "b" {
		t.Fatalf("pick after invalid payload = %v, %v", got, err)
	}
	updated, _ := manager.GetByID("b")
	if updated.Attributes["weight"] != "1" || updated.Attributes["priority"] != "50" || updated.Unavailable {
		t.Fatalf("credential mutated: %+v", updated)
	}
	request(`{"accounts":[]}`, http.StatusOK)
	got, err = selector.Pick(context.Background(), "codex", "m", cliproxyexecutor.Options{}, []*coreauth.Auth{a, b})
	if err != nil || got == nil || got.ID != "a" {
		t.Fatalf("pick after clear = %v, %v", got, err)
	}
	manager.SetSelector(&coreauth.WeightedRoundRobinSelector{})
	request(body, http.StatusConflict)
}

func TestPutRoutingResetHintsDropsStaleAndUnknown(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(nil, &coreauth.SoonestResetFirstSelector{}, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	now := time.Now().UTC().Add(-time.Second)
	for _, observed := range []time.Time{now, now.Add(-time.Hour)} {
		body := fmt.Sprintf(`{"accounts":[{"auth_index":"removed-account","observed_at":%q,"reset_at":%q}]}`, observed.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/v8/management/routing/reset-hints", strings.NewReader(body))
		h.PutRoutingResetHints(c)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accepted":0`) {
			t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
		}
	}
}
