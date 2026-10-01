# 主进程长期持有完整历史和完成回放

日期：2026-10-01  
范围：`internal/session`、`internal/server`、`internal/cli`、WebUI 恢复测试  
状态：P0 / P1 已实现；当前 daemon 未重启，测量在独立只读诊断进程中完成。

## 现象与证据

线上 `./ki serve --addr 0.0.0.0:19800` 主进程 PID 906917 的首次采样 RSS 为 335628KiB（约 328MiB），匿名驻留约 309MiB；后续 RSS 约 309MiB。约 5.9GiB 的 VSZ 是虚拟地址空间，不能当作物理内存消耗。

会话目录共有 255 份 transcript，总计约 1095MiB。最大的 6 份分别为 59.40 / 50.49 / 45.12 / 41.55 / 39.86 / 25.67MiB，合计 262.09MiB，覆盖 38385 条 entry。只读诊断使用 `session.List` 定位目录，按文件大小选取这 6 份，调用实际的 session 读取 API，完成每一阶段后 GC，读取 `runtime.MemStats.HeapAlloc`。没有给线上进程注入 GC、抓取线上 heap snapshot 或重启服务。

原实现的测量：读取 6 个尾部后保留堆约 14.14MiB；读取 6 个完整历史后约 526.91MiB；解除缓存所有权后约 2.04MiB。独立诊断的 heap profile 显示，通用 JSON map 解码占据大量存储，约 195MiB 来自相关分配路径。6 份历史包含 4360 个 request_header，但只有 13 种不同的 system/tools 组合；重复 system 和 tools 的序列化量分别约 53.85MiB 与 88.99MiB。

这些结果证明代码路径具有长期保留大对象图的能力，并解释了高内存的主要机制；它们没有精确分摊线上 daemon 的每一个字节。

## 根因

1. `entriesCaches` 原先是无上限 `sync.Map`。`AllEntries` 将尾部缓存提升成完整历史，之后默认读取也继承这个窗口；冷会话只要访问过，正文就留在进程中。
2. `request_header` 每次解码独立的 system 字符串和 tools 的嵌套 map。相同 schema 的 JSON 字节量低估了 Go 对象图的实际占用。
3. index、精确正文、旧页与 compact 都借助完整 Entry 列表。响应正文虽已分页/折叠，服务端仍会保留整个会话的正文、附件、参数和 checkpoint。
4. 完成的 `runState` 留在 `s.runs`，直到同一 session 下一次运行才替换。已有 transient payload 裁剪解决了慢读者钉住活动流的部分问题，却没有完成回放的全局预算和过期机制。
5. 资源 reload 为定位 cwd 再打开完整 Session，将本来只需 header 的操作也接到了重读取路径。

这延续了 [尾部优先读取](2026-09-19-session-view-tail-first.md) 和 [卡住读者钉住回放](2026-09-27-run-replay-pinned-by-a-stalled-reader.md) 的教训：控制首屏传输和活动流裁剪后，仍要逐一核对对象的所有者及其退出条件。

## 实施结果

| 项目 | 实现及约束 |
|---|---|
| 正文缓存 | 加权 LRU，全局 64MiB、单会话 8MiB、最多 256 个。超大读取正常返回，但不准入；尾部读取缩回近期窗口。 |
| 提示词共享 | system 和原始 tools JSON 以 SHA-256 定位，在当前快照或 Session 生命周期内共享；相同 tools 不重复解码嵌套 map。没有永久全局 intern 表。 |
| 结构索引 | 独立 16MiB / 256 会话预算，保留 parent/role/usage/clock/tool identity、有限预览和 JSONL 行偏移，冷读扫描全部记录，追加只扫描新增完整行。 |
| 正文读取 | index、before、turn、compact、entry/entries 使用结构元数据选条目，按偏移只解码所选正文；不提升完整正文缓存。模型运行及诊断需要的完整历史仍可读取。 |
| 文件一致性 | 校验文件 identity、大小与 mtime；原子替换、截断或改写会重建缓存。旧偏移读取失败，半写入尾行留给下次追加扫描。返回的快照按只读使用，淘汰不修改在途对象。 |
| 完成回放 | 全局 16MiB、2 分钟、最多 256 个 run；单轮超过预算直接不准入。一个定时器在服务空闲时也清理过期所有权。活动 run 与已有 SSE reader 持有的对象继续存活。 |
| 淘汰恢复 | `/events` 无可重放 run 的现存 session 返回 gzip JSON 410 和 canonical recovery URL；缺失 session 返回 404。WebUI 重读 snapshot；CLI 获取当前分支最新人工轮的完整 assistant entries。恢复只用 GET。 |
| 轻量 runtime/reload | `fields=runtime` 不解码 transcript；显式 turn 仍读取该 turn 的正文。资源 reload 只读 header 定位 cwd。 |

缓存权重按对象图估算，包括 slice 容量、字符串、嵌套 map 和共享分配。共享字符串取最大的保留长度；共享 slice 追加暴露出的元素继续计费，循环引用只访问一次。它是保守的准入预算，不是 allocator 或 RSS 指标。活动上下文、在途读取、附着的读者以及 Go 已保留的空闲页都可以使进程占用超过这些预算。

