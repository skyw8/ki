# freerouter 扩展方案

> **状态：已实现**（2026-08）。实现位于 `{KI_HOME}/extensions/freerouter/`（运行中服务实际使用 `/data/hgy/ki/extensions/freerouter`），TypeScript 源码编译为 Node ESM。
> 与本方案的差异：
> - settings schema 在 manifest 中的字段名是 `config`（ki 的 `ConfigSpec{schema,defaults}`），能力名仍是 `settings`。
> - 凭据顺序为：宿主 credential（`PUT /v1/providers/free-router/credential`）→ 扩展 config `apiKey` → `OPENROUTER_API_KEY` 环境变量；宿主凭据未配置时 session run 会被宿主拒绝，因此推荐第一种。
> - sidecar 事件不含 `partial` 快照（ki 契约），routing 进度放在 contentIndex 0 的 thinking 块，赢家内容块索引整体 +1，最终 message 的 content[0] 回填同一段 thinking 保证与流一致。
> - 401 类非配额错误按 exhausted 处理（进入 90s 冷却），仅 402 为 fatal。
> 验证：`node --test test/*.test.mjs` 41/41 通过（含 spawn 真进程 + 本地 mock OpenRouter 的 e2e）；并已接入运行中的 `ki serve` 做真实链路冒烟：竞速、冷却、错误回传（`stopReason=error`）均符合契约。
> 配置 UI：WebUI 为 `freerouter` 提供了定制表单（`FreeRouterConfigForm`，dispatch 在 `web/src/SessionConfig.tsx`），文案走扩展自带 i18n（manifest `i18n.resources` 指向 `locales/*.json`，注意是 locale→文件路径，不是内联文案表）；时长字段以秒/分钟展示，保存时换算回 ms。Playwright e2e：`web/e2e/extension-ui.spec.ts` 的 "freerouter config form" 用同名 fixture 验证表单往返，不落裸 JSON。

> 前置阅读：[pi-freerouter-analysis.md](pi-freerouter-analysis.md)（原版源码分析）。
> 目标：仿照 pi-freerouter，为 ki 写一个 provider 扩展 `freerouter`，把请求竞速路由到 OpenRouter 免费模型层。**每轮竞速的候选模型数可配置（`raceWidth`），默认 2。**
> 结论先行：ki 现有 provider sidecar 契约（`provider.stream.start` / `provider.stream.event` / `provider.stream.cancel`）已覆盖全部所需能力，**不需要改 ki 宿主代码**，纯扩展即可实现。

## 一、与 pi-freerouter 的宿主差异对照

| 关注点 | pi（pi-freerouter 的宿主） | ki | 对本方案的影响 |
|---|---|---|---|
| 注册方式 | JS 进程内 `pi.registerProvider` + `streamSimple` 回调 | `extension.json` 声明 `providers[]` 离线目录 + 进程级 RPC sidecar | 改为 NDJSON JSON-RPC sidecar（Node/TS） |
| 事件流 | 扩展自建 `AssistantMessageEventStream`，事件带完整 `partial` | `provider.stream.event` 通知，**只发增量**，宿主 adapter 内存重建 partial；`done` 可带完整 `message` | 不需要 snapshot/deep-clone，事件更瘦 |
| 协议接管 | 自造 `api: "freerouter"`，完全绕过 pi-ai | `api` 是自由字符串，sidecar 全权负责请求构造与响应解析 | 同样可完全接管，直接调 OpenRouter chat-completions |
| 取消 | `AbortSignal` 通过 `streamSimple` options 传入 | Host 发 `provider.stream.cancel {requestId}` | sidecar 内部维护 requestId → AbortController 映射 |
| 凭据 | 每次从 `process.env` 读 | `ProviderStreamRequest.Credential` 传入，或走 `provider.auth.*` 流程 | 见 §4：用 `settings` secret 存 apiKey，宿主自动脱敏 |
| 消息 IR | pi 内部 Message（`toolResult` role、toolCall 内嵌 content[]） | `loop.Request`（`System`、`Messages []types.Message`、`Tools []ToolSpec`、`MaxTokens`、`SessionID`） | 转换层对接 ki 的 `internal/types` IR，字段见 `go doc ./internal/types` |
| 配置 | 扩展自管 | `settings` capability 声明 schema，`GET/PATCH /v1/extensions/{name}/config` 校验存储，`config.updated` 通知 sidecar | `raceWidth` 走这条路 |
| 进程模型 | 随 Pi 进程内 | 进程级 sidecar，全局最多一个，reload 保留无变化进程 | 冷却表/模型缓存天然跨 session 共享 |

## 二、包布局与 manifest

