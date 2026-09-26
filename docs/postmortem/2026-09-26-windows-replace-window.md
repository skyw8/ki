# Windows 上 rename 替换期间并发读失败：v0.0.4 发布被 Windows e2e 拦住

日期：2026-09-26  
范围：`internal/session`（`config.json` 的读写门）、`e2e/queue_test.go`

## 现象

v0.0.4 的 Release 工作流里，`go-tests` 在 Ubuntu/macOS 全绿，Windows 上 `ki/e2e` 偶发失败（约两次推送里挂一次），签名固定：

```
--- FAIL: TestBusyQueuePromoteHTTP (0.14s)
    queue_test.go:47: queued = <nil>
```

`queued = <nil>` 是 `detail["queued"]` 为空——说明 `GET /v1/sessions/{id}` 根本没有返回 session 详情。把状态码和原始 body 打出来后才看到真正的答案：

```
promote enqueue 404 map[] body="read session config: open C:\Users\RUNNER~1\...
sessions\...\config.json: The process cannot access the file because it is being used
by another process.\n"
```

即 `POST /v1/sessions/{id}/prompt` 对一个**确实存在**的 session 返回 404，原因是读 `config.json` 时被 Windows 拒绝（`ERROR_SHARING_VIOLATION`，errno 32）。

## 根因

`internal/session` 的约定是「同一目录的控制文件，读写都过同一个文件门」：`Open` 和 `liteInfo` 读 `config.json` 时持 `fileGate(dir)` 读锁，`writeConfig`/`appendRaw` 写时持写锁。`ReadConfig`（`internal/server` 的 `open()` 与 `loadSessionSnap()` 都调它）**漏了这把读锁**，于是它可能落在一次 `writeFileConfig` 的替换窗口里。

而 Windows 的替换窗口对读者不是透明的：`os.Rename` 走 `MoveFileEx(MOVEFILE_REPLACE_EXISTING)`，替换期间旧文件处于 delete-pending，并发的 `open` 会失败（`ERROR_SHARING_VIOLATION`）。POSIX 的 rename 只是 unlink 旧 inode，已经打开的读者不受影响——所以 Linux/macOS 本地怎么跑都看不到，只有 Windows runner 上偶发。

触发频率取决于 `config.json` 被改写的频率：run 每追加一条 entry 都会 `SetLeaf` → `writeConfig`，所以「轮询/提交 prompt」撞上「run 正在追加」的概率不低；队列文件因为已经有 `queueGate` 串行，从来没出过这个问题。

## 修复

| 提交 | 做法 |
|---|---|
| `fix: order session config reads with the file gate` | `ReadConfig` 持 `fileGate(dir)` 读锁，与 `Open` / `liteInfo` / `writeConfig` 一致；新增 `TestReadConfigWaitsForTheFileGate`，先在缺锁版本上验证会红 |
| `add: report the server body when the busy-queue e2e fails` | `serveRaw` 返回状态码 + 原始 body + 读错误，prompt 步骤改用它；`serveJSON` 会吞掉非 JSON body 和读错误，所以最初只能看到 `queued = <nil>` |

验证方式：Windows 上一次 CI 跑 8 遍同一场景（临时把 `TestBusyQueuePromoteHTTP` 循环 8 次，修复前 8 次里能红 4 次以上），修复后同一份改动全绿。

## 防复发

- 一个目录里凡是「临时文件 + rename」落盘的文件，**读侧必须和写侧共用同一把门**，否则就默认依赖「rename 不影响读者」这个只在 POSIX 成立的假设。
- 同类还没上门的文件：`toggles.json`、`workspaces.json`、`models.json`、`credentials.json`（都由 WebUI 改写、也可能被并发读）。它们被改写的频率远低于 `config.json`，本次未动；真出问题时按同样的方式加门。
- 测试里不要把「非 2xx / 非 JSON 响应」悄悄折叠成空 map：`serveJSON` 这类助手要么报状态码，要么给出原始 body，否则排查一个偶发只会看到 `<nil>`。
