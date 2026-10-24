# 子代理取消持久化与 turn 公平调度

子代理 pending 原先只在 agent.json 中保存。stopTask 清空内存队列后忽略持久化错误，导致调用成功但旧 pending 仍在磁盘；之后崩溃恢复可能执行本应丢弃的任务。关闭和完成交付认领也没有向调用者返回写入失败。

取消现在先通过 internal/state 写入 versioned agent-stop.json，记录取消序号、pending request ID 与被中断代次，再更新 agent.json。恢复重放尚未合并的取消意图；agent.json 单独写失败不会复活已取消任务。失败会投影为 persistence_error 并阻断自动续跑。若意图与元数据都无法写入，则仅能保证当前进程 fail closed，操作明确返回错误；显式重试保留原取消 ID，直到可靠落盘。关闭和完成认领错误也必须返回。测试以注入的 state writer 制造单文件及全部写失败，并验证重启过滤和重试行为。

子树中断原先同步等待 runner 与 spawn reservation，调用者 context 只能在入口生效。StopSubtreeContext 将观察和清理责任分开：后台操作拥有 admission fence 和完整 drain，调用者取消只结束观察，后续 waiter 共享同一清理屏障。Controller 关闭也等待这些后台清理。context 不代表可以强制结束不响应取消的 runner。

原调度每次容量释放都扫描全部任务，并允许刚结束的 agent 立即启动自己的下一轮；持续 pending 可抢在已有等待者之前。现在每 root 持有 ready FIFO，一轮结束后重新排到队尾，容量检查仍在原 execution 锁内完成。调度不再扫描其他 root 或历史任务。确定性测试验证 A1/B1/A2/B2/A3 顺序；原有 per-root capacity、代次、恢复和 admission fence 测试继续覆盖原合同。
