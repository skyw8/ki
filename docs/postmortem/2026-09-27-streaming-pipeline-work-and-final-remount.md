# 流式输出：双重调度、全文处理与定稿重挂载

## 现象与原因

修复 debounce 和回放缓存上限后，流式链路仍有随正文长度增长的工作：SSE encoder 每帧 marshal/unmarshal 全文再 diff，浏览器按 16ms 合并后又等待 Markdown 的 80ms 时钟，稳定段之前的归一化与边界扫描重复读全文。一次网络 read 若带大量事件，async generator 的微任务链还会阻止浏览器进入下一帧。

完成时临时消息 id 换成 entryId，使虚拟列表重挂载 Markdown；新解析队列先放占位，已读内容可能闪空。只检查最终字符串和最终 DOM 数量的测试抓不到这些停顿。

## 修复

typed 字段投影保留字符串、克隆可变参数，用大小下界跳过不必要的全文序列化。解码器按 4ms/128 事件让出真实 task；只有一个 rAF 显示时钟，100ms 兜底不被新 delta 推迟，终态是即时屏障。reducer 跳过未变化的请求记录，展示 key 与持久化 id 分开。

Markdown scanner 保留行/容器状态，只扫描新增内容；短活动尾部富文本、稳定段复用，长语义块保留完整源码后排队格式化，跨段引用使整篇回退。定稿与 tail 协调保留已挂载的根节点，避免展示身份随落盘变化。

SSE 单次写等待限为 30s，取消打断阻塞且退出时先取消再等待心跳 goroutine，防止结束路径等写锁；归还 writer 前清除 deadline，避免 HTTP 终止 chunk 被过期 deadline 截断。手动中止保留最后 partial，而不是用空消息覆盖已经可见的正文；最后 partial 的本地变量与已经发布给 reader 的 message_start 分开，避免保留机制改写共享事件。原生 provider body Read 使用可配置 idle timeout；心跳字节计活性，有 partial 的 idle 失败禁止自动重试。断线 GET 失败保留 partial/busy，并带游标退避重连。

## 回归证据

`BenchmarkMessageWireEncode` 的 256KiB 样本由约 3.06ms、543KiB/op 降至 0.041ms、5.5KiB/op（同一机器，含周期性初始快照）。持续 5ms 输入、240 帧 burst、50ms 输入的浏览器回放检查每个可见 revision 对应正文；桌面/400 turn 历史/手机尺寸 CPU 4× 均保留 440 个标记、完成前后同一根节点。具体性能表与复现命令见流畅度方案的实施记录。

新增回归覆盖 UTF-8/CRLF 分片、解码 task 让出、取消 pending Read、非法帧、持续输入 deadline、终态顺序、scanner 线性扫描、跨段引用、失败协调后的游标续传、慢 reader deadline、上游心跳与 idle partial。既有完整响应式、滚动、紧凑历史和协议测试继续运行。真实网络/模型空档与客户端积压分别计量，不能用最终输出正确推断过程流畅，也不能用 CPU 仿真代替真机验收。
