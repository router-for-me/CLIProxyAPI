package executor

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/fpobserve"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// fingerprint_observe.go — opt-in, sampled, LOG-ONLY observability of the ACTUAL
// outbound request fingerprint per account. Controlled by config.FingerprintObserve
// (off by default). It NEVER mutates the request; it exists so an operator / the daily
// patrol can verify each account's UA / device-profile / TLS profile / key headers stay
// self-consistent and catch drift WITHOUT a packet capture (which is the only other way
// to see the executor-applied fingerprint). Account identifiers (session-id/account-id)
// are logged as shape/presence only, never their raw value; the account itself is a
// stable fnv tag, not the email.

const fpObserveDefaultIntervalSec = 3600

// fpObserveLastSeen throttles logging to at most one line per (kind|account) per interval.
var fpObserveLastSeen sync.Map // string -> int64 (unix seconds)

func fingerprintObserveEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.FingerprintObserve.Enabled
}

func fpObserveShouldLog(cfg *config.Config, key string) bool {
	interval := int64(fpObserveDefaultIntervalSec)
	if cfg != nil && cfg.FingerprintObserve.MinIntervalSeconds > 0 {
		interval = int64(cfg.FingerprintObserve.MinIntervalSeconds)
	}
	now := time.Now().Unix()
	if v, ok := fpObserveLastSeen.Load(key); ok {
		if last, _ := v.(int64); now-last < interval {
			return false
		}
	}
	fpObserveLastSeen.Store(key, now)
	return true
}

// fpAccountTag returns a stable, non-PII account identifier for grouping in the log.
func fpAccountTag(auth *cliproxyauth.Auth) string {
	scope := helps.AccountFingerprintKey(auth, "")
	if strings.TrimSpace(scope) == "" {
		return "none"
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(scope))
	return fmt.Sprintf("acct-%08x", h.Sum32())
}

// fpHeaderRaw reads a header value by case-insensitive RAW key. Some codex headers are
// set via raw map assignment that bypasses textproto canonicalization, so http.Header.Get
// (which canonicalizes the lookup key) would miss them.
func fpHeaderRaw(h http.Header, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// fpSessionShape returns the exact session-id header key form actually present — this
// reveals hyphen vs underscore (`session-id` vs `session_id`), a version-sensitive tell —
// or "none".
func fpSessionShape(h http.Header) string {
	for k := range h {
		if lk := strings.ToLower(k); lk == "session-id" || lk == "session_id" {
			return k
		}
	}
	return "none"
}

func fpPresence(h http.Header, name string) string {
	if fpHeaderRaw(h, name) != "" {
		return "present"
	}
	return "absent"
}

// fpTLSProfile reports the uTLS profile name resolveTLSProfile would pick for host.
func fpTLSProfile(cfg *config.Config, host string) string {
	host = strings.ToLower(host)
	switch {
	case strings.Contains(host, "anthropic"):
		if cfg != nil && cfg.DisableNodeTLSFingerprint {
			return "chrome-h2(node-disabled)"
		}
		return "node-h1"
	case strings.Contains(host, "chatgpt"), strings.Contains(host, "openai"):
		return "chrome-h2"
	default:
		return "stdlib"
	}
}

// observeCodexFingerprint records the sampled outbound codex fingerprint into the
// in-memory store (every request, feeds the observatory page) and emits a throttled
// [FP-OBSERVE] log line. No-op unless enabled.
func observeCodexFingerprint(cfg *config.Config, auth *cliproxyauth.Auth, r *http.Request) {
	if !fingerprintObserveEnabled(cfg) || r == nil || r.URL == nil {
		return
	}
	h := r.Header
	host := r.URL.Hostname()
	rec := fpobserve.Record{
		Account:    fpAccountTag(auth),
		Provider:   "codex",
		Host:       host,
		TLSProfile: fpTLSProfile(cfg, host),
		UserAgent:  fpHeaderRaw(h, "User-Agent"),
		Originator: fpHeaderRaw(h, "Originator"),
		SessionHdr: fpSessionShape(h),
		AccountID:  fpPresence(h, "chatgpt-account-id"),
		AcceptEnc:  fpHeaderRaw(h, "Accept-Encoding"),
	}
	fpobserve.Put(rec, 0)
	if !fpObserveShouldLog(cfg, "codex|"+rec.Account) {
		return
	}
	log.WithFields(log.Fields{
		"kind": "codex", "account": rec.Account, "host": rec.Host,
		"tls_profile": rec.TLSProfile, "user_agent": rec.UserAgent,
		"originator": rec.Originator, "session_hdr": rec.SessionHdr,
		"account_id": rec.AccountID, "accept_enc": rec.AcceptEnc,
	}).Info("[FP-OBSERVE] codex outbound fingerprint")
}

// observeClaudeFingerprint records the sampled outbound claude fingerprint into the
// in-memory store and emits a throttled [FP-OBSERVE] log line. No-op unless enabled.
func observeClaudeFingerprint(cfg *config.Config, auth *cliproxyauth.Auth, r *http.Request) {
	if !fingerprintObserveEnabled(cfg) || r == nil || r.URL == nil {
		return
	}
	h := r.Header
	host := r.URL.Hostname()
	rec := fpobserve.Record{
		Account:      fpAccountTag(auth),
		Provider:     "claude",
		Host:         host,
		TLSProfile:   fpTLSProfile(cfg, host),
		UserAgent:    fpHeaderRaw(h, "User-Agent"),
		XApp:         fpHeaderRaw(h, "X-App"),
		StainlessPkg: fpHeaderRaw(h, "X-Stainless-Package-Version"),
		StainlessOS:  fpHeaderRaw(h, "X-Stainless-OS"),
		StainlessRT:  fpHeaderRaw(h, "X-Stainless-Runtime-Version"),
		Arch:         fpHeaderRaw(h, "X-Stainless-Arch"),
		AcceptEnc:    fpHeaderRaw(h, "Accept-Encoding"),
	}
	fpobserve.Put(rec, 0)
	if !fpObserveShouldLog(cfg, "claude|"+rec.Account) {
		return
	}
	log.WithFields(log.Fields{
		"kind": "claude", "account": rec.Account, "host": rec.Host,
		"tls_profile": rec.TLSProfile, "user_agent": rec.UserAgent,
		"x_app": rec.XApp, "stainless_pkg": rec.StainlessPkg,
		"stainless_os": rec.StainlessOS, "stainless_rt": rec.StainlessRT,
		"accept_enc": rec.AcceptEnc,
	}).Info("[FP-OBSERVE] claude outbound fingerprint")
}
