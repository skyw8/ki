# 供应商协议

三套协议的请求形状不能混用。可复用的协议客户端入口见 `pkg/llmprotocol`；Ki 的目录、凭据和
运行时适配入口见 `internal/provider`。Live occupy 可经扩展 `before_provider_request` /
`before_provider_headers` 包装 Streamer 与 headers-only HTTPDoer；compact 只用 HTTPDoer，
看不见 `before_provider_request`。见 [extension.md](extension.md)。

## 协议形状

| API | 路径（拼在 base 后） | 消息 / 工具 |
|---|---|---|
| Completions | `/chat/completions` | `messages` + `role: tool` |
| Responses | `/responses` | `input` item：`message`、配对的 `function_call` / `function_call_output`，以及 `custom_tool_call` / `custom_tool_call_output` |
| Anthropic | `/v1/messages` | `tool_use` + user 里的 `tool_result`；system 可带 `cache_control` |

Responses **不能**把 Completions 的 `role: tool` 塞进 `input`，否则第二轮（回传工具结果）
会 400。

### Completions

对齐 Chat Completions wire contract：OpenAI provider 使用 `max_completion_tokens` 和
`stream_options.include_usage`；`prompt_tokens` 与 `prompt_tokens_details.cached_tokens`
原样接收，成本计算时再拆成 uncached input / cache read。SSE 消费 `choices[].delta` 的
content / refusal / tool_calls（以及兼容旧网关的 `function_call`），按
`tool_calls[].index` 累积 arguments。旧网关若在普通文本增量旁带空的 `function_call`
占位对象（名称和参数均为空），会忽略该占位，避免生成空工具名调用。终止原因覆盖 `stop`、
`length`、`tool_calls`、`function_call`、`content_filter`。

### Responses

core adapter 使用 `store:false` 和 `include:["reasoning.encrypted_content"]` 做无状态回放；
输出按 `item_id` 关联 message / reasoning / tool item，必须遇到 `response.completed` /
`response.failed` / `response.incomplete` 等终止事件才结束流。

文本消息兼容少数网关缺失 `message.id` 的返回：优先用 `item_id`，其次用 `output_index` 或
唯一未关闭文本项关联；function call、custom tool 和 reasoning 仍要求有效 item ID，以保证
无状态回放。

### Anthropic

对齐官方 SSE 生命周期：`message_start` → 带 `index` 的 `content_block_start/delta/stop` →
`message_delta` → `message_stop`；`error` 事件转为失败。text / thinking / tool input 按
block index 独立累积，保留 thinking signature 和 redacted-thinking data，tool input 必须
是 JSON object，未收到终止事件的流视为失败。

## 目录与凭据

### 内置目录

`catalog.json` 随二进制嵌入，内置 OpenRouter（默认模型 `openrouter/free`）、OpenAI、
Anthropic、DeepSeek、DashScope、Z.AI、Moonshot、MiniMax、Google、xAI 和 Magpie；
DashScope、Z.AI、Moonshot、MiniMax 另有 `-cn` provider。除 Magpie 外，目录随 Ki 发版
更新，不调用供应商模型列表 API。

### 用户配置

`{KI_HOME}/models.json` 保存上次选用的模型、自定义 provider/model 和内置项覆盖；
`{KI_HOME}/credentials.json` 保存 API key 或 provider-owned opaque credential（0600）。
密钥解析顺序为 credentials 文件再到 provider 环境变量，API 从不返回明文。

两个文件都带 schema `version`，通用规则见 [state.md](state.md)。读到旧版本时在加载阶段
就地迁移到当前 schema（例如 `models.json` v1 的 `remoteCompaction` 展开为 v2 的
`compaction.standalone` + `compaction.inline`），迁移只作用于内存，用户文件在下次写入时
才更新；读到更高版本则拒绝启动，避免静默丢弃本二进制不认识的字段。

### 合并与选用

