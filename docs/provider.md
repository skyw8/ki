# 供应商协议

三套协议的请求形状不能混用。包入口见 `internal/provider/doc.go`。Live occupy 可经扩展 `before_provider_request` / `before_provider_headers` 包装 Streamer 与 headers-only HTTPDoer；compact 只用 HTTPDoer，看不见 `before_provider_request`。见 [extension.md](extension.md)。

## 协议

| API | 路径（拼在 base 后） | 消息 / 工具 |
|---|---|---|
| Completions | `/chat/completions` | `messages` + `role: tool` |
| Responses | `/responses` | `input` item：`message`、配对的 `function_call` / `function_call_output`，以及 `custom_tool_call` / `custom_tool_call_output` |
| Anthropic | `/v1/messages` | `tool_use` + user 里的 `tool_result`；system 可带 `cache_control` |

Responses **不能**把 Completions 的 `role: tool` 塞进 `input`，否则第二轮（回传工具结果）会 400。

## 离线目录与配置

- `catalog.json` 随二进制嵌入，内置 OpenAI、Anthropic、DeepSeek、DashScope、Z.AI、Moonshot、MiniMax、Google 和 xAI；DashScope、Z.AI、Moonshot、MiniMax 另有 `-cn` provider。它只随 Ki 发版更新，不调用供应商模型列表 API。
- `{KI_HOME}/models.json` 保存上次选用的模型、自定义 provider/model 和内置项覆盖；`{KI_HOME}/credentials.json` 保存 API key 或 provider-owned opaque credential（0600）。密钥解析顺序为 credentials 文件再到 provider 环境变量，API 从不返回明文。
- registry 按嵌入目录 → 用户配置合并，并在校验成功、原子替换文件后发布新快照。provider/model 可新增、禁用和删除；内置项删除覆盖即恢复基线。上次选用不可用时落到第一个有凭据的可用模型，再不行落到目录里第一个启用项；没有「钉死不能禁用」的 default。
- provider 和模型可选 `completions` / `responses` / `anthropic`，模型可覆盖 provider 的 API/Base URL。Google 使用官方 OpenAI-compatible 入口。
- 模型的 `input` 声明输入模态；可选 `applyPatchToolType=freeform` 声明 Codex grammar-backed `apply_patch`。freeform custom tool 只允许配置在 Responses 模型上，避免生成协议无法表达的工具调用。

配置入口是 `GET/POST/PATCH/DELETE /v1/providers` 及其 `/credential`、`/models` 子资源。创建会话、改模型或发 prompt 会把当前模型记进 `models.json` 的 last-used；`PUT /v1/default-model` 仍可显式写入同一字段。`GET /v1/models` 是同一 registry 的扁平可选视图，不存在第二份目录。

## 细节

