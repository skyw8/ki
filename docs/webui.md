# WebUI

`ki serve` 同域出页面：一个二进制，静态资源嵌在 `web/dist`，API 仍是 `/v1/*`。

[异步提问与旁聊](async-user-input.md) 记录完整交互、模型上下文与持久化契约，附桌面 / 手机 UI 草图。

问题栏位于虚拟消息列表外，按 question ID / revision 对齐 GET 与 SSE。桌面答题窗口左右
分栏，手机使用全屏“回答问题 / 旁聊”页签及安全区布局。预选只是草稿，显式确认才发送；
提交后保留原来的全部选项、选中状态和补充文本，使用相同表单布局只读展示；答案文本可复制，
不允许改选、编辑或重复提交。确认后的 `answerForm` 随答案持久化，重开、刷新和其他窗口
均以服务端回执恢复，不使用未提交的本地草稿覆盖。仅有答案正文的记录按选项边界还原展示。
草稿按 server/session/question 存在当前 tab 的 sessionStorage，刷新恢复，其他 tab 不覆盖。
旁聊拥有独立 transcript controller、SSE 和停止按钮，首次旁问才创建；关闭窗口保留运行。
旁聊复用普通对话的输入卡片样式，仅提供发送 / 中断；用户手动将建议写入回答草稿。
问题栏浮在左上方，不改变消息区宽度、内边距或居中位置。主会话切换模型时空闲旁聊
同步 provider/model/thinking，正在生成的旁聊在下一轮跟随；历史背景仍固定。排队答案提供“继续主对话”；
已回答的其他分支回执只提示缺失，不自动重投。侧栏按来源归组旁聊，并区分问题、旁聊、
执行型子代理和普通分叉图标。列表的公开 `questionLinks` 让问题一出现即可展开来源会话、打开讨论，
但不会提前创建旁聊或发送提示；真正创建后入口与子会话行去重，并自动展开来源。后台问题事件也更新导航；
列表在途响应遇到新问题事件后重新获取，较旧修订不能覆盖新导航。重复选择当前会话不清空 transcript，
问题栏和消息导航的同级 key 分别使用 `questions:` / `requests:` 命名空间，避免重载时遗留重复 DOM。
点击旁聊定位来源问题窗口。相关交互回归在
`web/e2e/questions.spec.ts`，响应式矩阵继续覆盖其余布局。

## 交付与缓存

所有文本响应（SPA 资源、`/v1` JSON）统一 gzip，`text/event-stream` 除外（SSE 逐事件 Flush，
压缩缓冲会拖延流）。

| 资源 | 缓存策略 |
|---|---|
| `assets/`（Vite 内容哈希产物，含 favicon SVG） | `Cache-Control: public, max-age=31536000, immutable`；端口转发/慢链路下刷新不再逐个回源校验 |
| SPA HTML | `no-store` |
| `sw.js`、`manifest.webmanifest`（根路径，不带哈希） | `no-cache`，强制每次校验；service worker 被长期缓存会钉住旧的推送处理器 |

浏览器打开 `http://127.0.0.1:19800/`，或经 SSH/IDE 端口转发打开同一端口；`scripts/run.sh`
默认监听 `0.0.0.0:19800` 时，应使用主机的 LAN IP。

裸 `ki` 或双击二进制会打印并自动打开带 `#token=…` 的认证链接，浏览器无需再输入 token。SPA
读取 fragment 后立即清除，先查询 auth status 绑定 serverId，再通过 `POST /v1/auth/login`
换取短期 HttpOnly session cookie；已有有效 cookie 时直接进入。fragment 不发送到服务器或
Referer，不写入浏览器存储；链接本身是凭据，不要分享。普通 URL 首次打开仍可在登录页输入
本机 `server.json` 中的 token，自动登录失败也回退到此流程。

SPA HTML 和 URL query 不包含 server token。浏览器 API 写请求同时带同源 CSRF header，前端
只用同域相对路径调 `/v1/*` 和 `/assets/*`，不把宿主文件路径写进 `href`，也不用系统选目录。
server 重启后原 browser session 失效，可手动重新登录或再次运行 `ki` 获取新认证链接；切换
后端不会重用已消费的 fragment token。

`GET /v1/auth/status` 返回进程级随机 `serverId` 与 `csrfCookieName`；登录 Cookie 与 CSRF
Cookie 按此实例命名，避免多个 localhost 转发端口互相覆盖。绑定后的 API 请求携带
`X-Ki-Server-ID`，不同实例在执行前返回 421，堵住 SSE 尚未发现切换时的误读/误写；auth
status 与 health 允许重新发现实例，未绑定的 CLI Bearer 请求不受影响。进程重启或相同 origin
换后端时，push `ready.serverId` 变化或订阅返回 401/403/421 会撤销旧页面运行态、重新检查
认证。

## 浏览器状态

浏览器存储按 origin（协议、域名、端口）隔离，不按端口转发的目标服务器隔离；Cookie 更只按
host/path 隔离、不区分端口。

| 状态 | 存放位置 |
|---|---|
| 主题、语言、消息显示、通知开关 | 本浏览器 `localStorage` |
| 选中的模型与 thinking | `localStorage` + server 的 last-used |
| workspace 展开、subagent 展开 | 仅本次页面生命周期（刷新即折叠） |
| 跨 tab 正在查看的 session | `localStorage['ki-focused-session:<serverId>']`，30s 租约、10s 续租，按实例隔离 |
| 模型与 workspace ID | **不写** `localStorage`（旧 `ki-last-model` / `ki-ws-expanded` 不再读取） |

新建会话优先使用本标签页中仍在当前目录里的模型选择，否则回退服务端 last-used 默认，再到
目录首项；目录尚未加载、加载失败或当前选择已被移除时，请求省略模型与 thinking，让服务端
选择默认模型，不发送旧服务器或已卸载 extension 的模型。已有会话的模型不会因目录刷新而被
静默切换。

## 页面结构

### 侧栏

工作区树支持创建 / 重命名 / 删除登记和会话日志、组内 `+`、pin、拖拽、每组默认 5 条「显示
更多」。

**工作区默认折叠**：只显示工作区行，空闲会话不出现在侧栏；展开状态只存在于本次页面生命
周期（刷新即折叠），不写 `localStorage`。三件事会展开：

- 用户点工作区行；
- 打开 / 新建某个会话（其所在工作区自动展开）；
- 某个工作区里有 `running` 会话（包括别的 tab 或页面加载前就在跑的，列表刷新时自动展开）。

显式的折叠（`false`）优先于自动展开，否则运行期间开关会失效。置顶的会话稳定排在本组最前
（同组多个 pin 之间、以及未置顶会话之间沿用 `sessionIds` 顺序，段内拖拽仍可改序），新建
会话前插只落在未置顶段之前、不会盖过 pin。

subagent 子会话（`forkMode=tree`）常驻嵌套在父会话下方：默认折叠，点箭头展开/收起、可多级
递归展开，点行打开会话（打开子会话时切到对话页；当前会话的父链会自动展开）；子行按深度
缩进并有引导线，不可拖拽排序，菜单不提供 pin；孤儿 / 跨 workspace / 环这类不可信父子边
退化为顶层行。普通 `flat` fork 继续平铺。触控端把展开箭头和行菜单放大到至少 40px，缩进
引导线随箭头尺寸一起缩放。

标题+正文搜索只命中非 subagent 会话。侧栏会话灯在本端开始 listen 时立刻变绿（不等列表
刷新），其它客户端起的 run 与删除由 push 的 `invalidate` 帧驱动；列表刷新做合并，多个 run
同时结束或连续 pin/move/delete 只发一次请求外加一次尾随刷新，响应 ETag 未变时不重建侧栏
状态。workspace 和 session 各自加载、合并刷新与提交结果，一方缓慢或失败不阻塞另一方；
各有首次加载提示和独立重试按钮，后台刷新保留已有行。只有成功且仍有效的 session 列表
才能清除已删除的当前会话，workspace 查询不能据此改变会话选择。`invalidate` 按
`sessions` / `workspaces` 范围刷新；创建、删除等同时影响两者的操作仍可显式刷新两份列表。
问题事件使在途 session 列表重新获取，不会延后已返回的 workspace。`questionLinksLoading`
表示历史问题摘要仍在重建，来源行保持可展开并显示加载提示，已有旁聊链接继续可用；
`question_updated` 仍立即补入问题入口；加载中的摘要按 question ID 保留本地较新修订及尚未返回的入口，
只有完成的投影才能移除旧入口，摘要完成后的列表刷新再移除加载提示。

### 对话

#### 渲染

气泡、Markdown、Think、默认折叠的工具行。

Markdown 用 Streamdown + remend 补全未闭合标记，`@streamdown/cjk` 处理中日韩强调。`mermaid`
/ `plantuml`（含 `puml` 别名）围栏统一走 `DiagramBlock`：

- 顶部左侧 diagram/source 切换，右侧下载 PNG/SVG 与复制（复制在最右）；
- 图居中且可点击放大（全屏查看器，支持缩放条/滚轮缩放和拖拽平移）；
- mermaid 用 `@streamdown/mermaid` 在浏览器内渲染；
- plantuml 没有本地渲染器，改由 PlantUML 服务器出图（默认
  `https://www.plantuml.com/plantuml`，可用 `localStorage['ki.plantumlServer']` 覆盖为自建/
  内网服务，失败时回退到源码 + 重试）。

GFM 表格自带、工具条复制为 Markdown；外观仍走 `.md` 设计 token，不用 Streamdown 自带的
Tailwind/shadcn 外壳。

链接与图片不走 Streamdown 默认的 `rehype-harden` 白名单：`docs/plan.md` 这类不带 `./` 前缀的
相对路径会被它判成非法 URL 并渲染成 `文字 [blocked]`，而 `defaultOrigin` 又会把相对路径变成
站内地址、点击后整页刷新回空会话。Ki 改为自己给 Streamdown 传 `rehypePlugins`
（`features/markdown/rehype.ts`）：`rehype-raw` + `rehype-sanitize`，安全边界是 sanitize
（`javascript:`、`file:`、`vbscript:` 的 `href`/`src` 直接剥掉，`data:` 图片同样剥掉），
不再有任何 URL 被改写成标记文本。

`features/markdown/links.tsx` 接管 `a` / `img`，用 `lib/localPath.ts` 把 href 分成四类：

| href | 渲染 |
|---|---|
| `http(s):` / `mailto:` / `tel:` | `<a target="_blank">` |
| `#fragment` | 页内锚点 |
| 相对或主机绝对路径（`docs/x.md`、`./x`、`/abs/x:12`） | 解析成绝对路径后点开 `OutputPreview` |
| `javascript:`、`file:`、无法解析 | 纯文字（无 href、无标记） |

