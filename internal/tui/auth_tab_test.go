package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAuthTabImmediateDeleteAbnormalAccount(t *testing.T) {
	var gotMethod string
	var gotName string
	var gotRawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotName = r.URL.Query().Get("name")
		gotRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := &Client{
		baseURL: server.URL,
		http:    server.Client(),
	}
	model := newAuthTabModel(client)
	model.SetSize(100, 20)
	model.files = []map[string]any{
		{
			"name":             "codex-bad@example.com.json",
			"provider":         "codex",
			"email":            "bad@example.com",
			"status_code":      float64(http.StatusForbidden),
			"account_abnormal": true,
		},
	}

	updated, cmd := model.handleNormalInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if updated.confirm != -1 {
		t.Fatalf("confirm = %d, want -1", updated.confirm)
	}
	if cmd == nil {
		t.Fatal("expected immediate delete command")
	}
	msg := cmd()
	actionMsg, ok := msg.(authActionMsg)
	if !ok {
		t.Fatalf("message type = %T, want authActionMsg", msg)
	}
	if actionMsg.err != nil {
		t.Fatalf("delete returned error: %v", actionMsg.err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q, want %q", gotMethod, http.MethodDelete)
	}
	if gotName != "codex-bad@example.com.json" {
		t.Fatalf("name query = %q, want %q", gotName, "codex-bad@example.com.json")
	}
	wantRawQuery := "name=" + url.QueryEscape("codex-bad@example.com.json")
	if gotRawQuery != wantRawQuery {
		t.Fatalf("raw query = %q, want %q", gotRawQuery, wantRawQuery)
	}
}

func TestAuthTabDoesNotTreatStatusCodeOnlyAsAbnormal(t *testing.T) {
	model := newAuthTabModel(&Client{})
	model.SetSize(100, 20)
	model.files = []map[string]any{
		{
			"name":        "codex-status-only@example.com.json",
			"status_code": float64(http.StatusForbidden),
		},
	}

	updated, cmd := model.handleNormalInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})

	if updated.confirm != -1 {
		t.Fatalf("confirm = %d, want -1", updated.confirm)
	}
	if cmd != nil {
		t.Fatal("expected no delete command without account_abnormal flag")
	}
}

func TestAuthFileStatusCodeAcceptsJSONNumber(t *testing.T) {
	got := authFileStatusCode(map[string]any{
		"status_code": json.Number("403"),
	})

	if got != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d", got, http.StatusForbidden)
	}
}
