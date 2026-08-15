# 未决问题

对照 `docs/prd/agent-loop.md` 和 `/data/hgy/pi`、`/data/hgy/claude-code-source-code`。主循环事件分层、四工具对外 schema 已够写类型；下面这些不拍板会卡住实现。

## 已定

- 第一版是 **CLI + server**：CLI 启动后端 server，再对 server 发请求、拿返回。agent 循环、工具、会话写盘都在 server 里。
- 事件总线 + 10 个被动事件；拦截 hook 分批；扩展 hook 表本期不做。
- 四工具对外名 / input schema / description / prompt 跟 Claude Code，不用 pi 的工具名和参数。
- 会话 append-only jsonl、树、revert/regenerate 只动 leaf。
- 以后 B/S、HTTP SDK 是同一条路：现在的 server 就是以后 WebUI / 外语言 SDK 打的那个后端。

### §1 CLI + server（已拍）

- **1.1 进程**：一个二进制。默认不 fork：没有常驻 server 时在**本进程内**起 HTTP。
  - **`-d`**：把 server 放到后台（这时才拉起独立进程并 detach），写好地址/token，之后的 `ki` 都连这个。
  - `ki serve`：前台常驻，等价于不 detach 的 server 入口。
- **1.2 传输**：local HTTP，bind `127.0.0.1`。常驻 / `-d` 用固定默认可配端口；本进程临时起的用随机端口。
- **1.3 寿命**：
  - 无 `-d`、无已有 server：CLI 退出，本进程 server 一起没。
  - `-d` 或已有 `ki serve`：CLI 退出 server 还在，下次直接连。
- **1.4 CLI**：单次调用，不是 REPL。终端要**流式**打出循环事件（文本增量、工具进度）。跑完的结果里带 `session_id`，下次带上续聊。Ctrl+C 调 abort。
- **1.5 鉴权**：要 token。请求带 token；后台 / 常驻的 token 写本地文件。
- **1.6 API**：第一版就是 HTTP。
  - 创建/恢复 session
  - 发一条 prompt
  - **订事件**：SSE 推送主循环那 10 个事件，CLI 用来流式打印
  - **abort**：取消当前这次 run

## 1. CLI + server 形态

已拍，见上。留一个实现细节：默认端口号、token/地址文件路径（`~/.ki/server.json`？）。

### §2 第一批范围（已拍）

- **2.1 skills / MCP**：第一版都做。全局 `~/.ki/skills`、`~/.ki/.mcp.json`，项目级和 session 级可开关（细节见 §8 / §9）。
- **2.2 compaction**：第一版做。自动 + 可手动；compact 后 cache 失效、分层 prompt reload。阈值等见 §6。
- **2.3 权限**：第一版工具**全放行**，不向 CLI 弹确认。`before_tool` 仍在，但内置策略是 allow。
- **2.4 并行**：一个 server 同时跑多个 session。每个 session 自己的循环、jsonl、SSE、abort。同一 session 同时只允许一个 run（再发 prompt 要排队或 409），跨 session 并行。

## 2. 第一批范围

已拍，见上。§2.4 连带：同一 session 二次 prompt 排队还是直接拒绝，实现时再定，默认拒绝（409）。

### §3 对外 API / 循环边界（已拍）

- **3.1 一次 prompt**：跟 pi 的 `prompt(text)`。HTTP 必填 `text`；`session_id` 有则续、无则新建；`cwd` / `model` 可选（默认用 session 已有值，新 session 用进程 cwd 和配置默认模型）。abort 走独立接口，不塞在这条请求体里。第一版不做 steer / follow-up 队列。
- **3.2 事件 payload**：就用 pi 那 10 个 `AgentEvent` 的字段（`agent_end.messages`、`message_update.assistantMessageEvent`、`tool_execution_*` 的 id/name/args/result 等）。SSE 原样推。
- **3.3 工具执行**：默认 **parallel**（pi 默认）。某个工具标了 sequential 则整批串行。
- **3.4 重试**：供应商错误自动重试，**默认最多 5 次**。退避参考 pi：`baseDelayMs = 2000`，指数增长（2s / 4s / 8s / …）。
- **3.5 hook**：第一版**只内置**（写盘、计量、compaction 检查、全放行的 `before_tool`）。调用方不能注册 hook；要观察用 SSE。

## 3. 对外 API / 循环边界

已拍，见上。

### §4 工具行为（已拍）