相对路径按当前会话 `cwd` 拼接，`#fragment`、`?query` 和 `:line[:column]` 后缀在解析前剥离；
图片走 `GET /v1/fs?path=…&preview=1` 取 blob 再转 `blob:` URL（`<img src>` 带不上
`Authorization`），失败显示 `md-local-image-missing` 而不是破图。`MarkdownLinkProvider` 由
`App.tsx` 用会话 `cwd` 与 `OutputPreview` 的打开函数注入；没有 provider 的场合（扩展面板、
旁聊等）本地链接退化成纯文字，不会触发导航。

#### 工具行

默认折叠，展开状态由 `ChatView` 按 row id 持有——虚拟列表在批次结算时重挂尾部行也不会丢失。

| 工具 | 呈现 |
|---|---|
| `exec_command` / `write_stdin` | 只读终端卡（`tools/TerminalBlock`）：命令行 + workdir/TTY/`#session` 元信息 + 退出码徽标 + `pre` 输出的横向滚动窗口。运行中读 `process_updated` 的累积快照而不是拼接 delta，超过 2000 行只渲染尾部，有 `output_file` 时可打开完整输出 |
| `edit` / `apply_patch` / `write` | 统一 diff（`tools/DiffView`，`react-diff-view` 解析后端已生成的 unified diff，配色映射到 `--dsw-alias-state-*`）。双行号槽、文件头显示 kind 与 +/− 统计、可复制、超长 diff 只渲染首尾并显式给「显示完整 diff」，`apply_patch` 每文件一段 |
| `read` | 带行号与「显示第 X-Y 行 / 共 Z 行」「继续从第 N 行读」 |
| `grep` | 按 `details.hits/counts/paths` 渲染成可点的 path:line 行 |
| `glob` | 文件列表 |
| `spawn_agent` / `send_message` / `followup_task` / `wait_agent` / `interrupt_agent` | 一行回执 |
| `list_agents` | 计数摘要 |
| `exec` / `wait`（Code Mode） | 把 `Script completed{…}{…}` 拆成每个嵌套调用一块并标出失败项 |
| 其余 | IN·OUT 卡 |

所有行都保留 Inspect；`exec_command` 的 `cmd` 和路径预览可选择、可复制。渲染优先读结构化
`details`（含 `spill` 引用），缺失时回退正文，因此旧会话仍然可读。

#### 请求导航

右侧请求导航以人类请求为单位。选中会话后保留入口，即使当前尾部暂时没有用户消息或索引尚未
加载：桌面悬停或点按右侧三条杠，浮层列出当前分支的**人**发的所有 user 请求。

列表走 `view.allEntries`（含惰性 index 的整条分支），不随对话窗口变化，所以不必先往上翻。
`agent` / `agent:<id>` 这类运行时写入的 turn（subagent directive、`<task-notification>`）不
进列表，扩展来源（Telegram 等真人中继）保留。

高亮当前视口那条，点一项滚到该气泡：目标不在已加载窗口内时先按页补历史（`?before=`，
detailed 每页最多 500 条、compact 每页完整一轮）再定位；打开浮层按需请求 index，显示加载
状态、失败重试和确认空列表；不能以当前窗口没有 user 推断全分支没有请求。恢复/运行结束按
leaf 合并刷新已经请求过的索引，旧的在途索引完成后只补一次更新。超长对话沿已有虚拟列表
`scrollToIndex`，很多请求时列表自己虚拟化，12 条以上可筛选。

导航列表只滚动自身；普通行和虚拟行使用一致的局部坐标。触屏选中后关闭面板并恢复触发按钮
焦点，清除搜索；触控端不保留 hover 灰底，当前项由 `aria-selected` 与 active 统一表示。按钮
和条目至少 44px，虚拟列表估高与 CSS 一致；Escape 关闭并恢复焦点，条目支持方向键/Home/End。
桌面保留悬停打开，手机点按打开。

#### 每轮汇总

每个 turn（user → 该 turn 最后一条节点）在末尾挂一条分割线，两侧各一段 hairline、中间是该
turn 的单行汇总统计（`transcriptIdentity` 的 canonical TurnId + `projectTurnStats`，统计只在
该人工轮的最后渲染 item 展示）：轮次、耗时、步数、工具调用与失败数、最近一条 assistant 的
TTFT 与吞吐（`lastTtftMs` / `lastTps`，取该 turn 最后一条消息而非首步或整轮平均）、缓存
命中率与缓存未命中。输入/输出和 cost 已移到 composer 会话统计条，分割线只保留这一行（窄屏
自动换行）。

耗时在运行中按 `now - startedAt`（首个人工 input；无人工 input 的 runtime-only 分支取首次
runtime input）实时累加，后续通知不重置起点，结束后取 user 时间戳到该 turn 最后一条节点
的时间戳；工具行用 `startedAt + durationMs` 计入尾部，无时间戳时退回各步延迟之和。实时值
由 `hooks/useNow` 的共享 200ms 时钟驱动，只有运行中的行订阅它，空闲时不排任何定时器。

仍在流式或工具运行的 turn 标 `live`，汇总线照常就地渲染（轮次前加一个脉冲点，耗时按共享
时钟变化，永不折叠），结束时在原地换成固定值，因此每轮耗时从第一刻起可见。

composer 的会话统计条（`session-stats-live`）显示整个分支的**模型运行总耗时**：历史 turn 的
settled 耗时（`lib/model.ts` 的 `sessionStats`，trajectory records 覆盖整条分支含仅 index 的
entry；compact 折叠 turn 用服务端 `compactTurns` 的 `elapsedMs` / `cumulativeElapsedMs`）加上
最新 turn 的实时跨度，因此不再随当前 turn 归零、运行结束后也保留。

compact、展开、运行和结束统一由 `projectTurnStats` 投影：服务端快照 + 本地观察值 − 同一
快照已覆盖的本地节点；展开不会重复加计，运行结束不会用旧快照覆盖新结果。当前工具批次的
`toolStates` 提供被隐藏调用的身份及结果状态；`assistantAt` 提供已完成模型步骤边界。并行
工具的任意兄弟节点先结束都不能把其它调用判为陈旧。每轮只挂一条统计，挂在**最后一个渲染
item**（可以是 keep=0 的折叠行），而非必须存在的最后一个原始节点。汇总线在同一个虚拟 item
里追加，`jumpToId`/视口高亮仍按节点索引。窄屏（`max-width: 760px`）隐藏两侧 hairline，改成
整宽分隔线 + 居中的统计。

不展示 SYSTEM / system prompt 行（首次和变化都在上下文页查看）。

#### 折叠

compact 消息显示（见设置）**在 turn 内**折叠：每个 turn 的用户气泡始终保留，其后只留最近 N
条回复，之间的回复（长 turn 的工具往返）由 `lib/messageView.ts` 的 `foldReplies` 折成该 turn
内的一行，位于气泡与该 turn 可见回复之间；点该行就地展开，不重排时间线。

reading 或更早轮的操作锚定该行，原本 following 的最新人工轮继续跟随，`jumpToId` 落到折叠
范围内会先自动展开再滚动（行本身带首个被折叠节点的 `data-msg-id`）。短列表和长列表使用
同一个虚拟化实例与 DOM 外壳，折叠、分页或第 48 个节点不再切换列表结构。

展开/收起保留显式意图：reading 不因几何贴底恢复 following；只有原本 following 且操作最新
人工轮时继续跟随，更早轮转为 reading。仍在流式/运行中的节点永不折叠，但**运行中的最新
turn 也照常折叠**（实测一个在跑的 turn 有 61 个回复节点，折叠后只剩 2 行；这也是重新进入
运行中会话慢的一半原因）。真实运行节点可以突破 N 的折叠边界；陈旧标记由同一调用的持久化
结果、下一模型请求或服务器 `assistantAt` 边界清理，而不是按兄弟节点顺序猜测。

#### 停止与取消

WebUI 的 abort 请求标记 `source=webui`。`run_aborted` 立即在消息列表中插入独立红色状态行
（复用普通 `notice`，无左侧粗线或红点），按 `reason` 本地化，不把原始 `context canceled` 当
用户文案。loop 写完终止输出后，服务端把同一语义持久化为普通非 message leaf，刷新后仍在
正确分支和顺序中显示。

旧 transcript 的 unparented sideband 无法安全归属分支，WebUI 改由 branch-correct 的 aborted
assistant 合成通用取消行。取消行和 aborted assistant 与 compaction 行一样永不进入 compact
折叠，也不占 `keep` 名额。

#### 压缩状态

`compaction_start` 显示黄色「正在压缩上下文…」，`compaction_end` 显示成功、无需压缩或失败。
两种事件的 `lifecycleEntryId` / `parentId` / `timestamp` 与 jsonl 一致，实时事件立即进入身份
图，重放和正文补全不会复制或丢失状态。`entryId` 仍指成功提交的 checkpoint；加载 checkpoint
后由摘要替换状态行，不重复显示。empty/failed 没有 checkpoint，也保留其持久化终态。

生命周期行始终独立、不占 keep、不增加模型步数；正文缓存回收保留状态事实，只有 index 尚无
状态时显示本地化的待加载文案并补全，不能冒充成功。权威 idle 或后续实际请求使未结束的历史
start 停止转圈，不能因后来的 run busy 而复活。手动 `/compact` 没有 run stream，进度仍由现有
push 流（`GET /v1/events`，session sideband）传递。

#### 附件

底部和 edit composer 共用附件条与宿主机文件浏览器。选择器支持图片、纯文本/代码和 PDF.js
翻页预览，文本最多显示前 1 MiB，HTML/SVG 只作纯文本，不执行宿主内容。

composer 中图片用可放大的缩略图，文件用带名称与类型/大小的卡片；发送前/编辑态 composer 和
已发送 user 气泡显示图片预览，缩略图可打开同一全屏查看器。远端图片接近视口 200px 才加载，
卸载取消请求并撤销 Blob URL；Markdown 图片使用浏览器 lazy loading。

全屏查看器由虚拟行之上的共享 provider 持有，并保留独立 Blob URL，行卸载或临时消息换成
持久化条目不会关闭预览；关图时释放，不积累全局 Blob 缓存。浏览器文件拖到 WebUI 任意位置
都会显示全屏 drop target，并进入当前 edit composer，否则进入底部 composer；剪贴板文件仍
跟随获得焦点的 composer。

远端预览经带鉴权的同源 `/v1/fs` Blob 响应读取，不把宿主绝对路径导航给浏览器，也不把 token
放进资源 URL。粘贴/拖入文件上传成 session 内的内容寻址副本。文件引用 host-absolute path，
图片在 provider 边界读取。编辑移除只移除新消息引用，不删除工作区文件或旧分支仍引用的
blob。

#### 错误与 toast

操作结果（slash 回执、parentId/slash 的 409、Reload/切模型/会话操作失败）走右上角 toast，
portal 到 `body`，不嵌进 composer。提交（底部发送、edit 发送、Ctrl/⌘+Enter）立即清空输入框
与附件条并关闭 edit 卡片，不等服务端返回；`/compact` 这类同步 slash 命令在压缩进行中不会
残留命令文本。

