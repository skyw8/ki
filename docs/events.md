# 事件目录

本文汇总 ki 当前使用的事件名。HTTP SSE 的事件名同时出现在 `event:`
字段和 JSON 的 `type` 字段中。`lifecycle.event`、`provider.stream.event`
等 RPC 方法名只是传输方式，不是事件名。

## 事件通道

| 通道 | 方向 | 事件名来源 |
|---|---|---|
| Session SSE / `loop.Event` | server → CLI/WebUI | 下表的 `type`，也承载 session sideband |
| Extension lifecycle | host → extension sidecar | `lifecycle.invoke`（同步）或 `lifecycle.event`（异步） |
| Provider stream | provider sidecar → host | `provider.stream.event.type` |
| Provider auth | provider sidecar → host | `provider.auth.event.type` |
| WebUI push / `GET /v1/events` | server → WebUI | 每个 tab 一条：`invalidate`（`scope` = `sessions` / `workspaces` / `providers` / `extensions`，只表示"去重取"，数据仍走原 REST）与 `ready`；以及带 `sessionId` 的 session sideband：`extension_ui_updated`、`runtime_ready`、`run_aborted`、`queue_changed`、`compaction_start`/`compaction_end`、run 结束的 `agent_end`、以及 run 之外压缩（手动 `/compact`、threshold 自动压缩）后立即重算的 `context_usage`。这些不进 occupy 回放（`ready` 后客户端自行全量重取，重连靠它追平）；WebUI 用它们刷新侧栏、workspaces、扩展目录，并给后台完成的 session 发系统通知 |

## Session SSE 事件

以下是 Host 侧的 session 事件名。一次运行通常按时序图中的顺序发生；
sideband 事件可以并发到达。

| 分组 | 事件名 | 含义 |
|---|---|---|
| Run 和 turn | `agent_start`、`agent_end`、`turn_start`、`turn_end` | Run/turn 边界；`agent_end` 结束普通运行 SSE 回放。 |
| 请求 | `request_header`、`context_usage` | 面向模型的 system/tools 快照和上下文压力。 |
| 消息 | `message_start`、`message_update`、`message_end` | 用户、assistant、tool result 消息；assistant 增量通过 update 流式发送。 |
| 工具执行 | `tool_execution_start`、`tool_execution_update`、`tool_execution_end` | 工具开始、进度和结束。 |
| 压缩 | `compaction_start`、`compaction_end` | preflight、overflow recovery、threshold，以及手动 `/compact` 压缩。 |
| 队列和控制 | `queue_changed`、`steer_accepted`、`run_aborted` | 队列变化、实时 Inbox 接收和中止；`steer_accepted` 不是 JSONL leaf（run 在 drain 前被 abort 时，待处理 steer 会作为未回复的 user turn 落盘）。parent 的 run 还活着时，子代理完成通知也走这条 Inbox 路径（于是它以 `steer_accepted` 先到 push、随后由 drain 产生 `message_*`），run 已结束才落到 `queue_changed` + durable queue。 |
| 扩展 UI/状态 | `extension_error`、`extension_notice`、`extension_ui_prompt` | 扩展失败、toast，或 WebUI 确认/选择弹层。 |
| Runtime | `runtime_ready` | session 打开时的扩展视图准备结束；成功或失败都会解锁 session。 |
| 仅扩展生命周期 | `agent_settled` | `agent_end` 后 Host 收尾完成；发送给 lifecycle subscriber，不进入普通运行 SSE。 |
| WebUI push | `extension_ui_updated` | 扩展 status/panel/prompt 投影变化；客户端重新读取 session。它不是 `loop.EventType` 常量，只在 `GET /v1/events` 上带 `sessionId`下发。 |

`GET /v1/sessions/{id}/events` 是某个 run 的有序回放（`agent_end` 结束）。
`GET /v1/events` 是每个 tab 的 push 流，只带「失效」和「终态/边带」信息，不带
run 内的增量：`invalidate` 帧让客户端重取（sidebar 用 ETag 304 收尾），
sideband 帧带 `sessionId` 让客户端只处理相关 session。`agent_end` 在 push 流上
**不带 `messages`**——整份 run 消息只回放给持有该 run SSE 的客户端，push 只广播
「这个 session 结束了」。因此 push 可以丢帧而不影响正确性：状态永远由 REST
重取得出，`ready`/重连后的一次全量刷新即可追平。

