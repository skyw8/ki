
## agent主循环
事件总线架构，hooks

## 工具

- bash
- edit
- write
- read

- 主要参考/data/hgy/pi
- 兼容/data/hgy/claude-code-source-code的工具名和参数

## 会话管理

- 写盘通过hooks异步驱动
- append-only jsonl log，Event Sourcing风格
- 树状结构，revert/regenerate 移动leaf，旧数据不删除
- 一个session对应一个目录，先按照 年/月/日 的目录结构，然后下面是session的目录，目录名是时间戳
- fork session单起目录，迁移相关信息
- 需要记录input/output/cache token信息
- 需要记录响应时间，后续可能需要显示tps、ttft相关信息
- 需要记录工具调用时间，工具调用失败的记录、原因


## compaction
- 主要参考/data/hgy/pi
- compaction之后应当认为cache失效，所以分层prompt需要reload

## 模型供应商调用
- 抽象为统一的中间表示
- 支持OpenAI Completion, Responses API，Anthropic Messages
- 兼容各家api细节

## 上下文管理

- 分层提示词

1. 身份与角色
2. Available tools
3. Available skills
4. AGENTS.md（全局+向上收集遍历终止到git root），兼容CLAUDE.md
5. pwd, timezone+date
6. message history


## 配置

支持项目级配置目录

支持全局配置目录 ~/.ki
- skills：~/.ki/skills
- mcp: ~/.ki/.mcp.json（遵循mcp规范）

支持session级配置
- session 目录下独立配置文件记录skills、mcp等，主要是便于后续可以选择是开启还是关闭

## 未来架构说明

1. 后续会使用B/S架构，本地起web服务调用本地后端，前后端打包成一个二进制，极致易用性。这样做可以跨平台，在服务器远程开发的场景下，可以通过端口转发的形式使用webui
2. 后续可能会提供HTTP SDK给别的语言使用