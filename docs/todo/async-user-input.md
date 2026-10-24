# 异步提问与旁聊：WebUI 设计方案

状态：**待评审，尚未实现**。本文确定工具契约、UI 草图、旁聊边界与持久化方式；不包含服务重启或功能代码改动。

## 1. 设计目标

新增 `request_user_input_async`，模型提问后立即继续工作。用户在消息区左上角的问题栏查看全部问题，点击某一项打开独立窗口：

- 左边直接回答问题，右边通过 **旁聊（Side conversation）** 讨论、比较方案、澄清概念。
- 旁聊了解提问时的主对话背景，但有自己的消息、运行和停止按钮。
- 只有用户点击 **“将答案发送给主对话”**，答案才进入主模型上下文。
- 收到问题不抢焦点、不弹窗、不暂停主任务；关闭窗口不提交答案、不取消问题。

这里的“独立窗口”采用页面内 modal dialog，不使用浏览器新窗口。端口转发、手机和触屏都可用，也不会被浏览器拦截。

**核心区分：工具接受问题 ≠ 用户回答问题 ≠ 模型已经读到答案。** UI 必须分别表示。

## 2. UI 草图

下面是拟议视觉稿，不是现有页面截图。沿用 Ki 的浅色背景、蓝色强调、细边框与圆角；实际实现使用现有 `theme.css` token，自动支持深色模式。

### 2.1 主页面：消息左侧的问题栏

![主页面的问题栏](assets/async-questions-overview.svg)

问题栏属于 **当前会话的消息内容区**，位于全局会话侧栏右边、正文左边，不塞进 workspace/session 树。这里将“message 左上边空白”解释为整个聊天阅读区域的左侧留白，而不是每条气泡左侧重复放一份问题。

布局约束：

| 区域 | 建议设计 |
|---|---|
| 全局会话侧栏 | 保持现有宽度与交互 |
| 问题栏 | 左上独立浮层；超宽容器展开为 176px 卡片，普通宽度使用紧凑入口，不改变主正文几何 |
| 主正文 | 复用现有阅读宽度；有问题栏时整体阅读区域居中，不覆盖正文 |
| 右侧请求导航 | 保留现有 `RequestNav`，仍只导航人类请求，与问题栏功能分开 |
| 主输入框 | 保留原位置与草稿；问题到达不触发滚动、不改变输入内容 |

问题卡从上到下按创建顺序排列；一次调用的多个问题逐项显示，相邻分组用轻分隔和“本次 1 / 3”提示。不按最后活动时间重排，避免用户点选时列表跳动。

- 顶部显示“待你确认 · 3”和“主任务仍在运行 / 主任务已结束”。
- 每卡显示两行题目预览、序号和状态；“讨论中”只指该问题的旁聊在运行，不代表主任务等待它。
- 待回答是默认视图；“已回答 / 已收起”折叠入口保留全部历史问题。**主任务结束不移除待回答问题**。
- 新问题只增加计数和轻量提示；不自动打开窗口、不让原有卡片移位。
- 超出高度的问题栏自己滚动；键盘聚焦能读到完整标题，完整问题在弹窗展示。
- 没有问题时不显示空栏，不永久占用正文空间。

### 2.2 点击问题：回答 + 旁聊的双区窗口

![回答与旁聊窗口](assets/async-questions-dialog.svg)

建议窗口宽 `min(1120px, viewport - 48px)`，高 `min(820px, 88dvh)`。左栏约 360px、右栏自适应，栏间仅一条浅色分隔线。避免套多层卡片和过多工具栏。

**左栏：回答**

1. 显示完整问题，可前后切换当前调用中的问题；切换保留各自草稿。
2. 有选项时用整行 radio 卡片；第一项预选，与 Codex 一致，但明确“仅预选，尚未发送”。
3. 始终有自定义回答入口。选项与补充说明合成一个可预览的最终答案；纯自由问题直接展示输入框。
4. 固定底部显示最终提交按钮“将答案发送给主对话”。不能用“发送”这种含混短词。
5. 次要操作“稍后再答”只关闭窗口；菜单中的“收起这个问题”不向模型发送空答案。

**右栏：旁聊**

- 标题“旁聊”，副标题“独立讨论，不会自动发送到主对话”；显示所用模型与“基于提问时的上下文”。
- 提供可点击建议，例如“解释这些选项”“帮我比较取舍”“我还需要提供什么信息”。点击才发送旁聊消息。
- 展示旁聊自己的消息流与输入框，按钮叫“发送到旁聊”；运行中有“停止旁聊”，不复用主会话的停止动作。
- 旁聊输入区复用普通对话的输入卡片与发送 / 中断样式，只保留发送或中断一个动作按钮。建议答案可手动复制到左侧回答，旁聊不会覆盖手写草稿。
- 不把全部旁聊 transcript 自动贴回主对话。模型给出的建议和整理稿都只是建议，不代表用户确认。

用户始终可直接答题，不必启动旁聊。第一次发送旁聊消息时才创建旁聊 session，打开窗口本身不调用模型。

### 2.3 窄屏 / 手机

![手机端的问题清单与全屏旁聊](assets/async-questions-mobile.svg)

以 **可用消息区宽度** 决定布局，不仅看屏幕宽度：

| 条件 | 布局 |
|---|---|
| 可用宽度 ≥ 1280px | 左上浮动问题清单，可收起；不为它增加正文 padding 或改变正文居中 |
| 可用宽度不足 1280px | 消息区左上显示紧凑入口，点开浮动竖向清单；不挤压正文 |
| 窗口可用宽度 ≥ 900px | 回答、旁聊并排 |
| 窗口可用宽度 < 900px | 同一窗口内使用“回答 / 旁聊”两个 tab，各自保留滚动和草稿 |
| 手机（沿用现有 compact 布局） | 全屏 dialog；固定标题、tab、当前问题摘要；当前区内容独立滚动 |

旁聊回复不自动切 tab 或抢用户阅读位置。手机键盘弹起时输入框保持可见，使用 `100dvh`、`visualViewport` 现有机制及 safe-area，禁止页面级双重滚动。

### 2.4 细节与无障碍

