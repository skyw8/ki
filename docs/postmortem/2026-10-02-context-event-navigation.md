# Context 导航：元数据身份与聊天位置不能等同

日期：2026-10-02

## 发现

用户报告 Context events 的「在对话中查看」像是只跳到末尾。真实会话
`01a0f814b9207af7a121818552c93603` 的远程 checkpoint 在只读 Chromium
detailed / compact 验证中均可最终定位到原 ID；没有把这次验证说成真实会话持续失败。
隔离 compact fixture 的延迟历史页则确定性复现了加载期间的 `following`，并发现两个
其它实际不可达路径。

## 根因

1. Context 卸载 Chat，切回时新的滚动所有者默认 following。即使 hook 初始设 reading，
   Chat 的首次 layout effect 仍无条件调用 `latest()`，重新贴尾。
2. 翻页代码把原始 entry ID 当作可渲染节点。model_change / request_header 是元数据，
   不存在相同 ID 的聊天行，HTTP 已拿到该 ID 不等于视口可 seek。
3. compact 的隐藏消息可能只在整轮摘要/index 中；往根部翻页不能取回同轮隐藏正文。
   自动展开本地 fold 又复用了手动展开回调，调用 `onReadIntent` 取消正在执行的 jump。

这是已有 following/reading/seeking 所有权问题在新入口上的再现，而非再加一次
`scrollTop` 或固定延迟可以解决。

## 修复原则

- 压缩事件保留 checkpoint 原 ID；非渲染的模型变更定位到同分支的相邻真实行，
  不以尾部兜底。
- 可达性只依据同步 store 提交后的实际 chat nodes。
- compact 隐藏消息先加载所属完整 turn；自动展开幂等，保留原导航意图。
- 等待目标的 Chat 从 reading 挂载，首次 layout 尊重同一个滚动所有者。

## 分类与图表的独立问题

同一真实快照的压缩前请求有 31 条 Agent messages；跨远程 checkpoint 后 Current
为 Agent 0 / 本地 Compaction 0 / Remote 1，是不可展开远程状态替换旧消息的结果，
不是 origin 丢失。现在明确提示并保留历史请求浏览。只有 metadata 的历史不估价为完整
正文。初次修复画独立斜纹报告柱但仍共用纵轴，导致分类缩成底部细线，视觉复核失当。
后续改为完整持久化内容派生轻量分类估算随 index 传递，主图只画彩色分类柱；
报告输入放在独立折线与刻度，不按 provider usage 比例补造分类。

## 回归

脱敏隔离 fixture 覆盖 metadata Agent、摘要 hydration、加密状态脱敏、压缩前后输入边界、
同分支模型变更、早期 checkpoint、隐藏 Agent 的精确定位及延迟历史页的读取意图。
断言目标 `data-anchor-key`、行几何在视口内、距尾部超过 500px；不以「已打开 Chat」
作为定位成功，也不使用固定等待。
