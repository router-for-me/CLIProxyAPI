package main

import (
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coresession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// derivedIdentityPrefix marks an identity computed from conversation content.
const derivedIdentityPrefix = "derived:"

// identitySource names how routing treated one request's identity.
type identitySource string

const (
	// identitySourceDerived marks an identity computed from conversation content.
	identitySourceDerived identitySource = "derived"
	// identitySourceAbsent marks a request that carries no conversation content.
	identitySourceAbsent identitySource = "absent"
)

// conversationIdentity names one conversation. An empty identity marks a request
// carrying no conversation content, which keys no cursor.
type conversationIdentity string

// source reports how routing treated this identity.
func (c conversationIdentity) source() identitySource {
	if c == "" {
		return identitySourceAbsent
	}
	return identitySourceDerived
}

// turnIdentity names one conversation state. A value exists only when the
// request carried history, so an absent identity has no representation.
type turnIdentity struct {
	fingerprint string
}

// identityInput carries the request facts one conversation identity derives from.
type identityInput struct {
	SourceFormat string
	Body         []byte
	Metadata     map[string]any
}

// conversationState observes one request and names the conversation whose cursor
// that request advances. A self-contained request names its conversation by
// content. A continuation request carries only its newest items while the
// provider holds the rest, so it adopts the conversation bound to the response it
// names instead of naming a second conversation from its fragment.
func (r *runtimeState) conversationState(input identityInput, cfg *compiledConfig) (requestObservation, conversationIdentity) {
	observation := inspectRequest(input.Body, r.fingerprintSalt)
	identity := newConversationIdentity(input)
	chained := r.chains.lookupResponse(continuationResponseKey{
		Generation: cfg.Generation,
		ResponseID: observation.PreviousResponseID,
	})
	if chained != "" {
		identity = chained
	}
	return observation, identity
}

// newConversationIdentity resolves the identity that keys one conversation cursor.
// Sequence position belongs to the conversation, so identity folds the leading
// instructions and the first complete user input through the shared protocol-aware
// derivation. Every inbound format resolves one conversation the same way, and
// transport identifiers naming a cache lane, an affinity group, one request, or an
// account never reach a cursor key. The identity is empty when the body carries no
// user input.
func newConversationIdentity(input identityInput) conversationIdentity {
	callerScope, _ := input.Metadata[coreexecutor.CallerScopeMetadataKey].(string)
	derived := coresession.DeriveID(sdktranslator.FromString(input.SourceFormat), input.Body, callerScope)
	if derived == "" {
		return ""
	}
	return conversationIdentity(derivedIdentityPrefix + derived)
}

// newTurnIdentity names the conversation state carried by one request. It returns
// nil when the request carries no history, which suppresses replay for that route.
func newTurnIdentity(observation requestObservation) *turnIdentity {
	if len(observation.HistoryItems) == 0 {
		return nil
	}
	return &turnIdentity{fingerprint: observation.HistoryFingerprint}
}

// equals reports whether two requests present the same conversation state. An
// absent identity matches nothing, including another absent identity.
func (t *turnIdentity) equals(other *turnIdentity) bool {
	return t != nil && other != nil && t.fingerprint == other.fingerprint
}
