# 大工具输出后的 cache miss 与 Codex reasoning 污染

日期：2026-09-26
范围：`internal/tooloutput`、`internal/loop`、`internal/server`、`internal/tools`（Grep / Glob / JobStore）、`extensions/codex-oauth/main.py`

## 现象

两个现象由同一次会话调查串在一起（触发会话 `01a0ddf0c0327e54a56d92d36c8d1f25`）：

1. **大工具输出后紧邻的一次请求 cache miss**。同一 session 的 usage 序列：

   ```text
   13:43:21  request  cacheRead=74240
   13:43:25  Grep(**/*.go, pattern=Replayable, -A=70) 返回约 20109 字符
   13:43:25  request  input=81128 cacheRead=0        ← 唯一一次 miss
   13:43:31  request  cacheRead=74240                ← 下一次就恢复了
   ```

   zvec-grep 之后也是同一个"一次 miss、下一次恢复"的形态：不是缓存被清，而是某一次 continuation 没复用旧 prefix。

2. **assistant 内容里出现重复 reasoning + 幻影 toolCall**。同一段 jsonl 中多次出现"正常 reasoning + 同 itemId 的第二个 `thinking`（错误地带 `name`/`arguments`）+ 真实 `toolCall`"。

## 时间线

1. 先排除 Ki 侧原因：`prompt_cache_key` 已经在 `extensions/codex-oauth/main.py` 设置（= session id），不存在缺 key；session 的 request/内容没有突变，工具结果只是被追加。
2. 对比 usage：miss 只发生在"追加 tool_result 后的第一次 continuation"，再下一次请求又命中 74240 —— 说明上游 prefix 仍存在，只是那一次 continuation 没有复用它。
3. 读 sidecar 的流式关联代码，发现 `slot_id = str(output_index) if output_index is not None else item_id`：以 `output_index` 为主键。上游网关在同一个 `output_index` 上先后发 reasoning 和 function_call 时，两个 item 落进同一个 dict：reasoning 条目被 `existing.update({"name":…, "arguments":…})` 写成带工具字段的 reasoning，随后真实 toolCall 又追加一遍。
4. 被污染的 assistant 会写进 jsonl，并在之后**每一轮**重新回放；同时它在 prompt 里的位置正好是"工具结果之后"，与 miss 的触发点重合。

## 根因

1. **上游 Responses prompt cache 的行为**：sidecar 用 `store:false` + 全量重放（`include:["reasoning.encrypted_content"]`），Ki 不做任何缓存失效动作。miss 是"追加 tool_result 后首个 continuation 未复用旧 prefix"，一次之后自愈。Ki 无法保证命中，只能降低 miss 的代价。
2. **大工具输出直接进 prompt**：当时 Grep 在工具内就截到 20KB、Glob 100KB、Bash 尾部 50KB，这些内容**全部**进入模型上下文，miss 一次就是几十 KB 的未缓存输入乘以单价。没有任何统一的上限，也没有把完整结果落到可读文件。
3. **sidecar 用 `output_index` 当 item 主键**：复用/缺失 `output_index` 的网关会把不同 item 合并，产生上面第 2 个现象。

## 修复

- **统一 output spool**（`internal/tooloutput`，边界在 loop 的 `AfterTool` 之后）：超过 preview budget（默认 16KiB / 800 行）的文本完整写入 `<os.TempDir>/ki-tool-output/run-<pid>-<rand>/<session>/`，模型只拿到有界 preview + `Read(file_path, offset, limit)` 提示；jsonl/SSE 记录的也是有界结果。内置工具、扩展工具、zvec 等 sidecar 工具同一契约。
- **配额**：单文件 8MiB、单 session 256MiB；超限时只存前缀（`output.incomplete`），session 预算用完则只回有界 preview 并在 note 里说明，绝不把结果变成错误。
- **生命周期**：session 关闭删目录并释放预算，server 关闭删整个 run root；崩溃残留由启动时的 `owner.json`（pid/host/heartbeat）+ TTL 24h 清扫，持有者进程仍存活的 root 不清理。
- **不重复 spool**：已带 `output_file` 的工具结果、已带 `next_offset` 的 Read 结果、以及"读 spill 文件"本身都原样保留 —— 否则 Read 会指向自己那一页。Bash / PowerShell / Monitor 的任务日志也交给 store 创建，与 session 目录同生命周期。
- **Grep / Glob 不再在自己内部截断**：完整匹配交给 spool，只保留 16MiB 的内存保护上限，spill 文件因此真的是"完整结果"。
- **sidecar item 关联**：`SlotRegistry` 以 provider item ID 为主键，`call_id` / `output_index` 只作为 delta 缺 ID 时的回退（同一个 `output_index` 被复用时以最后注册的 item 为准）；reasoning item 按 `REASONING_FIELDS` 白名单过滤，落盘和回放各过滤一次；回放前按 reasoning id 去重。

## 验证

- `internal/tooloutput`：溢出、preview 边界、非文本块保留、跳过规则（`output_file` / `next_offset` / 读自己的 spill）、单文件上限、session 预算、owner 清扫（死进程 / 存活 / 外部主机 / 无 owner）、Close 清理。
- `internal/loop`：一次完整 Run 断言模型可见结果有界、`details.output.path` 指向的文件与工具原始输出逐字节相同、工具自身 details 仍平铺。
- `internal/server`：session 关闭删 spill 文件、server shutdown 删 run root。
- `internal/tools`：Grep 结果不再有 20KB 上限；JobStore 把任务日志写进 session spill 目录，store 拒绝时退回临时文件；Bash 端到端确认日志落在 spool 目录。
- `extensions/codex-oauth`：19 个单测，含"同一 `output_index`、不同 item ID → reasoning 与 toolCall 各一条且 reasoning 不带 `name`/`arguments`"、"delta 缺 item ID 时经 call_id / output_index 回到已注册 item"、"回放时剥掉 reasoning 上的工具字段"。
- `go test ./...`（含 `e2e` fake 矩阵）全绿。

## 现状

| 场景 | 行为 |
|---|---|
| 单次工具输出 20KB～16MiB | 完整结果落 spill 文件，模型只看到 16KiB preview + 路径 |
| 单文件超过 8MiB / session 超过 256MiB | 只存前缀或完全不落盘，note 说明原因，结果仍有界 |
| 追加 tool_result 后的首个 continuation | 仍可能 miss 一次（上游行为），但携带的是有界 preview |
| 网关复用 `output_index` | reasoning 与 toolCall 分开存；reasoning 不会带回工具字段 |
| serve 崩溃 | 下次启动按 owner pid/心跳清掉死进程的 run root |
| Read 读 spill 文件 | 不再生成二级 spill 文件 |
