# 会话格式

一个 session 一个目录，append-only jsonl 树。包入口见 `internal/session/doc.go`。

## 路径

`{sessions.root}/<encoded-cwd>/<timestamp>_<uuidv7>/`

- `encoded-cwd`：绝对路径去掉盘符，`/` `\` `:` 换成 `-`，两边加 `--`。
- 目录内：`events.jsonl` + `config.json`。

## jsonl

第一行 header：`type=session`，含 `id` / `cwd` / `parentSession`。  
之后每行 `{type,id,parentId,timestamp,…}`：`message`、`compaction`、`model_change`、`request_header`、`context_usage`、`patch_apply_updated`、`compaction_start`/`compaction_end`。entry id 为无连字符的 32 位 hex UUIDv7。

`request_header` 固定该轮的 `system`、`tools[]`、provider/model、thinking effort、catalog version 和价格快照。每个工具同时保存 `type`；custom 工具还保存 grammar `format`。消息里的工具调用保存 `toolType` 和 freeform `input`，使 resume 能保持 `custom_tool_call` / `custom_tool_call_output` 配对。`context_usage` 保存 `usedTokens`、有效 `contextWindow` 与 `estimated`；`patch_apply_updated` 保存模型生成 patch 时的结构化预览。两者都沿 SSE 到 WebUI，且不进入 provider context。

toolResult message 可带结构化 `details`。它随 jsonl 落盘并通过现有 session API/SSE 提供给 WebUI，但 provider 回放只使用模型可见的 `content`，不会把 diff、patch 或任务诊断元数据送回模型。

## 细节

- 新行永远 append 在文件末尾；`config.activeLeafId` 持久化当前分支。旧数据没有该字段时，重载以最后一条非 header 为 leaf。
- `SetLeaf` 只切换 active leaf 并写 config，旧行不删。edit/regenerate 从指定 parent append sibling branch。
- `MessagesToLeaf` 沿 parent 走到根；若路径上有 compaction，先注入 summary，再取 `retainedTail`（新条目，压缩时最近消息原文落盘）；旧 jsonl 无 `retainedTail` 时回退 `firstKeptEntryId` 截断。`LastCompactionAt` 返回最近 compaction 时间戳（stale-usage 防护用）。
- fork：`ForkAt` 新建 session 目录，只写 root → target 路径；新 header 使用新 id，`parentSession` 保存源 session id。子 session 复制源的 `provider` / `model` / `thinkingEffort`，不回落到 registry 默认。
- `config.json`：该 session 的 `provider` / `model` / `thinkingEffort` / `activeLeafId`，可选 `title` / `pinned` / `pinnedAt`。Skills/MCP 启用在 `{KI_HOME}/toggles.json`，不在 session 里。
- user message 可保存结构化 `text`、`workspace_file`、`file` 和带宿主绝对路径的 `image` content。站内浏览器选择的是 workspace 引用；粘贴/拖入文件按 SHA-256 保存到本 session 的 `attachments/`。jsonl 只存引用；server 在 provider 边界读取并编码图片，普通文件变成可供 `Read` 使用的路径说明。fork 复制附件并把新 jsonl 中的路径改到新 session 目录。
- 按 id 定位目录：serve 进程内维护 `session.Index`（id→dir 内存 map，见 `internal/session/index.go`），启动时由 `List` 的同一次 walk 顺路建好，零额外读盘。create/fork 后 `Add`、delete 后 `Remove`。命中即 O(1)；miss（别的进程建的会话、或目录被外部删除）回退到 `Find` 扫描并自愈，文件系统始终是唯一事实来源。
- `GET /v1/sessions/{id}` 带只读 `availableSkills` / `availableMcp`（含 session 级 MCP tools、加载状态和错误）和 `commands[]`。`enabled` 来自全局 `toggles.json`。Prompt 在组装模型请求前并行连接启用的 MCP server 并完成 `tools/list`；失败项发事件但不阻断其他 server 或内置工具。

会话 cwd 来自工作区 path（或临时 `{KI_HOME}/workspace/tmp+<时间戳>`），不是进程 Getwd。标题优先用 `config.title`。`Remove` 删除整个会话目录。工作区见 [workspace.md](workspace.md)。
