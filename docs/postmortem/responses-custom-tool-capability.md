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

## 验证

真实 DeepSeek 的 Completions、Responses、Anthropic 均通过 ping 和 exec 嵌套读文件后的结果 continuation；全量 embed 回归及相关 race 测试通过。另补真实 CLI 二进制 worker 的便宜 smoke 测试：进程内 `cli.Main` 的宿主是 Go 测试程序，直接让其启动默认 worker 会把测试 stdout 当成 NDJSON，不能把这种测试夹具错误误判为生产 IPC 故障。