`message_end`、`request_header`、`context_usage`、压缩事件、工具进度
和部分 sideband 会按各自的 server 路径持久化。并非每个 SSE
事件都会推进 conversation leaf。

`tool_execution_start` 带 `timestamp`（Unix 毫秒）作为调用开始时间；
`tool_execution_end` 带完成时间 `timestamp` 和 `durationMs`。耗时覆盖
工具执行及 `AfterTool`；校验、拦截和未知工具也会产生有计时的成对事件
和 toolResult。toolResult message 会携带同一组 `timestamp` / `durationMs`
并落入 jsonl；start/end 本身仍是实时事件，不单独生成 conversation entry。
`durationMs` 不做 `omitempty`：亚毫秒的调用（例如读小文件的 `Read`）真实
测得 0，字段必须保留，否则 WebUI 会把「0ms」误当成「无计时」而不显示。

## Extension lifecycle 事件

只有下列事件名可以被扩展订阅。`sync` 可以影响当前操作；`async` 是持久化
和 SSE 之后发送的通知。

| 模式 | 事件名 |
|---|---|
| Sync 和 async | `before_agent_start`、`context`、`before_provider_request`、`before_provider_headers`、`provider_error`、`tool_call`、`tool_result`、`input`、`message_end`、`session_before_compact` |
| 仅 async | `after_provider_response`、`agent_start`、`agent_end`、`agent_settled`、`turn_start`、`turn_end`、`message_start`、`message_update`、`request_header`、`tool_execution_start`、`tool_execution_end`、`compaction_start`、`compaction_end`、`queue_changed`、`steer_accepted`、`run_aborted` |

`provider_error` 的 sync 订阅只有扩展在 `initialize` 中声明 `fallback` 时才
能介入错误处理。

assistant 的 `message_end` 可能带 `stopReason=error`、`errorMessage` 和
`isError=true`；扩展应丢弃此前收到的 partial 文本并展示错误信息，不应把
partial 当作成功回复。

同一 run 的 async lifecycle 通知按 loop 产生顺序写入 sidecar：各次
`message_start/update/end` 不会被该 run 的 `agent_settled` 越过。async
表示 Host 不等待扩展完成外部 I/O，不表示事件可以乱序。

`tool_execution_update`、`context_usage`、
`extension_error`、`extension_notice`、
`extension_ui_prompt`、`runtime_ready` 和 `extension_ui_updated` 不是
lifecycle 订阅点。

## Provider sidecar 事件

### `provider.stream.event.type`

`start`；`text_start`、`text_delta`、`text_end`；`thinking_start`、
`thinking_delta`、`thinking_end`；`toolcall_start`、`toolcall_delta`、
`toolcall_end`；`custom_tool_call_input_delta`；`done`；`error`。

`done` 可以携带最终 message。Host 会把 stream event 转换成 loop 的
`message_update` 事件。

Anthropic、Responses、Completions 的原始 provider SSE 事件只在
`pkg/llmprotocol` 内部解析，不属于 ki 对外的事件契约；协议细节见
[`provider.md`](provider.md)。

### `provider.auth.event.type`

`auth_url`、`device_code`、`completed`、`error`。

credential 只在 sidecar 和 Host auth broker 之间传递，不会出现在 WebUI
或 session SSE 事件中。

## 时序图

`alt` 和 `par` 表示可选或并行分支；带 `*` 的事件可以重复发生。