- **4.1 格式**：Read 支持文本 / 图 / PDF（`pages`）/ `.ipynb`。
- **4.2 先 Read**：不限制。Write/Edit 不要求 session 里先 Read，也不做 `readFileState` / 「文件被外部改了」检查。
- **4.3 `run_in_background`**：做。`true` 时命令丢到后台立刻返回 task id，agent 不等结束；输出稍后可用 Read 看。schema 和 Bash prompt 保留该字段。
- **4.4 sandbox / 权限**：不做。`dangerouslyDisableSandbox` 从 schema 和 Bash prompt 里拿掉。
- **4.5 cwd**：跟 pi。每次 Bash 新进程，从 session cwd 起；`cd` 不跨命令保持，环境变量也不保持。
- **4.6 路径**：不限制。相对路径按 session cwd resolve；绝对路径照用。
- **4.7 结果格式**：
  - 文本 Read / Write / Edit / Bash **跟 pi**（Read 不打 `cat -n`；截断 2000 行或 50KB；Write/Edit 成功短句；Bash stdout+stderr 混排、留尾、非 0 当 error）。Read prompt 里「cat -n」那句不抄。
  - 图 / PDF / `.ipynb` 的结果形状 **跟 CC**（image block / 按页 / 按 cell）。

## 4. 工具行为

已拍，见上。

### §5 会话落盘（已拍）

- **5.1 路径**：不要年/月/日。两级目录：

  `~/.ki/sessions/<encoded-cwd>/<timestamp>_<id>/`

  `encoded-cwd` 跟 pi：绝对路径去掉盘符/`/`，`/` `\` `:` 换成 `-`，两边加 `--`。  
  `timestamp` 是 ISO 里 `:` `.` 换成 `-`。  
  `id` 是 uuidv7，也是 API 的 `session_id`。

- **5.2 目录里有什么**（pi 只有一个 jsonl；我们有 session 级配置，所以是目录）：
  - `events.jsonl`：append-only 树
  - `config.json`：session 级 skills / mcp 开关等
  - fork 时目录里已有的其它文件一并拷走
  - 不单独写 leaf 文件、不单独写 usage 汇总文件

- **5.3 jsonl**：跟 pi。
  - 第一行 header：`{type:session, version, id, timestamp, cwd, parentSession?}`
  - 之后每行 `{type, id, parentId, timestamp, ...}`
  - 需要的 type：`message` / `compaction` / `model_change`（以及 header）
  - entry `id`：8 位 hex，冲突再换（pi 的 `generateId`）
  - `message` 里就是那条 AgentMessage（assistant 带 `usage`）

- **5.4 leaf**：跟 pi。内存里一个 `leafId`；append 时新行 `parentId = leaf`，然后 `leaf = 新 id`。revert/regenerate 只改内存 leaf，再 append（旧行不删、不改）。落盘不写 leaf 指针。重载：文件里**最后一条非 header**就是当前 leaf（因为新行永远 append 在末尾）。

- **5.5 写盘**：按 pi，不按 PRD 原先那句「异步」。`message_end` / `tool_execution_end` 的内置订阅者里 **append 并 await 落盘完成**，循环等它写完再往下。不是丢 goroutine。循环本体仍然不直接写文件。

- **5.6 fork**：单起新目录，**全部迁移**：整份 `events.jsonl`（整棵树）、`config.json`、目录里其它文件。新 header：新 `id`，`parentSession` 指旧 session 路径。

- **5.7 token / 时间**：
  - token **跟 pi**：写在 assistant `message.usage`（input / output / cacheRead / cacheWrite / totalTokens）。compaction 自己的 usage 写在 compaction entry 上。不另开 usage 行。
  - 时间 pi 不落盘。补在同一条记录上，不另开类型：assistant 加 `latencyMs`、`ttftMs`；toolResult 加 `durationMs`（失败原因已有 `isError` + content）。

- **5.8 resume**：必须带 `session_id`。不带就新建。不自动接最近一次。

## 5. 会话落盘

已拍，见上。

### §6 Compaction（已拍）

- **6.1**：自动 + 手动都做。自动在 `agent_end` 上按阈值触发；HTTP 和 CLI 都能主动 compact。
- **6.2**：跟 pi。
  - 触发：`contextTokens > contextWindow - reserveTokens`，`reserveTokens = 16384`
  - 保留最近约 `keepRecentTokens = 20000`
  - summary 提示词用 pi 的 `SUMMARIZATION_SYSTEM_PROMPT`（结构化摘要，不继续对话）
- **6.3**：跟 pi。jsonl 追加 `{type:compaction, id, parentId, timestamp, summary, firstKeptEntryId, tokensBefore, usage?, details?}`。下次发给模型的是：重算后的分层 prompt + summary + 从 `firstKeptEntryId` 起的消息。
- **6.4**：compaction 之后供应商 **prompt cache 失效**，所以分层 prompt（身份 / tools / skills / AGENTS.md / pwd+date）必须重算再发出去，不能沿用 compact 前那份静态前缀。

## 6. Compaction

已拍，见上。

### §7 模型供应商（已拍）

- **7.1 中间表示**：就用 pi 的 `Message`（user / assistant / toolResult；content = text / image / thinking / toolCall）。流式事件用 pi 的 `AssistantMessageEvent`。实现对照 `/data/hgy/pi/packages/ai`。

- **7.2 协议 + provider**：三个协议都做——OpenAI Completions、OpenAI Responses、Anthropic Messages。适配层完全参照 pi 对应文件（含各家 compat：developer role、thinkingFormat、cache 字段等）。

  第一版内置 provider（CN + 海外都做）：

  | 对外名 | 对照 pi | 协议 | 备注 |
  |---|---|---|---|
  | `openai` | `openai` | Responses（官方）+ Completions（兼容） | `https://api.openai.com/v1` |
  | `anthropic` | `anthropic` | Messages | |
  | `zhipu` | `zai` | Completions | 海外 `https://api.z.ai/api/coding/paas/v4` |
  | `zhipu-cn` | `zai-coding-cn` | Completions | `https://open.bigmodel.cn/api/coding/paas/v4` |
  | `deepseek` | `deepseek` | Completions | `https://api.deepseek.com`（pi 只有这一份，官方也是一个入口） |
  | `dashscope` | 无同名；按 Completions 兼容加 | Completions | 海外 `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` |
  | `dashscope-cn` | 无同名（pi 的 qwen-token-plan 是另一套 MaaS） | Completions | `https://dashscope.aliyuncs.com/compatible-mode/v1` |

