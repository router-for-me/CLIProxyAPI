package claudemaster

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"
)

// The paid API-key backup is used only while it can pay. Every subscription out of quota hands work to it, and an
// organization with no credit answers every request with 400 "Your credit balance is too low": on 2026-10-10 that
// failed 30 requests that would otherwise have waited for a reset. So the backup's health is checked the way the
// subscriptions' quotas are polled:
//
//   - at start and every backendBackupCheckInterval, a token count (free: it is not billed, and an organization with
//     no credit is refused there as well; a model list is not, so it would prove nothing);
//   - and at once when a real request to the backup is refused for credit.
//
// While it is unavailable the pool behaves as if it had no backup: the session gets Anthropic's own weekly-limit 429
// with retry-after at the earliest reset, and waits, instead of an error. The next check that passes brings it back.
const backendBackupCheckInterval = 5 * time.Minute

var backendBackupCheckBaseURL = "https://api.anthropic.com"

// backendBackupCheckModel is only used to count tokens; a model the organization cannot use is still proof the key
// works and is not out of credit (see newBackupCreditCheck).
const backendBackupCheckModel = "claude-haiku-4-5"

type backupCheckFunc func(ctx context.Context) (available, conclusive bool, why string)

func backendCreditExhausted(record *backendUpstreamError) bool {
	if record == nil || record.status != http.StatusBadRequest {
		return false
	}
	_, message, ok := record.anthropicError()
	return ok && strings.Contains(strings.ToLower(message), "credit balance")
}

// noteBackupUpstream is the passive check: a backup request refused for credit marks the backup unavailable now.
func (s *backendSeriesSelector) noteBackupUpstream(authID string, record *backendUpstreamError) {
	if s == nil || authID == "" || authID != s.backupAuthID || !backendCreditExhausted(record) {
		return
	}
	s.setBackupHealth(false, "a request was refused: credit balance too low")
}

func (s *backendSeriesSelector) setBackupHealth(available bool, why string) {
	s.mu.Lock()
	changed := !s.backupChecked || s.backupUnavailable == available
	s.backupChecked, s.backupUnavailable, s.backupWhy = true, !available, why
	s.mu.Unlock()
	if !changed {
		return
	}
	if available {
		lg().Info("API-key backup available", "why", why)
		return
	}
	lg().Warn("API-key backup unavailable: it is not used until a check finds it can pay", "why", why)
}

// backupUsableLocked: unknown counts as usable (the check at start settles it within seconds).
func (s *backendSeriesSelector) backupUsableLocked() bool { return !s.backupUnavailable }

func (s *backendSeriesSelector) backupStateLocked() string {
	switch {
	case s.backupAuthID == "":
		return "none"
	case !s.backupChecked:
		return "unchecked"
	case s.backupUnavailable:
		return "unavailable"
	}
	return "available"
}

// newBackupCreditCheck counts the tokens of a one-word message with the backup key.
func newBackupCreditCheck(apiKey, baseURL string, client *http.Client) backupCheckFunc {
	body := []byte(`{"model":"` + backendBackupCheckModel + `","messages":[{"role":"user","content":"ping"}]}`)
	return func(ctx context.Context) (bool, bool, string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/messages/count_tokens", bytes.NewReader(body))
		if err != nil {
			return false, false, "could not build the check"
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("content-type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return false, false, "the check did not reach Anthropic"
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		record := &backendUpstreamError{status: resp.StatusCode, headers: resp.Header, body: raw}
		switch {
		case resp.StatusCode == http.StatusOK:
			return true, true, "a token count was answered"
		case backendCreditExhausted(record):
			return false, true, "credit balance too low"
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return false, true, "the key was refused (HTTP " + http.StatusText(resp.StatusCode) + ")"
		case resp.StatusCode == http.StatusNotFound:
			// Authorized and past billing; only the check's model is not offered to this organization.
			return true, true, "the key works (the check's model is not offered)"
		}
		return false, false, "inconclusive (HTTP " + http.StatusText(resp.StatusCode) + ")"
	}
}

// startBackupCreditChecks runs the check at start and on every tick; an inconclusive result changes nothing.
func startBackupCreditChecks(ctx context.Context, s *backendSeriesSelector, check backupCheckFunc, ticks <-chan time.Time) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		run := func() {
			if available, conclusive, why := check(ctx); conclusive && ctx.Err() == nil {
				s.setBackupHealth(available, why)
			}
		}
		run()
		if ticks == nil {
			ticks = jitteredTicks(ctx, backendBackupCheckInterval, backendPollJitterFraction)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case _, open := <-ticks:
				if !open {
					return
				}
				run()
			}
		}
	}()
	return done
}
