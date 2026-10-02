# MCP 与 Deferred 工具发现

Ki 使用官方 Go SDK [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)，当前固定为 v1.8.0。MCP 连接、初始化、分页 `tools/list`、通知和 `tools/call` 由 SDK 处理，不另写 JSON-RPC 客户端。

## 配置

MCP server 配置独立于 `ki.toml`，使用两个 JSON 文件：

- 全局：`{KI_HOME}/mcp.json`（默认 `~/.ki/mcp.json`）。
- 项目：`<cwd>/.ki/mcp.json`，按会话/工作区的实际 cwd 读取，不是 daemon 的启动目录。

两个文件都可省略。先加载全局，再加载项目；**项目中的同名服务器完整替换全局配置**，不逐字段合并凭据或参数。不同名字并存，名字区分大小写。文件使用 Ki 的版本头和严格 JSON 校验；未知字段、畸形配置和更新版本拒绝加载，不回写用户文件。`ki config path` 显示这两个路径。旧的 TOML `mcp_servers` 配置不再支持。

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

`command` 必须是可执行程序，不是 shell 表达式；不能同时指定 `url`。`cwd` 可指定当前平台的绝对工作目录。HTTP 使用 Streamable HTTP，不是旧 SSE-only transport。

`enabled` 缺省为 true；`required` 缺省为 false。启动与调用超时的零值分别使用 10 秒和 60 秒，不表示无限等待。`enabledTools` 未配置表示全部允许，空列表表示不允许任何工具；`disabledTools` 优先。过滤比较 MCP 原始工具名，不是模型看到的名字。

stdio 不经 shell 启动，默认仅继承 PATH、HOME、临时目录等运行环境，不继承整个 daemon 的 provider 凭据。`envVars` 显式继承其它变量；`env` 对象可提供固定覆盖，例如 `"env": {"DOCS_MODE": "readonly"}`；环境变量名称的大小写原样保留。

### Settings 与 Session Info

Settings → MCP 显示服务器来源、传输方式和启用状态，可分别切换服务器。开关保存到全局 `toggles.json` 的 `mcp.disabled`，按原始服务器名作用于所有工作区，不会修改配置文件或覆盖工具/Code Mode 开关。更新当前列表时保留其它项目不可见服务器的禁用状态。配置文件中 `enabled:false` 是硬禁用，界面不能将其重新启用。

配置文件与开关在下一次运行捕获快照；进行中的运行继续使用原能力和连接，不被设置变更中断。禁用的服务器不启动、不连接，`required` 也不强制启动已禁用的服务器。设置目录只读取配置及已缓存元数据，不为了显示列表调用 MCP。

Session Info 的 MCP 小节显示当前会话 cwd 对应的下一次运行配置与缓存工具数量，不展示 env、headers、token 或命令参数。缓存未建立时不伪装成已连接或已验证。只有可用服务器的工具才能进入 Deferred 搜索目录和 Code Mode。

MCP 程序是用户配置的受信任程序，**不承诺 OS 文件系统/网络沙箱**。HTTP 支持静态 headers 与环境 bearer token，凭据不转发到重定向地址。本版不提供 MCP OAuth 登录 UI，也不添加 MCP resources/prompts 浏览工具。

## 生命周期与执行

- server 按不可变配置缓存 MCP Manager，最多 32 个快照；occupy 持有引用，只有空闲快照能被淘汰。发布工具设置目录不建立连接，首次 occupy 获取目录时按需连接。开关变更不关闭进行中占用的连接；空闲缓存由淘汰或 shutdown 回收。
- SDK `tools/list` 分页建立目录。收到工具列表变更通知后，在下次 occupy 刷新；连接失效也仅在下次发现时重连，不重放已执行的调用。
- 每次 occupy 捕获允许工具与 schema 的固定快照；活动 callback 不跨边界切换身份或能力。
- 可选服务器故障保留其它服务器工具并报告警告；required 服务器故障使本次运行失败。取消与 shutdown 会取消并 join 宿主持有的操作，最后关闭 SDK session 和 stdio 进程树。
- 远端取消通知由 SDK 尽力发送；宿主取消/join 不代表远端副作用已停止。连接关闭也不保证远程业务事务回滚，Ki 不自动重试或重放这些调用。
- 模型名为 `mcp__<server>__<tool>`，非法字符、长名称及别名冲突使用确定性哈希消歧；协议调用始终使用原始服务器工具名。

工具执行走普通 ToolDispatcher 的校验、BeforeTool、执行身份、AfterTool、输出、遥测和事件链；Code Mode 嵌套调用也不绕过这些边界。输入 schema 使用 `google/jsonschema-go` 验证；不加载远程 schema 引用。

