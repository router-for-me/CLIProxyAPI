package clawmaster

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The kinds of banked reset a claim can spend, by what claude.ai's usage page calls them.
const (
	// ResetKindFable is a "Fable reset": it clears Fable's weekly allowance and gives the week back
	// the share Fable used (a grant that clears seven_day_overage_included but not seven_day).
	ResetKindFable = "fable"
	// ResetKindWeekly is a "Weekly reset": it clears the week, Fable's allowance with it (a grant that
	// clears seven_day).
	ResetKindWeekly = "weekly"
)

// claudeResetClaimURL is where Claude Code claims a banked reset (its "cedar-ember" program); claude.ai's
// "Reset for free" button makes the same claim. Anthropic does not document it.
const claudeResetClaimURL = "https://api.anthropic.com/api/organizations/%s/reset_rate_limits"

// claudeResetProgram names the program a claim belongs to, as Claude Code sends it.
const claudeResetProgram = "cedar_ember"

var (
	// The forms Claude Code accepts before it sends a claim; anything else is refused unsent.
	claudeResetGrantID   = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	claudeOrganizationID = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
)

// ClaudeResetGrant is one banked reset as the usage endpoint lists it. The grant id is kept for the
// claim and never printed.
type ClaudeResetGrant struct {
	id              string
	Clears          []string
	ResetsLeft      int
	UsableNow       bool
	Unavailable     string // why it cannot be used now ("blocked_seven_day", "cooldown"), empty when it can
	EndsAt          time.Time
	GivesBackWeekly bool
}

// ClaudeResetClaim is Anthropic's answer to a claim.
type ClaudeResetClaim struct {
	Result         string // reset, already_used, not_limited, cooldown, ineligible or unavailable
	Reason         string
	ResetsLeft     *int
	Cleared        []string
	WeeklyResetsAt string
}

// ClaudeResetUsage is the part of a usage reading a reset changes.
type ClaudeResetUsage struct {
	Week, Fable           *float64 // used fraction, nil when the reading has none
	WeekResetsAt          time.Time
	Exhausted             []string // the limits the account is at
	Eligible              bool
	IneligibleReason      string
	CooldownUntil         string
	FableResets, Weekly   int // banked resets of each kind, as the gauges count them
	FiveHourOnly          int
	grants                []ClaudeResetGrant
	nextGrantID           string
	grantsListedForAClaim bool
}

// ClaudeResetReport is what a reset command found and did. It holds no credential, account or grant id.
type ClaudeResetReport struct {
	Kind    string
	Before  ClaudeResetUsage
	Grant   *ClaudeResetGrant // the grant a claim of Kind spends, nil when none is banked
	Claimed bool              // Anthropic answered a claim
	// ClaimUnknown is a claim that was sent but whose answer could not be read: the grant may be spent.
	ClaimUnknown bool
	Claim        ClaudeResetClaim
	After        *ClaudeResetUsage // read again after a claim Anthropic answered
}

