package executor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// sentinelWebsocketLogValues holds values that must never appear in an emitted
// log record. Each sentinel stands in for a class of data that the Codex and
// xAI websocket lifecycle logs used to interpolate directly.
const (
	sentinelSessionID          = "sess-SENSITIVE-SESSION-ID"
	sentinelAuthID             = "auth-SENSITIVE-AUTH-ID"
	sentinelAccessToken        = "SENSITIVE-ACCESS-TOKEN"
	sentinelResponseID         = "resp-SENSITIVE-RESPONSE-ID"
	sentinelPreviousResponseID = "resp-SENSITIVE-PREVIOUS-RESPONSE-ID"
	sentinelProviderMessage    = "SENSITIVE-PROVIDER-ERROR-DETAIL"
)

// sentinelWebsocketURL embeds a credential in the query string, mirroring the
// signed upstream URLs that these executors dial.
var sentinelWebsocketURL = "wss://chatgpt.com/backend-api/codex/responses?access_token=" + sentinelAccessToken + "&session=" + sentinelSessionID

// forbiddenWebsocketLogValues lists every substring that must be absent from
// captured log output.
var forbiddenWebsocketLogValues = []string{
	sentinelSessionID,
	sentinelAuthID,
	sentinelAccessToken,
	sentinelResponseID,
	sentinelPreviousResponseID,
	sentinelProviderMessage,
	"access_token",
	"backend-api",
	"/responses",
}

// captureWebsocketLogs runs emit with a local logrus hook installed at debug
// level and returns the rendered entries.
func captureWebsocketLogs(t *testing.T, emit func()) []string {
	t.Helper()

	logger := log.StandardLogger()
	previousLevel := logger.GetLevel()
	logger.SetLevel(log.DebugLevel)
	hook := logtest.NewLocal(logger)
	t.Cleanup(func() {
		hook.Reset()
		logger.SetLevel(previousLevel)
	})

	emit()

	entries := hook.AllEntries()
	rendered := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		formatted, errFormat := entry.String()
		if errFormat != nil {
			formatted = ""
		}
		rendered = append(rendered, strings.Join([]string{entry.Message, formatted, fmt.Sprint(entry.Data)}, " "))
	}
	return rendered
}

// assertNoSentinelLeak fails the test if any captured record contains a
// sentinel value.
func assertNoSentinelLeak(t *testing.T, records []string) {
	t.Helper()
	if len(records) == 0 {
		t.Fatal("expected at least one websocket lifecycle log record")
	}
	for _, record := range records {
		for _, forbidden := range forbiddenWebsocketLogValues {
			if strings.Contains(record, forbidden) {
				t.Errorf("websocket lifecycle log leaked %q: %s", forbidden, record)
			}
		}
		// The acceptance criteria require identifiers and complete URLs to be
		// omitted, not merely masked. These field names must therefore be
		// absent from the formatted record as well as their sentinel values.
		for _, forbiddenField := range []string{
			" session=",
			" auth=",
			" endpoint=",
			" response_id=",
			" previous_response_id=",
		} {
			if strings.Contains(record, forbiddenField) {
				t.Errorf("websocket lifecycle log included forbidden field %q: %s", forbiddenField, record)
			}
		}
	}
}

// sentinelStatusError carries an HTTP status alongside provider text, matching
// the terminal failures produced by both executors.
type sentinelStatusError struct{}

func (sentinelStatusError) Error() string   { return "upstream refused: " + sentinelProviderMessage }
func (sentinelStatusError) StatusCode() int { return http.StatusUnauthorized }

func TestCodexWebsocketLifecycleLogsOmitSensitiveValues(t *testing.T) {
	records := captureWebsocketLogs(t, func() {
		logCodexWebsocketStreamStart("gpt-5-codex-" + sentinelSessionID)
		logCodexWebsocketConnected(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL)
		for _, reason := range []string{"read_error", "send_error", "upstream_error", sentinelProviderMessage} {
			logCodexWebsocketDisconnected(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, reason, errors.New(sentinelProviderMessage))
			logCodexWebsocketDisconnected(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, reason, nil)
		}
		logCodexWebsocketDisconnected(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, "terminal_failure", sentinelStatusError{})
		logCodexWebsocketCloseFailure("stream", errors.New(sentinelProviderMessage))
		logCodexWebsocketCloseFailure(sentinelProviderMessage, nil)
	})

	assertNoSentinelLeak(t, records)

	joined := strings.Join(records, "\n")
	for _, expected := range []string{"upstream connected", "upstream disconnected", "executing stream request"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("expected Codex lifecycle log containing %q, got:\n%s", expected, joined)
		}
	}
	// Operational metadata must survive redaction, otherwise the logs lose
	// their diagnostic value.
	for _, expected := range []string{"reason=read_error", "status=error", "status=ok"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("expected Codex lifecycle log field %q, got:\n%s", expected, joined)
		}
	}
}