仅在请求被拒绝（409、网络/校验失败）时恢复提交前的草稿，若用户期间已输入新内容则不覆盖。
对话气泡里的模型错误、目录/附件列失败、表单 JSON 校验仍贴在原处。扩展 `enqueue` 与
subagent completion notification 的消息带 origin，与用户气泡可区分；非人类 user 气泡使用
虚线框。

### Composer

#### 输入与发送

输入法组合期间（`isComposing` 或 keyCode 229），主输入和编辑输入的快捷键处理直接交还
浏览器；确认候选词不能发送、排队、停止运行或操作命令面板。结束组合后再按快捷键才执行动作。

命令按钮 + 行首 `/` 打开 slash 面板，数据来自 session `commands[]`；点选只填入输入框，回车
才发送；面板用不透明 `bg-layer-1`，描述单行省略。thinking 未选时显示该模型
`defaultThinking`，优先 medium 而不是列表第一项 off。

忙时停止和发送并存：发送走 `message.busy` 默认。Enter 带内容按默认发送（queue 则入队）；
Ctrl+Enter 带内容为 `delivery=steer`；Ctrl+Enter 空输入且 `queued[]` 非空则 `queueId` 提升
队尾进本轮。空输入 Enter 仍 abort；Ctrl+Enter 不 abort。composer 上方列出 `queued[]`（用户）
与 `extQueued[]`（扩展 FIFO，标 origin）：队尾标 Ctrl+Enter，每条有 Steer 按钮，可删。
`steer_accepted` / `run_aborted` 为 live SSE（后者兼 sideband）。成功 toast 约 3.5 秒消失，
错误需手动关闭。

#### 会话统计条

composer 下方一条统计：轮/步是当前分支总数，TTFT、吞吐、缓存命中、输入/输出和 cost 都是
**整个 session 的汇总**（`lib/model.ts` 的 `sessionStats`：`projectTurnStats` 把已加载窗口与
compact 快照按 turn 合并一次，未加载的折叠 turn 再用 `compactTurns` 的服务端统计按 turn
补齐，不重复计数；TTFT 取各 turn 首步延迟的平均，吞吐按输出加权的「总输出 / 总 decode
时长」，缓存命中为「总 cacheRead / 总 prompt」，输入/输出与 cost 为累计值），不再是最近
一条 assistant 的单次值；没有新 HTTP 接口。窄屏（≤520px）该行改成单行横向滚动并隐藏滚动
条（保留触摸拖动），不换行。

#### 编辑与 fork

edit 在原 user 位置展开为占满整列宽的编辑卡片：标题行（「编辑消息」+ Ctrl/⌘+Enter 发送 /
Esc 取消提示）、自动增高并按视口封顶的输入区和附件条，文本与附件一起形成当前 session 内的
sibling branch；分支用 `‹ 1 / N ›` 切换。

fork 从最终 assistant entry 创建并打开新的 session 目录（沿用源 session 的
provider/model/thinking），运行中仍可用（复制的是已落盘的完整前缀，不影响当前 run）；
regenerate 留在当前 session，运行中按钮置灰、点击 toast 提示「当前对话正在运行」，避免与
进行中的 loop 抢写。

#### slash

命令按钮 + 行首 `/` 打开面板，锚在整块 composer 卡片上、优先出现在输入框**上方**（不挡住
textarea）。有 session 时数据来自 session `commands[]`；尚未创建 session 时通过
workspace-scoped `/v1/commands` 预加载内置、prompt template 和 skill 命令。

点选只填入输入框，不覆盖已有正文；已有正文时命令默认插入开头。面板中 Tab 补全当前高亮项
（Shift+Tab 反向移动），首次回车只选择/关闭命令，面板关闭后再次回车才发送。两级：`/` 下列
`/{name}` + description（`argumentHint` 灰色写在名后，不 dump `completions`）；光标在
`/name` 或 `/name ` 且有 `completions` 时换成子命令列表。

新建会话：侧栏「新会话」和工作区 `+` 把当前 composer 的模型配置发给 `POST /v1/sessions`。

### 设置

设置页签顺序为「模型供应商」「Skills」「Tools」「MCP」「Extensions」「System Prompt」
「Message」「通知」「主题和语言」「关于」。

#### 关于

只读展示当前 daemon 的 `version` / `releaseTag` / `pid` / `startedAt`（含 uptime）/ `exe` /
`KI_HOME`，数据来自同一次 `/v1/meta` 刷新（浏览器读不到 `server.json`，所以这是 WebUI 显示
版本号的唯一来源）；窄屏下标签与值改为上下堆叠，路径自动换行，不横向溢出。

#### System Prompt

按渲染顺序列出每次请求追加进 system prompt 的来源：

- 内置层与已启用扩展的 `prompt.append` 层只读（展示路径与文本）；
- `{KI_HOME}/prompt/APPEND_SYSTEM.md`（全局）和当前 workspace 的
  `<cwd>/.ki/prompt/APPEND_SYSTEM.md`（项目）可编辑。

卡片显示真实路径、是否已创建、字节数（上限 64 KiB，超出即禁用保存）；保存（Ctrl/Cmd+S 或
按钮）与删除（确认对话框）走 `PUT`/`DELETE /v1/prompt/append`，成功后 server 立即全局
reload，运行中的会话在本轮结束的 release 边界生效。空内容保存被拒绝并提示改用「删除
文件」，未选中 workspace 时项目卡片显示提示而不猜 cwd。

页面底部「生效内容预览」直接渲染 `GET /v1/prompt/append` 的 `effective`，与模型实际收到的
追加栈一致。窄屏下字节计数换行、按钮换行排列，满足 40/44px 触控与响应式矩阵。

#### 通知

开关会话完成与新问题通知：开启时在用户手势里向浏览器申请 `Notification` 权限，权限被拒或非安全
上下文（局域网 http）时禁用并提示；开启状态存本浏览器 `localStorage['ki-notify']`。

完成感知不搭在选中会话的 run 流上（切会话就断了），也不为每个 running session 单开连接：
WebUI 每个 tab 只有一条 `GET /v1/events` push 流（server 在 run 结束时把带 `sessionId` 的
`agent_end` 也投给它，见 [events.md](events.md)），所以切到别的 session 或别的程序时，先前
的 run 跑完仍会通知。

发送条件是「没有聚焦的 tab 正在看这个 session」：聚焦 tab 把当前 session 写进跨 tab 共享的
`localStorage['ki-focused-session:<serverId>']`（含 tab 标识与 30s 租约、10s 续租，失焦/隐藏/
关闭时清掉自己那条），完成时读取它，只有等于完成 session 才静默——切到别的 session（值
不同）、切到别的程序（无值）都会通知；多开的 ki tab 共享同一份标记，因此前台 tab 正在看的
那个 session 完成时其它 tab 也不会重复提醒，且多 tab 同时观察到同一次完成时按 session 去重
（Notification `tag`），不会堆叠。

subagent session（`forkMode=tree`）一律不通知：它挂在 parent 下、由 parent 的 run 决定通知，
逐个子会话提醒是用户无法处理的噪音。用户主动 abort 的 run（先收到 `run_aborted`）不提醒。

通知标题取会话标题，正文为「`{cwd}` · 会话已完成」（标题相同的短 prompt 用工作目录区分）；
点击聚焦窗口。通知优先经已注册的 service worker（`registration.showNotification`）发出，
没有注册时才退回 `new Notification()`：Android Chrome 根本没有 `Notification` 构造函数
（`new Notification()` 抛 "Illegal constructor"），只有 service worker 能弹系统通知——否则
页面侧通知（包括设置页的「发送测试通知」按钮）在手机上会静默失败，而 Web Push 的完成通知
照常到达，看起来像「测试按钮坏了」。两条路都不可用时按钮会 toast 说明发送失败，而不是毫无
反应。

**新问题**：全局 `question_requested` 的每个新题触发一次系统通知，标题为会话标题，正文为
「待回答问题：`{title}`」（英文随界面语言切换）。沿用权限、通知开关、聚焦会话静默与子代理
过滤规则，不要求先在本浏览器见过 running 状态，因此 CLI 发起的主会话提问也能提醒。
同一 session + `questionItemId` 在当前页面生命周期内只处理一次；`question_updated`、回答、
恢复、历史加载以及相同工具调用重试不会再次提醒。页面与 SW 统一使用
`ki-question-<sessionId>-<questionItemId>` 标签，不覆盖同会话的完成通知或其它问题。
点击沿用聚焦 ki 窗口的行为；通知数据携带 session 和问题 ID，不包含快照、选项或答案。

完成通知只覆盖本浏览器已知 `running` 的 session（本 tab 发起的 run、以及经 push 刷新后见过的 running
行；push 流对每个 `agent_end` 都会到达，客户端用这个已知集合过滤，避免给 CLI 起的 run 弹
通知）。

**挂起补发**：手机锁屏、笔记本睡眠会冻结页面并断开 push 流，这期间跑完的 session 收不到
`agent_end`，而 push 重连不回放；所以 tab 恢复可见（或 push 重连发来 `ready`）后做的那次
全量刷新会顺带对比「之前 running、现在不 running」的集合，把错过的完成补成一次通知
（`lib/completion-catchup.ts`；删除的 session 和本 tab 主动 abort 的不补）。

**Web Push**：页面被冻结/关闭时由 Web Push 兜底——开启通知开关即注册 service worker 与
`PushManager` 订阅并发给 server，`agent_end` 与新问题同时投给推送服务，`sw.js` 在没有页面时弹系统
通知（详见 [push.md](push.md)）。两条路径共用同一个 `tag`，不会重复；有聚焦且可见的 ki
窗口时 worker 让位给页面。设置页在开关下多一行状态：推送已生效（后台/锁屏可收）或该浏览器
收不到后台推送及原因。

**端口转发注意**：`Notification` 是安全上下文 API（和 `navigator.clipboard` 同一条规则），
`http://localhost:<port>` / `http://127.0.0.1:<port>` 的转发（ssh -L、IDE/Codespace、
kubectl port-forward）可用；用 `http://<主机名或 LAN/Tailscale IP>:<port>` 明文访问时浏览器
根本不暴露该接口，此时通知页显示「非安全上下文」并提示改用 localhost 转发或 HTTPS
（Tailscale serve / 反向代理），而不是静默失败——`ki serve` 自身只跑 HTTP，HTTPS 由隧道/
代理提供。另外 push 由单条 `GET /v1/events` 承载，连接数不随同时运行的会话数增长；明文
HTTP/1.1 下仍会与 run 流、静态资源共用同源 6 连接，高延迟链路建议走 HTTPS/HTTP2。页里还有
「发送测试通知」按钮，用于验证权限和系统通知中心是否可见。

#### 开关与显示

Tools/Skills/Extensions 开关和 Message 忙碌策略写 `{KI_HOME}/toggles.json`。

Agents 页的「上下文窗口上限」写 `toggles.json` 的 `agents.contextWindow`（默认
500,000，范围 1–100,000,000，保存后下一次请求生效）：它是所有模型共用的硬上限，模型自身
窗口更大时按它计算 context meter 与压缩阈值，更小时保持模型窗口；输入非法时保存按钮禁用
并提示范围，保存成功以 toast 确认。

