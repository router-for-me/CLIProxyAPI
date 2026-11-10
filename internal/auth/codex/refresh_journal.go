package codex

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// RefreshJournal records consumption before sending a refresh grant. Claims never
// expire: an absent response is not evidence that the provider did not rotate it.
// Implementations must atomically create claims in the authoritative store, not
// a disposable local mirror. Receipts contain credentials and require the same
// protection as the credential store. Complete is called only by the claimant.
const PendingRefreshReceipt = `{"stage":"pending"}`

type RefreshJournal interface {
	RefreshClaim(context.Context, string) (bool, error)
	RefreshRead(context.Context, string) ([]byte, error)
	RefreshComplete(context.Context, string, []byte) error
}

// RefreshOutcomeUnknownError preserves the stage without exposing transport URLs,
// response bodies, or credentials in diagnostic messages.
type RefreshOutcomeUnknownError struct {
	Stage string
	Cause error
}

func (e *RefreshOutcomeUnknownError) Error() string {
	return "refresh_outcome_unknown: " + e.Stage + "; reconcile the refresh result or sign in again"
}

func (e *RefreshOutcomeUnknownError) Unwrap() error { return e.Cause }

// IsRefreshOutcomeUnknown also recognizes wrapped partition errors.
func IsRefreshOutcomeUnknown(err error) bool {
	var outcome *RefreshOutcomeUnknownError
	return errors.As(err, &outcome)
}

type refreshJournalContextKey struct{}

// WithRefreshJournal binds refresh attempts to their authoritative credential store.
func WithRefreshJournal(ctx context.Context, journal RefreshJournal) context.Context {
	return context.WithValue(ctx, refreshJournalContextKey{}, journal)
}

// SetRefreshJournal configures the journal for callers outside the auth manager.
func (o *CodexAuth) SetRefreshJournal(journal RefreshJournal) { o.refreshJournal = journal }

func refreshGrantKey(clientID, token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(clientID+"\x00"+token)))
}

func (o *CodexAuth) journal(ctx context.Context) RefreshJournal {
	if journal, ok := ctx.Value(refreshJournalContextKey{}).(RefreshJournal); ok && journal != nil {
		return journal
	}
	if o.refreshJournal != nil {
		return o.refreshJournal
	}
	return &processRefreshJournal
}

// journalRefresh returns a saved successful receipt to late callers. An incomplete
// claim is deliberately not reclaimed, even after a crash or a storage timeout.
func journalRefresh[T any](ctx context.Context, journal RefreshJournal, clientID, grant string, run func() (T, error), decode func(ojson.Result) (T, error)) (T, error) {
	var zero T
	key := refreshGrantKey(clientID, grant)
	claimed, errClaim := journal.RefreshClaim(ctx, key)
	if errClaim != nil {
		return zero, &RefreshOutcomeUnknownError{Stage: "claim", Cause: errClaim}
	}
	if !claimed {
		var value T
		var expires string
		seen := make(map[string]bool)
		for len(seen) < 1024 {
			if seen[key] {
				return zero, &RefreshOutcomeUnknownError{Stage: "receipt-cycle"}
			}
			seen[key] = true
			receipt, errRead := journal.RefreshRead(ctx, key)
			if errRead != nil {
				return zero, &RefreshOutcomeUnknownError{Stage: "receipt-read", Cause: errRead}
			}
			if len(receipt) == 0 && len(seen) > 1 {
				return usableRefreshReceipt(value, expires)
			}
			if !ojson.ValidBytes(receipt) {
				return zero, &RefreshOutcomeUnknownError{Stage: "pending"}
			}
			doc := ojson.ParseBytes(receipt)
			if stage := doc.Get("stage").String(); stage != "" {
				return zero, &RefreshOutcomeUnknownError{Stage: stage}
			}
			if code := doc.Get("rejected").String(); code != "" {
				return zero, fmt.Errorf("token refresh rejected: %s", code)
			}
			var errDecode error
			value, errDecode = decode(doc.Get("value"))
			if errDecode != nil {
				return zero, &RefreshOutcomeUnknownError{Stage: "receipt-decode", Cause: errDecode}
			}
			expires = doc.Get("value.expired").String()
			nextGrant := doc.Get("value.refresh_token").String()
			if nextGrant == "" || refreshGrantKey(clientID, nextGrant) == key {
				return usableRefreshReceipt(value, expires)
			}
			key = refreshGrantKey(clientID, nextGrant)
		}
		return zero, &RefreshOutcomeUnknownError{Stage: "receipt-chain-limit"}
	}

	value, errRun := run()
	doc := ojson.NewDoc(nil)
	var errSet error
	if errRun == nil {
		errSet = doc.Set("value", value)
	} else {
		var outcome *RefreshOutcomeUnknownError
		if errors.As(errRun, &outcome) {
			errSet = doc.Set("stage", outcome.Stage)
		} else {
			errSet = doc.Set("rejected", refreshRejectionCode(errRun))
		}
	}
	if errSet != nil {
		return zero, &RefreshOutcomeUnknownError{Stage: "receipt-encode", Cause: errSet}
	}
	// Use an independent acquisition context for the receipt, just as for the
	// exchange itself. Caller cancellation must not discard a successful rotation.
	if errComplete := journal.RefreshComplete(ctx, key, doc.Bytes()); errComplete != nil {
		return zero, errors.Join(errRun, &RefreshOutcomeUnknownError{Stage: "receipt-write", Cause: errComplete})
	}
	return value, errRun
}

