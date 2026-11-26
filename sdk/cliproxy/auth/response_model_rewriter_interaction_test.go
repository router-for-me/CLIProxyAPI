package auth

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestModelRewriter_InteractionModelNonStream(t *testing.T) {
	payload := []byte(`{"event_type":"interaction.completed","interaction":{"id":"int_123","model":"gemini-3-flash","status":"completed"}}`)
	got := rewriteModelInResponse(payload, "my-forced-alias")

	gotModel := ojson.GetBytes(got, "interaction.model").String()
	if gotModel != "my-forced-alias" {
		t.Fatalf("interaction.model = %q, want %q; payload=%s", gotModel, "my-forced-alias", string(got))
	}
	if strings.Contains(string(got), "gemini-3-flash") {
		t.Fatalf("rewritten payload still contains upstream model: %s", string(got))
	}
}

func TestModelRewriter_InteractionModelStream(t *testing.T) {
	events := []string{
		"interaction.created",
		"interaction.start",
		"interaction.complete",
		"interaction.completed",
	}

	for _, eventType := range events {
		t.Run(eventType, func(t *testing.T) {
			rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "my-forced-alias"})
			chunk := []byte("event: " + eventType + "\n" +
				`data: {"event_type":"` + eventType + `","interaction":{"id":"int_123","model":"gemini-3-flash"}}` + "\n\n")

			out := append([]byte(nil), rewriter.RewriteChunk(chunk)...)
			if tail := rewriter.Finish(); len(tail) > 0 {
				out = append(out, tail...)
			}

			got := string(out)
			if !strings.Contains(got, `"model":"my-forced-alias"`) {
				t.Fatalf("rewritten SSE chunk = %q, want alias %q", got, "my-forced-alias")
			}
			if strings.Contains(got, "gemini-3-flash") {
				t.Fatalf("rewritten SSE chunk still contains upstream model: %q", got)
			}
			if !rewriter.loggedPaths["interaction.model"] {
				t.Fatalf("expected interaction.model to be tracked in loggedPaths")
			}
		})
	}
}

func TestModelRewriter_InteractionModelSSEPayloadLines(t *testing.T) {
	payload := []byte("event: interaction.created\n" +
		`data: {"event_type":"interaction.created","interaction":{"id":"int_123","model":"gemini-3-flash"}}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_123","model":"gemini-3-flash","status":"completed"}}` + "\n\n")

	got := string(rewriteSSEPayloadLines(payload, "my-forced-alias"))
	if strings.Count(got, `"model":"my-forced-alias"`) != 2 {
		t.Fatalf("rewritten SSE payload = %q, want 2 rewritten interaction.model entries", got)
	}
	if strings.Contains(got, "gemini-3-flash") {
		t.Fatalf("rewritten SSE payload still contains upstream model: %q", got)
	}
}
