package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	xxHash64 "github.com/pierrec/xxHash/xxHash64"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeCCHSeed   uint64 = 0x4D659218E32A3268
	claudeCCHLength        = 5
	claudeCCHZero          = "00000"
)

type claudeCCHNormalizationEdit struct {
	start int
	end   int
}

type claudeCCHJSONMember struct {
	start       int
	end         int
	commaBefore int
	commaAfter  int
	excluded    bool
}

type claudeCCHJSONScanner struct {
	body  []byte
	pos   int
	edits []claudeCCHNormalizationEdit
}

type claudeCCHUpstreamKind uint8

const (
	claudeCCHUpstreamOther claudeCCHUpstreamKind = iota
	claudeCCHUpstreamAnthropic
	claudeCCHUpstreamVertex
)

func finalizeAnthropicMessagesBodyCCH(body []byte, fallbackBilling string) ([]byte, error) {
	bodyWithPlaceholder, err := ensureClaudeBillingHeaderCCHPlaceholder(body, fallbackBilling)
	if err != nil {
		return nil, err
	}
	return signAnthropicMessagesBody(bodyWithPlaceholder)
}

// claudeBodyNeedsBillingFallback reports whether a confirmed native helper request
// still needs CPA's billing-header fallback.
//
// The measured minimal helper carries no system field at all, which is exactly the
// native wire shape, so injecting a billing header there would be the deviation.
// Keying on "system is absent" rather than "no billing header present" means that
// if anything later in the pipeline (a payload rule, for instance) does attach a
// system prompt, the fallback comes back and the request cannot go upstream with a
// system block that native would never send unsigned.
func claudeBodyNeedsBillingFallback(body []byte) bool {
	return gjson.GetBytes(body, "system").Exists()
}

func ensureClaudeBillingHeaderCCHPlaceholder(body []byte, fallbackBilling string) ([]byte, error) {
	billing := gjson.GetBytes(body, "system.0.text")
	if billing.Type != gjson.String || !strings.HasPrefix(billing.String(), "x-anthropic-billing-header:") {
		if fallbackBilling == "" {
			return body, nil
		}
		var errPrepend error
		body, errPrepend = prependClaudeBillingSystemBlock(body, fallbackBilling)
		if errPrepend != nil {
			return nil, errPrepend
		}
		billing = gjson.GetBytes(body, "system.0.text")
	}
	if _, ok := claudeBillingCCHDigitsOffset(body); ok {
		return body, nil
	}

	billingText := billing.String()
	entrypoint := strings.Index(billingText, "cc_entrypoint=")
	if entrypoint < 0 {
		return body, nil
	}
	entrypointEnd := strings.IndexByte(billingText[entrypoint:], ';')
	if entrypointEnd < 0 {
		return body, nil
	}
	insertAt := entrypoint + entrypointEnd + 1
	billingText = billingText[:insertAt] + " cch=00000;" + billingText[insertAt:]
	updated, err := sjson.SetBytes(body, "system.0.text", billingText)
	if err != nil {
		return nil, fmt.Errorf("insert Claude CCH placeholder: %w", err)
	}
	return updated, nil
}

func prependClaudeBillingSystemBlock(body []byte, billingText string) ([]byte, error) {
	billingBlock := []byte(buildTextBlock(billingText, nil))
	system := gjson.GetBytes(body, "system")
	var systemArray []byte
	switch {
	case system.Type == gjson.String:
		originalBlock := []byte(buildTextBlock(system.String(), nil))
		systemArray = make([]byte, 0, len(billingBlock)+len(originalBlock)+3)
		systemArray = append(systemArray, '[')
		systemArray = append(systemArray, billingBlock...)
		systemArray = append(systemArray, ',')
		systemArray = append(systemArray, originalBlock...)
		systemArray = append(systemArray, ']')
	case system.IsArray():
		rawSystem := bytes.TrimSpace([]byte(system.Raw))
		if bytes.Equal(rawSystem, []byte("[]")) {
			systemArray = make([]byte, 0, len(billingBlock)+2)
			systemArray = append(systemArray, '[')
			systemArray = append(systemArray, billingBlock...)
			systemArray = append(systemArray, ']')
		} else {
			systemArray = make([]byte, 0, len(billingBlock)+len(rawSystem)+1)
			systemArray = append(systemArray, '[')
			systemArray = append(systemArray, billingBlock...)
			systemArray = append(systemArray, ',')
			systemArray = append(systemArray, rawSystem[1:]...)
		}
	default:
		systemArray = make([]byte, 0, len(billingBlock)+2)
		systemArray = append(systemArray, '[')
		systemArray = append(systemArray, billingBlock...)
		systemArray = append(systemArray, ']')
	}

	updated, err := sjson.SetRawBytes(body, "system", systemArray)
	if err != nil {
		return nil, fmt.Errorf("prepend Claude CCH billing block: %w", err)
	}
	return updated, nil
}