- **7.3 key**：写在 `~/.ki/ki.toml`。环境变量可覆盖同名 key。CLI `--model` / 请求体 `model` 只覆盖当次，不改文件。

- **7.4 默认模型（pi 怎么做）**：pi **没有写死「永远用某某模型」**。新建 session 且请求没带 model 时按顺序找：
  1. `settings` 里存的 defaultProvider / defaultModel（有 key 才用）
  2. 内置 `defaultModelPerProvider` 表，按 provider 顺序找**已经配了 key** 的那个默认 id
  3. 否则用第一个有 key 的模型
  4. resume 优先用 session 里上次的 provider/model
  
  Ki 同样做：不写死一个品牌默认。`ki.toml` 可配 `[defaults] provider` / `model`；没有则按上面 2–3；resume 用 session 里的。

- **7.5 thinking / prompt cache**：跟 pi，尽量做全。中间表示带 thinking block 和 thinking level；`usage` 带 cacheRead / cacheWrite。各协议按 pi 实现：
  - Anthropic：`cache_control`、thinking budget
  - OpenAI Responses / Completions：prompt cache + `reasoning_effort` 等
  - Completions compat：`thinkingFormat`（openai / deepseek / zai / qwen…）按 provider 打开
  - compact 后 cache 失效，分层 prompt 重算（§6.4）

## 7. 模型供应商

已拍，见上。


### §8 分层提示词（已拍）

- **8.1 身份**：沿用 pi 那段，把 pi/Pi 换成 ki/Ki：

  *You are an expert coding assistant operating inside ki, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.*

  pi 原文后面那大段「Pi documentation / docs 路径」第一版**不抄**（ki 没有对等文档树，抄了会教模型去读不存在的文件）。

- **8.2 Available tools / skills**：跟 pi 分层，不要和 CC 长 prompt 重复。
  - 发给供应商的 **tool definition** 用 CC 的 `description` / `prompt`（§4 已拍）。
  - system prompt 里「Available tools」只放 **一行 snippet**（跟 pi 的 `promptSnippet`），不是把 CC 长文再贴一遍。
  - skills 用 pi 的 `<available_skills>` XML（name / description / location），模型用 Read 去加载 SKILL.md。有 Read 才注入。
  - skills 发现跟 pi，路径换成 ki：`~/.ki/skills/`、`~/.agents/skills/`、项目 `.ki/skills/`、cwd 向上的 `.agents/skills/`。

- **8.3 AGENTS.md**：先加载全局（见 8.4），再从 **cwd 走到 git root（含）** 就停，不到文件系统根。不在 git 仓库里则只收 cwd 这一层 + 全局。
  - 每个目录只取一份，优先级跟 pi：`AGENTS.override.md` > `AGENTS.md` > `AGENTS.MD` > `CLAUDE.md` > `CLAUDE.MD`。
  - 放进 `<project_context>` / `<project_instructions path="...">`。
  - worktree 去重跟 pi（嵌套 worktree 不重复加载主仓那份）。

