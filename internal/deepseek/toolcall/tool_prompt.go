package toolcall

import "strings"

// BuildToolCallInstructions generates the unified tool-calling instruction block
// used by all adapters (OpenAI, Claude, Gemini). It uses attention-optimized
// structure: rules → negative examples → positive examples → anchor.
//
// The toolNames slice should contain the actual tool names available in the
// current request; the function picks real names for examples.
func BuildToolCallInstructions(toolNames []string) string {
	return `工具调用格式规范 — 请严格遵照执行：

<|EPSE|tool_calls>
  <|EPSE|invoke name="TOOL_NAME_HERE">
    <|EPSE|parameter name="PARAMETER_NAME"><![CDATA[PARAMETER_VALUE]]></|EPSE|parameter>
  </|EPSE|invoke>
</|EPSE|tool_calls>

规则说明：
1) 必须使用 <|EPSE|tool_calls> 标签作为整体的封装容器。
2) 允许在一个 <|EPSE|tool_calls> 根节点内部放置一个或多个 <|EPSE|invoke> 调用项。
3) 需在 invoke 元素的 name 属性中明确指定具体工具名称：<|EPSE|invoke name="TOOL_NAME">。
3a) 标签语法中所允许使用的标点符号字符集仅限 ASCII 的 < > / = " 以及半角竖线 |。
4) 所有字符串类型的参数值都必须通过 <![CDATA[...]]> 进行包裹，即使内容极其简短也不例外；这涵盖了代码段、脚本、文件内容、提示词、路径、名称和查询语句等。
5) 每一个顶层参数都必须体现为 <|EPSE|parameter name="ARG_NAME">...</|EPSE|parameter> 节点形式。
6) 对象在参数主体内使用嵌套的 XML 元素。数组可以使用重复的 <item> 子元素。
7) 数字、布尔值和 null 保持为纯文本。
8) 仅使用工具架构(schema)中定义的参数名称。请勿自行创建字段。
9) 使用本次调用所需的实际值填充参数。请勿输出占位符、空参数或仅包含空白字符的参数。
10) 如果所需的参数值未知，请询问用户或正常回答，而不是输出空的工具调用。
11) 对于 Bash / execute_command 等 Shell 工具，命令或脚本必须包含在 command 参数中。切勿在命令为空的情况下调用它们。
12) 请勿使用 Markdown 代码块标记包裹 XML。请勿输出解释、角色标识或内心独白。
13) 如果调用工具，该工具代码块的第一个非空白字符必须严格为 <|EPSE|tool_calls>。
14) 切勿省略起始标签 <|EPSE|tool_calls>，即使你打算随后使用 </|EPSE|tool_calls> 闭合标签。
15) 兼容性说明：运行时环境也接受旧版 XML 标签 <tool_calls> / <invoke> / <parameter>，但建议优先使用上述带有 EPSE 前缀的格式。

参数格式：
- string => <|EPSE|parameter name="x"><![CDATA[value]]></|EPSE|parameter>
- object => <|EPSE|parameter name="x"><field>...</field></|EPSE|parameter>
- array => <|EPSE|parameter name="x"><item>...</item><item>...</item></|EPSE|parameter>
- number/bool/null => <|EPSE|parameter name="x">plain_text</|EPSE|parameter>

【Wrong — 请勿这样操作】:

Wrong 1 — XML 之后包含其他文本:
  <|EPSE|tool_calls>...</|EPSE|tool_calls> I hope this helps.
Wrong 2 — 使用 Markdown 代码块标记:
  ` + "```xml" + `
  <|EPSE|tool_calls>...</|EPSE|tool_calls>
  ` + "```" + `
Wrong 3 — 缺少起始包裹标签:
  <|EPSE|invoke name="TOOL_NAME">...</|EPSE|invoke>
  </|EPSE|tool_calls>
Wrong 4 — 参数为空:
  <|EPSE|tool_calls>
    <|EPSE|invoke name="Bash">
      <|EPSE|parameter name="command"></|EPSE|parameter>
    </|EPSE|invoke>
  </|EPSE|tool_calls>

请记住：使用工具的唯一正确方式是在回复末尾使用 <|EPSE|tool_calls>...</|EPSE|tool_calls> 代码块。
` + buildCorrectToolExamples(toolNames)
}

