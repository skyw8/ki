# 供应商协议

三套协议的请求形状不能混用。包入口见 `internal/provider/doc.go`。

## 协议

| API | 路径（拼在 base 后） | 消息 / 工具 |
|---|---|---|
| Completions | `/chat/completions` | `messages` + `role: tool` |
| Responses | `/responses` | `input` item：`message` / `function_call` / `function_call_output` |
| Anthropic | `/v1/messages` | `tool_use` + user 里的 `tool_result`；system 可带 `cache_control` |

Responses **不能**把 Completions 的 `role: tool` 塞进 `input`，否则第二轮（回传工具结果）会 400。

## 细节

- `catalog.go`：内置 openai / anthropic / zhipu(+cn) / deepseek / dashscope(+cn)。dashscope 含 `qwen-plus` / `qwen-max` / `qwen3.7-plus`（国内 `dashscope-cn`，国际 `dashscope`）。未知 model id 仍用该 provider 的 API 和 base。
- 图片（对齐 pi）：user 里的 `image` 编成 Completions `image_url` / Responses `input_image` / Anthropic `image`。Completions 的 `role:tool` 只放文本；**连续** toolResult 先全部写出，这一组的图攒成一条 user 跟在这组后面（不要每张图插一条 user，否则并行 Read 会把 tool 拆开导致 400）。历史里更早的图仍待在当时那一组后面。Responses 图进 `function_call_output.output` 数组。Anthropic 图进 `tool_result`，连续结果收成一条 user。
- `resolve.go`：请求 `--model` → session 默认 → toml（有 key）→ 已有 key 的默认表 → 否则第一个有 key 的。
- 流式工具参数：碎片拼成完整 JSON 再 `Unmarshal`。Anthropic 要读 `content_block.input`；Responses 要处理 `function_call` 的 item / arguments.delta。
- `Scripted`：测试和 `KI_FAKE=1` 用。
- DeepSeek Anthropic 的 base 是 `https://api.deepseek.com/anthropic`，不是 Completions 那个根。