- 8px 间距基线；卡片圆角 12px、窗口圆角 16px；正文保持 14–16px，状态使用图标 + 文字，不仅靠颜色。
- 所有触控目标 ≥ 40px，导航、tab、关闭和主操作 ≥ 44px；长问题、CJK、URL 不横向撑破布局。
- dialog 使用 focus trap、`aria-modal`、可访问标题。关闭恢复原问题卡焦点；若卡片已归档，恢复到问题栏标题。
- `Escape` 只关闭窗口，不中止任何 run；草稿、旁聊、正在生成的回复继续保留。需停止时显式点“停止旁聊”。
- 两个输入区各有独立标签与快捷键作用域。只有聚焦答案输入框时 `Ctrl/Cmd+Enter` 提交答案；聚焦旁聊时仅发送旁聊。
- 新问题用 polite live region 告知数量；主 transcript 不为此切换 following / reading 状态。
- 发送请求中显示“正在提交”，服务器确认后才能归档；失败保留草稿，可重试。
- 隐藏弹窗不复制主 composer，必须保留现有主输入、附件和排队消息。不能因通用 `.app.modal-open .composer` 规则误隐藏旁聊 composer。

## 3. 与 Codex 一致的工具契约

### 3.1 名称与 description

名称：`request_user_input_async`。description 尽量逐字采用当前 Codex 的 bundled 文案：

> Ask the user one or more questions during ongoing work. Use this tool only to request missing information, preferences, constraints, clarification, or approval. The tool returns immediately without ending the turn or waiting for a reply; any reply arrives asynchronously as a new user message. Keep questions concise, self-contained, and easy to understand, using a level of detail appropriate to the user and task. The UI always allows a free-text answer, including when suggested options are provided. A preselected option is not submitted automatically.

### 3.2 Schema

```json
{
  "type": "object",
  "properties": {
    "questions": {
      "type": "array",
      "minItems": 1,
      "description": "One or more self-contained questions to present together, in display order.",
      "items": {
        "type": "object",
        "properties": {
          "title": {
            "type": "string",
            "description": "The complete question shown to the user, including any context needed to answer it."
          },
          "options": {
            "type": "array",
            "minItems": 1,
            "description": "Suggested answers, in display order. Put the recommended answer first; the first option is preselected by default. The user can select one option or enter a free-text answer. Do not include an Other option or a free-text placeholder; the UI provides free-text input automatically. Omit options for a free-text-only question.",
            "items": { "type": "string" }
          }
        },
        "required": ["title"],
        "additionalProperties": false
      }
    }
  },
  "required": ["questions"],
  "additionalProperties": false
}
```

成功输出正文精确为 `{"accepted":true}`。不添加 `isBlocking`、`autoResolutionMs`、UI 布局或 side session 参数，也不让模型生成 question ID。structured details / loop event 携带内部身份，不改变模型可见输出。

与 Codex 一样，拒绝空问题数组、空白标题、空选项数组、空白选项；额外设置总参数字节预算防止超大负载，整批验证通过后才提交，不能创建一半问题后返回失败。预算与错误测试在实现时统一定义，不在 UI 静默截掉问题或选项。

### 3.3 提示词与注册

主提示词增加下面这段，保持 Codex 的使用语义：

> When available, use `request_user_input_async` to ask for missing information, preferences, constraints, or clarification. Ask early and continue useful work that does not depend on the answer. Replies arrive as new user messages. For optional clarification, give the user a reasonable opportunity to reply before proceeding with a stated assumption. If an answer or approval is required, do not proceed with dependent work until it arrives. Elapsed time and preselected options are not answers or approval.

Ki 的拟议差异：

- 对支持工具调用的主会话统一提供，不复制 Codex 的模型名 / catalog 白名单，也不限定 Plan 模式；是否使用由模型选择。
- Code Mode 下也作为直接控制工具暴露，避免为等用户而占用 JS worker；不需要额外的 `wait_question` 工具。
- 工具始终异步；“需要答案才能继续”是模型的任务依赖，不通过让工具 future 等待用户实现。
- root 主会话可用；subagent 和旁聊不提供，避免问题归属不清和递归旁聊。
- 无 WebUI 在线时仍可创建、持久化，并在 CLI / 非交互消费者中给出可读问题；已有 session API 能读取，后续打开 WebUI 可接着回答。
- 不引入旧的阻塞 `request_user_input`。此工具也不替代执行权限、工具审核和危险操作的服务端权限检查。

## 4. 旁聊上下文与生命周期

### 4.1 借鉴什么，不照搬什么

Codex `/side`（别名 `/btw`）使用独立 ephemeral fork：继承主线程 history，界面只显示 side boundary 后的消息；返回主线程不会自动把旁聊内容合并进主模型。Ki 保留这一隔离思路。

Ki 的问题可能需要稍后回答、刷新继续讨论，因此建议 **旁聊 session 持久化**，不像 Codex 在退出时丢弃 ephemeral thread。不要直接使用 `spawn_agent`：其 agent 身份、父任务调度和自动完成回灌不是用户旁聊的语义。

### 4.2 安全的上下文快照

旁聊固定使用 **提问时** 的上下文，不追随主会话不断变化的 active leaf。记录主会话 ID、问题 ID、提问源 entry 和安全快照 entry，弹窗明确显示快照时间。

这里区分三个时间 / 位置，避免把“惰性创建”误解为“打开时取最新历史”：

| 概念 | 推荐规则 |
|---|---|
| 逻辑锚点与快照确定时间 | 服务端接受 `request_user_input_async`、持久化问题时，固定来源分支、问题内容、模型配置及安全快照引用 |
| 实际 fork 节点 | 提问调用之前最近的完整、已持久化的模型协议前缀，不直接截在尚未配对的提问 tool call 上 |
| 旁聊 session 创建时间 | 用户第一次发送旁聊消息时才创建，但使用已经固定的快照，不重新选择父会话最新节点 |

例如，完整历史 A 之后，assistant 同批调用 `request_user_input_async` 和一个尚未结束的长工具；主任务之后继续产生 B、C。旁聊以 A 为安全前缀，追加当前问题、选项、A 尚未包含的当前用户请求、旁聊指令和用户旁问。即使用户等到 C 之后才打开窗口或首次发送，仍不自动加入 B、C，也不因原工具批次后来完成就前移快照。

