## 1. Specification & Validation
- [x] 1.1 编写 delta specs（provider-integration、auth）
- [x] 1.2 运行 `openspec validate add-copilot-auth-provider --strict` 并修复

## 2. Provider 注册与模型可见性
- [x] 2.1 在 provider registry 中新增 `copilot` 类型（仅注册，不默认启用）
- [x] 2.2 `/v0/management/providers` 返回包含 `copilot`
- [x] 2.3 `/v0/management/models?provider=copilot` 可列举（初期映射 OpenAI 集合，后续替换）

## 3. 登录流程（管理端）
- [x] 3.1 新增 `GET /v0/management/copilot-device-code` 返回 `device_code` 等信息
- [x] 3.2 后台轮询 GitHub/PAT 与 Copilot Token 并落盘
- [x] 3.3 新增 `GET /v0/management/copilot-device-status` 返回 `wait|ok|error`
- [x] 3.4 保留 `GET /v0/management/copilot-auth-url` 与 `/copilot/callback` 回调式路径
- [x] 3.5 令牌写入 `auth-dir`，与现有文件管理 API 兼容

## 4. 执行器与路由
- [x] 4.1 在执行路径中增加 `copilot` 分支（当前复用 Codex 执行器）
- [x] 4.2 `/v1/models` 展示包含 provider=`copilot` 的模型元数据（当前映射 OpenAI 集合）

## 5. CLI 参数与交互
- [ ] 5.1 新增 `--copilot-auth-login`（别名 `--copilot-login`）：
  - 默认 Device Flow：打印 `user_code/verification_uri`，自动轮询，完成后提示保存路径
  - 回调式：支持 `--no-browser` 打印授权 URL，不自动打开浏览器
  - 兼容日志与退出码（失败返回非零，stderr 输出错误）

## 6. 文档与归档
- [x] 5.1 最小子集测试（providers/models、auth-url、callback、device-code/status、/v1/models 可见性）
- [x] 5.2 文档与 README 增补 Device Flow 与回调说明
- [ ] 5.3 `openspec archive add-copilot-auth-provider --yes`
