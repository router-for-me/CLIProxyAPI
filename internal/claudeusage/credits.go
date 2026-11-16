package claudeusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/resetcredits"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RedeemFunc claims one reset from grantID and returns Claude's result.
type RedeemFunc func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, organizationUUID, grantID, requestID string) (string, error)

// grant is one batch of resets; resets_left of them remain.
type grant struct {
	ID                string `json:"id"`
	Label             string `json:"label"`
	ResetsLeft        int    `json:"resets_left"`
	EndsAt            string `json:"ends_at"`
	StartsAt          string `json:"starts_at"`
	Paused            bool   `json:"paused"`
	UsableNow         bool   `json:"usable_now"`
	Clears            []string
	GivesBackWeekly   *bool
	UnavailableReason string
}

// usable reports a grant that can be claimed now.
func (g grant) usable(now time.Time) bool {
	startsAt, errStart := time.Parse(time.RFC3339Nano, g.StartsAt)
	_, validExpiry := g.endsAt()
	return g.UsableNow && !g.Paused && g.UnavailableReason == "" &&
		(g.StartsAt == "" || (errStart == nil && !startsAt.After(now))) && (g.EndsAt == "" || validExpiry)
}

// scope uses restoration semantics; both known scopes report upstream type "full".
func (g grant) scope() string {
	for _, key := range g.Clears {
		if key != "five_hour" && key != "seven_day" && key != "seven_day_overage_included" {
			return "unknown"
		}
	}
	if g.GivesBackWeekly == nil {
		return "unknown"
	}
	if slices.Contains(g.Clears, "seven_day") && slices.Contains(g.Clears, "seven_day_overage_included") && !*g.GivesBackWeekly {
		return "general"
	}
	if !slices.Contains(g.Clears, "seven_day") && slices.Contains(g.Clears, "seven_day_overage_included") && *g.GivesBackWeekly {
		return "fable"
	}
	return "unknown"
}

func parseGrant(raw ojson.Result) grant {
	g := grant{ID: raw.Get("id").String(), Label: raw.Get("label").String(), ResetsLeft: int(raw.Get("resets_left").Int()),
		EndsAt: raw.Get("ends_at").String(), StartsAt: raw.Get("starts_at").String(), Paused: raw.Get("paused").Bool(),
		UsableNow: raw.Get("usable_now").Bool(), UnavailableReason: raw.Get("unavailable_reason").String(), Clears: []string{}}
	for _, clear := range raw.Get("clears").Array() {
		g.Clears = append(g.Clears, clear.String())
	}
	if givesBack := raw.Get("gives_back_weekly"); givesBack.Type == ojson.True || givesBack.Type == ojson.False {
		value := givesBack.Bool()
		g.GivesBackWeekly = &value
	}
	return g
}

// endsAt is the grant's expiry; false when it never expires or the time is unreadable.
func (g grant) endsAt() (time.Time, bool) {
	at, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(g.EndsAt))
	return at.UTC(), errParse == nil
}

// creditState is a parsed reset-credit block.
type creditState struct {
	credits *resetcredits.Credits
	// nextGrant is the grant Claude spends next; empty when none is usable.
	nextGrant string
	// coolingUntil is set while Claude refuses claims.
	coolingUntil time.Time
}

// parseCredits reads the reset credits from a usage document. Credits are nil when the account is
// ineligible or the block is missing, so nothing is shown.
func parseCredits(raw json.RawMessage, now time.Time) creditState {
	if !ojson.ValidBytes(raw) {
		return creditState{}
	}
	block := ojson.GetBytes(raw, "cedar_ember")
	if !block.Get("eligible").Bool() {
		return creditState{}
	}
	var state creditState
	if until, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(block.Get("cooldown_until").String())); errParse == nil && until.After(now) {
		state.coolingUntil = until
	}
	credits := &resetcredits.Credits{Grants: []resetcredits.Grant{}}
	for _, rawGrant := range block.Get("grants").Array() {
		g := parseGrant(rawGrant)
		endsAt, expires := g.endsAt()
		if g.ID == "" || g.ResetsLeft <= 0 || (expires && !endsAt.After(now)) {
			continue
		}
		item := resetcredits.Grant{ID: g.ID, Label: g.Label, Scope: g.scope(), Available: g.ResetsLeft, Clears: g.Clears, GivesBackWeekly: g.GivesBackWeekly}
		if expires {
			item.ExpiresAt = &endsAt
		}
		if g.UnavailableReason != "" {
			item.UnavailableReason = &g.UnavailableReason
		}
		credits.Available += g.ResetsLeft
		if g.usable(now) && state.coolingUntil.IsZero() {
			item.Applicable = g.ResetsLeft
			credits.Applicable += g.ResetsLeft
			if g.ID == block.Get("next_grant_id").String() {
				state.nextGrant = g.ID
			}
		}
		credits.Grants = append(credits.Grants, item)
		if expires {
			for range g.ResetsLeft {
				credits.Expiries = append(credits.Expiries, endsAt)
			}
		}
	}
	slices.SortFunc(credits.Expiries, func(a, b time.Time) int { return a.Compare(b) })
	state.credits = credits
	return state
}