同一次调用的多道问题共享这一来源快照，各题旁聊分别追加对应问题。安全快照引用和补充材料应随问题一起持久化，确保首次旁聊创建前经历重启或主会话压缩，也能恢复同一个逻辑快照，而不是退回使用当时最新历史。

不能直接 fork 最新落盘节点：现有 busy fork 接受显式 `entryId`，但 `ForkAt` 不保证截断处的 assistant tool calls / tool results 已配对。提问又可能发生在并行工具批次中。

建议新增共享的 `SafeContextBoundary` 选择逻辑：

1. 在提问来源之前的已持久化主分支中，选择最近的**完整模型协议前缀**；其中每个 tool call 必须有对应 result，包括并行批次中的全部兄弟调用。
2. 若当前工具批次未完成，回退到该批次之前；不得等待其它长工具结束才接受问题。
3. side 继承该安全前缀的有效历史与压缩上下文。再以独立上下文片段补入当前问题的完整标题、选项，以及前缀未包含的当前用户请求；避免重复补入已继承的请求。
4. 不把未配对的 assistant tool-call 消息或孤立 toolResult 当作 side 的真实协议消息复制。若未来需补充部分已完成工具的发现，必须转换成带来源的只读材料，而不是伪造 toolResult。
5. 使用服务端构建的实际模型 history 校验完整性；不能仅检查最后一个节点的 role，也不能直接用 `LastUserBoundary` 丢掉整个当前人工轮而不补问题。

不继承主会话尚未消费的 Inbox、人工 queue、扩展 queue 或 context queue，也不把继承的问题元数据变成 side 自己的待回答题。临时工具输出 spill 文件不随 fork 持久保存；需要的资料以已有持久正文为准，不能让旁聊恢复依赖旧进程的临时文件。

此快照冻结的是**会话上下文**，不是工作区文件系统。主任务继续修改文件时，旁聊读取的是当前文件；UI 不应声称存在文件快照。

### 4.3 旁聊 session

- 每个问题最多关联一个旁聊 session，第一次发送旁聊消息时惰性创建，多轮继续复用。
- 使用独立 session ID、run、SSE、usage、composer 和取消作用域；不得通过切换全局 `currentId` 来复用主聊天窗口。
- 工作目录与历史背景固定在提问时；provider/model/thinking 跟随来源主会话。主会话点击切换模型时同步空闲旁聊；正在生成的旁聊完成当前轮，下一轮使用来源的新模型，不中断当前回复。首次旁聊也使用主会话当前模型。
- 模型可见完整允许的继承上下文；UI 只展示 `sideBoundaryEntryId` 后的旁聊消息，避免弹窗重复长篇主历史。
- 在继承历史之后加入明确的 side 指令边界：之前的任务、计划、审批和工具调用只作背景，不继续执行；只响应边界之后用户在旁聊提出的问题。developer policy 与首条上下文说明共同表达边界，执行层白名单负责硬性限制。
- 标记 `purpose=question_side`、来源 question 和 parent session。不作为普通顶层会话展示；是否在父会话下展开、如何区分子代理，见文末待确认项。这是会话用途，不是 `forkMode=tree` subagent。
- 首期允许解释讨论和工作区内的 `read` / `grep` / `glob`，执行层强制白名单与路径边界；不开放 shell、写文件、extension/MCP 动作、子 agent、再次提问。不能只靠提示词说“请勿修改”。
- 旁聊模型指令说明：帮助用户理解当前问题；给建议而不代用户授权；不继续执行主任务；主对话上下文是背景材料，不能覆盖旁聊的能力限制。
- 关闭窗口或切问题不停止旁聊。显式停止只取消该 side run；主会话完成、失败或被停止也不应串联取消用户旁聊。
- 答案已提交后旁聊仍可查看，但主提交区变为只读回执；删除主会话时按关联生命周期清理旁聊，正常重启后可恢复。

### 4.4 旁聊与回答草稿

旁聊只保留发送 / 中断按钮。用户可通过普通旁聊消息请模型建议答案，再手动写入左侧回答。
旁聊回复不自动替换草稿，也不直接提交给主会话。

## 5. 问题、答案与运行状态

### 5.1 身份与归属

内部身份采用 `questionId = JSON.stringify(["request_user_input_async", toolCallId, questionIndex])`，与 Codex 的逐题身份方式一致。跨会话寻址必须再带 `sessionId`，不可假设 fork 后 toolCallId 全局唯一。

记录至少包括：

| 字段 | 意义 |
|---|---|
| `sessionId`、`sourceEntryId`、`sourceTurnId` | 来源主分支与人工轮 |
| `toolCallId`、`questionIndex`、`questionId` | 本次提问与逐题身份 |
| `title`、`options`、`createdAt` | 原问题，按原顺序保存 |
| `safeContextEntryId` | 旁聊可用的完整上下文边界 |
| `status`、`revision` | 服务端状态与并发修订号 |
| `sideSessionId`、`sideBoundaryEntryId` | 惰性创建的旁聊关联 |
| `answerSubmissionId`、`answerEntryId` | 答案投递及实际进入上下文的身份 |

打开、选中选项、正在编辑是 UI 状态，不冒充问题的已答状态；“旁聊运行中”是关联 session 的运行投影。

### 5.2 状态

| 状态 | UI 与语义 |
|---|---|
| `pending` | 待回答，可编辑、旁聊、稍后处理 |
| `answer_queued` | 答案已持久接受，等待当前 run 的安全投递点；不可重复提交 |
| `answered` | 答案已成为来源后代分支上的 typed user message；全局首次提交回执保留，不等于模型已经采纳 |
| `dismissed` | 用户收起；不产生默认答案，不向模型注入批准 |

提交中的 spinner 是本地状态；失败回到原状态并保留草稿。重复 HTTP 请求用同一个 submission ID 返回同一回执。两个 tab 同时答同一题，服务端串行校验 `revision`，只接受第一份，另一端提示“此问题已在其它窗口回答”。

主任务完成、停止、压缩、SSE 断开都不自动 dismiss；不设置自动提交或自动批准的计时器。用户可恢复收起的题目继续回答。

