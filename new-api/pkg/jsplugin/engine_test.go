package jsplugin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/gin-gonic/gin"
	jsoniter "github.com/json-iterator/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngineCallsESMExportWithInjectedUtils(t *testing.T) {
	t.Parallel()
	logs := make([]string, 0, 1)
	engine, err := Compile(`
export function sign(ctx) {
  console.log("called", ctx.name);
  return {
    now: utils.unixNow(),
    digest: utils.hmacSHA256(ctx.message, ctx.secret),
    encoded: utils.base64(ctx.message),
  };
}

export const meta = { apiVersion: 1, key: "mock" };
`, Options{
		Key: "mock", Version: "1.0.0",
		Now: func() time.Time { return time.Unix(1234, 0) },
		Log: func(message string) { logs = append(logs, message) },
	})
	require.NoError(t, err)

	result, err := engine.Call(context.Background(), "sign", map[string]any{
		"name": "fixture", "message": "hello", "secret": "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"now": int64(1234), "digest": "88aab3ede8d3adf94d26ab90d3bafd4a2083070c3bcce9c014ee04a443847c0b", "encoded": "aGVsbG8=",
	}, result)
	assert.Equal(t, []string{"[plugin:mock@1.0.0] called fixture"}, logs)

	meta, err := engine.Export(context.Background(), "meta")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"apiVersion": int64(1), "key": "mock"}, meta)
}

func TestEngineJSONCloneProducesIndependentNativeContainers(t *testing.T) {
	engine, err := Compile(`
export function clone(input) {
  const result = utils.json.clone(input);
  result.items.push({text:"appended"});
  result.items[0].text = "changed";
  return {result, array:Array.isArray(result.items),
    prototype:Object.getPrototypeOf(result) === Object.prototype,
    ownProto:Object.prototype.hasOwnProperty.call(result,"__proto__"),
    available:utils.hasCapability("json-clone@1"), unavailable:utils.hasCapability("json-clone@2")};
}
export function invalid(kind) {
  let value;
  switch(kind) {
    case "cycle": value={}; value.self=value; break;
    case "function": value={run:function(){}}; break;
    case "undefined": value={missing:undefined}; break;
    case "nan": value=NaN; break;
    case "bigint": value=1n; break;
    case "date": value=new Date(); break;
    case "sparse": value=[,1]; break;
    case "size": value="<".repeat(200000); break;
    case "nodes": value=new Array(32768).fill(0); break;
  }
  return utils.json.clone(value);
}`, Options{})
	require.NoError(t, err)
	input := map[string]any{"items": []any{map[string]any{"text": "original"}}, "__proto__": map[string]any{"marker": "data"}, "zero": math.Copysign(0, -1)}
	value, err := engine.Call(t.Context(), "clone", input)
	require.NoError(t, err)
	result := value.(map[string]any)
	assert.Equal(t, true, result["array"])
	assert.Equal(t, true, result["prototype"])
	assert.Equal(t, true, result["ownProto"])
	assert.Equal(t, true, result["available"])
	assert.Equal(t, false, result["unavailable"])
	cloned := result["result"].(map[string]any)
	assert.Equal(t, []any{map[string]any{"text": "changed"}, map[string]any{"text": "appended"}}, cloned["items"])
	assert.Equal(t, map[string]any{"marker": "data"}, cloned["__proto__"])
	assert.True(t, math.Signbit(cloned["zero"].(float64)))
	assert.Equal(t, []any{map[string]any{"text": "original"}}, input["items"])
	for _, kind := range []string{"cycle", "function", "undefined", "nan", "bigint", "date", "sparse", "size", "nodes"} {
		t.Run(kind, func(t *testing.T) {
			_, err := engine.Call(t.Context(), "invalid", kind)
			require.Error(t, err)
		})
	}
}