Message 页还有「消息显示」detailed / compact：compact 下每个 turn 的人工输入照常显示，只保留
该 turn 最近 N 条回复，subagent 等 runtime user-role 消息和其他更早回复一起折叠成一行（带
折叠条数与末条预览），N 默认 1、范围 0–20。点折叠行可就地展开；最新人工 turn 的折叠行仅在
原本已 following 时保持跟随，始终钉住该 turn 最新那条回复的底部，而不是钉折叠行本身——
展开引入的中间行不是 append，另有一次显式钉尾把新增行落到尾部，所以折叠行会随最新消息
一起被移出虚拟窗口；更早的折叠行则暂停跟随并锚定该行原地不动。这是本机显示偏好，和主题/
语言一样只写 `localStorage['ki-message-view']` / `['ki-message-view-keep']`，不落 server。

仍在流式或运行的节点始终展开（进行中的工作不会被折起来），该 turn 其余回复照常折叠。
Tools 始终列出所有可全局开关的内置工具；`write` / `edit` 与 `apply_patch` 即使不适用于当前
模型也保留在列表并标记「当前模型不使用」，避免切换其它开关时丢失模型专用工具的禁用状态。
扩展工具不在此处管理。

#### 扩展入口

扩展的全局配置、goal 等 panel 和 Telegram 等表单都从统一的扩展 Modal 进入。扩展配置页由
`config.schema` 的 `x-ki-ui` 统一渲染（协议见 [extension.md](extension.md)），不再按扩展名
分派专用表单。

凡是有选模型控件的地方（Telegram 回复模型、deep-web-search 的 Codex 模型和摘要模型），都在
同一行并排一个 thinking effort 下拉框，选项取自所选模型支持的思考等级，样式与模型按钮等
高；模型不支持思考时只显示模型按钮，effort 留空则跟随模型默认。

顶栏 chip 和 Extensions 设置里每个**已启用**扩展的 Configure 打开同一页面（goal 没有 config
schema 也一样）；停用的扩展没有 Configure，避免打开空的或不相关的 inspector。全局 UI 在
没有 session 时也能显示；选中 session 后，session UI 覆盖同名的全局状态。无信任按钮、无
原生文件选择器。

#### 选模型

支持按 provider、model ID、显示名称和完整 spec 进行不区分大小写的子串 / 顺序模糊搜索。没有
「设为默认」：composer 里切模型或 thinking 即记住；server 把上次选用的模型写入
`models.json`，thinking 只保留在会话与当前标签页 composer。冷启动采用服务端默认，再回退到
当前目录首项，不读取浏览器旧模型记录。页签和主按钮与对话页同一套 tab / 主按钮样式。

供应商页的外层不滚动，左侧供应商列表只显示名称，有凭据且启用的排在前面，与右侧连接、
凭据、模型编辑区分别独立滚动，新增供应商和使用「编辑」打开的模型高级 JSON 都用二级弹窗。
模型高级 JSON 编辑保留 `input` 等能力字段。供应商目录通常只在本机维护；Magpie 支持设置页
显式刷新，浏览器经同源 Ki API 请求，由服务端读取 Magpie 目录，不直接向浏览器暴露 provider
key。Base URL、API 协议等表单控件共享尺寸和排版；API 协议与 thinking effort 共用 ARIA
combobox/listbox 组件，支持方向键、Enter、Escape，并按可用空间向上或向下展开。

#### 扩展 OAuth

供应商页对 `auth.type=oauth` 的 provider 显示 Browser login、Device code login 和 Logout；
登录进度通过同源 `/v1/providers/{id}/auth/{requestId}` 轮询，页面只显示授权 URL、设备码和
脱敏错误。Browser flow 还允许粘贴 redirect URL/code，适用于端口转发；不会在页面中打开外部
窗口，也不会要求用户填 access token。

### Info

本会话元数据之后依次是 Agents、Processes 两节，再列 skills、extensions、slash 命令；内容右侧
提供 sticky outline。运行状态不再占据 composer 上方。

Agents 按 canonical task path 显示可折叠父子树（恢复时缺少父节点，挂到最近已知祖先），保留
已完成任务的状态、轮次、当前工具和统计；点击名称进入该 agent 的 Conversation。

Processes 按 owner session 分组为树，只列运行中的进程，命令/owner 可跳转到对应会话，输出按
需展开；停止进程按实际 owner 请求，中断 agent 按其 session 请求。

两节为空时仍保留标题与空状态，实时 SSE/恢复快照更新状态，移动端操作目标至少 44px，长任务
名、命令和路径换行不撑宽页面。

每个 extension 下列出它加载的 skills、tools、slash 命令、prompt append 文件、PATH 目录
（标记已存在 / 缺失）和 providers。`path` 只展示字符串，不当 `href`。来源 session
（`parentSessionId`）渲染为可点链接，点击切到该 parent 会话。subagent 子会话在侧栏内联
展开，本页不再有独立的 Tree 浏览器。Reload 清资源快照，复用全局 extension sidecar。Edit
打开设置。不在此页开关。标题按 h2 / h3 / h4 分层。

末尾是完整的 system prompt：随 `fields=runtime,system` 取回该会话最近一次 `request_header`
实际发送的 `system` 原文，用等宽、可滚动的代码卡片原样展示（带行数/字符数与复制按钮），
从未运行过的会话显示空状态；不重新渲染，所以与模型收到的内容逐字节一致。

### 上下文仪表盘

Context 是独立仪表盘，不挂对话 composer，替代原 WebUI Trace。

#### 概览

顶部显示 session 用量、缓存、费用和轮/步统计。Current Context 用分类堆叠条显示当前已知
占用；Trend 支持 Step / Turn、Total / Delta，点击步骤联动 Context Browser。

Current 的 System 来源默认展开；趋势置于双栏上方，窗口随可用宽度缩放、最多 24 步，保持
44px 触控目标；可向前/后翻页并定位历史选中步骤。主图是彩色分类堆叠柱，纵轴仅按分类估算
取值；provider 报告输入/仪表用量移到下方独立折线与刻度，不能以巨大报告值把彩色分类压成
细线，也不能按报告用量比例虚构分类。Delta 的分类增减和同口径报告变化分别展示，缺失值不
当作零。首个跨过 local/remote checkpoint 的模型步骤带压缩标记。

#### 分类

Browser 按 System、Tool Schemas、人工输入、代理消息、扩展消息、Assistant、Tool Results、压缩
摘要和 Remote Context 分类，不设笼统的 Injected Context。System 根据 Ki 固定结构继续拆为
内置规则、项目 AGENTS/CLAUDE、APPEND、扩展指令、Skills 目录、环境等；历史中未包裹的
operator APPEND 无法证明全局/项目来源，合并显示并明确注明未知，不用当前设置替代历史。
Skill 正文若由 read 加载仍归 Tool Results，仅添加来源标签，不重复计数。

分类色板统一用于柱子、构成条、图例、Browser 与事件：Human inputs 用珊瑚红，Tool results 用
青色，Agent 用莓紫、Extension 用棕色、本地 Compaction 用橄榄绿，拉开与 System 紫色、工具定义
琥珀色的差异。分类标签始终保留，不只依赖颜色辨认。

本地摘要归 Compaction；远程加密检查点归 Remote Context，token 大小未知，不伪造本地摘要。
跨过检查点后原消息被摘要/不透明内容覆盖，Agent 等分类为空不代表历史没有代理消息；Current
和 Browser 明确解释，并可选择压缩前的请求浏览当时分类。

#### 数据口径

当前版本是可视化重建，不是最终 provider 请求的实录：`request_header` 记录 system/tools，但
之后的 `before_provider_request` 仍可改写消息、工具和模型，原生编码器还会过滤、补配对和
转换消息。因此所有重建明确标为 approximate，历史请求取其响应之前的分支，不把响应混入本次
输入。

分类 tokens 为文本/JSON 估算；provider usage 与现有 `context_usage` 独立显示，不把分类之和
冒充 provider 精确计量。缺失的 parent、未加载/截断正文、多模态和不可展开 remote context 必须
有覆盖提示。Current Context 表示当前已知状态，不保证下一请求，打开页面不执行扩展 hook。

#### 估算与正文

历史 slim/index 附带由完整持久化内容派生的 `contextEstimate`（System、tools、message、local
summary 独立数字）；正文没加载不等于分类估算为零。前端复用这些数字绘图，展开内容仍走既有
按需 hydration，不下载整份历史以画柱子。缺少数字的旧载荷保持未知；已知零与未知区分。数字
覆盖 `estimatesPartial` 与正文覆盖 `partial` 分开，完整数字不表示已拿到完整正文。远程加密
内容和非文本 token 开销不估价；这些字段不是 provider 精确计量或请求实录。

复用 session `fields=index` 和 `entry`/`entries`，不增加 Context REST 路由，也不在页面打开时
下载整份正文。未变化 header 的省略字段从此前 header 继承；历史来源不可恢复时保持未知。CLI
`session trace`、`view=trace` 和 telemetry 不受 WebUI Trace 删除影响；共享请求/执行 records
继续支持统计和实时覆盖。

#### Browser 与定位

Browser 初始每类最多呈现 40 项，详情按需挂载。内容详情支持搜索、Raw / Markdown 和按需
补全，按持久化 entry 身份定位回对话。

System/schema 来自请求 header，没有对应聊天行，不提供对话定位；工具结果的持久化 entry ID
则先映射到聊天行的 toolCallId，再沿既有分页和滚动控制器定位。Events 的压缩定位使用
checkpoint 原 ID；不单独渲染的 model-change 定位到同分支最近的前置消息/压缩行（没有前置行
时取首个后置行），绝不默认落到末尾。

导航只以已提交的实际 chat nodes 判断可达，原始分页里的 metadata ID 不能证明存在聊天行；
Context 切回 Chat、等待历史页时从 reading 意图挂载，避免异步加载期间先贴尾。桌面为卡片
双栏，窄屏为单栏，导航和控制至少 44px；大正文只在展开后挂载，不影响聊天首屏。

#### 工具调用排行

Session Context 在趋势图上方单独展示工具调用排行，统计当前分支的全部已知调用，不随 Browser
选中的请求改变。按调用次数降序排列，同次数按工具名排序；每个工具一条横条，长度相对调用
最多的工具统一缩放，最左侧红段表示失败次数，其余为非失败调用（包括尚未完成的调用），并
明确显示总调用数和失败数，不只靠颜色传达信息。

使用完整历史 index 的轻量元数据和实时调用记录，不为排行下载工具正文；index 未加载完成时
标记统计范围不完整。调用按 call ID 去重，Code Mode 内层调用独立计入对应工具。默认展示前 8
个工具，按需每次增加 20 个；窄屏保持名称可换行、计数可读，展开按钮满足触控尺寸，沿用主题
色和错误色。

## 扩展 UI 壳

