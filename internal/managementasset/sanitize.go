package managementasset

import "strings"

var managementHTMLSponsorReplacer = strings.NewReplacer(
	"Vx=[`gemini`,`codex`,`claude`,`vertex`,`openaiCompatibility`,`apikeyFun`,`claudeApi`,`code0`]",
	"Vx=[`gemini`,`codex`,`claude`,`openaiCompatibility`]",
	",...T?[]:[{label:e(`dashboard.quick_start_card`),value:e(`dashboard.quick_start_entry`),icon:(0,R.jsx)(zs,{size:24}),path:`/quick-start`,sublabel:e(`dashboard.quick_start_entry_desc`)}]",
	"",
	"{path:`/quick-start`,element:(0,R.jsx)(aC,{fixedBrand:`apikeyFun`})},{path:`/quick-start/*`,element:(0,R.jsx)(Vr,{to:`/quick-start`,replace:!0})}",
	"{path:`/quick-start`,element:(0,R.jsx)(Vr,{to:`/`,replace:!0})},{path:`/quick-start/*`,element:(0,R.jsx)(Vr,{to:`/`,replace:!0})}",
	"ce={path:`/quick-start`,label:se?pv:void 0,labelKey:se?void 0:`nav.quick_start`,metaKey:`nav_meta.quick_start`,icon:d2.quickStart}",
	"ce=null",
	",...se?[]:[ce]",
	"",
	",...se?[ce]:[]",
	"",
	",claudeApi:`ClaudeAPI`",
	"",
	",vertex:`Vertex`",
	"",
	"filter_vertex:`Vertex`,",
	"",
	"filter_vertex:`Vertex`;",
	"",
	"type_vertex:`Vertex`,",
	"",
	"type_vertex:`Vertex`;",
	"",
	",apikeyFun:`APIKEY.FUN`",
	"",
	",code0:`Code0`",
	"",
	",\"claudeApi\":\"ClaudeAPI\"",
	"",
	",\"vertex\":\"Vertex\"",
	"",
	",\"apikeyFun\":\"APIKEY.FUN\"",
	"",
	",\"code0\":\"Code0\"",
	"",
	"fv=`apikeyFun`",
	"fv=``",
	"pv=`APIKEY.FUN`",
	"pv=``",
	"ib=`ClaudeAPI`",
	"ib=``",
	"lb=`code0`",
	"lb=``",
	"ub=`Code0`",
	"ub=``",
	"https://apikey.fun/register?aff=AKCPA",
	"",
	"https://apikey.fun/dashboard",
	"",
	"https://api.apikey.fun",
	"",
	"https://slb.apikey.fun",
	"",
	"https://gw.claudeapi.com",
	"",
	"https://console.claudeapi.com/agent/register/pJq9T52Fpugrhpgo",
	"",
	"https://code0.ai/agent/register/slxVMR3uVBoRgNBf",
	"",
	"https://code0.ai",
	"",
	"APIKEY.FUN",
	"",
	"ClaudeAPI",
	"",
	"Code0",
	"",
)

// SanitizeManagementHTML strips known sponsored-provider entries from cached management bundles.
func SanitizeManagementHTML(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	sanitized := managementHTMLSponsorReplacer.Replace(string(data))
	sanitized = removeWordFromJSStringLiterals(sanitized, "Vertex")
	return []byte(sanitized)
}

func removeWordFromJSStringLiterals(input, word string) string {
	if input == "" || word == "" || !strings.Contains(input, word) {
		return input
	}

	var out strings.Builder
	out.Grow(len(input))

	quote := byte(0)
	escaped := false
	templateExprDepth := 0

	for i := 0; i < len(input); {
		ch := input[i]
		if quote == 0 {
			out.WriteByte(ch)
			if ch == '\'' || ch == '"' || ch == '`' {
				quote = ch
				escaped = false
				templateExprDepth = 0
			}
			i++
			continue
		}

		if escaped {
			out.WriteByte(ch)
			escaped = false
			i++
			continue
		}
		if ch == '\\' {
			out.WriteByte(ch)
			escaped = true
			i++
			continue
		}

		if quote == '`' && templateExprDepth > 0 {
			out.WriteByte(ch)
			switch ch {
			case '{':
				templateExprDepth++
			case '}':
				templateExprDepth--
			}
			i++
			continue
		}

		if quote == '`' && i+1 < len(input) && ch == '$' && input[i+1] == '{' {
			out.WriteString("${")
			templateExprDepth = 1
			i += 2
			continue
		}

		if ch == quote {
			out.WriteByte(ch)
			quote = 0
			i++
			continue
		}

		if strings.HasPrefix(input[i:], word) {
			i += len(word)
			continue
		}

		out.WriteByte(ch)
		i++
	}

	return out.String()
}