```plantuml
@startuml
title Ki 事件流
hide footbox
autonumber

actor 用户 as User
participant "CLI / WebUI" as UI
participant Server
participant Extension
participant Loop
participant Tool
participant Provider
database "events.jsonl" as JSONL
participant "SSE (run 回放)" as SSE
participant "push GET /v1/events" as Push

== 打开 session ==
UI -> Server: 打开 session
Server -> Extension: session.open
Server -> Push: runtime_ready（带 sessionId）
Extension -> Server: ui.setStatus / ui.setPanel / ui.clearPanel
Server -> Push: extension_ui_updated（带 sessionId）

== 发送 prompt ==
User -> UI: 发送 prompt
UI -> Server: POST /prompt
Server -> Extension: lifecycle.invoke input
Extension --> Server: 改写、吞掉或接受

alt 接受
  Server -> Loop: RunMessage
  Loop -> SSE: agent_start
  Loop -> Extension: lifecycle.event agent_start
  Loop -> SSE: turn_start
  Loop -> Extension: lifecycle.event turn_start
  Loop -> SSE: message_start / message_end（user）
  Loop -> Extension: lifecycle.event message_start
  Loop -> Extension: lifecycle.invoke before_agent_start
  Extension --> Loop: 更新 system / messages

  loop 每个 model request
    Loop -> Extension: lifecycle.invoke context
    Extension --> Loop: 更新 messages
    Loop -> SSE: request_header
    Loop -> Extension: lifecycle.event request_header
    Loop -> SSE: context_usage
    Loop -> Extension: lifecycle.invoke before_provider_request
    Extension --> Loop: 更新 request 或 shortCircuit
    Loop -> Extension: lifecycle.invoke before_provider_headers
    Extension --> Loop: 更新 URL / headers
    Loop -> Provider: provider request / stream

    opt provider extension sidecar
      Provider --> Loop: provider.stream.event
      note right
        start
        text_start / text_delta / text_end
        thinking_start / thinking_delta / thinking_end
        toolcall_start / toolcall_delta / toolcall_end
        custom_tool_call_input_delta
        done / error
      end note
    end

    alt provider response
      Provider --> Loop: assistant deltas
      Loop -> Extension: lifecycle.event after_provider_response
      Loop -> SSE: message_start
      Loop -> SSE: message_update*
      Loop -> Extension: lifecycle.invoke message_end
      Extension --> Loop: 可选的最终 message 替换
      Loop -> JSONL: message_end
      Loop -> SSE: message_end（assistant）
      Loop -> Extension: lifecycle.event message_end
    else provider error
      Provider --> Loop: error
      Loop -> Extension: lifecycle.invoke provider_error
      Extension --> Loop: fallback 或继续
    end

    opt tool calls
      Loop -> Extension: lifecycle.invoke tool_call
      Extension --> Loop: 更新 args / block / terminate
      Loop -> SSE: tool_execution_start
      Loop -> Tool: execute
      Tool --> Loop: result / progress
      Loop -> SSE: tool_execution_update*
      Loop -> SSE: tool_execution_end
      Loop -> Extension: lifecycle.invoke tool_result
      Extension --> Loop: 更新 result / terminate
      Loop -> SSE: message_start / message_end（toolResult）
    end

    Loop -> SSE: turn_end
    Loop -> Extension: lifecycle.event turn_end
  end

  opt overflow recovery
    Loop -> SSE: compaction_start
    Loop -> Extension: lifecycle.invoke session_before_compact
    Extension --> Loop: cancel 或 summary customization
    Loop -> JSONL: compaction_start / compaction_end
    Loop -> SSE: compaction_end
    Loop -> Extension: lifecycle.event compaction_start / compaction_end
  end

  Loop -> SSE: agent_end
  Loop -> Push: agent_end（不带 messages）/ invalidate(sessions)
  Loop -> Extension: lifecycle.event agent_end
  opt agent_end 后的 threshold compaction
    Server -> SSE: compaction_start
    Server -> Extension: lifecycle.invoke session_before_compact
    Server -> JSONL: compaction_start / compaction_end
    Server -> SSE: compaction_end
    Server -> Extension: lifecycle.event compaction_start / compaction_end
  end
  Server -> Extension: lifecycle.event agent_settled
else 被吞掉或拒绝
  Server --> UI: handled / error
end

== 手动 /compact ==
note over User, Push: 同步请求，没有 run stream；进度走 push 流
User -> Server: prompt "/compact"
Server -> Push: compaction_start (reason=manual)
Server -> JSONL: compaction
Server -> Push: compaction_end (reason=manual 或 empty)
Server --> UI: handled（随后 UI 重新读取 session）

== 并发 sideband ==
par 队列和控制
  User -> Server: queue / steer / abort
  Server -> SSE: queue_changed / steer_accepted / run_aborted
  Server -> Push: queue_changed / run_aborted
  Server -> JSONL: 按情况持久化 sideband
else 扩展 UI
  Extension -> Server: notice / error / confirm / select
  Server -> Push: extension_notice / extension_error / extension_ui_prompt
end

== Provider auth ==
UI -> Server: provider auth start/input/cancel
Server -> Provider: provider.auth.*
Provider --> Server: auth_url / device_code / completed / error
Server --> UI: 脱敏后的 auth 状态

@enduml
```