WebUI **不加载扩展 JS**，不 `window.open`，不按扩展名写死控件。每个扩展只投一份投影，壳按
同一套布局渲染。扩展文案由扩展包自己的 `extension.json -> i18n.resources` 提供，Host 只
读取、校验并随 catalog 转发；WebUI 只负责按当前浏览器语言解析通用 `UIText`，不认识任何
扩展 key。

### 面上有什么

| 面 | 数据 | 行为 |
|---|---|---|
| 全局 extension chip | `/v1/extensions` 中启用且有 global UI 或配置的扩展 | 与当前 session 无关，初始页面也显示在 `title-row` 右侧；点击打开统一扩展 Modal，并定位到该扩展。runtime 状态用于 chip tone。Extensions 设置里每个已启用扩展的 Configure 打开同一 Modal |
| 顶栏 status chip | `status` | 只在 `title-row` 右侧。无 `status.text` 则无 chip。tone：`info` / `active` / `success` / `warning` / `error`。按 tone（error → warning → active → success → info）再按扩展名排；最多 4 颗全展示，超过则留 3 颗加 `+N`。右侧始终有展开钮。窄屏藏单颗 status chip，只留「扩展 · N」 |
| 统一扩展 Modal | global `ui` + session `extensionUi` + 全局 config | 点全局 chip、status chip、展开钮**或** Extensions 设置的 Configure 打开同一 Modal。左侧导航列出全部已启用扩展（窄屏改成顶部横滑）；右侧显示当前扩展的 global/session UI 或配置。标题下独立红条显示完整的 error status、runtime/manifest 错误，切换详情/配置时保留，长路径换行、超长错误纵向滚动。global panel 只读；选中 session 后，同名 session UI 覆盖 global UI。关 Modal **不**卸 chip，也不 `clearPanel` |
| 确认 / 选择 | `prompt` | 叠在详情 Modal 上。120s 或 abort = 取消 |
| slash | `commands[]` | 扩展命令 `source=extension`，`argumentHint` + `completions` |
| 气泡 origin | user `origin` | `extension:<name>`、`agent:<task-id>` 与用户气泡可区分；非人类来源使用虚线框 |
| 就绪锁 | `runtime.ready` | 未就绪不能打字；避免 `/goal` 尚未注册就 404 |

不要在 tabs 下、composer 上再做第二条 status 横条。全局 chip 不依赖 session；global config、
global UI 与 session 的 `status` / `panel` 在同一个扩展 Modal 中呈现；这些 UI 投影**不进
jsonl**。Reload 后 sidecar 按自己的状态重新发布 global/session UI。

### 投影

`GET /v1/extensions` 的每个 extension item 可带 global `ui`；`GET /v1/sessions/{id}` 的
`extensionUi[]` 是 session 投影（每个扩展一条）：

```json
{
  "extension": "goal",
  "status": { "key": "goal", "text": "Goal · active", "tone": "active" },
  "panel": { },
  "prompt": null
}
```

实时更新使用每 tab 一条 `GET /v1/events` push SSE。服务端每 15s 发注释心跳；`readSSE` 的
45s 读空闲守卫在前台检测停滞并结束流，隐藏时不因空闲自行重连。`visibilitychange` 回到可见、
`online` 和 bfcache 的 `pageshow(persisted)` 是独立的恢复信号：push 立即换连接并清除旧退避；
当前会话取消旧 run reader，读取现有 `GET /v1/sessions/{id}` 的 tail（遵守 detailed / compact
偏好），合并已有历史后按 running 决定是否从已应用游标续听。恢复不依赖隐藏时长或是否收过
心跳；`ready` 也执行正文协调，不能只取 `fields=runtime`。idle 快照清理错过终态事件留下的
partial、工具运行标记和 stopping；running 快照保留现场并续传，绝不重发 prompt。切会话/分支
使旧响应失效，网络失败保留当前正文并退避重试。`pagehide` 关闭 push/run 两条订阅，等待
`pageshow` 恢复，不能在缓存期间重新打开连接。列表与扩展照常刷新，列表走 ETag，完成通知
补发走已知 running 集合。短暂网络失败不弹浏览器原始报错，HTTP 错误仍提示。

该流推三类帧：

| event | 何时 | 客户端动作 |
|---|---|---|
| `ready` | 订阅建立（含重连），带 `serverId` | 同实例重取列表 + workspaces + models/meta + extensions，并协调当前会话的正文 tail 与 runtime；新实例撤销旧页面运行态并重新检查认证 |
| `invalidate` + `scope` | `sessions` / `workspaces` / `providers` / `extensions` 变了 | 重取对应 REST（`GET /v1/sessions` 带 ETag，未变为 304）；`sessions` 还会把「当前打开的会话」与列表对齐：变 running 就接上 run 流，已不存在就退出该会话。`extensions` 额外重读当前会话的 `fields=runtime` |
| session sideband（带 `sessionId`） | 见下表 | 只处理 `sessionId` 等于当前会话的事件；`agent_end` / `run_aborted` / `question_requested` 例外，用于后台完成与新问题的系统通知 |

SSE（带 `sessionId`，不进 occupy 回放）：

| event | 何时 |
|---|---|
| `extension_ui_updated` | status / panel / prompt 变了 → 客户端再 GET |
| `extension_notice` | 非错误 toast（`reason` = `info` / `warn` / `error`） |
| `extension_error` | sidecar 失败；可带 Reload |
| `runtime_ready` | 该 session 打开时的 Prepare 结束（失败也算） |
| `compaction_start` / `compaction_end` | 手动 `/compact` 的进度（同步请求，没有 run 流） |
| `queue_changed` | 队列变化 |
| `run_aborted` / `agent_end` | 中止 / run 结束；`agent_end` 不带 `messages` |

用户点面板：`POST /v1/sessions/{id}/extension-ui`

| `kind` | body | Host → sidecar |
|---|---|---|
| `action` | `{ extension, value }` | `ui.action` `{ id: value }` |
| `submit` | `{ extension, fields }` | `ui.submit` `{ fields }` |
| `confirm` | `{ extension, ok }` | 解开 `ui.confirm` |
| `select` | `{ extension, ok, value }` | 解开 `ui.select` |

### Sidecar → Host（写投影）

| method | params | 壳怎么用 |
|---|---|---|
| `ui.setStatus` | `{ key, text, tone }` | 顶栏 chip。`text` 空则去掉该扩展 chip |
| `ui.setPanel` | 见下文 | 详情 Modal 的内容 |
| `ui.clearPanel` | `{}` | 清 panel，chip 仍在（若还有 status） |
| `ui.confirm` | `{ title, message }` | 是/否；result `{ ok }`；**120s** |
| `ui.select` | `{ title, options[] }` | 点一项；result `{ ok, value }`；**120s** |

Host **不解析** panel 里的业务字段。扩展自己决定列哪些 action、何时 `disabled`。

### ui.setPanel

```json
{
  "title": "Goal",
  "sections": [
    {
      "heading": "Details",
      "items": [
        { "label": "Status", "value": "active" },
        { "label": "Turns", "value": "1 / 25" }
      ]
    }
  ],
  "fields": [
    { "id": "objective", "label": "Objective", "type": "textarea", "value": "说你好" }
  ],
  "submitLabel": "Update",
  "actions": [
    { "id": "pause", "label": "Pause" },
    { "id": "resume", "label": "Resume", "disabled": true, "title": "Nothing to resume" },
    { "id": "clear", "label": "Clear", "style": "danger" }
  ]
}
```

| 字段 | 壳 |
|---|---|
| `title` | Modal 标题 |
| `summary` | 只读摘要卡片。和 `fields` 里已有的值不要重复；目标正文放 field，空状态才用 summary 提示 |
| `sections[].heading` | 小节标题 |
| `sections[].items[]` | `{label,value}` 属性表；`label` 也认 `key` / `name` |
| `sections[].kv` | 对象展成属性表 |
| `sections[].markdown` | Markdown（无 items/kv 时） |
| `sections[].text` | 纯文本 |
| `fields[]` | 可编辑。`type`：省略=单行，`textarea`，或 `options[]`=select |
| `submitLabel` | 有 fields 时主按钮文案，缺省「提交」。点了走 `ui.submit` |
| `actions[].id` | 点了走 `ui.action`，只回这个 id |
| `actions[].label` | 按钮文字 |
| `actions[].style` | `danger` / `primary`；其余次按钮 |
| `actions[].disabled` | 显示但不可点 |
| `actions[].title` | tooltip（为何 disabled） |

渲染顺序：status chip → summary → sections → fields → 底栏 actions + submit。

### 文案本地化

扩展需要本地化 WebUI 固定文案时，可在 `title`、`summary`、`status.text`、section 的
`heading`/`label`/`value`、field label、action label/title 和 `submitLabel` 中发送原始字符串，
或发送扩展自有的 `UIText`：

```json
{ "key": "status.connected", "params": { "count": 2 }, "fallback": "Connected" }
```

壳按以下顺序查找翻译：当前语言、当前语言的 regional/base 变体、扩展 `defaultLocale`、`en`，
最后使用 `fallback` 或 key。`params` 使用 `{name}` 形式插值。普通用户输入、业务正文、field
的提交值和 `options[]` 的提交值仍直接发送原文；`options[]` 是业务值而不是 host 文案。扩展
没有必要绑定某一个浏览器语言，缺失 locale/key 只会触发上述回退。

扩展配置页的扩展描述同样使用 `manifest.description` key；由 `x-ki-ui` 渲染的字段、提示和
选项通过扩展自己的 i18n catalog 解析文案。Host 通用的关闭、提交、配置项标签、空状态等壳
文案仍属于 WebUI 自己的 `i18n.tsx`，不得写入 `ext.<extension>` 或 `cfg.<extension>` host
key。

### Sidecar ← Host（用户点了）

| method | params |
|---|---|
| `ui.action` | `{ id }` 对应 `actions[].id` |
| `ui.submit` | `{ fields }` 当前表单（id → 值） |

需要二次确认用 `ui.confirm`（例如 Clear），不要在 SPA 里写死文案。

slash 子命令不必和按钮一一同名；面板是控制面，slash 是输入面。goal 的做法：pause / resume /
clear 做 action（不能用的 `disabled`），status 用 sections，edit/start 用 field + submit。

## 同步与恢复

`SessionSyncController` 统一协调重新进入（`visibilitychange` / `pageshow` / `online`）、push
ready 与 run EOF，经 `TranscriptStore` 原子接纳后投影；运行状态与正文按身份合并，SSE/已接受
的新输入推进 revision，旧 idle 响应不能结束新工作。请求导航的滚动落点与视口高亮因此始终
落在真实存在的用户气泡上。渲染列表由 `ChatRenderItem` 驱动，虚拟列表按 item（而非 node）
计数。

### 历史协调与统计不变量

- **提交不依赖 React 调度**：`TranscriptStore` 是同步、React 无关的状态/响应接纳边界，React
  经 `useSyncExternalStore` 订阅不可变快照。`SessionSyncController` 独占 run reader、恢复、
  退避和已应用游标；`TranscriptRequests` 独占历史/正文/index/投影请求的去重、取消与状态。
  组件 updater 不确认网络工作，不再维护第二份分页边界。
