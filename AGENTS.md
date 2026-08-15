## description
A lean and extensible agent runtime designed for easy integration.

## tech stack
- go

## directory structure

包注释在各目录 `doc.go`（`go doc ./internal/session`）。跨包说明只留下面几份 md。

```
ki/
├── cmd/ki/              唯一二进制入口
├── docs/
│   ├── prd/             需求与拍板（agent-loop / problem / plan）
│   ├── architecture.md  一次 prompt：CLI → HTTP → loop
│   ├── session.md       jsonl 树、leaf、fork
│   ├── provider.md      三套协议怎么组包
│   └── tools.md         四工具参数和结果
├── internal/
│   ├── cli/             flag、起/连 server、SSE 打终端
│   ├── server/          HTTP 编排
│   ├── loop/            主循环，只 emit
│   ├── session/         jsonl 树
│   ├── tools/           Read / Write / Edit / Bash
│   ├── provider/        Completions / Responses / Anthropic
│   ├── prompt/          分层 system prompt
│   ├── compact/         compaction
│   ├── config/          合并 ki.toml 与环境变量
│   ├── skills/          发现 SKILL.md
│   ├── mcp/             .mcp.json → loop.Tool
│   ├── types/           Message / Usage IR
│   ├── idgen/           session / entry id
│   └── klog/            stderr + ki.log
├── AGENTS.md
├── README.md
└── go.mod
```


## docs

跨包说明在 `docs/`。包内不变量看各目录 `doc.go`。

| 文档 | 内容 |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 一次 prompt：CLI → HTTP → loop；路由、SSE、事件序 |
| [docs/session.md](docs/session.md) | 会话目录、jsonl header/entry、leaf、fork、`config.json` |
| [docs/provider.md](docs/provider.md) | Completions / Responses / Anthropic 组包；DeepSeek base |
| [docs/tools.md](docs/tools.md) | Read / Write / Edit / Bash 的参数和结果契约 |
| [docs/prd/agent-loop.md](docs/prd/agent-loop.md) | 主循环、工具、会话、compaction、供应商、分层 prompt |
| [docs/prd/problem.md](docs/prd/problem.md) | 未决问题与已拍结论 |
| [docs/prd/plan.md](docs/prd/plan.md) | 分阶段实现计划 |

## constraint

- 代码修改时，相关文档包含AGENTS.md需要相应修改