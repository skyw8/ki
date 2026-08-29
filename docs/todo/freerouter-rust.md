# freerouter Rust 重写方案

> **状态：已实现**（2026-08-29）。Rust 二进制 `extensions/freerouter/bin/freerouter`；独立 `freerouter` / 扩展 `freerouter sidecar` 均常开 HTTP（默认 `127.0.0.1:18427`）。实测：standalone 流式/非流式；sidecar RPC + 同进程 HTTP；`ki serve` reload 后 `free-router/auto` 会话返回 `hello`，随后 HTTP 代理复用宿主 credential 返回 `ok`。
>
> 前置：现有 TypeScript 实现位于 `extensions/freerouter/`（ki provider sidecar，对 OpenRouter 只打 `/chat/completions`）。
> 目标：用 Rust 重写；**无论独立启动还是作为 ki 扩展 sidecar，进程都提供 HTTP 转发服务**。两种启动方式的差异主要在配置 / API key 来源，以及扩展模式额外多一条 NDJSON sidecar 通道给 ki。
> **v1 协议范围：只做 Chat Completions**（OpenAI 兼容 `/v1/chat/completions`）。Responses / Anthropic 明确不做。

## 一、动机与结论

### 现状

- `extensions/freerouter` 是 Node/TS sidecar：`provider.stream.start` → 竞速 OpenRouter `:free` 模型 → `provider.stream.event`。
- 对上游只 `POST {baseUrl}/chat/completions`；伪模型 `auto`；冷却与模型列表缓存在 sidecar 进程内。
- 只能被 ki 拉起；其它客户端无法把 freerouter 当 OpenAI-compatible 网关用。

### 要做成什么

| 能力 | v1 |
|---|---|
| 语言 | Rust 单一二进制 |
| HTTP 转发 | **始终开启**（独立启动与扩展 sidecar 都监听） |
| ki 扩展额外通道 | NDJSON JSON-RPC（`provider.stream.*`），与 HTTP 共用同一 core |
| 上游协议 | 仅 OpenRouter Chat Completions（含 SSE） |
| 对外 HTTP 协议 | 仅 Chat Completions |
| 配置差异 | 扩展：插件 config / 宿主 credential；独立：环境变量与 CLI |

**结论**：竞速、发现、冷却、SSE 解析是进程级单例 core；上面**永远**挂 HTTP frontend。扩展启动时再并行挂 sidecar frontend。两条入口打同一冷却表与模型池。**不改 ki 宿主契约**。

## 二、运行形态（不是互斥的 mode）

```
                         ┌──────────────────────────────┐
                         │       freerouter core        │
                         │ discovery / router / race    │
                         │ openrouter chat + SSE        │
                         └──────────────┬───────────────┘
                                        │
              ┌─────────────────────────┴─────────────────────────┐
              ▼                                                   ▼
     HTTP frontend（始终）                              Sidecar frontend
     POST /v1/chat/completions                          （仅扩展启动时）
     GET  /v1/models                                    NDJSON ↔ ki host
     GET  /healthz                                      provider.stream.*
```

| 启动方式 | HTTP | NDJSON sidecar | 典型配置来源 |
|---|---|---|---|
| 独立：`freerouter` / `freerouter serve` | ✅ | ❌ | 环境变量、CLI flags、可选本地配置文件 |
| 扩展：ki 拉起 `bin/freerouter sidecar` | ✅ | ✅ | 扩展 `config.json`、宿主 `credential`；环境变量作回落 |

两种启动**不是**「要么 RPC 要么 HTTP」，而是「HTTP 必有；扩展再多一条给 ki 的 RPC」。

### 2.1 HTTP 表面（两种启动共用）

```bash
# 独立（默认监听见下；可 --listen 覆盖）
export OPENROUTER_API_KEY=sk-or-...
freerouter

# 扩展被 ki 拉起后，其它程序同样可打 sidecar 进程开出的 HTTP：
#   OPENAI_BASE_URL=http://127.0.0.1:18427/v1
```