- **身份缓存不等于可见窗口**：缓存可保留不连续的旧正文；只有当前分页范围渲染。恢复后若新
  尾部与旧窗口断开，以新 `oldestId` / `hasMore` 重新开放缺口；不能沿用旧窗口的「已到
  最早」，也不能用整树 index 或 SSE 临时阅读桥掩盖正文缺页。连续性必须回溯到原分页边界，
  最新 leaf 已被 SSE 收到并不能证明此前历史连续。连续窗口的普通刷新保留阅读位置和已推进
  的边界。detailed → compact 的在途投影也绑定发起时的边界和 leaf；恢复换窗后丢弃旧投影，
  改为投影新边界，不能把旧根游标写回来。
- **快照有明确边界**：compact 的 `entryCount` 是该轮快照覆盖的完整 entry 数，同一分支迟到的
  旧投影不能覆盖新 `tailId` 或计数；但权威尾部确认 leaf 在同一人工 turn 内回退/分叉时，必须
  接受新分支更短的摘要。跨轮稀疏连接必须同时满足相邻轮次及当前 `parentId` 等于前轮
  `tailId`；轮次数字相邻不能证明属于同一分支。恢复后不在已确认活跃路径上的旧摘要不参与
  连接和统计，正文身份仍保留缓存，分页可恢复正确摘要。单条隐藏正文补全但其父仍缺失时，
  继续沿该快照原有的可见前驱回溯，不能因补全反而丢掉输入气泡。
- **完成事件立即进入身份图**：`message_end` 的 `entryId` / `parentId` 是持久化事实。SSE
  定稿立即更新本地 entries 和活跃 leaf，索引里已出现该 id 也不等于 leaf 已经走到那里。父
  metadata 暂缺时只使用本地阅读桥，不伪造持久化 parent、更不能让省略正文的 frontier 形成
  自环。
- **执行状态独立于正文加载**：工具状态按 toolCallId 覆盖 node 和 trajectory record；普通正文
  补全不能结束任务，也不能丢掉耗时、错误或输出。服务器已落盘结果优先；后续模型步骤证明
  之前整批工具已经结束。
- **统计与显示解耦**：快照与本地重叠按身份去重，重叠重建共享索引并只遍历该轮。keep=0、
  展开、收起不改变总数和计时。`cumulativeElapsedMs` 是冻结的历史前缀，当前轮延长不能从
  过去耗时里减掉；新 prompt 确认 ID 后仍从该 prompt 的服务端时间戳起算。
- **折叠计数只数回复节点**：`hiddenCount` 包含被折叠的 assistant、按调用 ID 去重的 tool 和
  runtime user 消息，不含 compaction、取消行及 aborted assistant。keep 只数正常
  assistant/tool，不能将「可折叠」与「占 keep 名额」混为同一分类；可见区从第一条保留/运行中
  的真实回复起，旧通知不会刷屏。`visibleNodeIds` 可含生命周期元数据，因此快照总数和重叠
  扣除按可折叠分类统计。无人工输入 turn 的隐藏 runtime 指令锚点只计一次。刷新、逐条补全和
  展开不能制造额外折叠条目。

### 统一身份、载荷与响应权限

`transcriptIdentity` 定义唯一的人工 turn 归属。`origin=agent` / `agent:*` 的 directive、通知
仍保留 provider 所需的 user role，但只是 runtime 回复，不新增人工轮；真人中继的
`extension:*` 仍是人工请求。首个人工 input 前的 prelude 归入该人工轮；没有人工 input 的
runtime-only 或 assistant-only 分支使用稳定的第一组身份。Chat、Context、请求导航与统计都
使用相同 TurnId，数字轮次只是显示值。通知不重置计时起点，不凭自己的到达时间延长已经完成
的模型工作；assistant-only 历史没有 user 起点时使用真实步骤时长。

浏览器为提交生成 `clientRequestId`，HTTP 接受结果、accepted SSE、正式 message 与队列提升保留
该 ID；内部消息由 server 生成。只有明确的请求 ID 确认关系能合并 optimistic 与 canonical
消息，不用文本相等判断身份。接受新的输入也推进 revision，不能被之前发出的 idle GET 清掉；
重复确认同一请求不复活 busy。

运行中 subagent 通知采用中性的 runtime note（12px 圆角、细虚线边框和随明暗主题适配的淡
背景，整张通知只保留一层边框，正文不再套气泡框；标题/来源/正文保持正常流式布局），不提供
人工编辑/分支按钮，不把机器输入短暂显示成真人请求。

Agents 使用无卡片底色的简单树形列表：所有节点（包括叶子）共用 44px 展开图标列，子层级统一
右移 24px，细连接线在末个子节点处收尾；状态用次要文字，统计与名称左边界对齐，与名称相同
的描述不重复显示。展开和会话跳转使用独立按钮，窄屏深层树仅限制视觉缩进，不改变父子关系。
截图与几何回归覆盖 root → 带子任务的 child → leaf，防止叶节点左移。

`transcriptCoverage` 区分浏览器规范化的 `index/helper/slim/full` 载荷。完整 ID 链不等于节点
结构完整，尤其 compact helper 可以省略同一 assistant 内的正文和兄弟调用。结构完整的 slim
turn 可以展开节点，全文仍按需加载；helper 不能证明完整。用户展开意图、节点结构覆盖、全文
缓存和当前分页范围各自独立；完整正文不会被迟到 slim 降级。

每个请求捕获本地 session epoch、确认快照的 frontier generation 及必要边界；这些本地数字是
撤销能力，不冒充服务端分支 ID。真实分支仍以 canonical parent/leaf 和 compact tail 为依据。
重新打开同一个 session 也撤销旧 epoch。

| 输入 | 接纳依据 | 可修改范围 |
|---|---|---|
| tail / 恢复 | 当前 epoch/frontier；SSE/新输入 revision 决定运行状态新鲜度 | 确认 active leaf、窗口/摘要与 runtime；撤销旧投影权限。idle 响应与已提交 busy 不一致时继续确认，不能退出后留下无人监听的 busy |
| older page | 当前 epoch/frontier 且捕获的 oldest/hasMore 未改变 | 本窗口历史及游标；陈旧页不能夹带摘要污染新分支 |
| compact 转换 | 上述边界加 captured leaf；响应各 turn tail 已在已知 canonical 活跃路径上 | 当前分支展示投影；未知/sibling tail 必须先走权威恢复，不能自行选分支 |
| keep 重投影 | 当前 epoch/frontier、同一 TurnId 与 tail | 同一快照的可见节点；失配至多合并一次恢复并有限重试 |
| turn 展开 | 当前 epoch/frontier、目标 tail 未改变且响应包含它 | 整轮节点一次提交；不把其它分支的下载标为当前 turn 完整 |
| index | 当前 epoch | 树身份元数据；不能改正文覆盖、active leaf 或分页游标 |
| body | 当前 epoch、canonical entry ID | 正文补全；没有选择分支、确认执行终态或推进游标的权力 |
| run SSE | 当前 reader/打开作用域；先还原 wire patch，再原子提交 batch | 已观察的执行/消息事实；提交后才推进 ACK |

普通恢复保留已应用 ACK；重新打开会话会丢弃 transient 投影，因此也撤销其 ACK，必须重放仍在
执行的 partial/tool start。`pagehide` 关闭 reader 并取消尚未完成的初始打开，push hint 或迟到
的 GET 不能自行解除暂停；显式 resume 才重连。unmount 使 controller 失活，晚回调不能重新
启动连接。读取失败保留已有内容，header 提供本地化同步/重试状态；它不替代消息列表中持久的
取消红条。

### 子任务活动

侧栏 `running` 仅表示自身 run。`activeDescendantCount` 独立表示仍在工作的有效 tree 后代；父
自身 idle 而子任务工作时显示委派活动灯和「等待子任务」文案，折叠子树也能看见。它不把父
composer 伪装成 busy，不错误显示父 Stop。计数随既有 sessions invalidation/GET 刷新，含
zh/en 与完整可访问文本。

### 呈现几何

工具、轮与会话的 `Duration` 共用左对齐、正常字号的等宽数字槽：宽度按当前格式所需位数预留，
同格式 tick 不抖动，仅单位格式或天数位数变化时调整，不为普通秒数留出天数的空白。短耗时
保留小数，分钟/小时/天仍保留秒，不通过舍弃精度或缩小字体消除跳动；精确毫秒有本地化
title/可访问名称。

虚拟行高度缓存只存正文对象的弱映射数字 token、有限呈现信息与几何，不强引用正文/图片而绕过
body 缓存预算。工具详情/通知的局部展开与 hydrate 在 `TranscriptRow` 的 layout 测量上下文
提交；Markdown 的源码→格式化局部状态也必须加入同一次绘制前量测，不能因为父消息身份未变化
而等下一帧 ResizeObserver 补偿。计时 tick 不重测所有行。reading/seeking/following 的控制者
只有 `useTranscriptScroll`，任何组件都不能因几何靠近底部自行切回 following。

## 渲染性能

### 虚拟列表

对话滚动几何统一由 React Virtual 3.14.12 / virtual-core 3.17.10 管理；`anchorTo: end` 在提交
新数据时按当前 item key 和内部偏移恢复位置。容器 `overflow-anchor: none`，避免浏览器再补
一次。

行高按实际文本列宽、正文身份和呈现 revision 校验，保留小数，LRU 2000；未测量行按字符数
估算，占位不进入缓存。首次量测补偿视口上方的估算误差，重新量测只补偿上方内容；prepend
后按原内容 key 判断「上方」，包括原先位于顶部留白中的视口。

量测仍由同一个虚拟实例提交；WebKit 不能立即写入的触摸/原生 wheel 修正由同一 viewport owner
保留为逻辑偏移与视觉平移，静止后一次转移，不能只延后 `scrollTop` 而先移动内容。没有业务侧
逐帧 `scrollTop` 循环。overscan 为 4。

运行占位的底部留白变化不属于行高增量；`useTranscriptScroll` 在 layout 阶段仅对既有 following
意图调用同一虚拟实例的 `scrollToEnd`，保证消息结束后的占位与格式化也贴尾，不重新开启
reading/seeking 的跟随。

### 跟随意图

`useTranscriptScroll` 独立拥有 following、reading 与 seeking 意图。

- 向上 wheel、touchmove、PageUp/Home/ArrowUp 或滚动条上拖立即暂停，包括仍距底部不足 80px 的
  情况。内部代码/工具滚动区能消费手势时不影响聊天。
- 仅「回到底部」或主动向下滚到末尾并停止后恢复；几何接近底部、流式输出、量测和分页都不能
  自行恢复。
- reading 在适配器内同时关闭 append follow 和 resize pinning（本版本非负距离用
  `threshold=-1` 关闭），软键盘/视口高度变化时 following 保持末尾。
