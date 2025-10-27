# Tasks: remove-python-bridge-and-claude-agent-sdk

1. 更新规范（REMOVED）
   - [ ] 对 `python-agent-bridge-routing` 标记移除全部要求与场景
   - [ ] 在相关变更档案中添加交叉引用，声明被移除
2. 配置与文档
   - [ ] 从配置结构与样例中移除 `claude-agent-sdk-for-python` 键与注释
   - [ ] 清理 README/README_CN 中 Bridge/Claude Agent SDK 描述
3. 代码实现
   - [ ] 删除 `ensureClaudePythonBridge`、`StartPythonBridge` 等函数及调用
   - [ ] 移除 `CLAUDE_AGENT_SDK_URL`、`CLAUDE_AGENT_SDK_ALLOW_REMOTE` 等环境变量引用
   - [ ] ZhipuExecutor 统一直连上游（OpenAI 兼容）并保留已存在的流式分片/emoji 处理
   - [ ] Service 启动流程移除 Bridge 启停逻辑
4. 测试
   - [ ] 删除/重写 Bridge 相关单元测试与集成测试
   - [ ] 保留 zhipu 直连路径的非流/流式覆盖
5. 校验与验收
   - [ ] `openspec validate remove-python-bridge-and-claude-agent-sdk --strict`
   - [ ] 本地最小测试子集通过

