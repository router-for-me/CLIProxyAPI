package claude

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Two clients share the handler, account and models; only their session IDs and
// transcripts differ. Hold both advisor calls open before allowing either to finish.
func TestParallelAdvisorSessionsDoNotShareHistory(t *testing.T) {
	t.Setenv("CCS_CODEX_ADVISOR_MODEL", ccsAdvisor)
	h, e := ccsHandler(t, "codex", ccsWorker, ccsAdvisor)
	markers := map[string]string{"session-A": "alpha-private-17", "session-B": "bravo-private-29"}
	var arrived atomic.Int32
	together := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	e.execute = func(c context.Context, _ *coreauth.Auth, r coreexecutor.Request, o coreexecutor.Options) (coreexecutor.Response, error) {
		id := o.Headers.Get("X-Claude-Code-Session-Id")
		own, ok := markers[id]
		body := string(r.Payload)
		if !ok || !strings.Contains(body, own) {
			return coreexecutor.Response{}, fmt.Errorf("missing session context for %q", id)
		}
		for other, foreign := range markers {
			if id != other && strings.Contains(body, foreign) {
				return coreexecutor.Response{}, fmt.Errorf("foreign marker at %s", r.Model)
			}
		}
		reason, content := "end_turn", ""
		switch {
		case r.Model == ccsAdvisor:
			if arrived.Add(1) == 2 {
				close(together)
			}
			select {
			case <-together:
			case <-ctx.Done():
				return coreexecutor.Response{}, ctx.Err()
			}
			content = fmt.Sprintf(`{"type":"text","text":"advice:%s"}`, own)
		case strings.Contains(body, "advice:"+own):
			content = fmt.Sprintf(`{"type":"text","text":"answer:%s"}`, own)
		default:
			reason = "tool_use"
			content = `{"type":"tool_use","id":"same-upstream-id-in-both","name":"advisor","input":{"question":"review my history"}}`
		}
		return coreexecutor.Response{Payload: []byte(fmt.Sprintf(`{"type":"message","id":"msg","role":"assistant","model":%q,"content":[%s],"stop_reason":%q}`, r.Model, content, reason))}, nil
	}
	type result struct {
		id string
		w  *httptest.ResponseRecorder
	}
	done := make(chan result, 2)
	for id, marker := range markers {
		go func() {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q},{"role":"assistant","content":"noted"},{"role":"user","content":"continue"}],"tools":[%s]}`, ccsWorker, marker, nativeAdvisor)
			c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", strings.NewReader(body))
			c.Request.Header.Set("X-Claude-Code-Session-Id", id)
			h.ClaudeMessages(c)
			done <- result{id, w}
		}()
	}
	ids := map[string]bool{}
	for range markers {
		r := <-done
		body := r.w.Body.String()
		call := ojson.Get(body, "content.0.id").String()
		if r.w.Code != 200 || call == "" || ids[call] || ojson.Get(body, "content.1.tool_use_id").String() != call || ojson.Get(body, "content.1.content.text").String() != "advice:"+markers[r.id] || ojson.Get(body, "content.2.text").String() != "answer:"+markers[r.id] {
			t.Errorf("incorrect response for %s: %s", r.id, body)
		}
		ids[call] = true
	}
}
