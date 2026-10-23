package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchStartPlanStatusActive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/zcode-plan/billing/balance" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("app_version"); got != StartPlanAppVersion {
			t.Fatalf("unexpected app_version: %s", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer zcode-jwt" {
			t.Fatalf("unexpected authorization header: %s", got)
		}
		if got := r.Header.Get("X-Device-Mid"); got != "device-mid" {
			t.Fatalf("unexpected device mid header: %q", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"server_time":1000,"plans":[{"plan_id":"zcode-v3-start-plan","name":"ZCode V3 Start Plan","status":"active","ends_at":2000}],"balances":[{"plan_id":"zcode-v3-start-plan","show_name":"GLM-5.3-Flash","capabilities":["model:GLM-5.3-Flash"],"total_units":100,"available_units":40,"period_start":0,"period_end":2000}]}}`))
	}))
	defer server.Close()

	service := NewService(server.Client())
	service.SetStartPlanBaseURL(server.URL + "/api/v1/zcode-plan")
	status, err := service.FetchStartPlanStatus(context.Background(), "zcode-jwt", "device-mid")
	if err != nil {
		t.Fatalf("FetchStartPlanStatus() error = %v", err)
	}
	if !status.Active {
		t.Fatal("FetchStartPlanStatus() expected active start plan")
	}
	if len(status.Models) != 1 || status.Models[0] != "glm-5.3-flash" {
		t.Fatalf("unexpected models: %#v", status.Models)
	}
}

func TestFetchStartPlanStatusInactive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"server_time":1000,"plans":[{"plan_id":"zcode-v3-start-plan","status":"expired","ends_at":500}],"balances":[{"plan_id":"zcode-v3-start-plan","capabilities":["model:GLM-5.3-Flash"]}]}}`))
	}))
	defer server.Close()

	service := NewService(server.Client())
	service.SetStartPlanBaseURL(server.URL + "/api/v1/zcode-plan")
	status, err := service.FetchStartPlanStatus(context.Background(), "zcode-jwt", "device-mid")
	if err != nil {
		t.Fatalf("FetchStartPlanStatus() error = %v", err)
	}
	if status.Active {
		t.Fatal("FetchStartPlanStatus() expected inactive start plan")
	}
	if len(status.Models) != 0 {
		t.Fatalf("expired plan buckets must not contribute models: %#v", status.Models)
	}
}

func TestStartPlanBalanceActiveStartPlan(t *testing.T) {
	var balance StartPlanBalance
	payload := `{"server_time":1000,"plans":[
		{"plan_id":"zcode-v3-start-plan","name":"ZCode V3 Start Plan","status":"active","ends_at":2000},
		{"plan_id":"other-plan","status":"active"}],
		"balances":[
		{"plan_id":"zcode-v3-start-plan","show_name":"GLM-5V-Turbo","total_units":10,"remaining_units":2},
		{"plan_id":"zcode-v3-start-plan","capabilities":["model:GLM-5.3","model:GLM-5.3-Flash"],"total_units":10},
		{"plan_id":"other-plan","capabilities":["model:GLM-4.5"]},
		{"show_name":"GLM-5.3"}]}`
	if err := json.Unmarshal([]byte(payload), &balance); err != nil {
		t.Fatal(err)
	}
	if !balance.ActiveStartPlan() {
		t.Fatal("ActiveStartPlan() = false, want true")
	}
	models := balance.ModelIDs()
	want := []string{"glm-5v-turbo", "glm-5.3", "glm-5.3-flash", "glm-4.5"}
	if len(models) != len(want) {
		t.Fatalf("ModelIDs() = %#v, want %v", models, want)
	}
	for i, id := range want {
		if models[i] != id {
			t.Fatalf("ModelIDs()[%d] = %s, want %s", i, models[i], id)
		}
	}
}

func TestStartPlanBalanceExpiredEndsAt(t *testing.T) {
	var balance StartPlanBalance
	payload := `{"server_time":1000,"plans":[{"plan_id":"zcode-v3-start-plan","status":"active","ends_at":1000}],"balances":[{"plan_id":"zcode-v3-start-plan","capabilities":["model:GLM-5.3"]}]}`
	if err := json.Unmarshal([]byte(payload), &balance); err != nil {
		t.Fatal(err)
	}
	if balance.ActiveStartPlan() {
		t.Fatal("ActiveStartPlan() = true for an expired active plan")
	}
}
