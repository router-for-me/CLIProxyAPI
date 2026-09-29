package service

import (
	"slices"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

const requestPolicyContextKey = "request_policy_state"

type PolicyDecision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	Source string `json:"source"`
}

type PolicyEvent struct {
	Attempt     int            `json:"attempt"`
	ChannelID   int            `json:"channel_id,omitempty"`
	Group       string         `json:"group,omitempty"`
	Rule        string         `json:"rule,omitempty"`
	Status      int            `json:"status,omitempty"`
	ErrorCode   string         `json:"error_code,omitempty"`
	ErrorSource string         `json:"error_source,omitempty"`
	ElapsedMS   int64          `json:"elapsed_ms"`
	Decision    PolicyDecision `json:"decision"`
	Health      string         `json:"health,omitempty"`
}

// RequestPolicyState records how one request was routed so administrators can
// read the decision flow in the log details. Channel selection and the retry
// decision stay in the relay flows; this state only records them.
type RequestPolicyState struct {
	FinalLogged       bool
	StartedAt         time.Time
	Attempts          int
	SelectedGroup     string
	SessionMode       string
	SessionModeSource string
	RuleName          string
	Successful        bool
	OutcomeRecorded   bool
	mu                sync.Mutex
	events            []PolicyEvent
}

func RequestPolicy(c *gin.Context) *RequestPolicyState {
	if c != nil {
		if value, ok := c.Get(requestPolicyContextKey); ok {
			return value.(*RequestPolicyState)
		}
	}
	state := &RequestPolicyState{StartedAt: time.Now()}
	if c != nil {
		c.Set(requestPolicyContextKey, state)
	}
	return state
}

func (s *RequestPolicyState) AddEvent(event PolicyEvent) {
	event.Attempt, event.ElapsedMS = s.Attempts, time.Since(s.StartedAt).Milliseconds()
	if event.Group == "" {
		event.Group = s.SelectedGroup
	}
	if event.Rule == "" {
		event.Rule = s.RuleName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) < 512 {
		s.events = append(s.events, event)
	}
}

func (s *RequestPolicyState) Events() []PolicyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *RequestPolicyState) BeginAttempt(channel *model.Channel, group string) {
	s.Attempts++
	s.Successful = false
	s.OutcomeRecorded = false
	s.SelectedGroup = group
	s.AddEvent(PolicyEvent{ChannelID: channel.Id, Decision: PolicyDecision{Action: "attempt", Reason: "channel_selected", Source: "routing"}})
}

// RecordPolicyFailure appends the failed attempt and the retry decision made
// for it. The health entry mirrors the check in ProcessChannelError, which
// performs the actual disable.
func RecordPolicyFailure(c *gin.Context, channelID int, err *types.NewAPIError, decision PolicyDecision) {
	if c == nil || err == nil {
		return
	}
	source := "upstream"
	switch {
	case types.IsChannelError(err):
		source = "channel"
	case err.GetErrorCode() == types.ErrorCodeDoRequestFailed:
		source = "transport"
	case err.GetErrorType() == types.ErrorTypeNewAPIError:
		source = "local"
	}
	state := RequestPolicy(c)
	event := PolicyEvent{ChannelID: channelID, Status: err.StatusCode, ErrorCode: string(err.GetErrorCode()), ErrorSource: source, Decision: PolicyDecision{Action: "failure", Reason: "upstream_failure", Source: source}}
	if source == "local" {
		event.Decision.Reason = "local_rejection"
	}
	state.AddEvent(event)
	event.Decision, event.Health = decision, "unchanged"
	if source != "local" && c.GetBool("auto_ban") && ShouldDisableChannel(err) {
		event.Health = "channel_disable_requested"
		if common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) {
			event.Health = "key_disable_requested"
		}
	}
	state.AddEvent(event)
}

func MarkRequestPolicySuccess(c *gin.Context, stream *relaycommon.StreamStatus) {
	state := RequestPolicy(c)
	if state.OutcomeRecorded {
		return
	}
	state.OutcomeRecorded = true
	state.Successful = stream == nil || stream.IsNormalEnd() && !stream.HasErrors() && (stream.ResponseOutcome() == "" || stream.ResponseOutcome() == "completed")
	decision := PolicyDecision{Action: "success", Reason: "request_completed", Source: "upstream"}
	if !state.Successful {
		decision = PolicyDecision{Action: "stop", Reason: "stream_not_successful", Source: "system"}
	}
	channelID := 0
	if c != nil {
		channelID = c.GetInt("channel_id")
	}
	state.AddEvent(PolicyEvent{ChannelID: channelID, Decision: decision})
}

// Rules explicitly inherit the global default or override it. Rules without a
// mode retain their legacy retry behavior until an administrator changes them.
func EffectiveSessionMode(setting *operation_setting.ChannelAffinitySetting, rule operation_setting.ChannelAffinityRule) (mode, source string) {
	if rule.SessionMode == "inherit" {
		if setting.SessionMode != "" {
			return setting.SessionMode, "global"
		}
		return "prefer", "global"
	}
	if rule.SessionMode != "" {
		return rule.SessionMode, "session_rule"
	}
	if rule.SkipRetryOnFailure {
		return "strict", "session_rule"
	}
	return "prefer", "session_rule"
}

// RecordRequestPolicyTermination appends the final decision after routing has
// stopped. It never writes a log row itself: the per-channel error log written
// by ProcessChannelError already carries the decision record, so a second row
// here would duplicate it.
func RecordRequestPolicyTermination(c *gin.Context, apiErr *types.NewAPIError) {
	if c == nil || apiErr == nil {
		return
	}
	state := RequestPolicy(c)
	if state.FinalLogged {
		return
	}
	state.FinalLogged = true
	events := state.Events()
	if len(events) == 0 || events[len(events)-1].Decision.Action != "stop" {
		state.AddEvent(PolicyEvent{ChannelID: c.GetInt("channel_id"), Status: apiErr.StatusCode, ErrorCode: string(apiErr.GetErrorCode()), Decision: PolicyDecision{Action: "stop", Reason: "request_failed", Source: "system"}, Health: "unchanged"})
	}
}