```
{KI_HOME}/extensions/freerouter/
├── extension.json
├── bin/
│   └── extension.mjs          # sidecar 入口（编译产物，或直接 ESM 源码）
├── src/
│   ├── index.mjs              # JSON-RPC readLoop：initialize / provider.stream.* / config.updated
│   ├── discovery.mjs          # 免费模型发现 + 排序（speedScore + context 升序）
│   ├── router.mjs             # TTL 冷却表（exhausted 90s / slow 15s）
│   ├── racer.mjs              # 竞速状态机（每轮 raceWidth 个候选）
│   └── openrouter.mjs         # ki IR → OpenRouter 消息转换 + SSE 解析
└── test/                      # node --test
```

`extension.json`：

```json
{
  "name": "freerouter",
  "version": "0.1.0",
  "description": "Race-routes requests through OpenRouter's free model tier",
  "capabilities": ["provider", "settings"],
  "providers": [{
    "id": "free-router",
    "name": "FreeRouter",
    "api": "freerouter",
    "baseUrl": "https://openrouter.ai/api/v1",
    "models": [{
      "id": "auto",
      "name": "FreeRouter Auto",
      "contextWindow": 128000,
      "maxTokens": 8192,
      "input": ["text"]
    }]
  }],
  "settings": {
    "schema": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "apiKey": { "type": "string", "secret": true, "description": "OpenRouter API key (free tier)" },
        "raceWidth": { "type": "integer", "minimum": 1, "maximum": 8, "default": 2, "description": "候选模型数，每轮并发竞速" },
        "exhaustedTtlMs": { "type": "integer", "default": 90000 },
        "slowTtlMs": { "type": "integer", "default": 15000 },
        "firstTokenTimeoutMs": { "type": "integer", "default": 10000 },
        "maxBatches": { "type": "integer", "minimum": 1, "maximum": 6, "default": 3 },
        "refreshIntervalMs": { "type": "integer", "default": 3600000 }
      }
    }
  },
  "runtime": { "kind": "rpc", "command": "node", "args": ["bin/extension.js"] }
}
```

要点：

- **伪模型**：目录里只声明 `auto` 一个模型，真实免费模型列表由 sidecar 运行时发现。`contextWindow` 用保守的 128k（免费模型普遍 32k–128k，声明过大可能把超长上下文塞给小窗口模型——与 pi-freerouter 相同的已知局限，接受）。
- **`raceWidth` 默认 2**（需求）：比 pi-freerouter 的 3 更省配额——竞速的隐性成本是落选模型的请求也计入各模型的免费限流，宽度 2 在"首 token 延迟收益"和"配额消耗"之间更平衡。范围钳制 1–8；`=1` 时退化为串行轮询（不并发，仍保留冷却回退语义）。
- 凭据优先级：settings `apiKey`（secret，宿主脱敏存储，`config.updated` 后 sidecar 读私有配置）→ 回落 `OPENROUTER_API_KEY` 环境变量（`runtime.env` 之外，sidecar 直接读 `process.env`）。

## 三、核心流程（sidecar）

### 3.1 请求生命周期

```
Host → provider.stream.start {requestId, model:"auto", credential, request{system, messages, tools, maxTokens, sessionId}}
sidecar:
  1. ensureModels()：懒加载 GET /models 过滤 :free（剔 moderation/vision 类），缓存 + 定时刷新
  2. 循环 batch = 1..maxBatches：
     a. candidates = router.nextModels(∞) − triedThisRequest，取前 raceWidth 个
     b. 并发 POST chat/completions（stream:true），各自独立 AbortController
     c. 竞速：谁先产生 text_start / toolcall_start / 空 done 谁赢
        - 赢家：增量事件原样转发（text_delta / toolcall_delta / done…），contentIndex 由 sidecar 统一编号
        - 落选者：立即 abort
     d. 无赢家 → 分档冷却（429/5xx/400/422 → exhaustedTtl；超时 → slowTtl）→ 下一轮
     e. 402（欠费）→ fatal，立即终止并回 error
  3. 全部耗尽 → error："All free models exhausted…"
Host → provider.stream.cancel {requestId} → abort 全部在途候选，回 stopReason=aborted 的 error
```

### 3.2 事件映射（sidecar → `provider.stream.event`）

| 内部事件 | 通知 params |
|---|---|
| 竞速进度 | `thinking_delta`（"Round 1: groq/llama-3.3:free, cerebras/qwen:free"），宿主 UI 自然展示 |
| 赢家产生 | `thinking_end`（"Using <modelId>"），先于正文 |
| 正文 | `text_start` / `text_delta` / `text_end` |
| 工具调用 | `toolcall_start`（带 `toolCallId`+`toolName`）/ `toolcall_delta` / `toolcall_end`（带完整 `toolCall`） |
| 结束 | `done` + 完整 `message`（ki `types.Message`，含 usage、stopReason） |
| 失败 | `error`（宿主按契约丢弃 partial 并展示 errorMessage） |

与 pi-freerouter 的关键简化：ki 事件**不带 `partial` 快照**，宿主 adapter 自行重建（`applyProviderStreamEvent`，见 `internal/extension/provider_rpc.go`）。因此原版的 `snapshot()` deep-clone、以及"文本固定在 content[0]、靠 buffer 重放"的技巧都不需要——sidecar 只需保证事件配对（每个 `*_start` 有对应 `*_end`）和 contentIndex 单调一致。

