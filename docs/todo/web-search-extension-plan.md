# deep-web-search Extension 接入方案

> 状态：已实现，并完成 TypeScript 构建、扩展自测和真实供应商烟测；本文同时保留设计、验收和后续增强项。
>
> 目标组合：Codex OAuth、Exa、TinyFish、DuckDuckGo。
>
> 参考实现：[pi-web-access](../../../pi-web-access/README.md) 0.25.0；参考宿主契约：[extension.md](../extension.md)、[provider.md](../provider.md)、[events.md](../events.md)。

## 1. 目标与取舍

实现一个名为 `deep-web-search` 的 Ki extension，提供统一的 `deep_web_search` 能力，并保留
后续网页抓取、来源核验和摘要审阅的空间。

| Provider | 认证 | 主要角色 |
|---|---|---|
| Codex | 复用现有 codex-oauth OAuth | 专业搜索、模型回答、最终综合 |
| Exa | 无 Key 时 MCP；有 exaApiKey 时 REST | 语义检索、相关网页发现、可选全文 |
| TinyFish | tinyfishApiKey | 实时网页搜索和正文抓取 |
| DuckDuckGo | 无认证 | 无 Key、无自建服务的普通关键词兜底 |

不把四个 provider 当作 Ki 的四个模型供应商。只有 codex-oauth 继续注册
openai-codex 模型 provider；Exa、TinyFish、DuckDuckGo 是搜索 extension 内部
的 HTTP/MCP client，不进入模型选择器。

### 1.1 不做的事情

- 不接入 Brave、SearXNG 或其他未选定 provider。
- Codex OAuth 登录仍由 codex-oauth 负责；deep-web-search 直接读取 KI_HOME/credentials.json 中
  的 openai-codex credential，不新增第二套登录。
- 不把 OAuth token 放进 extension bus、工具参数、session entry 或 WebUI；token 只在
  deep-web-search sidecar 的请求内存中短暂存在。
- 不为搜索结果新增公开 REST 查询路由；结果通过 tool result、现有 session/SSE 和 custom jsonl entry 传递。
- 不让四个 provider 各自生成一份最终答案后再拼接；provider 答案和来源分开，最终由一个总结阶段处理。

## 2. 推荐的整体架构

### 2.1 包和运行时

新增：

~~~text
extensions/
└── deep-web-search/
    ├── extension.json
    ├── package.json
    ├── package-lock.json
    ├── src/
    │   ├── main.ts
    │   ├── rpc.ts
    │   ├── config.ts
    │   ├── providers/
    │   │   ├── codex.ts
    │   │   ├── exa.ts
    │   │   ├── tinyfish.ts
    │   │   └── duckduckgo.ts
    │   ├── aggregate.ts
    │   ├── normalize.ts
    │   ├── fetch.ts
    │   ├── cache.ts
    │   ├── evidence.ts
    │   └── locales/
    │       ├── en.json
    │       └── zh.json
    └── dist/main.js
~~~

建议使用 TypeScript/Node sidecar，原因是 [pi-web-access](../../../pi-web-access/package.json)
和各 provider parser 已经是 TypeScript，可逐段移植并用 fixture 测试。sidecar 是全局
进程，工具请求带 sessionId，符合 Ki 当前的 process-level extension runtime 设计。

发布包应包含编译后的 dist 和锁定依赖，避免每次 Ki 启动都联网安装依赖。开发环境
可以使用 runtime.install，但生产启动不应依赖安装成功。

### 2.2 extension.json 初步形态

~~~json
{
  "name": "deep-web-search",
  "version": "0.1.0",
  "description": "Four-provider web search and source processing",
  "capabilities": ["tool", "settings"],
  "i18n": {
    "defaultLocale": "en",
    "resources": { "en": "locales/en.json", "zh": "locales/zh.json" }
  },
  "config": {
    "schema": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "exaApiKey": {"type": "string", "secret": true},
        "exaMode": {"type": "string", "enum": ["auto", "api", "mcp"]},
        "tinyfishApiKey": {"type": "string", "secret": true},
        "codexModel": {"type": "string"},
        "provider": {"type": "string", "enum": ["auto", "all", "codex", "exa", "tinyfish", "duckduckgo"]},
        "providerToggles": {
          "type": "object",
          "additionalProperties": false,
          "properties": {
            "codex": {"type": "boolean"},
            "exa": {"type": "boolean"},
            "tinyfish": {"type": "boolean"},
            "duckduckgo": {"type": "boolean"}
          }
        },
        "maxResults": {"type": "integer"},
        "fetchContent": {"type": "boolean"},
        "summaryModel": {"type": "string"},
        "queryRewriteModel": {"type": "string"},
        "summaryGenerationDeadlineMs": {"type": "integer", "minimum": 1000},
        "curatorTimeoutSeconds": {"type": "integer", "minimum": 5},
        "autoOpenBrowser": {"type": "boolean"},
        "workflow": {"type": "string", "enum": ["none", "summary-review", "auto-summary"]}
      }
    },
    "defaults": {
      "codexModel": "gpt-5.5",
      "provider": "all",
      "exaMode": "auto",
      "providerToggles": {
        "codex": true,
        "exa": true,
        "tinyfish": true,
        "duckduckgo": true
      },
      "maxResults": 5,
      "fetchContent": false,
      "summaryModel": "openai-codex/gpt-5.5",
      "queryRewriteModel": "",
      "summaryGenerationDeadlineMs": 30000,
      "curatorTimeoutSeconds": 20,
      "autoOpenBrowser": true,
      "workflow": "none"
    }
  },
  "runtime": {
    "kind": "rpc",
    "command": "node",
    "args": ["dist/main.js"]
  }
}
~~~

