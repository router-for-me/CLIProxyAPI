package executor

import (
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// antigravityKnownFalse429Phrases are system-instruction phrases that Google's Cloud Code
// Assist content rule answers with a misleading 429 RESOURCE_EXHAUSTED body
// ("Resource has been exhausted (e.g. check quota).") while the credential's quota is
// untouched.
//
// The failure is expensive to diagnose without this list: the misleading upstream body
// makes the executor treat the call as a rate-limit hit, the credential cools down, and
// the operator sees only the local "All credentials for model ... are cooling down" error
// afterwards — which points at quota, not at request content. Masking the phrase with the
// existing antigravity.sensitive-words option is what clears it.
//
// Keep entries limited to phrases with public reproduction evidence, currently
// router-for-me/CLIProxyAPI#5751 and can1357/oh-my-pi#11699 (the `<system-conventions>`
// + `RFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL.` opening block that
// several coding harnesses hardcode). A phrase that does not actually trip the upstream
// rule would turn this diagnostic into a false warning.
var antigravityKnownFalse429Phrases = []string{
	"RFC 2119",
	"system-conventions",
	"system_conventions",
	"system-directive",
	"system_directive",
}

// antigravityFalse429Warned deduplicates the diagnostic to one line per phrase per
// process. The check runs for every translated request, and a client that permanently
// sends a flagged prompt would otherwise turn one misconfiguration into a per-request log
// flood.
var antigravityFalse429Warned sync.Map

// warnKnownFalse429Phrases reports system-instruction phrases that still reach the
// upstream after payload transforms. It is a diagnostic only: the payload is never
// rewritten here, because masking request content stays an explicit operator decision
// (antigravity.sensitive-words).
//
// Pass the payload that will be sent upstream. Obfuscated text no longer contains the
// literal phrases, so an operator who already masks a phrase is not warned about it.
func (e *AntigravityExecutor) warnKnownFalse429Phrases(payload []byte) {
	if len(payload) == 0 {
		return
	}
	parts := gjson.GetBytes(payload, "request.systemInstruction.parts")
	if !parts.Exists() || !parts.IsArray() {
		return
	}

	// Only the system instruction is inspected, matching the surface the upstream rule and
	// the sensitive-word obfuscator both operate on. Scanning user content as well would
	// fire on any client that merely quotes one of these phrases.
	var systemText strings.Builder
	for _, part := range parts.Array() {
		systemText.WriteString(part.Get("text").String())
		systemText.WriteByte('\n')
	}
	text := systemText.String()
	if text == "" {
		return
	}

	for _, phrase := range antigravityKnownFalse429Phrases {
		if !strings.Contains(text, phrase) {
			continue
		}
		if _, warned := antigravityFalse429Warned.LoadOrStore(phrase, struct{}{}); warned {
			continue
		}
		log.Warnf("antigravity executor: system instruction contains %q, which Google Cloud Code Assist rejects with a misleading 429 RESOURCE_EXHAUSTED (\"Resource has been exhausted (e.g. check quota).\") while quota is intact, after which the credential cools down; mask it with antigravity.sensitive-words when the client prompt cannot change", phrase)
	}
}
