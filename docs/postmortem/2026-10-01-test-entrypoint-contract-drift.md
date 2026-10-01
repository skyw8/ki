# 测试入口与当前契约脱节

## 现象

真实模型图片/PDF 已给出正确颜色和标记，但 `TestLiveImageAndPDF` 仍要求
JSONL 出现旧拼写 `Read`，实际工具结果使用 `read`。双图片测试的全文件字符串
匹配还会把 model_request 中的 schema 当成执行证据。

Rust launcher 构建能成功，独立 native 单元测试却在 bindgen 阶段找不到
`stdbool.h`。Linux 的标准头文件回退只在 launcher 构建入口设置，直接启动
native Cargo 测试绕过了它。native 的 build.rs 来不及修复依赖 build script
已经读取的环境。

## 修复

- 图片/PDF 与双图片测试读取结构化 session entries，以注册的 `read` 拼写
  匹配 assistant 调用，按 toolCallId 配对成功 toolResult，并逐个检查指定文件。
  提示词使用 canonical 名称；schema、错误结果或只读取其中一个文件均不能通过。
- 本地和 CI 使用 `node extensions/zvec-grep/test/native.mjs` 运行 Rust 单元测试。
  在启动 Cargo 前处理 Linux 标准头文件路径，沿用 launcher 的回退策略，保留
  显式环境设置、Rust 1.98.0、锁文件和现有编译缓存。

## 后续约束

工具 schema、实际调用和结果身份应分开验证。测试应观察真实持久化结果，避免
文本搜索恰好匹配到请求 schema 或系统提示。native 构建与测试入口都须在启动
依赖编译前配置环境；修改入口时实际跑对应单元测试，不只验证 launcher 构建。