// storeCredits records a lookup's reset credits. It skips the write when nothing changed and the
// stored check is younger than MaxLookupAge, so frequent lookups don't rewrite the auth file.
func (p *Poller) storeCredits(ctx context.Context, auth *coreauth.Auth, credits *resetcredits.Credits, lookedUpAt time.Time) {
	stored, had := resetcredits.Of(auth)
	if credits == nil && !had {
		return
	}
	if credits != nil && had && stored.Same(*credits) && lookedUpAt.Sub(stored.CheckedAt) < MaxLookupAge {
		return
	}
	_, _, errModify := p.manager.Modify(ctx, auth.ID, 0, func(current *coreauth.Auth) error {
		if current.Metadata == nil {
			current.Metadata = map[string]any{}
		}
		resetcredits.Store(current.Metadata, lookedUpAt, credits)
		return nil
	})
	if errModify != nil {
		log.Warnf("claude reset credits for auth_index=%s not stored: %v", auth.EnsureIndex(), errModify)
	}
}

// claimResults maps Claude's claim results to the redeem outcome codes.
var claimResults = map[string]string{
	"reset":        resetcredits.Reset,
	"not_limited":  resetcredits.NothingToReset,
	"already_used": resetcredits.AlreadyRedeemed,
	"ineligible":   resetcredits.NoCredit,
}

// RedeemResetCredit spends one of the account's reset credits. A fresh lookup picks Claude's next
// grant; each claim carries a new request id, so it spends at most one reset.
func (p *Poller) RedeemResetCredit(ctx context.Context, auth *coreauth.Auth, grantID string) (string, error) {
	if !Eligible(auth) {
		return "", fmt.Errorf("credential %s is not an enabled Claude OAuth credential", auth.ID)
	}
	organization, _ := auth.Metadata["organization_uuid"].(string)
	if strings.TrimSpace(organization) == "" {
		return "", errors.New("this Claude login has no organization; sign in to it again")
	}
	lookupCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	raw, errFetch := p.fetch(lookupCtx, p.config(), auth)
	if errFetch != nil {
		return "", errFetch
	}
	state := parseCredits(raw, p.now())
	if !state.coolingUntil.IsZero() {
		return "", fmt.Errorf("Claude resets are cooling down until %s", state.coolingUntil.Format(time.RFC3339))
	}
	selected := state.nextGrant
	if grantID != "" {
		selected = ""
		if state.credits != nil {
			for _, grant := range state.credits.Grants {
				if grant.ID == grantID && grant.Applicable > 0 && grant.Scope != "unknown" {
					selected = grant.ID
				}
			}
		}
	}
	if selected == "" {
		return resetcredits.NoCredit, nil
	}
	result, errRedeem := p.redeem(lookupCtx, p.config(), auth, organization, selected, uuid.NewString())
	var httpErr *claudeauth.UsageHTTPError
	switch {
	case errors.As(errRedeem, &httpErr) && httpErr.StatusCode == http.StatusTooManyRequests:
		return "", errors.New("Claude is rate limiting resets; try again soon")
	case errors.As(errRedeem, &httpErr) && (httpErr.StatusCode == http.StatusUnauthorized || httpErr.StatusCode == http.StatusForbidden):
		return "", errors.New("Claude refused the login; sign in to this account again")
	case errRedeem != nil:
		return "", errRedeem
	case result == "cooldown":
		return "", errors.New("Claude resets are cooling down; try again later")
	case result == "unavailable":
		return "", errors.New("Claude could not confirm the reset; check the limits before trying again")
	}
	code, known := claimResults[result]
	if !known {
		return "", fmt.Errorf("unexpected Claude reset result %q", result)
	}
	return code, nil
}

func redeemResetCredit(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, organizationUUID, grantID, requestID string) (string, error) {
	return claudeauth.NewClaudeAuthWithProxyURL(cfg, auth.ProxyURL).RedeemResetCredit(ctx, accessToken(auth), claudeCodeUserAgent(cfg, auth), organizationUUID, grantID, requestID)
}