- **8.4 全局**：是。`~/.ki/AGENTS.md`（同一套文件名优先级，从 `~/.ki` 这个目录收）。

- **8.5 时间**：机器本地 timezone + date，拼在 cwd 后面。不可配。

组装顺序（compact 后整段重算，§6.4）：

1. 身份（8.1）
2. Available tools（短 snippet）
3. Available skills（XML）
4. AGENTS.md / CLAUDE.md（全局 + 从 cwd 走到 git root）
5. cwd + 本地日期时区
6. message history（不在 system prompt 里，在 messages）

## 8. 分层提示词

已拍，见上。


### §9 配置（大半已从前面推出）

已经明确的：

- **9.1 项目目录**：是 `.ki`（§8.2 已写项目 `.ki/skills/`，对齐 `~/.ki`）。
- **9.2 格式**：全局主配置是 **`~/.ki/ki.toml`**（§7.3）。MCP 跟规范用 JSON：`~/.ki/.mcp.json`、项目 `.ki/.mcp.json`（PRD / §2.1）。session 级是目录里的 **`config.json`**（§5.2）。项目级 ki 自己的键用 `.ki/ki.toml`，和全局同一套 toml。
- **9.3 第一版要写的**：不是占位。`ki.toml` 里至少：provider key / base url、`[defaults] provider` / `model`、session 根目录可覆写。skills、MCP 第一版就做。server 地址/token 另文件（§1 未决细节）。
- **9.4 文件名**：session 目录下 `config.json`：这个 session 的默认 `provider`/`model`，以及 skills/mcp 开关。
- **9.5 已定的覆盖**：环境变量覆盖 toml 里的 key；session 有自己的默认模型；显式 `--model` / 请求 `model` 改 **session** 默认并落盘，不改全局/项目 toml。

**9.4 / 9.5 方案（待你点头）：**

发现和连接定义在全局/项目；session 只裁剪「这次用哪些」，不改 key、不改 MCP 怎么起进程。

`~/.ki/ki.toml` / `.ki/ki.toml` 同一套键，项目盖全局：

```toml
[defaults]
provider = "anthropic"
model = "claude-sonnet-4-5"

[providers.anthropic]
api_key = "..."          # 也可用环境变量 ANTHROPIC_API_KEY 覆盖
base_url = ""            # 空则用内置

[providers.dashscope-cn]
api_key = "..."

[sessions]
root = ""                # 空则 ~/.ki/sessions

[compaction]
enabled = true
reserve_tokens = 16384
keep_recent_tokens = 20000
```

MCP 仍是规范 JSON，不进 toml。同名 server **项目盖全局**：

```json
{ "mcpServers": { "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] } } }
```

