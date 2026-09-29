package plugins_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	builtinplugins "github.com/QuantumNous/new-api/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Google long-running operations serialize proto3 defaults by omission: a
// still-running operation has no `done` key at all. Treating that as
// unrecognized would count every normal poll as a failure and fail the task
// at the poll-failure threshold while the video is still rendering.
func TestVeoParseTaskResultTreatsMissingDoneAsInProgress(t *testing.T) {
	cases := []struct {
		name       string
		body       map[string]any
		wantStatus string
	}{
		{
			name:       "running operation omits done",
			body:       map[string]any{"name": "operations/abc", "metadata": map[string]any{"@type": "x"}},
			wantStatus: "IN_PROGRESS",
		},
		{
			name:       "explicit done false",
			body:       map[string]any{"name": "operations/abc", "done": false},
			wantStatus: "IN_PROGRESS",
		},
		{
			name:       "body without operation name is unrecognized",
			body:       map[string]any{"foo": "bar"},
			wantStatus: "UNKNOWN",
		},
		{
			name:       "operation error is failure",
			body:       map[string]any{"name": "operations/abc", "done": true, "error": map[string]any{"message": "quota exceeded"}},
			wantStatus: "FAILURE",
		},
	}
	for _, key := range []string{"google", "vertex-ai"} {
		source, err := builtinplugins.Source(key)
		require.NoError(t, err)
		plugin, err := jsplugin.NewRegistry().RegisterFactory(source, jsplugin.Options{Key: key})
		require.NoError(t, err)
		for _, tc := range cases {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				value, err := plugin.Engine.Call(t.Context(), "parseTaskResult", map[string]any{}, tc.body)
				require.NoError(t, err)
				encoded, err := common.Marshal(value)
				require.NoError(t, err)
				var result map[string]any
				require.NoError(t, common.Unmarshal(encoded, &result))
				assert.Equal(t, tc.wantStatus, result["status"])
			})
		}
	}
}
