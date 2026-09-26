# 跨平台测试期望：macOS `/private/var` 与 Windows 短名 `%TEMP%`

日期：2026-09-26  
范围：`internal/extension`、`internal/resources`、`internal/server`、`internal/tooloutput`、`web/e2e`（v0.0.4 发版前的 CI 红灯）

## 现象

v0.0.4 打 tag 后 Release 工作流在 `tests` 阶段失败：Ubuntu 的 Go 测试全绿，macOS / Windows 各挂一批，release 因此不发布。

```
Go tests (macos-latest)
--- FAIL: TestPathDirsListsExistingDirsInChainOrder
    path_test.go:73: PathDirs = [/private/var/folders/36/…/001/extensions/alpha/bin], want only alpha/bin
--- FAIL: TestScanIncludesExtensionPathDirs
--- FAIL: TestPromptAppendWritesBothSourcesAdditively
    prompt_test.go:161: project path = "/private/var/folders/…/.ki/prompt/APPEND_SYSTEM.md", want "/var/folders/…"

Go tests (windows-latest)
--- FAIL: TestDiscoverRejectsEscapingPathDirs/absolute
    path_test.go:47: expected manifest error for runtime.path "/usr/bin"
--- FAIL: TestPathDirsListsExistingDirsInChainOrder
--- FAIL: TestScanIncludesExtensionPathDirs
--- FAIL: TestPromptAppendWritesBothSourcesAdditively
    prompt_test.go:152: global prompt mode = -rw-rw-rw-, want 0600
--- FAIL: TestSweepRemovesDeadOwnersAndKeepsLiveOnes
    store_test.go:230: dead owner was not swept
```

本地 `go test ./...` 与 WebUI 套件都是绿的——两边平台差异比 Linux 本机的差异更大，本地跑通不能代表 CI 通过。

## 根因

一类问题，四种表现。

### 1. 期望值没跟代码一样归一化

扩展目录的 root 在 `loadPackage` 里过 `filepath.EvalSymlinks`，工作区路径在 `workspace.Normalize` 里也是（Abs + Clean + EvalSymlinks），所以代码报出的是 **canonical** 路径；测试却拿 `t.TempDir()` 的原始字符串去比：

- macOS：`/var/folders/…` 是 `/private/var/folders/…` 的符号链接；
- Windows：`%TEMP%` 可能是短名（`C:\Users\RUNNER~1\…`），`EvalSymlinks` 返回长名。

同一个坑 `e2e/e2e_test.go` 早就踩过一次（`TestCWDEncodesSessionPath` 里已有 `workspace.Normalize` 的 why-comment），只是当时只修了那一处。

### 2. 路径合法性按宿主判定

`withinRoot` 用 `filepath.IsAbs` 判「绝对路径」。Windows 上 `/usr/bin` 没有卷名，`IsAbs` 为假，于是 POSIX 绝对路径被当成包内相对路径收了进去。文档写的是「绝对路径拒绝」，代码只在部分平台成立。

### 3. 权限位在 Windows 上不存在

`0600` 的断言在 Windows 恒为 `0666`。仓库里其它权限断言都带 `runtime.GOOS != "windows"`，这条漏了。

### 4. 无 liveness 检查的平台走 TTL 兜底

`tooloutput` 的 sweep 在非 Unix 上 `processAlive` 返回 `known=false`，改用 heartbeat + TTL。fixture 的 dead owner 心跳正好压在 TTL 边界（`now-1h` vs `ttl=1h`，判断是 `>`），兜底保留，于是 Windows 上「死进程没被清掉」。测试只验证了 liveness 这一条通道。

## 修复

| 提交 | 做法 |
|---|---|
| `fix: reject rooted extension manifest paths on every platform` | 新增 `isRootedManifestPath`：`/`、`\` 开头、卷名、盘符一律拒绝，与宿主无关；`filepath.IsAbs` 保留为快速路径 |
| `fix: compare extension and workspace paths after normalization` | 测试先用 `EvalSymlinks` / `workspace.Normalize` 归一化临时目录再比；WebUI fixture 用 `realpathSync` |
| `fix: assert the appended prompt mode only where POSIX modes apply` | 权限断言回到仓库既有的 `runtime.GOOS != "windows"` 写法 |
| `fix: sweep tool-output runs by TTL where liveness is unknown` | fixture 的心跳改到 TTL 之外，让 liveness 与兜底两条通道证明同一件事 |

## 防复发

- 测试里凡是拿 `t.TempDir()` 去比 **代码报出的路径**（扩展 root、工作区 cwd、扩展 PATH 明细），先归一化；扩展包与 resources 各放了一个 `resolvedTempDir` 助手。
- 校验「留在包内」这类 manifest 规则时，用字符串判据而不是宿主 API：manifest 会在别的操作系统上被读。
- 平台相关断言（权限位、进程存活）按仓库既有写法显式分流，不要写「在 Linux 上恰好为真」的期望。
- 本地验收可以近似非 Linux 宿主：把 `TMPDIR` 指到一个符号链接目录再跑 Go 套件，就能复现 macOS 的 `/private/var` 一类差异。

```sh
mkdir -p /tmp/ki-real-tmp && ln -sfn /tmp/ki-real-tmp /tmp/ki-link-tmp
TMPDIR=/tmp/ki-link-tmp go test -timeout 10m ./...
TMPDIR=/tmp/ki-link-tmp bun run test:e2e   # 在 web/
```