// ParseClaudeResetUsage reads a usage response fetched with cedar_ember=1 for a reset: the week's and
// Fable's use and every grant with resets left that is neither paused nor ended.
func ParseClaudeResetUsage(payload []byte, now time.Time) (ClaudeResetUsage, error) {
	var usage ClaudeResetUsage
	if week, known, err := ParseClaudeWeeklyQuota(payload); err != nil {
		return usage, err
	} else if known {
		used := week.UsedFraction
		usage.Week, usage.WeekResetsAt = &used, week.ResetsAt
	}
	if fable, known, err := ParseClaudeFableQuota(payload); err == nil && known {
		used := fable.UsedFraction
		usage.Fable = &used
	}
	if counts, known, err := ParseClaudeResetGrants(payload, now); err == nil && known {
		usage.Weekly, usage.FableResets, usage.FiveHourOnly = counts.Weekly, counts.Fable, counts.FiveHour
	}
	var root struct {
		CedarEmber *struct {
			Eligible         bool              `json:"eligible"`
			IneligibleReason *string           `json:"ineligible_reason"`
			Exhausted        []string          `json:"exhausted"`
			CooldownUntil    *string           `json:"cooldown_until"`
			NextGrantID      *string           `json:"next_grant_id"`
			Grants           []json.RawMessage `json:"grants"`
		} `json:"cedar_ember"`
	}
	if json.Unmarshal(payload, &root) != nil || root.CedarEmber == nil {
		return usage, nil
	}
	block := root.CedarEmber
	usage.grantsListedForAClaim = true
	usage.Eligible, usage.Exhausted = block.Eligible, block.Exhausted
	if block.IneligibleReason != nil {
		usage.IneligibleReason = *block.IneligibleReason
	}
	if block.CooldownUntil != nil {
		usage.CooldownUntil = *block.CooldownUntil
	}
	if block.NextGrantID != nil {
		usage.nextGrantID = *block.NextGrantID
	}
	for _, raw := range block.Grants {
		var grant struct {
			ID              string   `json:"id"`
			ResetsLeft      *int     `json:"resets_left"`
			Paused          bool     `json:"paused"`
			EndsAt          *string  `json:"ends_at"`
			Clears          []string `json:"clears"`
			UsableNow       bool     `json:"usable_now"`
			Unavailable     *string  `json:"unavailable_reason"`
			GivesBackWeekly bool     `json:"gives_back_weekly"`
		}
		if json.Unmarshal(raw, &grant) != nil || grant.ResetsLeft == nil || *grant.ResetsLeft <= 0 || grant.Paused || !claudeResetGrantID.MatchString(grant.ID) {
			continue
		}
		g := ClaudeResetGrant{id: grant.ID, Clears: grant.Clears, ResetsLeft: *grant.ResetsLeft, UsableNow: grant.UsableNow, GivesBackWeekly: grant.GivesBackWeekly}
		if grant.EndsAt != nil {
			if ends, err := time.Parse(time.RFC3339, *grant.EndsAt); err == nil {
				if !ends.After(now) {
					continue
				}
				g.EndsAt = ends
			}
		}
		if grant.Unavailable != nil {
			g.Unavailable = *grant.Unavailable
		}
		usage.grants = append(usage.grants, g)
	}
	return usage, nil
}

// grantFor is the grant a claim of kind spends: of the grants of that kind, one usable now first, then
// the one Anthropic names next, then the one that ends soonest.
func (u ClaudeResetUsage) grantFor(kind string) *ClaudeResetGrant {
	var best *ClaudeResetGrant
	rank := func(g ClaudeResetGrant) (int, int, time.Time) {
		usable, next := 1, 1
		if g.UsableNow {
			usable = 0
		}
		if g.id == u.nextGrantID {
			next = 0
		}
		ends := g.EndsAt
		if ends.IsZero() {
			ends = time.Unix(1<<40, 0)
		}
		return usable, next, ends
	}
	for i := range u.grants {
		g := u.grants[i]
		if !claudeResetGrantIsKind(g, kind) {
			continue
		}
		if best == nil {
			best = &u.grants[i]
			continue
		}
		au, an, ae := rank(g)
		bu, bn, be := rank(*best)
		if au < bu || au == bu && (an < bn || an == bn && ae.Before(be)) {
			best = &u.grants[i]
		}
	}
	if best == nil {
		return nil
	}
	copied := *best
	return &copied
}

func claudeResetGrantIsKind(g ClaudeResetGrant, kind string) bool {
	week := slices.Contains(g.Clears, "seven_day")
	switch kind {
	case ResetKindWeekly:
		return week
	case ResetKindFable:
		return !week && slices.Contains(g.Clears, "seven_day_overage_included")
	}
	return false
}

