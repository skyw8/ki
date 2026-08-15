
## agent主循环

事件总线架构。循环只 `emit`，业务通过订阅接入，不把写盘 / 计量 / UI 写进循环本体。

参考 `/data/hgy/pi` 的分层：

- **事件（被动）**：订阅者不能改执行路径。抛错记日志，不影响循环。
- **拦截 hook**：awaited，按注册顺序执行，可以改上下文、拦工具、改结果。
- **扩展 hook 表**（`session_start` / `input` / `model_select` 等）：本期不做，会话生命周期先做成内部函数。

### 事件顺序

```
agent_start
  turn_start
    message_start → message_update* → message_end     # user / assistant
    tool_execution_start → tool_execution_update* → tool_execution_end
    message_start / message_end                       # toolResult
  turn_end
  …（还有 tool call 再转一轮）
agent_end
```

### 事件（第一批全做）

| 事件 | 用途 |
|---|---|
| `agent_start` / `agent_end` | 一次 prompt 的起止。`agent_end` 上检查 compaction |
| `turn_start` / `turn_end` | 一轮 = 一次 assistant 回复 + 其工具调用/结果 |
| `message_start` | user / assistant / toolResult 开始。assistant 此时还是 partial |
| `message_update` | 仅 assistant 流式增量，给后续 UI |
| `message_end` | 消息定稿。**写盘挂这里**：异步 append jsonl；assistant 上记 token、响应时间 / ttft |
| `tool_execution_start` / `tool_execution_update` / `tool_execution_end` | 单次工具执行。`tool_execution_end` 记耗时、失败、原因 |

第一批订阅者：

- `message_end` → 异步写 jsonl（树、leaf、token）
- `tool_execution_end` → 工具耗时 / 失败原因
- `agent_end` → compaction 检查

### 拦截 hook（按能力分批，不一次铺满）

| hook | 时机 | 能力 | 批次 |
|---|---|---|---|
| `before_run` | 进入循环前，组装分层 prompt | 可追加消息、替换 system prompt | 2（上下文管理） |
| `transform_context` | 每次请求模型前 | 改消息列表。compaction 后 cache 失效，分层 prompt 在这里 reload | 2 |
| `before_tool` | 参数校验后、执行前 | 可改 args，或 `{ block, reason }` 拦掉 | 1（四个工具） |
| `after_tool` | 执行完、发出 `tool_execution_end` 前 | 可改 content / isError / details | 1 |
| `before_compaction` | 决定是否 compact 时 | 可 decline，或直接给 compact 结果 | 随 compaction |

`before_tool` 失败封闭：handler 抛错视为 block，不执行工具。其余 hook 抛错跳过该 handler，后面继续。

本期不做：`before_request` / `before_payload` / `after_response`、`prepareNextTurn`、`shouldStopAfterTurn`、steering / follow-up 队列、以及整张扩展 hook 表。

## 工具

第一批四个：`Bash` / `Edit` / `Write` / `Read`。

工具名、input schema、`description` / `prompt`、关键行为（先读再写、行号格式、timeout 单位、`replace_all`）全部按 `/data/hgy/claude-code-source-code` 对应工具移植，不要用 pi 的工具名或参数。提示词从源码生成函数抄，不要自己改写。

源码锚点：

| 工具 | name | schema / 行为 | prompt |
|---|---|---|---|
| `Read` | `FileReadTool/prompt.ts` `FILE_READ_TOOL_NAME` | `FileReadTool/FileReadTool.ts` | `FileReadTool/prompt.ts` `renderPromptTemplate` |
| `Write` | `FileWriteTool/prompt.ts` `FILE_WRITE_TOOL_NAME` | `FileWriteTool/FileWriteTool.ts` | `FileWriteTool/prompt.ts` `getWriteToolDescription` |
| `Edit` | `FileEditTool/constants.ts` `FILE_EDIT_TOOL_NAME` | `FileEditTool/types.ts` | `FileEditTool/prompt.ts` `getEditToolDescription` |
| `Bash` | `BashTool/toolName.ts` `BASH_TOOL_NAME` | `BashTool/BashTool.tsx` `fullInputSchema` | `BashTool/prompt.ts` `getSimplePrompt` |

### Read

- name: `Read`
- description: `Read a file from the local filesystem.`
- schema（strict）：
  - `file_path` string，绝对路径
  - `offset` 可选，非负整数，起始行号。默认 `1`（1-indexed）；`0` 当文件头
  - `limit` 可选，正整数，最多读多少行。默认最多 `2000` 行
  - `pages` 可选 string，仅 PDF，如 `"1-5"` / `"3"` / `"10-20"`，单次最多 20 页
- prompt：`renderPromptTemplate` 全文。要点：必须绝对路径；默认从头读 2000 行；结果用 `cat -n`（行号从 1）；支持图 / PDF / `.ipynb`；目录用 `Bash` 的 `ls`，不要用 Read。
- 文本默认上限：整文件 256KB、输出约 25000 token，超限报错并让模型用 offset/limit。

### Write