- 正文/折叠/局部展开变化在 layout 阶段量测已挂载行，滚动时也通过虚拟库的 `resizeItem` 提交
  真实高度，不依赖其滚动中跳过的 DOM 量测；程序化跳转同步发布虚拟范围，避免先画旧范围而
  出现空帧。局部展开通过 `TranscriptRow` 的测量上下文接入同一提交；计时 tick 不触发行列表
  重测。

经典滚动条的空间从一开始保持稳定，避免展开工具时列宽突然减少、计时字段移动；WebKit 对
styled scrollbar 的 stable gutter 不完全生效，因此同时保留 `overflow-y: scroll`。使用原生
overlay scrollbar 的平台不会因此多占一列空间。

### 平台差异

桌面 WebKit 的原生 wheel 动画会被量测补偿的 `scrollTop` 写入中断。不能为此停掉向上滚动的
补偿：这种尝试在既有 revisit 预算中产生了 122px 漂移（上限 80px），已经撤回。

该引擎的可信 wheel 与 iOS reading 状态的 touch/惯性滚动共用同一个逻辑偏移差，同时用于行
平移和物理/逻辑坐标映射。iOS 在公开的 resize predicate 接管量测修正，在同次提交更新逻辑
offset 与可见区，不再让 virtual-core 同时排队一份私有修正；合成 wheel 仅改变阅读意图，随后
的显式 `scrollTop` 不是原生惯性。手指离开并静止 180ms 后，在绘制前一次转移到物理偏移：
正差先增长 sizer，负差先写入更小的 `scrollTop`，避免浏览器偷偷 clamp 导致双重位移。新的
手势、read/latest 导航与触顶都由同一个所有者结算或终止偏移；无效 no-op 重试和旧 scrollend
不得抢回手势。Chromium 继续走库的普通补偿路径。

### Markdown 流式

流式正文由 `MarkdownDocument` 保留增量 scanner：只归一化新增文本、扫描新完成行，保留跨
chunk 的 CRLF、围栏、列表、引用和 HTML 状态。

每个安全边界累计 ≥4096 UTF-16 code units 时封成稳定 segment；跨段引用定义出现时整篇回退，
重写则 reset。活动区 ≤4096 时先提交完整文本、再一次性允许格式化，后续小尾部复用增量语义
splitter，不随新 delta 重置 admission；更大的未闭合块先显示完整源码（按 4096 code units 复用
文本节点，不拆 surrogate pair），定稿后进入格式化队列。完整列表、表格、围栏不被任意切断。

活动片段封存时在同一个兄弟数组中沿用原 key，新尾部获得独立的文本优先 admission；流式期间
从未进入视口的封存段保留完整源码，进入视口或运行结束后才放行格式化。已封存段及活动消息
的 React key 在 `message_end` 与 tail 协调后继续复用；持久化 entryId 仍用于分页、去重、分支
与导航。长尾格式化可能改变行高，现有 following/reading 几何补偿保持生效；不承诺任意巨大语义
块都能在一帧内完成解析。

格式化排队时立即显示完整源码，共享队列按可见距离、活动尾部优先级和等待时间排序；格式化
状态更新使用 transition 优先级，不能抢占正文显示。每次放行一个解析任务，观察到下一帧的
耗时超过 32ms 时额外让出一帧。卸载取消任务，格式化不清空已显示正文。不可安全拆分且超过
64K UTF-16 code units 的单块显示完整源码，用户可点「格式化 Markdown」。块缓存受 300 条和
约 8 MiB（UTF-16 近似计费）限制。大语义块直接交给 Markdown renderer，避免再次经过可能退化
的块切分器。

### 运行事件批处理

API 按顺序还原每个 wire patch，再交给 `streamBatch`，仅合并同一 run/messageStream 的连续
`message_update`。一个 `requestAnimationFrame` 时钟负责显示，100ms 定时器兜底；新输入不重置
deadline，积压 128 个事件或 100ms 立即追赶，`message_end` / `agent_end` / `run_aborted` 立即
flush。游标在 `TranscriptStore` 同步提交该批事件之后推进，不能跟随 socket 或动画推进。

SSE 解码每 128 个事件或约 4ms 主动让出一个真实 task，使单次大 read 的重放不会耗尽渲染机会；
UTF-8/CRLF 跨 read 保留，非法 JSON 终止连接并走恢复流程。断线先协调 session tail，失败保留
partial 与 busy，重连指数退避并带 jitter（250ms 起步，最多 10s），使用已应用的
Last-Event-ID，绝不重发 prompt。服务端已完成 replay 最多保留 2 分钟 / 全局 16 MiB / 256 项；
过期或预算淘汰返回 410，沿同一恢复控制器读取持久化 session，不以空流结束代替权威结果。

### 自检

`localStorage['ki-perf-hud'] = '1'` 后刷新，会话右下角显示 fps、最差帧、long task、内容漂移、
挂载行数、`scrollTop`，以及流式提交 p95 和待显示内容最大年龄。仅开 `ki-stream-metrics=1` 可
采集而不显示 HUD；有界保留最近 240 次可见消息提交，不记录正文。

计时起点是 patch 解码完成，终点是真实正文的 layout effect，不冒充网络延迟或屏幕 paint；测试
再用 rAF 检查 revision 与实际语义标记。聊天的 `data-scroll-intent` 标 following/reading，
`data-anchor-key` 标翻页锚点。WebKit 不支持 longtask observer 时保留其它指标。

## 复制的可用性

复制按钮统一走 `web/src/lib/clipboard.ts` 的 `copyText()`：浏览器只在安全上下文（HTTPS，或
`localhost`/`127.0.0.1` 上的 HTTP）暴露 `navigator.clipboard`，而 WebUI 经常通过主机名以明文
HTTP 访问（Tailscale/LAN 地址等），此时该 API 不存在。`copyText()` 在非安全上下文回落到
`document.execCommand('copy')`，保证任何访问方式下复制都可用。

## 响应式与交互

WebUI 以动态视口为边界，不假设固定桌面尺寸。

### 断点

| 条件 | 布局 |
|---|---|
| 桌面 | 保留可折叠侧栏 |
| 宽度 ≤ 900px | 带 scrim 的抽屉导航，主区常驻打开按钮，抽屉关闭时自身为 `inert`，打开时主区为 `inert`。顶栏只显示一颗「扩展 · N」聚合入口，扩展、Provider 和设置内部导航改为横向可滚动列表，不能用 0 宽网格轨道隐藏仍在绘制的侧栏 |
| 宽度 ≤ 760px，或高度 ≤ 540px 的横屏 | 完整移动布局：设置、扩展、附件和目录占满可用视口；header、可滚动 body、sticky footer 各自分层，不得让内容画到 footer 下方 |

平板可以保留居中弹窗，但内部必须使用与 compact 壳一致的单列或横向导航布局。目录的 Miller
两列在手机上纵向排列并各自滚动。

Hero 等单列网格必须用 `minmax(0, 1fr)` 约束轨道，否则隐式列按最大内容（如长模型名）撑宽，
窄屏出现可拖动的横向滚动；长模型名在 composer chip 内省略号截断，不得改变布局宽度。

Provider 新建与高级模型弹层固定头尾、只滚动字段区。

### 视口与软键盘

页面高度使用 `dvh`，浮动菜单同时读取 `visualViewport`，四边 padding 合并
`safe-area-inset-*`。

软键盘遮挡由 `useKeyboardInset` 统一处理：可编辑控件聚焦时比较 layout viewport 与
`visualViewport` 的高度差，以 `--keyboard-inset` 发布到 `:root`；底部 composer、底弹 dialog
的 padding 与全屏 dialog（设置、扩展、附件、目录）的高度都减去该值。不扣减
`visualViewport.offsetTop`：不支持 resizes-content 的浏览器靠平移视觉视口露出聚焦字段，若把
平移量记作可见，inset 会归零、全屏对话框保持超出可视带的高度（用户只能拖着看）；收缩进
可视带后由 reveal 把平移收敛回 0。Chrome 的 `interactive-widget=resizes-content` 已同步缩小
layout viewport，差值自然为 0，该机制只在 iOS Safari 等平台生效；页面缩放（scale > 1）时
交给浏览器自行平移。

浏览器原生 reveal 在固定高度外壳与嵌套滚动区（dialog body、消息列表）下会滚错容器或对齐到
仍在动画中的可视带，因此每次视口 resize 都对落在可视带外的聚焦字段重做
`scrollIntoView({ block: 'nearest' })`（配合聚焦后 300/600ms 的动画收尾复查）收敛到可见；
内部滚动器不触发 visualViewport 事件，用户主动滚离聚焦字段不会被拉回。

聚焦且可视带低于会话最高 layout 高度 75% 时置 `html[data-keyboard="open"]`，移动端设置/供应
商页的章节标题区据此隐藏，把空间让给表单。进入消息编辑时 textarea 聚焦并把光标移到文末
（程序聚焦默认把光标放在 0 处），其最大高度同样按 `visualViewport` 而非 `innerHeight` 计算
并随键盘开合重算。

### 触控与焦点

移动端可编辑的 input、textarea、select 字号至少 16px，避免 iOS 聚焦缩放。主要触控目标至少
40px，列表行和关键导航目标至少 44px；关闭的抽屉和弹层不能被 Tab 聚焦。底部 composer 的
textarea 不显示品牌色 focus outline（不影响布局），其他可交互控件保留键盘焦点提示。

所有 dialog 均需 `aria-modal`、Escape 只关闭栈顶、Tab 焦点圈定和关闭后焦点恢复；嵌套
dialog 打开时，下层 dialog 同时设为 `inert` 和 `aria-hidden`，关闭后精确恢复其先前属性。
延迟初始焦点只在焦点仍位于该 dialog 外时补位；用户已经移到内部其它字段时不得再抢回首
字段，避免后续输入写错控件。

命令面板的 combobox 必须显式关联 listbox，且在焦点移交给 Select 或 dialog 时立即释放键盘；
侧栏操作菜单使用 menu/menuitem 语义，支持方向键、Tab、Escape，并在关闭后把焦点还给触发
按钮。

抽屉中的菜单打开时，Escape 归菜单所有，即使首个焦点帧尚未执行、焦点仍在触发按钮上；抽屉
的 window 监听器不能同时关闭父层，第二次 Escape 才关闭抽屉并回到主区触发按钮。抽屉触发
目录或设置 dialog 时，焦点直接交给 dialog，关闭后回到主区的抽屉按钮，不得回到已变为 inert
的侧栏节点。

Settings 和扩展 Details/Config 使用完整 tablist/tab/tabpanel 关系、roving tabindex 和方向键/
Home/End 导航，inactive panel 保留关联节点但必须 hidden。手机设置页签按当前语言的完整文案
计算宽度，水平内边距保留，触控目标至少 44px；超出视口时只滚动页签栏，键盘导航应把当前
页签滚入可视区。`prefers-reduced-motion` 下关闭抽屉和控件过渡。

### 回归覆盖

