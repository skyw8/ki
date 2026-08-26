# Provider 插件与 Codex OAuth 方案

调研对象：`/data/hgy/pi` 的 provider、custom streamer、OAuth 实现，以及 ki 当前的 `internal/provider` / `internal/extension`。结论是：ki 目前的扩展是“请求拦截器”，还不是“可注册的 provider runtime”；Codex 订阅需要先补齐这个边界。

## 实现进度

- 第一阶段基础设施已完成：`provider` capability、进程级 ProviderManager、provider stream NDJSON RPC、紧凑 delta 到 `loop.AssistantDelta` 的 Host adapter、插件模型目录和 opaque credential 存储。
- 已覆盖 provider sidecar 惰性启动、同一进程并发 stream、取消、重载 epoch 防串接、目录冲突和凭据不泄漏到 catalog 的测试。
- 第二阶段尚未开始：OAuth login/refresh、Codex 专用请求伪装、Responses opaque replay 元数据和 Login/Logout UI 仍待实现。

## 1. pi 的实现要点

- `pi.registerProvider()` 注册的是完整 provider：provider ID、模型目录、base URL、API 类型、认证方式和 stream 实现属于同一个运行时对象。既可以复用内置 API，也可以通过 `streamSimple(model, context, options)` 完全接管请求、SSE/WS 解析和统一事件流。
- OAuth 由 provider 提供 `login`、`refreshToken`、`getApiKey`，宿主负责交互和 `auth.json` 持久化；支持浏览器、device code、手工输入，OAuth token 会在请求前自动刷新。`isSubscription` 用于标识订阅型凭据。
- Codex 的模板是 `packages/ai/src/providers/openai-codex.ts` + `auth/oauth/openai-codex.ts` + `api/openai-codex-responses.ts`：PKCE/state、浏览器回调和 device-code 登录，JWT 提取 `chatgpt_account_id`，再访问 `/codex/responses`。请求还需要 Codex 专用 headers、`store: false`、reasoning encrypted content，以及 SSE/WS 的 Responses 事件转换。

### Streamer 归属结论

Codex streamer 应该属于 provider 插件，而不是 ki core 或只负责 OAuth 的薄插件。pi 中 OAuth 模块只负责登录、刷新和返回 access token；真正的“伪装 Codex 客户端”位于 `openai-codex-responses.ts`，包括：

- 将 ki 的 system prompt、历史消息和工具转换为 Codex Responses request body；
- 生成 `/codex/responses`、`Authorization`、`chatgpt-account-id`、`originator`、`User-Agent`、`OpenAI-Beta`、`session-id` 等请求信息；
- 处理 SSE（后续可选 WebSocket）、Responses 事件、reasoning、function/custom tool call、usage 和 stop reason；
- 保存和恢复 `responseId`、reasoning encrypted signature 等 provider-specific 状态。

因此 ki 的边界应是：宿主负责通用 loop、取消、背压、凭据存储和 UI 交互；provider 插件负责 OAuth 协议、Codex 请求构造、网络传输和响应解析。插件可以直接复用/移植 pi-ai 的 Codex API 实现，但不能只把 access token 交给 core 的普通 OpenAI streamer。

## 2. ki 当前缺口

| 能力 | 当前实现 | 对 provider 插件的影响 |
|---|---|---|
| provider 目录 | `Registry` 只有固定的 `completions` / `responses` / `anthropic` 和 API key | 不能声明自定义 API、插件模型或插件认证 |
| 路由 | `server.router` 统一 `Resolve` 后构造 `provider.NewLiveModel` | 没有按 provider 绑定 runtime/streamer |
| 扩展 sidecar | `before_provider_request` 只能改 model/messages/tools，`before_provider_headers` 只能改 URL 和非敏感 headers | 不能改完整 body、接管响应流、实现自定义协议 |
| 生命周期 | sidecar 按 session 启动 | OAuth/provider 状态无法自然跨 session 复用 |
| 凭据 | `credentials.json` 只有 `apiKey`，没有 OAuth 类型、刷新锁和交互流程 | 无法安全保存/刷新 Codex subscription token |
| IR 回放 | `types` 没有持久化 `thinkingSignature`、`responseId`、text signature 等 Responses 元数据 | Codex reasoning 加密内容和连续请求无法可靠回放 |
| 客户端 | WebUI 只有 API key 表单，CLI 没有 provider login | 无法发起浏览器/device-code OAuth |

## 3. 第一阶段：provider 插件基础设施

