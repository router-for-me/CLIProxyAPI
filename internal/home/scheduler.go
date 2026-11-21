package home

import (
	"context"
	"fmt"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// SchedulerProtocol reports the read-only scheduling protocol advertised by Home.
// An absent advertisement keeps newer CPA compatible with older Home versions.
func (c *Client) SchedulerProtocol() int {
	if c == nil {
		return 0
	}
	return int(c.schedulerProtocol.Load())
}

func (c *Client) updateSchedulerProtocol(raw []byte) error {
	var features struct {
		SchedulerProtocol int64 `yaml:"home-scheduler-protocol"`
	}
	if errDecode := yaml.Unmarshal(raw, &features); errDecode != nil {
		return fmt.Errorf("home: decode scheduler capabilities: %w", errDecode)
	}
	c.schedulerProtocol.Store(features.SchedulerProtocol)
	return nil
}

// SchedulerContinuityError preserves the structured Home error envelope so the
// execution layer can apply the normal dispatch error classification.
type SchedulerContinuityError struct {
	Payload []byte
}

func (e *SchedulerContinuityError) Error() string {
	return "home rejected scheduler continuity observation"
}

type SchedulerContinuityQuery struct {
	Request                   pluginapi.SchedulerContinuityRequest `json:"request"`
	CredentialPolicy          string                               `json:"credential_policy,omitempty"`
	RetryRound                int                                  `json:"retry_round"`
	ApplyRetryRoundExclusions bool                                 `json:"apply_retry_round_exclusions"`
	ExcludedAuthIDs           []string                             `json:"excluded_auth_ids,omitempty"`
	PinnedAuthID              string                               `json:"pinned_auth_id,omitempty"`
}

// EvaluateSchedulerContinuity observes Home candidates without acquiring an execution scope.
func (c *Client) EvaluateSchedulerContinuity(ctx context.Context, query SchedulerContinuityQuery) (pluginapi.SchedulerContinuityResponse, bool, error) {
	empty := pluginapi.SchedulerContinuityResponse{}
	if c == nil || c.dispatchFenced.Load() {
		return empty, false, ErrDispatchFenced
	}
	if c.SchedulerProtocol() < 1 {
		return empty, false, nil
	}
	payload, errMarshal := json.Marshal(struct {
		Type string `json:"type"`
		SchedulerContinuityQuery
	}{Type: "scheduler-continuity", SchedulerContinuityQuery: query})
	if errMarshal != nil {
		return empty, false, fmt.Errorf("home: encode scheduler continuity request: %w", errMarshal)
	}
	cmd, errClient := c.commandClient()
	if errClient != nil {
		return empty, false, errClient
	}
	raw, errRPop := cmd.RPop(ctx, string(payload)).Bytes()
	if errRPop != nil {
		return empty, false, fmt.Errorf("home: scheduler continuity: %w", errRPop)
	}
	var envelope struct {
		Response *pluginapi.SchedulerContinuityResponse `json:"response"`
		Handled  *bool                                  `json:"handled"`
		Error    *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if errDecode := json.Unmarshal(raw, &envelope); errDecode != nil {
		return empty, false, fmt.Errorf("home: decode scheduler continuity response: %w", errDecode)
	}
	if envelope.Error != nil {
		return empty, false, &SchedulerContinuityError{Payload: append([]byte(nil), raw...)}
	}
	if envelope.Handled == nil {
		return empty, false, fmt.Errorf("home: scheduler continuity response missing handled")
	}
	if envelope.Response == nil {
		return empty, false, fmt.Errorf("home: scheduler continuity response missing decision envelope")
	}
	return *envelope.Response, *envelope.Handled, nil
}
