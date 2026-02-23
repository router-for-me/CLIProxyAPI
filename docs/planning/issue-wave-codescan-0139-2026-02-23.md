# Code Scanning 139-Item Remediation Worklog (Phased WBS)

**Date:** 2026-02-23
**Source:** `https://github.com/KooshaPari/cliproxyapi-plusplus/security/code-scanning`
**Scope:** 139 open code-scanning alerts, each mapped to one canonical GitHub issue.

## Inventory Snapshot

- Total tracked issues: **139**
- Severity: **critical=7**, **high=126**, **medium=6**
- Rules:
  - `go/clear-text-logging`: **61**
  - `go/path-injection`: **54**
  - `go/weak-sensitive-data-hashing`: **8**
  - `go/request-forgery`: **6**
  - `go/reflected-xss`: **4**
  - `go/allocation-size-overflow`: **3**
  - `go/bad-redirect-check`: **1**
  - `go/unsafe-quoting`: **1**
  - `go/unvalidated-url-redirection`: **1**

## Phased WBS

| Phase | Task ID | Deliverable | Issue Group | Count | Depends On | ETA (agent runtime) |
|---|---|---|---|---:|---|---|
| P0 | CS-00 | Baseline + guardrails (tests, secure defaults, banlist assertions) | all | 139 | - | 8 min |
| P1 | CS-01 | Critical SSRF/redirect fixes + regression tests | `go/request-forgery`, `go/unvalidated-url-redirection`, `go/bad-redirect-check` | 8 | CS-00 | 12 min |
| P2 | CS-02 | Path traversal/injection hardening + canonical path validation | `go/path-injection` | 54 | CS-01 | 35 min |
| P3 | CS-03 | Sensitive logging redaction and structured-safe logging | `go/clear-text-logging` | 61 | CS-00 | 40 min |
| P4 | CS-04 | Hashing upgrades and crypto migration tests | `go/weak-sensitive-data-hashing` | 8 | CS-00 | 15 min |
| P5 | CS-05 | XSS/output encoding fixes | `go/reflected-xss` | 4 | CS-00 | 10 min |
| P6 | CS-06 | Overflow and unsafe quoting edge-case protections | `go/allocation-size-overflow`, `go/unsafe-quoting` | 4 | CS-02 | 10 min |
| P7 | CS-07 | Closure sweep: close/verify alerts, update docs + changelog + status board | all | 139 | CS-01, CS-02, CS-03, CS-04, CS-05, CS-06 | 15 min |

## DAG (Dependencies)

- `CS-00 -> CS-01`
- `CS-00 -> CS-03`
- `CS-00 -> CS-04`
- `CS-00 -> CS-05`
- `CS-01 -> CS-02`
- `CS-02 -> CS-06`
- `CS-01, CS-02, CS-03, CS-04, CS-05, CS-06 -> CS-07`

## Execution Lanes (7x parallel)

| Lane | Primary Task IDs | Issue Focus | Target Count |
|---|---|---|---:|
| L1 | CS-01 | request-forgery + redirect checks | 8 |
| L2 | CS-02A | path-injection (batch A) | 18 |
| L3 | CS-02B | path-injection (batch B) | 18 |
| L4 | CS-02C | path-injection (batch C) | 18 |
| L5 | CS-03A | clear-text-logging (batch A) | 30 |
| L6 | CS-03B + CS-04 | clear-text-logging (batch B) + weak-hash | 39 |
| L7 | CS-05 + CS-06 + CS-07 | reflected-xss + overflow + unsafe-quoting + closure | 8 + closure |

## Complete Rule-to-Issue Worklog Map

Format: `issue#(alert#): path:line`

### go/clear-text-logging (61)