主分支切换时只展示来源在当前分支上的待办问题，提供“其他分支仍有问题”提示。`sourceBranchValid` 控制新答案提交；`answerBranchValid` 表示已投递答案是否包含在当前分支。切到答案之前的兄弟分支时保留全局 `answered` 回执并提示当前分支没有该答案，不重新排队或自动注入历史回答。更正或向新分支补充信息需要新的明确用户消息。busy 时绝不通过 `parentId` 强行修改主分支。

## 6. 后端与协议方案

### 6.1 创建：持久化后广播，工具不等待人

新增 builtin handler，验证整批参数后，提交结构化问题事实，随后返回 `{"accepted":true}`。不复用扩展 `ui.confirm/select` 的 Host RPC：该路径是有超时的阻塞等待，而且当前内存槽不能唯一寻址每道题。

扩展 `loop.Event` 与 jsonl，让 `question_requested` 携带问题批次、来源节点和上下文锚点。**事件必须先持久化再发 SSE**，handler 成功只能表示可恢复的问题已被接受；不得先让浏览器看到未落盘问题。模型没有等用户回答的 channel、promise 或 held worker。

持久化实现应走会话现有串行写入路径，确保运行中插入问题元数据不会抢走正在写的 active leaf：

- 问题事实可作为有明确 source/branch anchor 的非 message 记录；原 tool call/result 仍按正常协议闭合。
- 生命周期记录不额外生成 provider toolResult，不进入模型上下文；提供明确 branch 投影规则，不使用无法归属的 unparented sideband。
- 并行工具产生的题目立刻可展示，旁聊使用前述安全前缀；不以整个工具批次结束为展示条件。
- 失败发生在提交前时不生成问题；提交后即使 run 取消，也通过确定性 question ID 去重并恢复，不重建另一批问题。

### 6.2 读取与同步

在已有 `GET /v1/sessions/{id}` 中增加 `questions` / 关联 side metadata 投影，与 SSE 新事件使用同一 reducer 规则。不能只扫描当前已加载的工具行推断待回答问题：

- compact 模式、分页未加载、工具行折叠都不能让问题栏漏题。
- SSE 重连按已有快照 / 事件恢复机制对齐，按 question ID + revision 去重。
- 服务端重启可由 jsonl 重建状态；不另设仅内存 question registry。
- 新增读取接口没有必要，不增加 `/questions/list`、独立 polling 或第二套 WebSocket。

### 6.3 回答：复用 prompt 的投递路径

现有 `POST /v1/sessions/{id}/prompt` 接收 **typed question reply**，由服务器构造模型信封：

```json
{
  "questionReply": {
    "questionItemId": "[\"request_user_input_async\",\"call_123\",0]",
    "revision": 1,
    "answer": "先面向内部团队灰度，观察一周再开放给客户。",
    "submissionId": "client-generated-unique-id"
  }
}
```

服务端根据 question ID 读取原问题，验证分支、修订、状态、来源与长度（答案最大 64 KiB），然后构造模型 user message：

```text
<send_user_message_question_reply>
[{"questionItemId":"[\"request_user_input_async\",\"call_123\",0]","question":"发布面向哪些用户？","answer":"先面向内部团队灰度，观察一周再开放给客户。"}]
</send_user_message_question_reply>
```

用 JSON 序列化保证转义，保留 question 的结构化 metadata；UI 渲染为“针对问题的回答”而不是裸 XML/JSON。对齐 Codex 的 envelope 是模型提示格式，不把来自任意用户文本的同名标签当作授权或问题状态更新。

| 主会话状态 | 答案投递 |
|---|---|
| 正在运行 | 显式采用现有 steer 路径，在安全边界进入当前任务；不受普通消息 busy 默认 queue 设置误导 |
| 正在工具调用 / 压缩 | durable pending，等安全注入点；UI 显示 `answer_queued`，不丢答案 |
| 已空闲 | 作为新的 user 输入启动一轮，关联原问题 |
| 提交后当前 run 结束 / 被停止 | 未注入的答案保留在 durable queue，展示“待继续”；不因 run 结束标记已读或删除 |

`question_updated` 先持久保存答案、submission ID 和 `answer_queued`，然后尝试 live Inbox 投递或启动空闲会话。接受事实本身就是恢复账本，不额外依赖 `queue.json` 的第二次事务。下一次明确启动从账本恢复未投递且来源有效的答案；停止不会自动唤醒会话。UI 对排队回执提供“继续主对话”，通过 `question-action` 的 `continue` 显式恢复，不重新提交答案。只有匹配 question ID、submission ID 和来源祖先的 typed user entry 才派生 `answered`。相同提交重试幂等，跨题复用 submission ID 或同题更换答案返回 409。

`POST /v1/sessions/{id}/question-action` 接收 `{questionItemId, revision, action}`。`dismiss` / `restore` 只追加生命周期事实，不启动模型；`continue` 仅恢复已接受且来源有效的排队答案。它是用户命令，不是另起读取状态的 REST 服务。问题已回答后首期不支持直接改写历史答案；更正走新的明确 user 消息。

### 6.4 旁聊：复用 fork、prompt、events

扩展已有 `POST /v1/sessions/{id}/fork`，接收 `purpose=question_side` 和 question ID；**快照 entry 由服务端计算和验证**，不能信任客户端传来的任意 entryId。创建与绑定需串行幂等，两个 tab 首次发旁聊不能创建两个 session。

创建后复用现有 side session 的 `/prompt`、`/events`、停止与历史接口，不新增 `/side-chat/completion`。继承历史渲染过滤以 `sideBoundaryEntryId` 为界，不能通过前端删历史来决定模型上下文。

purpose、问题关联、边界及能力白名单由服务器设置，普通创建接口保留这些 metadata 键。创建时先保存带来源身份的初始化标记，完成后再关联问题；重试会查找同来源与问题的已完成旁聊，清理同身份下未初始化完的会话，避免崩溃后重复创建。Ki-owned JSON 经 `internal/state` 读写；新增字段不 bump version。

## 7. 前端落地边界

建议新建 `web/src/features/questions/`，承载问题投影、问题栏、答题窗口、旁聊 pane 和草稿；纯 reducer / 排序 / 选择器放 `web/src/lib/`，不把逻辑继续堆进 `App.tsx`。