func TestJSONStateChangesAndEncodedLimits(t *testing.T) {
	const changes = `[
  [{"op":"set","path":[],"value":{"text":"<","parts":[],"nested":{"value":null}}}],
  [{"op":"appendText","path":["text"],"value":"&\n\"图像😀\u2028"},
   {"op":"append","path":["parts"],"value":{"text":"a","enabled":false}},
   {"op":"set","path":["nested","value"],"value":[1,2]}],
  [{"op":"appendText","path":["parts",0,"text"],"value":"b"},
   {"op":"set","path":["nested","value",0],"value":0},
   {"op":"set","path":["nested","a<b"],"value":{}}]
]`
	var frames []any
	require.NoError(t, common.UnmarshalJsonStr(changes, &frames))
	const expected = `{"text":"<&\n\"图像😀\u2028","parts":[{"text":"ab","enabled":false}],"nested":{"value":[0,2],"a<b":{}}}`
	var want any
	require.NoError(t, common.UnmarshalJsonStr(expected, &want))
	encoded, err := common.Marshal(want)
	require.NoError(t, err)
	state := NewJSONState(len(encoded))
	for _, frame := range frames {
		require.NoError(t, state.Apply(t.Context(), frame))
		_, err = state.Value()
		require.NoError(t, err, "every intermediate frame must match the codec's actual byte size")
	}
	value, err := state.Value()
	require.NoError(t, err)
	assert.Equal(t, want, value)
	// One extra byte fails at the update boundary and invalidates publication.
	require.Error(t, state.Apply(t.Context(), []any{map[string]any{"op": "appendText", "path": []any{"text"}, "value": "x"}}))
	_, err = state.Value()
	require.Error(t, err)

	t.Run("replacement releases byte and node budget", func(t *testing.T) {
		state := NewJSONState(16)
		for _, value := range []any{"abcdefghijklmn", map[string]any{}, []any{int64(0), false, nil}, []any(nil), map[string]any(nil)} {
			require.NoError(t, state.Apply(t.Context(), []any{map[string]any{"op": "set", "path": []any{}, "value": value}}))
			_, err := state.Value()
			require.NoError(t, err)
		}
	})
	t.Run("encoded limit includes HTML escaping", func(t *testing.T) {
		state := NewJSONState(13)
		require.NoError(t, state.Apply(t.Context(), []any{map[string]any{"op": "set", "path": []any{}, "value": "<"}}))
		require.Error(t, state.Apply(t.Context(), []any{map[string]any{"op": "appendText", "path": []any{}, "value": "<"}}))
	})
	t.Run("request cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		state := NewJSONState(64)
		require.ErrorIs(t, state.Apply(ctx, []any{}), context.Canceled)
	})
	t.Run("bounded operation and result complexity", func(t *testing.T) {
		many := make([]any, 257)
		for index := range many {
			many[index] = map[string]any{"op": "set", "path": []any{}, "value": nil}
		}
		require.Error(t, NewJSONState(1024).Apply(t.Context(), many))
		var deep any = nil
		for range 33 {
			deep = []any{deep}
		}
		for _, value := range []any{deep, make([]any, 32768), math.Inf(1)} {
			state := NewJSONState(1 << 20)
			require.Error(t, state.Apply(t.Context(), []any{map[string]any{"op": "set", "path": []any{}, "value": value}}))
		}
	})
	for _, invalid := range []string{
		`[{"op":"remove","path":[],"value":null}]`,
		`[{"op":"set","path":[],"value":null,"extra":true}]`,
		`[{"op":"set","path":["missing","child"],"value":1}]`,
		`[{"op":"set","path":["parts",-1],"value":1}]`,
		`[{"op":"set","path":["parts",0.5],"value":1}]`,
		`[{"op":"set","path":["parts",100000000000000000000],"value":1}]`,
		`[{"op":"appendText","path":["parts"],"value":"x"}]`,
		`[{"op":"append","path":["text"],"value":"x"}]`,
	} {
		t.Run(invalid, func(t *testing.T) {
			state := NewJSONState(1024)
			require.NoError(t, state.Apply(t.Context(), frames[0]))
			var changes any
			require.NoError(t, common.UnmarshalJsonStr(invalid, &changes))
			require.Error(t, state.Apply(t.Context(), changes))
			_, err := state.Value()
			require.Error(t, err)
		})
	}
}

