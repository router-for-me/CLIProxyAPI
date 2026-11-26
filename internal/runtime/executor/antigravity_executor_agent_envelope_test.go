package executor

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityAgentEnvelope(t *testing.T) {
	const model = "gemini-4-hydrogen-medium"
	const modelEnum = "MODEL_PLACEHOLDER_M526"
	const catalogHistory = `{"request":{"labels":{"custom":"keep","model_enum":"stale"},"contents":[
		{"role":"user","parts":[{"text":"start"}]},
		{"role":"model","parts":[{"functionCall":{}}]},
		{"role":"model","parts":[{"functionResponse":{}}]},
		{"role":"model","parts":[{"functionCall":{}},{"functionCall":{}},{"functionCall":{}}]},
		{"role":"model","parts":[{"functionResponse":{}},{"functionResponse":{}},{"functionResponse":{}}]},
		{"role":"model","parts":[{"functionCall":{}},{"functionCall":{}},{"functionCall":{}}]},
		{"role":"user","parts":[{"functionResponse":{}},{"functionResponse":{}},{"functionResponse":{}}]}
	]}}`
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Metadata: map[string]any{"project_id": "project"}}
	cliproxyauth.StoreAntigravityModelEnums(t.Context(), auth, map[string]string{model: modelEnum})
	e := &AntigravityExecutor{}
	var previousCascade, previousTrajectory, previousRequestID string
	for _, stream := range []bool{false, true} {
		_, raw, errBuild := e.buildRequestDoc(t.Context(), auth, "token", model, ojson.NewDocString(catalogHistory), stream, "", "https://upstream.example")
		if errBuild != nil {
			t.Fatal(errBuild)
		}
		body := ojson.ParseBytes(raw)
		id := body.Get("requestId").String()
		segments := strings.Split(id, "/")
		if len(segments) != 5 || segments[0] != "agent" || segments[4] != "11" {
			t.Fatalf("unexpected agent requestId: %q", id)
		}
		cascade, trajectory := segments[1], segments[3]
		for _, value := range []string{cascade, trajectory} {
			if u, errParse := uuid.Parse(value); errParse != nil || u.Version() != 4 {
				t.Fatalf("invalid UUIDv4: %q", value)
			}
		}
		if stamp, errParse := strconv.ParseInt(segments[2], 10, 64); errParse != nil || stamp <= 0 || cascade == trajectory {
			t.Fatalf("invalid identifiers in %q", id)
		}
		labels := body.Get("request.labels")
		for key, want := range map[string]string{"model_enum": modelEnum, "cascade_id": cascade, "root_cascade_id": cascade, "trajectory_id": trajectory, "request_id": trajectory + "-3", "last_step_index": "10", "used_claude": "false", "used_claude_conservative": "false", "used_non_gemini_model": "false", "custom": "keep"} {
			if value := labels.Get(key); value.Type != ojson.String || value.Str != want {
				t.Errorf("label %s = %s, want %q", key, value.Raw, want)
			}
		}
		if previousCascade != "" && (cascade != previousCascade || trajectory != previousTrajectory || id == previousRequestID) {
			t.Fatalf("same-session requests lost correlation or uniqueness: %s vs %s", previousRequestID, id)
		}
		previousCascade, previousTrajectory, previousRequestID = cascade, trajectory, id
	}
}

