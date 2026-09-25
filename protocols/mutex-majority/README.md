# 多数投票互斥（完全去中心化）

课件：L06《Distributed Synchronization (2)》第 24–26 页；算法对比表第 47 页。

## 算法

- n 个节点都是协调者，各持有一票（`votedFor`）。需要 m = ⌊n/2⌋+1 票才能进入（5 节点时 m=3）。
- 申请：先投自己一票（若自己的票空闲），再向其余 n-1 个节点发送 `Vote.Request{from, try}`。
- 投票者**立即**回复：票空闲则 `grant=true` 并记下投给谁，否则 `grant=false`（DENY）。回复搭载在 RPC 返回里。
- 得到 m 票即进入临界区（之后迟到的 GRANT 也记下）；离开时向每个投票者发送 `Vote.Release` 退票（这就是课件「2m + m」中的 m 条通知）。
- 所有回复到齐仍不足 m 票：退还已得的票，随机退避 400–2000ms 后重试（`retries` 加一）。
- 2 秒没有回复的请求按 DENY 处理（投票者可能已宕机）；若迟到的回复是 GRANT，立即把这张票退回去。
- `try` 是请求者每次尝试的编号：迟到的 Release 只能退掉同一次尝试得到的票，不会误退下一次尝试的票。

安全性来自「任意两个多数派必然相交」：一个投票者不会同时把票投给两个人——前提是它**记得**自己投过票。

### 持久化选择

选票默认只在内存中，重启即遗忘（课件：forgetting vote on reboot）。`Application.Persist(enabled=true)` 后每次投票变化用 `sdk.Save` 落盘，重启读回。`incarnation`（重启次数）总是持久化，只用于观察节点是否已重启。

## 节点状态

| 字段 | 含义 |
| --- | --- |
| `role` | `released` / `wanted` / `critical` |
| `phase` | 等待时：`voting` 正在收票 / `backoff` 退避中 |
| `votedFor` | 本节点这一票当前投给了谁（空串 = 空闲） |
| `votes` / `needed` | 本次尝试已得到的票 / 需要的票数 m |
| `attempts` | 当前（或最近一次）申请用了几次尝试 |
| `retries` | 累计失败的尝试次数 |
| `entries` / `lastWaitMs` | 进入次数 / 最近一次等待时间 |
| `messagesSent` | 发出的 Request 与 Release 数量 |
| `persist` / `incarnation` | 是否持久化选票 / 第几次启动 |

## 应用输入

| 方法 | 作用 |
| --- | --- |
| `Application.Acquire(holdMs)` | 申请临界区，进入后停留 holdMs 毫秒（默认 2000） |
| `Application.Auto(enabled, intervalMs, holdMs)` | 随机负载 |
| `Application.Persist(enabled)` | 本节点的选票是否落盘 |

## 观察场景

`node scripts/scenarios.mjs run mutex-majority [场景ID]` 自动回放并检查。

### normal · 无竞争时一次成功（5 节点）

node-2 单独申请：自己一票加上最先返回的两个 `grant=true` 凑够 3 票即进入，之后到达的 GRANT 也记入 `votes`；离开时向 4 个投票者各发一个 Release。共 4 个 Request + 4 个 Release。

思考：向全部 n 个节点要票，而不是只向 m 个节点要票，有什么好处和代价？

### contention · 全体同时申请（5 节点，延迟 300ms）

1. 5 个节点同时申请，每个节点都先投了自己一票，其余请求全部 DENY：人人只有 1 票，没有人过半。
2. 大家退还选票、随机退避后重试；观察 `retries`、`phase` 和时空图中成片的 Request/Release。
3. 最终每个节点都进入一次，任意时刻至多一个 `critical`。

思考：课件问「饥饿是怎么发生的？」——如果退避时间是固定的会怎样？每轮消息数为什么没有上界？

### forget-vote · 投票者重启遗忘选票（5 节点）

1. node-4、node-5 宕机；node-1 仍然拿到 1、2、3 三票进入（多数派能容忍少数节点故障），并长时间持有。
2. node-4、node-5 恢复；投给 node-1 的 node-3 崩溃重启，`votedFor` 变回空串。
3. node-5 申请，得到 3、4、5 的票，也进入临界区：两个多数派 {1,2,3} 和 {3,4,5} 只在 node-3 相交，而 node-3 忘了。

思考：为什么只需要**一个**投票者失忆就足以破坏互斥？

### persist-vote · 持久化选票后重启仍然安全（5 节点）

同上，但所有节点先开启 `Persist`：node-3 重启后仍记得票属于 node-1，node-5 只拿到 2 票，退避重试直到 node-1 离开。

## 实现边界

- 请求者自己崩溃时，它持有的选票不会被收回（锁泄漏），不在本实验范围。
- 退避是均匀随机的，不做指数退避，也不保证有界等待。
