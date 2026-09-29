// Package convmeta defines the conversion-context contract between format
// converters (future relaykit) and the hosting application. Converters read
// protocol state and per-request options exclusively through the Meta
// interface; the host's RelayInfo implements it.
package convmeta

import (
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// Meta is the only view of the relay session that format converters may use.
// It is satisfied by *relaycommon.RelayInfo on the host side; other embedders
// (tests, external relaykit users) can use *Values.
// Implementations backed by pointer types must make every method safe on a nil
// receiver: a typed-nil pointer stored in Meta is still a non-nil interface,
// and relaykit deliberately does not use reflection to detect that case.
type Meta interface {
	GetOriginModelName() string
	GetUpstreamModelName() string
	// HasChannelMeta reports whether upstream channel information is attached;
	// converters use it to decide if GetUpstreamModelName is meaningful.
	HasChannelMeta() bool
	GetChannelID() int
	GetChannelType() int
	GetIsStream() bool
	GetReasoningEffort() string
	// SetReasoningEffort records the effort level a converter derived from a
	// model-name suffix so downstream billing/logging can see it.
	SetReasoningEffort(effort string)
	// ReasoningState returns the suffix-derived reasoning intent attached at
	// the host entry layer. Standalone callers that do not set it receive nil;
	// converters then use only explicit request fields.
	ReasoningState() *dto.ReasoningConversionState
	GetEstimatePromptTokens() int

	// EnsureClaudeConvertInfo lazily creates and returns the mutable
	// OpenAI→Claude stream conversion state. For non-nil receivers, the same
	// instance must be returned for the lifetime of one streaming session; a
	// nil receiver may return a temporary initialized state.
	EnsureClaudeConvertInfo() *ClaudeConvertInfo

	// GetSendResponseCount / IncrSendResponseCount expose the shared
	// downstream-chunk counter (the host may also increment it).
	GetSendResponseCount() int
	IncrSendResponseCount()

	// AppendRequestConversion records a hop in the request format chain.
	AppendRequestConversion(format types.RelayFormat)

	// ConvOptions returns the request-scoped conversion options snapshot.
	// Must never return nil.
	ConvOptions() *Options
}

// ClaudeConvertInfo carries mutable state for OpenAI chat → Claude Messages
// stream conversion. Moved here from relay/common (which keeps an alias).
type ClaudeConvertInfo struct {
	LastMessagesType string
	Index            int
	Usage            *dto.Usage
	FinishReason     string
	Done             bool

	ToolCallBaseIndex      int
	ToolCallMaxIndexOffset int
	ToolCalls              []*ClaudeStreamToolCall
	ToolCallByIndex        map[int]*ClaudeStreamToolCall
	ToolCallByID           map[string]*ClaudeStreamToolCall
}

// ClaudeStreamToolCall tracks one OpenAI tool_calls entry while it is encoded
// as a Claude tool_use content block. Chat tool indexes and Claude content
// block indexes are separate domains, so the mapping must remain explicit.
type ClaudeStreamToolCall struct {
	BlockIndex       int
	ID               string
	Name             string
	PendingArguments string
	Started          bool
}

const (
	LastMessageTypeNone     = "none"
	LastMessageTypeText     = "text"
	LastMessageTypeTools    = "tools"
	LastMessageTypeThinking = "thinking"
)

// Values is a plain-struct Meta implementation for tests and non-RelayInfo
// hosts (the relaykit-native entry point).
type Values struct {
	OriginModelName      string
	UpstreamModelName    string
	ChannelMetaAttached  bool
	ChannelID            int
	ChannelType          int
	IsStream             bool
	ReasoningEffort      string
	ReasoningConversion  *dto.ReasoningConversionState
	EstimatePromptTokens int

	ClaudeConvertInfo *ClaudeConvertInfo
	SendResponseCount int
	ConversionChain   []types.RelayFormat

	Options *Options
}

var _ Meta = (*Values)(nil)

func (v *Values) GetOriginModelName() string {
	if v == nil {
		return ""
	}
	return v.OriginModelName
}

func (v *Values) GetUpstreamModelName() string {
	if v == nil {
		return ""
	}
	return v.UpstreamModelName
}

func (v *Values) HasChannelMeta() bool {
	return v != nil && v.ChannelMetaAttached
}

func (v *Values) GetChannelID() int {
	if v == nil {
		return 0
	}
	return v.ChannelID
}

func (v *Values) GetChannelType() int {
	if v == nil {
		return 0
	}
	return v.ChannelType
}

func (v *Values) GetIsStream() bool {
	return v != nil && v.IsStream
}

func (v *Values) GetReasoningEffort() string {
	if v == nil {
		return ""
	}
	return v.ReasoningEffort
}

func (v *Values) SetReasoningEffort(effort string) {
	if v != nil {
		v.ReasoningEffort = effort
	}
}

func (v *Values) ReasoningState() *dto.ReasoningConversionState {
	if v == nil {
		return nil
	}
	return v.ReasoningConversion
}

func (v *Values) GetEstimatePromptTokens() int {
	if v == nil {
		return 0
	}
	return v.EstimatePromptTokens
}

func (v *Values) EnsureClaudeConvertInfo() *ClaudeConvertInfo {
	if v == nil {
		return &ClaudeConvertInfo{LastMessagesType: LastMessageTypeNone}
	}
	if v.ClaudeConvertInfo == nil {
		v.ClaudeConvertInfo = &ClaudeConvertInfo{LastMessagesType: LastMessageTypeNone}
	}
	return v.ClaudeConvertInfo
}

func (v *Values) GetSendResponseCount() int {
	if v == nil {
		return 0
	}
	return v.SendResponseCount
}

func (v *Values) IncrSendResponseCount() {
	if v != nil {
		v.SendResponseCount++
	}
}

func (v *Values) AppendRequestConversion(format types.RelayFormat) {
	if v == nil || format == "" {
		return
	}
	if n := len(v.ConversionChain); n > 0 && v.ConversionChain[n-1] == format {
		return
	}
	v.ConversionChain = append(v.ConversionChain, format)
}

func (v *Values) ConvOptions() *Options {
	if v == nil {
		return &Options{}
	}
	if v.Options == nil {
		v.Options = &Options{}
	}
	return v.Options
}

// UpstreamModelName / ChannelTypeOf are nil-safe accessors for optional Meta
// values (converters are often called with a nil Meta in tests and compat
// shims).
func UpstreamModelName(m Meta) string {
	if m == nil || !m.HasChannelMeta() {
		return ""
	}
	return m.GetUpstreamModelName()
}

func ChannelTypeOf(m Meta) int {
	if m == nil || !m.HasChannelMeta() {
		return 0
	}
	return m.GetChannelType()
}

// OptionsOf returns m's conversion options, or empty defaults when m is nil.
func OptionsOf(m Meta) *Options {
	if m == nil {
		return &Options{}
	}
	return m.ConvOptions()
}

// ReasoningStateOf is a nil-safe reader for Meta.ReasoningState.
func ReasoningStateOf(m Meta) *dto.ReasoningConversionState {
	if m == nil {
		return nil
	}
	return m.ReasoningState()
}
