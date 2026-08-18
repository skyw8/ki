# 模型与供应商配置方案

> 实现状态：已完成。内置离线目录、用户 registry、凭据、配置 API、设置 UI、会话 thinking、费用和 context usage 已落地；明确不包含在线刷新。

## 范围

在左下角“设置”中增加全局供应商和模型管理：

- 首期内置 OpenAI、Anthropic、DeepSeek、DashScope、Z.AI、Moonshot、MiniMax、Google、xAI。
- 用户可以新增、修改、禁用和删除自定义供应商及模型。
- 只支持现有 `completions`、`responses`、`anthropic` 三种协议。
- 模型目录完全离线，不实现远程目录、供应商 `/models` 探测或在线刷新。
- 全局配置决定可选模型；具体模型和 thinking effort 仍是会话配置。

## 内置供应商

国际和中国端点使用不同 provider ID，避免 Base URL、密钥和默认模型互相覆盖。

| 品牌 / provider ID | API | 默认 Base URL | 环境变量 |
|---|---|---|---|
| OpenAI / `openai` | `responses` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| Anthropic / `anthropic` | `anthropic` | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` |
| DeepSeek / `deepseek` | `completions` | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` |
| DashScope / `dashscope` | `completions` | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_API_KEY` |
| DashScope CN / `dashscope-cn` | `completions` | `https://dashscope.aliyuncs.com/compatible-mode/v1` | `DASHSCOPE_CN_API_KEY`，缺省回退到 `DASHSCOPE_API_KEY` |
| Z.AI / `zai` | `completions` | `https://api.z.ai/api/coding/paas/v4` | `ZAI_API_KEY` |
| Z.AI CN / `zai-cn` | `completions` | `https://open.bigmodel.cn/api/coding/paas/v4` | `ZAI_CODING_CN_API_KEY` |
| Moonshot / `moonshot` | `completions` | `https://api.moonshot.ai/v1` | `MOONSHOT_API_KEY` |
| Moonshot CN / `moonshot-cn` | `completions` | `https://api.moonshot.cn/v1` | `MOONSHOT_CN_API_KEY`，缺省回退到 `MOONSHOT_API_KEY` |
| MiniMax / `minimax` | `anthropic` | `https://api.minimax.io/anthropic` | `MINIMAX_API_KEY` |
| MiniMax CN / `minimax-cn` | `anthropic` | `https://api.minimaxi.com/anthropic` | `MINIMAX_CN_API_KEY` |
| Google / `google` | `completions` | `https://generativelanguage.googleapis.com/v1beta/openai` | `GEMINI_API_KEY` |
| xAI / `xai` | `completions`，模型可覆盖为 `responses` | `https://api.x.ai/v1` | `XAI_API_KEY` |

Google 首期使用官方 OpenAI compatibility 入口，以保持三协议边界。pi 的原生 Google 适配器能力更完整；Ki 只有出现明确兼容缺口后才增加第四种协议。

内置离线目录只收录支持工具调用的主流文本/视觉模型族：GPT、Claude、DeepSeek、Qwen、GLM、Kimi、MiniMax、Gemini/Gemma、Grok。不收录 realtime、音频、纯图片及不能调用工具的模型。目录改为嵌入式数据文件生成，不继续手写 `catalog.go`。

## 文件与职责

- 内置 catalog：随 Ki 版本发布的只读供应商和模型基线。
- `{KI_HOME}/models.json`：UI 管理的全局默认模型、供应商、模型、内置项覆盖和禁用状态，不保存密钥。
- `{KI_HOME}/credentials.json`：UI 保存的 API key，仅服务端读写，不经 API 返回明文。
- `ki.toml`：server、compaction 等运行配置；设置 UI 不修改全局或项目级 TOML。
- session `config.json`：保存 `provider`、`model`、`thinkingEffort`。

不再需要 `models-store.json`。全局文件使用临时文件加原子替换；保存成功后构建不可变 registry 快照。新请求获取新快照，进行中的请求继续使用旧快照。