// claudeResetFlow reads one subscription's usage and, when claim is set and the grant of kind is usable,
// spends it, then reads the usage again. do sends a request with the subscription's credential.
func claudeResetFlow(ctx context.Context, auth *coreauth.Auth, do ClaudeQuotaRequestFunc, kind string, claim bool, now func() time.Time) (ClaudeResetReport, error) {
	kind, ok := ClaudeResetKind(kind)
	report := ClaudeResetReport{Kind: kind}
	if !ok {
		return report, errors.New("the reset kind must be fable or weekly")
	}
	payload, err := fetchClaudeUsage(ctx, auth, do)
	if err != nil {
		return report, err
	}
	if report.Before, err = ParseClaudeResetUsage(payload, now()); err != nil {
		return report, fmt.Errorf("parse Claude usage: %w", err)
	}
	if !report.Before.grantsListedForAClaim {
		return report, errors.New("the usage answer lists no limit resets for this account")
	}
	report.Grant = report.Before.grantFor(kind)
	if !claim || report.Grant == nil || !report.Before.Eligible || !report.Grant.UsableNow {
		return report, nil
	}
	org, err := claudeOrganization(ctx, auth, do)
	if err != nil {
		return report, err
	}
	var sent bool
	report.Claim, sent, err = claimClaudeReset(ctx, auth, do, org, report.Grant.id)
	if err != nil && !sent {
		return report, err
	}
	report.Claimed, report.ClaimUnknown = err == nil, err != nil
	if after, errAfter := fetchClaudeUsage(ctx, auth, do); errAfter == nil {
		if usage, errParse := ParseClaudeResetUsage(after, now()); errParse == nil {
			report.After = &usage
		}
	}
	if err != nil {
		return report, fmt.Errorf("the claim was sent but its answer could not be read (%w): the reset may be spent; run reset without --claim to see", err)
	}
	return report, nil
}

// ClaudeResetKind is kind as a reset kind (fable or weekly, any case and surrounding space), and whether it
// is one.
func ClaudeResetKind(kind string) (string, bool) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	return kind, kind == ResetKindFable || kind == ResetKindWeekly
}