- #187(A1): `pkg/llmproxy/api/middleware/response_writer.go:416`
- #185(A2): `pkg/llmproxy/api/server.go:1425`
- #183(A3): `pkg/llmproxy/api/server.go:1426`
- #181(A4): `pkg/llmproxy/cmd/iflow_cookie.go:74`
- #179(A5): `pkg/llmproxy/executor/antigravity_executor.go:216`
- #177(A6): `pkg/llmproxy/executor/antigravity_executor.go:370`
- #175(A7): `pkg/llmproxy/executor/antigravity_executor.go:761`
- #173(A8): `pkg/llmproxy/executor/gemini_cli_executor.go:239`
- #172(A9): `pkg/llmproxy/executor/codex_websockets_executor.go:402`
- #171(A10): `pkg/llmproxy/executor/gemini_cli_executor.go:376`
- #169(A11): `pkg/llmproxy/executor/codex_websockets_executor.go:1298`
- #167(A12): `pkg/llmproxy/executor/codex_websockets_executor.go:1303`
- #165(A13): `pkg/llmproxy/executor/codex_websockets_executor.go:1303`
- #163(A14): `pkg/llmproxy/executor/codex_websockets_executor.go:1306`
- #161(A15): `pkg/llmproxy/executor/iflow_executor.go:414`
- #159(A16): `pkg/llmproxy/executor/iflow_executor.go:439`
- #157(A17): `pkg/llmproxy/executor/kiro_executor.go:1648`
- #155(A18): `pkg/llmproxy/executor/kiro_executor.go:1656`
- #153(A19): `pkg/llmproxy/executor/kiro_executor.go:1660`
- #151(A20): `pkg/llmproxy/executor/kiro_executor.go:1664`
- #149(A21): `pkg/llmproxy/executor/kiro_executor.go:1668`
- #148(A22): `pkg/llmproxy/executor/kiro_executor.go:1675`
- #147(A23): `pkg/llmproxy/executor/kiro_executor.go:1678`
- #146(A24): `pkg/llmproxy/executor/kiro_executor.go:1683`
- #145(A25): `pkg/llmproxy/registry/model_registry.go:605`
- #144(A26): `pkg/llmproxy/registry/model_registry.go:648`
- #143(A27): `pkg/llmproxy/registry/model_registry.go:650`
- #142(A28): `pkg/llmproxy/registry/model_registry.go:674`
- #141(A29): `pkg/llmproxy/runtime/executor/codex_websockets_executor.go:402`
- #140(A30): `pkg/llmproxy/runtime/executor/codex_websockets_executor.go:1298`
- #139(A31): `pkg/llmproxy/runtime/executor/codex_websockets_executor.go:1303`
- #138(A32): `pkg/llmproxy/runtime/executor/codex_websockets_executor.go:1303`
- #137(A33): `pkg/llmproxy/runtime/executor/codex_websockets_executor.go:1306`
- #136(A34): `pkg/llmproxy/runtime/executor/iflow_executor.go:414`
- #135(A35): `pkg/llmproxy/runtime/executor/iflow_executor.go:439`
- #134(A36): `pkg/llmproxy/thinking/apply.go:101`
- #133(A37): `pkg/llmproxy/thinking/apply.go:123`
- #132(A38): `pkg/llmproxy/thinking/apply.go:129`
- #131(A39): `pkg/llmproxy/thinking/apply.go:140`
- #130(A40): `pkg/llmproxy/thinking/apply.go:150`
- #128(A41): `pkg/llmproxy/thinking/apply.go:161`
- #126(A42): `pkg/llmproxy/thinking/apply.go:171`
- #124(A43): `pkg/llmproxy/thinking/apply.go:184`
- #122(A44): `pkg/llmproxy/thinking/apply.go:191`
- #120(A45): `pkg/llmproxy/thinking/apply.go:236`
- #118(A46): `pkg/llmproxy/thinking/apply.go:264`
- #116(A47): `pkg/llmproxy/thinking/apply.go:273`
- #114(A48): `pkg/llmproxy/thinking/apply.go:280`
- #112(A49): `pkg/llmproxy/thinking/validate.go:173`
- #110(A50): `pkg/llmproxy/thinking/validate.go:194`
- #106(A51): `pkg/llmproxy/thinking/validate.go:240`
- #105(A52): `pkg/llmproxy/thinking/validate.go:272`
- #102(A53): `pkg/llmproxy/thinking/validate.go:370`
- #100(A54): `pkg/llmproxy/watcher/clients.go:60`
- #98(A55): `pkg/llmproxy/watcher/clients.go:115`
- #96(A56): `pkg/llmproxy/watcher/clients.go:116`
- #94(A57): `pkg/llmproxy/watcher/clients.go:117`
- #92(A58): `pkg/llmproxy/watcher/config_reload.go:122`
- #90(A59): `sdk/cliproxy/auth/conductor.go:2171`
- #88(A60): `sdk/cliproxy/auth/conductor.go:2171`
- #86(A61): `sdk/cliproxy/auth/conductor.go:2174`