MCP text/image/audio 投影到 Ki Content；resource/resource_link 保持为惰性 JSON 文本，不读取 URI 或文件。结构化结果和协议元数据保留在 Details；正文不在 Details 中重复保存，避免 AfterTool 重写/脱敏 Content 后仍泄露旧的正文副本。宿主策略若需修改结构化数据或元数据，必须同样处理 Details。最终输出仍受普通 spool 预算约束，JS 中间结果使用独立预算。

## search_tool 与 Deferred

`search_tool` 是可切换的内置工具，默认允许，出现在 **Settings → Tools**。当存在本次运行允许的 MCP 工具，且工具开关/activeTools 保留这个入口时：

1. MCP 工具进入固定执行 registry，但完整声明标为 Deferred，不进入首轮 provider tools 或 exec 描述。
2. 模型先看到 `search_tool` 和有界来源摘要；不会预先收到全部 MCP 参数 schema。
3. `search_tool({query, limit?})` 在宿主对工具名称、描述、来源和参数做本地词法 BM25 搜索，不访问 MCP 服务器搜索接口，也不使用 embedding。
4. 成功结果返回匹配工具的完整 schema。下一次模型请求按已接受的搜索结果补充声明；重载时从配对的 assistant search 调用与成功 toolResult 中恢复，始终重新对照当前允许目录。

**没有可用的 `search_tool`，就没有 Deferred。** 关闭该工具或用 activeTools 排除它后，允许的 MCP 工具直接进入 provider tools；Code Mode-only 则直接进入 exec 工具描述。没有 MCP 工具时，不发布空搜索入口。

三个 provider 协议都使用普通 function tool `search_tool`，不是 Codex 专用的 Responses `tool_search_call/tool_search_output`。搜索输出在工具正文中，后续真实 schemas 在既有 request_header/模型请求中；没有新增 REST 路由或伪造 provider 消息。成功披露只取 post-hook、实际模型历史中的结果；失败或被删除的搜索结果不激活声明。过时/被禁用名称不能通过历史恢复重新获取能力。

## 与 Code Mode 配合

`mixed` 保留直接工具和 exec/wait；`only` 保留 exec/wait **以及直接的 search_tool**。only 搜索后把声明加入 exec 描述，不把 MCP 工具改为直接入口。

Deferred 是声明披露优化，**不是权限加载机制**：

- JS 的 `tools` 和 `ALL_TOOLS` 在每个 exec 创建时已有全部允许的嵌套 MCP 工具。
- 知道名称和参数即可调用，不要求先 search；搜索不授予新权限。
- `search_tool` 不进入 JS 的 tools/ALL_TOOLS；模型直接调用它。JS 也可过滤 ALL_TOOLS，再只输出所需元数据。
- worker 不直接持有 MCP client；真实调用仍由父进程的固定 dispatcher 路由。

搜索默认 8 条，limit 范围 1–32，query 最多 4096 UTF-8 字节；索引每工具元数据最多 64KiB，来源摘要总计 4KiB。结果正文最多 12KiB，完整 schema 放不下时明确报告省略，绝不截断参数定义后冒充完整 schema，也不暗中激活未返回的声明。

## 测试

MCP adapter 使用 SDK 的真实 HTTP server 与同一 test binary 的 stdio server 验证协议、分页、过滤、验证、取消与回收。服务端矩阵覆盖三个模型 API、三个 Code Mode 模式、搜索关闭回退、搜索后声明/历史恢复，以及未搜索前的 MCP JS 调用经过 AfterTool 和嵌套审计；不增加重复构建或浏览器用例。

强制运行实测 MCP SDK 包约 0.08 秒，发现层约 0.01 秒；Code Mode/MCP 服务端合并用例约 1.08 秒（原 Code Mode 约 0.33 秒），新增成本来自真实 HTTP/worker/AfterTool sidecar 边界。完整 fresh WebUI + embed 回归约 54.5 秒，相关 race 与三个支持目标的无 CGO 构建均通过。耗时为当前机器观测，不是性能保证。

独立 JSON 配置与 Settings 增量覆盖合并/来源、严格解码、版本拒绝、跨工作区隔离、原始名称开关、禁用服务器零连接，以及进行中快照不被新配置打断。配置包约 0.013 → 0.027 秒；受影响服务端组合约 0.790 → 0.987 秒，增加约 0.20 秒来自带可观察 gate 的 SDK/缓存边界测试。单个隔离浏览器用例约 3.5 秒，使用真实配置与 PATCH，覆盖持久化、失败恢复、Session Info、390px/44px 控件和元数据读取不启动 stdio；完整响应式矩阵也包含 MCP 设置页。本次 fresh WebUI + embed 完整回归约 52.6 秒，未观察到整体墙钟时间回退。