session `config.json`：skills/mcp 开关 + **这个 session 的默认模型**。缺省 skills/mcp = 发现到的全开。有 `only` 则只开名单里的；否则用 `disabled` 做黑名单。

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-4-5",
  "skills": { "disabled": ["legacy-deploy"] },
  "mcp": { "only": ["github"] }
}
```

新建 session：把当时解析出的默认（toml / 已有 key 的 provider 表）写进这份 `config.json`。  
之后跑循环用 **session 里的** `provider` / `model`。

显式指定（CLI `--model` 或 HTTP 请求体 `model`）：这一轮用新模型，并且 **写回这个 session 的 `config.json`**，jsonl 追加 `model_change`。下次不带 `--model` 也用新的。  
**不改** `~/.ki/ki.toml` / 项目 `.ki/ki.toml`（那是跨 session 的默认）。

`cwd` 仍是 session 创建时定的，请求里带 `cwd` 只对新建生效，不因为一次 prompt 改已有 session 的目录。

名字用 skill 的 frontmatter `name`、MCP 的 server key。

覆盖顺序（低 → 高）：

1. 内置默认（provider 表、compaction 阈值、session 根目录）
2. `~/.ki/ki.toml`
3. `<cwd>/.ki/ki.toml`（项目盖全局，deep merge 到表）
4. 环境变量：只盖 **key / base_url**（`ANTHROPIC_API_KEY` 这类），不盖 defaults.model
5. session `config.json`：`provider` / `model`（这个 session 的默认）+ skills / mcp 裁剪
6. CLI `--model` / 本次请求 `model`：覆盖第 5 层，并写回 session `config.json` + `model_change` 行。不写全局/项目 toml

skills 目录是并集：`~/.ki/skills`、`~/.agents/skills`、`.ki/skills`、向上的 `.agents/skills`，再套 session 开关。

第一版不做 pi 那种项目 trust 询问（工具已全放行）；项目 `.ki` 直接加载。

## 9. 配置

已明确 + 上面方案，等确认 9.4/9.5。


### §10 工程（方案）

对照整份 PRD + 前面已拍项：一个 Go 二进制，CLI 调本机 HTTP；循环只 emit；写盘/计量/compaction 是内置订阅者；server 同时跑多个 session。包按这个边界切，不要把写盘或 HTTP 写进 loop。

**10.1 module**

仓库还没有远端约定。第一版：

```
module ki
```

以后要发 HTTP SDK / 给别人 import 再改成完整路径（例如 `github.com/<org>/ki`），内部 import 跟着改一次即可。不要先发明一个不存在的 GitHub 地址。

**10.2 包怎么切**

一个 `cmd/ki`，两种入口共用同一份 `internal/server`。

```
cmd/ki/                 唯一二进制：ki / ki serve / ki -d
internal/
  server/               HTTP：session CRUD、prompt、SSE、abort、token
                        管「多个 session 并行、同一 session 一个 run」
  loop/                 主循环 + 10 个事件 + 拦截 hook 点
                        不写盘、不认 HTTP、不拼 prompt 文件
  session/              jsonl 树、leaf、fork、config.json
                        内置订阅者：message_end / tool_execution_end await append
  tools/                Read / Write / Edit / Bash（含 run_in_background）
  provider/             pi 的 Message IR；Completions / Responses / Messages
                        openai、anthropic、zhipu(+cn)、deepseek、dashscope(+cn)
  prompt/               分层 prompt：身份、snippet、skills XML、AGENTS.md、cwd+date
  compact/              阈值、summary、compaction entry；触发后通知 prompt 重算
  config/               合并 ki.toml / 环境变量 / session config.json
  skills/               发现 SKILL.md
  mcp/                  读 .mcp.json、拉起 server、当工具挂进 loop
  cli/                  解析 flag、起/连 server、SSE 打到终端
```

依赖方向（不许反着 import）：

```
cli → server → loop
               ↘ session / tools / provider / prompt / compact
server → config / skills / mcp
loop 不 import server、cli
```

HTTP 先这些就够（以后 WebUI / 外语言 SDK 打同一套）：

| 方法 | 路径 | 作用 |
|---|---|---|
| POST | `/v1/sessions` | 新建；可选 cwd / model |
| GET | `/v1/sessions/{id}` | 读 header + 当前 leaf / model |
| POST | `/v1/sessions/{id}/prompt` | 发一条；显式 model 写回 session |
| GET | `/v1/sessions/{id}/events` | SSE，订这次 run 的 AgentEvent |
| POST | `/v1/sessions/{id}/abort` | 取消当前 run |
| POST | `/v1/sessions/{id}/compact` | 手动 compaction |
| POST | `/v1/sessions/{id}/fork` | 整目录迁移 |

鉴权：`Authorization: Bearer <token>`。

§1 留下的端口/token 一并定：

- 常驻 / `-d`：`127.0.0.1:19800`，可在 `ki.toml` `[server] addr` 改
- 本进程临时起：`127.0.0.1:0`（随机）
- 地址 + token 写 `~/.ki/server.json`（`ki -d` / `ki serve` 写；client 读这个连）

**10.3 日志**

三层，别混：

| 层 | 打哪 | 记什么 |
|---|---|---|
| 进程 | **stderr**（`ki serve` 前台能看见；`-d` 可重定向） | 启动、listen、请求、panic、供应商/MCP 进程挂了 |
| 全局文件 | `~/.ki/ki.log` | 同上的持久副本，方便后台 server |
| 会话事实 | **只进 `events.jsonl`** | token、ttft、工具耗时/失败。不另写 session/debug.log 当账本 |

约定：

- 用标准库 `log/slog`，默认 info；`KI_DEBUG=1` 或 `ki.toml` `[log] level` 调 debug
- **禁止**把 API key、Bearer token、完整文件内容打进进程日志（工具结果已经在 jsonl / SSE 里）
- 循环里 hook 抛错：记一条 warn，按已拍语义继续（`before_tool` 除外，失败封闭）

测试跟包走：`internal/loop`、`session`、`provider` 的协议适配必须有测试；CLI 用 httptest 打 `server`。

## 10. 工程

方案见上。10.1 用 `module ki`；10.2 按层切、loop 不依赖 HTTP；10.3 stderr + `~/.ki/ki.log`，账本只在 jsonl。端口 `19800`、token 文件 `~/.ki/server.json`。