type bytePreservingJSONCodec struct {
	jsoniter.API
}

func (c bytePreservingJSONCodec) Decode(reader io.Reader, value any) error {
	return c.NewDecoder(reader).Decode(value)
}

func TestJSONStateUTF8Normalization(t *testing.T) {
	const codecEnv = "JSPLUGIN_TEST_BYTE_PRESERVING_JSON"
	if os.Getenv(codecEnv) == "1" {
		// Only this test runs in the child process; the parent's codec and
		// parallel tests are unaffected. Disabling HTML escaping in jsoniter
		// also preserves invalid UTF-8, independently of Sonic's platform support.
		kitutil.SetCodec(bytePreservingJSONCodec{jsoniter.Config{SortMapKeys: true}.Froze()})
		encoded, err := common.Marshal("\xff\xfe")
		require.NoError(t, err)
		require.Equal(t, []byte("\"\xff\xfe\""), encoded)
		var decoded any
		require.NoError(t, common.Unmarshal(encoded, &decoded))
		require.Equal(t, "\xff\xfe", decoded)
	} else {
		t.Run("byte preserving codec", func(t *testing.T) {
			executable, err := os.Executable()
			require.NoError(t, err)
			command := exec.CommandContext(t.Context(), executable, "-test.run=^TestJSONStateUTF8Normalization$", "-test.v")
			command.Env = append(os.Environ(), codecEnv+"=1")
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}

	const raw = "图像\xff\xfe😀<&\n\"\\\u2028"
	const normalized = "图像\ufffd\ufffd😀<&\n\"\\\u2028"
	for _, tc := range []struct {
		name    string
		op      string
		path    []any
		initial any
		value   any
		want    any
	}{
		{"set", "set", []any{}, false, raw, normalized},
		{"append", "append", []any{}, []any{float64(0), false}, raw, []any{float64(0), false, normalized}},
		{"appendText", "appendText", []any{"a<b"}, map[string]any{"a<b": "前\xff"}, raw, map[string]any{"a<b": "前\ufffd" + normalized}},
		{"separate invalid bytes", "appendText", []any{}, "\xff", "\xfe", "\ufffd\ufffd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := common.Marshal(tc.want)
			require.NoError(t, err)
			for _, shortBy := range []int{0, 1} {
				state := NewJSONState(len(encoded) - shortBy)
				err := state.Apply(t.Context(), []any{
					map[string]any{"op": "set", "path": []any{}, "value": tc.initial},
					map[string]any{"op": tc.op, "path": tc.path, "value": tc.value},
				})
				if shortBy == 0 {
					require.NoError(t, err, "normalized JSON must fit exactly")
					value, err := state.Value()
					require.NoError(t, err)
					assert.Equal(t, tc.want, value)
					continue
				}
				require.Error(t, err, "normalized JSON must exceed the limit by one byte")
				value, valueErr := state.Value()
				require.ErrorIs(t, valueErr, err)
				assert.Nil(t, value, "a failed batch must not publish its successful prefix")
				require.ErrorIs(t, state.Apply(t.Context(), []any{
					map[string]any{"op": "set", "path": []any{}, "value": nil},
				}), err, "a later replacement must not revive failed state")
			}
		})
	}

	t.Run("clone", func(t *testing.T) {
		engine, err := Compile(`export function clone(input) { return utils.json.clone(input); }`, Options{})
		require.NoError(t, err)
		input := map[string]any{"text": raw, "items": []any{float64(0), false, raw}}
		value, err := engine.Call(t.Context(), "clone", input)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"text": normalized, "items": []any{int64(0), false, normalized}}, value)
		assert.Equal(t, raw, input["text"])

		encoded, err := common.Marshal(normalized)
		require.NoError(t, err)
		padding := strings.Repeat("x", MaxJSONToolBytes-len(encoded))
		value, err = engine.Call(t.Context(), "clone", raw+padding)
		require.NoError(t, err, "normalized clone must fit exactly")
		assert.Equal(t, normalized, strings.TrimSuffix(value.(string), padding))
		value, err = engine.Call(t.Context(), "clone", raw+padding+"x")
		require.Error(t, err, "normalized clone must exceed the limit by one byte")
		assert.Nil(t, value)
	})

	t.Run("invalid keys and paths remain rejected", func(t *testing.T) {
		for _, change := range []any{
			map[string]any{"op": "set", "path": []any{}, "value": map[string]any{"\xff\xfe": raw}},
			map[string]any{"op": "set", "path": []any{"\xff\xfe"}, "value": raw},
		} {
			state := NewJSONState(128)
			require.Error(t, state.Apply(t.Context(), []any{
				map[string]any{"op": "set", "path": []any{}, "value": map[string]any{"\ufffd\ufffd": "original"}},
				change,
			}))
			value, err := state.Value()
			require.Error(t, err)
			assert.Nil(t, value)
		}
	})
}

