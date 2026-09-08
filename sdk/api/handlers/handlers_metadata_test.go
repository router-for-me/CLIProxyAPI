package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"golang.org/x/net/context"
)

func TestGetContextWithCancelCapturesClientRequestMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.RemoteAddr = "192.0.2.10:43123"
	ginCtx.Request.Header.Add("X-Forwarded-For", "203.0.113.5")
	ginCtx.Request.Header.Add("X-Forwarded-For", "198.51.100.8")
	ginCtx.Request.Header.Set("User-Agent", "test-client/1.0")

	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()

	metadata := logging.GetClientRequestMetadata(ctx)
	if metadata.ClientIP != "192.0.2.10" {
		t.Fatalf("ClientIP = %q, want direct peer IP", metadata.ClientIP)
	}
	if metadata.XForwardedFor != "203.0.113.5, 198.51.100.8" {
		t.Fatalf("XForwardedFor = %q", metadata.XForwardedFor)
	}
	if metadata.UserAgent != "test-client/1.0" {
		t.Fatalf("UserAgent = %q", metadata.UserAgent)
	}
}

func TestRequestExecutionMetadataIncludesExecutionSessionWithoutIdempotencyKey(t *testing.T) {
	ctx := WithExecutionSessionID(context.Background(), "session-1")

	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("ExecutionSessionMetadataKey = %v, want %q", got, "session-1")
	}
	if _, ok := meta[idempotencyKeyMetadataKey]; ok {
		t.Fatalf("unexpected idempotency key in metadata: %v", meta[idempotencyKeyMetadataKey])
	}
}

func TestRequestExecutionMetadataIncludesHashedCallerScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Set("userApiKey", "downstream-secret")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	meta := requestExecutionMetadata(ctx)
	got, _ := meta[coreexecutor.CallerScopeMetadataKey].(string)
	want := coresession.CallerScope("downstream-secret")
	if got != want {
		t.Fatalf("CallerScopeMetadataKey = %q, want %q", got, want)
	}
	if got == "downstream-secret" {
		t.Fatal("caller scope contains the raw downstream credential")
	}
}

func TestRequestExecutionMetadataTraceCallbackWebsocketDetection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("skips websocket upgrade", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		ginCtx.Request.Header.Set("Connection", "Upgrade")
		ginCtx.Request.Header.Set("Upgrade", "websocket")
		logging.SetGinRequestID(ginCtx, "1234abcd")
		ctx := context.WithValue(context.Background(), "gin", ginCtx)

		meta := requestExecutionMetadata(ctx)

		if _, exists := meta[coreexecutor.SelectedAuthIndexCallbackMetadataKey]; exists {
			t.Fatal("unexpected selected auth index callback for websocket upgrade")
		}
	})

	t.Run("keeps callback for incomplete upgrade headers", func(t *testing.T) {
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		ginCtx.Request.Header.Set("Upgrade", "websocket")
		logging.SetGinRequestID(ginCtx, "1234abcd")
		ctx := context.WithValue(context.Background(), "gin", ginCtx)

		meta := requestExecutionMetadata(ctx)

		if _, exists := meta[coreexecutor.SelectedAuthIndexCallbackMetadataKey]; !exists {
			t.Fatal("missing selected auth index callback for ordinary HTTP request")
		}
	})
}

func TestSetReasoningEffortMetadataUsesSuffixOverBody(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai", "gpt-5.4(high)", []byte(`{"reasoning_effort":"low"}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "high" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "high")
	}
}

func TestSetReasoningEffortMetadataSupportsOpenAIResponses(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai-response", "gpt-5.4", []byte(`{"reasoning":{"effort":"medium"}}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "medium" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "medium")
	}
}

func TestSetServiceTierMetadataExtractsValue(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"service_tier":"priority"}`))

	gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]
	if gotServiceTier != "priority" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "priority")
	}
}

func TestSetServiceTierMetadataDefaultsWhenMissing(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"model":"gpt-5.4"}`))

	gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]
	if gotServiceTier != "auto" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "auto")
	}
}