上面是逻辑结构，不是最终 manifest。Codex 不需要增加 provider delegation 权限；
deep-web-search 按约定读取 KI_HOME/credentials.json 中的 openai-codex credential。

扩展自己的配置页和投影文案也放在扩展包内的 locale 资源中，不把
`cfg.deepWebSearch.*` 等扩展专用 key 写入 Host 的 WebUI 字典。Host 只加载并通过
extension catalog 转发资源；投影文案使用通用 `UIText`（`{key, params, fallback}`），
由 WebUI 按当前语言解析。这样同一个全局 sidecar 可以同时服务不同语言的浏览器。

## 3. Codex OAuth 联动方案：直接读取 credentials.json

### 3.1 现有凭据格式

现有 [codex-oauth/extension.json](../../extensions/codex-oauth/extension.json) 注册：

~~~text
provider id: openai-codex
api:         openai-codex-responses
auth:        oauth, subscription
runtime:     uv run --project . main.py
~~~

Ki Registry 将 provider credential 保存到：

~~~text
KI_HOME/credentials.json
~~~

当前结构由 [internal/provider/registry.go](../../internal/provider/registry.go) 定义，
Codex OAuth 关键部分类似：

~~~json
{
  "version": 1,
  "providers": {
    "openai-codex": {
      "type": "oauth",
      "value": {
        "access": "...",
        "refresh": "...",
        "expires": 0,
        "accountId": "..."
      }
    }
  }
}
~~~

deep-web-search 直接读取该文件中的 openai-codex.value，不新增 Host delegation、
ProviderManager 转发、extension bus 通道或第二套 OAuth 登录。Codex 搜索 sidecar
自行调用现有 Codex Responses endpoint，并使用 access/accountId 构造请求头。

### 3.2 读取和刷新策略

推荐按以下顺序处理：

1. 每次 Codex 搜索请求前重新读取 credentials.json，避免长期缓存旧 access token。
2. 校验 type=oauth、access 和 accountId；缺失时返回 codex-auth-missing。
3. expires 距离当前时间足够时直接请求。
4. access token 过期或即将过期时，使用同一 value.refresh 调用 auth.openai.com 的
   OAuth token endpoint，算法和 client id 参考 [codex-oauth/main.py](../../extensions/codex-oauth/main.py)。
5. refresh 成功后原子更新 credentials.json 中 openai-codex.value，保留 refresh
   token 轮换后的新值。
6. refresh 失败返回 codex-auth-expired，并提示用户使用已有的
   ki provider login openai-codex。

直接读文件会让 deep-web-search 和 codex-oauth 都可能刷新同一个 token。为降低竞态，
deep-web-search 进程内使用 provider 级互斥；写回时使用 credentials.json.lock 的独占创建、
临时文件和 rename。若锁被 codex-oauth 占用，重新读取文件再决定是否还需要刷新。
如果第一阶段不实现写回，则过期 token 直接要求重新登录；不能把 refresh token 放入
搜索结果或日志。

这是一个有意接受的实现取舍：直接读取 credentials.json 简单、无需新增 Host/Provider
协议，但 extension 会依赖 Ki 当前凭据文件格式。后续如果凭据格式变化，应优先更新
deep-web-search parser 和 codex-oauth 的共享文档/测试。

### 3.3 Codex 搜索请求和结果

请求参考 [pi-web-access/openai-search.ts](../../../pi-web-access/openai-search.ts)：

~~~json
{
  "model": "<selected codex model>",
  "store": false,
  "stream": true,
  "input": [{"role": "user", "content": [{"type": "input_text", "text": "..."}]}],
  "tools": [{"type": "web_search"}]
}
~~~

请求发往 Codex provider 的 base URL 加 /codex/responses，并携带：

~~~text
Authorization: Bearer <access>
chatgpt-account-id: <accountId>
OpenAI-Beta: responses=experimental
~~~

解析规则：

- 保留最终回答文本。
- 从 url_citation annotation 和 web_search_call sources 提取 URL。
- 使用 citation offset 生成 snippet；没有 citation 时保留 answer，但不伪造来源。
- URL 去重、HTTP(S) 校验和结果数量截断在统一聚合层再次执行。
- 401、refresh 失败、订阅额度耗尽、429 返回可识别的错误类别。

Codex 搜索不需要在 deep-web-search 中配置 Key。模型使用 `codexModel`，默认 `gpt-5.5`；
sidecar 启动时用 Host 提供的 provider catalog 校验模型 ID，避免请求不存在的模型。
如果未来 `tool.execute` 上下文包含当前 session 模型，可优先使用该模型，否则回退到
配置值。Codex OAuth 未安装、未登录或被禁用时，返回结构化 provider error，但继续处理
其他 provider。

### 3.4 安全边界

- 只读取 credentials.json 中的 providers.openai-codex.value。
- 不读取其他 provider 的 credential，不把整份 credentials.json 发送给网络服务。
- 不通过 Host RPC、bus、session entry、SSE 或 WebUI 传递 access/refresh。
- 日志只记录 configured/missing/expired/refresh-failed 等状态，不记录 token 内容。
- credentials.json 和 lock 文件沿用 Ki 的私有权限；写回必须原子化，不能部分覆盖。

## 4. Exa API Key 与额度

### 4.1 API 鉴权可提高额度路径

可以。pi-web-access 的 [exa.ts](../../../pi-web-access/exa.ts) 已实现：

~~~text
没有 exaApiKey → https://mcp.exa.ai/mcp
有 exaApiKey   → https://api.exa.ai/answer 或 /search
~~~

有 Key 时发送 x-api-key，保留以下路由策略：

- 普通默认查询优先 /answer。
- 指定域名、时间、非默认结果数或 includeContent 时使用 /search。
- /search 的 highlights/text 进入统一结果和 inline content。
- Keyless MCP 仅作为零配置路径。