### go/path-injection (54)

- #68(A72): `pkg/llmproxy/api/handlers/management/auth_files.go:523`
- #67(A73): `pkg/llmproxy/api/handlers/management/auth_files.go:591`
- #66(A74): `pkg/llmproxy/api/handlers/management/auth_files.go:653`
- #65(A75): `pkg/llmproxy/api/handlers/management/auth_files.go:696`
- #64(A76): `pkg/llmproxy/api/handlers/management/oauth_sessions.go:277`
- #63(A77): `pkg/llmproxy/auth/claude/token.go:55`
- #62(A78): `pkg/llmproxy/auth/claude/token.go:60`
- #61(A79): `pkg/llmproxy/auth/codex/token.go:49`
- #60(A80): `pkg/llmproxy/auth/codex/token.go:53`
- #59(A81): `pkg/llmproxy/auth/copilot/token.go:77`
- #58(A82): `pkg/llmproxy/auth/copilot/token.go:81`
- #57(A83): `pkg/llmproxy/auth/gemini/gemini_token.go:52`
- #56(A84): `pkg/llmproxy/auth/gemini/gemini_token.go:56`
- #55(A85): `pkg/llmproxy/auth/iflow/iflow_token.go:30`
- #54(A86): `pkg/llmproxy/auth/iflow/iflow_token.go:34`
- #53(A87): `pkg/llmproxy/auth/kilo/kilo_token.go:37`
- #52(A88): `pkg/llmproxy/auth/kilo/kilo_token.go:41`
- #51(A89): `pkg/llmproxy/auth/kimi/token.go:77`
- #50(A90): `pkg/llmproxy/auth/kimi/token.go:81`
- #49(A91): `pkg/llmproxy/auth/kiro/token.go:43`
- #48(A92): `pkg/llmproxy/auth/kiro/token.go:52`
- #47(A93): `pkg/llmproxy/auth/qwen/qwen_token.go:47`
- #46(A94): `pkg/llmproxy/auth/qwen/qwen_token.go:51`
- #45(A95): `pkg/llmproxy/auth/vertex/vertex_credentials.go:48`
- #44(A96): `pkg/llmproxy/auth/vertex/vertex_credentials.go:51`
- #43(A97): `pkg/llmproxy/logging/request_logger.go:251`
- #42(A98): `pkg/llmproxy/store/gitstore.go:230`
- #41(A99): `pkg/llmproxy/store/gitstore.go:242`
- #40(A100): `pkg/llmproxy/store/gitstore.go:256`
- #39(A101): `pkg/llmproxy/store/gitstore.go:264`
- #38(A102): `pkg/llmproxy/store/gitstore.go:267`
- #37(A103): `pkg/llmproxy/store/gitstore.go:267`
- #36(A104): `pkg/llmproxy/store/gitstore.go:350`
- #35(A105): `pkg/llmproxy/store/objectstore.go:173`
- #34(A106): `pkg/llmproxy/store/objectstore.go:181`
- #33(A107): `pkg/llmproxy/store/objectstore.go:195`
- #32(A108): `pkg/llmproxy/store/objectstore.go:203`
- #31(A109): `pkg/llmproxy/store/objectstore.go:206`
- #30(A110): `pkg/llmproxy/store/objectstore.go:206`
- #29(A111): `pkg/llmproxy/store/postgresstore.go:203`
- #28(A112): `pkg/llmproxy/store/postgresstore.go:211`
- #27(A113): `pkg/llmproxy/store/postgresstore.go:225`
- #26(A114): `pkg/llmproxy/store/postgresstore.go:233`
- #25(A115): `pkg/llmproxy/store/postgresstore.go:236`
- #24(A116): `pkg/llmproxy/store/postgresstore.go:236`
- #23(A117): `pkg/llmproxy/store/objectstore.go:275`
- #22(A118): `pkg/llmproxy/store/postgresstore.go:335`
- #21(A119): `pkg/llmproxy/store/postgresstore.go:493`
- #20(A120): `sdk/auth/filestore.go:55`
- #19(A121): `sdk/auth/filestore.go:63`
- #18(A122): `sdk/auth/filestore.go:78`
- #17(A123): `sdk/auth/filestore.go:82`
- #16(A124): `sdk/auth/filestore.go:97`
- #15(A125): `sdk/auth/filestore.go:158`

