package toolcall

import (
	"testing"
)

// TestParseTruncatedEPSEClose verifies that a tool_calls block whose root
// close tag is truncated to </|EPSE> (instead of </|EPSE|tool_calls>) is still
// parsed correctly. Models emit this truncation pattern frequently.
func TestParseTruncatedEPSEClose(t *testing.T) {
	input := `<|EPSE|tool_calls> <|EPSE|invoke name="read"> <|EPSE|parameter name="file_path"><![CDATA[/Users/wangxilei/work/OutlookRegister/gui/main_window.py]]></|EPSE|parameter> </|EPSE|invoke> <|EPSE|invoke name="read"> <|EPSE|parameter name="file_path"><![CDATA[/Users/wangxilei/work/OutlookRegister/gui/core/config_manager.py]]></|EPSE|parameter> </|EPSE|invoke> <|EPSE|invoke name="read"> <|EPSE|parameter name="file_path"><![CDATA[/Users/wangxilei/work/OutlookRegister/gui/core/task_runner.py]]></|EPSE|parameter> </|EPSE|invoke> <|EPSE|invoke name="read"> <|EPSE|parameter name="file_path"><![CDATA[/Users/wangxilei/work/OutlookRegister/gui/core/log_bus.py]]></|EPSE|parameter> </|EPSE|invoke> </|EPSE>`

	calls := ParseToolCalls(input, []string{"read"})
	if len(calls) != 4 {
		t.Fatalf("expected 4 tool calls, got %d: %+v", len(calls), calls)
	}
	for i, call := range calls {
		if call.Name != "read" {
			t.Errorf("call %d: expected name 'read', got %q", i, call.Name)
		}
		fp, ok := call.Input["file_path"].(string)
		if !ok {
			t.Errorf("call %d: expected file_path string, got %+v", i, call.Input)
			continue
		}
		if fp == "" {
			t.Errorf("call %d: file_path is empty", i)
		}
	}
}

// TestParseMixedEPSEAndXML verifies that a tool_calls block mixing EPSE and
// plain XML tag styles is parsed correctly.
func TestParseMixedEPSEAndXML(t *testing.T) {
	input := `<|EPSE|tool_calls> <|EPSE|invoke name="read"> <|EPSE|parameter name="file_path"><![CDATA[/a.py]]></|EPSE> </|EPSE> <invoke name="read"> <|EPSE|parameter name="file_path"><![CDATA[/b.py]]></|EPSE> </invoke> </tool_calls>`

	calls := ParseToolCalls(input, []string{"read"})
	// At minimum, the XML-style invokes should parse.
	if len(calls) == 0 {
		t.Fatalf("expected at least 1 tool call, got 0")
	}
	t.Logf("parsed %d calls: %+v", len(calls), calls)
}
