# WebUI

`ki serve` 同域出页面：一个二进制，静态资源嵌在 `web/dist`，API 仍是 `/v1/*`。

浏览器打开 `http://127.0.0.1:19800/`。token 写进 `index.html`，前端用 Bearer 调 API、用 `fetch` 读 SSE。

## 页面

- 侧栏：新会话、按 cwd 分组的列表、主题切换
- 对话：用户气泡、助手 Markdown、Think、工具卡、压缩行、composer
- 轨迹：turn 表、时间条、选中检查器（概览 / Input / Output）
- 设置：空页

数据来自 session jsonl（`GET /{id}` 的 `entries` / `messages`）和本次 run 的 SSE，不走模型厂商 SDK。

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
