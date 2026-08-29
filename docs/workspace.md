# 工作区

登记文件 `{KI_HOME}/workspaces.json`。一条记录是稳定 id + 规范化目录 path + title + `sessionIds`。

- 成员：session header 的 cwd 与 path 同一套 `Abs` / `EvalSymlinks` 后相等。
- 开会话：`workspaceId` → 显式 `cwd`（会保证有登记）→ `{KI_HOME}/workspace/tmp+<FileTimestamp>`。
- 删除工作区：abort 组内 run → 删会话 jsonl 目录 → 去登记。**不删**工作区磁盘目录和用户文件。
- 组序是文件里数组顺序；组内序是 `sessionIds`。pin 把会话挪到组首并写 `config.pinned`。
- `GET /v1/fs` 默认只列目录，带 `separator` 与绝对 `path`；前端不拼接路径。`files=1` 时也列普通文件供附件选择；`preview=1` 同源预览图片、文本/代码和 PDF。
- 内容搜索扫 jsonl 的 user / assistant 文本，字面匹配，最多 20 条。
