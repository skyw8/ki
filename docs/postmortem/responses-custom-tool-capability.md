# Responses 协议不等于任意 custom 工具支持

## 故障

2026-10-03，DeepSeek `deepseek-flash` 会话在第一条 `ping` 请求就返回 HTTP 400：`Unsupported custom tool: 'exec'. Only 'apply_patch' is supported.` 请求没有执行任何工具。

## 原因

Code Mode 用 `API == "responses"` 选择 custom/freeform `exec`，把协议形状误当成供应商能力。DeepSeek Responses 为 Codex 兼容仅支持名为 `apply_patch` 的 custom 工具，而普通 function 工具可以使用。已有 patch 与 compaction 能力独立声明，exec 却绕过了同样的约束；测试只比较协议，没有比较同一协议下不同模型的能力。

另一个风险是覆盖模型 API 后仍沿用 native patch 能力：Completions / Anthropic 不支持发布 Responses custom grammar，不能仅检查目录中的 patch 标记。

## 修复与防复发

- 模型通过独立 `execToolType: "freeform"` 显式启用 custom exec；默认使用 JSON `{code: "…"}` function。
- core 的 freeform patch 与 exec 都要求 Responses 协议；扩展私有 API 自行声明并兑现该契约，host 不硬编码其别名。二者能力不可互相推断。
- 回归矩阵同时覆盖协议与模型能力，而不把所有 Responses 服务端视为 OpenAI 的完整实现。
- 历史回放保留原始工具输入及 call/output 配对；不得因切换格式静默丢弃 payload。
- 公共客户端在发送前拒绝 Completions / Anthropic custom 声明与未知工具类型，避免静默构造缺少 schema 的 function；历史 raw input 根据当前单一 string 字段无损包装，无法映射时明确失败。
- 与 standalone / inline compaction 一样，新协议特性必须显式声明能力，不能由 API 名或模型名自动推断。

## 2026-10-25 模型列表导入补充

将 Magpie 专用刷新扩展到其他 provider 时，不能把推理协议当成完整的模型列表契约。
Anthropic / MiniMax 的列表需要 `x-api-key` 和游标分页，DashScope 的列表位于独立的
`/api/v1/models`，响应使用 `output.models[].model`，而非兼容推理 base 下的 `data[].id`。
因此按官方 host 和列表协议适配，分页全部成功后才原子同步；自定义 provider 名称不改变
官方端点的识别，未知兼容端点只尝试其已有 base，不猜测其他计费或订阅入口。

OpenAI 等列表只给模型 ID，缺失能力字段不代表原能力关闭。导入保留已知基线的能力、限制和
价格，新模型使用明确的保守默认值，不根据 ID 猜测 Responses、图片或推理能力。手动模型、
内置模型与用户覆盖保持各自来源；不能把已应用覆盖的 resolved model 写回基线，否则一次
Fetch 就会固化禁用或自定义字段，使恢复内置值失效。同步期间协议、地址、凭据变化返回 409。
删除同步模型时也同时清除 `syncedModels` 归属，随后同 ID 的手动重建必须作为手动配置保留。

回归同时检查协议认证、路径、完整分页、错误保留目录、重复 ID、连接变化以及本地配置来源；
浏览器复用供应商编辑流程验证普通 provider 和 Magpie 共用 Fetch 按钮、加载时禁用和失败保留。
原四项定向浏览器检查从 19.0 秒增至 23.9 秒（包含测试服务构建与启动）；新增步骤覆盖成功、
挂起、失败和 extension 目录归属，没有固定等待。最终完整 embed 回归约 68 秒，236 项
Chromium 用例全部通过，无失败、跳过或 flaky；类型检查和新鲜 Vite 构建通过。

## 验证

真实 DeepSeek 的 Completions、Responses、Anthropic 均通过 ping 和 exec 嵌套读文件后的结果 continuation；全量 embed 回归及相关 race 测试通过。另补真实 CLI 二进制 worker 的便宜 smoke 测试：进程内 `cli.Main` 的宿主是 Go 测试程序，直接让其启动默认 worker 会把测试 stdout 当成 NDJSON，不能把这种测试夹具错误误判为生产 IPC 故障。
