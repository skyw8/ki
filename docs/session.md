# 会话格式

一个 session 一个目录，append-only jsonl 树。包入口见 `internal/session/doc.go`。

## 路径

`{sessions.root}/<encoded-cwd>/<timestamp>_<uuidv7>/`

- `encoded-cwd`：绝对路径去掉盘符，`/` `\` `:` 换成 `-`，两边加 `--`。
- 目录内：`events.jsonl` + `config.json`。

## jsonl

第一行 header：`type=session`，含 `id` / `cwd` / `parentSession`。  
之后每行 `{type,id,parentId,timestamp,…}`：`message`、`compaction`、`model_change`。entry id 为 8 位 hex。

## 细节

- leaf 只在内存；新行永远 append 在文件末尾。重载：最后一条非 header 即当前 leaf。
- revert 只改内存 leaf，旧行不删。
- `MessagesToLeaf` 沿 parent 走到根；若路径上有 compaction，先注入 summary，再从 `firstKeptEntryId` 往后取。
- fork：整目录拷贝，改 header 的 `id` 和 `parentSession`。
- `config.json`：该 session 的 `provider`/`model`，以及 skills/mcp 的 `only` / `disabled`。