func TestEngineConsoleLogUsesDebugLoggerAndRequestContext(t *testing.T) {
	previousDebug := common.DebugEnabled
	common.DebugEnabled = false
	t.Cleanup(func() { common.DebugEnabled = previousDebug })

	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousWriter
		common.LogWriterMu.Unlock()
	})

	plugin, err := CompilePlugin(`
export const meta = {
  apiVersion: 1,
  key: "console-debug",
  name: "Console debug",
  version: "1.2.3",
  author: {name: "Test"},
  models: ["debug-model"],
  fetchMode: "per_task",
};
export function run(label) {
  console.log("checkpoint", label);
  return true;
}
export function buildSubmitRequest() { return {url: "https://example.com"}; }
export function parseSubmitResponse() { return {taskId: "one"}; }
export function buildQueryRequest() { return {url: "https://example.com"}; }
export function parseTaskResult() { return {status: "SUCCESS"}; }
`, Options{})
	require.NoError(t, err)
	engine := plugin.Engine

	disabledContext := context.WithValue(context.Background(), common.RequestIdKey, "plugin-console-disabled")
	_, err = engine.Call(disabledContext, "run", "disabled")
	require.NoError(t, err)
	assert.Empty(t, output.String())

	common.DebugEnabled = true
	contextA := context.WithValue(context.Background(), common.RequestIdKey, "plugin-console-request-a")
	_, err = engine.Call(contextA, "run", "context-a")
	require.NoError(t, err)
	logA := output.String()
	output.Reset()

	contextB := context.WithValue(context.Background(), common.RequestIdKey, "plugin-console-request-b")
	_, err = engine.Call(contextB, "run", "context-b")
	require.NoError(t, err)
	logB := output.String()
	output.Reset()

	_, err = engine.Call(context.Background(), "run", "background")
	require.NoError(t, err)
	logBackground := output.String()

	assert.Contains(t, logA, "plugin-console-request-a")
	assert.NotContains(t, logA, "plugin-console-request-b")
	assert.Contains(t, logA, "task_plugin subsystem=runtime event=console")
	assert.Contains(t, logA, "[plugin:console-debug@1.2.3] checkpoint context-a")
	assert.NotContains(t, logA, "disabled")

	assert.Contains(t, logB, "plugin-console-request-b")
	assert.NotContains(t, logB, "plugin-console-request-a")
	assert.Contains(t, logB, "[plugin:console-debug@1.2.3] checkpoint context-b")

	assert.Contains(t, logBackground, "| SYSTEM |")
	assert.NotContains(t, logBackground, "plugin-console-request-a")
	assert.NotContains(t, logBackground, "plugin-console-request-b")
	assert.Contains(t, logBackground, "[plugin:console-debug@1.2.3] checkpoint background")
}

