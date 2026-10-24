# 工作区

工作区把会话按目录分组。登记文件是 `{KI_HOME}/workspaces.json`，一条记录包含稳定 id、
规范化目录 `path`、`title` 和 `sessionIds`。

## 成员判定

session header 的 cwd 与工作区 `path` 必须经同一套 `Abs` / `EvalSymlinks` 后相等。
临时工作区识别（`IsTemp`）对 home 一侧做同样的规范化：macOS 的 `/var/folders`、Windows
短名 `%TEMP%` 下，home 与已规范化记录 path 才可比。

session 列表每次请求对相同 cwd 只匹配一次，命中与未命中都在该次请求内复用，避免
同一工作区的多个会话重复访问文件系统解析路径。缓存不跨请求保留，工作区登记与符号
链接变化会在后续请求重新匹配；规范化与平台大小写比较沿用 `Store.Match`。

新建会话的目录优先级：

1. 显式 `workspaceId`；
2. 显式 `cwd`（会保证登记）；
3. `{KI_HOME}/workspace/tmp+<FileTimestamp>`。

## 排序

文件里的数组顺序决定组序，`sessionIds` 决定组内序。pin 写入 `config.pinned` 并把会话挪到
组首；侧栏渲染时再按 `pinned` 做稳定分区（置顶在前，各段内保持 `sessionIds` 顺序），
所以之后新建的会话（`AttachSession` 前插）不会把置顶挤下去，段内拖拽排序照常生效。

## 删除

删除工作区依次执行：abort 组内 run → 删会话 jsonl 目录 → 去登记。**不删**工作区磁盘目录
和用户文件。

## 文件接口

`GET /v1/fs` 默认只列目录，返回 `separator` 与绝对 `path`，前端不拼接路径。`files=1` 时也
列普通文件供附件选择；`preview=1` 同源预览图片、文本/代码和 PDF。

内容搜索扫描 jsonl 的 user / assistant 文本，字面匹配，最多返回 20 条。
