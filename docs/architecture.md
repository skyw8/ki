# 一次 prompt 怎么走

跨 `cli` → `server` → `loop` 的编排。包内不变量见各自 `doc.go`。

## 进程

- `ki serve [--addr]`：前台 HTTP，默认 `127.0.0.1:19800`，写 `~/.ki/server.json`（addr + token）。
- `ki -d`：setsid 拉起 `serve`，CLI 退出 server 还在。
- `ki [flags] <text>`：client。`server.json` health 通则连；否则本进程听 `127.0.0.1:0`，退出带走。

续聊必须 `--session <id>`。`--model` 随 prompt 发给 server，写回**该 session** 的 `config.json`，不改 toml。`KI_FAKE=1` 用假模型。

## HTTP

除 `GET /v1/health` 外都要 `Authorization: Bearer`（也认 `?token=`）。非 `/v1` 路径是同域 WebUI，`index.html` 注入 token。

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/v1/models` | 内置 catalog（已有进程数据） |
| GET | `/v1/meta` | 默认模型、用户 home（无进程 cwd） |
| GET | `/v1/sessions` | 列出全部 session（含 title / running / workspaceId / pinned） |
| POST | `/v1/sessions` | 新建：`workspaceId` → `cwd` → 临时 `{KI_HOME}/workspace/tmp+…` |
| GET | `/v1/sessions/search` | 正文字面搜索，最多 20 条 |
| GET | `/v1/sessions/{id}` | header、leaf、模型、`entries`、`messages`、running、skills/mcp、`availableSkills` / `availableMcp` |
| PATCH | `/v1/sessions/{id}` | 写 `model` / `title` / `pinned` / skills / mcp |
| DELETE | `/v1/sessions/{id}` | 删该会话目录 |
| POST | `/v1/sessions/{id}/prompt` | `202` 开跑；同一 session 未结束再来 **409** |
| GET | `/v1/sessions/{id}/events` | SSE，按游标重放本次 run 的事件 |
| POST | `/v1/sessions/{id}/abort` | cancel |
| POST | `/v1/sessions/{id}/compact` | 手动 compaction |
| POST | `/v1/sessions/{id}/fork` | 整目录拷走 |
| GET | `/v1/workspaces` | 工作区登记（含 `sessionIds` / `temp`） |
| POST | `/v1/workspaces` | 登记 path（可 mkdir） |
| PATCH | `/v1/workspaces/{id}` | 改 title |
| DELETE | `/v1/workspaces/{id}` | 删组内会话日志和登记，不删工作区磁盘目录 |
| POST | `/v1/workspaces/{id}/move` | 工作区排序 |
| POST | `/v1/workspaces/{id}/sessions/move` | 组内会话排序 |
| GET | `/v1/fs` | 列目录（`path` / `home` / `separator` / `crumbs` / `entries`） |
| POST | `/v1/fs` | 在已有目录下建子文件夹 |

`message_end` 上 await 写 jsonl。`agent_end` 上按阈值自动 compact。SSE 在 run `done` 后先排空剩余事件，再结束。MCP 连接在 serve 级池里，prompt 只按 Toggle 绑缓存 schema，工具调用时才 ensure。

## 循环事件

```
agent_start
  turn_start
    message_start → message_end                       # user（仅首 turn）
    request_header                                    # system + tools 快照
    message_start → message_update* → message_end     # assistant
    tool_execution_start → … → tool_execution_end
    message_start / message_end                       # toolResult
  turn_end
agent_end
```

字段跟 pi。写盘和 SSE 是 server 挂在 `emit` 上的订阅者，不进 loop 包。