registry 按「嵌入目录 → 用户配置」合并，并在校验成功、原子替换文件后发布新快照。
provider / model 可新增、禁用和删除；内置项删除覆盖即恢复基线。上次选用不可用时落到
第一个有凭据的可用模型，再不行落到目录里第一个启用项；没有「钉死不能禁用」的 default。

provider 和模型可选 `completions` / `responses` / `anthropic`，模型可覆盖 provider 的
API / Base URL。Google 使用官方 OpenAI-compatible 入口。扩展 provider 的 `defaultModel`
只是可选偏好；缺失、被删除或被禁用时自动使用第一个启用模型。

配置入口是 `GET/POST/PATCH/DELETE /v1/providers` 及其 `/credential`、`/models` 子资源；
Magpie 显式同步使用受保护的 `POST /v1/providers/magpie/models/refresh`。创建会话、改模型或
发 prompt 会把当前模型记进 `models.json` 的 last-used；`PUT /v1/default-model` 仍可显式
写入同一字段。`GET /v1/models` 是同一 registry 的扁平可选视图，不存在第二份目录。

### Magpie

Magpie 默认 base URL 为 `http://127.0.0.1:3425/v1`，使用内置 Completions adapter。设置页
的显式刷新请求先校验 `/api/hello` 身份，再读取 `/v1/models`；列出 provider 或模型时不联网。
刷新根据 base URL 自动定位 Magpie 根地址下的两个端点，所以 Completions 常见的 `/v1`
base 和 Anthropic 所需的根 base 都可用。

模型 ID（包括 `vendor/model` 和路由组 ID）原样保留为上游请求 ID。刷新会原子替换先前同步
的目录，保留用户手动添加模型和仍匹配的逐模型覆盖；已移除的同步模型及其孤立覆盖会清理。
同步 ID 记录在 `models.json` provider overlay 的 `syncedModels` 字段中，是向前兼容的新增
字段，不改变 schema version。未提供的限制值使用 32K context / 4K output 保守估值（并非
上游承诺，可在模型设置中修改）；未给出受支持推理等级的模型不暴露推理选择，价格保持未知
（`cost:null`）。

请求先原子快照连接与凭据、禁止跨站重定向；若网络请求期间连接或凭据变更，刷新以 409 结束
而不写入过时目录。本机 Magpie 接受任意 key，可填 `magpie-ki` 进行用量归因；远端必须使用
用户配置的 gateway key。Ki server（不是浏览器）连接 Magpie，所以两者必须网络可达。

## 模型能力

### 声明式能力

模型的 `input` 声明输入模态。`applyPatchToolType: "freeform"` 声明 Responses
custom/freeform patch 能力，`execToolType: "freeform"` 独立声明 custom/freeform
JavaScript 能力。core 中两者只在 Responses 上生效，不能从 API 名推断，也不能互相推断；
扩展 provider 的私有 API（如 Codex）自行声明并兑现该工具契约，host 不硬编码其 API 别名。

内置 OpenAI GPT 与 bundled `codex-oauth` GPT 明确声明两种能力；Completions / Anthropic
以及未声明 patch 能力的模型使用 `Write` / `Edit`，编辑器不会同时暴露。未声明 exec 能力
的模型使用普通 function `exec`，参数为 `{code: "…"}`。新增字段不改变 state schema 版本。