func usableRefreshReceipt[T any](value T, expires string) (T, error) {
	expiry, errParse := time.Parse(time.RFC3339, expires)
	if errParse != nil || !expiry.After(time.Now()) {
		var zero T
		return zero, &RefreshOutcomeUnknownError{Stage: "receipt-expired", Cause: errParse}
	}
	return value, nil
}

func decodeRefreshReceipt(value ojson.Result) (*CodexTokenData, error) {
	if !value.IsObject() || value.Get("access_token").String() == "" || value.Get("expired").String() == "" {
		return nil, fmt.Errorf("invalid refresh receipt")
	}
	return &CodexTokenData{
		AccessToken:  value.Get("access_token").String(),
		RefreshToken: value.Get("refresh_token").String(),
		IDToken:      value.Get("id_token").String(),
		AccountID:    value.Get("account_id").String(),
		Email:        value.Get("email").String(),
		Expire:       value.Get("expired").String(),
		PlanType:     value.Get("plan_type").String(),
	}, nil
}

func decodeSIWCRefreshReceipt(value ojson.Result) (*CodexSIWCTokenData, error) {
	if !value.IsObject() || value.Get("access_token").String() == "" || value.Get("refresh_token").String() == "" || value.Get("client_id").String() == "" {
		return nil, fmt.Errorf("invalid SIWC refresh receipt")
	}
	scope := value.Get("scope").String()
	return &CodexSIWCTokenData{
		AccessToken:       value.Get("access_token").String(),
		RefreshToken:      value.Get("refresh_token").String(),
		IDToken:           value.Get("id_token").String(),
		ClientID:          value.Get("client_id").String(),
		Scope:             scope,
		Scopes:            strings.Fields(scope),
		Expire:            value.Get("expired").String(),
		ExpiresAt:         value.Get("expires_at").String(),
		EarliestRefreshAt: value.Get("earliest_refresh_at").Value(),
		LastRefresh:       value.Get("last_refresh").String(),
		DeviceID:          value.Get("device_id").String(),
	}, nil
}

func refreshRejectionCode(err error) string {
	raw := strings.ToLower(err.Error())
	for _, code := range []string{"refresh_token_reused", "refresh_token_expired", "refresh_token_invalidated", "token_revoked", "invalid_grant", "invalid_client"} {
		if strings.Contains(raw, code) {
			return code
		}
	}
	return "401 unauthorized"
}

// Direct SDK callers without a configured store still cannot replay an unresolved
// grant in this process. Server and utility callers attach a durable journal.
type memoryRefreshJournal struct {
	mu       sync.Mutex
	receipts map[string][]byte
}

var processRefreshJournal memoryRefreshJournal

func (j *memoryRefreshJournal) RefreshClaim(_ context.Context, key string) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.receipts == nil {
		j.receipts = make(map[string][]byte)
	}
	if _, exists := j.receipts[key]; exists {
		return false, nil
	}
	j.receipts[key] = nil
	return true, nil
}

func (j *memoryRefreshJournal) RefreshRead(_ context.Context, key string) ([]byte, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	receipt, exists := j.receipts[key]
	if exists && receipt == nil {
		return []byte(PendingRefreshReceipt), nil
	}
	return append([]byte(nil), receipt...), nil
}

func (j *memoryRefreshJournal) RefreshComplete(_ context.Context, key string, receipt []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.receipts[key] = append([]byte(nil), receipt...)
	return nil
}

// doRefreshRequest forbids 307/308 redirects from silently replaying a POST body.
func (o *CodexAuth) doRefreshRequest(req *http.Request) (*http.Response, error) {
	client := *o.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}
