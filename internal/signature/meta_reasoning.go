package signature

import "strings"

// Muse reasoning encrypted_content (SignatureProviderMeta) is an opaque
// envelope bound to the Meta account that issued it: Meta rejects a replay from
// any other account with "reasoning encrypted_content was not issued to this
// caller". The envelope shape says nothing about the issuing account, so the
// proxy records it in the provenance tag below.

// MetaReasoningTagPrefix opens the provenance tag the proxy wraps around Muse
// reasoning handed to a client: meta#<account-key>#<envelope>. The client
// replays the whole string; the Meta executor keeps the envelope only when the
// account key names the credential it selected for the request.
const MetaReasoningTagPrefix = "meta#"

// maxMetaReasoningAccountKeyLen bounds the account key so a hostile or corrupted
// replay cannot make the tag parser retain an arbitrarily long prefix.
const maxMetaReasoningAccountKeyLen = 64

// TagMetaReasoning binds a Muse reasoning envelope to the account that issued
// it. It returns the envelope unchanged when either part is empty or the key is
// not a valid account key, so a caller that cannot identify the account never
// emits a tag that would later be mistaken for provenance.
func TagMetaReasoning(accountKey, envelope string) string {
	if envelope == "" || !validMetaReasoningAccountKey(accountKey) {
		return envelope
	}
	if _, _, tagged := ParseMetaReasoningTag(envelope); tagged {
		return envelope
	}
	return MetaReasoningTagPrefix + accountKey + "#" + envelope
}

// ParseMetaReasoningTag splits meta#<account-key>#<envelope>. ok is false for
// anything that is not a well-formed tag, including every legacy untagged Muse
// envelope, so callers can treat "not a tag" and "foreign tag" differently.
func ParseMetaReasoningTag(raw string) (accountKey, envelope string, ok bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, MetaReasoningTagPrefix) {
		return "", "", false
	}
	accountKey, envelope, ok = strings.Cut(raw[len(MetaReasoningTagPrefix):], "#")
	if !ok || envelope == "" || !validMetaReasoningAccountKey(accountKey) {
		return "", "", false
	}
	return accountKey, envelope, true
}

// IsMetaReasoningTagged reports whether raw is a well-formed Muse provenance tag.
func IsMetaReasoningTagged(raw string) bool {
	_, _, ok := ParseMetaReasoningTag(raw)
	return ok
}

func validMetaReasoningAccountKey(key string) bool {
	if key == "" || len(key) > maxMetaReasoningAccountKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
