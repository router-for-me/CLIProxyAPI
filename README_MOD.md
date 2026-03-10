# CLIProxyAPI 开发维护文档 (README_MOD)

## 1. 仓库与分支说明

- **主仓库 (Upstream)**: `git@github.com:router-for-me/CLIProxyAPI.git`
- **个人开发仓库 (Origin)**: `git@github.com:xx299x/CLIProxyAPI.git`
- **当前开发分支**: `feat/refactor-auth-logic` (基于工作区变更创建)

## 2. 同步与合并流程

为了在 `upstream` 更新时保持本地代码同步，请遵循以下步骤：

### 2.1 获取上游更新
首先确保已配置上游仓库：
```powershell
git remote add upstream git@github.com:router-for-me/CLIProxyAPI.git
git fetch upstream
```

### 2.2 合并上游变更
当你想要将 `upstream` 的 `main` 分支（或其他指定分支）合并到当前开发分支时：
```powershell
# 确保在开发分支上
git checkout feat/refactor-auth-logic

# 合并上游 main 分支
git merge upstream/main
```

### 2.3 处理冲突
如果合并过程中出现冲突：
1. 使用编辑器解决冲突文件。
2. 标记冲突已解决：`git add <file>`。
3. 完成合并：`git commit`。

### 2.4 推送到个人仓库
```powershell
git push origin feat/refactor-auth-logic
```

## 3. VS Code 编译与调试

已为您配置好 VS Code 快捷操作：

- **一键编译**: 按下 `Ctrl + Shift + B`，选择 `go: build (server)`。这将在根目录生成 `server.exe`。
- **本地调试**: 按下 `F5`，将直接启动 `cmd/server` 进行调试。
- **任务面板**: 您也可以通过 `终端 (Terminal) -> 运行任务 (Run Task)` 找到配置好的编译选项。

## 4. 脚本自动化建议 (可选)

建议将以下内容保存为 `sync_upstream.ps1` 以便快速执行：
```powershell
git fetch upstream
git merge upstream/main
```

---
*本文档由 Antigravity 助手根据用户要求生成，用于记录分支合并与同步逻辑。*