### 3.3 竞速状态机要点（照搬并修正 pi-freerouter 的经验）

1. **`Map<候选下标, Promise>` 做 `Promise.race`**，不用数组下标过滤（原版已踩过的坑，直接绕开）。
2. **获胜判定**：`text_start` / `toolcall_start` / `done`；`thinking_start` 等前置事件继续消费不判胜。注意 OpenRouter 免费模型有的会先吐 reasoning chunk——这些不算赢。
3. **双重防卡死**：批级 first-token 超时（自适应：第 1/2/3 批 10s/15s/20s）+ 赢家转发阶段每 `.next()` 与 idle 超时 race（30s），停滞即 abort 并报 error。
4. **`triedThisRequest` Set**：防止 `slowTtl < firstTokenTimeout` 时超时模型在下一轮回池造成死循环；每请求最多尝试 `maxBatches × raceWidth` 个不同模型（默认 3×2=6）。
5. **错误分级**：402 = `ModelFatalError`（立即冒泡）；429/5xx = exhausted（90s）；400/422 = exhausted（模型拒绝该请求形态，如不支持 tools）；HTTP 200 + 内联 `{"error":...}` chunk 同样按此分级。
6. **`raceWidth=1` 特判**：跳过竞速逻辑直接顺序尝试，省掉一个 AbortController 和 race 开销；语义上等价于"带冷却的串行回退"。
7. **每请求局部状态 vs 跨请求状态**：`triedThisRequest`/batchCount 每请求重建；`exhausted` 冷却表、模型列表缓存是 sidecar 进程级单例（跨 session 共享，ki 的 provider sidecar 本来就是进程级资源，这点比 pi 更自然）。

### 3.4 ki IR → OpenRouter 协议转换

输入：`request.System`、`request.Messages`（ki `types.Message`）、`request.Tools`（`loop.ToolSpec`，schema 形态跟 Claude Code）。转换规则与 pi-freerouter 的 `normalizeMessages` 同构（user/assistant/toolResult → OpenRouter 的 user/assistant+tool_calls/tool），差异：

- 以 ki `internal/types` 的实际 JSON 形状为准实现，先 `go doc ./internal/types` 核对 content 块类型（text / thinking / toolCall 等），`thinking` 块丢弃不回传（免费模型基本不支持 reasoning 回传，回传反而可能 400）。
- `tools` 透传为 OpenRouter `tools[]`；不支持 function calling 的免费模型会 400 → 按 exhausted 跳过，自然收敛到支持的模型。
- `maxTokens`：min(宿主 request.MaxTokens, 模型目录 maxTokens)。

## 四、配置热更新

- Host 在 PATCH `/v1/extensions/freerouter/config` 后发 `config.updated`（脱敏 config）→ sidecar 重新读私有配置文件，**在途请求沿用旧参数，新请求用新值**（每次 `provider.stream.start` 开头快照一次 config，原版"每请求快照 localRouter"的同款手法）。
- `raceWidth` 变更即时生效（作用于下一个请求的第一轮），无需重启 sidecar。
- `refreshIntervalMs` 变更重置定时器。

## 五、测试

| 层 | 内容 | 方式 |
|---|---|---|
| 单元 | discovery 过滤/排序（groq 优先、context 升序、非助手模型剔除）；router TTL 过期/回池/markSlow 不降级；racer 竞速获胜/全超时/402 fatal/raceWidth=1 | `node --test`，mock fetch（注入假 `/models` 与 SSE 流） |
| 协议 | IR→OpenRouter 转换、SSE 解析（内联 error chunk、无 `[DONE]` 兜底、tool call delta 累积）、事件配对 | 单测 + gold file |
| 宿主联动 | extension.json 校验、sidecar 拉起、provider.stream 事件经 `applyProviderStreamEvent` 重建 | `go test ./e2e`（fake model 链路 + 假 OpenRouter httptest server） |
| 实链路 | 真实 OpenRouter 免费 key 竞速 | `scripts/run.sh` 起服务，`ki` CLI / WebUI 选中 `free-router/auto` 手测 |

## 六、交付物与不做的事

**做**：包布局如 §2；sidecar 五个模块；配置含 `raceWidth`（默认 2）；README 记录安全注意（prompt 会发往 OpenRouter 及各免费推理商，勿用于敏感数据）。

**不做**：
- 不做多伪模型暴露（v1 只有 `auto`；按具体模型锁定用 ki 原生 provider 配 OpenRouter 即可）；
- 不做 thinking/reasoning 转发；
- 不做主动负载均衡/赢家粘性——保持"静态排序定偏好、冷却表被动轮换"的简单模型；
- 不改 `internal/` 任何代码；若实现中发现契约缺口（如 error 事件无法表达"可重试"语义），先记 `docs/postmortem/` 再评估扩展宿主契约，不绕路 hack。
