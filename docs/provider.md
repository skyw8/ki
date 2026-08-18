# 供应商协议

三套协议的请求形状不能混用。包入口见 `internal/provider/doc.go`。

## 协议

| API | 路径（拼在 base 后） | 消息 / 工具 |
|---|---|---|
| Completions | `/chat/completions` | `messages` + `role: tool` |
| Responses | `/responses` | `input` item：`message`、配对的 `function_call` / `function_call_output`，以及 `custom_tool_call` / `custom_tool_call_output` |
| Anthropic | `/v1/messages` | `tool_use` + user 里的 `tool_result`；system 可带 `cache_control` |

Responses **不能**把 Completions 的 `role: tool` 塞进 `input`，否则第二轮（回传工具结果）会 400。

## 离线目录与配置

- `catalog.json` 随二进制嵌入，内置 OpenAI、Anthropic、DeepSeek、DashScope、Z.AI、Moonshot、MiniMax、Google 和 xAI；DashScope、Z.AI、Moonshot、MiniMax 另有 `-cn` provider。它只随 Ki 发版更新，不调用供应商模型列表 API。
- `{KI_HOME}/models.json` 保存全局默认项、自定义 provider/model 和内置项覆盖；`{KI_HOME}/credentials.json` 只保存 API key（0600）。密钥解析顺序为 credentials 文件再到 provider 环境变量，API 从不返回明文。
- registry 按嵌入目录 → 用户配置合并，并在校验成功、原子替换文件后发布新快照。provider/model 可新增、禁用和删除；内置项删除覆盖即恢复基线。默认项不能被禁用或删除。
- provider 和模型可选 `completions` / `responses` / `anthropic`，模型可覆盖 provider 的 API/Base URL。Google 使用官方 OpenAI-compatible 入口。
- 模型的 `input` 声明输入模态；可选 `applyPatchToolType=freeform` 声明 Codex grammar-backed `apply_patch`。freeform custom tool 只允许配置在 Responses 模型上，避免生成协议无法表达的工具调用。

配置入口是 `GET/POST/PATCH/DELETE /v1/providers` 及其 `/credential`、`/models` 子资源，`PUT /v1/default-model` 修改全局默认。`GET /v1/models` 是同一 registry 的扁平可选视图，不存在第二份目录。

## 细节

- 发给模型前丢掉不该回放的 assistant（对齐 pi `transformMessages`）：`stopReason` 为 `aborted` / `error` 的整条跳过；无 text / thinking / toolCall 的空 assistant 也跳过。它们后面的对应 toolResult 一并丢掉。留下的 assistant 若有 toolCall 没结果，补一条 `No result provided`。会话 jsonl 仍保留这些行给 UI。
- 图片（对齐 pi）：user 里的 `image` 编成 Completions `image_url` / Responses `input_image` / Anthropic `image`。Completions 的 `role:tool` 只放文本；**连续** toolResult 先全部写出，这一组的图攒成一条 user 跟在这组后面（不要每张图插一条 user，否则并行 Read 会把 tool 拆开导致 400）。历史里更早的图仍待在当时那一组后面。Responses 图进 `function_call_output.output` 数组。Anthropic 图进 `tool_result`，连续结果收成一条 user。
- 切换到不含 `image` 输入模态的模型时，loop 在最终请求边界移除历史图片块；这同时兜底旧 Read 结果和返回图片的 MCP 工具。
- 模型解析顺序是显式 `provider/model` → session provider 下的模型 ID → registry 全局默认。新会话固定该引用；禁用后的已有会话保留历史，但下次请求明确失败。
- `thinkingEffort` 使用 `off/minimal/low/medium/high/xhigh/max`，按模型映射到 OpenAI effort、Qwen `enable_thinking`、DeepSeek/Z.AI `thinking` 或 Anthropic adaptive/budget 形状。切换模型时夹到最近的可用等级。
- usage 先归一为互斥的 uncached input/cache read/cache write/output，再按目录每百万 token 单价计算；`cost=null` 表示未知而不是免费。长上下文 tier 命中最高阈值。
- 流式工具参数：function 碎片拼成完整 JSON 再 `Unmarshal`；Responses custom tool 的 `response.custom_tool_call_input.*` 保留原始文本。回放时严格保持 function/custom 的 call-output 配对。
- `Scripted`：测试和 `KI_FAKE=1` 用。