- 在现有 `.chat-stage` 下挂问题栏，与 `.scroll` / `RequestNav` 同级；不能挂在虚拟 `TranscriptRow` 内，否则滚出视口就销毁题目。
- 消息区用局部容器宽度切布局；保留 `ChatView` 虚拟滚动和 following / reading 不变量。
- 主会话与旁聊分别管理 session controller、SSE 和 abort；可以抽取现有会话 hook，但不能共用一个 active run 引用。
- 父快照只同步问题状态和 side ID；side transcript 惰性加载、独立 SSE。多个题目时只订阅打开的 side，其余靠状态失效刷新，不为每张卡常驻连接。
- 旁聊耗时、token、费用单独展示；主对话统计不重复累计复制的历史或 side run。可另显示“关联旁聊费用”，但须与主任务费用分开。
- 用户答案草稿和旁聊输入草稿分别按 `serverId/sessionId/questionId` 保存；参考现有浏览器 draft 策略，刷新可恢复。它们是未提交的用户私有草稿，不伪装成已持久回答。
- 已提交状态以服务端事实为准；模型回复、题目历史以 jsonl 为准。不同 tab 的未提交草稿不互相覆盖。
- 请求导航可识别 typed question reply 为人类输入，但用“回答：发布范围”短标题，避免把整个 envelope 当导航文本。
- 问题出现时不额外全量下载主 transcript；预览、全量 options、side 历史按需加载，保持 gzip 与 same-origin 契约。

## 8. 验收与实施顺序

### 8.1 分阶段

1. **异步核心 + 问题栏 + 直接答题**：工具/schema、持久化/重放、主回复投递、桌面/手机窗口、跨 tab 幂等。
2. **旁聊**：安全 fork 边界、独立运行与历史显示、执行能力隔离、关闭/切换/重启恢复。
3. **整理草稿与细节**：显式整理/替换、状态文案、键盘/焦点、深浅色和高延迟重连打磨。

完整需求包含以上三个阶段；阶段一不能被当成“已实现旁聊”的最终交付。

### 8.2 必测场景

| 层级 | 覆盖 |
|---|---|
| Go 单元 | schema/验证、立即接受且无需回答可进入下一模型步、稳定 ID、source branch、完整协议快照边界 |
| 持久化 / 服务端 | 同批多题、并行工具、取消前后落盘、重启重放、compact、提交幂等、多 tab 竞争、队列恢复与分支切换 |
| 旁聊 | 主任务继续运行；side 可多轮；返回不回灌；停止互不影响；禁止写工具与越界路径；不同问题上下文互不串线 |
| Bun 单元 | 问题 reducer、排序/分组、状态与 revision、草稿选择、envelope 展示与 source 分类 |
| Playwright | 左上问题栏、不抢焦点；radio 预选不提交；同窗口回答/旁聊；关闭保留；整理不静默覆盖；刷新/掉线恢复 |
| Responsive | 390 / 760 / 1024 / 1440 等现有矩阵、软键盘、safe-area、触控、长标题/选项、焦点恢复、无横向溢出 |

验证优先廉价单元测试，浏览器只测真实交互；等待可观察状态，不使用固定 sleep。重点回归已有 `responsive.spec.ts`、请求导航和长历史虚拟滚动。记录受影响套件前后耗时；最终按项目 full regression 要求执行。验证使用隔离实例，不重启现有服务。

## 9. 参考与后续文档更新

现有 Ki 契约：[WebUI](webui.md)、[事件](events.md)、[会话](session.md)、[工具](tools.md)、[提示词](system_prompt.md)、[状态文件](state.md)、[架构](architecture.md)、[扩展](extension.md)。

当前代码接入点：

- `web/src/App.tsx` 的 `.chat-stage`、`web/src/features/chat/Chat.tsx`、`RequestNav.tsx`、`web/src/app.css`。
- `internal/loop/loop.go` 的 `Event`；`internal/server/server.go` 的 prompt/fork/events；session append-only tree 与 queue/agent delivery。

Codex 对照源码位于 `/data/hgy/codex`：

- `codex-rs/core/src/tools/handlers/request_user_input_async.rs`：schema、校验、`accepted` 与异步 UI item。
- `codex-rs/prompts/src/model_messages.rs`：工具 description。
- `codex-rs/context-fragments/src/answered_question.rs`：答案 envelope。
- `codex-rs/tui/src/app/side.rs`、`tui/src/app_server_session.rs`：`/side` / `/btw`、ephemeral fork 与 side boundary。
- `codex-rs/tui/src/bottom_pane/async_questions/`：逐题 identity、草稿和去重。Ki 不照搬其 turn 结束清除待答题的行为。

实现时把最终字段、事件名、生命周期和响应式细节回写上述已有契约文档；本文保留产品设计与交互决策，不充当实现后第二份不一致的 API 规范。

## 10. 实现前需要确认的细节与推荐做法

**本节全部为待确认的设计决策，推荐不代表已经确认或实现。** 前文涉及的默认值按本节进一步细化；尤其是旁聊的列表展示，推荐从“完全隐藏”改为“不占顶层、在父会话下可展开”。

### 10.1 决策速览

| 待确认项 | 推荐做法 |
|---|---|
| 旁聊是不是只读 | 是，执行层强制只读；允许限定范围的 `read` / `grep` / `glob`，不开放 shell、写工具和外部动作 |
| 是否为了 cache 保留主会话全部工具 | 不保留；权限与工具语义优先，接受主/旁聊工具差异导致的部分或全部前缀 miss |
| cache affinity | 同 provider / model / 凭据边界下，复用 fork 已有的 parent `cacheKey` 亲和机制；session/run/续传身份保持独立 |
| 模型、thinking 与上下文 | 模型及 thinking 跟随主会话；安全历史前缀固定在提问时，不刷新成父会话最新上下文 |
| 是否持久化 | 是，首次发送旁聊消息才创建；关闭窗口不删除 |
| 是否进入 session list | 进入可恢复的会话索引，但不作为顶层行；在来源父会话下分组展开 |
| 展开方式 | 复用父行左侧 disclosure，展开后分“子代理”“旁聊”组；手动展开优先，不因后台消息抢展开 |
| 子代理 / 旁聊图标 | 子代理改用机器人头 `IAgent`；旁聊用双对话气泡 `ISideChat`；fork 图标只表达真正的分叉操作 |
| 问题和旁聊数量关系 | 每题一个旁聊，多轮复用；问题卡与 session 子行打开同一个窗口 |
| 答案和旁聊内容回传 | 只回传用户明确提交的最终答案；旁聊 transcript 与整理稿不自动回灌 |
| 主任务已空闲时提交答案 | 默认启动新一轮；主任务被用户明确停止时，已有未注入答案保留待继续，不擅自复活 run |
| 无人回答 / “收起” | 不自动选答案、不按超时批准；收起只是 UI 归档，不代表答案 |
| 历史问题和草稿保留 | 问题随分支持久保存；未提交草稿按浏览器隔离保存，跨 tab 不覆盖，删除来源会话时清理关联 |