- name: `Write`
- description: `Write a file to the local filesystem.`
- schema（strict）：
  - `file_path` string，必须绝对路径
  - `content` string
- prompt：`getWriteToolDescription` 全文。要点：会覆盖已有文件；改已有文件必须先 `Read`，否则失败；已有文件优先 `Edit`；未明确要求不要新建 `*.md` / README。

### Edit

- name: `Edit`
- description: `A tool for editing files`
- schema（strict）：
  - `file_path` string，绝对路径
  - `old_string` string，要被替换的原文
  - `new_string` string，必须和 `old_string` 不同
  - `replace_all` 可选 bool，默认 `false`；为 true 时替换全部出现
- prompt：`getEditToolDescription` 全文。要点：会话里至少 `Read` 过该文件才能 Edit；从 Read 结果里抄文本时丢掉行号前缀；`old_string` 不唯一则失败，补上下文或用 `replace_all`。
- 不要暴露 pi 的 `edits[]` / `oldText` / `newText`。

### Bash

- name: `Bash`
- description：有入参 `description` 时用它，否则 `Run shell command`
- schema（strict）：
  - `command` string
  - `timeout` 可选 number，**毫秒**，上限 600000（10 分钟），默认 120000（2 分钟）
  - `description` 可选 string，给 UI 的短描述，不影响执行
  - `run_in_background` 可选 bool
  - `dangerouslyDisableSandbox` 可选 bool
- 不要把内部字段 `_simulatedSedEdit` 暴露给模型
- prompt：`getSimplePrompt` 全文。要点：优先用 Read/Edit/Write，不要用 `cat`/`sed`/`echo` 代替；timeout 按毫秒写进 prompt；工作目录跨命令保持，shell 状态不保持。

### 不做

- 不引入 pi 的 `read`/`write`/`edit`/`bash` 小写名，也不做 `path` ↔ `file_path` 对外双 schema
- 本期不实现 Claude Code 的其它工具（Glob / Grep / NotebookEdit / Agent 等）

## 会话管理

- 写盘由 `message_end` 等内置订阅者驱动：append 并 await 落盘，循环本体不写文件
- append-only jsonl，Event Sourcing；树靠 `id` / `parentId`，revert/regenerate 只动 leaf，旧行不删
- 一个 session 一个目录：`~/.ki/sessions/<encoded-cwd>/<timestamp>_<id>/`（`events.jsonl` + `config.json`）
- fork 单起目录，整目录迁移，header 记 `parentSession`
- token 写在 assistant `usage` 上；另记 `latencyMs` / `ttftMs` / 工具 `durationMs` 和失败原因
- resume 必须带 session id


## compaction

- 自动：`contextTokens > contextWindow - 16384`（pi 默认）时在 `agent_end` 上触发
- 手动：HTTP + CLI 可主动 compact
- 阈值、保留窗口（最近约 20k token）、summary 提示词、entry 格式跟 pi
- jsonl 追加 `compaction` entry（`summary` + `firstKeptEntryId` + `tokensBefore`）
- compact 之后供应商 prompt cache 失效，分层 prompt 必须重算后再发给模型

## 模型供应商调用

- 中间表示用 pi 的 `Message` / `AssistantMessageEvent`
- 三个协议都做：OpenAI Completions、OpenAI Responses、Anthropic Messages；适配层参照 `/data/hgy/pi/packages/ai`
- 内置 provider：`openai`、`anthropic`、`zhipu` / `zhipu-cn`、`deepseek`、`dashscope` / `dashscope-cn`
- key 和默认模型写在 `~/.ki/ki.toml`；环境变量可覆盖；没有配置默认模型时按「已有 key 的 provider」解析（跟 pi）
- thinking、prompt cache 按 pi 做全；compact 后 cache 失效，分层 prompt 重算

## 上下文管理

分层提示词（compact 后整段重算）：

1. 身份：pi 原文，pi → ki（不抄 pi 文档路径那段）
2. Available tools：system prompt 里一行 snippet；CC 长 prompt 只挂在 tool definition 上
3. Available skills：pi 的 `<available_skills>` XML；目录 `~/.ki/skills`、项目 `.ki/skills` 等
4. AGENTS.md：`~/.ki` 全局 + 从 cwd 走到 git root（含）；每目录 `AGENTS.override.md` > `AGENTS.md` > `CLAUDE.md` 取一份。不在 git 仓库则只收 cwd + 全局
5. cwd + 机器本地 timezone/date
6. message history


## 配置

支持项目级配置目录

支持全局配置目录 ~/.ki
- skills：~/.ki/skills
- mcp: ~/.ki/.mcp.json（遵循mcp规范）

支持session级配置
- session 目录下独立配置文件记录skills、mcp等，主要是便于后续可以选择是开启还是关闭

## 未来架构说明

1. 后续会使用B/S架构，本地起web服务调用本地后端，前后端打包成一个二进制，极致易用性。这样做可以跨平台，在服务器远程开发的场景下，可以通过端口转发的形式使用webui
2. 后续可能会提供HTTP SDK给别的语言使用