# Web Push

会话完成通知的「页面被冻结时也能送达」通道。它是 WebUI 通知的扩展，不是第二套开关：
打开「通知」开关后，页面在有用户手势的点击里申请 `Notification` 权限并注册 Web Push
订阅；关掉时同时 `unsubscribe()` 并通知 server 删除。

## 为什么需要

`GET /v1/events` 是页面活着时才存在的 push 流。手机锁屏、浏览器切到后台、标签页关闭
后，页面被冻结，run 跑完的 `agent_end` 无人接收。Web Push 把投递交给浏览器的推送服务，
由 service worker 在没有页面的情况下唤起并弹系统通知。

## 组成

| 部分 | 位置 | 职责 |
|---|---|---|
| VAPID 密钥 | `{KI_HOME}/vapid.json` | P-256 应用服务器密钥对（RFC 8292）；公钥给浏览器做 `applicationServerKey`，私钥给每个请求签 JWT |
| 订阅注册表 | `{KI_HOME}/push-subscriptions.json` | 每个浏览器 profile 一条：endpoint + `p256dh` + `auth` |
| 发送端 | `internal/push` | 每条消息按 RFC 8291（`aes128gcm`）逐订阅加密并 POST |
| service worker | `web/public/sw.js` | 收 `push` 弹系统通知；`notificationclick` 聚焦已有窗口或打开首页 |
| 客户端订阅 | `web/src/lib/push.ts` | 注册 SW、`PushManager.subscribe`、把订阅 POST 给 server |
| PWA 元数据 | `web/public/manifest.webmanifest`、`web/index.html` | 可安装；iOS 必须先「添加到主屏幕」才有 Web Push |

## 接口

server 侧（都要鉴权；写操作走 CSRF，与其它浏览器请求一致）：

- `GET /v1/push/config` → `{ enabled, publicKey }`。`publicKey` 是 base64url 的非压缩
  P-256 点，即浏览器的 `applicationServerKey`。
- `POST /v1/push/subscriptions` `{ endpoint, keys: { p256dh, auth } }`。按 endpoint
  upsert，所以客户端每次加载都会重发：这既是幂等的同步，也是订阅被推送服务注销
  （404/410）后 server 剪掉该行、客户端下次访问自愈的路径。
- `DELETE /v1/push/subscriptions?endpoint=...`。

endpoint 必须是 `https://`，或 `http://127.0.0.1` / `http://localhost`（仅测试与纯本机
场景）；`p256dh` 必须解出 65 字节 P-256 点、`auth` 16 字节，否则 400。

## 投递流程

1. run 的 `agent_end` 走 `runEmitter.publishCompletion`：先投 WebUI push 流（活的页面），
   再 `Server.notifyPushCompletion`。
2. `notifyPushCompletion` 读 session 行拿标题与 cwd；`forkMode=tree` 的 subagent 会话直接
   跳过（它挂在 parent 下，由 parent 的完成通知代表）；用户主动 abort 的 run 也会跳过
   （`run_aborted` 先落标记，随后的 `agent_end` 消费它，与页面侧的抑制同源）。
3. 消息进 `push.Service` 的缓冲队列（深度 64），由单 worker 出队；队列满则丢弃——推送是
   活的 push 流之外的兜底，不是权威信号。
4. worker 对每个订阅加密并 POST；推送服务回 404/410 时删除该订阅。其它失败只记日志。

负载是 JSON：`{ type: "run_complete", sessionId, title, cwd }`。

## 抑制规则

页面和 service worker 用同一条规则、同一个 `tag`（`ki-run-<sessionId>`）：

- 页面侧通知也优先走 service worker（`registration.showNotification`），没有注册时才退回
  `new Notification()`。Android Chrome 没有 `Notification` 构造函数，只有注册能弹通知，
  所以这条路径不是优化而是必需；两条路都不可用时设置页的测试按钮会提示失败。
- 有**聚焦且可见**的 ki 窗口时，SW 不弹：那是活的页面，它自己经 push 流收到了这次完成，
  并按「正在看的那个会话才静默」的规则处理（见 [webui.md](webui.md)）。SW 只能看到
  「有没有聚焦窗口」，看不到窗口在看哪个会话，但活的页面本身对未来所有会话的完成都有
  感知，所以结果一致。
- 没有聚焦窗口（锁屏、切到别的应用、关掉页面）时，推送是唯一信号，弹。
- 页面与 SW 同时弹时，`tag` 相同，系统只保留一条。

## 配置

`ki.toml`：

```toml
[push]
enabled = true                       # 关掉则只有活的页面发通知
subject = "mailto:you@example.com"   # VAPID JWT 的 sub，RFC 8292 要求 mailto:/https:
```

## 限制

- 需要安全上下文。HTTPS（Tailscale serve / 反向代理）可以；`http://<局域网 IP>` 不行。
  `http://localhost` 的端口转发可以。
- iOS 上 Web Push 只对「添加到主屏幕」的 PWA 生效（iOS 16.4+），Safari 标签页里没有
  `PushManager`。
- 浏览器可能轮换订阅而页面无从得知（下发在 `subscriptionchange` 里写回需要 CSRF，SW 读
  不到 cookie）。旧 endpoint 会被推送服务以 410 拒绝并被剪掉，页面下次加载重新订阅。
- 密钥文件丢失会导致所有既有订阅失效（浏览器钉住了旧公钥）；客户端下次加载会重新订阅。