建议把“模型目录/认证/传输”从当前的静态 registry 拆成可组合的 provider runtime；保留 `loop.Streamer` 作为 loop 的中性边界，但 provider-specific streamer 在插件进程内执行，core 只提供 host-side adapter。

1. 定义 `ProviderSpec`、`ProviderAuth`、`ProviderStreamer` 三个中性接口。内置三种协议实现适配到该接口；registry 只负责模型快照、能力校验和 `provider/model` → runtime 的解析。插件的 `ProviderStreamer` 必须拥有完整的 provider-specific request/transport/parse 逻辑。
2. 增加 `provider` capability，但使用独立的、进程级 `ProviderManager`；不要复用当前每 session 的 lifecycle sidecar。provider sidecar 要支持多 session 并发、reload、退出清理和请求取消。
3. 扩展 NDJSON RPC：初始化返回 provider/model/auth 描述；增加 `auth.login`、`auth.refresh`、`provider.stream.start`、`provider.stream.cancel`，以 `requestId` 标识流。stream 只传输紧凑的 `start`、文本/thinking/tool-call delta、`done`、`error` 事件，不要每个 token 重复传输完整 `Partial`；host adapter 负责累积并还原 ki 的 `AssistantDelta`。
4. 增加宿主 auth broker：OAuth 的 URL、device code、select、manual code 等 UI-neutral 事件由 server 统一转发到 CLI/WebUI；OAuth 的 endpoint、PKCE、token exchange/refresh 和 account ID 提取由插件实现，凭据作为 provider-owned opaque value 存储并只在私有 RPC 中传递。凭据文件改为带 `type` 的 API key/OAuth 联合 schema，原子写入、`0600`，刷新按 provider 加锁并双重检查。
5. 扩展 provider/model 元数据：自定义 API ID、输入模态、thinking 映射、custom tool/freeform 能力、pricing 和 auth 类型；`/v1/providers` 与模型选择只返回非敏感状态。项目级 provider 首版建议拒绝或固定为全局，先保证 provider 列表、session、reload 的确定性。
6. 补齐 Responses 兼容 IR：至少持久化 provider opaque 的 `thinkingSignature` 和 assistant `responseId`，必要时补充 text signature、raw stop reason、end-turn；明确哪些字段进入 jsonl、哪些只在流式期间存在。新增 fake provider/plugin，覆盖多 session、取消、断流、重载、错误和 secret 泄漏测试。

## 4. 第二阶段：Codex OAuth provider 插件

以第一阶段 RPC 为唯一接入面，实现 `openai-codex` provider 插件：

- **认证**：浏览器 OAuth 默认，PKCE + state；headless 提供 device code。使用 `auth.openai.com` 授权/换 token/刷新 token，保存 `access`、`refresh`、`expires`、`accountId`，JWT 无法提取 account ID 时拒绝登录；token 剩余有效期不足时由宿主自动刷新，刷新失败提示重新登录。
- **传输**：由插件内独立的 `openai-codex-responses` streamer 完成，不复用 ki core 的普通 OpenAI Responses。首版先实现 SSE：`https://chatgpt.com/backend-api/codex/responses`、Codex headers、`store:false`、`include: reasoning.encrypted_content`、Responses message/function/custom-tool 事件和 usage/stop reason 映射；WS 连接复用、增量上下文和压缩作为后续优化。插件向 ki 只回传 delta RPC 事件。
- **模型与工具**：插件声明一组受支持的 Codex 模型和 thinking 映射；保留 `apply_patch` 的 custom/freeform tool；工具调用、reasoning signature 和 response id 必须能在 session resume 后重新编码。
- **配置体验**：provider 列表显示“订阅已配置/未配置”，提供 Login/Logout 和模型选择；CLI 增加等价的 login/logout 命令。不要让用户把 OAuth access token 当作 API key 填入 WebUI。
- **安全与测试**：OAuth callback 只监听明确的 loopback 地址和一次性 state；不把 token 放入 SSE、jsonl、错误日志或普通扩展事件。用 fake OAuth server + fake Codex SSE 做单测，再用真实账号做手工/live 测试；覆盖过期刷新、401、429、取消、工具循环、resume 和多 session 并发。

验收标准：启用插件后，用户无需编辑 `models.json` 或手工复制 token，完成一次 OAuth 登录即可在 WebUI/CLI 选择 Codex 模型并完成文本、thinking、工具调用、resume；禁用/卸载插件后，内置 provider 和已有 session 的历史不被破坏。
