# WebUI

`ki serve` 同域出页面：一个二进制，静态资源嵌在 `web/dist`，API 仍是 `/v1/*`。

浏览器打开 `http://127.0.0.1:19800/`。token 写进 `index.html`，前端用 Bearer 调 API、用 `fetch` 读 SSE。

## 页面

- 侧栏：新会话、按 cwd 分组的列表、设置入口（无主题切换）
- 对话：气泡、Markdown（含表格）、Think、Read/Edit/Bash 专用卡、用量脚注（in→out / cache read/write）下常亮 copy/fork/regen、用户气泡右下 copy/edit、压缩、composer
- 轨迹：SYSTEM / USER / ASSISTANT / TOOL / COMPACTED 各有色标；时长/折工具/跟尾为带 tip 的图标；检查器标题为 Turn N · Step/Message，详情对齐 Summary / Preview / Raw（SYSTEM 仍是 System Prompt / Tools / Context）
- 设置 / 选模型：各自弹窗；外观（默认浅色）只在设置里改；对话输入条上的模型芯片打开模型列表

数据来自 session jsonl（`GET /{id}` 的 `entries` / `messages`）和本次 run 的 SSE，不走模型厂商 SDK。

这不是 DSH WebUI 的一比一复刻。差距见 [docs/prd/webui-parity.md](prd/webui-parity.md)。下一步能对齐什么见 [docs/prd/webui-next.md](prd/webui-next.md)。

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