响应式回归覆盖 320×568、390×844、844×390、768/820 平板、1024×768 和桌面，逐页检查登录、
Hero、Chat、Context、Info、Settings、Provider、扩展配置、模型、命令菜单、选择菜单、附件、
目录和会话树；根页面不得横向溢出，header、composer、dialog/footer 必须保持在可视区内。

各 profile 会检查可见交互控件具有可访问名称；触控 profile 进一步扫描按钮、菜单项、链接、
输入、select、textarea 以及 checkbox/radio 的 label 命中区，不允许小于 40px（Chromium 的布局
盒子按 1/64px 舍入，所以断言用 39.95px 作为下限，避免 40px 控件量到 39.999999 时误报）。
dialog 内的 action 按钮同样固定 40px 最小命中区，不随 label 文本宽度收缩。长消息、分支切换、
排队操作、扩展开关等低频状态也遵守同一命中区契约。

## 构建

```bash
cd web && bun install && bun run build
cd .. && go build -tags embed -o ki ./cmd/ki
```

`web/dist` 是构建产物、不进 git（见 `.gitignore`）。必须用 `-tags embed` 才能把它嵌进二进制：
不带该 tag 时 `web/embed.go` 不参与编译，改由 `web/stub.go` 提供空 FS，`ki serve` 对 `/`
返回 503 提示。改前端后必须重新 `bun run build` 再编 Go。依赖用 bun 管理（`web/bun.lock`）。

`scripts/run.sh` 用输入 hash 判断前端有没有变：覆盖 `src/`、`public/`、`index.html`、
`vite.config.ts`、`tsconfig.json`、`package.json`、`bun.lock` 和 bun 版本；没变就跳过
`bun run build`，直接复用 `web/dist`（指纹存在
`web/node_modules/.cache/ki/web-dist-hash`，不放进 dist，因为 `//go:embed all:dist` 会把
dotfile 一起打进二进制）。`--force-web` 强制重建。跳过与否都始终带 `-tags embed`，避免把旧
前端资源嵌入新的二进制。

打包器是 **Vite 8**，默认用 **Rolldown**（`vite` 的依赖里是 `rolldown`，不再依赖 Rollup），
config、插件和 `vite` / `vite build` 命令都照旧。`@vitejs/plugin-react` 用 6.x，其 peer 要求
`vite ^8`。构建快了以后就不必再为重型依赖（mermaid）做预打包，`DiagramBlock` 直接
`import('@streamdown/mermaid')`。升级 Vite 或换插件后都要用 `bun run test` 和
`bun run test:perf` 验证。

类型检查独立于打包：`bun run typecheck` 开启 TypeScript 增量检查，缓存写入
`node_modules/.cache/ki/web.tsbuildinfo`，不进入 dist。开发时常驻 `bun run typecheck:watch`，
避免每次编辑都重新解析完整依赖图；首次检查仍覆盖全部源码。

纯逻辑测试位于 `web/unit/`，使用 `bun run test:unit` 或 `bun run test:unit:watch`，不构建 Go、
不启动服务或浏览器。`bun run test` 依次运行完整 Bun 单元测试和 fake 浏览器套件，是 CI 与 Go
E2E 共用的 WebUI 总入口。

## 测试

### 浏览器套件

假模型打通对话与上下文页（每个并行单元各起一个隔离的 `ki serve`）：

```bash
cd web && bun install && bunx playwright install chromium
bun run test             # 完整 WebUI：Bun 纯逻辑 + Playwright
bun run test:e2e          # 仅浏览器，并行 runner
bun run test:e2e:serial   # 单进程串行，便于定位单个失败
```

`bun run test:e2e` 由 `web/scripts/e2e-parallel.ts` 驱动：用
`playwright test --list --reporter=json` 自动枚举 `--project=fake` 的用例，再按文件声明的方式
拆成独立进程：

- `test.describe.configure({ mode: 'parallel' })` —— **一个用例一个进程**。这是文件在声明其
  用例彼此独立；加这行前必须先逐个用例单独跑通（`-g` 精确匹配单条标题）验证。
- `mode: 'serial'` —— 整文件保持单进程，顺序叙事不得拆散。
- 其余：超过 `KI_E2E_SPLIT` 个用例、且全部挂在顶层 describe 下的大文件按 describe 拆分
  （`responsive.spec.ts` 的历史行为），否则整文件一个进程。

`transcript-presentation.spec.ts` 和 `request-nav.spec.ts` 的各条用例已分别独立验证，声明为
parallel。前者含一条顶层 resize 用例，不能仅按 describe 分组猜测可拆性。

### runner 隔离

默认并发 `min(CPU, 32)`，每个进程独立端口。每次 invocation 使用独立的临时状态、鉴权文件、
二进制和随机 loopback 端口；runner 产物也放在独立的 `test-results/e2e-parallel/run-*/`，不会
删除另一次运行的报告或可执行文件。可与 Go e2e 并行运行，不得复用固定临时状态文件。

extension UI 的只读 sidecar 由 runner 编译一次，再复制到各测试的独立 home；直接运行
Playwright（包括串行调试）则在该 invocation 的 home 中编译并复用一次。

fake runner 默认复用最多 4 个 Chromium 进程，通过 Playwright `connectOptions` 连接；每条测试
仍创建独立 BrowserContext，cookie、localStorage、权限和页面不会复用。`KI_E2E_BROWSERS` 调整
进程数，`0` 关闭复用以便对照；直接 Playwright 调用不使用此池。浏览器连接服务由 Node 承载
（与 Playwright CLI 一致），只绑定 loopback，runner 完成或父进程管道关闭时释放；Bun 的该服务
传输路径会在 fixture 初始化时挂起。

`freePort()` 的探测 socket 会先关闭再由 `ki serve` 绑定，两者之间可能与另一个单元抢同一
端口；runner 仅在「一个用例都没跑 + `address already in use`」时换端口重试一次，因此不会掩盖
真实失败。

为保证不牺牲覆盖，runner 将 `--list` 的测试 ID 与各单元实际执行的 ID 逐项核对：缺失、重复、
意外多选或跳过都会使运行失败，避免总数相同却测错了用例。发现用例失败也直接失败退出。可用
`KI_E2E_JOBS` 调并发、`KI_E2E_SPLIT` 调拆分阈值、`KI_BIN` 复用已构建的二进制、
`KI_SKIP_WEB_BUILD` 禁止自动构建前端。

用例需要「先锁后放」这类瞬态时，不要靠固定 sleep 撞窗口（高并发下会偶发）：`sidecar` fixture
支持 `KI_INIT_WAIT_FILE`，测试可以先断言锁定态、再写文件放行。

折叠行的滚动回归在同一个浏览器任务里采样点击前几何并点击，等待该行 `aria-expanded=true` 后
再检查位置，运行结束后再次检查；自动跟随期间不得把较早任务里的采样当成点击瞬间的位置。
保持 `rowTop`、`scrollTop` 精确相等断言，不用固定等待或扩大误差掩盖跳位。

### 入口

`go test ./e2e -run WebUI` 运行 `bun run test`，包含 Bun 单元测试和同一个浏览器 runner，并用
`KI_BIN` 指向 Go 构建的二进制，因此每个 spec 仍然打到 Go 编出来的 SPA（需已 `bun install`、
`bun run build` 和装好 chromium）。Go 测试复用现有 dist；改前端后先重新构建，避免新用例测到
旧页面。

完整本地回归先 `bun run typecheck && bun run build`，再回根目录运行
`go test -tags embed -count=1 ./...`：包含静态资源契约、CLI 与完整 WebUI，无需再重复执行
`bun run test`。

开发中采用「修改 → 最小相关检查」的循环：Go 改动先跑受影响包/用例，纯逻辑改动跑 Bun，交互
改动跑对应 Playwright spec，不要每改一处就启动全量。改动稳定、准备交付时再集中跑一次完整
的必需套件；跨层契约或测试框架改动可以提前跑一次集成检查。文档改动检查 diff、链接和格式
即可。失败时先读日志、用最小范围复现并修复，不盲目重跑全量。保留 Go 编译缓存，只有需要
强制执行或比较真实测试耗时时才禁用测试结果缓存。性能预算单独执行，避免与高并发浏览器测试
争抢 CPU；调并发先测量，不按核心数盲目加倍。

### 长会话与性能预算

长会话 / 超长消息压测不进 fake 矩阵。生成 jsonl 夹具后测尾部 GET 的体积与延迟（并验证它不带
`index`）、`fields=index` / `fields=runtime` / `before` / `entry` 的预算、打开 Chat/Context 的 DOM
与 JS heap，以及向上翻页 / 截断正文补全：

```bash
cd web && bun run test:perf
```

Go 侧同一套夹具：`go test ./internal/session ./internal/server -run
'SeedView|ViewPerf|SeedTranscript' -v`；微基准 `go test ./internal/session -bench . -benchmem`。

弱网预算可用
`cd web && KI_PERF_WEAK=1 bunx playwright test --project=perf --grep 'long history and huge'`
复测同一用例：Chromium 下行 1.6Mbps、上行 750Kbps、延迟 800ms、CPU 4×。与普通预算相同，不放
宽 GET/UI 断言；同时记录 decoded / encoded / transfer 字节。此 profile 不代表真实端口转发和
移动设备，真机需再验证惯性、回弹、软键盘和旋转。

### 真模型

DeepSeek `deepseek-flash`，读 `DEEPSEEK_API_KEY` 或 `~/.ki` 凭据：

```bash
cd web && KI_LIVE=1 bun run test:e2e:live
```

或 `go test -tags live -timeout 5m ./e2e -run LiveWebUI`。

### 浏览器范围

自动化浏览器测试只使用 Chromium（Chrome 引擎），所有 project 都继承同一浏览器设置；不再维护
Firefox / WebKit project 或安装步骤。默认 fake 矩阵已包含滚动、compact、恢复、Markdown 和跨
尺寸呈现专项，桌面、平板、手机的 viewport / touch / mobile layout 覆盖保持在同一套用例中。
只跑滚动相关文件可用：

```bash
cd web && bunx playwright test --project=fake e2e/transcript-scroll.spec.ts e2e/compact-history.spec.ts
```

Chromium 的触摸事件与 viewport 仿真不代表真实 iPhone Safari 的惯性、回弹或软键盘验收。

## 进程与 agent 面板

Chat composer 上方的可展开运行时面板展示当前 session 的 live 进程与 root 范围 agent 身份。
数据来自既有 session GET 的 processes/agents 和 `process_updated`/`agent_updated` 事件；global
SSE 在 idle session 也更新面板，重新打开/恢复时用 GET 当前投影。工具卡默认显示 canonical
snake_case，原始调用名仍留在 transcript。

停止进程调用既有 abort 路由的 `scope=process` 与数值 session_id，中断 agent 调用目标 child 的
turn abort。普通 turn abort 不杀独立进程；`scope=tree` 为结构后代清理。面板在移动端换行，
命令与输出不撑开横向布局，停止/中断按钮至少 44px；safe-area 和 composer 原有动态 viewport
规则继续生效。
