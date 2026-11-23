package executor

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
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
