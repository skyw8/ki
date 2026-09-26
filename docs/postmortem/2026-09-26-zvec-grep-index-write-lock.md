# zvec-grep 搜索偶发失败：把索引写锁竞争误报成 daemon

日期：2026-09-26  
范围：`extensions/zvec-grep`（`src/tool.ts`、`src/index-job.ts`、`src/main.ts`）、上游 `@zvec/zvec-grep@0.2.1` 的引擎锁与错误文案

## 现象

一次会话里同一条消息并行发出两次 `zvec_grep_search`，其中一次直接失败：

```text
zvec_grep_search failed: A zvec-grep daemon owns index writes for this root
root=/data/hgy/ki
pid=0
hint=Run with --mode auto so a ready daemon handles indexed operations. If auto is already active, check zg server status and restore or stop the daemon before retrying.
config=Edit ~/.zvec-grep/config.json and set client.mode to "auto" to persist this behavior.
```

但当时**根本没有 daemon**：`zg server status` 是 stopped，`~/.zvec-grep/` 下连 `config.json` 都不存在，`<root>/.zvec-grep/locks/` 里只有 `home.readers`（没有 `daemon.json`，也没有 `daemon.guard` 残留）。重试同一个调用立刻成功。

## 时间线

1. 扩展的搜索默认 `freshness: wait_for_fresh`（`src/tool.ts`），映射到库的 `autoUpdate: true`，即“先刷新索引再搜”。
2. 库的 `context()` 在 `autoUpdate !== false` 时先跑 `refreshWorkspaceIndexForContext()`，该函数**第一行**就取索引写许可（`assertDaemonWriteAllowed` → mkdir `locks/daemon.guard`），并**持有到整个刷新结束**；真正写盘时还会持有 `locks/home.write`。
3. 同一条消息里的另一次搜索并行进入：抢不到守卫，库**不等待、不退避**，立刻抛错。错误里的 `pid` 取自 `locks/daemon.json` 的租约记录，而该文件不存在，于是 `pid=0`——这条报错把“自己人撞自己人”说成了“daemon 占用写锁”。
4. 复现时抓到两个证据：`.zvec-grep/index.zvec` 20:39:00 → `files.zvec` 20:39:07，说明刷新窗口约 7 秒（正是许可被持有的时长）；失败那次的 `locks/` 下没有任何租约文件。
5. 用真实库写探针脚本（两条并行 `tool.execute`）复核，发现**第二种失败形态**：输的那条第一次拿到 `ZVEC_GREP.ENGINE.DAEMON_LEASE_ACTIVE`，250ms 后第二次拿到 `ZVEC_GREP.ENGINE.LOCK.BUSY`

   ```text
   Index unavailable
   lock=/data/hgy/ki/.zvec-grep/locks/home.write
   operation=context
   ```

   即赢家在刷新期间持有 `home.write`，而库的**读**路径（`context`/`info`）会先 `assertNearestWorkspaceHomeUnlocked`，只要存在活跃写者就拒绝读取——和 `autoUpdate` 无关，所以“降级成不刷新”也救不了这一形态。
6. 探针还量化了代价：仓库有改动时，一次搜索的自动刷新要 21~22 秒；这段时间内同 workspace 的其它搜索必然失败。
7. 修完后同一探针 3/3 全部成功（修复前 1/3 失败）。

## 根因

三层叠在一起，前两层在上游，第三层在扩展：

1. **上游读路径拿写锁**：只有 `autoUpdate` 路径调 `assertDaemonWriteAllowed`，而它在判断“需不需要刷新”之前就取；许可覆盖整个刷新；`home.write` 也覆盖整个刷新。上游自己的 daemon 读路径传 `autoUpdate: false`（`openWorkspaceReadSession`），说明它知道读不该拿写许可，只是扩展走的入口默认没关。
2. **上游的竞争处理与文案**：守卫竞争（`daemon.guard` 是毫秒级 mkdir 互斥）即硬失败、无等待；无租约时 `pid=0`；`hint` / `config` 两行指向 CLI 的 `--mode auto` 与 `~/.zvec-grep/config.json`，而扩展是 in-process 直连库，既不读 `client.mode`，也没有 `--mode` 这个开关可用。
3. **扩展自己的默认值与并发**：默认每次搜索都走刷新路径；单进程放行 4 路并发（`MAX_CONCURRENT_SEARCHES`）；`/zg-index` 用 CLI 建索引，而 `index()` 同样持有这两个锁直到构建结束——构建期间（首次可能要几分钟）任何 in-process 搜索都读不到索引。

## 修复