凭据解析顺序为 `credentials.json` → 环境变量。API 只返回 `hasCredential` 和来源，不返回密钥或掩码后的密钥片段。

## Provider 定义

自定义 provider 的完整形状：

```json
{
  "id": "my-openai",
  "name": "My OpenAI",
  "api": "responses",
  "baseUrl": "https://example.com/v1",
  "enabled": true,
  "models": []
}
```

字段规则：

- `id`：全局唯一、创建后不可修改；仅允许字母、数字、`.`、`_`、`-`。
- `name`：展示名称。
- `api`：三种协议之一，是该 provider 下模型的默认协议。
- `baseUrl`：绝对 HTTP(S) URL；允许局域网和 localhost，末尾 `/` 统一去除。
- `enabled`：关闭后不参与默认解析和模型选择，但保留配置与凭据。
- `models`：用户新增的完整模型；至少添加一个模型后 provider 才可用于会话。
- `modelOverrides`：对内置模型做字段级覆盖或设置 `enabled=false`。

内置 provider 的 `id`、环境变量和内置模型不可删除；允许覆盖 `name`、`api`、`baseUrl`、`enabled`，也允许添加模型。删除其用户配置表示恢复内置值。

## Model 定义

```json
{
  "id": "example-model",
  "name": "Example Model",
  "enabled": true,
  "api": "completions",
  "contextWindow": 200000,
  "maxTokens": 32000,
  "input": ["text", "image"],
  "reasoning": true,
  "thinkingLevelMap": {
    "off": "off",
    "low": "low",
    "medium": "medium",
    "high": "high",
    "xhigh": null,
    "max": null
  },
  "cost": {
    "input": 1,
    "output": 4,
    "cacheRead": 0.1,
    "cacheWrite": 1.25,
    "tiers": []
  },
  "compat": {
    "thinkingFormat": "openai",
    "supportsReasoningEffort": true,
    "supportsDeveloperRole": false,
    "maxTokensField": "max_tokens",
    "requiresReasoningContent": false
  }
}
```

字段规则：

- `id`、`name`：请求 ID 和展示名称；同一 provider 内 ID 唯一，ID 是任意非空字符串并允许 `/`。
- `enabled`：关闭后不出现在模型选择器，已有会话再次请求时返回明确错误。
- `api`、`baseUrl`：可选模型级路由覆盖；通常继承 provider。
- `contextWindow`、`maxTokens`：正整数，分别用于上下文/压缩和最大输出。
- `input`：至少包含 `text`，可增加 `image`。
- `reasoning`：是否支持 thinking；为 false 时不显示 effort 控件。
- `thinkingLevelMap`：`off/minimal/low/medium/high/xhigh/max` 到上游值的映射；`null` 表示明确不支持。
- `cost`：可为 `null`；否则是每百万 token 的 input、output、cache read/write 单价。
- `cost.tiers`：可选长上下文阶梯价；命中的最高阈值作用于整个请求。
- `compat`：只保留确实改变请求形状的兼容字段。

`compat` 首期支持 `thinkingFormat`、`supportsReasoningEffort`、`supportsDeveloperRole`、`maxTokensField`、`requiresReasoningContent`，用于表达 OpenAI effort、DeepSeek `thinking`、Qwen `enable_thinking`、Z.AI `thinking` 和 Kimi reasoning 回放。先不引入 pi 的任意 headers、samplingParams 和完整 compat 矩阵。

用户新增模型的缺省值为 `name=id`、`enabled=true`、`input=["text"]`、`reasoning=false`、`contextWindow=128000`、`maxTokens=16384`、`cost=null`。UI 标记“自定义/元数据未确认”，不能从同 provider 的其他模型继承能力、上下文和价格。

## 合并与解析

registry 以 `(provider, model)` 为主键，按“内置 catalog → `models.json`”合并：

1. 自定义 provider 追加到目录。
2. `models` 按模型 ID upsert；同 ID 用户完整定义替换内置模型。
3. `modelOverrides` 最后做字段级覆盖。
4. 删除自定义模型会删除该条目；若它覆盖内置模型则恢复内置模型。
5. 禁用项保留配置，但不参与默认选择。

