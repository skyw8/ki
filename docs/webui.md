# WebUI

`ki serve` 同域出页面：一个二进制，静态资源嵌在 `web/dist`，API 仍是 `/v1/*`。

浏览器打开 `http://127.0.0.1:19800/`，或经 SSH/IDE **端口转发** 打开同一端口。token 写进 `index.html`，前端只用同域相对路径调 `/v1/*` 和 `/assets/*`，不把宿主文件路径写进 `href`，也不用系统选目录。

## 页面

- 侧栏：工作区树（创建 / 重命名 / 删除登记和会话日志、组内 `+`、pin、拖拽、每组默认 5 条「显示更多」）、Miller 选目录、标题+正文搜索、未分组只给旧脏数据
- 对话：气泡、Markdown、Think、默认折叠的工具行（Read 行号 / Edit diff / Bash 终端 / IN·OUT、Inspect）、用量脚注下 copy/fork/regen、离底「回到底部」、composer（cwd 芯片来自当前会话）
- 轨迹：SYSTEM / USER / ASSISTANT / TOOL / COMPACTED 各有色标；检查器 Summary / Preview / Raw
- 配置：本会话只读元数据（cwd / 模型 / id）；列出当前发现到的 skills 和 MCP server，勾选即时写入 `disabled`（下次发送生效）
- 设置 / 选模型：各自弹窗。选模型支持按 provider、model ID、显示名称和完整 spec 进行不区分大小写的子串 / 顺序模糊搜索。设置顶部按「模型供应商」「主题和语言」分成两个页面；供应商页的外层不滚动，左侧供应商列表与右侧连接、凭据、模型编辑区分别独立滚动，新增供应商使用二级弹窗。模型高级 JSON 编辑保留 `input` 和 `applyPatchToolType` 等能力字段。目录只在本机维护，不在线刷新。Base URL、API 协议等表单控件共享尺寸和排版；API 协议与 thinking effort 共用 ARIA combobox/listbox 组件，支持方向键、Enter、Escape，并按可用空间向上或向下展开。主题（默认浅色）和语言（中 / 英）存在本浏览器 `localStorage`

数据来自 session jsonl 和本次 run 的 SSE。工作区见 [workspace.md](workspace.md)。

## 构建

```bash
cd web && npm install && npm run build
go build -o ki ./cmd/ki
```

改前端后必须重新 `npm run build`，再编 Go，嵌入的才是新资源。

## Playwright

假模型打通对话和轨迹（`KI_FAKE=1` 起 `ki serve`，同域打开页面）：

```bash
cd web && npm install && npx playwright install chromium
npm run test:e2e
```

`go test ./e2e -run WebUI` 会先起 server，再跑同一套 Playwright（需已 `npm install` 和装好 chromium）。

真模型（DashScope `qwen3.7-plus`，读 `DASHSCOPE_CN_API_KEY` 或 `~/.ki/ki.toml`）：

```bash
cd web && KI_LIVE=1 npm run test:e2e:live
```

或 `go test -tags live -timeout 5m ./e2e -run LiveWebUI`。
