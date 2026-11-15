package executor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A Responses request with an output schema reaches Anthropic as
// output_config.format, unchanged and beside output_config.effort, for an API
// key and for an OAuth login with and without cloaking, on every execution
// path that sends a body.
func TestClaudeExecutor_ResponsesOutputSchemaReachesOutputConfigFormat(t *testing.T) {
	const schema = `{"type":"object","properties":{"title":{"type":"string"},"difficulty":{"type":"string","enum":["EASY","HARD"]},"notes":{"type":["string","null"]}},"required":["title","difficulty","notes"],"additionalProperties":false}`
	payload := []byte(`{"model":"claude-sonnet-5-5","instructions":"You are a coding agent.","input":[{"role":"user","content":[{"type":"input_text","text":"Suggest a recipe."}]}],"reasoning":{"effort":"high"},"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],"tool_choice":"auto","parallel_tool_calls":false,"text":{"format":{"type":"json_schema","name":"output_schema","strict":true,"schema":` + schema + `}}}`)

	oauth := func() *cliproxyauth.Auth {
		metadata := claudeOAuthTestMetadata()
		metadata["access_token"] = "sk-ant-oat01-structured"
		return &cliproxyauth.Auth{Provider: "claude", Metadata: metadata}
	}
	cloakOff := &config.Config{}
	cloakOff.DisableClaudeCloakMode = true
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		auth *cliproxyauth.Auth
	}{
		{name: "api key", cfg: &config.Config{}, auth: &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-structured"}}},
		{name: "oauth, cloaking off", cfg: cloakOff, auth: oauth()},
		{name: "oauth, cloaking auto", cfg: &config.Config{}, auth: oauth()},
	} {
		for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(tc.cfg), tc.auth, cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}, method)
				if got := ojson.GetBytes(body, "output_config.format").Value(); !reflect.DeepEqual(got, ojson.Parse(`{"type":"json_schema","schema":`+schema+`}`).Value()) {
					t.Fatalf("output_config.format = %#v; body=%s", got, body)
				}
				if strings.Contains(string(body), "conforms strictly") {
					t.Errorf("schema instruction must not be sent beside output_config.format: %s", body)
				}
				if method != "CountTokens" && ojson.GetBytes(body, "output_config.effort").String() != "high" {
					t.Errorf("output_config.effort lost: %s", body)
				}
			})
		}
	}
}
