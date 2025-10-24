## 1. Implementation
- [x] 1.1 登录流程：copilot_login 写入 `refresh_in`（秒）与 `expires_at`（原样，秒或毫秒）到 Metadata；保持 `expired`(RFC3339) 兼容。
- [x] 1.2 Manager：在 `checkRefreshes` 中若 provider=copilot，优先读取 `refresh_in` 与上次获取时间，计算下一次刷新点 (`last_refresh + refresh_in - safety_margin`)；计划到点刷新。
- [x] 1.3 Config：在 SDKConfig 下新增 `copilot.refresh_safety_margin_seconds`，默认 60，校验 5–300。
- [x] 1.4 刷新失败：沿用现有 `refreshFailureBackoff`（5m），写入 `NextRefreshAfter`，避免紧密重试；成功后清理状态并更新 `LastRefreshedAt`。
- [x] 1.5 持久化恢复：重启后读取 `last_refresh`/`expires_at`/`refresh_in` 恢复下一刷新时间。
- [ ] 1.6 单元测试：
  - [x] 登录持久化时写入 refresh_in 与 expires_at 的最小校验
  - [x] copilot 有 refresh_in：应提前刷新（mock 时钟）
  - [x] refresh_in 缺失：回退 ExpirationTime()/RefreshLead 逻辑
  - [x] 刷新失败退避：NextRefreshAfter推进，后续窗口重试
- [ ] 1.7 文档：openspec/specs/auth 与 specs/provider-integration 增补说明（预刷新与字段约定）。

## 2. Rollout
- [ ] 2.1 向下兼容验证：旧凭据无 refresh_in 仍正常
- [ ] 2.2 观察日志：预刷新触发频率与失败率
- [ ] 2.3 若必要，调整默认安全边界为 30–90 秒范围