- 发给模型前丢掉不该回放的 assistant（对齐 pi `transformMessages`）：`stopReason` 为 `aborted` / `error` 的整条跳过；无 text / thinking / toolCall 的空 assistant 也跳过。它们后面的对应 toolResult 一并丢掉。留下的 assistant 若有 toolCall 没结果，补一条 `No result provided`。会话 jsonl 仍保留这些行给 UI。
- 图片（对齐 pi）：user 里的 `image` 编成 Completions `image_url` / Responses `input_image` / Anthropic `image`。Completions 的 `role:tool` 只放文本；**连续** toolResult 先全部写出，这一组的图攒成一条 user 跟在这组后面（不要每张图插一条 user，否则并行 Read 会把 tool 拆开导致 400）。历史里更早的图仍待在当时那一组后面。Responses 图进 `function_call_output.output` 数组。Anthropic 图进 `tool_result`，连续结果收成一条 user。
- DeepSeek 的内置视觉模型为 `deepseek-v4-flash-vision-exp`（实验性，OpenAI Chat Completions，1M 上下文，输入支持 `text` + `image`）；它复用上述 Completions `image_url` 编码。模型名和发布信息以 [DeepSeek 更新日志](https://api-docs.deepseek.com/zh-cn/updates/) 为准。
- 切换到不含 `image` 输入模态的模型时，loop 在最终请求边界移除历史图片块；这同时兜底旧 Read 结果和返回图片的 MCP 工具。
- 模型解析顺序是显式 `provider/model` → session provider 下的模型 ID → 上次选用（不可用则第一个可用模型）。新会话固定该引用；禁用后的已有会话保留历史，但下次请求明确失败。
- `thinkingEffort` 使用 `off/minimal/low/medium/high/xhigh/max`，按模型映射到 OpenAI effort、Qwen `enable_thinking`、DeepSeek/Z.AI `thinking` 或 Anthropic adaptive/budget 形状。未指定时用该模型的 default thinking（优先 `medium`）。切换模型时夹到最近的可用等级，而不是回到 default。`GET /v1/models` 每项带 `thinkingLevels` 与 `defaultThinking`。
- usage 先归一为互斥的 uncached input/cache read/cache write/output，再按目录每百万 token 单价计算；`cost=null` 表示未知而不是免费。长上下文 tier 命中最高阈值。DeepSeek 官方有高峰/空闲两套价，内置目录用高峰（空闲是一半）；目录不按时段切换。
- 流式工具参数：function 碎片拼成完整 JSON 再 `Unmarshal`；Responses custom tool 的 `response.custom_tool_call_input.*` 保留原始文本，并把 delta、call ID 和工具名交给 loop 的参数预览消费者。回放时严格保持 function/custom 的 call-output 配对。
- Completions 对齐 Chat Completions wire contract：OpenAI provider 使用 `max_completion_tokens` 和 `stream_options.include_usage`；`prompt_tokens` 与 `prompt_tokens_details.cached_tokens` 原样接收，成本计算时再拆成 uncached input/cache read。SSE 消费 `choices[].delta` 的 content/refusal/tool_calls（以及兼容旧网关的 `function_call`），按 `tool_calls[].index` 累积 arguments，并处理 `stop`、`length`、`tool_calls`、`function_call`、`content_filter`。
- Anthropic Messages 对齐官方 SSE 生命周期：`message_start` → 带 `index` 的 `content_block_start/delta/stop` → `message_delta` → `message_stop`；`error` 事件转为失败。text/thinking/tool input 按 block index 独立累积，保留 thinking signature 和 redacted-thinking data，tool input 必须是 JSON object，未收到终止事件的流视为失败。
- Responses core adapter 使用 `store:false` 和 `include:["reasoning.encrypted_content"]` 做无状态回放；输出按 `item_id` 关联 message/reasoning/tool item，必须遇到 `response.completed` / `response.failed` / `response.incomplete` 等终止事件才结束流。
- toolResult 的 `details` 只供 session 和客户端使用；Completions、Responses、Anthropic 的请求转换都只序列化模型可见 `content` 和错误状态。
- `Scripted`：测试和 `KI_FAKE=1` 用。

## Provider 扩展

扩展声明 `provider` capability 和 `providers` 目录项后，provider 会以 `runtime.kind=rpc` 的进程级 sidecar 注册到 Registry。provider 扩展只从 `{KI_HOME}/extensions` 全局发现。provider 的 `api` 可以是内置协议名，也可以是扩展自定义字符串；自定义 API 不会落入 ki 的 Completions/Responses/Anthropic HTTP adapter。

服务端只做三件事：把 provider/model/auth 元数据并入离线目录、按 provider 解析凭据、把一次完整 `loop.Request` 通过 `provider.stream.start` 交给 sidecar。RPC 中的 `request` 使用显式 lower camelCase 字段名（如 `messages`、`system`、`maxTokens`），不能依赖 Go 默认 JSON 字段名。sidecar 负责完整 request body、headers、网络传输、SSE 解析、provider-specific tool/reasoning 状态和最终 message；Host adapter 只消费紧凑增量、做背压/取消并重建 `loop.AssistantDelta`。因此 Codex 这类需要专用客户端伪装的 streamer 应放在 provider 扩展里。

扩展 provider 不写入 `models.json`，其目录随扩展启用状态动态替换；provider/model 的 CRUD 端点对这类目录只读。API key 继续使用 `{"apiKey":"..."}`，OAuth 或其他扩展凭据使用 `{"type":"oauth","value":{...}}`，`value` 原样保存和私有传递，catalog/status 不包含明文。

OAuth provider 的登录/刷新也由 sidecar 完成：`provider.auth.start` 启动 browser 或 `device_code` 流程，`provider.auth.event` 只上报 UI-neutral 的授权 URL、设备码、完成或错误；`provider.auth.input` 接收手工 redirect URL/code，`provider.auth.cancel` 终止流程，`provider.auth.refresh` 在凭据临近过期时返回新的 opaque value。Server 的 `/v1/providers/{id}/auth/*` 只暴露脱敏状态，完成事件才原子写入 `credentials.json`，因此 WebUI 不需要、也不能把 OAuth access token 当 API key 输入。

Responses provider 的 `types.Content.ItemID`、`ArgumentsRaw`、`ThinkingSignature`、`TextSignature` 以及 `types.Message.ResponseID` 会随 jsonl 保存；它们对通用 loop 透明，Codex 扩展可据此重新编码 reasoning/function/custom tool item。流式期间仍只通过 compact delta 传输，raw provider payload 不进入 SSE。