func TestSetServiceTierMetadataPreservesExplicitDefault(t *testing.T) {
	meta := make(map[string]any)

	setServiceTierMetadata(meta, []byte(`{"service_tier":"default"}`))

	if gotServiceTier := meta[coreexecutor.ServiceTierMetadataKey]; gotServiceTier != "default" {
		t.Fatalf("ServiceTierMetadataKey = %v, want %q", gotServiceTier, "default")
	}
}

func TestSetGenerateMetadataDefaultsWhenMissing(t *testing.T) {
	meta := make(map[string]any)

	setGenerateMetadata(meta, []byte(`{"model":"gpt-5.4"}`))

	if got := meta[coreexecutor.GenerateMetadataKey]; got != true {
		t.Fatalf("GenerateMetadataKey = %v, want true", got)
	}
}

func TestSetGenerateMetadataPreservesTrue(t *testing.T) {
	meta := make(map[string]any)

	setGenerateMetadata(meta, []byte(`{"generate":true}`))

	if got := meta[coreexecutor.GenerateMetadataKey]; got != true {
		t.Fatalf("GenerateMetadataKey = %v, want true", got)
	}
}

func TestSetGenerateMetadataHonorsExplicitFalse(t *testing.T) {
	meta := make(map[string]any)

	setGenerateMetadata(meta, []byte(`{"generate":false}`))

	if got := meta[coreexecutor.GenerateMetadataKey]; got != false {
		t.Fatalf("GenerateMetadataKey = %v, want false", got)
	}
}

func TestExtractSessionIDsFromRequestCanonicalHierarchy(t *testing.T) {
	// 1. Claude Code main session
	reqMain := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	reqMain.Header.Set("X-Claude-Code-Session-Id", "sess-claude-main")
	sid, pid := extractSessionIDsFromRequest(reqMain)
	if sid != "claude:sess-claude-main" || pid != "" {
		t.Fatalf("claude main session = (%q, %q), want (claude:sess-claude-main, empty)", sid, pid)
	}

	// 2. Claude Code subagent with parent agent
	reqSub := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	reqSub.Header.Set("X-Claude-Code-Session-Id", "sess-claude-main")
	reqSub.Header.Set("X-Claude-Code-Agent-Id", "agent-worker-1")
	reqSub.Header.Set("X-Claude-Code-Parent-Agent-Id", "agent-orchestrator")
	sid, pid = extractSessionIDsFromRequest(reqSub)
	if sid != "claude:sess-claude-main:agent:agent-worker-1" || pid != "claude:sess-claude-main:agent:agent-orchestrator" {
		t.Fatalf("claude subagent = (%q, %q), want (claude:sess-claude-main:agent:agent-worker-1, claude:sess-claude-main:agent:agent-orchestrator)", sid, pid)
	}

	// 3. Generic X-Session-ID and X-Parent-Session-ID
	reqGeneric := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	reqGeneric.Header.Set("X-Session-ID", "sess-worker-99")
	reqGeneric.Header.Set("X-Parent-Session-ID", "sess-root-1")
	sid, pid = extractSessionIDsFromRequest(reqGeneric)
	if sid != "header:sess-worker-99" || pid != "header:sess-root-1" {
		t.Fatalf("generic header session = (%q, %q), want (header:sess-worker-99, header:sess-root-1)", sid, pid)
	}

	// 4. Codex Session-Id
	reqCodex := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	reqCodex.Header.Set("Session-Id", "codex-sess-uuid")
	sid, pid = extractSessionIDsFromRequest(reqCodex)
	if sid != "codex:codex-sess-uuid" || pid != "" {
		t.Fatalf("codex session = (%q, %q), want (codex:codex-sess-uuid, empty)", sid, pid)
	}
}

