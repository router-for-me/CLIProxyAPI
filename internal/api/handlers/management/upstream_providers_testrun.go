package management

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// upstreamProviderTestRequest is the JSON body for POST
// /v0/management/upstream-providers/:id/test. A nil (or non-positive)
// EntryID probes the provider level (single-key and OAuth rows); a positive
// EntryID pins one api_key_entries child row.
type upstreamProviderTestRequest struct {
	EntryID *int64 `json:"entry_id"`
	Model   string `json:"model"`
}

// upstreamProviderTestResponse reports one probe run. Question/Expected are
// echoed so the dashboard can show what was asked and compare the model's
// answer; Answer carries the model's completion text.
type upstreamProviderTestResponse struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latency_ms"`
	Question  string `json:"question"`
	Expected  int    `json:"expected_answer"`
	Answer    string `json:"answer,omitempty"`
	Model     string `json:"model"`
	EntryID   *int64 `json:"entry_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// testProbeRoutingKey returns the CANONICAL routing key for an upstream
// provider row — the exact string util.UpstreamProviderKey produces, which
// the synthesizer stamps into each live auth's `provider_key` attribute and
// the conductor's auth filters match against. Do NOT hand-roll channel
// mapping here: openai-compatibility keys carry no row id
// ("openai-compatible-<name>"), interactions routes under
// "gemini-interactions:<rowID>", and OAuth rows use the bare channel —
// hand-rolled keys silently break resolution with a 404.
func testProbeRoutingKey(p store.UpstreamProvider) string {
	return util.UpstreamProviderKey(p.ProviderType, p.Name, p.ID)
}

// resolveTestTargetAuth finds the live auth for a provider row (and
// optionally one of its entries) given the row's CANONICAL routing key
// (testProbeRoutingKey) and the provider_key / entry_provider_key
// attributes the synthesizer stamps onto every rendered auth. Entry-level
// probes match the compound `<routing-key>:key-<entryID>` key exactly;
// provider-level probes prefer an auth without an entry key and fall back
// to any auth under the routing key. Matching is case-insensitive — the
// conductor lower-cases these keys everywhere. Returns nil when the
// credential is not live in the registry (never rendered, disabled, or
// mid-reload).
func (h *Handler) resolveTestTargetAuth(routingKey string, entryID *int64) *coreauth.Auth {
	if h == nil || h.authManager == nil || routingKey == "" {
		return nil
	}
	wantEntry := ""
	if entryID != nil && *entryID > 0 {
		wantEntry = routingKey + ":key-" + strconv.FormatInt(*entryID, 10)
	}
	auths := h.authManager.List()
	if wantEntry != "" {
		for _, auth := range auths {
			if auth == nil || auth.Disabled {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(auth.Attributes[coreauth.AttributeEntryProviderKey]), wantEntry) {
				return auth
			}
		}
		return nil
	}
	// Provider level: prefer a non-entry auth under the routing key (the
	// single-key and OAuth shapes), then fall back to any auth under it
	// (entry-bearing pools, legacy rows without the attribute distinction).
	for _, auth := range auths {
		if auth == nil || auth.Disabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["provider_key"]), routingKey) &&
			auth.Attributes[coreauth.AttributeEntryProviderKey] == "" {
			return auth
		}
	}
	for _, auth := range auths {
		if auth == nil || auth.Disabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["provider_key"]), routingKey) {
			return auth
		}
	}
	return nil
}

// TestUpstreamProvider handles POST /v0/management/upstream-providers/:id/test.
// It runs one small chat-completion probe (a random math question, so the
// request never lands in an upstream prompt cache) pinned to the resolved
// credential via PinnedAuthMetadataKey, executing through the same
// authManager.Execute path as live traffic — translators, executor, proxy,
// and cloak all behave exactly as in production.
func (h *Handler) TestUpstreamProvider(c *gin.Context) {
	id, errID := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if errID != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider id"})
		return
	}
	var body upstreamProviderTestRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing model"})
		return
	}

	src, ok := h.upstreamProvidersStore(c)
	if !ok {
		return // upstreamProvidersStore already wrote the 503
	}
	row, errGet := src.Get(c.Request.Context(), id)
	if errGet != nil || row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}

	channel := testProbeRoutingKey(*row)
	auth := h.resolveTestTargetAuth(channel, body.EntryID)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not live in registry — save and wait for reload, then retry"})
		return
	}

	q := generateMathQuestion()
	payload := []byte(fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":200,"stream":false}`,
		model, q.Question,
	))
	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
		Format:  sdktranslator.Format(""), // empty = the manager infers from SourceFormat/registry
	}
	opts := cliproxyexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: model,
			cliproxyexecutor.PinnedAuthMetadataKey:     auth.ID,
		},
	}

	probeCtx, cancel := context.WithTimeout(c.Request.Context(), modelHealthProbeTimeout)
	defer cancel()
	start := time.Now()
	// channel here is the row's canonical ROUTING key (testProbeRoutingKey),
	// not the bare executor channel — the conductor matches candidate auths
	// against routing keys (provider_key / entry_provider_key attributes) and
	// derives the executor internally via executorKeyFromRoutingKey.
	resp, errExec := h.authManager.Execute(probeCtx, []string{channel}, req, opts)
	elapsed := time.Since(start)

	out := upstreamProviderTestResponse{
		LatencyMs: elapsed.Milliseconds(),
		Question:  q.Question,
		Expected:  q.Expected,
		Model:     model,
		EntryID:   body.EntryID,
	}
	if errExec != nil {
		out.OK = false
		out.Error = errExec.Error()
		c.JSON(http.StatusOK, out)
		return
	}
	out.OK = true
	out.Answer = extractOpenAICompletionText(resp.Payload)
	c.JSON(http.StatusOK, out)
}