API Key 会让请求进入 Exa 账号/团队限额，而不是匿名 MCP 的共享限流；但不能把
“有 Key”硬编码成固定 QPS。Exa 当前官方资料列出不同层级和 endpoint 的限制：
API 文档列出 /search、/answer 默认 10 QPS、/contents 100 QPS，并说明更高限额
需要 Enterprise；定价页列出 Free Starter 5 QPS、每月 $10 credits
且无需 payment method，Developer 10 QPS，Enterprise 可定制。[Rate Limits](https://exa.ai/docs/reference/rate-limits)
[Pricing](https://exa.ai/pricing?tab=api)

实现应依赖实际返回的 429、402、Retry-After 和错误标签，而不是承诺永久固定的
免费或高额度。

### 4.2 Exa 配置行为

~~~text
exaMode = auto:
  有 exaApiKey → Exa REST
  无 exaApiKey → Exa MCP

exaMode = api:
  无 Key → 配置错误，不退回 MCP

exaMode = mcp:
  忽略 API Key，强制走 MCP
~~~

默认使用 auto。用户填入 API Key 后下一次请求切换 REST，删除 Key 后恢复 MCP。
配置优先级为 extension 配置 > EXA_API_KEY 环境变量；环境变量用于 CI/开发，
不回显到页面。

## 5. TinyFish API Key 配置页面

### 5.1 使用现有 Extension Settings API

不新增 TinyFish 专用 REST 路由，使用现有：

~~~text
GET   /v1/extensions/deep-web-search/config
PATCH /v1/extensions/deep-web-search/config
~~~

manifest 将 tinyfishApiKey 标为 secret: true。Host 配置层已经具备：

- 配置保存到 extension 私有 config.json，权限为 0600。
- GET 返回 <configured>，不返回原始 Key。
- PATCH 传 <configured> 时保留原值。
- config.updated 通知运行中的 sidecar 重新加载配置。

### 5.2 UI 实现

当前通用 ExtensionConfigEditor 是 JSON textarea；增加 WebSearchConfigForm，
与 Telegram 专用表单类似：

~~~text
Deep Web Search
├── Codex OAuth
│   ├── 启用搜索 [toggle]
│   ├── 已登录/未登录（只读状态）
│   └── 跳转 Provider Settings 或提示 ki provider login openai-codex
├── Exa
│   ├── 启用搜索 [toggle]
│   ├── API Key（password，不回显）
│   ├── 模式：自动 / API / MCP
│   └── 当前来源：API / MCP / 未配置
├── TinyFish
│   ├── 启用搜索 [toggle]
│   ├── API Key（password，不回显）
│   ├── 已配置/未配置
│   └── Search/Fetch 免费但有限流的说明
├── DuckDuckGo
│   ├── 启用搜索 [toggle]
│   └── 无需配置
└── 汇总设置
    ├── 结果排序和聚合设置
    ├── 默认结果数
    ├── 是否后台抓取正文
    ├── 默认工作流：none / summary-review / auto-summary
    ├── 摘要模型：provider/model-id
    ├── 查询改写模型：provider/model-id（可选）
    ├── 摘要超时
    └── 是否自动打开 curator 浏览器
~~~

保存行为：

- 输入为空且已有 <configured>：发送 <configured>，保留原 Key。
- 用户点击清除：发送 null，删除 Key。
- 页面不显示原始 Key，不把 Key 放进 ui.setPanel、SSE、URL、日志或错误信息。
- 状态只显示 configured、missing、error、rate-limited 等，不显示 Key 前缀和长度。

三种 workflow 配置优先级为：单次 `deep_web_search.workflow` 参数 > extension 配置中的
`workflow` > 默认 `none`。`summaryModel` 和 `queryRewriteModel` 使用
`provider/model-id` 格式，不是 API Key；Codex 模型直接使用 `credentials.json` 中的
OAuth credential。MVP 只保证 `openai-codex/*`，其他模型需要后续增加 Host completion
能力或对应的 credential 读取方案。

### 5.3 Provider toggle

四个供应商分别使用独立 toggle，持久化到 `providerToggles`：

~~~json
{
  "providerToggles": {
    "codex": true,
    "exa": false,
    "tinyfish": true,
    "duckduckgo": true
  }
}
~~~

- toggle 只控制是否允许发起该 provider 的请求，不删除 API Key、OAuth 状态或缓存。
- 默认调用只选择已启用的 provider；工具参数中的显式 `provider` 也必须经过 toggle
  过滤，不能绕过关闭状态。
- 显式请求了已关闭的 provider 时，返回 `provider_disabled` 诊断，不发起网络请求。
- 所有 provider 都关闭时直接返回配置错误并提示打开至少一个 toggle。
- `config.updated` 后立即生效；正在执行的请求使用启动时确定的 provider 快照，不中途
  改变并发集合。

### 5.4 三种 workflow 配置示例

全局默认配置写入 deep-web-search 的 extension config：

~~~json
{
  "workflow": "none"
}
~~~

单次调用可以覆盖全局配置：

~~~typescript
deep_web_search({ query: "...", workflow: "none" })
deep_web_search({ query: "...", workflow: "summary-review" })
deep_web_search({ query: "...", workflow: "auto-summary" })
~~~

| workflow | curator 页面 | `queryRewriteModel` | `summaryModel` | 无模型时的行为 |
|---|---|---|---|---|
| `none` | 不启动 | 不调用 | 不调用 | 返回 source pack，由当前会话模型继续回答 |
| `summary-review` | 启动；`autoOpenBrowser` 控制是否自动弹窗 | 仅点击 AI 改写时调用 | 仅生成摘要草稿时调用 | 可人工发送原始结果；摘要生成失败则确定性 fallback |
| `auto-summary` | 不启动 | 不调用 | 搜索完成后调用 | 自动切换为 `none`，返回 source pack 并记录 fallback 原因 |

模式配置不改变四个 provider 的搜索、清洗、去重和缓存流程；它只控制搜索结束后的
curator、查询改写和摘要阶段。

### 5.5 Workflow fallback 和实际模式

fallback 不只返回一个错误文本，而是完成一次明确的 workflow 状态切换，并在工具
`details` 中同时记录 `requestedWorkflow`、`effectiveWorkflow`、`fallbackTo` 和
`fallbackReason`：

| 请求模式 | 失败点 | fallback 后模式 | 行为 |
|---|---|---|---|
| `none` | 无额外模型 | `none` | 不切换；继续返回 source pack |
| `summary-review` | `queryRewriteModel` 失败 | `summary-review` | 保留 curator，禁用本次 AI 改写，允许人工修改 query |
| `summary-review` | `summaryModel` 缺失、超时或失败 | `summary-review` | 保留 curator，允许发送人工选择的原始结果；可生成确定性摘要，但不再重试模型 |
| `summary-review` | curator 服务启动/连接失败 | `none` | 关闭 curator，直接返回已完成的 source pack |
| `auto-summary` | `summaryModel` 缺失、超时或失败 | `none` | 不再生成确定性摘要，返回 source pack，由当前会话模型继续回答 |

provider 的 401、429、网络错误和单路失败属于 provider fallback/部分成功，不改变
workflow；只有 workflow 自身的 curator 或模型阶段失败才触发上表切换。fallback 只执行
一次，不能在 `none` 和 `auto-summary` 之间循环。

## 6. 四个 provider 的实现边界

### 6.1 统一接口

~~~ts
type SearchOptions = {
  numResults?: number
  recencyFilter?: "day" | "week" | "month" | "year"
  domainFilter?: string[]
  includeContent?: boolean
  signal?: AbortSignal
}

type ProviderSearchResult = {
  provider: "codex" | "exa" | "tinyfish" | "duckduckgo"
  answer: string
  results: SearchResult[]
  inlineContent?: InlineContent[]
  diagnostics: ProviderDiagnostics
}

type SearchResult = {
  title: string
  url: string
  snippet: string
  rank?: number
  publishedAt?: string
  provider: string
  providerRef?: string
}
~~~

provider 只负责请求、超时、响应解析、字段映射和基础错误分类；不负责跨 provider
去重、最终排序或最终总结。

### 6.2 Codex

- deep-web-search sidecar 直接读取 `KI_HOME/credentials.json`，自行向 Codex Responses endpoint
  发起请求；不经过 codex-oauth sidecar 或 Host delegation。
- Codex answer 保留为 providerAnswer，citation 进入 results。
- 缺少 citation 时不把 answer 中的普通 URL 当作可信来源。
- Codex 失败不阻断 Exa、TinyFish、DuckDuckGo。

### 6.3 Exa

- 有 Key 使用 REST，无 Key 使用 MCP。
- MCP 同时解析 JSON、SSE 和格式化文本块。
- REST /answer 的 citations 和 /search 的 results 映射到统一结构。
- includeContent 时优先保留 Exa 的 text，不足时进入统一 fetch。
- 诊断中区分 exa-api 与 exa-mcp，不能把 MCP fallback 伪装成 API 成功。

### 6.4 TinyFish

- 使用 X-API-Key。
- Search 映射 query、include/exclude domains、recency minutes、page。
- 超过 10 个结果时分页请求并合并。
- includeContent 使用 Fetch endpoint，批量上限 10 个 URL。
- Search/Fetch 与付费 Agent/Browser 能力分开；本 extension 不调用后两者。

### 6.5 DuckDuckGo

- GET https://html.duckduckgo.com/html/，使用固定 User-Agent 和 30 秒超时。
- 解析 .result、.result__a、.result__snippet，跳过广告。
- 解码 uddg redirect URL，只接受 HTTP(S)。
- 时间过滤只作为弱提示，不承诺精确 recency。
- 403、429、HTML 结构不可解析时返回清晰错误。
- 只作为显式聚合成员，不进入无限 retry loop。

## 7. `deep_web_search` 工具

工具注册名固定为 `deep_web_search`，schema 和执行语义参考
[pi-web-access/index.ts](../../../pi-web-access/index.ts) 的 `web_search`，但 provider 集合
只保留 Codex、Exa、TinyFish、DuckDuckGo：

~~~json
{
  "name": "deep_web_search",
  "parameters": {
    "type": "object",
    "properties": {
      "query": {"type": "string"},
      "queries": {"type": "array", "items": {"type": "string"}},
      "numResults": {"type": "integer", "minimum": 1, "maximum": 20},
      "includeContent": {"type": "boolean"},
      "recencyFilter": {"type": "string", "enum": ["day", "week", "month", "year"]},
      "domainFilter": {"type": "array", "items": {"type": "string"}},
      "provider": {
        "oneOf": [
          {"type": "string", "enum": ["auto", "all", "codex", "exa", "tinyfish", "duckduckgo"]},
          {"type": "array", "minItems": 1, "items": {"type": "string", "enum": ["codex", "exa", "tinyfish", "duckduckgo"]}}
        ]
      },
      "workflow": {"type": "string", "enum": ["none", "summary-review", "auto-summary"]},
      "proxy": {"type": "string"}
    }
  }
}
~~~

实现时对应 pi-web-access 的 TypeBox schema：`query` 与 `queries` 都是 optional，但执行
时至少需要一个；两者同时提供时以 `queries` 为准。工具描述中应明确 `queries` 建议使用
2～4 个不同角度，避免重复查询导致结果高度相似。

参数语义：

- `numResults` 默认 5，单 query 最大 20；每条 query 保留独立 answer、results 和 diagnostics。
- `provider` 省略时使用配置中的默认值（默认 `all`）；`all` 表示所有已开启 provider，
  `auto` 按可用性选择一个 provider，字符串或数组显式选择时仍必须经过
  `providerToggles` 过滤。
- 显式请求已关闭 provider 时返回 `provider_disabled` 诊断，不发起该 provider 的网络请求；
  `all` 只遍历开启的成员。
- `includeContent=true` 只触发后台正文抓取，不把长正文塞进初始 tool result；使用返回的
  responseId 调用 `get_search_content` 分片读取。
- `workflow=none` 返回 source pack；`summary-review` 打开来源审阅；`auto-summary` 由
  `summaryModel` 生成无搜索摘要。默认 workflow 为 `none`，避免工具调用隐式打开 UI。
- `domainFilter` 沿用 pi-web-access 约定，`-example.com` 表示排除域名；不能把域名过滤
  转换成任意 URL 请求。
- `proxy` 是单次调用级别的 HTTP(S) 代理覆盖项，空字符串表示直连；代理配置不能绕过
  SSRF、超时和响应大小限制。

工具结果分成两部分：

1. 给模型的短 source pack：去重后的标题、URL、snippet、provider provenance。
2. session custom entry：完整 provider answer、原始排序、诊断和 cache responseId；不把
   OAuth/API Key 写入结果。

### 7.1 从 pi-web-access 继承的执行技术点

- 注册接口保留 `label`、`description`、`promptSnippet`、`parameters` 和 `execute` 五个
  层次；只把对外工具名改为 `deep_web_search`，Codex 上游请求体中的 `web_search` tool
  类型不能改名。
- `execute(callId, params, signal, onUpdate, ctx)` 先清洗 query；当 `queries` 存在时按
  顺序保留每条 query 的结果槽位和诊断，provider 请求在每条 query 内按 toggle 并发。
- 组合用户取消信号和内部搜索 AbortController；所有 provider client 都必须传递同一个
  AbortSignal，取消后丢弃 partial answer，不把它当作成功结果。
- 使用 `allSettled`/等价逻辑保留部分成功和 typed error；`onUpdate` 只发送阶段、进度、
  provider 状态等短信息，长正文和原始响应写入 cache。
- 返回值遵循 `AgentToolResult`：`content` 给模型可读的短文本，`details` 放 responseId、
  queryCount、providerRuns、resultCount、fetchId、workflow 状态和错误诊断，workflow 至少
  包含 requestedWorkflow、effectiveWorkflow、fallbackTo、fallbackReason，避免把完整上游
  响应塞进上下文。
- `includeContent` 走后台 `fetchAllContent`，先本地 HTTP/Readability，再按策略 fallback
  到 TinyFish Fetch；正文通过 responseId + `get_search_content` 的 offset/limit/findText
  读取。参考 [extract.ts](../../../pi-web-access/extract.ts) 和
  [storage.ts](../../../pi-web-access/storage.ts)。
- 错误展示按 configuration、authentication、quota、rate_limit、network、invalid_response、
  unsupported 分类；错误文本先做 credential redaction。参考
  [render-search-error.ts](../../../pi-web-access/render-search-error.ts)。
- Codex citation 解析沿用 [openai-search.ts](../../../pi-web-access/openai-search.ts)，但
  凭据读取改为本方案第 3 节的 `credentials.json` reader。

## 8. 聚合、清洗与去重

### 8.1 调用流程

~~~text
normalize input
    ↓
build provider-specific query/options
    ↓
filter explicit/default providers by providerToggles
    ↓
run selected providers concurrently
    ↓
allSettled: keep successes + typed errors
    ↓
normalize URLs and text
    ↓
dedupe exact/near duplicates
    ↓
rank with source diversity
    ↓
persist result event/cache metadata
    ↓
return source pack
~~~

每个 provider 设置独立 timeout，同时设置全局 deadline。一个 provider 失败不丢弃
其他结果；全部失败才返回整体错误。

### 8.2 URL 和文本清洗

参考 pi-web-access 的 normalize、SSRF 和 provider parser，但把清洗集中到聚合层：

- 只接受 HTTP(S)，拒绝 javascript:、data: 和空 URL。
- hostname 小写化，去默认端口和 fragment，去掉 utm_*、gclid、fbclid
  等追踪参数；不随意删除业务 query 参数。
- 解析 DuckDuckGo uddg、Codex citation redirect，并对最终 URL 再校验。
- title/snippet 合并连续空白、去控制字符，设置长度上限。
- 长正文只放 cache，不放初始 tool result。
- 保留原始 URL、canonical URL、provider 和原始 rank，便于审计。

### 8.3 去重和结果保留

由强到弱：

1. canonical URL 精确相等。
2. 相同 hostname + pathname，且标题归一化后相似。
3. title/snippet token 集合的 Jaccard 或 SimHash 达到近重复阈值。

合并时不能丢 provider 信息：

~~~json
{
  "url": "https://example.com/article",
  "title": "...",
  "snippet": "...",
  "seenBy": ["codex", "exa", "tinyfish"],
  "ranks": {"codex": 1, "exa": 3, "tinyfish": 2}
}
~~~

### 8.4 排序

建议使用带 provider 权重的 Reciprocal Rank Fusion：

~~~text
score(url) = Σ providerWeight / (rrfK + providerRank)
             + agreementBonus
             + freshnessBonus
             - repeatedDomainPenalty
~~~

初始 rrfK=60。四家 provider 权重可配置，默认接近但不完全相同；同 URL 多次
发现时只增加有限 agreement bonus；前 10 个结果最多保留同一 registrable domain
的 2～3 条。用户指定 domainFilter 时，域名条件优先于多样性配额。

“互斥”只能做到职责和结果尽量去重，不能保证底层索引完全不重合；高权威页面被
多个 provider 找到时，应保留 seenBy 作为可信度和审计信息。

## 9. 抓取、缓存和按需读取

### 9.1 工具拆分

参考 pi-web-access：

~~~text
deep_web_search     搜索并返回 source pack
fetch_content       指定 URL 抓取正文
get_search_content  从 responseId/cache 分片读取正文
source_check        搜索 + 证据核验（第二阶段）
~~~

### 9.2 抓取顺序

当前四个 provider 下建议：

~~~text
local HTTP readable/raw
    → TinyFish Fetch（JS、反爬或正文不足时）
    → Exa inline text（搜索已经返回时）
~~~

HTTP 直抓参考 [extract.ts](../../../pi-web-access/extract.ts)：

- SSRF、防重定向、hostname policy。
- 默认 30 秒、5 MB 响应上限。
- HTML 用 Readability + Turndown 转 markdown。
- 正文为空或质量不足时才 fallback 到 TinyFish。
- 不把远程抓取 provider 作为无条件第一选择。

### 9.3 cache 和 jsonl

搜索完成后通过 Host 追加：

~~~text
session.appendEntry(
  customType = "deep-web-search-results",
  data = { responseId, query, providerRuns, results, diagnostics }
)
~~~

正文补齐后追加 deep-web-search-content-ready entry。搜索结果进入 session jsonl，遵循
Ki “loop 已有数据则扩展 event/jsonl，不新增数据 REST 路由”的约束；全文不写入
普通对话 context。

cache 参考 [storage.ts](../../../pi-web-access/storage.ts)：

- 默认 TTL 1 小时，最多 128 个 URL 条目或 128 MiB。
- key 使用 responseId + canonical URL，不使用用户提供的文件路径。
- get_search_content 支持 offset/limit、按行切片、findText。
- cache miss 可按 session entry 中的原 URL 重新抓取；重抓仍受 SSRF、timeout 和 policy 约束。
- sidecar 重启后至少保留 jsonl 中的结果和 URL；正文 cache 失效时返回明确 cache miss。

MVP 可先使用 sidecar 进程级 LRU；跨重启全文持久化时，再使用 KI_HOME 下的
extension-owned data directory，原子写入并使用 0600 权限。不要把全文写入安装目录
或 config.json。

## 10. 汇总、整理和证据核验

### 10.1 三种模式

#### none

- 只返回清洗、去重、排序后的 source pack 和 provider diagnostics，不启动 curator。
- 不调用 `summaryModel` 或 `queryRewriteModel`；最终回答由当前 Ki 会话模型完成。
- `includeContent=true` 仍可后台抓取正文，但只通过 responseId/get_search_content 读取。

#### summary-review

- 启动临时 curator 页面，展示 query、provider 成功/失败、来源和正文状态。
- 人工选择来源、追加搜索、编辑 query、编辑/批准摘要都不强制调用模型。
- curator 的 AI 改写按钮调用 `queryRewriteModel`；生成摘要草稿调用 `summaryModel`。
- 用户可以直接发送所选原始结果，跳过摘要模型；摘要模型超时或不可用时不再重试模型，
  仍保留 curator 供人工审阅，可选择确定性摘要或发送原始结果。
- `autoOpenBrowser=false` 只禁止自动弹窗，仍可返回 curator URL 手动打开；要完全跳过
  curator 必须使用 `workflow=none`。若 curator 服务自身启动/连接失败，自动切换为
  `none`，返回已完成的 source pack。
- `ui.action`/`ui.submit` 只提交 source IDs、query 和编辑结果，不提交 token；审阅结果
  写入 `deep-web-search-review` custom entry。

#### auto-summary

- 不启动 curator 页面；聚合器选出前 N 个来源后调用 `summaryModel` 生成摘要。
- 输入只有 query、清洗后的来源和正文片段，不附加上游 `web_search` tool，禁止递归搜索。
- 每个事实带 `[n]` citation；无证据内容标记不确定，模型只允许使用提供的来源。
- `summaryModel` 缺失、超时或调用失败时自动切换为 `none`，返回 source pack，由当前
  会话模型继续回答；在 details 中记录 `fallbackFrom=auto-summary`、`fallbackTo=none`、
  `fallbackReason` 和模型状态。

三种模式都保留 provider answer、source pack、原始 rank、去重 provenance 和错误诊断；
区别只在于是否打开 curator，以及是否额外执行查询改写/摘要模型。

### 10.2 source_check

参考 [source-check.ts](../../../pi-web-access/source-check.ts)：

1. 将 claim 拆为最多 8 个角度 query。
2. 四个 provider 并发搜索，最多保留 20 个 URL。
3. 去重后只抓取前 5 个高价值页面。
4. 记录 passage、字符 offset、URL、title、抓取时间和 content hash。
5. 输出 supported、contradicted、unclear、missing-evidence。

结果应是可审计 artifact，而不是一句“正确/错误”；artifact 元数据进 jsonl，长正文
继续放 cache，由 get_search_content 按 offset 读取。

## 11. 错误、限流和可观测性

统一错误类别：

~~~text
configuration       Key 缺失、Codex 未登录、provider disabled
authentication      401/403、OAuth refresh 失败
quota               402、免费额度耗尽、订阅额度耗尽
rate_limit          429、Retry-After
network             DNS、连接、timeout、abort
invalid_response    2xx 但 JSON/HTML/SSE 不符合预期
unsupported         provider 不支持某个过滤或内容能力
~~~

每次 provider run 记录非敏感 diagnostics：

~~~json
{
  "provider": "exa-api",
  "status": "success",
  "latencyMs": 842,
  "resultCount": 5,
  "inlineContentCount": 0,
  "fallbackFrom": null,
  "errorClass": null
}
~~~

不记录 Authorization、X-API-Key 或完整敏感上游错误 body；日志和错误都先执行
credential redaction。

Exa 的 api 模式遇到 401/403 直接报告配置错误，不静默退回 MCP；auto 模式可以
对网络/暂时性限流尝试 MCP，但诊断必须标明实际路径。402 不应无限重试。

TinyFish 的 Key 缺失不重试；429 读取 Retry-After，最多一次延迟重试；本 extension
只调用 Search/Fetch，不调用付费 Agent/Browser。DuckDuckGo 的 403/429 或 HTML
结构变化不做高频 retry。

## 12. 测试和验收

### 12.1 单元测试

- 各 provider 使用 JSON/SSE/HTML fixture，覆盖正常、空结果、字段缺失、乱码和错误响应。
- Exa API/MCP 两条路径；验证 Key、mode 强制行为和实际 provider 标记。
- Codex credentials reader：正确读取 `openai-codex.value`，覆盖缺失、过期、refresh、
  轮换写回、文件锁和权限；验证 access/refresh 不进入 tool result、session entry、
  SSE、WebUI 和日志。
- OAuth refresh 竞态：并发搜索只刷新并持久化一次。
- URL canonicalization、tracking 参数、DDG redirect、近重复去重。
- RRF、agreement bonus、domain cap、provider error 保留。
- cache TTL、容量淘汰、offset/limit、findText 和 cache miss 重抓。
- workflow 配置校验、单次参数覆盖全局配置，以及三种 workflow 的模型调用边界。
- workflow fallback 状态机：`auto-summary → none`、`summary-review` 的模型失败保留人工
  审阅、curator 启动失败时 `summary-review → none`，并验证不会循环重试。

### 12.2 Host/extension/UI 集成测试

- manifest、sidecar initialize、tool registration、config.updated。
- GET/PATCH /v1/extensions/deep-web-search/config 的 secret redaction、保留和清除。
- TinyFish/Exa Key 页面输入、保存、刷新后不回显原 Key。
- Codex OAuth 未安装、未登录、登录成功、refresh、logout 和 sidecar 失败。
- 四个 provider 分别关闭/开启、显式请求关闭 provider、全部关闭时不发起网络请求。
- `none` 不启动 curator 且不调用摘要/改写模型；`summary-review` 支持人工原始结果提交、
  AI 改写和摘要草稿；`auto-summary` 不打开浏览器并验证摘要模型 fallback。
- `summaryModel`、`queryRewriteModel` 的 `provider/model-id` 校验、Codex credential 复用、
  超时和敏感信息脱敏。
- 三种 workflow 的 `requestedWorkflow`/`effectiveWorkflow`/`fallbackTo`/`fallbackReason`
  诊断字段。
- tool 调用的 session 隔离、取消、provider 独立 timeout 和全局 deadline。
- deep-web-search-results、deep-web-search-content-ready entry 不进入普通 prompt context。

### 12.3 E2E/Live

Fake 模式覆盖四个 provider 的并发部分成功、单 provider 失败、全失败、相同 URL
多 provider provenance、Key 页面和 <configured> 处理。

Live 模式单独覆盖 Codex OAuth、Exa MCP/API、TinyFish Search/Fetch，以及 DuckDuckGo
HTML 结构变化告警。

## 13. 分阶段实施

### Phase 0：Codex credentials reader 契约

- 定义 `credentials.json` 的 `openai-codex` OAuth 读取、校验和错误分类。
- 复用 codex-oauth 已有的 OAuth token endpoint、client id 和 credential 字段语义；不新增
  Host delegation、ProviderManager 转发或 provider.search 事件协议。
- 实现 access token 过期处理；若支持自动 refresh，增加进程内互斥、跨进程 lock、临时文件
  和原子 rename，并覆盖 refresh token 轮换。
- 用 fixture 验证 Codex Responses 请求头、citation 解析和敏感字段不泄漏。

### Phase 1：四 provider 和基础工具

- 建立 extensions/deep-web-search Node sidecar。
- 移植 Codex parser、Exa、TinyFish、DuckDuckGo。
- 注册 deep_web_search、fetch_content、get_search_content。
- 实现统一 response、timeout、error class、URL/文本清洗。
- 实现 selected provider 并发和部分成功返回。

### Phase 2：配置页面和持久化

- manifest schema 和 WebSearchConfigForm。
- 扩展自带 `locales/en.json` / `locales/zh.json`；配置表单和 UI projection 不依赖 Host 的
  extension-specific i18n key。
- 四个 provider toggle 的展示、保存、单独生效和 config reload；关闭 provider 不删除其凭据。
- TinyFish/Exa Key 保存、清除、脱敏和 config reload。
- workflow、summaryModel、queryRewriteModel、curatorTimeoutSeconds 和 autoOpenBrowser 的
  保存、脱敏/校验和 config reload。
- deep-web-search-results/deep-web-search-content-ready jsonl entry。
- LRU cache、分片读取和 fetch fallback。

### Phase 3：排序和整理

- canonical URL、近重复、RRF、domain diversity cap。
- 多 query 角度改写和查询去重。
- `none` source pack 稳定化。
- `auto-summary`（无搜索 `summaryModel` completion）和确定性 fallback。
- workflow fallback 状态机和实际模式切换。

### Phase 4：证据和交互审阅

- source_check artifact、passage offsets、hash 和 claim status。
- review 面板、来源选择、摘要批准/编辑。
- richer fetch modes、正文质量评分和 cache 清理。

## 14. 验收标准

1. ki provider login openai-codex 登录后，deep_web_search 使用同一 OAuth credential，没有第二套 Codex 登录。
2. deep-web-search sidecar 只在发起预期的 Codex 请求时短暂持有 token；token 不进入工具参数、
   session entry、日志、SSE 或 WebUI，也不出现在搜索结果和诊断中。
3. Exa 无 Key 走 MCP；填入 exaApiKey 后走 REST，并区分 exa-mcp 与 exa-api。
4. TinyFish Key 可以在 WebUI 专用配置页保存、清除和脱敏显示。
5. DuckDuckGo 不需要 Key、不需要 SearXNG 或其他自建服务。
6. 四个 provider 并发时，一个失败不影响其他成功结果。
7. 相同 URL 只在最终 source pack 出现一次，但保留 seenBy 和各 provider rank。
8. 搜索结果进入现有 session/jsonl/SSE 体系，不新增搜索数据 REST 路由。
9. 正文抓取受到 SSRF、大小、超时、cache TTL 和上下文长度限制。
10. `none` 不启动 curator、不调用 `summaryModel` 或 `queryRewriteModel`。
11. `summary-review` 可人工选择并发送原始结果；模型只用于 AI 改写或摘要草稿，
    `autoOpenBrowser=false` 时不自动弹窗。
12. `auto-summary` 不启动 curator，使用 `summaryModel` 生成摘要；模型缺失、超时或失败时
    自动切换为 `none`，返回 source pack 并保留 fallback 诊断。
13. `summary-review` 的改写/摘要模型失败不会丢失人工审阅能力；curator 启动或连接失败时
    自动切换为 `none`。
14. 三种 workflow 均不会递归触发第二轮隐式 web search；Codex、Exa、TinyFish、DuckDuckGo
    均有独立 WebUI toggle，关闭后不会被默认或显式请求调用。

## 15. 本次实现结果

本方案已经落地为 `extensions/deep-web-search`，源码统一使用 TypeScript，运行时由
`tsc` 编译为 `dist/*.js`，extension manifest 的 RPC 入口仍然是 `node dist/main.js`。

- 已注册 `deep_web_search`、`fetch_content`、`get_search_content`、`source_check` 四个工具，
  使用 NDJSON JSON-RPC sidecar 与 Ki Host 通信。
- 已接入 Codex OAuth、Exa、TinyFish、DuckDuckGo。Codex 直接读取 Ki 的
  `credentials.json`，不复制或另存 OAuth；Exa 在 `auto` 下有 Key 走 API、无 Key 走 MCP；
  TinyFish 使用 WebUI 保存的 API Key；DuckDuckGo 直连公开 HTML 搜索，不依赖 Brave 或自建
  SearXNG。
- 已实现四个 provider 的独立 toggle、provider availability 检查、并发搜索、失败隔离、
  canonical URL、近重复过滤、RRF 风格排序、域名多样性限制和诊断信息。
- 已实现搜索缓存、正文抓取、重定向/私网 SSRF 防护、大小和超时限制、TinyFish 正文 fallback，
  以及通过 `responseId` 的正文读取。
- 已实现三种 workflow：`none` 只返回 source pack；`auto-summary` 调用 summary model，
  失败后自动切换为 `none`；`summary-review` 启动本地 curator，人工提交后再生成摘要，
  curator 启动/连接失败时自动切换为 `none`。
- 已实现 evidence passages、offset/hash、claim 的 supported/contradicted/unclear/
  missing-evidence 判定；搜索结果和正文 ready 信号写入现有 session entry，不新增搜索数据
  REST 路由。
- WebUI 已增加 Exa/TinyFish 密钥配置、脱敏状态、四个 provider toggle、默认 provider、
  workflow、summary/query-rewrite model、正文抓取和 curator 设置。
- 四个内置扩展已声明自己的 i18n catalog；Goal 的 session/global UI、扩展描述和
  deep-web-search/Telegram 配置文案会随 WebUI 中英文切换，Host 不再维护扩展专用文案。
- 本地 `config.json`、`cache.json`、`node_modules` 和运行期临时文件已加入扩展自己的
  `.gitignore`；API Key 只写入本地 extension 配置，不进入源码、session entry、日志或工具结果。

真实烟测结果（不记录密钥）：

1. `provider=all`、`workflow=none`、TypeScript 5.9 release notes：Codex、Exa、TinyFish、
   DuckDuckGo 均返回成功，最终结果完成跨 provider 去重；Exa 诊断标记为 `transport=api`。
2. `provider=exa`、Exa API documentation query：API 请求成功，诊断标记为 `transport=api`。
3. `fetch_content` 抓取 TypeScript 官方 release notes：HTTP 正文提取成功，返回约 16 KiB
   可读文本。

验证命令：

```text
npm run typecheck --prefix extensions/deep-web-search
npm run build --prefix extensions/deep-web-search
npm test --prefix extensions/deep-web-search
go test ./internal/extension ./internal/server
go build -o ki ./cmd/ki
```

## 16. 后续增强项

当前实现已覆盖方案中的运行主链路；以下是有明确边界的增强，不影响四 provider 和三 workflow
的现有契约：

1. 为四个 provider 增加完全离线的 fixture/contract 测试，避免 CI 依赖外部搜索服务。
2. 接入跨平台 HTTP proxy dispatcher；在此之前，非空 `proxy` 会返回明确的
   `proxy-unsupported`，不会假装代理已经生效。
3. curator 增加更丰富的来源正文质量审阅和人工摘要编辑持久化；当前人工选择、追加搜索、
   查询改写、摘要草稿和提交链路已可用。