### 10.2 旁聊只读：限制到哪一层？

**需要确认：** 是“默认不修改，但用户可以临时授权写入”，还是“这个旁聊模式始终只读”？

**推荐：首期始终只读，不提供窗口内的写入授权切换。** 旁聊的职责是帮助用户理解问题并决定如何回答，执行更改应回到主任务；用户在旁聊中说“直接改吧”也不能突破能力边界。

只读的具体含义：

- 可以解释、比较、推理、给出代码 / 命令 / 补丁文本，但不会执行生成的命令或应用补丁。
- 可以读取主任务已允许的 workspace / 显式附件范围；不能因为是 fork 就扩大到任意宿主路径。`grep` / `glob` 也必须受路径、符号链接与文件读取策略约束。
- 不开放 `exec_command` 等 shell 工具：不能根据命令看起来像 `cat` 或 `git status` 就认定任意 shell 是只读。
- 不开放写文件、会改变外部系统的 MCP/extension 工具、子代理或递归旁聊；Code Mode 如果保留，也只能调用同一只读白名单，不能绕过执行层。
- 工具列表做过滤，dispatch 再做能力校验；历史里的工具调用只是背景，不构成新的调用权限。
- “只读”不等于文件快照，也不等于不会花费 token。主任务修改后，旁聊新读文件可看到新内容；读到的文件版本应可追溯。

**取舍：** 旁聊不能直接修复它发现的问题，但避免了两个模型同时写工作区、旁聊误继续主任务的问题。以后若确实需要可执行旁聊，应设计为明确的新会话能力，而不是在现有只读旁聊里悄悄升级。

### 10.3 Prompt cache：能复用什么，不能承诺什么？

**需要确认：** 优先完整沿用主上下文，还是优先低 token 的摘要上下文？是否允许为命中缓存而保留不该开放的工具？

**推荐：继承完整且安全的历史前缀，复用现有 cache affinity；不以牺牲只读隔离换命中率。** 把“第一轮尽量共享父缓存”和“旁聊自己的后续轮稳定命中”分开看。

#### 已有可复用机制

