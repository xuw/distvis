# Lamport 分布式互斥

课件：L06《Distributed Synchronization (2)》第 27–31 页（基于第 4–7 页的全序组播）；第 47 页对比表、第 48 页总结（复制的数据结构）。

## 算法

- 每个节点维护 Lamport 时钟和一个**请求优先队列** `queue`，按时间戳 `L.id` 排序（先比 L，再比节点编号，因此是全序）。
- 申请：时钟加一得到请求时间戳，放进自己的队列，并向其余 n-1 个节点广播 `Lamport.Request{L, from}`。
- 收到请求：更新时钟、把请求放入队列，再**单播**一个带时间戳的 `Lamport.Reply` 给请求者（只有请求者需要知道）。
- 进入条件（两条同时满足）：
  1. 自己的请求在自己队列的队首；
  2. 从每个其他节点都收到过一条时间戳晚于自己请求的消息（`laterFrom` 收齐；Reply 必然满足）。
- 离开：从队列删除自己的请求，向所有节点广播 `Lamport.Release`；收到者删除该节点的请求，再检查自己能否进入。
- 每次进入 3(n-1) 条消息：n-1 Request + n-1 Reply + n-1 Release。

### 为什么需要 FIFO 通道

条件 2 的含义是「对方不可能再发来一条比我更早的请求」——只有当对方的消息按发送顺序到达时才成立。平台网络在每条有向链路上是 FIFO 的，但并发的 RPC 调用可能被发送端重排，因此本实现给每个对端一个发送队列和一个发送 goroutine，逐条同步发送（前一条返回后才发下一条）。发送失败（对方宕机）时每秒重试，队列中后面的消息都要等待；`unsent` 显示尚未送达的消息数。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `released` / `wanted` / `critical` |
| `clock` | Lamport 时钟 |
| `queue` | 请求优先队列，元素写作 `L.id`；所有节点的队列是同一个数据结构的副本 |
| `request` | 本节点当前请求的时间戳 |
| `laterFrom` / `laterNeeded` | 已收到「时间戳晚于本请求」消息的节点 / 需要的数量 n-1 |
| `entries` / `lastWaitMs` | 进入次数 / 最近一次等待时间 |
| `messagesSent` | 发出的 Request / Reply / Release 总数 |
| `unsent` | 发送队列中尚未送达的消息 |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Acquire(holdMs)` | 申请临界区，进入后停留 holdMs 毫秒（默认 2000） |
| `Application.Tick(events)` | 本地发生 events 个事件，时钟前进，用于构造特定时间戳 |
| `Application.Auto(enabled, intervalMs, holdMs)` | 随机负载 |

## 观察场景

`node scripts/scenarios.mjs run mutex-lamport [场景ID]` 自动回放并检查。

### concurrent · 两个节点同时请求（3 节点，延迟 300ms）

1. node-1、node-3 同时申请，时间戳 `1.1` 与 `1.3`。
2. 三个节点的 `queue` 都变成 `[1.1, 1.3]`。
3. node-1 在队首且收齐回复，进入；node-3 也收齐了回复，但不在队首，继续等待。
4. node-1 离开时广播 Release，node-3 进入。共 4 Request + 4 Reply + 4 Release = 2 × 3(n-1)。

思考：与 Ricart & Agrawala 相比，多出来的 n-1 条消息是哪一种？R&A 用什么办法省掉了它？

### replicated-queue · 复制的优先队列（5 节点，延迟 400ms）

1. node-5 先 `Tick(30)`，时钟很快；node-2 以 `1.2` 持锁。
2. node-5 申请（`33.5`），紧接着 node-3 在收到它之前申请（`4.3`）。
3. 5 个节点的队列完全一致：`[1.2, 4.3, 33.5]`。node-2 离开后 node-3 先进入，尽管在真实时间上 node-5 先申请。

思考：课件总结说 Lamport 算法展示了「如何维护数据结构的一致副本」。这里的一致性由什么保证？如果某条链路不是 FIFO 会怎样？

### crash-blocks · 任一节点崩溃都会阻塞（3 节点）

1. node-2 崩溃（它从不申请锁）。
2. node-1 申请：它在自己的队首，也收到了 node-3 的回复，但 `laterFrom` 缺 node-2，一直等待；发往 node-2 的请求卡在发送队列（`unsent ≥ 1`）。
3. node-2 恢复后请求被重新送达并得到回复，node-1 进入。

思考：课件问「节点故障怎么办？」——能否在超时后把 node-2 移出成员名单？这需要什么额外的协议？

## 实现边界

- 节点状态只在内存中；崩溃恢复后队列与时钟从空开始，只适合演示「阻塞直到恢复」。持锁者或排队者崩溃会让其他节点永久阻塞。
- 发送重试可能造成重复消息；Request 按节点去重（每个节点至多一个未完成请求），重复的 Reply/Release 无害。