func TestXAIWebsocketLifecycleLogsOmitSensitiveValues(t *testing.T) {
	requestPayload := []byte(fmt.Sprintf(
		`{"type":"response.create","previous_response_id":%q,"generate":true,"input":[{"role":"user"},{"role":"user"}],"authorization":"Bearer %s"}`,
		sentinelPreviousResponseID, sentinelAccessToken,
	))
	terminalPayload := []byte(fmt.Sprintf(
		`{"type":"response.completed","response":{"id":%q,"previous_response_id":%q}}`,
		sentinelResponseID, sentinelPreviousResponseID,
	))

	records := captureWebsocketLogs(t, func() {
		logXAIWebsocketConnected(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL)
		logXAIWebsocketRequest(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, requestPayload)
		logXAIWebsocketRequest(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, nil)
		logXAIWebsocketWarmupCompleted(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, terminalPayload)
		logXAIWebsocketTerminalResponse(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, "response.completed", terminalPayload)
		logXAIWebsocketTerminalResponse(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, sentinelProviderMessage, terminalPayload)
		logXAIWebsocketDisconnected(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, "read_error", errors.New(sentinelProviderMessage))
		logXAIWebsocketDisconnected(sentinelSessionID, sentinelAuthID, sentinelWebsocketURL, "completed", nil)
		logXAIWebsocketCompactFallback(sentinelSessionID, sentinelAuthID, 3, true)
		logXAIWebsocketCloseFailure("session_close", errors.New(sentinelProviderMessage))
	})

	assertNoSentinelLeak(t, records)

	joined := strings.Join(records, "\n")
	for _, expected := range []string{
		"upstream connected",
		"upstream request sent",
		"upstream warmup completed",
		"upstream terminal response",
		"upstream disconnected",
		"compact fallback",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("expected xAI lifecycle log containing %q, got:\n%s", expected, joined)
		}
	}
	for _, expected := range []string{"event=response.create", "input_items=2", "generate=true"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("expected xAI lifecycle log field %q, got:\n%s", expected, joined)
		}
	}
}

func TestSafeWebsocketLifecycleReasonRejectsUnlistedValues(t *testing.T) {
	if got := safeWebsocketLifecycleReason("read_error"); got != "read_error" {
		t.Fatalf("safeWebsocketLifecycleReason(read_error) = %q, want read_error", got)
	}
	if got := safeWebsocketLifecycleReason(""); got != absentLogPlaceholder {
		t.Fatalf("safeWebsocketLifecycleReason(empty) = %q, want %q", got, absentLogPlaceholder)
	}
	if got := safeWebsocketLifecycleReason(sentinelProviderMessage); got != unknownLogPlaceholder {
		t.Fatalf("safeWebsocketLifecycleReason(sentinel) = %q, want %q", got, unknownLogPlaceholder)
	}
}

func TestSafeWebsocketEventTypeRejectsUnlistedValues(t *testing.T) {
	if got := safeWebsocketEventType("response.completed"); got != "response.completed" {
		t.Fatalf("safeWebsocketEventType(response.completed) = %q, want response.completed", got)
	}
	if got := safeWebsocketEventType(sentinelProviderMessage); got != unknownLogPlaceholder {
		t.Fatalf("safeWebsocketEventType(sentinel) = %q, want %q", got, unknownLogPlaceholder)
	}
}

func TestSafeWebsocketErrorDiagnosticReportsStructureOnly(t *testing.T) {
	testCases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: absentLogPlaceholder},
		{name: "opaque", err: errors.New(sentinelProviderMessage), want: redactedLogPlaceholder},
		{name: "canceled", err: context.Canceled, want: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "timeout"},
		{name: "net closed", err: net.ErrClosed, want: "connection_closed"},
		{name: "status", err: sentinelStatusError{}, want: "status=401"},
		{
			name: "close code",
			err:  &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: sentinelProviderMessage},
			want: "close_code=1006",
		},
		{
			name: "out of range close code",
			err:  &websocket.CloseError{Code: 17, Text: sentinelProviderMessage},
			want: "close_code=" + unknownLogPlaceholder,
		},
		{
			name: "wrapped status",
			err:  fmt.Errorf("wrapped %s: %w", sentinelProviderMessage, sentinelStatusError{}),
			want: "status=401",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := safeWebsocketErrorDiagnostic(testCase.err)
			if got != testCase.want {
				t.Fatalf("safeWebsocketErrorDiagnostic() = %q, want %q", got, testCase.want)
			}
			if strings.Contains(got, sentinelProviderMessage) {
				t.Fatalf("safeWebsocketErrorDiagnostic() leaked provider text: %q", got)
			}
		})
	}
}

func TestSafeWebsocketModelResolvesThroughRegistry(t *testing.T) {
	if got := safeWebsocketModel("codex", ""); got != absentLogPlaceholder {
		t.Fatalf("safeWebsocketModel(codex, empty) = %q, want %q", got, absentLogPlaceholder)
	}
	if got := safeWebsocketModel("codex", sentinelSessionID); got != unknownLogPlaceholder {
		t.Fatalf("safeWebsocketModel(codex, sentinel) = %q, want %q", got, unknownLogPlaceholder)
	}
	if got := safeWebsocketModel("codex", "gpt-5-codex"); strings.Contains(got, sentinelSessionID) {
		t.Fatalf("safeWebsocketModel() leaked sentinel: %q", got)
	}
}

func TestSafeXAIGenerateModeReportsFixedLiterals(t *testing.T) {
	testCases := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "absent", payload: `{}`, want: "default"},
		{name: "true", payload: `{"generate":true}`, want: "true"},
		{name: "false", payload: `{"generate":false}`, want: "false"},
		{name: "string", payload: fmt.Sprintf(`{"generate":%q}`, sentinelAccessToken), want: unknownLogPlaceholder},
		{name: "object", payload: fmt.Sprintf(`{"generate":{"token":%q}}`, sentinelAccessToken), want: unknownLogPlaceholder},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := safeXAIGenerateMode([]byte(testCase.payload)); got != testCase.want {
				t.Fatalf("safeXAIGenerateMode(%s) = %q, want %q", testCase.payload, got, testCase.want)
			}
		})
	}
}