func TestEnrichContextWithSessionHierarchyFromBody(t *testing.T) {
	ctx := context.Background()

	// 1. Body payload contains session_id and parent_session_id
	bodyJSON := []byte(`{"session_id":"body-task-1","parent_session_id":"body-parent-task"}`)
	ctx = enrichContextWithSessionHierarchy(ctx, nil, bodyJSON, nil)
	meta := logging.GetClientRequestMetadata(ctx)
	if meta.SessionID != "session:body-task-1" || meta.ParentSessionID != "session:body-parent-task" {
		t.Fatalf("body session = (%q, %q), want (session:body-task-1, session:body-parent-task)", meta.SessionID, meta.ParentSessionID)
	}

	// 2. Claude Code header combined with metadata.agent_id in body
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"claude-root-001"}}
	subagentBody := []byte(`{"metadata":{"agent_id":"search-specialist"}}`)
	ctx2 := enrichContextWithSessionHierarchy(context.Background(), headers, subagentBody, nil)
	meta2 := logging.GetClientRequestMetadata(ctx2)
	if meta2.SessionID != "claude:claude-root-001:agent:search-specialist" || meta2.ParentSessionID != "claude:claude-root-001" {
		t.Fatalf("claude body subagent = (%q, %q), want (claude:claude-root-001:agent:search-specialist, claude:claude-root-001)", meta2.SessionID, meta2.ParentSessionID)
	}

	// 3. Ghost Parent avoidance: Context had parent, but body resolves to a top-level root session
	ctxWithOldParent := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{
		SessionID:       "header:old-child",
		ParentSessionID: "header:old-parent",
	})
	topLevelBody := []byte(`{"session_id":"top-level-session"}`)
	ctx3 := enrichContextWithSessionHierarchy(ctxWithOldParent, nil, topLevelBody, nil)
	meta3 := logging.GetClientRequestMetadata(ctx3)
	if meta3.SessionID != "session:top-level-session" || meta3.ParentSessionID != "" {
		t.Fatalf("top level body session = (%q, %q), want (session:top-level-session, empty parent)", meta3.SessionID, meta3.ParentSessionID)
	}

	// 4. Self-loop avoidance: SessionID == ParentSessionID
	selfLoopBody := []byte(`{"session_id":"same-id","parent_session_id":"same-id"}`)
	ctx4 := enrichContextWithSessionHierarchy(context.Background(), nil, selfLoopBody, nil)
	meta4 := logging.GetClientRequestMetadata(ctx4)
	if meta4.SessionID != "session:same-id" || meta4.ParentSessionID != "" {
		t.Fatalf("self loop session = (%q, %q), want (session:same-id, empty parent)", meta4.SessionID, meta4.ParentSessionID)
	}

	// 5. Length bound: ID length is bounded to 256 bytes
	longID := strings.Repeat("a", 250)
	longBody := []byte(`{"session_id":"` + longID + `"}`)
	ctx5 := EnrichContextWithSessionHierarchy(context.Background(), nil, longBody, nil)
	meta5 := logging.GetClientRequestMetadata(ctx5)
	if len(meta5.SessionID) > 256 {
		t.Fatalf("SessionID length = %d, want <= 256", len(meta5.SessionID))
	}
	if !strings.HasPrefix(meta5.SessionID, "session:") {
		t.Fatalf("SessionID = %q, want session: prefix", meta5.SessionID)
	}

	// 6. Clearing hierarchy when interceptor removes all session headers and body has no session
	ctxWithHeaderSession := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{
		SessionID:       "header:cleared-session",
		ParentSessionID: "header:cleared-parent",
	})
	ctxCleared := EnrichContextWithSessionHierarchy(ctxWithHeaderSession, nil, nil, nil)
	metaCleared := logging.GetClientRequestMetadata(ctxCleared)
	if metaCleared.SessionID != "" || metaCleared.ParentSessionID != "" {
		t.Fatalf("cleared context = (%q, %q), want (empty, empty)", metaCleared.SessionID, metaCleared.ParentSessionID)
	}
}
