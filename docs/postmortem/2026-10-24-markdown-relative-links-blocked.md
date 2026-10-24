# Markdown 相对链接被渲染成 `[blocked]`

## 现象

WebUI 里 agent 正文的 `[方案文档](docs/async-user-input.md)` 显示成灰字
`方案文档 [blocked]`，`![图](assets/x.svg)` 显示成 `[Image blocked: 图]`。会话 jsonl 里
并没有字面 `[blocked]`，所以不是模型输出、不是落盘改写，也不是供应商的内容过滤。

## 根因

`Markdown.tsx` 只覆盖了 `linkSafety`，没有传 `rehypePlugins`，于是 Streamdown 2.6 的默认
插件链 `rehype-raw → rehype-sanitize → rehype-harden` 生效。`rehype-harden` 的判定不是
"安全/不安全"，而是"URL 是否以 http(s) 或 `/`、`./`、`../` 开头"：`docs/x.md` 这类最常见
的 Markdown 相对路径落在三类之外，被判非法，链接被换成 `span` 并追加 ` [blocked]`。

改 `defaultOrigin` 只会把相对路径改写成 `http://host:port/docs/x.md`；`serveUI` 找不到该
文件时回退 `index.html`，点击会整页刷新并丢掉当前会话——比 `[blocked]` 更糟。

另外 `allowDataImages` 也救不了 `data:` 图片：插件顺序是 sanitize 在前，`src` 已被剥掉，
harden 拿不到 URL，仍然输出 `[Image blocked: …]`。

## 修复

不再依赖 harden 的白名单，改为自己声明 `rehypePlugins`（`features/markdown/rehype.ts`）：

- `rehype-raw` + `rehype-sanitize`，安全边界由 sanitize 承担：`javascript:`、`file:`、
  `vbscript:` 的 `href`/`src` 被直接剥掉，`data:` 图片同样剥掉，且不会改写任何文本；
- `a` / `img` 由 `features/markdown/links.tsx` 接管：外链仍是 `<a target="_blank">`，锚点
  保留，本地路径经 `lib/localPath.ts` 解析成绝对路径后点开 `OutputPreview`，非法协议退化为
  纯文字；
- 图片走 `GET /v1/fs?path=…&preview=1` 取 blob 再转 `blob:` URL（`<img src>` 带不上
  `Authorization`）。

## 教训

- 第三方 Markdown 管线的默认安全插件会把"非绝对 URL"和"危险协议"混为一谈；接入时要检查
  默认插件列表，而不是只覆盖显眼的开关（`linkSafety` 只控制外链确认弹窗）。
- 判定"渲染期改写"要看产物字符串与运行时管线，不要只看源码 grep：`[blocked]` 只存在于
  `node_modules` 与 `web/dist`，仓库源码里一个字都没有。
- 断言要写成"绝不出现 `blocked`"这种否定式回归测试（`web/unit/markdown-links.test.ts`），
  否则升级 Streamdown 后同类问题会悄悄回来。