### go/weak-sensitive-data-hashing (8)

- #14(A126): `pkg/llmproxy/auth/diff/models_summary.go:116`
- #13(A127): `pkg/llmproxy/auth/diff/openai_compat.go:181`
- #12(A128): `pkg/llmproxy/auth/synthesizer/helpers.go:38`
- #11(A129): `pkg/llmproxy/executor/user_id_cache.go:48`
- #10(A130): `pkg/llmproxy/watcher/diff/models_summary.go:116`
- #9(A131): `pkg/llmproxy/watcher/diff/openai_compat.go:181`
- #8(A132): `pkg/llmproxy/watcher/synthesizer/helpers.go:38`
- #7(A133): `sdk/cliproxy/auth/types.go:135`

### go/request-forgery (6)

- #6(A134): `pkg/llmproxy/api/handlers/management/api_tools.go:233`
- #5(A135): `pkg/llmproxy/api/handlers/management/api_tools.go:1204`
- #4(A136): `pkg/llmproxy/auth/kiro/sso_oidc.go:208`
- #3(A137): `pkg/llmproxy/auth/kiro/sso_oidc.go:254`
- #2(A138): `pkg/llmproxy/auth/kiro/sso_oidc.go:301`
- #1(A139): `pkg/llmproxy/executor/antigravity_executor.go:941`

### go/reflected-xss (4)

- #74(A67): `pkg/llmproxy/api/middleware/response_writer.go:77`
- #72(A68): `pkg/llmproxy/api/modules/amp/response_rewriter.go:98`
- #71(A69): `pkg/llmproxy/auth/claude/oauth_server.go:253`
- #70(A70): `pkg/llmproxy/auth/codex/oauth_server.go:250`

### go/allocation-size-overflow (3)

- #80(A64): `pkg/llmproxy/config/config.go:1657`
- #78(A65): `pkg/llmproxy/translator/kiro/claude/kiro_websearch.go:414`
- #76(A66): `sdk/api/handlers/handlers.go:476`

### go/bad-redirect-check (1)

- #84(A62): `pkg/llmproxy/api/handlers/management/auth_files.go:246`

### go/unsafe-quoting (1)

- #69(A71): `pkg/llmproxy/api/responses_websocket.go:99`

### go/unvalidated-url-redirection (1)

- #82(A63): `pkg/llmproxy/api/handlers/management/auth_files.go:166`

## Worklog Checklist

- [ ] CS-00 complete with baseline CI gates
- [ ] CS-01 complete and alerts resolved in GitHub
- [ ] CS-02 complete and alerts resolved in GitHub
- [ ] CS-03 complete and alerts resolved in GitHub
- [ ] CS-04 complete and alerts resolved in GitHub
- [ ] CS-05 complete and alerts resolved in GitHub
- [ ] CS-06 complete and alerts resolved in GitHub
- [ ] CS-07 complete (`security/code-scanning` shows zero open alerts for fixed scope)

## Notes

- This worklog is intentionally execution-first and agent-oriented: each task is directly testable and can be closed with command evidence.
- Keep one canonical issue per CodeScanning alert key (`[CodeScanning #N]`) to avoid duplicate closure bookkeeping.
