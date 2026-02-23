# CLIProxyAPIPlus Issue Wave (21 items, 7 lanes x 3)

Date: 2026-02-22  
Execution model: 6 child agents + 1 local lane (you)  
Lane size: 3 items each  
Scope: current upstream open issues/PRs with highest execution value

## Lane 1 (you) - Codex/Reasoning Core
- #259 PR: Normalize Codex schema handling
- #253: Codex support
- #251: Bug thinking

## Lane 2 (agent) - OAuth/Auth Reliability
- #246: fix(cline): add grantType to token refresh and extension headers
- #245: fix(cline): add grantType to token refresh and extension headers
- #177: Kiro Token 导入失败: Refresh token is required

## Lane 3 (agent) - Cursor/Kiro UX Paths
- #198: Cursor CLI / Auth Support
- #183: why no kiro in dashboard
- #165: kiro如何看配额？

## Lane 4 (agent) - Provider Model Expansion
- #219: Opus 4.6
- #213: Add support for proxying models from kilocode CLI
- #169: Kimi Code support

## Lane 5 (agent) - Config/Platform Ops
- #201: failed to save config: open /CLIProxyAPI/config.yaml: read-only file system
- #158: 在配置文件中支持为所有 OAuth 渠道自定义上游 URL
- #160: kiro反代出现重复输出的情况

## Lane 6 (agent) - Routing/Translation Correctness
- #178: Claude thought_signature forwarded to Gemini causes Base64 decode error
- #163: fix(kiro): handle empty content in messages to prevent Bad Request errors
- #179: OpenAI-MLX-Server and vLLM-MLX Support?

## Lane 7 (agent) - Product/Feature Frontier
- #254: 请求添加新功能：支持对Orchids的反代
- #221: kiro账号被封
- #200: gemini能不能设置配额,自动禁用 ,自动启用?

## Execution Rules
- Use one worktree per lane branch; no stash-based juggling.
- Each lane produces one report: `docs/planning/reports/issue-wave-gh-next21-lane-<n>.md`.
- For each item: include status (`done`/`partial`/`blocked`), commit hash(es), and remaining gaps.
- If item already implemented, add evidence and close-out instructions.

## Suggested Branch Names
- `wave-gh-next21-lane-1`
- `wave-gh-next21-lane-2`
- `wave-gh-next21-lane-3`
- `wave-gh-next21-lane-4`
- `wave-gh-next21-lane-5`
- `wave-gh-next21-lane-6`
- `wave-gh-next21-lane-7`