func TestAntigravityAgentEnvelopeIsolatesCredentialReplacements(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		e := &AntigravityExecutor{}
		auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", RegistrationEpoch: 1, Metadata: map[string]any{"project_id": "p"}, Attributes: map[string]string{}}
		if homeMode {
			auth.RegistrationEpoch = 0
			auth.Attributes["antigravity_model_enum_scope"] = strings.Repeat("a", 64)
		}
		build := func() ojson.Result {
			t.Helper()
			_, raw, errBuild := e.buildRequestDoc(t.Context(), auth, "token", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"12345","contents":[]}}`), false, "", "https://upstream.example")
			if errBuild != nil {
				t.Fatal(errBuild)
			}
			return ojson.GetBytes(raw, "request.labels")
		}
		before := build()
		if homeMode {
			auth.Attributes["antigravity_model_enum_scope"] = strings.Repeat("b", 64)
		} else {
			auth.RegistrationEpoch++
		}
		after := build()
		for _, key := range []string{"cascade_id", "trajectory_id"} {
			if after.Get(key).Str == before.Get(key).Str {
				t.Errorf("home=%v: replacement inherited %s", homeMode, key)
			}
		}
	}
}

func TestAntigravityAgentEnvelopeBeforePayloadRules(t *testing.T) {
	e := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Metadata: map[string]any{"project_id": "project"}}
	calls := 0
	ctx := helps.WithPayloadFinalizer(t.Context(), func(raw []byte) []byte {
		calls++
		doc := ojson.NewDoc(raw)
		labels := doc.Get("request.labels")
		if labels.Get("model_enum").Exists() || labels.Get("used_claude").Str != "true" || labels.Get("used_non_gemini_model").Str != "true" || labels.Get("last_step_index").Str != "0" {
			t.Errorf("wrong first-turn labels before finalization: %s", labels.Raw)
		}
		_ = doc.Delete("request.labels")
		_ = doc.Set("requestId", "configured")
		return doc.Bytes()
	})
	_, raw, errBuild := e.buildRequestDoc(ctx, auth, "token", "claude-sonnet-4-6", ojson.NewDocString(`{"request":{"labels":{"model_enum":"wrong"},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`), false, "", "https://upstream.example")
	if errBuild != nil || calls != 1 || ojson.GetBytes(raw, "requestId").Str != "configured" || ojson.GetBytes(raw, "request.labels").Exists() {
		t.Fatalf("payload rules not final: calls=%d payload=%s err=%v", calls, raw, errBuild)
	}
}

func TestAntigravityAgentEnvelopeHomeModeWithoutEnumScope(t *testing.T) {
	t.Run("isolates_distinct_accounts", func(t *testing.T) {
		e := &AntigravityExecutor{}
		authA := &cliproxyauth.Auth{
			ID:                "ag-home-no-scope-distinct",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"refresh_token": "rt-home-A",
				"email":         "home-a@example.com",
				"project_id":    "proj-home-A",
			},
		}
		authB := &cliproxyauth.Auth{
			ID:                "ag-home-no-scope-distinct",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"refresh_token": "rt-home-B",
				"email":         "home-b@example.com",
				"project_id":    "proj-home-B",
			},
		}
		_, rawA, errA := e.buildRequestDoc(t.Context(), authA, "token-a", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-home","contents":[]}}`), false, "", "https://upstream.example")
		if errA != nil {
			t.Fatal(errA)
		}
		_, rawB, errB := e.buildRequestDoc(t.Context(), authB, "token-b", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-home","contents":[]}}`), false, "", "https://upstream.example")
		if errB != nil {
			t.Fatal(errB)
		}
		labelsA := ojson.GetBytes(rawA, "request.labels")
		labelsB := ojson.GetBytes(rawB, "request.labels")
		for _, k := range []string{"cascade_id", "root_cascade_id", "trajectory_id", "request_id"} {
			if labelsA.Get(k).Str == labelsB.Get(k).Str {
				t.Fatalf("home mode without enum scope leaked %s=%q across accounts", k, labelsB.Get(k).Str)
			}
		}
		if ojson.GetBytes(rawA, "requestId").Str == ojson.GetBytes(rawB, "requestId").Str {
			t.Fatalf("home mode without enum scope leaked requestId=%q across accounts", ojson.GetBytes(rawB, "requestId").Str)
		}
	})

	t.Run("isolates_sanitized_home_accounts_without_refresh_token", func(t *testing.T) {
		e := &AntigravityExecutor{}
		authA := &cliproxyauth.Auth{
			ID:                "ag-home-sanitized",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"access_token": "tok-home-A",
				"email":        "sanitized-a@example.com",
				"project_id":   "proj-sanitized-A",
			},
		}
		authB := &cliproxyauth.Auth{
			ID:                "ag-home-sanitized",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"access_token": "tok-home-B",
				"email":        "sanitized-b@example.com",
				"project_id":   "proj-sanitized-B",
			},
		}
		_, rawA, errA := e.buildRequestDoc(t.Context(), authA, "tok-home-A", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-sanitized","contents":[]}}`), false, "", "https://upstream.example")
		if errA != nil {
			t.Fatal(errA)
		}
		_, rawB, errB := e.buildRequestDoc(t.Context(), authB, "tok-home-B", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-sanitized","contents":[]}}`), false, "", "https://upstream.example")
		if errB != nil {
			t.Fatal(errB)
		}
		labelsA := ojson.GetBytes(rawA, "request.labels")
		labelsB := ojson.GetBytes(rawB, "request.labels")
		for _, k := range []string{"cascade_id", "root_cascade_id", "trajectory_id"} {
			if labelsA.Get(k).Str == labelsB.Get(k).Str {
				t.Fatalf("sanitized home mode without enum scope leaked %s=%q across accounts", k, labelsB.Get(k).Str)
			}
		}
	})

	t.Run("isolates_shared_project_accounts_by_access_token_fallback", func(t *testing.T) {
		e := &AntigravityExecutor{}
		authA := &cliproxyauth.Auth{
			ID:                "ag-home-shared-project",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"project_id":   "shared-project",
				"access_token": "shared-proj-token-a",
			},
		}
		authB := &cliproxyauth.Auth{
			ID:                "ag-home-shared-project",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"project_id":   "shared-project",
				"access_token": "shared-proj-token-b",
			},
		}
		_, rawA, errA := e.buildRequestDoc(t.Context(), authA, "shared-proj-token-a", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-shared-proj","contents":[]}}`), false, "", "https://upstream.example")
		if errA != nil {
			t.Fatal(errA)
		}
		_, rawB, errB := e.buildRequestDoc(t.Context(), authB, "shared-proj-token-b", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-shared-proj","contents":[]}}`), false, "", "https://upstream.example")
		if errB != nil {
			t.Fatal(errB)
		}
		labelsA := ojson.GetBytes(rawA, "request.labels")
		labelsB := ojson.GetBytes(rawB, "request.labels")
		for _, k := range []string{"cascade_id", "root_cascade_id", "trajectory_id", "request_id"} {
			if labelsA.Get(k).Str == labelsB.Get(k).Str {
				t.Fatalf("shared-project home accounts without account identity leaked %s=%q", k, labelsB.Get(k).Str)
			}
		}
	})

	t.Run("isolates_ephemeral_token_fallback_when_stable_fields_empty", func(t *testing.T) {
		authA := &cliproxyauth.Auth{
			ID:                "ag-home-ephemeral",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata:          map[string]any{"access_token": "ephemeral-token-a"},
		}
		authB := &cliproxyauth.Auth{
			ID:                "ag-home-ephemeral",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata:          map[string]any{"access_token": "ephemeral-token-b"},
		}
		docA := ojson.NewDocString(`{"request":{"contents":[]}}`)
		docB := ojson.NewDocString(`{"request":{"contents":[]}}`)
		helps.ApplyAntigravityAgentEnvelope(docA, authA, "sess-ephemeral", "gemini-3-flash", "")
		helps.ApplyAntigravityAgentEnvelope(docB, authB, "sess-ephemeral", "gemini-3-flash", "")
		for _, k := range []string{"cascade_id", "root_cascade_id", "trajectory_id"} {
			if docA.Get("request.labels."+k).Str == docB.Get("request.labels."+k).Str {
				t.Fatalf("home mode token fallback leaked %s=%q across accounts", k, docB.Get("request.labels."+k).Str)
			}
		}
	})

	t.Run("preserves_session_on_same_account_access_token_rotation", func(t *testing.T) {
		e := &AntigravityExecutor{}
		authBefore := &cliproxyauth.Auth{
			ID:                "ag-home-rotate",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"access_token":  "access-old",
				"refresh_token": "rt-home-same",
				"email":         "same@example.com",
				"project_id":    "proj-same",
			},
		}
		authAfter := &cliproxyauth.Auth{
			ID:                "ag-home-rotate",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"access_token":  "access-new",
				"refresh_token": "rt-home-same",
				"email":         "same@example.com",
				"project_id":    "proj-same",
			},
		}
		_, rawBefore, errBefore := e.buildRequestDoc(t.Context(), authBefore, "access-old", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-rotate","contents":[]}}`), false, "", "https://upstream.example")
		if errBefore != nil {
			t.Fatal(errBefore)
		}
		_, rawAfter, errAfter := e.buildRequestDoc(t.Context(), authAfter, "access-new", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-rotate","contents":[]}}`), false, "", "https://upstream.example")
		if errAfter != nil {
			t.Fatal(errAfter)
		}
		labelsBefore := ojson.GetBytes(rawBefore, "request.labels")
		labelsAfter := ojson.GetBytes(rawAfter, "request.labels")
		for _, k := range []string{"cascade_id", "root_cascade_id", "trajectory_id"} {
			if labelsBefore.Get(k).Str != labelsAfter.Get(k).Str {
				t.Fatalf("same-account access_token rotation changed %s from %q to %q", k, labelsBefore.Get(k).Str, labelsAfter.Get(k).Str)
			}
		}
	})

	t.Run("preserves_session_on_sanitized_home_access_token_rotation", func(t *testing.T) {
		e := &AntigravityExecutor{}
		authBefore := &cliproxyauth.Auth{
			ID:                "ag-home-sanitized-rotate",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Attributes:        map[string]string{cliproxyauth.AntigravityModelEnumHomeScopeAttribute: "   "},
			Metadata: map[string]any{
				"access_token": "sanitized-access-old",
				"email":        "Sanitized-Same@Example.com",
				"project_id":   "proj-sanitized-same",
			},
		}
		authAfter := &cliproxyauth.Auth{
			ID:                "ag-home-sanitized-rotate",
			Provider:          "antigravity",
			RegistrationEpoch: 0,
			Metadata: map[string]any{
				"access_token": "sanitized-access-new",
				"email":        "sanitized-same@example.com",
				"project_id":   "proj-sanitized-same",
			},
		}
		_, rawBefore, errBefore := e.buildRequestDoc(t.Context(), authBefore, "sanitized-access-old", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-sanitized-rotate","contents":[]}}`), false, "", "https://upstream.example")
		if errBefore != nil {
			t.Fatal(errBefore)
		}
		_, rawAfter, errAfter := e.buildRequestDoc(t.Context(), authAfter, "sanitized-access-new", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-sanitized-rotate","contents":[]}}`), false, "", "https://upstream.example")
		if errAfter != nil {
			t.Fatal(errAfter)
		}
		labelsBefore := ojson.GetBytes(rawBefore, "request.labels")
		labelsAfter := ojson.GetBytes(rawAfter, "request.labels")
		for _, k := range []string{"cascade_id", "root_cascade_id", "trajectory_id"} {
			if labelsBefore.Get(k).Str != labelsAfter.Get(k).Str {
				t.Fatalf("sanitized same-account access_token rotation changed %s from %q to %q", k, labelsBefore.Get(k).Str, labelsAfter.Get(k).Str)
			}
		}
	})

	t.Run("preserves_session_on_local_inplace_manager_update", func(t *testing.T) {
		mgr := cliproxyauth.NewManager(nil, nil, nil)
		mgr.SetConfig(&config.Config{})
		authA := &cliproxyauth.Auth{
			ID:       "ag-local-inplace-keep",
			Provider: "antigravity",
			Metadata: map[string]any{
				"refresh_token": "rt-local-A",
				"email":         "local-a@example.com",
				"project_id":    "proj-local-A",
			},
		}
		regA, errReg := mgr.Register(cliproxyauth.WithSkipPersist(t.Context()), authA)
		if errReg != nil {
			t.Fatalf("Register A: %v", errReg)
		}
		e := &AntigravityExecutor{}
		_, rawA, errA := e.buildRequestDoc(t.Context(), regA, "token-a", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-local-inplace","contents":[]}}`), false, "", "https://upstream.example")
		if errA != nil {
			t.Fatal(errA)
		}
		authB := &cliproxyauth.Auth{
			ID:       "ag-local-inplace-keep",
			Provider: "antigravity",
			Metadata: map[string]any{
				"refresh_token": "rt-local-B",
				"email":         "local-b@example.com",
				"project_id":    "proj-local-B",
			},
		}
		updatedB, errUpdate := mgr.Update(cliproxyauth.WithSkipPersist(t.Context()), authB)
		if errUpdate != nil {
			t.Fatalf("Update B: %v", errUpdate)
		}
		_, rawB, errB := e.buildRequestDoc(t.Context(), updatedB, "token-b", "gemini-3-flash", ojson.NewDocString(`{"request":{"sessionId":"sess-local-inplace","contents":[]}}`), false, "", "https://upstream.example")
		if errB != nil {
			t.Fatal(errB)
		}
		labelsA := ojson.GetBytes(rawA, "request.labels")
		labelsB := ojson.GetBytes(rawB, "request.labels")
		for _, k := range []string{"cascade_id", "root_cascade_id", "trajectory_id"} {
			if labelsA.Get(k).Str != labelsB.Get(k).Str {
				t.Fatalf("local in-place Manager.Update unexpectedly changed %s from %q to %q", k, labelsA.Get(k).Str, labelsB.Get(k).Str)
			}
		}
	})
}
