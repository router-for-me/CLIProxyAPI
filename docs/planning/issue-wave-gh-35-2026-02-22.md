# CLIProxyAPIPlus Issue Wave (35 items, 7 lanes)

Date: 2026-02-22  
Repo: `router-for-me/CLIProxyAPIPlus`  
Execution model: 6 child agents + 1 local lane (you), 5 issues per lane, worktree-isolated

## Branch and worktree mapping
- Lane 1 (self): `workstream-cpb-1` -> `../cliproxyapi-plusplus-worktree-1`
- Lane 2 (agent): `workstream-cpb-2` -> `../cliproxyapi-plusplus-worktree-2`
- Lane 3 (agent): `workstream-cpb-3` -> `../cliproxyapi-plusplus-worktree-3`
- Lane 4 (agent): `workstream-cpb-4` -> `../cliproxyapi-plusplus-worktree-4`
- Lane 5 (agent): `workstream-cpb-5` -> `../cliproxyapi-plusplus-worktree-5`
- Lane 6 (agent): `workstream-cpb-6` -> `../cliproxyapi-plusplus-worktree-6`
- Lane 7 (agent): `workstream-cpb-7` -> `../cliproxyapi-plusplus-worktree-7`

## Lane assignments

### Lane 1 (self)
- #258 Support `variant` parameter as fallback for `reasoning_effort` in codex models
- #254 请求添加新功能：支持对Orchids的反代
- #253 Codex support
- #251 Bug thinking
- #246 fix(cline): add grantType to token refresh and extension headers

### Lane 2 (agent)
- #245 fix(cline): add grantType to token refresh and extension headers
- #241 context length for models registered from github-copilot should always be 128K
- #232 Add AMP auth as Kiro
- #221 kiro账号被封
- #219 Opus 4.6

### Lane 3 (agent)
- #213 Add support for proxying models from kilocode CLI
- #210 [Bug] Kiro 与 Ampcode 的 Bash 工具参数不兼容
- #206 bug: Nullable type arrays in tool schemas cause 400 error on Antigravity/Droid Factory
- #201 failed to save config: open /CLIProxyAPI/config.yaml: read-only file system
- #200 gemini能不能设置配额,自动禁用 ,自动启用?

### Lane 4 (agent)
- #198 Cursor CLI \ Auth Support
- #183 why no kiro in dashboard
- #179 OpenAI-MLX-Server and vLLM-MLX Support?
- #178 Claude thought_signature forwarded to Gemini causes Base64 decode error
- #177 Kiro Token 导入失败: Refresh token is required

### Lane 5 (agent)
- #169 Kimi Code support
- #165 kiro如何看配额？
- #163 fix(kiro): handle empty content in messages to prevent Bad Request errors
- #158 在配置文件中支持为所有 OAuth 渠道自定义上游 URL
- #160 kiro反代出现重复输出的情况

### Lane 6 (agent)
- #149 kiro IDC 刷新 token 失败
- #147 请求docker部署支持arm架构的机器！感谢。
- #146 [Feature Request] 请求增加 Kiro 配额的展示功能
- #145 [Bug]进一步完善 openai兼容模式对 claude 模型的支持（完善 协议格式转换 ）
- #136 kiro idc登录需要手动刷新状态

### Lane 7 (agent)
- #133 Routing strategy "fill-first" is not working as expected
- #129 CLIProxyApiPlus不支持像CLIProxyApi一样使用ClawCloud云部署吗？
- #125 Error 403
- #115 -kiro-aws-login 登录后一直封号
- #111 Antigravity authentication failed

## Lane output contract
- Create `docs/planning/reports/issue-wave-gh-35-lane-<n>.md`.
- For each assigned issue: classify as `fix`, `feature`, `question`, or `external`.
- If code changes are made:
  - include touched files,
  - include exact test command(s) and results,
  - include follow-up risk/open points.
- Keep scope to lane assignment only; ignore unrelated local changes.
