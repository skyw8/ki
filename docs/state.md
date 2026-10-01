# 状态文件版本与迁移

`{KI_HOME}` 下由 Ki 自己拥有的 JSON 状态文件，读写统一走 `internal/state`。本文件是
这些文件的 schema 版本与迁移契约；每个包的 `doc.go` 只写本包字段含义，不重复规则。

## 适用范围

| 文件 | owner | 当前 `version` | 更高版本时 |
|---|---|---|---|
| `models.json` | `internal/provider` | 2 | fail-fast：`NewRegistry` 报错，serve 起不来 |
| `credentials.json` | `internal/provider` | 1 | fail-fast |
| `workspaces.json` | `internal/workspace` | 1 | fail-fast：`Open` 返回错误 |
| `toggles.json` | `internal/toggles` | 2 | best-effort：`Load` 回退默认值 |
| `push-subscriptions.json` | `internal/push` | 1 | best-effort：`OpenStore` 返回空表（浏览器会重新订阅） |
| `extensions/<name>/config.json` | `internal/extension` | 1 | 配置读取/更新报错，绝不覆盖 |
| `extensions/deep-web-search/cache.json` | Go sidecar | 1 | best-effort：空缓存，绝不覆盖 |
| `extensions/telegram-bot/state.json` | Go sidecar | 1 | sidecar 初始化报错，绝不覆盖 |
| `goal/<sessionId>.json` | Go sidecar | 1 | best-effort：不恢复 goal，绝不覆盖 |
| session `agent.json` | `internal/tools` | 3 | 恢复该任务失败并记录 warning，绝不覆盖 |
| session `queue.json` / `ext-queue.json` / `context-queue.json` | `internal/session` | 1 | 队列操作返回错误，绝不覆盖 |

扩展配置的 `version` 仅描述持久化 envelope：Host 解码时删除该头，再按 manifest 的 `config.schema` 校验业务字段。HTTP 读取不返回该头，PATCH 不能修改它。Go sidecar 共享 `internal/state`；Rust sidecar只读同样的版本头并拒绝不支持的版本。

「fail-fast」用于读不到就无法正常工作的文件：宁可启动报错，也不静默丢字段。
「best-effort」用于可恢复的旁路状态：加载失败退化为空/默认，但写入侧仍拒绝覆盖更新的文件。

`agent.json` v2 将 task-only 通知标记替换为按 generation 的 delivery ledger，
并把 pending 字符串改为带 `clientRequestId` 的输入对象。v1 迁移保留已消费 generation；
旧的「已入队」标记不再代表已交付。v3 移除 foreground/background 与旧共享 task 语义，增加 task_name/task_path/root_session_id；v2 迁移为稳定 legacy_<sessionId> 名字，恢复时由结构父链补全路径。pending 与 ledger 保留。队列文档为对象 envelope（`version` + `items`；
context queue 另带 `next`），不再写裸数组。

**不适用**（不要给它们加 `version`）：

- `catalog.json` 是 `//go:embed` 进二进制的，`CatalogVersion` 与文件头的核对是构建期自检，
  不存在「新文件 + 旧二进制」。
- `server.json` 是进程握手文件（addr + token），每次 `serve` 重写，无常驻状态。
- `ki.toml` 由 Viper 合并，不是 JSON 文档。

## 规则

1. **顶层整数 `version`**。缺省（`0`）视为当前版本：手写文档和加字段之前的旧文件都算。
   纯**新增**字段不升版本——新二进制读旧文件照常，旧二进制读新文件因为字段被忽略也照常。
   只有**破坏性变更**（改名、删除、语义变化）才 `+1`。
2. **向前迁移，链式，只读时在内存里做**。加载时 `state.Migrate` 从文件版本逐步调用
   `steps[from]` 直到当前版本；迁移函数一经发布就冻结，新增版本只追加新函数，永不修改旧的。
   迁移结果不立刻落盘，用户文件在下次真正写入时才更新（避免启动即写、也避免把只读打开变成写）。
3. **更高版本绝不降级**。文件版本 > 本二进制支持版本时返回 `state.ErrNewerVersion`，
   调用方要么 fail-fast，要么回退——**绝不**把它解码成旧结构再写回，那会静默丢弃字段。
   错误信息包含文件路径、发现版本、支持版本和「upgrade ki」。
4. **写入原子且不覆盖更新的文件**。统一用 `state.WriteJSON`（同目录 temp + rename，
   Windows 走 `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)`）或 `state.WriteVersioned`。
   后者在写前读一次文件版本：更高 → 拒绝；更低 → 先 `state.Backup` 出一份
   `path.bak-<UTC 时间戳>` 再写。

## 场景对照

| 场景 | 结果 |
|---|---|
| 新二进制读旧 schema（如 `models.json` v1） | 内存迁移到当前，正常启动；文件下次写入时升级，并留一份 `.bak-*` |
| 新二进制读当前 schema | 原样使用 |
| 新二进制读更高 schema | 按上表 fail-fast / best-effort |
| 旧二进制读更高 schema | `ErrNewerVersion`；fail-fast 或回退，绝不覆盖 |
| 旧二进制写更高 schema | `WriteVersioned` 拒绝覆盖 |

## 新增一个迁移

1. 把 owner 包里的 `version` 常量 `+1`（`modelsFileVersion` / `credentialsFileVersion` /
   `workspace.version` / `toggles.version` / `push.storeVersion`）。
2. 写一个 `func(raw []byte) ([]byte, error)`，在 owner 包里加进 `map[int]state.Migration{旧版本: fn}`；
   文档用通用 JSON 走字段重命名，让解码继续做唯一的字段校验。
3. 加一个用例：旧文档加载后拿到新语义，且磁盘上的文件仍是旧版本（证明迁移是读路径）。

## 为什么不用库

Go 没有 config-migration 的事实标准；`golang-migrate` / `goose` / `atlas` 面向 SQL schema，
套 JSON 配置是误用。这里每文件就是「读头版本 → 链式转 → 原子写」三段逻辑，共享在
`internal/state` 里已经足够，不引第三方依赖。

`toggles.json` v2 将工具名称规范为 snake_case，v1 的 Bash/PowerShell/Agent/SendMessage/TaskOutput/TaskStop 开关展开为新能力的保守并集。禁用优先，迁移不会因新名字重新启用旧禁用能力。新 schema 不恢复这些旧工具执行入口。
