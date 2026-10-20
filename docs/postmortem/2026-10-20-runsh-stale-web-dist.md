# 运行中的服务嵌入了被覆盖的 web/dist

## 现象

2026-10-20 移动端键盘避让修复分三轮交付，用户两次反馈"没修好"。排查发现运行中的
`ki serve` 一直嵌入第一轮之前的旧 UI：新构建的 go 二进制复用了磁盘上被"干净树"构建
覆盖的 `web/dist`。

## 经过

1. 为验证一个 e2e flake 是否预存，执行 `git stash -u` 暂时撤掉全部前端改动，并用
   `bunx playwright test` 跑干净树。其 global-setup 的 `ensureWebDist`（按 mtime 判断
   过期）在此时用**干净源码重建了 dist**。
2. `git stash pop` 恢复源码（含修复）。
3. `scripts/run.sh` 重新构建：它对 `web/src` 等输入做内容哈希并记录在
   `web/node_modules/.cache/ki/web-dist-hash`。源码恢复后哈希与记录值一致，于是跳过
   前端构建——但磁盘上的 dist 已经是干净树产物。go 二进制嵌入了它。
4. 用户拿到的"新构建"不含任何修复；后续测量一度混淆（不同时间窗口内 dist 来源不同）。

## 根因

dist 的正确性由两条互不知晓的路径维护：run.sh 的**输入哈希**（假设 dist 与记录的
输入哈希对应）和 playwright 的 **mtime 过期重建**（不知道 run.sh 的哈希记录）。
stash 窗口让"输入哈希匹配"与"dist 实际内容"脱钩，run.sh 的跳过逻辑无法察觉。

## 修复

`scripts/run.sh` 构建时同时记录 `dist/index.html` 的内容哈希；跳过前端构建的条件
改为输入哈希与产物哈希都匹配。dist 被带外重建（内容不符）时强制重新构建。
`--force-web` 仍可手动强制。

## 教训

- 验证"服务端实际跑的是哪一版"要直接 grep 服务端返回的 CSS/JS 内容（哈希文件名 +
  特征字符串），不要从构建日志推断。
- 在 stash 窗口内跑任何会重建 dist 的命令（playwright、e2e-parallel、run.sh）都会
  污染 dist；恢复源码后必须显式 `cd web && bun run build` 或 `run.sh --force-web`。