func TestCompileRejectsAsynchronousAndImportedPlugins(t *testing.T) {
	t.Parallel()
	for name, source := range map[string]string{
		"async":           `export async function run() {}`,
		"static import":   `import value from "dependency"; export function run() { return value; }`,
		"dynamic import":  `export function run() { return import("dependency"); }`,
		"top-level await": `const value = await work(); export function run() { return value; }`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Compile(source, Options{Key: "invalid"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported plugin syntax")
		})
	}

	_, err := Compile(`export function run() { return "import async await"; }`, Options{Key: "valid"})
	require.NoError(t, err)
}

func TestCompileIgnoresSourceMapDirectives(t *testing.T) {
	t.Parallel()
	// A sourceMappingURL comment must stay inert. Sobek's default loader
	// os.ReadFiles the referenced server path during Compile and turns any
	// load failure into a compile error, so an unresolvable path compiling
	// cleanly proves the loader is disabled.
	engine, err := Compile("export function run() { return 1; }\n//# sourceMappingURL=/nonexistent/leak-probe.map\n", Options{Key: "sourcemap"})
	require.NoError(t, err)

	value, err := engine.Call(context.Background(), "run")
	require.NoError(t, err)
	assert.Equal(t, int64(1), value)
}

func TestEngineInterruptsLongRunningHook(t *testing.T) {
	t.Parallel()
	engine, err := Compile(`let calls = 0; export function run(loop) { calls++; if (loop) { while (true) {} } return calls; }`, Options{
		Key: "loop", Version: "1", Timeout: 20 * time.Millisecond,
	})
	require.NoError(t, err)

	_, err = engine.Call(context.Background(), "run", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	var hookErr *HookError
	assert.False(t, errors.As(err, &hookErr), "timeouts must not be HookError")
	value, err := engine.Call(context.Background(), "run", false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), value, "an interrupted runtime must be discarded")
}

func TestEngineDoesNotReinitializeIdleRuntimeAfterGC(t *testing.T) {
	var initialized atomic.Int64
	engine, err := Compile(`console.log("initialized"); export function run(){return 42;}`, Options{
		Log: func(string) { initialized.Add(1) },
	})
	require.NoError(t, err)
	runtime.GC()
	runtime.GC()
	value, err := engine.Call(t.Context(), "run")
	require.NoError(t, err)
	assert.Equal(t, int64(42), value)
	assert.Equal(t, int64(1), initialized.Load())
}

