package main

import (
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// coreMessageHashPrefix marks the content hash core returns when a client
	// supplies no durable session value. That hash folds in the first assistant
	// reply, so it names one conversation differently before and after its first
	// answer.
	coreMessageHashPrefix = "msg:"

	// derivedIdentityPrefix marks an identity this plugin computed, keeping it
	// distinguishable from any value core supplied.
	derivedIdentityPrefix = "conv:"
)

// identitySource names where a conversation identity came from.
type identitySource string

const (
	// identitySourceCore marks an identity the client or core supplied durably.
	identitySourceCore identitySource = "core"
	// identitySourceDerived marks an identity computed from conversation content.
	identitySourceDerived identitySource = "derived"
	// identitySourceAbsent marks a request that carries no identity at all.
	identitySourceAbsent identitySource = "absent"
)

// conversationIdentity names one conversation and records the origin of its value.
type conversationIdentity struct {
	Value  string
	Source identitySource
}

// turnIdentity names one conversation state. A value exists only when the
// request carried history, so an absent identity has no representation.
type turnIdentity struct {
	fingerprint string
}

// newConversationIdentity resolves the identity that keys one conversation cursor.
// A durable core identity passes through unchanged. A core content-hash fallback is
// replaced by an identity built from values that hold constant across every turn.
func newConversationIdentity(req pluginapi.ModelRouteRequest, observation requestObservation, salt []byte) conversationIdentity {
	var identity conversationIdentity
	core := strings.TrimSpace(coreauth.ExtractSessionID(req.Headers, req.Body, req.Metadata))
	if core != "" && !strings.HasPrefix(core, coreMessageHashPrefix) {
		identity = conversationIdentity{Value: core, Source: identitySourceCore}
	} else if derived := derivedConversationIdentity(observation, salt); derived != "" {
		identity = conversationIdentity{Value: derived, Source: identitySourceDerived}
	} else {
		// Neither a durable client value nor conversation content names this request.
		identity = conversationIdentity{Source: identitySourceAbsent}
	}
	return identity
}

// derivedConversationIdentity folds the system fingerprint and the first history
// item fingerprint into one identity. Both hold constant for the life of a
// conversation. The result is empty when the request carries neither value.
func derivedConversationIdentity(observation requestObservation, salt []byte) string {
	parts := make([]string, 0, 2)
	if observation.SystemFingerprint != "" {
		parts = append(parts, observation.SystemFingerprint)
	}
	if len(observation.HistoryItems) > 0 {
		parts = append(parts, observation.HistoryItems[0])
	}
	identity := ""
	if len(parts) > 0 {
		identity = derivedIdentityPrefix + fingerprintStrings(parts, salt)
	}
	return identity
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