type promptToolExample struct {
	name   string
	params string
}

func buildCorrectToolExamples(toolNames []string) string {
	names := uniqueToolNames(toolNames)
	examples := make([]string, 0, 4)

	if single, ok := firstBasicExample(names); ok {
		examples = append(examples, "示例 A — 单个工具：\n"+renderToolExampleBlock([]promptToolExample{single}))
	}

	if parallel := firstNBasicExamples(names, 2); len(parallel) >= 2 {
		examples = append(examples, "示例 B — 两个工具并行：\n"+renderToolExampleBlock(parallel))
	}

	if nested, ok := firstNestedExample(names); ok {
		examples = append(examples, "示例 C — 带嵌套 XML 参数的工具：\n"+renderToolExampleBlock([]promptToolExample{nested}))
	}

	if script, ok := firstScriptExample(names); ok {
		examples = append(examples, "示例 D — 使用 CDATA 的长脚本工具（对代码/脚本可靠）：\n"+renderToolExampleBlock([]promptToolExample{script}))
	}

	if len(examples) == 0 {
		return ""
	}
	return "【正确示例】：\n\n" + strings.Join(examples, "\n\n") + "\n\n"
}

func uniqueToolNames(toolNames []string) []string {
	names := make([]string, 0, len(toolNames))
	seen := map[string]bool{}
	for _, name := range toolNames {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func firstBasicExample(names []string) (promptToolExample, bool) {
	for _, name := range names {
		if params, ok := exampleBasicParams(name); ok {
			return promptToolExample{name: name, params: params}, true
		}
	}
	return promptToolExample{}, false
}

func firstNBasicExamples(names []string, count int) []promptToolExample {
	out := make([]promptToolExample, 0, count)
	for _, name := range names {
		if params, ok := exampleBasicParams(name); ok {
			out = append(out, promptToolExample{name: name, params: params})
			if len(out) == count {
				return out
			}
		}
	}
	return out
}

func firstNestedExample(names []string) (promptToolExample, bool) {
	for _, name := range names {
		if params, ok := exampleNestedParams(name); ok {
			return promptToolExample{name: name, params: params}, true
		}
	}
	return promptToolExample{}, false
}

func firstScriptExample(names []string) (promptToolExample, bool) {
	for _, name := range names {
		if params, ok := exampleScriptParams(name); ok {
			return promptToolExample{name: name, params: params}, true
		}
	}
	return promptToolExample{}, false
}

func renderToolExampleBlock(calls []promptToolExample) string {
	var b strings.Builder
	b.WriteString("<|EPSE|tool_calls>\n")
	for _, call := range calls {
		b.WriteString(`  <|EPSE|invoke name="`)
		b.WriteString(call.name)
		b.WriteString(`">` + "\n")
		b.WriteString(indentPromptParameters(call.params, "    "))
		b.WriteString("\n  </|EPSE|invoke>\n")
	}
	b.WriteString("</|EPSE|tool_calls>")
	return b.String()
}

func indentPromptParameters(body, indent string) string {
	if strings.TrimSpace(body) == "" {
		return indent + `<|EPSE|parameter name="content"></|EPSE|parameter>`
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = line
			continue
		}
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

func wrapParameter(name, inner string) string {
	return `<|EPSE|parameter name="` + name + `">` + inner + `</|EPSE|parameter>`
}

func exampleBasicParams(name string) (string, bool) {
	switch strings.TrimSpace(name) {
	case "Read":
		return wrapParameter("file_path", promptCDATA("README.md")), true
	case "Glob":
		return wrapParameter("pattern", promptCDATA("**/*.go")) + "\n" + wrapParameter("path", promptCDATA(".")), true
	case "read_file":
		return wrapParameter("path", promptCDATA("src/main.go")), true
	case "list_files":
		return wrapParameter("path", promptCDATA(".")), true
	case "search_files":
		return wrapParameter("query", promptCDATA("tool call parser")), true
	case "Bash", "execute_command":
		return wrapParameter("command", promptCDATA("pwd")), true
	case "exec_command":
		return wrapParameter("cmd", promptCDATA("pwd")), true
	case "Write":
		return wrapParameter("file_path", promptCDATA("notes.txt")) + "\n" + wrapParameter("content", promptCDATA("Hello world")), true
	case "write_to_file":
		return wrapParameter("path", promptCDATA("notes.txt")) + "\n" + wrapParameter("content", promptCDATA("Hello world")), true
	case "Edit":
		return wrapParameter("file_path", promptCDATA("README.md")) + "\n" + wrapParameter("old_string", promptCDATA("foo")) + "\n" + wrapParameter("new_string", promptCDATA("bar")), true
	case "MultiEdit":
		return wrapParameter("file_path", promptCDATA("README.md")) + "\n" + `<|EPSE|parameter name="edits"><item><old_string>` + promptCDATA("foo") + `</old_string><new_string>` + promptCDATA("bar") + `</new_string></item></|EPSE|parameter>`, true
	}
	return "", false
}

func exampleNestedParams(name string) (string, bool) {
	switch strings.TrimSpace(name) {
	case "MultiEdit":
		return wrapParameter("file_path", promptCDATA("README.md")) + "\n" + `<|EPSE|parameter name="edits"><item><old_string>` + promptCDATA("foo") + `</old_string><new_string>` + promptCDATA("bar") + `</new_string></item></|EPSE|parameter>`, true
	case "Task":
		return wrapParameter("description", promptCDATA("Investigate flaky tests")) + "\n" + wrapParameter("prompt", promptCDATA("Run targeted tests and summarize failures")), true
	case "ask_followup_question":
		return wrapParameter("question", promptCDATA("Which approach do you prefer?")) + "\n" + `<|EPSE|parameter name="follow_up"><item><text>` + promptCDATA("Option A") + `</text></item><item><text>` + promptCDATA("Option B") + `</text></item></|EPSE|parameter>`, true
	}
	return "", false
}

func exampleScriptParams(name string) (string, bool) {
	scriptCommand := `cat > /tmp/test_escape.sh <<'EOF'
#!/bin/bash
echo 'single "double"'
echo "literal dollar: \$HOME"
EOF
bash /tmp/test_escape.sh`
	scriptContent := `#!/bin/bash
echo 'single "double"'
echo "literal dollar: $HOME"`

	switch strings.TrimSpace(name) {
	case "Bash":
		return wrapParameter("command", promptCDATA(scriptCommand)) + "\n" + wrapParameter("description", promptCDATA("Test shell escaping")), true
	case "execute_command":
		return wrapParameter("command", promptCDATA(scriptCommand)), true
	case "exec_command":
		return wrapParameter("cmd", promptCDATA(scriptCommand)), true
	case "Write":
		return wrapParameter("file_path", promptCDATA("test_escape.sh")) + "\n" + wrapParameter("content", promptCDATA(scriptContent)), true
	case "write_to_file":
		return wrapParameter("path", promptCDATA("test_escape.sh")) + "\n" + wrapParameter("content", promptCDATA(scriptContent)), true
	}
	return "", false
}

func promptCDATA(text string) string {
	if text == "" {
		return ""
	}
	if strings.Contains(text, "]]>") {
		return "<![CDATA[" + strings.ReplaceAll(text, "]]>", "]]]]><![CDATA[>") + "]]>"
	}
	return "<![CDATA[" + text + "]]>"
}