默认模型必须指向 enabled provider 下的 enabled model。删除、禁用当前默认项时要求先选择新默认项，不能静默落到目录第一项。已有 session 固定 provider/model；其模型被删除或禁用后保留历史，下一次请求返回可操作的配置错误。

## API 与设置界面

配置 API：

- `GET /v1/providers`：返回合并目录、来源、默认项和凭据状态。
- `POST /v1/providers`：新增自定义 provider。
- `PATCH /v1/providers/{id}`：修改、禁用或恢复 provider 覆盖。
- `DELETE /v1/providers/{id}`：删除自定义 provider；内置 provider 删除用户覆盖、恢复基线。
- `PUT /v1/providers/{id}/credential`：设置或显式清除 API key。
- `POST /v1/providers/{id}/models`：新增模型。
- `PATCH /v1/providers/{id}/models`：请求体携带模型 ID，修改、禁用或恢复模型覆盖。
- `DELETE /v1/providers/{id}/models?model=...`：删除用户模型；内置模型恢复基线。模型 ID 必须 URL 编码，不能作为 path segment。
- `PUT /v1/default-model`：设置全局默认 provider/model。

保留 `GET /v1/models` 作为会话模型选择器的扁平只读视图，数据来自同一 registry，不维护第二份 catalog。

设置弹窗增加“供应商”页：左侧 provider 列表，右侧编辑连接信息、凭据状态和模型表；提供“添加供应商”“添加模型”“设为全局默认”“恢复内置值”。简单表单展示常用字段，thinking map、cost tiers、compat 放在高级区域。所有写操作服务端校验后再落盘，409 用于 ID 冲突，422 用于字段或默认引用无效。

模型选择器只展示 enabled 且有凭据的项，并区分内置/自定义。环境变量提供凭据时显示来源，但不能通过 UI 清除该环境变量。

## Thinking 与计费

thinking effort 放在输入框模型按钮旁，仅在 `reasoning=true` 时显示。`xhigh`、`max` 只有被模型显式映射时才出现；切换模型时按 pi 的规则夹到最近可用等级。适配器根据 `api + compat.thinkingFormat` 转换请求参数，响应 thinking 继续使用现有 IR。

计费按 pi 口径：`input*inputRate + output*outputRate + cacheRead*cacheReadRate + cacheWrite*cacheWriteRate`。各协议 usage 先统一成互斥的 uncached input、cache read、cache write 和 output，避免重复计费。

每次 `request_header` 记录 provider、model、catalogVersion 和价格快照；费用由该快照与 assistant usage 计算，不持久化会话累计值。`cost=null` 时不显示金额；目录记录 `catalogVersion`、`generatedAt` 和来源，未知价格不能用零冒充免费。

## Context used

参考 deepseek-harness，在发送按钮旁显示上下文占用环，点击显示 `~used / contextWindow`。不能用会话累计 token 代替上下文：

1. 以最近一次模型返回的 prompt usage 为基线；没有 usage 时使用现有 `compact.EstimateTokens`。
2. 加上该次请求后新增消息的估算 token；压缩或切换模型后重新计算。
3. 新增并持久化 `loop.Event` 的 `context_usage`，沿现有 jsonl、SSE 和 session detail 到 WebUI，不增加专用查询路由。
4. `window` 来自当前模型并应用 compaction 的 `max_context_tokens` 上限；估算值使用 `~`。

## 实施顺序

1. 建立 9 个品牌（含区域 provider）的嵌入式 catalog 和 registry，替换当前手写 `Catalog` / 首项继承逻辑。
2. 实现 `models.json`、`credentials.json`、原子保存、热更新和配置 API。
3. 完成设置页、新增 provider、新增模型及模型选择联动。
4. 增加会话级 thinking effort、精简 compat 和三协议映射。
5. 增加 usage 规范化、费用快照、`context_usage` 和输入框占用环。

每一步同步更新 `docs/provider.md`、`docs/architecture.md`、`docs/session.md` 以及对应包的 `doc.go`。