func TestEngineBoundsConcurrentCallsAndCancelsAnOccupiedRuntime(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	engine, err := Compile(`export function run(loop) { utils.unixNow(); if (loop) { while (true) {} } return 42; }`, Options{
		Concurrency: 2,
		Now: func() time.Time {
			entered <- struct{}{}
			<-release
			return time.Unix(1, 0)
		},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	canceled := make(chan error, 1)
	healthy := make(chan error, 1)
	go func() { _, err := engine.Call(ctx, "run", true); canceled <- err }()
	go func() { _, err := engine.Call(t.Context(), "run", false); healthy <- err }()
	<-entered
	<-entered
	_, err = engine.CallPathWithAdmissionTimeout(t.Context(), time.Nanosecond, "run", nil, false)
	require.ErrorIs(t, err, ErrCallAdmissionTimeout)
	cancel()
	release <- struct{}{}
	release <- struct{}{}
	require.ErrorIs(t, <-canceled, context.Canceled)
	require.NoError(t, <-healthy)
}

func TestEnginePreservesMutableExportAndMemberBindings(t *testing.T) {
	engine, err := Compile(`
export let run = function(){return 1;};
export const native = {render:function(){return 1;}};
export function replace(){run=function(){return 2;};native.render=function(){return 3;};}
`, Options{Concurrency: 1})
	require.NoError(t, err)
	value, err := engine.Call(t.Context(), "run")
	require.NoError(t, err)
	assert.Equal(t, int64(1), value)
	_, err = engine.Call(t.Context(), "replace")
	require.NoError(t, err)
	value, err = engine.Call(t.Context(), "run")
	require.NoError(t, err)
	assert.Equal(t, int64(2), value)
	value, err = engine.CallMember(t.Context(), "native", "render")
	require.NoError(t, err)
	assert.Equal(t, int64(3), value)
}

func TestEngineHookErrorExtractsSanitizedJSMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		source      string
		wantMessage string
		wantLen     int
	}{
		{
			name:        "Error object",
			source:      `export function run() { throw new Error("model is required"); }`,
			wantMessage: "model is required",
		},
		{
			name:        "raw string throw",
			source:      `export function run() { throw "raw string"; }`,
			wantMessage: "raw string",
		},
		{
			name:        "truncates to 512 runes",
			source:      `export function run() { throw new Error("x".repeat(2000)); }`,
			wantLen:     512,
			wantMessage: strings.Repeat("x", 512),
		},
		{
			name:        "scrubs control characters",
			source:      "export function run() { throw new Error(\"line1\\nline2\\x1b[31mred\"); }",
			wantMessage: "line1 line2 [31mred",
		},
		{
			name:        "throwing message getter falls back without crashing",
			source:      `export function run() { throw {get message() { throw {get message() { return "deep"; }}; }}; }`,
			wantMessage: "plugin hook failed",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			engine, err := Compile(testCase.source, Options{Key: "diag", Version: "1.0.0"})
			require.NoError(t, err)

			_, err = engine.Call(context.Background(), "run")
			require.Error(t, err)

			var hookErr *HookError
			require.True(t, errors.As(err, &hookErr))
			assert.Equal(t, "run", hookErr.Hook)
			assert.Equal(t, testCase.wantMessage, hookErr.Message)
			if testCase.wantLen > 0 {
				assert.Equal(t, testCase.wantLen, utf8.RuneCountInString(hookErr.Message))
			}
			assert.Contains(t, hookErr.Error(), "plugin diag@1.0.0")
			assert.NotContains(t, hookErr.Message, "Error:")
			assert.NotContains(t, hookErr.Message, "plugin diag@")
		})
	}
}

func TestEngineReportsProtocolAdmissionTimeoutSeparately(t *testing.T) {
	engine, err := Compile(`
export const protocols = {
	responses: {renderEvents: function() { return {events: [], done: false}; }},
};
`, Options{Key: "admission", Version: "1", Concurrency: 1})
	require.NoError(t, err)

	engine.semaphore <- struct{}{}
	_, err = engine.CallPathWithAdmissionTimeout(
		context.Background(),
		time.Nanosecond,
		"protocols",
		[]string{"responses", "renderEvents"},
	)
	<-engine.semaphore

	require.ErrorIs(t, err, ErrCallAdmissionTimeout)
	result, err := engine.CallPathWithAdmissionTimeout(
		context.Background(),
		time.Second,
		"protocols",
		[]string{"responses", "renderEvents"},
	)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"events": []any{}, "done": false}, result)
}

func TestEngineExportInterruptsLongRunningGetter(t *testing.T) {
	t.Parallel()
	engine, err := Compile(`
export const meta = {
	apiVersion: 1,
	get name() { while (true) {} },
};
`, Options{Key: "meta-loop", Version: "1.0.0", Timeout: 20 * time.Millisecond})
	require.NoError(t, err)

	_, err = engine.Export(context.Background(), "meta")
	require.ErrorContains(t, err, "export meta interrupted")
}