| 方法 | 路径 | 作用 |
|---|---|---|
| `POST` | `/v1/chat/completions` | 竞速免费模型；流式 / 非流式 OpenAI 兼容响应 |
| `GET` | `/v1/models` | 伪模型 `auto`；可选附带当前发现到的 `:free` 列表 |
| `GET` | `/healthz` | 存活探针 |

行为要点：

- 请求体按 OpenAI Chat Completions 解析；`model` 缺省或为 `auto` / `free-router/auto` 时竞速；若为池中某个 `:free` id 可直连（不竞速），否则 400。
- `stream: true` → OpenAI 兼容 SSE；`stream: false` → 聚合 JSON。
- 响应头 `X-Freerouter-Model: <winner-id>`；chunk / 响应里的 `model` 写赢家 id。
- 客户端断开 → abort 全部在途候选。
- **不做** `/v1/responses`、`/v1/messages`、OAuth、WebUI。

监听地址：

- **默认固定 `127.0.0.1:18427`**（独立启动与扩展 sidecar 相同）。选在冷门高位端口，避开常见本地开发占用（`3000` / `5173` / `8080` / `8888` 等）；可被 `--listen` / `FREEROUTER_LISTEN` / 扩展 config `listen` 覆盖。
- 若默认端口已被占用：启动失败并提示改 `listen`（v1 不做自动换端口，避免「地址飘忽」不好抄给其它程序）。
- 绑定成功后把实际地址打到 stderr；扩展模式可用 `ui.setGlobalStatus`（或等价）把 `http://127.0.0.1:18427` 投影到 WebUI。
- 只绑回环为默认；若配置成非 loopback，README 标明风险（无 TLS、弱鉴权）。

### 2.2 Sidecar 通道（仅扩展启动）

- stdin/stdout NDJSON JSON-RPC 2.0，契约见 `docs/extension.md`。
- `initialize`、`provider.stream.start` / `cancel`、`config.updated`、`shutdown`、`cancel`。
- `provider.stream.start` 立刻 `{accepted:true}`，竞速异步，经 `provider.stream.event` 回传。
- 输入为 ki `loop.Request` IR → core 转成 OpenRouter chat body（对齐现 `openrouter.ts`）。
- routing 进度仍放 thinking 块（contentIndex 0），赢家正文 +1；`done.message` 与流一致。
- 与 HTTP 请求**共享** discovery 缓存与冷却表：ki 会话打爆某模型后，外部 HTTP 客户端也会跳过它，反之亦然。

进程模型：tokio 上并行跑「stdin RPC loop」+「HTTP server」；`shutdown` / stdin EOF 时两者一起停。

### 2.3 配置与 API key（按启动方式分源，解析成同一 Config）

独立启动（无 `KI_EXTENSION_ROOT` / 显式非 sidecar）：

1. CLI flags（若有）
2. 环境变量：`OPENROUTER_API_KEY` 或别名 `FREEROUTER_API_KEY`，以及 `OPENROUTER_BASE_URL`、`FREEROUTER_LISTEN`、`FREEROUTER_RACE_WIDTH` 等
3. 可选：用户传入的配置文件路径（v1 可不做文件，仅 env+CLI 也可）

**无 OpenRouter key → 拒绝启动**（fail-fast）。

扩展 sidecar（ki 拉起，存在 `KI_EXTENSION_ROOT`）：

1. 每次 stream：宿主 `credential.apiKey`（仅作用于该次 ki `provider.stream`；HTTP 入口不自动用 per-request host credential）
2. 扩展私有 `{KI_EXTENSION_ROOT}/config.json`（含 `apiKey`、`listen`、竞速参数；`config.updated` 后热更新）
3. 环境变量回落（`OPENROUTER_API_KEY` / `FREEROUTER_API_KEY` 等）

扩展允许无 key 启动（宿主可能稍后写入 credential / PATCH config）；HTTP 与 sidecar 在实际发起上游请求时若仍无 key，分别返回 HTTP `401` / `provider.stream.event` `error`。