- **P0 串行化**：`RefreshGate`（`src/tool.ts`）按 root 串行化本 sidecar 的搜索，后来者等赢家刷完再跑（此时自己的刷新是空操作）。这是消除自撞的关键——`LOCK.BUSY` 形态靠重试救不回来。
- **P0 降级**：刷新路径遇到 `DAEMON_LEASE_ACTIVE` 先退避 250ms 重试一次（覆盖毫秒级守卫竞争），仍失败则以 `autoUpdate: false` 再搜一次，结果文本加 `note: refresh skipped — …`，`details.refreshSkipped` 记原因。
- **P0 有界等待**：`LOCK.BUSY`（他人持写锁）退避 400ms 重试至多 2 次，仍失败则报出锁路径与持有者（`ownerOperation` / `ownerPid`）。
- **P2 自知**：`IndexJobs.hasActiveRoot(root)` 让搜索知道本 sidecar 正在为这个 root 建索引；此时跳过刷新，并在读被拒时直接报“/zg-index 正在构建，读锁要等它结束”，不消耗满预算去等构建。
- **P1 文案**：不再转发上游那条会误导的提示。`pid=0` 一律按“其它写者占用索引写锁”解释（并给出重试 / `freshness=eventual` / 改用 Grep 的出路）；只有真的读到 daemon 租约 pid 时才说 daemon，并给出扩展侧的可行动作（`zg server off`，或把扩展 `mode` 切到 `external-daemon`）。
- 每次尝试都只花剩余预算（`deadline - now`），门等 + 重试 + 降级不会超过 `searchTimeoutMs`。

验证：`extensions/zvec-grep` 的 `bun run test` 新增 5 例（守卫竞争降级、eventual 不受影响、daemon 与非 daemon 两种报错、并发不撞、他人写锁有界重试、索引任务期间的报错）；真实库并发探针从 1/3 失败变为 3/3 成功。

## 上游

已提交：https://github.com/zvec-ai/zvec-grep/issues/213 —— `[Bug]: context() auto-update holds the index write permit and the workspace write lock, so concurrent reads fail with DAEMON_LEASE_ACTIVE / LOCK.BUSY`。正文包含库/CLI 两种复现、mtime 与锁目录证据、以及 5 条修复建议：

1. `engine/service/zvec-grep.js` 的 `refreshWorkspaceIndexForContext` 在 staleness 检查**之前**调用 `assertDaemonWriteAllowed`，并在整个 refresh 期间持有该 permit；`withHomeWriteLock` 同样覆盖整个 refresh。读路径（`contextWithTimings` → `assertNearestWorkspaceHomeUnlocked`）只要存在 writer 就拒绝执行，于是“一次搜索的自动刷新”会让同 workspace 的并行搜索拿到 `ZVEC_GREP.ENGINE.DAEMON_LEASE_ACTIVE` 或 `ZVEC_GREP.ENGINE.LOCK.BUSY`。
2. `assertDaemonWriteAllowed` 在 guard 竞争时立即失败（无 wait/backoff），尽管 guard 是毫秒级临界区；`daemonLeaseActiveError(root, initial?.pid ?? 0)` 在没有 `daemon.json` 时打印 `pid=0` 并把原因说成 daemon。
3. 报错里的 `hint` / `config` 只适用于 CLI 的传输模式；库的 in-process 调用方没有 `client.mode` 可设（本机 `~/.zvec-grep/config.json` 甚至不存在），照做不会有任何变化。
4. 复现：一个 workspace 的索引过期后，同一进程内并发两次 `context({autoUpdate: true})`；或对任意库调用方重复两次并行 `context`。附带证据：`.zvec-grep/index.zvec` 与 `files.zvec` 的 mtime 差（刷新窗口 = 锁窗口），以及失败时 `<root>/.zvec-grep/locks/` 下没有 `daemon.json`。
5. 建议：读路径在做 staleness 检查前不取写许可；permit/`home.write` 只覆盖真正的写；竞争时等待而非抛错；`pid=0` 时不要把原因归给 daemon；`hint` 按调用方（CLI / 库 / daemon）区分。

## 现状

| 场景 | 行为 |
|---|---|
| 同一 sidecar 内并行搜索，索引过期 | 串行化：都成功，后来者等刷新结束 |
| 刷新输给毫秒级守卫竞争 | 退避 250ms 重试；仍失败则降级为不刷新并加 `note:` |
| 他人（外部 `zg index` / daemon / 另一个 ki）持写锁 | 退避 400ms × 2 后报锁与持有者；`details.retryable = true` |
| `/zg-index` 构建期间搜索 | 直接报“正在构建”，提示等任务结束或用 Grep |
| daemon 持有 root | 报 daemon pid + `zg server off` / 切 `external-daemon`，不再转发 `client.mode` 提示 |