结构索引中的预览复制有限内容，避免 substring 钉住整段正文；归一化扫描达到所需前缀后停止，不为一个预览拆分整段巨大消息。remote checkpoint 的 encrypted payload 仍只在 provider 上下文重建中使用，精确正文接口继续执行原有 redaction。

## 收尾时序修正

验证过程中原有测试暴露了两个时序边界：

- 若 done 先关闭，排队的资源 reload 还没完成，客户端观察到 idle 后启动下一轮可能读到旧资源。
- 若 agent_end 已发送，而 release 正在做较慢的缓存工作，客户端立即压缩/续跑可能收到伪 409 busy。

现在先处理排队资源重载，再在 run 锁内 close(done) → Broadcast；SSE 的 terminal agent_end 等待匹配 done（不持有 run 锁）。完成缓存在 done 之后、extension settled 通知及队列 dispatch 之前登记，避免其它收尾工作阻塞过期计时。完成后的 sideband 不扩充旧回放。淘汰只解除 server 所有权；已有 reader 继续排空，旧期限不会删除同 session 的替代 run。

## 实测对比

最终代码对同一组 6 份真实历史重新测量：

| GC 后保留堆 | 原实现 | 当前实现 |
|---|---:|---:|
| 读取 6 个尾部 | 14.14MiB | 8.13MiB |
| 读取 6 个完整历史 | 526.91MiB | 2.00MiB |
| 再读取独立结构索引 | — | 11.70MiB |
| 再生成尾部及 compact 投影 | — | 12.01MiB |
| 解除所有诊断缓存所有权 | 2.04MiB | 2.06MiB |

完整读取的 6 个解码结果均超过单会话准入上限，读取结束后由 GC 回收。当前实现随后仍保留有界结构索引；包括索引和视图读取在内的保留堆，相对旧实现完整缓存约降低 **97.7%**。这不能等同于线上 RSS 同比例下降。

真实数据验证分别比较了 6 份历史的全树 index、最新 100 条 tail，以及 keep=0/1/3/20 的 compact 输出；全部与完整历史的原有纯投影逐字节 JSON 一致。`HeapSys` 在诊断后仍约 194MiB：Go 分配器保留页和活对象是不同指标；删除所有权能回收活对象，不保证 RSS 立即贴着 HeapAlloc。

## 验证

- `cd web && bun run typecheck && bun run build`：通过，先构建新 dist 再使用 embed 二进制。
- `go test ./internal/session ./internal/server ./internal/cli ./internal/memory`：通过。
- `go test -race ./internal/session ./internal/server ./internal/cli ./internal/memory`：最终版本通过。
- `GOOS=windows GOARCH=amd64 go build -o /tmp/ki-memory-windows.exe ./cmd/ki`：交叉构建通过。
- 聚焦浏览器检查：`KI_BIN=/tmp/ki-memory-test bun run test:e2e:serial e2e/stream-recovery.spec.ts e2e/history-recovery.spec.ts e2e/compact-history.spec.ts`，11/11 通过。
- `go test -tags embed -count=1 ./...`：最终版本全部通过，包含 Bun unit 和并行 WebUI fake 浏览器矩阵（e2e 包约 111s）。
- `cd web && bun run test:perf`：6/6 通过，与本次其它重负载检查串行执行。长历史首屏 205ms、巨大消息首屏 208ms；history tail / index / before 的压缩正文分别为 4126 / 21560 / 3561 字节，均符合既有预算。

新增验证覆盖预算/LRU/过大拒收、旧读者与替代 cache、提示词共享、追加/改写/半行恢复、Unicode 预览边界、当前分支和旧页的投影一致性、定时过期、替代活动 run、附着 reader 的数据所有权、CLI 完整恢复和 HTTP 错误、WebUI 410 后不重发 prompt，以及 runtime-only 无需解码正文。

## 取舍与后续观测

- 淘汰的完整历史或索引再次访问时会重新扫描；以可预测的空闲持有量换取冷访问的 CPU/I/O。模型运行仍可能需要较大的活动上下文，后续观察需区分活动峰值与空闲基线。
- CLI 的持久化恢复针对当前分支最新人工轮，不能从已过期的内存回放还原所有瞬时工具进度；最终正文、分支身份和统计仍由 JSONL 提供。
- 64/16/16MiB 的缓存预算没有作为进程内存硬上限。在线 heap/GC 指标与压力下的预算调优属于后续 P2；本次不以强制 GC 或压低运行时内存上限替代所有权修复。
- 更新运行中的 daemon 后，可在同样浏览、分页、运行并空闲的负载下重新采样 RSS 和 heap；本次没有切换线上进程。

经验是：缓存每一种对象都要定义生命周期、全局预算、单项准入和失效后的恢复方式。界面只展示少量历史，不能证明服务端只持有少量历史；响应字节预算、缓存所有权预算和进程驻留内存必须分别验证。