`config.updated` 后：重读 config、刷新 listen **不**强行 reboot 端口（v1：listen 变更需重启 sidecar；竞速 TTL / raceWidth / apiKey 热更新即可）。若实现成本低，也可支持 listen 热重绑，但非必须。

## 三、包布局

```
extensions/freerouter/
├── extension.json
├── config.json
├── locales/
├── README.md
├── Cargo.toml
├── src/
│   ├── main.rs          # 解析启动方式；始终起 HTTP；sidecar 再起 RPC
│   ├── config.rs
│   ├── discovery.rs
│   ├── router.rs
│   ├── race.rs
│   ├── openrouter.rs
│   ├── ir.rs            # ki IR → chat messages
│   ├── sidecar.rs
│   └── http.rs
└── tests/
```

`extension.json`：

```json
{
  "runtime": {
    "kind": "rpc",
    "command": "bin/freerouter",
    "args": ["sidecar"]
  }
}
```

`config.schema` 在现有字段上增加 `listen`；其余（`raceWidth`、`maxBatches`、TTL、timeout、`apiKey`、`baseUrl`、`refreshIntervalMs`）保持兼容，减少 WebUI 表单改动。

## 四、Core 行为（从 TS 迁过来，语义不变）

### 4.1 Discovery

- `GET {baseUrl}/models`，`id` 含 `:free`；剔除 moderation/guard/vision 类。
- 排序：`groq/` → `cerebras/` → `fireworks/` → `together/` → `mistralai/`，同档 context 升序。
- 懒加载 + `refreshIntervalMs` 后台刷新。

### 4.2 Router

- exhausted / slow TTL；402 fatal。
- 冷却表进程级，HTTP 与 sidecar 共享。

### 4.3 Race

- `raceWidth` × `maxBatches`；首个 qualifying 事件获胜；thinking/reasoning 不算赢。
- 批级 first-token 超时 + 赢家 idle 超时；`raceWidth=1` 串行；`triedThisRequest` 防死循环。

### 4.4 内部事件 → 前端

| 前端 | 映射 |
|---|---|
| Sidecar | `provider.stream.event` |
| HTTP stream | OpenAI chat.completion.chunk SSE |
| HTTP non-stream | chat.completion JSON |

## 五、错误与可观测

- 全池耗尽：sidecar `error`；HTTP `503` + OpenAI 风格 error body。
- 无 key：独立启动直接退出；扩展下按请求失败。
- stderr 打实际 listen 地址与赢家（可选 verbose）；禁止打印 key。

## 六、测试与验收

| 层 | 内容 |
|---|---|
| `cargo test` | discovery / router / race / IR / SSE |
| Sidecar + HTTP 同进程 | 拉起 `sidecar`：stdin 走 RPC 的同时，reqwest 打本进程 HTTP，两者共享冷却 |
| 独立 HTTP | 无 RPC，仅 env key + `/v1/chat/completions` |
| ki e2e / WebUI | 扩展切换后 provider 路径与 freerouter 表单仍可用 |
| 手测 | ki 启用扩展后，另开终端用 curl 打扩展进程的 listen 端口 |

## 七、迁移步骤

1. Cargo + core，单测对齐现有 Node 行为。
2. HTTP server 先独立可跑。
3. 加上 sidecar RPC，确认「RPC + HTTP 同进程」。
4. 切 `extension.json`，删 Node 产物；更新 README（写明扩展也会开 HTTP、如何看 listen 地址）。
5. WebUI：若需要展示 listen，接 global status；config 增加 `listen` 字段。

## 八、明确不做（v1）

- Responses / Anthropic。
- 编进 `cmd/ki`。
- 多伪模型产品化、赢家粘性、主动负载均衡。
- TLS / 多租户；默认信任本机回环。
- 为独立启动单独做 WebUI。

## 九、后续

- 入站 Responses / Anthropic 转换层。
- 多平台预编译 artifact。