DeepSeek Responses 的 [custom 工具兼容性](https://api-docs.deepseek.com/guides/responses_api)
只允许 `apply_patch` 名称，其余 custom 名称返回 400；普通 function 工具受支持。因此
DeepSeek 三种协议都发布 function `exec`，不因切换为 Responses 而启用任意 custom 工具。

### DeepSeek

内置模型为 `deepseek-flash`（OpenAI Chat Completions，1M 上下文，384K 最大输出，原生多模态，
输入支持 `text` + `image`，thinking effort 为 `low`/`high`/`max`）；视觉输入复用
Completions `image_url` 编码。同一凭据还提供 Responses（根路径 `/responses`）与 Anthropic
（`/anthropic` + `/v1/messages`）入口；内置目录只暴露 Completions，live 测试覆盖三套协议
（`internal/provider/live_wire_test.go` 直接切换 API/base，端到端用例用 `models.json`
overlay）。模型名与发布信息以
[DeepSeek 更新日志](https://api-docs.deepseek.com/zh-cn/updates/) 为准。

### 模型解析

解析顺序是显式 `provider/model` → session provider 下的模型 ID → 上次选用（不可用则第一个
可用模型）。session 已保存的 provider/model 是两个独立字段；后续只改 thinking 或发送未带
model 的 prompt 时按这两个字段直接重建引用，不再解析 model ID，因为 Magpie 的
`vendor/model` 等 ID 本身可以含 `/`。

**带斜杠的显式 spec 不按第一个 `/` 切**：先认已知 provider 前缀
（`magpie/codex/gpt-6.1-sol` → magpie + `codex/gpt-6.1-sol`），再认 fallback provider 下的
完整模型 ID（会话内 `codex/gpt-6.1-sol` 仍落到该会话的 provider），创建会话没有 fallback
时用唯一属主；只有都不成立才退回首个 `/` 切分，让错误信息指出用户实际输入的 token。新
会话固定该引用；禁用后的已有会话保留历史，但下次请求明确失败。

### thinking effort

`thinkingEffort` 使用 `off/minimal/low/medium/high/xhigh/max`，按模型映射到 OpenAI effort、
Qwen `enable_thinking`、DeepSeek / Z.AI `thinking` 或 Anthropic adaptive/budget 形状。未指定
时用该模型的 default thinking（优先 `medium`）。切换模型时夹到最近的可用等级，而不是回到
default。`GET /v1/models` 每项带 `thinkingLevels` 与 `defaultThinking`。

扩展模型可声明 `fastServiceTier: "priority"`，host 为**每个可用基础等级**生成相邻的
`<level> fast` 选项；支持 `off` 的模型也提供 `off fast`，被映射隐藏的等级不会重新出现。
`thinkingLevelMap` 的 key 仍只允许标准基础等级，默认值仍是普通等级。会话只保存一个
`thinkingEffort`；组合值是客户端选择 ID，扩展在发请求前拆成基础 effort 与服务等级。切到
支持 Fast 的模型保留 Fast 意图并夹到最近基础等级；切到不支持的模型先去掉 Fast，再保留 /
夹取基础等级。内置 HTTP adapter 不声明该能力，也不接受未经拆分的 Fast 值。

bundled `codex-oauth` 普通选项省略 `service_tier`，Fast 选项发送 `"priority"`，并同步
`x-codex-routing-hint: model=<model>;tier=priority`；`off fast` 保持现有 off 语义，不发送
reasoning block。远程压缩与本地 summary 都继承会话选项。`ki run --thinking "high fast"`
设置新会话；resume 时通过现有会话 PATCH 原子设置模型与 thinking，忙碌时遵守 409。WebUI
和 deep-web-search 的 Codex search/summary 使用相同解析规则；public OpenAI search/summary
adapter 拒绝 Codex-only Fast。

## 请求构造与回放

### 历史清洗

发给模型前丢掉不该回放的 assistant（对齐 pi `transformMessages`）：`stopReason` 为
`aborted` / `error` 的整条跳过；无 text / thinking / toolCall 的空 assistant 也跳过。它们
后面的对应 toolResult 一并丢掉。留下的 assistant 若有 toolCall 没结果，补一条
`No result provided`。会话 jsonl 仍保留这些行给 UI。

### 图片

user 里的 `image` 编成 Completions `image_url` / Responses `input_image` / Anthropic
`image`。Completions 的 `role:tool` 只放文本；**连续** toolResult 先全部写出，这一组的图攒
成一条 user 跟在这组后面（不要每张图插一条 user，否则并行 Read 会把 tool 拆开导致 400）。
历史里更早的图仍待在当时那一组后面。Responses 图进 `function_call_output.output` 数组；
Anthropic 图进 `tool_result`，连续结果收成一条 user。

切换到不含 `image` 输入模态的模型时，loop 在最终请求边界移除历史图片块；这同时兜底旧
Read 结果和返回图片的扩展工具。

### 流式工具参数

function 碎片拼成完整 JSON 再 `Unmarshal`；Responses custom tool 的
`response.custom_tool_call_input.*` 保留原始文本，并把 delta、call ID 和工具名交给 loop 的
参数预览消费者。回放时严格保持 function / custom 的 call-output 配对。

声明 `applyPatchToolType: "freeform"` 的 Responses 模型会产生 custom `apply_patch` 调用；
遗留 custom 历史也继续可读。Responses 原生回放 custom call/output；当当前同名工具已改为
function 时，两者成对改成 function call/output，保留原始输入、call/item ID 与其它
canonical checkpoint 项。Completions / Anthropic 始终把 custom 历史改成同名 function
call。若当前 function schema 有唯一必填 string 字段，以该字段包装原始输入（如 `exec` 使用
`{"code": <原始文本>}`）；没有当前声明时，结构化回放保留传统 `{"input": <原始文本>}`。
无法无损映射到已声明 schema 时，在发送前报告不可重试错误；不丢弃 payload。结构化回放仍
丢弃没有对应 call 的孤立 tool result。

Completions / Anthropic 不支持发布 custom 工具声明；公共 `Client.Stream` 在发送前拒绝该
声明及未知工具类型，错误不可重试，不能静默编码成缺少 schema 的 function。Responses 的
供应商名称限制由调用方的模型能力选择负责，不在公共协议包硬编码。

toolResult 的 `details` 只供 session 和客户端使用；三种协议的请求转换都只序列化模型可见
`content` 和错误状态。

### 重试边界

确定性的请求/协议错误（普通 4xx、缺失终止事件、非法 SSE、请求体校验失败）不重试；网络
错误、408/409/425/429、5xx，以及兼容网关明确返回的瞬时 `bad_response_status_code` 才进入
loop 的退避重试。三种协议的请求体校验失败都在发送前返回 `nonRetryableError`，否则 loop
会按退避静默重试约 60 秒，用户只看到「没有回复」。

## 压缩能力

模型目录的 `compaction` 分开声明 `standalone` 与 `inline`。OpenAI core 模型使用
`{"standalone":"openai","inline":"openai"}`；Codex OAuth 使用 `{"standalone":"codex-v2"}`，
由 sidecar 发送普通 `/codex/responses` + `compaction_trigger`，并伪装 Codex CLI 客户端
（`originator: codex_cli_rs`、`x-codex-beta-features: remote_compaction_v2`、
`x-codex-window-id`，以及 body `client_metadata` 里的 installation/window/session/thread/
turn id 与 `x-codex-turn-metadata`；压缩请求的 `request_kind` 为 `compaction`）。ChatGPT
Codex 后端只对声明该 beta 能力的客户端兑现 trigger，否则返回空 output；它不会误启用
OpenAI inline 字段。不能从 API 名或模型名推断。

standalone output 不裁剪；inline 普通流从最后一个 compaction item 到 terminal output 末尾
构造 checkpoint。上述 wire contract 以 OpenAI 官方
[Compaction](https://developers.openai.com/api/docs/guides/compaction) 为准：standalone 输入
本身仍须放入模型窗口，返回 output 是不可自行裁剪的下一 canonical window；`store:false`
保持 ZDR-friendly。

`[compaction] mode=auto|local|remote` 选择 host checkpoint 策略；`server_side=true` 只使用
inline 能力。`auto` 的 remote 调用失败后使用现有 local summary，取消/超时以及已发生
provider context 脱敏变换时不 fallback；`remote` 直接报告失败。opaque binding 包含
provider / API / base / model / 实际请求 credential fingerprint / compaction protocol。
存在 `before_provider_headers` 时 remote / server-side compaction 保守禁用。触发时机与阈值见
[architecture.md](architecture.md)。

## Codex 请求身份

Codex 请求身份与 metadata 共用 `pkg/codexclient`：installation ID 存在 versioned
`{KI_HOME}/codex-client.json`，logical turn ID 跨工具 continuation/retry 保持稳定，窗口在
成功远程压缩后推进。`prompt_cache_key` 默认等于 session ID；fork 出的会话继承源会话的
`config.cacheKey`，此时 body 里的 `prompt_cache_key` 和 `session-id` 头指向源（ChatGPT 后端
据该头做缓存亲和），而 `thread-id`、client metadata、window/sticky 状态仍绑定子会话，因此
子会话不会复用父会话的流式续传基线。`CreateChild` 不继承，因为委派子代理从空上下文开始、
与父会话没有共享前缀。

Responses WebSocket 复用按会话、凭据与请求属性隔离，增量 continuation 保留服务端 output；
无法握手时回退 HTTP，已收到流式事件后不得盲目重发。HTTP Codex body 使用 zstd。这些实现
对齐 Codex 客户端协议，但不保证 TLS/提示词/工具或可选 attestation 与官方客户端不可区分，
也不保证账号授权、速度或额度倍率。

WebSocket 握手只把明确的 426 当作 HTTP capability fallback，普通请求/API 错误不会被吞掉。
连接/lease（含 gate 等待）上限 64，容量压力可安全回退 HTTP；inactive TTL 为 5 分钟。增量
baseline 保存序列化 prefix，单连接 4 MiB、全局 32 MiB；超预算保留 full-request 路径而不
拒绝长历史。routing metadata 按 endpoint/account/credential/model/tier binding 与 logical
turn 隔离，最多 256 binding、每个 8 turn token。观察过上游事件的失败通过 provider RPC 的
`nonRetryable` 标记阻止 host loop 再次提交。

provider 扩展（`extensions/codex-oauth`）与 core adapter 共享同一套 item 关联规则，并额外
防污染：流式关联以 provider item ID 为准，`output_index` 和 `call_id` 只作为 delta 缺 ID 时
的回退（同一个 `output_index` 被多个 item 复用时以最后注册的 item 为准）；reasoning item
只保留 API 允许的字段，落盘和回放前各过滤一次。reasoning 的 `content` 与 Responses
`status` 都是输出侧字段，不随历史 input 重放；Codex sidecar 仅在构造请求副本时过滤它们，
session 中的 canonical compaction / message 历史保持不变。

## 用量与成本

usage 先归一为互斥的 uncached input / cache read / cache write / output，再按目录每百万
token 单价计算；`cost=null` 表示未知而不是免费。长上下文 tier 命中最高阈值。DeepSeek 官方
有高峰/空闲两套价，内置目录用高峰；目录不按时段切换。

extension provider 的 stream / standalone compact 返回已归一的 usage；Host 按本次 resolved
model 的价格补齐缺失的 `usage.cost`，不再次扣除缓存 token，也不覆盖 sidecar 显式提供的
成本。费用随新消息/压缩记录落盘并通过现有 SSE、session compact stats 到达 WebUI；旧的
无成本记录不会因目录更新而回填。

`codex-oauth` 的美元成本仅表示**按 API 标准价折算的使用估算**，不表示 ChatGPT 订阅实际
扣费。订阅内额度和购买的 Codex credits 以
[Codex 官方 rate card](https://help.openai.com/en/articles/20001106-codex-rate-card) 为准；
Ki 不做 credits→美元换算。目录不自动应用 Batch/Flex/Fast、区域附加费或套餐折扣；用户
内置模型覆盖仍可自定义 `cost`。

## Provider 扩展

扩展声明 `provider` capability 和 `providers` 目录项后，provider 会以 `runtime.kind=rpc`
的进程级 sidecar 注册到 Registry。provider 扩展只从 `{KI_HOME}/extensions` 全局发现。
provider 的 `api` 可以是内置协议名，也可以是扩展自定义字符串；自定义 API 不会落入 ki 的
Completions / Responses / Anthropic HTTP adapter。

服务端把完整 `loop.Request` 通过 `provider.stream.start` 交给 sidecar；sidecar 负责 request
body、headers、网络、SSE 和 provider-specific 状态。声明 `compaction.standalone` 的
sidecar 还要实现同步 `provider.compact`，返回完整 `{items,usage?}` canonical window；只有
声明并实际启用 `compaction.inline:"openai"` 的 occupy 才接受 stream event 的私有
`responsesItems`。

扩展 provider 不写入 `models.json`，其目录随扩展启用状态动态替换；provider/model 的 CRUD
端点对这类目录只读。API key 继续使用 `{"apiKey":"..."}`，OAuth 或其他扩展凭据使用
`{"type":"oauth","value":{...}}`，`value` 原样保存和私有传递，catalog/status 不包含明文。
扩展 provider 目录仍由扩展描述符静态声明，目前没有扩展模型发现/刷新 RPC；Magpie 使用已
支持的公共 wire API，不需要 sidecar。

OAuth provider 的登录/刷新也由 sidecar 完成：`provider.auth.start` 启动 browser 或
`device_code` 流程，`provider.auth.event` 只上报 UI-neutral 的授权 URL、设备码、完成或
错误；`provider.auth.input` 接收手工 redirect URL/code，`provider.auth.cancel` 终止流程，
`provider.auth.refresh` 在凭据临近过期时返回新的 opaque value。Server 的
`/v1/providers/{id}/auth/*` 只暴露脱敏状态，完成事件才原子写入 `credentials.json`，因此
WebUI 不需要、也不能把 OAuth access token 当 API key 输入。

Responses provider 的 `types.Content.ItemID`、`ArgumentsRaw`、`ThinkingSignature`、
`TextSignature` 以及 `types.Message.ResponseID` 会随 jsonl 保存；它们对通用 loop 透明，
Codex 扩展可据此重新编码 reasoning / function / custom tool item。流式期间仍只通过 compact
delta 传输，raw provider payload 不进入 SSE。

## 公共协议包

`pkg/llmprotocol` 是不依赖 Ki agent loop 的公共 Go 包，提供协议中立的 `Request`、
`Message`、`Content`、`Usage`、`AssistantDelta` 和 `Client`，以及 Completions、Responses、
Anthropic 的请求编码与 SSE 流解析。它不负责模型目录、凭据、重试、session 或工具执行；
调用方负责这些策略。

`internal/provider` 仅把 `loop.Request` / `types.Message` 适配到公共包，并继续负责 Ki 的
catalog、registry、credential、cost 和 extension runtime。协议实现的新增 wire 行为应优先
修改 `pkg/llmprotocol`，而不是在适配层复制一份。

## 流式读取活性

内置三种 HTTP 协议共用 `pkg/llmprotocol.Client.IdleTimeout`（`NewClient` 默认 5 分钟）。
Ki 的全局或项目 `ki.toml` 可设置：

```toml
[streaming]
idle_timeout_seconds = 300 # 0 disables; negative values are rejected
```

计时覆盖 response body 的单次阻塞 Read：任何字节（含心跳、reasoning）都算活性，解析、
emit 和工具执行不计入。它不限制等待 HTTP headers 的时间，也不是「正文 5 分钟没变就中止」。
到期关闭 body 解阻塞；已有 partial 时保留内容并返回不重试错误，避免重复答案或工具调用；
用户中止时 loop 的最终 aborted 消息也保留已经输出的 partial，尚无内容时沿现有 loop 退避
策略重试。extension provider 自管传输与超时，此配置只作用于内置 adapter。

loop 的 debug `provider delta` 采样记录增量间隔、emit 耗时与字节数，用于区分上游空档和
本地发送阻塞，不记录正文。

`Scripted` 是测试和 `KI_FAKE=1` 用的假模型实现。
