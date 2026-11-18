package codex

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
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
	RefreshRelease(context.Context, string) error
}

// RefreshPreSendError reports a failure that occurred strictly before any
// refresh request bytes could be sent to the token endpoint.
type RefreshPreSendError struct {
	Stage string
	Cause error
}

func (e *RefreshPreSendError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("token refresh failed before send (%s): %v", e.Stage, e.Cause)
	}
	return "token refresh failed before send: " + e.Stage
}

func (e *RefreshPreSendError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsRefreshPreSendError reports whether err occurred strictly before sending
// the refresh grant, including wrapped partition errors when all failed
// partitions failed before send.
func IsRefreshPreSendError(err error) bool {
	if err == nil || IsRefreshOutcomeUnknown(err) {
		return false
	}
	var partitions interface {
		OuterRefreshError() error
		SIWCRefreshError() error
	}
	if errors.As(err, &partitions) && partitions != nil {
		outerErr := partitions.OuterRefreshError()
		siwcErr := partitions.SIWCRefreshError()
		if outerErr == nil && siwcErr == nil {
			return false
		}
		if outerErr != nil && !IsRefreshPreSendError(outerErr) {
			return false
		}
		if siwcErr != nil && !IsRefreshPreSendError(siwcErr) {
			return false
		}
		return true
	}
	var preSend *RefreshPreSendError
	return errors.As(err, &preSend)
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
			if len(receipt) == 0 {
				if len(seen) > 1 {
					return usableRefreshReceipt(value, expires)
				}
				return zero, &RefreshPreSendError{Stage: "claim-released"}
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
	completeCtx, cancelComplete := context.WithTimeout(context.WithoutCancel(ctx), codexRefreshTimeout)
	defer cancelComplete()
	if errRun != nil && IsRefreshPreSendError(errRun) {
		if errRelease := journal.RefreshRelease(completeCtx, key); errRelease != nil {
			return zero, errors.Join(errRun, &RefreshOutcomeUnknownError{Stage: "claim-release", Cause: errRelease})
		}
		return zero, errRun
	}
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
	if errComplete := journal.RefreshComplete(completeCtx, key, doc.Bytes()); errComplete != nil {
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

func (j *memoryRefreshJournal) RefreshRelease(_ context.Context, key string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	receipt, exists := j.receipts[key]
	if !exists {
		return nil
	}
	if receipt != nil {
		return fmt.Errorf("refresh receipt already exists")
	}
	delete(j.receipts, key)
	return nil
}

type refreshBodyReadTracker struct {
	body     io.ReadCloser
	readSeen *atomic.Bool
}

func (r *refreshBodyReadTracker) Read(p []byte) (int, error) {
	r.readSeen.Store(true)
	return r.body.Read(p)
}

func (r *refreshBodyReadTracker) Close() error {
	if r.body == nil {
		return nil
	}
	return r.body.Close()
}

func isTypedPreConnectRefreshError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr != nil {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr != nil && opErr.Op == "dial"
}

// doRefreshRequest forbids 307/308 redirects from silently replaying a POST body
// and classifies errors that occurred strictly before a connection could carry
// any request bytes.
func (o *CodexAuth) doRefreshRequest(req *http.Request) (*http.Response, error) {
	var sawPreConn, sawConnReady, sawBodyRead atomic.Bool
	if req != nil {
		// Prevent net/http.Transport from internally replaying the POST body across connections.
		req.GetBody = nil
		if req.Body != nil && req.Body != http.NoBody {
			req.Body = &refreshBodyReadTracker{body: req.Body, readSeen: &sawBodyRead}
		}
		trace := &httptrace.ClientTrace{
			GetConn:           func(string) { sawPreConn.Store(true) },
			DNSStart:          func(httptrace.DNSStartInfo) { sawPreConn.Store(true) },
			ConnectStart:      func(string, string) { sawPreConn.Store(true) },
			TLSHandshakeStart: func() { sawPreConn.Store(true) },
			GotConn:           func(httptrace.GotConnInfo) { sawConnReady.Store(true) },
			WroteHeaderField:  func(string, []string) { sawConnReady.Store(true) },
			WroteHeaders:      func() { sawConnReady.Store(true) },
			WroteRequest:      func(httptrace.WroteRequestInfo) { sawConnReady.Store(true) },
		}
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	}
	var client http.Client
	if o != nil && o.httpClient != nil {
		client = *o.httpClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, errDo := client.Do(req)
	if errDo != nil {
		if !sawConnReady.Load() && !sawBodyRead.Load() && (sawPreConn.Load() || isTypedPreConnectRefreshError(errDo)) {
			return nil, &RefreshPreSendError{Stage: "connect", Cause: errDo}
		}
		return nil, &RefreshOutcomeUnknownError{Stage: "transport", Cause: errDo}
	}
	return resp, nil
}
