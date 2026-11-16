package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	kiroeventstream "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/kiro"
)

func TestKiroExecutorBuildHeadersUsesSupportedIDEVersion(t *testing.T) {
	exec := NewKiroExecutor(nil)

	t.Run("oauth streaming headers advertise supported Kiro IDE version", func(t *testing.T) {
		headers := exec.BuildHeaders("https://q.us-east-1.amazonaws.com/generateAssistantResponse", "test-token", "idc")

		ua := headers.Get("User-Agent")
		amzUA := headers.Get("X-Amz-User-Agent")

		expectedTag := "KiroIDE-" + KiroDefaultIDEVersion + "-kiro-anonymous"
		if !strings.Contains(ua, expectedTag) {
			t.Fatalf("expected User-Agent to contain %q, got %q", expectedTag, ua)
		}
		if !strings.Contains(amzUA, expectedTag) {
			t.Fatalf("expected X-Amz-User-Agent to contain %q, got %q", expectedTag, amzUA)
		}
		if strings.Contains(ua, "KiroIDE-0.") || strings.Contains(amzUA, "KiroIDE-0.") {
			t.Fatalf("headers must not contain deprecated 0.x KiroIDE version: ua=%q amzUA=%q", ua, amzUA)
		}
		if got := headers.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("expected Authorization Bearer header, got %q", got)
		}
	})

	t.Run("api_key runtime headers advertise supported Kiro IDE version", func(t *testing.T) {
		headers := exec.BuildHeaders("https://runtime.us-east-1.kiro.dev/", "ksk_test", "api_key")

		ua := headers.Get("User-Agent")
		amzUA := headers.Get("X-Amz-User-Agent")

		expectedTag := "KiroIDE-" + KiroDefaultIDEVersion + "-kiro-anonymous"
		if !strings.Contains(ua, expectedTag) {
			t.Fatalf("expected User-Agent to contain %q, got %q", expectedTag, ua)
		}
		if !strings.Contains(amzUA, expectedTag) {
			t.Fatalf("expected X-Amz-User-Agent to contain %q, got %q", expectedTag, amzUA)
		}
		if got := headers.Get("tokentype"); got != "API_KEY" {
			t.Fatalf("expected tokentype API_KEY, got %q", got)
		}
	})
}

func TestClassifyKiroStreamErrorIsRequestScoped(t *testing.T) {
	emptyErr := classifyKiroStreamError(kiroeventstream.ErrEmptyKiroStream)
	reqScoped, ok := emptyErr.(interface{ IsRequestScoped() bool })
	if !ok || !reqScoped.IsRequestScoped() {
		t.Fatalf("expected ErrEmptyKiroStream to be request-scoped, got %T (%v)", emptyErr, emptyErr)
	}
	statusCoder, ok := emptyErr.(interface{ StatusCode() int })
	if !ok || statusCoder.StatusCode() != http.StatusRequestTimeout {
		t.Fatalf("expected status %d, got %v", http.StatusRequestTimeout, emptyErr)
	}

	contextErr := classifyKiroStreamError(fmt.Errorf("%w: CONTENT_LENGTH_EXCEEDS_THRESHOLD: Input is too long", kiroeventstream.ErrKiroEventStreamUpstream))
	reqScopedCtx, ok := contextErr.(interface{ IsRequestScoped() bool })
	if !ok || !reqScopedCtx.IsRequestScoped() {
		t.Fatalf("expected content length error to be request-scoped, got %T (%v)", contextErr, contextErr)
	}

	quotaErr := classifyKiroStreamError(fmt.Errorf("%w: MONTHLY_REQUEST_COUNT: quota exceeded", kiroeventstream.ErrKiroEventStreamUpstream))
	if reqScopedQuota, ok := quotaErr.(interface{ IsRequestScoped() bool }); ok && reqScopedQuota.IsRequestScoped() {
		t.Fatalf("expected quota error not to be request-scoped")
	}
}

func TestParseKiroStreamWithRetryRecoversEmptyFirstStream(t *testing.T) {
	retried := false
	err := parseKiroStreamWithRetry(context.Background(), bytes.NewReader(nil), func() (io.ReadCloser, error) {
		retried = true
		return io.NopCloser(bytes.NewReader(nil)), nil
	}, nil)
	if !retried {
		t.Fatalf("expected parseKiroStreamWithRetry to retry on initial ErrEmptyKiroStream")
	}
	if err != kiroeventstream.ErrEmptyKiroStream {
		t.Fatalf("expected ErrEmptyKiroStream after empty retry, got %v", err)
	}
}