func isKimiAPIEndpoint(endpoint string) bool {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.kimi.com")
}

func isKimiMessagesUpstream(auth *cliproxyauth.Auth, endpoint string) bool {
	if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "kimi") {
		return true
	}
	return isKimiAPIEndpoint(endpoint)
}

// stripDefaultKimiClaudeCodeAttribution removes the Claude Code billing/CCH
// attribution block from a Kimi Messages body when the caller did not opt into
// the full CLI profile. Kimi treats the block as prompt text, so forwarding it
// unchanged would leak CPA's attribution into the model's context. Other system
// content is preserved.
func stripDefaultKimiClaudeCodeAttribution(auth *cliproxyauth.Auth, endpoint string, cliFingerprint bool, body []byte) []byte {
	if cliFingerprint || !isKimiMessagesUpstream(auth, endpoint) {
		return body
	}
	return util.StripClaudeCodeAttributionSystem(body)
}

// claudeCCHSigningEnabled applies CPA's CCH policy.
//
// Native gate, identical in Claude Code 2.1.220 through 2.1.234:
//
//	s = (provider === "firstParty" && isFirstPartyBaseURL()) || provider === "vertex"
//	      ? " cch=00000;" : ""
//
// where isFirstPartyBaseURL() is true when ANTHROPIC_BASE_URL is unset or its
// host is api.anthropic.com. Every other backend (bedrock, foundry, mantle,
// anthropicAws, anthropicGoogleCloud, gateway, any custom base URL) sends the
// billing header without cch.
//
// CPA maps that onto two authorities:
//
//   - A real Claude OAuth credential always signs, on every upstream. CPA is the
//     hop that restores the first-party shape: a downstream Claude Code pointed at
//     CPA sees a non-first-party base URL and therefore omits cch itself, so the
//     value has to be regenerated here rather than inherited.
//   - An API key or delegated provider signs only when it explicitly opted into
//     the claude-code-cli profile AND the upstream is one the native gate accepts.
//     On any other gateway the billing header still goes out, but without cch, so
//     a per-request hash cannot bust that gateway's prompt cache.
//
// origin is the concrete upstream URL of the request being built. CPA additionally
// requires https and the default port, which native does not check.
func claudeCCHSigningEnabled(apiKey string, kind claudeCCHUpstreamKind, cliFingerprint bool, origin string) bool {
	if isClaudeOAuthToken(apiKey) {
		return true
	}
	if kind == claudeCCHUpstreamVertex {
		return true
	}
	if !cliFingerprint {
		return false
	}
	return kind == claudeCCHUpstreamAnthropic && isAnthropicUpstreamBase(origin)
}

// signAnthropicMessagesBody reproduces Claude Code 2.1.220's final-body CCH.
// It changes only the five CCH digits in the outgoing body.
func signAnthropicMessagesBody(body []byte) ([]byte, error) {
	cchOffset, ok := claudeBillingCCHDigitsOffset(body)
	if !ok {
		return body, nil
	}

	unsignedBody := bytes.Clone(body)
	copy(unsignedBody[cchOffset:cchOffset+claudeCCHLength], claudeCCHZero)
	normalizedBody, err := normalizeClaudeCCHInput(unsignedBody)
	if err != nil {
		return nil, fmt.Errorf("normalize Claude CCH input: %w", err)
	}

	hasher := xxHash64.New(claudeCCHSeed)
	if _, err = hasher.Write(normalizedBody); err != nil {
		return nil, fmt.Errorf("hash Claude CCH input: %w", err)
	}
	cch := fmt.Sprintf("%05x", hasher.Sum64()&0xFFFFF)
	copy(unsignedBody[cchOffset:cchOffset+claudeCCHLength], cch)
	return unsignedBody, nil
}

