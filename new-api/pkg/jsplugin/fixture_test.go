package jsplugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixturePlugin = `
export const meta = { apiVersion: 1, key: "fixture", name: "Fixture", version: "1.0.0", author: {name: "Test"}, channelTypes: [1002], models: ["fixture-model"], fetchMode: "per_task", protocols: [{name: "openai_responses", supports: ["sync", "background"]}] };
export function buildSubmitRequest(ctx) { return {url: ctx.baseUrl + "/submit"}; }
export function parseSubmitResponse(ctx, resp) { return {taskId: resp.body.id}; }
export function buildQueryRequest(ctx) { return {url: ctx.baseUrl + "/task"}; }
export function parseTaskResult(ctx, body) { return body; }
export function stamp(value) { return {value: value, now: utils.unixNow()}; }
export function fail() { throw new Error("fixture failure"); }
export const native = { compact: function(value) { return {id: value.task_id}; } };
export const protocols = { openai_responses: {
  decodeRequest: function(value) { return {kind: "submit", model: value.model}; },
  renderFinal: function(ctx, task) { return task; }
} };
`

func TestReplayFixture(t *testing.T) {
	t.Parallel()
	report, err := ReplayFixture(context.Background(), fixturePlugin, []byte(`{
  "unixNow": 1700000000,
  "cases": [
    {"name":"deterministic time","hook":"stamp","args":["ok"],"expected":{"value":"ok","now":1700000000}},
    {"name":"member call","hook":"native","member":"compact","args":[{"task_id":"task-1"}],"expected":{"id":"task-1"}},
    {"name":"nested path call","hook":"protocols","path":["openai_responses","decodeRequest"],"args":[{"model":"video-1"}],"expected":{"kind":"submit","model":"video-1"}},
    {"name":"expected failure","hook":"fail","args":[],"expectedError":"fixture failure"}
  ]
}`))
	require.NoError(t, err)
	assert.Equal(t, FixtureReport{Total: 4, Passed: 4}, report)
}

func TestReplayFixtureReportsMismatch(t *testing.T) {
	t.Parallel()
	report, err := ReplayFixture(context.Background(), fixturePlugin, []byte(`{
  "cases": [{"name":"wrong output","hook":"stamp","args":["ok"],"expected":{"value":"different"}}]
}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wrong output: result mismatch")
	assert.Equal(t, FixtureReport{Total: 1}, report)
}
