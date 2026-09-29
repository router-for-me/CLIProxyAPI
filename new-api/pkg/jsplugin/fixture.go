package jsplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Fixture describes deterministic calls into a plugin. Arguments and expected
// values stay as JSON so fixtures remain portable across engine implementations.
type Fixture struct {
	UnixNow *int64        `json:"unixNow"`
	Cases   []FixtureCase `json:"cases"`
}

type FixtureCase struct {
	Name          string            `json:"name"`
	Hook          string            `json:"hook"`
	Member        string            `json:"member,omitempty"`
	Path          []string          `json:"path,omitempty"`
	Args          []json.RawMessage `json:"args"`
	Expected      json.RawMessage   `json:"expected"`
	ExpectedError string            `json:"expectedError,omitempty"`
}

type FixtureReport struct {
	Total  int
	Passed int
}

// ReplayFixture compiles a plugin and runs every fixture case in declaration
// order. unixNow is fixed by the fixture to keep signing and timestamp hooks
// reproducible.
func ReplayFixture(ctx context.Context, source string, data []byte) (FixtureReport, error) {
	var fixture Fixture
	if err := common.Unmarshal(data, &fixture); err != nil {
		return FixtureReport{}, fmt.Errorf("decode fixture: %w", err)
	}
	if len(fixture.Cases) == 0 {
		return FixtureReport{}, fmt.Errorf("fixture must contain at least one case")
	}

	options := Options{Key: "fixture", Version: "fixture"}
	if fixture.UnixNow != nil {
		fixed := time.Unix(*fixture.UnixNow, 0)
		options.Now = func() time.Time { return fixed }
	}
	plugin, err := NewRegistry().Register(source, options)
	if err != nil {
		return FixtureReport{}, fmt.Errorf("compile plugin: %w", err)
	}

	report := FixtureReport{Total: len(fixture.Cases)}
	for index, testCase := range fixture.Cases {
		caseName := strings.TrimSpace(testCase.Name)
		if caseName == "" {
			caseName = fmt.Sprintf("case %d", index+1)
		}
		if strings.TrimSpace(testCase.Hook) == "" {
			return report, fmt.Errorf("%s: hook is required", caseName)
		}

		args := make([]any, len(testCase.Args))
		for argumentIndex, raw := range testCase.Args {
			if err = common.Unmarshal(raw, &args[argumentIndex]); err != nil {
				return report, fmt.Errorf("%s: decode argument %d: %w", caseName, argumentIndex+1, err)
			}
		}
		var result any
		if testCase.Member != "" && len(testCase.Path) > 0 {
			return report, fmt.Errorf("%s: member and path are mutually exclusive", caseName)
		}
		if len(testCase.Path) > 0 {
			result, err = plugin.Engine.CallPath(ctx, testCase.Hook, testCase.Path, args...)
		} else if testCase.Member == "" {
			result, err = plugin.Engine.Call(ctx, testCase.Hook, args...)
		} else {
			result, err = plugin.Engine.CallMember(ctx, testCase.Hook, testCase.Member, args...)
		}
		if testCase.ExpectedError != "" {
			if err == nil || !strings.Contains(err.Error(), testCase.ExpectedError) {
				return report, fmt.Errorf("%s: expected error containing %q, got %v", caseName, testCase.ExpectedError, err)
			}
			report.Passed++
			continue
		}
		if err != nil {
			return report, fmt.Errorf("%s: %w", caseName, err)
		}
		if len(testCase.Expected) == 0 {
			return report, fmt.Errorf("%s: expected is required when expectedError is empty", caseName)
		}
		var expected any
		if err = common.Unmarshal(testCase.Expected, &expected); err != nil {
			return report, fmt.Errorf("%s: decode expected value: %w", caseName, err)
		}
		actualData, marshalErr := common.Marshal(result)
		if marshalErr != nil {
			return report, fmt.Errorf("%s: encode actual value: %w", caseName, marshalErr)
		}
		var actual any
		if err = common.Unmarshal(actualData, &actual); err != nil {
			return report, fmt.Errorf("%s: normalize actual value: %w", caseName, err)
		}
		if !reflect.DeepEqual(expected, actual) {
			return report, fmt.Errorf("%s: result mismatch: expected %s, got %s", caseName, testCase.Expected, actualData)
		}
		report.Passed++
	}
	return report, nil
}
