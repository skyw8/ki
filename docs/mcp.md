# MCP 与 Deferred 工具发现

Ki 使用官方 Go SDK [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
（当前 v1.8.0）。MCP 连接、初始化、分页 `tools/list`、通知和 `tools/call` 由 SDK 处理，不另写
JSON-RPC 客户端。

## 配置

MCP server 配置独立于 `ki.toml`，使用两个 JSON 文件：

| 位置 | 作用域 |
|---|---|
| `{KI_HOME}/mcp.json`（默认 `~/.ki/mcp.json`） | 全局 |
| `<cwd>/.ki/mcp.json` | 按会话/工作区的实际 cwd 读取，不是 daemon 的启动目录 |

两个文件都可省略。先加载全局，再加载项目；**项目中的同名服务器完整替换全局配置**，不逐
字段合并凭据或参数。不同名字并存，名字区分大小写。文件使用 Ki 的版本头和严格 JSON 校验；
未知字段、畸形配置和更新版本拒绝加载，不回写用户文件。`ki config path` 显示这两个路径。旧
的 TOML `mcp_servers` 配置不再支持。

```json
{
  "version": 1,
  "mcpServers": {
    "local_docs": {
      "command": "docs-mcp-server",
      "args": ["--stdio"],
      "envVars": ["DOCS_API_TOKEN"],
      "startupTimeoutSeconds": 10,
      "toolTimeoutSeconds": 60,
      "maxConcurrent": 8,
      "disabledTools": ["delete_document"],
      "required": false
    },
    "remote_docs": {
      "url": "https://example.com/mcp",
      "bearerTokenEnv": "REMOTE_DOCS_TOKEN",
      "httpHeaders": {"X-Workspace": "example"}
    }
  }
}
```

### 传输与超时

`command` 必须是可执行程序，不是 shell 表达式；不能同时指定 `url`。`cwd` 可指定当前平台的
绝对工作目录。HTTP 使用 Streamable HTTP，不是旧 SSE-only transport。

`enabled` 缺省为 true；`required` 缺省为 false。启动与调用超时的零值分别使用 10 秒和 60
秒，不表示无限等待。

### 并发策略

`maxConcurrent` 为同一服务器在共享调度器中的并发上限，零值使用 8，负值拒绝。
缺省仅 `readOnlyHint: true` 的工具可并行，其它工具在所属服务器组独占。可选 `parallel`
布尔值显式覆盖该服务器的并行准入策略；它不会改写工具真实的只读 hint 或授予新权限。
direct 和 Code Mode 嵌套调用共享同一策略，活动占用保持原配置。

### 工具过滤

`enabledTools` 未配置表示全部允许，空列表表示不允许任何工具；`disabledTools` 优先。过滤
比较 MCP 原始工具名，不是模型看到的名字。

### 环境

stdio 不经 shell 启动，默认仅继承 PATH、HOME、临时目录等运行环境，不继承整个 daemon 的
provider 凭据。`envVars` 显式继承其它变量；`env` 对象可提供固定覆盖，例如
`"env": {"DOCS_MODE": "readonly"}`；环境变量名称的大小写原样保留。

### 安全边界

MCP 程序是用户配置的受信任程序，**不承诺 OS 文件系统/网络沙箱**。HTTP 支持静态 headers 与
环境 bearer token，凭据不转发到重定向地址。本版不提供 MCP OAuth 登录 UI，也不添加 MCP
resources/prompts 浏览工具。

## 生命周期

server 按不可变配置缓存 MCP Manager，最多 32 个快照；occupy 持有引用，只有空闲快照能被
淘汰。发布工具设置目录不建立连接，首次 occupy 获取目录时按需连接。开关变更不关闭进行中
占用的连接；空闲缓存由淘汰或 shutdown 回收。

- SDK `tools/list` 分页建立目录。收到工具列表变更通知后，在下次 occupy 刷新；连接失效也
  仅在下次发现时重连，不重放已执行的调用。
- 每次 occupy 捕获允许工具与 schema 的固定快照；活动 callback 不跨边界切换身份或能力。
- 可选服务器故障保留其它服务器工具并报告警告；required 服务器故障使本次运行失败。取消与
  shutdown 会取消并 join 宿主持有的操作，最后关闭 SDK session 和 stdio 进程树。
- 远端取消通知由 SDK 尽力发送；宿主取消/join 不代表远端副作用已停止。连接关闭也不保证
  远程业务事务回滚，Ki 不自动重试或重放这些调用。

模型名为 `mcp__<server>__<tool>`，非法字符、长名称及别名冲突使用确定性哈希消歧；协议调用
始终使用原始服务器工具名。

工具执行走普通 `ToolDispatcher` 的校验、BeforeTool、执行身份、AfterTool、输出、遥测和事件
链；Code Mode 嵌套调用也不绕过这些边界。输入 schema 使用 `google/jsonschema-go` 验证；不
加载远程 schema 引用。

## 结果投影

MCP text/image/audio 投影到 Ki Content；resource/resource_link 保持为惰性 JSON 文本，不读取
URI 或文件。结构化结果和协议元数据保留在 Details；正文不在 Details 中重复保存，避免
AfterTool 重写/脱敏 Content 后仍泄露旧的正文副本。宿主策略若需修改结构化数据或元数据，
必须同样处理 Details。最终输出仍受普通 spool 预算约束，JS 中间结果使用独立预算。

## search_tool 与 Deferred

`search_tool` 是可切换的内置工具，默认允许，出现在 **Settings → Tools**。当存在本次运行
允许的 MCP 工具，且工具开关/activeTools 保留这个入口时：

1. MCP 工具进入固定执行 registry，但完整声明标为 Deferred，不进入首轮 provider tools 或
   exec 描述。
2. 模型先看到 `search_tool` 和有界来源摘要；不会预先收到全部 MCP 参数 schema。
3. `search_tool({query, limit?})` 在宿主对工具名称、描述、来源和参数做本地词法 BM25 搜索，
   不访问 MCP 服务器搜索接口，也不使用 embedding。
4. 小声明直接返回完整 schema；较大声明返回 `schema_pages` 取页提示。用
   `search_tool({name, cursor?})` 获取完整工具 Spec JSON 的连续 UTF-8 分片，不能与 query/limit
   混用。`schema_page` 含 name、digest、offset、total_bytes、fragment 和可选 next_cursor；
   按 next_cursor 继续直到它消失，cursor 绑定当前声明 SHA-256 和字节偏移。
5. 下一次模型请求只为真正完整交付的声明补充直接工具 schema；重载从配对成功结果的实际
   正文重建，逐段核对当前目录、digest、偏移与连续完整内容，不信任 Details 或孤立最后页。

**没有可用的 `search_tool`，就没有 Deferred。** 关闭该工具或用 activeTools 排除它后，允许
的 MCP 工具直接进入 provider tools；exec 仍只保留短元数据和引用。没有 MCP 工具时，不发布空搜索入口。

三个 provider 协议都使用普通 function tool `search_tool`，不是 Codex 专用的 Responses
`tool_search_call`/`tool_search_output`。搜索输出在工具正文中，后续真实 schemas 在既有
request_header/模型请求中；没有新增 REST 路由或伪造 provider 消息。成功披露只取 post-hook、
实际模型历史中的结果；失败或被删除的搜索结果不激活声明。过时/被禁用名称不能通过历史恢复
重新获取能力。

### 搜索预算

搜索默认 8 条，limit 范围 1–32，query 最多 4096 UTF-8 字节；索引每工具元数据最多 64 KiB，
来源摘要总计 4 KiB。结果正文最多 12 KiB，完整 schema 放不下时明确报告省略，绝不截断参数
定义后冒充完整 schema，也不暗中激活未返回的声明。大声明改用取页提示而非永久省略；
每页 JSON 正文仍不超过 12 KiB，转义与 UTF-8 边界计入预算。

Manager 缓存最多 8 个允许工具投影的搜索索引，按显式不可变工具身份与顺序复用，并以
序列化声明和索引项的估算成本控制在 32 MiB；超大索引正常使用但不缓存。list_changed、
连接重建或 activeTools 改变后，下一次 occupy 选择新索引，旧占用不受影响。普通可变工具
未声明不可变身份时不复用。adapter 参数投影用递归副本，不能反向修改目录。

## 与 Code Mode 配合

Code Mode 固定为 `mixed`，保留直接工具和 `exec`/`wait`；完整披露后把 schema 加入 provider
tools，exec 只增加短元数据与直接声明指引，避免两份完整 schema。

Deferred 是声明披露优化，**不是权限加载机制**：

- JS 的 `tools` 和 `ALL_TOOLS` 在每个 exec 创建时已有全部允许的嵌套 MCP 工具。
- 知道名称和参数即可调用，不要求先 search；搜索不授予新权限。
- `search_tool` 不进入 JS 的 `tools`/`ALL_TOOLS`；模型直接调用它。JS 也可过滤 `ALL_TOOLS`，
  再只输出所需元数据。
- worker 不直接持有 MCP client；真实调用仍由父进程的固定 dispatcher 路由。

## Settings 与 Session Info

Settings → MCP 显示服务器来源、传输方式和启用状态，可分别切换服务器。开关保存到全局
`toggles.json` 的 `mcp.disabled`，按原始服务器名作用于所有工作区，不会修改配置文件或覆盖
工具开关。更新当前列表时保留其它项目不可见服务器的禁用状态。配置文件中 `enabled:false`
是硬禁用，界面不能将其重新启用。

配置文件与开关在下一次运行捕获快照；进行中的运行继续使用原能力和连接，不被设置变更中断。
禁用的服务器不启动、不连接，`required` 也不强制启动已禁用的服务器。设置目录只读取配置及
已缓存元数据，不为了显示列表调用 MCP。

Session Info 的 MCP 小节显示当前会话 cwd 对应的下一次运行配置与缓存工具数量，不展示 env、
headers、token 或命令参数。缓存未建立时不伪装成已连接或已验证。只有可用服务器的工具才能
进入 Deferred 搜索目录和 Code Mode。

## 测试

MCP adapter 使用 SDK 的真实 HTTP server 与同一 test binary 的 stdio server 验证协议、分页、
过滤、验证、取消与回收。服务端矩阵覆盖三个模型 API、固定 mixed 的 Code Mode、搜索关闭
回退、搜索后声明/历史恢复，以及未搜索前的 MCP JS 调用经过 AfterTool 和嵌套审计。

独立 JSON 配置与 Settings 用例覆盖合并/来源、严格解码、版本拒绝、跨工作区隔离、原始名称
开关、禁用服务器零连接，以及进行中快照不被新配置打断。浏览器用例使用真实配置与 PATCH，
覆盖持久化、失败恢复、Session Info、390px/44px 控件，并断言元数据读取不启动 stdio。