// claudeOrganization is the subscription's organization: from its saved login, or else from Anthropic's
// OAuth profile, as a login reads it.
func claudeOrganization(ctx context.Context, auth *coreauth.Auth, do ClaudeQuotaRequestFunc) (string, error) {
	if auth != nil {
		if org := credentialString(auth.Metadata, "organization_uuid"); claudeOrganizationID.MatchString(org) {
			return org, nil
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/api/oauth/profile", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", claudeCodeUserAgent())
	body, status, err := sendClaudeControl(ctx, auth, do, request)
	if err != nil {
		return "", fmt.Errorf("read the account's organization: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("read the account's organization: HTTP status %d", status)
	}
	var profile struct {
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if json.Unmarshal(body, &profile) != nil || !claudeOrganizationID.MatchString(profile.Organization.UUID) {
		return "", errors.New("read the account's organization: the profile names none")
	}
	return profile.Organization.UUID, nil
}

// claimClaudeReset spends one grant, as Claude Code claims it. sent reports whether the claim may have
// reached Anthropic, so a failure after it is not mistaken for a claim never made.
func claimClaudeReset(ctx context.Context, auth *coreauth.Auth, do ClaudeQuotaRequestFunc, org, grantID string) (claim ClaudeResetClaim, sent bool, err error) {
	if !claudeOrganizationID.MatchString(org) || !claudeResetGrantID.MatchString(grantID) {
		return ClaudeResetClaim{}, false, errors.New("refusing to claim with a malformed organization or grant id")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ClaudeResetClaim{}, false, errors.New("cannot make a claim's request id")
	}
	body, err := json.Marshal(map[string]string{"program": claudeResetProgram, "grant_id": grantID, "request_id": hex.EncodeToString(nonce[:])})
	if err != nil {
		return ClaudeResetClaim{}, false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf(claudeResetClaimURL, org), bytes.NewReader(body))
	if err != nil {
		return ClaudeResetClaim{}, false, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", claudeCodeUserAgent())
	payload, status, err := sendClaudeControl(ctx, auth, do, request)
	if err != nil {
		return ClaudeResetClaim{}, true, fmt.Errorf("claim the reset: %w", err)
	}
	switch {
	case status == http.StatusTooManyRequests:
		// Refused before it was looked at: nothing was spent.
		return ClaudeResetClaim{Result: "rate_limited"}, true, nil
	case status < http.StatusOK || status >= http.StatusMultipleChoices:
		// Never echo the body: it can name the account.
		return ClaudeResetClaim{}, true, fmt.Errorf("claim the reset: HTTP status %d", status)
	}
	var answer struct {
		Result         string   `json:"result"`
		Reason         *string  `json:"reason"`
		ResetsLeft     *int     `json:"resets_left"`
		Cleared        []string `json:"cleared"`
		WeeklyResetsAt *string  `json:"weekly_resets_at"`
	}
	if json.Unmarshal(payload, &answer) != nil || answer.Result == "" {
		return ClaudeResetClaim{}, true, errors.New("claim the reset: the answer could not be read")
	}
	claim = ClaudeResetClaim{Result: answer.Result, ResetsLeft: answer.ResetsLeft, Cleared: answer.Cleared}
	if answer.Reason != nil {
		claim.Reason = *answer.Reason
	}
	if answer.WeeklyResetsAt != nil {
		claim.WeeklyResetsAt = *answer.WeeklyResetsAt
	}
	return claim, true, nil
}

// sendClaudeControl sends one request to Anthropic's account API with the subscription's credential and
// returns its bounded body and status.
func sendClaudeControl(ctx context.Context, auth *coreauth.Auth, do ClaudeQuotaRequestFunc, request *http.Request) ([]byte, int, error) {
	response, err := do(ctx, auth, request)
	if err != nil {
		// A transport error's text holds the request URL, which names the organization: keep only its cause.
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, 0, fmt.Errorf("%s request failed: %w", urlErr.Op, urlErr.Err)
		}
		return nil, 0, errors.New("request failed")
	}
	if response == nil || response.Body == nil {
		return nil, 0, errors.New("empty response")
	}
	payload, errRead := io.ReadAll(io.LimitReader(response.Body, claudeQuotaMaxBodyBytes+1))
	_ = response.Body.Close()
	if errRead != nil {
		return nil, 0, errRead
	}
	if len(payload) > claudeQuotaMaxBodyBytes {
		return nil, 0, errors.New("response exceeds size limit")
	}
	return payload, response.StatusCode, nil
}

// ResetClaudeLimit reads a Claude subscription's banked limit resets and, when claim is set, spends one of
// kind if Anthropic says it is usable now. The caller must hold the profile lock until it returns.
func ResetClaudeLimit(ctx context.Context, profile Profile, kind string, claim bool) (ClaudeResetReport, error) {
	return resetClaudeLimit(ctx, profile, kind, claim, nil)
}

// resetClaudeLimit is ResetClaudeLimit sending through do, or the login's own executor when do is nil.
func resetClaudeLimit(ctx context.Context, profile Profile, kind string, claim bool, do ClaudeQuotaRequestFunc) (ClaudeResetReport, error) {
	if ctx == nil {
		return ClaudeResetReport{}, errors.New("reset requires a context")
	}
	if _, ok := ClaudeResetKind(kind); !ok {
		return ClaudeResetReport{}, errors.New("the reset kind must be fable or weekly")
	}
	if profile.Provider != "claude" {
		return ClaudeResetReport{}, errors.New("limit resets exist only for Claude subscriptions")
	}
	backend, err := NewBackend(ctx, BackendOptions{AuthDir: profile.AuthDir, Provider: profile.Provider, AuthID: profile.AuthID, UseRequestModel: true})
	if err != nil {
		return ClaudeResetReport{}, errors.New("cannot load the profile's login")
	}
	defer func() { _ = backend.Close() }()
	if len(backend.authIDs) == 0 {
		return ClaudeResetReport{}, errors.New("the profile has no login")
	}
	authID := backend.authIDs[0]
	auth, ok := backend.manager.GetByID(authID)
	if !ok || auth == nil || auth.AuthKind() != coreauth.AuthKindOAuth {
		return ClaudeResetReport{}, errors.New("the profile has no subscription login")
	}
	if !auth.HasValidAccessToken(time.Now()) {
		// Only credential acquisition has a deadline (repository policy).
		refreshCtx, cancel := context.WithTimeout(ctx, claudeQuotaDefaultTimeout)
		refreshed, errRefresh := backend.manager.ForceRefreshAuth(refreshCtx, authID)
		cancel()
		if errRefresh != nil || refreshed == nil {
			return ClaudeResetReport{}, errors.New("the profile's login could not be refreshed")
		}
		auth = refreshed
	}
	if do == nil {
		do = backend.manager.HttpRequest
	}
	return claudeResetFlow(ctx, auth, do, kind, claim, time.Now)
}
