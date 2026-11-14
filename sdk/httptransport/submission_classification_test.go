package httptransport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type parsedProviderFailure struct{ error }

func (parsedProviderFailure) IsProviderErrorPayload() bool { return true }

func TestSubmissionAcceptedErrorsDefaultToUnknown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		provider bool
	}{
		{"local validation", errors.New("invalid response"), false},
		{"local translation", errors.New("invalid tool arguments"), false},
		{"local error-shaped JSON", errors.New(`{"error":{"message":"local translation failed"}}`), false},
		{"cancelled after acceptance", context.Canceled, false},
		{"EOF", io.EOF, false},
		{"parsed provider refusal", parsedProviderFailure{errors.New("provider refused")}, true},
		{"wrapped provider refusal", fmt.Errorf("provider response: %w", parsedProviderFailure{errors.New("provider refused")}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			ctx := WithSubmissionTracking(context.Background())
			response, errDo := generationClient(ctx, server.Client()).Post(server.URL, "application/json", strings.NewReader(`{}`))
			if errDo != nil {
				t.Fatal(errDo)
			}
			if errClose := response.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			classified := ClassifySubmissionError(ctx, tc.err)
			if tc.provider {
				if classified != tc.err {
					t.Fatalf("parsed provider refusal changed: %v", classified)
				}
				return
			}
			if !IsOutcomeUnknown(classified) || !errors.Is(classified, tc.err) {
				t.Fatalf("local error=%v, want unknown outcome retaining cause", classified)
			}
			if ClassifySubmissionError(ctx, parsedProviderFailure{errors.New("later refusal")}) != classified {
				t.Fatal("provider payload cleared an already terminal attempt")
			}
		})
	}
}

func generationClient(ctx context.Context, client *http.Client) *http.Client {
	clone := *Decorate(ctx, client)
	clone.Transport = GenerationTransport(ctx, clone.Transport)
	return &clone
}

func TestSubmissionAuxiliaryHTTPDoesNotChangeGenerationOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	ctx := WithSubmissionTracking(context.Background())
	local := errors.New("generation has not been submitted")
	response, errDo := Decorate(ctx, server.Client()).Post(server.URL, "application/json", strings.NewReader(`{}`))
	if errDo != nil {
		t.Fatal(errDo)
	}
	_ = response.Body.Close()
	if ClassifySubmissionError(ctx, local) != local {
		t.Fatal("auxiliary 200 accepted a generation")
	}
	response, errDo = generationClient(WithoutDecorator(ctx), server.Client()).Post(server.URL, "application/json", strings.NewReader(`{}`))
	if errDo != nil {
		t.Fatal(errDo)
	}
	_ = response.Body.Close()
	if !IsOutcomeUnknown(ClassifySubmissionError(ctx, local)) {
		t.Fatal("generation 200 was not tracked")
	}
	// Even a terminal generation cannot prevent a credential HTTP exchange.
	response, errDo = Decorate(ctx, server.Client()).Post(server.URL, "application/json", strings.NewReader(`{}`))
	if errDo != nil {
		t.Fatal(errDo)
	}
	_ = response.Body.Close()
}