func claudeBillingCCHDigitsOffset(body []byte) (int, bool) {
	billing := gjson.GetBytes(body, "system.0.text")
	if billing.Type != gjson.String || !strings.HasPrefix(billing.String(), "x-anthropic-billing-header:") {
		return 0, false
	}

	raw := []byte(billing.Raw)
	for searchFrom := 0; searchFrom < len(raw); {
		relative := bytes.Index(raw[searchFrom:], []byte("cch="))
		if relative < 0 {
			return 0, false
		}
		prefix := searchFrom + relative
		digits := prefix + len("cch=")
		end := digits + claudeCCHLength
		if end < len(raw) && raw[end] == ';' && isLowerHex(raw[digits:end]) {
			return billing.Index + digits, true
		}
		searchFrom = prefix + len("cch=")
	}
	return 0, false
}

func isLowerHex(value []byte) bool {
	if len(value) != claudeCCHLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// normalizeClaudeCCHInput builds the hash view without reserializing JSON.
// Model string values are emptied, while dispatch-only members are omitted.
func normalizeClaudeCCHInput(body []byte) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid JSON body")
	}

	scanner := claudeCCHJSONScanner{
		body:  body,
		edits: make([]claudeCCHNormalizationEdit, 0),
	}
	if err := scanner.parseValue(true); err != nil {
		return nil, err
	}
	scanner.skipWhitespace()
	if scanner.pos != len(body) {
		return nil, fmt.Errorf("unexpected JSON data at byte %d", scanner.pos)
	}

	sort.Slice(scanner.edits, func(i, j int) bool {
		return scanner.edits[i].start < scanner.edits[j].start
	})
	normalized := make([]byte, 0, len(body))
	last := 0
	for _, edit := range scanner.edits {
		if edit.start < last || edit.end > len(body) {
			return nil, fmt.Errorf("overlapping CCH normalization edit at byte %d", edit.start)
		}
		normalized = append(normalized, body[last:edit.start]...)
		last = edit.end
	}
	normalized = append(normalized, body[last:]...)
	return normalized, nil
}

func (scanner *claudeCCHJSONScanner) parseValue(collect bool) error {
	scanner.skipWhitespace()
	if scanner.pos >= len(scanner.body) {
		return fmt.Errorf("missing JSON value at byte %d", scanner.pos)
	}

	switch scanner.body[scanner.pos] {
	case '{':
		return scanner.parseObject(collect)
	case '[':
		return scanner.parseArray(collect)
	case '"':
		_, _, err := scanner.parseString()
		return err
	default:
		start := scanner.pos
		for scanner.pos < len(scanner.body) {
			switch scanner.body[scanner.pos] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				if scanner.pos == start {
					return fmt.Errorf("missing JSON value at byte %d", start)
				}
				return nil
			default:
				scanner.pos++
			}
		}
		if scanner.pos == start {
			return fmt.Errorf("missing JSON value at byte %d", start)
		}
		return nil
	}
}

func (scanner *claudeCCHJSONScanner) parseObject(collect bool) error {
	scanner.pos++
	scanner.skipWhitespace()
	if scanner.consume('}') {
		return nil
	}

	members := make([]claudeCCHJSONMember, 0)
	commaBefore := -1
	for {
		scanner.skipWhitespace()
		memberStart := scanner.pos
		keyStart, keyEnd, err := scanner.parseString()
		if err != nil {
			return err
		}
		scanner.skipWhitespace()
		if !scanner.consume(':') {
			return fmt.Errorf("missing object colon at byte %d", scanner.pos)
		}
		scanner.skipWhitespace()

		key := scanner.body[keyStart:keyEnd]
		excluded := collect && isClaudeCCHExcludedKey(key)
		if collect && bytes.Equal(key, []byte(`"model"`)) && scanner.pos < len(scanner.body) && scanner.body[scanner.pos] == '"' {
			valueStart, valueEnd, errString := scanner.parseString()
			if errString != nil {
				return errString
			}
			scanner.addEdit(valueStart+1, valueEnd-1)
		} else if err = scanner.parseValue(collect && !excluded); err != nil {
			return err
		}
		memberEnd := scanner.pos
		scanner.skipWhitespace()

		commaAfter := -1
		if scanner.consume(',') {
			commaAfter = scanner.pos - 1
		}
		members = append(members, claudeCCHJSONMember{
			start:       memberStart,
			end:         memberEnd,
			commaBefore: commaBefore,
			commaAfter:  commaAfter,
			excluded:    excluded,
		})
		if commaAfter >= 0 {
			commaBefore = commaAfter
			continue
		}
		if !scanner.consume('}') {
			return fmt.Errorf("missing object end at byte %d", scanner.pos)
		}
		break
	}

	if collect {
		scanner.addExcludedMemberEdits(members)
	}
	return nil
}

