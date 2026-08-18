# 会话格式

一个 session 一个目录，append-only jsonl 树。包入口见 `internal/session/doc.go`。

## 路径

`{sessions.root}/<encoded-cwd>/<timestamp>_<uuidv7>/`

- `encoded-cwd`：绝对路径去掉盘符，`/` `\` `:` 换成 `-`，两边加 `--`。
- 目录内：`events.jsonl` + `config.json`。

## jsonl

第一行 header：`type=session`，含 `id` / `cwd` / `parentSession`。  
之后每行 `{type,id,parentId,timestamp,…}`：`message`、`compaction`、`model_change`、`request_header`、`context_usage`、`compaction_start`/`compaction_end`。entry id 为无连字符的 32 位 hex UUIDv7。

`request_header` 固定该轮的 `system`、`tools[]`、provider/model、thinking effort、catalog version 和价格快照。每个工具同时保存 `type`；custom 工具还保存 grammar `format`。消息里的工具调用保存 `toolType` 和 freeform `input`，使 resume 能保持 `custom_tool_call` / `custom_tool_call_output` 配对。`context_usage` 保存 `usedTokens`、有效 `contextWindow` 与 `estimated`，同时沿 SSE 到 WebUI。

## 细节

- leaf 只在内存；新行永远 append 在文件末尾。重载：最后一条非 header 即当前 leaf。
- revert 只改内存 leaf，旧行不删。
- `MessagesToLeaf` 沿 parent 走到根；若路径上有 compaction，先注入 summary，再取 `retainedTail`（新条目，压缩时最近消息原文落盘）；旧 jsonl 无 `retainedTail` 时回退 `firstKeptEntryId` 截断。`LastCompactionAt` 返回最近 compaction 时间戳（stale-usage 防护用）。
- fork：整目录拷贝，改 header 的 `id` 和 `parentSession`。
- `config.json`：该 session 的 `provider` / `model` / `thinkingEffort`，可选 `title` / `pinned` / `pinnedAt`，以及 skills/mcp 的 `only` / `disabled`。
- 按 id 定位目录：serve 进程内维护 `session.Index`（id→dir 内存 map，见 `internal/session/index.go`），启动时由 `List` 的同一次 walk 顺路建好，零额外读盘。create/fork 后 `Add`、delete 后 `Remove`。命中即 O(1)；miss（别的进程建的会话、或目录被外部删除）回退到 `Find` 扫描并自愈，文件系统始终是唯一事实来源。
- `GET /v1/sessions/{id}` 在 Toggle 之外带 `availableSkills` / `availableMcp`（按 cwd 现算的目录，含 `enabled` / `source`）。列举 MCP 不 spawn。Prompt 用 serve 级池的缓存 schema 组 tools，真 call 才连。

会话 cwd 来自工作区 path（或临时 `{KI_HOME}/workspace/tmp+<时间戳>`），不是进程 Getwd。标题优先用 `config.title`。`Remove` 删除整个会话目录。工作区见 [workspace.md](workspace.md)。
