package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// laneProjection reports one lane's view of a stored transcript: the body that
// lane receives, and how much reasoning the view withheld and kept.
type laneProjection struct {
	Lane     reasoningLane
	Body     []byte
	Dropped  int
	Retained int
}

// interceptRequestAfter answers the after-credential hook with the lane
// projection of the outgoing request. A projection withholding nothing answers
// with an empty response, which leaves the host payload untouched. A body the
// projection cannot edit answers with the edit error.
func (r *runtimeState) interceptRequestAfter(req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	projection, errProject := r.projectRequestLane(req)
	if errProject != nil {
		return pluginapi.RequestInterceptResponse{}, errProject
	}
	r.observeRequestContext(req, projection)
	if projection.Dropped == 0 {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	return pluginapi.RequestInterceptResponse{Body: projection.Body}, nil
}

// projectRequestLane resolves the lane one request targets and withholds the
// reasoning another lane produced. A request naming no single lane keeps its
// body, because no lane membership is provable for its items.
func (r *runtimeState) projectRequestLane(req pluginapi.RequestInterceptRequest) (laneProjection, error) {
	cfg := r.loadedConfig()
	if cfg == nil || !cfg.Enabled {
		return laneProjection{Body: req.Body}, nil
	}
	lane, laneKnown := cfg.laneFor(req.RequestedModel, req.Model)
	if !laneKnown {
		return laneProjection{Body: req.Body}, nil
	}
	return projectLaneHistory(req.Body, lane, r.owners, cfg)
}

// projectLaneHistory withholds the reasoning items one lane did not produce and
// leaves every other item class in place, so both providers' answers stay
// interleaved in one thread. Every identified item renews its ownership record,
// withheld or kept, because the conversation still carries it. A body the JSON
// writer cannot edit answers with that error.
func projectLaneHistory(body []byte, lane reasoningLane, owners *reasoningOwnerStore, cfg *compiledConfig) (laneProjection, error) {
	projection := laneProjection{Lane: lane, Body: body}
	field, carried := historyFieldName(body)
	if !carried {
		return projection, nil
	}
	foreign := make([]int, 0, 1)
	for index, item := range gjson.GetBytes(body, field).Array() {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			// absent a join key there is no evidence of foreign origin
			projection.Retained++
			continue
		}
		owner, recorded := owners.renewLane(reasoningOwnerKey{Generation: cfg.Generation, ItemID: itemID}, cfg.SessionTTL)
		if recorded && owner == lane {
			projection.Retained++
			continue
		}
		// An unrecorded item cannot be shown to belong to this lane, and the two
		// outcomes are asymmetric: withholding costs one cache miss that the next
		// turn repairs, while a foreign item costs the turn.
		foreign = append(foreign, index)
	}
	if len(foreign) == 0 {
		return projection, nil
	}

	projected := bytes.Clone(body)
	var errDelete error
	// Descending order keeps each remaining index valid as later items leave.
	for cursor := len(foreign) - 1; cursor >= 0 && errDelete == nil; cursor-- {
		projected, errDelete = sjson.DeleteBytes(projected, field+"."+strconv.Itoa(foreign[cursor]))
	}
	if errDelete != nil {
		return laneProjection{}, fmt.Errorf("withhold %d foreign reasoning items from %s for lane %s: %w", len(foreign), field, lane, errDelete)
	}
	projection.Body = projected
	projection.Dropped = len(foreign)
	return projection, nil
}
