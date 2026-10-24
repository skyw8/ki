# System Prompt

`internal/prompt.Build` 从 session 的资源快照和本轮运行输入纯渲染 system prompt，不直接
读取磁盘或探测系统。

## 分层

| 层 | 内容 |
|---|---|
| 1. 身份与职责 | 说明模型运行在 ki agent harness 中，可以读取文件、执行命令、修改代码和创建文件。subagent 身份、派它的 agent 和 canonical task path 放在 child 首条 user 信封中；通常复用 parent 的工具前缀，但不提供仅主会话可用的异步用户提问。工具集合不随 busy/idle 改变；执行容量在 agent.Controller 的 admission 边界检查，没有固定 depth 限制。 |
| 2. Ki 配置位置 | 存在 `KI_HOME` 时列出 `ki.toml`、`skills/`、`models.json`、`credentials.json`、扩展目录、项目级 `<cwd>/.ki/`，以及 `ki config path`。 |
| 3. 可用工具 | 逐项输出本轮工具的名称和简短说明；没有工具时输出 `(none)`。包括内置工具、已绑定的扩展工具，并补充项目可能提供其他自定义工具。 |
| 4. 通用行为约束 | 要求回答简洁，并在操作文件时清晰展示路径。有 `request_user_input_async` 时追加异步提问使用规则：尽早提问、继续不依赖答案的工作；必要回答未到前不执行依赖工作；时间与预选项不代表回答或批准。 |
| 5. 内置追加指令 | `prompt.DefaultAppendSystemPrompt`：优先使用 read/grep/glob；shell 搜索优先 rg/fd 而不是 grep/find（支持的目标由 Ki 捆绑并放入 PATH，不支持的目标使用可用的平台搜索命令；默认尊重 `.gitignore`，`-H`/`-I` 有说明）。这是 harness 层规则，无条件输出。工具名只发布 canonical snake_case，接受 PascalCase 调用别名。 |
| 6. operator 追加指令 | 按来源顺序读取 `{KI_HOME}/prompt/APPEND_SYSTEM.md`（global）与 `<cwd>/.ki/prompt/APPEND_SYSTEM.md`（project）。两者**叠加**而非覆盖，global 在前、project 在后，各自整份文件作为一个块渲染（空文件或只有空白则跳过）。路径由 `resources.AppendSystemPromptPath` 统一解析，读取与设置页写入共用同一个函数。 |
| 7. 扩展追加 | 启用的全局 extension `prompt.append` 文件，按扩展名序，每段 `<extension_instructions name="…">`。 |
| 8. Skills | 仅当本轮存在直接 `read` 工具且至少有一个启用的 skill 时输出。每个 skill 包含名称、描述和 `SKILL.md` 路径，并说明按需读取及相对路径解析规则。 |
| 9. 项目指令 | 输出 AGENTS/CLAUDE 文件的路径和完整内容。先加载 `{KI_HOME}` 下的全局文件，再按 git 仓库根目录到 cwd 的顺序加载；不在 git 仓库中时只加载 cwd。每个目录按 `AGENTS.override.md`、`AGENTS.md`、`AGENTS.MD`、`CLAUDE.md`、`CLAUDE.MD` 的优先级选取一个文件。 |
| 10. 运行系统 | OS（macOS、Windows、Linux 或 WSL）和架构。 |
| 11. 当前环境 | session cwd、资源快照创建日期和时区。 |

第 5–7 层合称「追加栈」，由 `prompt.AppendSection` 按同一顺序渲染成连续文本。设置页的
生效预览（`GET /v1/prompt/append` 的 `effective`）直接返回它，`Build` 与 `AppendSection`
共用 `appendBlocks`，两者顺序只能一起改。

Prompt templates 不直接进入 system prompt，只用于 slash command 展开。模板中的 `$1`、
`$@` 等占位符替换为命令参数；没有占位符时参数不会自动追加。扩展工具通过「可用工具」层
体现。

## 设置页编辑

`GET /v1/prompt/append?workspaceId=` 列出全部来源：内置层（只读）、两个可写文件（global、
project），以及已启用扩展的只读层，同时返回 `effective`。

`PUT`/`DELETE /v1/prompt/append` 按 `source` 名称写入或删除某个文件，客户端只传来源名、
不传路径——写入目标由 `cfg.Home` 与 `workspacePath(workspaceId)` 解析。workspace 未注册或
不存在时返回 400，project 源还必须带 `workspaceId`。写入上限
`maxAppendSystemPromptBytes`（64 KiB，超限 `413`），空文本用 `PUT` 拒绝（请用 `DELETE`
删文件）。目录 `0700`、文件 `0600`，临时文件 + rename 原子替换，避免并发 reload 读到写了
一半的 prompt。写成功后执行一次全局 reload，空闲 session 下一轮即生效。

内置层与扩展层在设置页只读：前者是 harness 不变量，后者跟随扩展包更新。要让某段文字不再
出现，删对应文件即可。

会话信息页在末尾展示该会话最近一次请求实际发送的完整 system prompt：
`GET /v1/sessions/{id}?fields=runtime,system` 从最新 `request_header` 行读回 `system` 原文
（按 offset 补读该行，不重新渲染），因此与模型收到的内容逐字节一致；从未运行过的会话为空。

## Reload

以下情况执行全局 reload：

- `POST /v1/reload` 或 `/reload`；
- tools、skills 或 extensions 开关修改成功；
- 追加 system prompt 的文件写入或删除成功；
- 自动或手动 compaction 成功。

Reload 清空空闲 session 的资源快照并重载扩展视图。正在跑 prompt 或 compact 的 session 把
reload 排到 `occupy` 对应的 `release` 之后。删除 session 时只清理该 session 的快照。

Compaction 会重建模型上下文，旧 prompt 缓存可视为失效；同步 reload 让下一轮使用最新的
项目指令、skills、prompt templates、扩展配置和运行环境。

## 缓存与动态计算

`resources.Loader` 以真实 session ID 为唯一缓存键。首次加载生成完整 `Snapshot`，包含 OS、
架构、KI_HOME、cwd、日期和时区，AGENTS/CLAUDE 文件，已存在的 global / project 追加文件
（各自 `Source`、`Path`、`Text`），skills 元数据，prompt templates，以及已发现的全局扩展
声明。同一 session 后续消息复用该快照，直到 reload。设置页没有 session，使用不缓存的
`Scan(cwd)`。

每轮动态输入只有当前工具列表（模型能力、内置工具开关或扩展状态可能改变它）和 Skills
开关（决定展示哪些 skills）。Tools 开关在 `Set.Build` 之后过滤内置工具；Extension 开关不
直接传入 `Build`，但会影响绑定到本轮的扩展工具。`Build` 每轮仍执行字符串组装，但不再读取
资源或重新计算运行环境。