Ki 的普通 `ForkAt` 已继承源会话的 `config.cacheKey`，没有源 key 时用源 session ID；loop 使用 `CacheAffinityKey()`。Codex provider 中，`prompt_cache_key` / 亲和用的 `session-id` 可沿用源身份，而 `thread-id`、client metadata、window/sticky 和流式续传状态仍是 child 自己的。见 [provider.md 的 Codex 请求身份](provider.md#codex-请求身份) 与 [session.md](session.md)。

这只是**缓存路由亲和，不是命中保证**。其它 provider 是否支持同类 key、自动缓存还是显式 `cache_control`，必须按适配器能力处理，不能把 Codex 的 headers 发给所有兼容 API。

#### 推荐的请求构建规则

1. **配置先一致。** 默认沿用 provider、API/base、model、thinking 和合法凭据边界；不在首次旁聊偷偷换成更便宜的模型。不同模型/账号/端点的 KV cache 不能靠相同 key 共享。
2. **历史先稳定。** 安全前缀内消息的顺序、正文、tool call/result、图片/附件表示和合法 opaque context 尽量沿用 canonical 构建结果；不要为了显示简洁而重排或总结整段历史。
3. **变量尽量后置。** question ID、side session ID、旁聊模式说明、当前问题与用户旁问放在继承历史之后的固定初始化片段里，不把主任务的实时运行状态、时间戳或 UI 状态写进前缀。
4. **指令优先级不能为 cache 降级。** 协议支持有序的后置 developer message 时可使用；若适配器只能通过 system 表达 side policy，则接受 system 变化带来的 miss。不能为保持相同 system，仅依靠一条低优先级用户文本实现只读或指令边界。
5. **工具稳定但真实。** side 使用固定只读工具 profile，名称、schema 和排序稳定，不随窗口开关、问题回答状态或每轮聊天变化。主会话工具更丰富时，允许两者不同；不能把写工具继续声明给 side，再指望模型不调用。
6. **资源快照稳定。** 固定继承可复用的 prompt 资源版本；全局 reload、模型切换、权限变化等明确失效事件才刷新。不能从“每轮重新读取当前日期/资源”引入无意义前缀变化，也不能冻结已经撤销的权限。
7. **多轮只追加。** 初始化的 side policy 和原问题只写入一次；后续追加旁聊 user/assistant 与明确的整理草稿请求。不能每次发送都重新 fork 或把完整父对话再塞一遍。
8. **身份不能混用。** 只继承合法的 cache affinity，不复制父 run ID、活跃 WebSocket 续传基线或 `previous_response_id`。provider opaque/checkpoint 仍按现有 binding 校验，失配时走合法降级。

**必须提前接受的限制：** 当前 `prompt.Build` 会根据实际工具生成 “Available tools”，Anthropic adapter 会给 system text 设置 cache marker，provider 也可能把工具声明编码在最前面。side 变为只读后，工具和 system 都可能变化，从而较早破坏父缓存前缀；“历史相同 + cacheKey 相同”不代表父历史一定命中。即使主、旁聊工具相同，fork 重写附件路径等 wire 差异也可能造成 miss，不能凭数据库里的原文相同推断请求相同。

#### 优化顺序与测量

- 首期优先验证：相同 side 的第二、三轮不会因无意义的 system/tools/资源变化而持续 miss。
- 再比较：同模型安全 fork 的首轮与不带 affinity 的对照请求。以 provider 报告的 cache read / cache write / uncached input、TTFT 和费用为准，不用本地复制消息数量冒充缓存命中。
- 把诊断中的 `system` / tool schema / canonical message prefix 的差异定位清楚。请求 trace 用 hash/已有脱敏信息，不为排查 cache 暴露凭据或完整私密旁聊。
- 不设“必须 100% 复用父缓存”的验收承诺，不跨 provider 生搬 cache 控制参数，不为保温定时发送空请求。
- 长历史超过窗口时沿现有 compaction 策略处理；摘要 fork 可作为用户未来显式选择的“轻量上下文”模式，不默认牺牲问题背景来省 token。

**取舍：** 只读工具可能降低首轮父缓存复用，但权限清晰、上下文完整；旁聊后续轮仍可在自身稳定前缀上获益。默认不自动换小模型，也让结果质量和缓存诊断更可解释。

### 10.4 持久旁聊要不要显示在 session list？

**需要确认：** 完全隐藏，只能从问题卡重进；还是作为普通会话平铺；还是作为父会话下的关联会话？

**推荐：第三种——可在父会话下展开，但不占普通顶层列表。**

![会话树与图标区分建议](assets/async-questions-session-tree.svg)

建议交互：

- 父会话左侧 disclosure 只负责展开 / 收起，不触发打开会话。展开后分组显示“子代理 · N”“旁聊 · N”，避免两个概念混排。
- 子代理保持现有递归树结构；旁聊首期只挂在来源主会话下，不支持在旁聊里再创建旁聊。
- 只有实际创建并持久化的 side session 才进入旁聊分组。收到一个问题不等于创建一个 session，单纯打开问题窗口也不增加子会话。
- 旁聊行用问题的简短标题，例如“发布范围”；副标题显示“待回答 / 已回答”和“旁聊运行中 / 已停止”，不能仅用一个绿点混淆状态。
- 点击旁聊子行，打开与问题卡相同的答题 / 旁聊窗口，定位到对应问题；默认不替换主 transcript。打开已回答问题时显示回执与旁聊历史。
- 从其它会话点击时，先定位来源主会话，再打开窗口；原问题不在当前分支时允许查看旁聊，但禁用对错误分支的答案提交。
- 展开状态沿用当前浏览器页面生命周期规则；用户主动点旁聊或问题卡时可展开其父链，后台产生 side 消息不强制展开整个会话树。
- 窄屏复用侧栏 drawer，打开窗口后关闭 drawer，避免三层遮罩；返回时能回到刚才的子行。
- 提问一落盘就通过公开 `questionLinks` 提供来源会话的可展开讨论入口；此时只打开问题窗口，
  首次发送才创建旁聊。已有真实旁聊时按 question ID 替换入口，不重复显示；首次创建自动展开来源。

**API / 数据约束：**

- “不占顶层”是展示和过滤规则，不代表 server 隐藏这类 session。已有 session list/详情返回 `purpose`、parent、question、运行状态与只显示 side 后缀的边界，客户端可恢复和导航；不能只在浏览器内维护 side 列表。
- 不再使用 `forkMode == tree` / `depth > 0` 直接推断“这是 subagent”。会话用途、父子关系和删除策略必须分开；side purpose 与真实 agent identity 不得同时成立。
- 父行的运行灯只表示主 run；子代理运行数、旁聊运行数分开聚合，不把旁聊算成主任务正在执行或 agent 正在等待。
- session 列表从独立持久摘要读取问题入口；首次重建和增量追赶在后台进行，通过
  `questionLinksLoading` 明确尚未完成，不阻塞 workspace 或基础列表，也不占用聊天 transcript 缓存。
- 分组默认各显示少量条目并提供“显示更多”，不能为旁聊列表读取全部 transcript；搜索主列表时不把大量旁聊平铺挤掉主任务，提供“包含旁聊”过滤后按父会话归组展示。
- 导航展开关系由服务端持久 metadata 重建；处理来源缺失、无效 parent 或环时，应显示可恢复的独立“旁聊”记录，不静默丢失历史，也不误标为 subagent。

**取舍：** 比完全隐藏多一点侧栏复杂度，但用户能找回关闭、刷新后或已回答问题的讨论；比平铺更不容易淹没主会话。它也明确区分“问题队列”和“已经发生的旁聊”。

### 10.5 图标：子代理与旁聊怎么区分？

**需要确认：** 是否替换目前子代理行使用的 `IFork`，以及旁聊是否用相似的分叉图标？

**推荐：替换子代理图标；旁聊使用另一种明确的图形。**

| 类型 / 操作 | 推荐图形 | 建议组件名 | 意义 |
|---|---|---|---|
| 子代理 | 机器人头轮廓 | `IAgent` | 交给另一个 agent 执行的任务 |
| 旁聊 | 两个重叠的对话气泡 | `ISideChat` | 用户参与的独立讨论 |
| 普通 fork / 分叉操作 | 分支节点与连线 | 保留 `IFork` | 从某个上下文分出新历史 |
| 问题栏 / 待回答入口 | 问号气泡 | `IQuestion` | 需要用户回答的问题 |

统一采用项目现有 inline SVG 风格（16 / 18px、相同线宽），不用 emoji 和新图标依赖。建议类型图标保持中性色；状态灯和选中态再使用现有颜色 token，**不能靠蓝色/绿色区分类型**。

同步替换会话树、agent 面板和其它表达“子代理身份”的位置；普通消息上的“fork 此处”操作仍用 `IFork`，不要全局替换所有 fork 图标。旁聊 tooltip / 可访问名称叫“旁聊”，子代理叫“子代理”，分组和行副标题也提供文字线索。

**取舍：** 机器人图形更容易联想到执行代理；双气泡强调讨论。与分叉操作分离后，不需要用户理解底层都用了 fork 才能辨认。

### 10.6 一题一个旁聊，还是整批问题共用？

**需要确认：** 一次 `questions[]` 中的多题是否共用一个 side session？是否同时支持不依附问题的普通 `/side`？

**推荐：首期一题一个旁聊、按需创建，多轮复用；不额外引入通用 `/side` 入口。**

一题一个 side 能使上下文、草稿、历史、session 子行和回传目标保持一一对应；切换问题不能把上一题讨论误整理到下一题。整批问题的其它题目可作为同批背景提供，但每份答案仍有明确的 question ID。

代价是实际讨论多个问题时会创建多个 session。用惰性创建和共享合法 cache affinity 控制成本，比让一条多题旁聊反复切换提交目标更可靠。后续通用 side 可以复用相同 purpose / 生命周期基础，但属于额外功能。

### 10.7 模型和上下文能不能在旁聊中更新？

**需要确认：** 使用主模型还是便宜模型？旁聊展开期间是否跟随父历史？主模型切换后是否同步？

**当前行为：模型跟随来源主会话，历史背景保留提问时的安全快照。**

- 旁聊标题显示模型和快照时点；主对话继续推进不反复改变 side 的输入前缀。
- 首期不提供“刷新父上下文”按钮，避免把新历史插到已有 side 对话之前，造成语义跳变、cache miss 和工具配对风险。
- 主会话显式选择模型时同步空闲旁聊。忙碌旁聊不切断正在生成的回复，下一轮重新读取来源配置；更换模型遵循现有 model-change 与上下文降级规则。
- 读取当前文件不代表刷新了父对话；需要新背景时用户可以在旁聊中补充。以后若加入上下文更新，应作为可审计的新上下文材料追加，或明确重开一个 side，不改写旧历史。

**取舍：** 背景可能落后于主任务最新发现，但问题的原始语境可复现，成本和缓存也更稳定。

### 10.8 关闭、停止、归档和删除是否区分？

**需要确认：** 关闭是否取消正在运行的旁聊？问题回答后是否删除 side？保留多久？

**推荐：四种动作分开，不做隐式删除。**

| 动作 | 推荐结果 |
|---|---|
| 关闭窗口 / 切换问题 | 保存本地草稿，side 可继续运行；之后从问题卡或父会话子行返回 |
| 停止旁聊 | 只取消当前 side run，保存已经产生的历史 |
| 收起问题 / 提交答案 | 问题归档，side 历史仍保留，不能再自动向父会话投递内容 |
| 删除来源主会话 | 在删除确认中包含关联旁聊，按明确 ownership 级联清理 |
| 删除单个旁聊 | 仅在 side 空闲且确认后删除；问题和已提交的答案保留，清除关联，下次讨论重新创建 |

首期不做定时清理，也不因主任务完成、重启或关闭浏览器而丢讨论记录。删除策略不能仅凭“为了显示成树”而选 `forkMode=tree`；用明确的关联所有权实现或验证级联。若以后允许“保存为独立会话”，必须显式解除所有权，并提示其不会再随父会话删除。

### 10.9 答案何时唤醒主模型，能否撤回？

**需要确认：** 主任务已结束后，点击最终答案是否立即开始新一轮？已排队时能否修改？

**推荐：最终提交默认就是向主任务投递输入；运行中 steer，空闲时启动新一轮。**

- 按钮附近在空闲时说明“发送后将继续主对话”，不能让用户以为只是保存表单。
- 若用户此前明确停止了主任务，**之后主动点击**最终提交属于新的继续指令，可以启动；但停止之前已经排队、尚未注入的答案不能自行触发重启。
- `answer_queued` 时可提供“撤回待发送答案”，但仅限服务端尚未将其提升为 user message；撤回与消费必须在同一投递边界串行仲裁。首期若不实现此能力，应明确显示不可编辑的待投递回执，而不是只在浏览器里撤销。
- 已进入模型上下文的答案不可从历史中“撤销”。更正是新的 user 消息，不能回写原 tool result 或删除模型已读的内容。
- 一批多题按题提交，不强迫全部填完。批量提交可后续增加，不能默认把其它题的预选值一起发送。

**取舍：** 行为与聊天发送一致；清晰区分“保存草稿”“已接收等待注入”“已进入主上下文”，减少误以为模型已经读到答案的问题。

### 10.10 需要强制阻塞的问题怎么办？

**需要确认：** `request_user_input_async` 是否要加入 `required` / `blocking` 参数，或无人回答时自动采用推荐项？

**推荐：不加阻塞参数、不自动采用选项，保持 Codex schema 与异步语义。**

模型自行判断可做哪些独立工作；必要的业务权限仍由相应执行 / approval 机制保证。若没有可继续的工作，模型可以结束当前轮并说明缺少什么，问题仍然待答，用户回答后再继续。不能通过循环 `sleep` / 轮询刷模型请求来装作后台等待，也不能将超时当授权。

“收起”默认只影响 UI，不伪造用户回答。若以后要告诉模型“请你自行决定”，应是另一项明确的用户发送动作，附可编辑文本，并经相同 typed delivery 路径进入上下文。

### 10.11 持久内容、草稿与能力开关如何处理？

**需要确认：** 旁聊内容是否自动同步所有客户端？关闭功能后是否丢历史？子代理能不能创建问题？

**推荐：已提交内容服务端持久化，未提交草稿浏览器本地保存；关闭新建能力不删除历史。**

- 已提交 side 消息、问题状态和最终答案按现有认证与 session 读取权限可访问；不因 UI 默认折叠就称为“私密旁聊”。主模型默认不见 side transcript，与服务端有权客户端可见是两个概念。
- 未提交的答题和旁聊输入草稿分别保存，不跨浏览器自动同步，也不通过 SSE 广播；本地持久化遵循现有草稿和附件策略，不把敏感内容塞进 URL。
- 工具全局开关关闭后，不允许新建问题；历史仍可读。对已有 pending 问题保留正常答案提交与既有 side 查看/继续入口，除非管理员同时撤销了会话运行能力；不能使待答问题变成无法处理的死记录。
- 子代理和 side 不暴露 `request_user_input_async`。需要人类决策时，子代理向主 agent 汇报，由主 agent 生成可归属的用户问题；不能只在 schema 列表里隐藏而允许嵌套执行绕过。
- 服务端将 readonly profile、purpose 和幂等关联当成持久能力元数据，不信任前端传入的 `readOnly=true` 或 session 类型字符串自行授权。

### 10.12 建议优先确认顺序

建议先确认 **只读权限、缓存取舍、嵌套 session list、图标替换** 四项，它们会直接影响后端会话类型和前端骨架；随后确认一题一旁聊、生命周期和答案投递细节。

若采用本节全部推荐，首期的整体形态就是：**只读且持久的逐题旁聊，继承安全上下文并尽量复用缓存，在父会话下可展开找回；子代理用机器人图标、旁聊用双气泡，只有用户最终确认的答案才进入主模型。**