func TestEngineExportReturnsThrownGetterError(t *testing.T) {
	t.Parallel()
	engine, err := Compile(`
export const meta = {
	apiVersion: 1,
	get name() { throw new Error("getter failed"); },
};
`, Options{Key: "meta-throw", Version: "1.0.0"})
	require.NoError(t, err)

	_, err = engine.Export(context.Background(), "meta")
	require.ErrorContains(t, err, "export meta failed")
	assert.Contains(t, err.Error(), "getter failed")
}

func TestEngineNestedHooksRequireOwnProperties(t *testing.T) {
	t.Parallel()
	engine, err := Compile(`
const inheritedRenderers = {
	inherited: function(value) { return value; },
	constructor: function(value) { return value; },
	toString: function(value) { return value; },
	["__proto__"]: function(value) { return value; },
};
export const renderers = Object.create(inheritedRenderers);
renderers.own = function(value) { return {id: value.id}; };

const inheritedProtocol = {
	renderFinal: function(value) { return value; },
};
export const protocols = {
	responses: Object.create(inheritedProtocol),
};
`, Options{Key: "own-hooks", Version: "1.0.0"})
	require.NoError(t, err)

	found, err := engine.HasCallablePath(context.Background(), "renderers", "own")
	require.NoError(t, err)
	assert.True(t, found)
	result, err := engine.CallMember(context.Background(), "renderers", "own", map[string]any{"id": "task-1"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"id": "task-1"}, result)

	for _, member := range []string{"inherited", "constructor", "toString", "__proto__"} {
		t.Run(member, func(t *testing.T) {
			found, err := engine.HasCallablePath(context.Background(), "renderers", member)
			require.NoError(t, err)
			assert.False(t, found)

			_, err = engine.CallMember(context.Background(), "renderers", member)
			require.ErrorContains(t, err, "not found")
		})
	}

	found, err = engine.HasCallablePath(context.Background(), "protocols", "responses", "renderFinal")
	require.NoError(t, err)
	assert.False(t, found)
	_, err = engine.CallPath(context.Background(), "protocols", []string{"responses", "renderFinal"})
	require.ErrorContains(t, err, "not found")
}

func TestCompileInterruptsLongRunningInitialization(t *testing.T) {
	t.Parallel()
	_, err := Compile(`while (true) {}; export function run() {}`, Options{
		Key: "loop", Version: "1", Timeout: 20 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initialization timed out")
}

func TestValidateRequestURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		requestURL   string
		baseURL      string
		allowedHosts []string
		wantError    string
	}{
		{name: "same host", requestURL: "https://api.example.com/v1/task", baseURL: "https://api.example.com/v1"},
		{name: "default port", requestURL: "https://api.example.com:443/v1/task", baseURL: "https://api.example.com"},
		{name: "approved host", requestURL: "https://upload.example.com/task", baseURL: "https://api.example.com", allowedHosts: []string{"upload.example.com"}},
		{name: "approved host with port", requestURL: "http://192.168.1.10:8080/task", baseURL: "http://192.168.1.10:8000", allowedHosts: []string{"192.168.1.10:8080"}},
		{name: "approved host with explicit default port", requestURL: "https://upload.example.com/task", baseURL: "https://api.example.com", allowedHosts: []string{"upload.example.com:443"}},
		{name: "approved host port mismatch", requestURL: "http://192.168.1.10:9000/task", baseURL: "http://192.168.1.10:8000", allowedHosts: []string{"192.168.1.10:8080"}, wantError: "not allowed"},
		{name: "subdomain is not implicit", requestURL: "https://evil.api.example.com/task", baseURL: "https://api.example.com", wantError: "not allowed"},
		{name: "userinfo trick", requestURL: "https://api.example.com@evil.example/task", baseURL: "https://api.example.com", wantError: "not allowed"},
		{name: "relative URL", requestURL: "/v1/task", baseURL: "https://api.example.com", wantError: "absolute"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRequestURL(test.requestURL, test.baseURL, test.allowedHosts)
			if test.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), test.wantError), err.Error())
		})
	}
}
