# SDK Watcher集成说明

本文档介绍SDK服务与文件监控器之间的增量更新队列，包括接口契约、高频变更下的处理策略以及接入步骤。

## 更新队列契约

- `watcher.AuthUpdate`描述单条凭据变更，`Action`可能为`add`、`modify`或`delete`，`ID`是凭据标识。对于`add`/`modify`会携带完整的`Auth`克隆，`delete`可以省略`Auth`。
- `WatcherWrapper.SetAuthUpdateQueue(chan<- watcher.AuthUpdate)`用于将服务侧创建的队列注入watcher，必须在watcher启动前完成。
- 服务通过`ensureAuthUpdateQueue`创建容量为256的缓冲通道，并在`consumeAuthUpdates`中使用专职goroutine消费；消费侧会主动“抽干”积压事件，降低切换开销。

## Watcher行为

- `internal/watcher/watcher.go`维护`currentAuths`快照，文件或配置事件触发后会重建快照并与旧快照对比，生成最小化的`AuthUpdate`列表。
- 以凭据ID为维度对更新进行合并，同一凭据在短时间内的多次变更只会保留最新状态（例如先写后删只会下发`delete`）。
- watcher内部运行异步分发循环：生产者只向内存缓冲追加事件并唤醒分发协程，即使通道暂时写满也不会阻塞文件事件线程。watcher停止时会取消分发循环，确保协程正常退出。

## 高频变更处理

- 分发循环与服务消费协程相互独立，因此即便短时间内出现大量变更也不会阻塞watcher事件处理。
- 背压通过两级缓冲吸收：
  - 分发缓冲（map + 顺序切片）会合并同一凭据的重复事件，直到消费者完成处理。
  - 服务端通道的256容量加上消费侧的“抽干”逻辑，可平稳处理多个突发批次。
- 当通道长时间处于高压状态时，缓冲仍持续合并事件，从而在消费者恢复后一次性应用最新状态，避免重复处理无意义的中间状态。

## 接入步骤

1. 实例化SDK Service（构建器或手工创建）。
2. 在启动watcher之前调用`ensureAuthUpdateQueue`创建共享通道。
3. watcher通过工厂函数创建后立刻调用`SetAuthUpdateQueue`注入通道，然后再启动watcher。
4. Reload回调专注于配置更新；认证增量会通过队列送达，并由`handleAuthUpdate`自动应用。

遵循上述流程即可在避免全量重载的同时保持凭据变更的实时性。