func (scanner *claudeCCHJSONScanner) parseArray(collect bool) error {
	scanner.pos++
	scanner.skipWhitespace()
	if scanner.consume(']') {
		return nil
	}

	for {
		if err := scanner.parseValue(collect); err != nil {
			return err
		}
		scanner.skipWhitespace()
		if scanner.consume(',') {
			continue
		}
		if !scanner.consume(']') {
			return fmt.Errorf("missing array end at byte %d", scanner.pos)
		}
		return nil
	}
}

func (scanner *claudeCCHJSONScanner) parseString() (start, end int, err error) {
	if scanner.pos >= len(scanner.body) || scanner.body[scanner.pos] != '"' {
		return 0, 0, fmt.Errorf("missing JSON string at byte %d", scanner.pos)
	}

	start = scanner.pos
	scanner.pos++
	for scanner.pos < len(scanner.body) {
		switch scanner.body[scanner.pos] {
		case '\\':
			scanner.pos += 2
		case '"':
			scanner.pos++
			return start, scanner.pos, nil
		default:
			scanner.pos++
		}
	}
	return 0, 0, fmt.Errorf("unterminated JSON string at byte %d", start)
}

func (scanner *claudeCCHJSONScanner) addExcludedMemberEdits(members []claudeCCHJSONMember) {
	for start := 0; start < len(members); {
		if !members[start].excluded {
			start++
			continue
		}

		end := start
		for end+1 < len(members) && members[end+1].excluded {
			end++
		}
		switch {
		case end+1 < len(members):
			scanner.addEdit(members[start].start, members[end].commaAfter+1)
		case start > 0 && end > start:
			// Claude Code 2.1.220 leaves the preceding comma in its hash view
			// when an object ends with multiple consecutive dispatch members.
			scanner.addEdit(members[start].start, members[end].end)
		case start > 0:
			scanner.addEdit(members[start].commaBefore, members[end].end)
		default:
			scanner.addEdit(members[start].start, members[end].end)
		}
		start = end + 1
	}
}

func (scanner *claudeCCHJSONScanner) addEdit(start, end int) {
	if start >= end {
		return
	}
	scanner.edits = append(scanner.edits, claudeCCHNormalizationEdit{start: start, end: end})
}

func (scanner *claudeCCHJSONScanner) skipWhitespace() {
	for scanner.pos < len(scanner.body) {
		switch scanner.body[scanner.pos] {
		case ' ', '\t', '\r', '\n':
			scanner.pos++
		default:
			return
		}
	}
}

func (scanner *claudeCCHJSONScanner) consume(character byte) bool {
	if scanner.pos >= len(scanner.body) || scanner.body[scanner.pos] != character {
		return false
	}
	scanner.pos++
	return true
}

func isClaudeCCHExcludedKey(key []byte) bool {
	switch string(key) {
	case `"max_tokens"`, `"fallbacks"`, `"fallback_credit_token"`:
		return true
	default:
		return false
	}
}

func resolveClaudeKeyConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.ClaudeKey {
	if cfg == nil || auth == nil {
		return nil
	}

	apiKey, baseURL := claudeCreds(auth)
	if apiKey == "" {
		return nil
	}

	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if !strings.EqualFold(cfgKey, apiKey) {
			continue
		}
		if baseURL != "" && cfgBase != "" && !strings.EqualFold(cfgBase, baseURL) {
			continue
		}
		return entry
	}

	return nil
}

// resolveClaudeKeyCloakConfig finds the matching ClaudeKey config and returns its CloakConfig.
func resolveClaudeKeyCloakConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.CloakConfig {
	entry := resolveClaudeKeyConfig(cfg, auth)
	if entry == nil {
		return nil
	}
	return entry.Cloak
}

func rebuildMidSystemMessageEnabled(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	if auth != nil && auth.Attributes != nil && strings.EqualFold(strings.TrimSpace(auth.Attributes["rebuild_mid_system_message"]), "true") {
		return true
	}
	entry := resolveClaudeKeyConfig(cfg, auth)
	return entry != nil && entry.RebuildMidSystemMessage
}
